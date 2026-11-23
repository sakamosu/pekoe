package backend

// The bundled backend: `pekoe voice-backend-say`, macOS's say behind the
// backend protocol (docs/voice.md 10.6). pekoed runs it like any other
// backend, so it passes the same contract tests.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// SayBin is macOS's say. Never looked up in PATH.
var SayBin = "/usr/bin/say"

// SayOptions are config.toml's [voice.say]: the voice for each language
// ("" is the default: Kyoko or the first ja_JP voice for ja, the system's
// voice for en).
type SayOptions struct {
	JA, EN string
}

// SayVoice is one voice `say -v '?'` lists.
type SayVoice struct {
	Name, Locale string
}

var sayVoiceRe = regexp.MustCompile(`^(.+?)\s+([a-z]{2,3}_[A-Za-z0-9]+)\s+#`)

// ParseSayVoices reads the output of `say -v '?'`.
func ParseSayVoices(out string) []SayVoice {
	var vs []SayVoice
	for _, l := range strings.Split(out, "\n") {
		if m := sayVoiceRe.FindStringSubmatch(l); m != nil {
			vs = append(vs, SayVoice{Name: strings.TrimSpace(m[1]), Locale: m[2]})
		}
	}
	return vs
}

// sayJA picks the Japanese voice: the configured one, else Kyoko, else the
// first ja_JP voice ("" when there is none).
func sayJA(o SayOptions, vs []SayVoice) string {
	if o.JA != "" {
		return o.JA
	}
	first := ""
	for _, v := range vs {
		if !strings.HasPrefix(v.Locale, "ja_") {
			continue
		}
		if v.Name == "Kyoko" {
			return v.Name
		}
		if first == "" {
			first = v.Name
		}
	}
	return first
}

func sayVoices() ([]SayVoice, error) {
	out, err := exec.Command(SayBin, "-v", "?").Output()
	if err != nil {
		return nil, fmt.Errorf("%s -v '?': %v", SayBin, err)
	}
	return ParseSayVoices(string(out)), nil
}

// SayMain runs the bundled backend with the protocol's arguments (after
// "voice-backend-say") and returns the exit code.
func SayMain(args []string, o SayOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintln(stderr, firstLine(fmt.Sprintf(format, a...)))
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pekoe voice-backend-say version|check|voices|synth --voice NAME --lang ja|en --out PATH")
		return 2
	}
	ready := func() error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("say is macOS only")
		}
		return checkExecutable(SayBin)
	}
	switch args[0] {
	case "version":
		if len(args) != 1 {
			return fail("version takes no arguments")
		}
		fmt.Fprintln(stdout, Protocol)
		return 0
	case "check":
		if err := ready(); err != nil {
			return fail("%v", err)
		}
		if _, err := sayVoices(); err != nil {
			return fail("%v", err)
		}
		return 0
	case "voices":
		if err := ready(); err != nil {
			return fail("%v", err)
		}
		vs, err := sayVoices()
		if err != nil {
			return fail("%v", err)
		}
		var out []Voice
		if ja := sayJA(o, vs); ja != "" {
			out = append(out, Voice{"tapi", "ja", ja}, Voice{"reader", "ja", ja})
		}
		en := o.EN
		if en == "" {
			en = "(system voice)"
		}
		out = append(out, Voice{"tapi", "en", en}, Voice{"reader", "en", en})
		json.NewEncoder(stdout).Encode(out)
		return 0
	case "synth":
		return saySynth(args[1:], o, stdin, stderr, fail, ready)
	}
	return fail("unknown command %q", args[0])
}

func saySynth(args []string, o SayOptions, stdin io.Reader, stderr io.Writer, fail func(string, ...any) int, ready func() error) int {
	f := map[string]string{}
	for i := 0; i < len(args); i++ {
		k, ok := strings.CutPrefix(args[i], "--")
		if !ok || i+1 >= len(args) || (k != "voice" && k != "lang" && k != "out") {
			return fail("synth: unexpected argument %q", args[i])
		}
		f[k] = args[i+1]
		i++
	}
	if f["out"] == "" {
		return fail("synth: --out is required")
	}
	if err := ready(); err != nil {
		return fail("%v", err)
	}
	// the voice name (tapi / reader / one pekoe adds later) does not matter:
	// both are the language's voice
	argv := []string{}
	switch f["lang"] {
	case "ja":
		vs, err := sayVoices()
		if err != nil {
			return fail("%v", err)
		}
		v := sayJA(o, vs)
		if v == "" {
			return fail("no Japanese voice: add one in System Settings > Accessibility > Spoken Content")
		}
		argv = append(argv, "-v", v)
	case "en":
		if o.EN != "" {
			argv = append(argv, "-v", o.EN)
		}
	default:
		return fail("unsupported language %q", f["lang"])
	}
	text, err := io.ReadAll(io.LimitReader(stdin, MaxText+1))
	if err != nil {
		return fail("reading the text: %v", err)
	}
	if len(text) > MaxText {
		return fail("the text is longer than %d bytes", MaxText)
	}
	if strings.TrimSpace(string(text)) == "" {
		return fail("no text")
	}
	argv = append(argv, "--file-format=WAVE", "--data-format=LEI16@24000", "-o", f["out"], "-f", "-")
	cmd := exec.Command(SayBin, argv...)
	cmd.Stdin = strings.NewReader(string(text))
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if l := firstLine(errb.String()); l != "" {
			return fail("say: %s", l)
		}
		return fail("say: %v", err)
	}
	if fi, err := os.Stat(f["out"]); err != nil || fi.Size() == 0 {
		return fail("say wrote nothing")
	}
	return 0
}
