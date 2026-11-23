package main

// pekoe voice: tapi says state changes aloud (docs/voice.md 12). on / off
// rewrite the one line [voice] enable in config.toml themselves, never
// through pekoed (D4: like pekoe config edit, ~/.config/pekoe is out of an
// agent's sandbox on purpose). The rest asks pekoed over voice.*, which
// needs the human token.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/client"
	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/paths"
	"pekoe/internal/voice"
	"pekoe/internal/voice/backend"
)

func cmdVoice(args []string) error {
	sub, name := "", "voice"
	if len(args) > 0 && !isFlag(args[0]) {
		sub, args = args[0], args[1:]
		name = "voice " + sub
	}
	if _, ok := specs[name]; !ok {
		name = "voice" // unknown subcommand: --help still answers
	}
	if sub == "log" {
		// -n is the one short flag (docs/voice.md 12); the parser takes --n
		for i, a := range args {
			if a == "--" {
				break
			}
			if a == "-n" || strings.HasPrefix(a, "-n=") {
				args[i] = "-" + a
			}
		}
	}
	f, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	use := specs[name].use
	noArgs := func() error {
		if len(pos) > 0 {
			return &usageError{use: use, msg: "unexpected argument " + pos[0]}
		}
		return nil
	}
	switch sub {
	case "", "status":
		if err := noArgs(); err != nil {
			return err
		}
		return voiceStatus(os.Stdout)
	case "on", "off":
		if err := noArgs(); err != nil {
			return err
		}
		return voiceOnOff(paths.GlobalConfig(), sub == "on", os.Getenv("PEKOE_SESSION"), os.Stdout)
	case "mute":
		if err := noArgs(); err != nil {
			return err
		}
		p := map[string]any{}
		if f["for"] != "" {
			d, err := time.ParseDuration(f["for"])
			if err != nil || d <= 0 {
				return &usageError{use: use, msg: fmt.Sprintf("--for %q is not a positive duration like 30m", f["for"])}
			}
			p["for"] = f["for"]
		}
		return voiceMute(p, os.Stdout)
	case "unmute":
		if err := noArgs(); err != nil {
			return err
		}
		return voiceMute(map[string]any{"off": true}, os.Stdout)
	case "mode":
		if len(pos) != 2 {
			return &usageError{use: use, msg: "a session and a mode (read, narrate, mute or auto) are required"}
		}
		if _, ok := voice.ParseMode(pos[1]); !ok {
			return &usageError{use: use, msg: fmt.Sprintf("%q is not read, narrate, mute or auto", pos[1])}
		}
		cl := connect()
		defer cl.Close()
		return voiceMode(cl, pos[0], voice.Mode(pos[1]), os.Stdout)
	case "stop":
		if err := noArgs(); err != nil {
			return err
		}
		cl := connect()
		defer cl.Close()
		if err := cl.Call("voice.stop", nil, nil); err != nil {
			return err
		}
		if !jsonOut {
			fmt.Println("stopped what was being said (the queue is kept)")
		}
		return nil
	case "test":
		if len(pos) == 0 {
			return &usageError{use: use, msg: "the sentence to say is required"}
		}
		text := strings.Join(pos, " ")
		for k, ok := range map[string][]string{"voice": {voice.VoiceTapi, voice.VoiceReader}, "lang": {voice.LangJa, voice.LangEn}} {
			if v := f[k]; v != "" && v != ok[0] && v != ok[1] {
				return &usageError{use: use, msg: fmt.Sprintf("--%s %q is not %s or %s", k, v, ok[0], ok[1])}
			}
		}
		cl := connect()
		defer cl.Close()
		return voiceTest(cl, text, f["voice"], f["lang"], 30*time.Second, os.Stdout)
	case "backends":
		if err := noArgs(); err != nil {
			return err
		}
		cl := connect()
		defer cl.Close()
		var v daemon.VoiceBackends
		if err := cl.Call("voice.backends", nil, &v); err != nil {
			return err
		}
		if jsonOut {
			printJSON(v)
			return nil
		}
		printVoiceBackends(os.Stdout, v)
		return nil
	case "log":
		if err := noArgs(); err != nil {
			return err
		}
		n := 50
		if f["n"] != "" {
			v, err := strconv.Atoi(f["n"])
			if err != nil || v < 1 || v > 1000 {
				return &usageError{use: use, msg: fmt.Sprintf("-n %q is not a number from 1 to 1000", f["n"])}
			}
			n = v
		}
		cl := connect()
		defer cl.Close()
		var es []voice.LogEntry
		if err := cl.Call("voice.log", map[string]any{"n": n}, &es); err != nil {
			return err
		}
		if jsonOut {
			if es == nil {
				es = []voice.LogEntry{}
			}
			printJSON(es)
			return nil
		}
		printVoiceLog(os.Stdout, es)
		return nil
	}
	return &usageError{use: specs["voice"].use, msg: "unknown subcommand " + sub}
}

// ---- status ----

func voiceStatus(out io.Writer) error {
	cl := connect()
	defer cl.Close()
	var st daemon.VoiceStatus
	if err := cl.Call("voice.status", nil, &st); err != nil {
		return err
	}
	if jsonOut {
		printJSON(st)
		return nil
	}
	printVoiceStatus(out, st, time.Now())
	return nil
}

func printVoiceStatus(out io.Writer, st daemon.VoiceStatus, now time.Time) {
	switch {
	case !st.Supported:
		fmt.Fprintln(out, "voice: unsupported (pekoe voice works on macOS only)")
		return
	case !st.Enabled:
		fmt.Fprintln(out, "voice: off ([voice] enable = false) · pekoe voice on")
	default:
		line := "voice: on · backend " + st.Backend
		switch {
		case st.Using == "":
			line += " · nothing can speak"
		case st.Using != st.Backend:
			line += " · using the fallback " + st.Using
		default:
			line += " · ok"
		}
		fmt.Fprintln(out, line)
	}
	if st.BackendError != "" {
		fmt.Fprintln(out, "  backend:", st.BackendError)
	}
	if st.Error != "" {
		fmt.Fprintln(out, "  error:", st.Error)
	}
	if p := pausedLine(st, now); p != "" {
		fmt.Fprintln(out, "  "+p)
	}
	fmt.Fprintln(out, "muted:", mutedText(st.MutedUntil, now))
	focus := st.Focus
	if focus == "" {
		focus = "none"
	}
	fmt.Fprintln(out, "focus:", focus)
	if len(st.Modes) > 0 {
		slugs := make([]string, 0, len(st.Modes))
		for s := range st.Modes {
			slugs = append(slugs, s)
		}
		slices.Sort(slugs)
		var parts []string
		for _, s := range slugs {
			parts = append(parts, s+" "+string(st.Modes[s]))
		}
		fmt.Fprintln(out, "modes:", strings.Join(parts, " · "))
	}
	if !st.Enabled {
		return
	}
	line := fmt.Sprintf("queue: %d", st.Queue)
	if sp := st.Speaking; sp != nil {
		who := sp.Slug
		if who == "" {
			who = "-"
		}
		line += fmt.Sprintf(" · speaking: %s %s %q", who, sp.Kind, sp.Text)
	}
	fmt.Fprintln(out, line)
}

// pausedLine: synthesis is paused after repeated failures (Q11): for how
// much longer, and why (the last failure is in Error). "" when not.
func pausedLine(st daemon.VoiceStatus, now time.Time) string {
	if st.PausedUntil == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, st.PausedUntil)
	if err != nil {
		return "synthesis paused until " + st.PausedUntil
	}
	return fmt.Sprintf("synthesis paused for another %s (until %s) after repeated failures; nothing is said till then", voiceDur(t.Sub(now)), t.Local().Format("15:04:05"))
}

func mutedText(until string, now time.Time) string {
	switch until {
	case "":
		return "no"
	case "forever":
		return "yes, until pekoe voice unmute"
	}
	t, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return "until " + until
	}
	return fmt.Sprintf("until %s (%s left)", t.Local().Format("15:04"), voiceDur(t.Sub(now)))
}

// voiceDur: 45s, 28m, 1h30m.
func voiceDur(d time.Duration) string {
	if d = max(d.Round(time.Second), time.Second); d < time.Minute {
		return d.String()
	}
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// ---- mute ----

func voiceMute(p map[string]any, out io.Writer) error {
	cl := connect()
	defer cl.Close()
	var st daemon.VoiceStatus
	if err := cl.Call("voice.mute", p, &st); err != nil {
		return err
	}
	if jsonOut {
		printJSON(st)
		return nil
	}
	if st.MutedUntil == "" {
		fmt.Fprintln(out, "voice unmuted")
	} else {
		fmt.Fprintln(out, "voice muted:", mutedText(st.MutedUntil, time.Now()))
	}
	if !st.Enabled {
		fmt.Fprintln(out, "(voice is off: pekoe voice on)")
	}
	return nil
}

// ---- mode ----

func voiceMode(cl voiceCaller, slug string, m voice.Mode, out io.Writer) error {
	var st daemon.VoiceStatus
	if err := cl.Call("voice.mode", map[string]any{"slug": slug, "mode": string(m)}, &st); err != nil {
		return err
	}
	if jsonOut {
		printJSON(st)
		return nil
	}
	if m == voice.ModeAuto {
		fmt.Fprintf(out, "%s: no mode of its own (the focus / background default)\n", slug)
	} else {
		fmt.Fprintf(out, "%s: %s\n", slug, m)
	}
	switch {
	case !st.Enabled:
		fmt.Fprintln(out, "(voice is off: pekoe voice on)")
	case st.MutedUntil != "":
		fmt.Fprintln(out, "(voice is muted: pekoe voice unmute)")
	}
	return nil
}

// ---- test ----

// voiceCaller is the part of client.Client pekoe voice test uses.
type voiceCaller interface {
	Call(method string, params, out any) error
}

var _ voiceCaller = (*client.Client)(nil)

// voiceTest queues the sentence (voice.say), then follows voice.log until
// it was said or failed, up to wait: when synthesis is paused (Q11) or the
// backend fails, the reason is shown, not only "queued".
func voiceTest(cl voiceCaller, text, v, lang string, wait time.Duration, out io.Writer) error {
	start := time.Now()
	var st daemon.VoiceStatus
	if err := cl.Call("voice.say", map[string]any{"text": text, "voice": v, "lang": lang}, &st); err != nil {
		return err
	}
	if st.PausedUntil != "" {
		if jsonOut {
			printJSON(st)
		}
		why := pausedLine(st, time.Now())
		if st.Error != "" {
			why += "\n  " + st.Error
		}
		return errors.New("not said: " + why)
	}
	clean := backend.CleanText(text)
	deadline := start.Add(wait)
	for {
		var es []voice.LogEntry
		if err := cl.Call("voice.log", map[string]any{"n": 50}, &es); err != nil {
			return err
		}
		for _, e := range es {
			if e.Kind != voice.KindTest || e.Text != clean || e.Time.Before(start.Add(-time.Second)) {
				continue
			}
			if jsonOut {
				printJSON(e)
			}
			switch e.Result {
			case voice.ResultSpoken, voice.ResultMerged:
				if !jsonOut {
					fmt.Fprintf(out, "said (%s, %dms from the request to the sound)\n", e.Backend, e.LatencyMS)
				}
				return nil
			default:
				why := string(e.Result)
				if e.Reason != "" {
					why += ": " + e.Reason
				}
				var now daemon.VoiceStatus
				if cl.Call("voice.status", nil, &now) == nil {
					if p := pausedLine(now, time.Now()); p != "" {
						why += "\n  " + p
					}
				}
				return errors.New("not said: " + why)
			}
		}
		if time.Now().After(deadline) {
			if !jsonOut {
				fmt.Fprintln(out, "queued, not said yet; pekoe voice log shows how it went")
			} else {
				printJSON(st)
			}
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ---- backends ----

func printVoiceBackends(out io.Writer, v daemon.VoiceBackends) {
	fmt.Fprintln(out, "backends:")
	for _, b := range v.Backends {
		var marks []string
		if b.Using {
			marks = append(marks, "using")
		}
		if b.Fallback {
			marks = append(marks, "fallback")
		}
		mark := " "
		if b.Using {
			mark = "*"
		}
		line := fmt.Sprintf("%s %-10s %s", mark, b.Name, b.Path)
		if len(marks) > 0 {
			line += "  (" + strings.Join(marks, ", ") + ")"
		}
		fmt.Fprintln(out, line)
		detail := "check: " + b.Check
		if b.Version != "" {
			detail = "version " + b.Version + " · " + detail
		}
		fmt.Fprintln(out, "    "+detail)
		if len(b.Voices) > 0 {
			var vs []string
			for _, x := range b.Voices {
				vs = append(vs, x.Name+" ("+x.Lang+")")
			}
			fmt.Fprintln(out, "    voices: "+strings.Join(vs, ", "))
		}
		for _, w := range b.Warnings {
			fmt.Fprintln(out, "    warning: "+w)
		}
	}
	if v.Note != "" {
		fmt.Fprintln(out, "note:", v.Note)
	}
	fmt.Fprintln(out, "A backend is given the text to say: say stays on this Mac, a backend that calls an online service sends the text out.")
	if len(v.Candidates) == 0 {
		fmt.Fprintln(out, "candidates: no pekoe-voice-* in pekoed's PATH")
		return
	}
	fmt.Fprintln(out, "candidates in pekoed's PATH (not run; to use one, add its line to config.toml):")
	for _, c := range v.Candidates {
		fmt.Fprintf(out, "  %-10s %s\n", c.Name, c.Path)
	}
	fmt.Fprintln(out, "  [voice.backends]")
	for _, c := range v.Candidates {
		fmt.Fprintln(out, "  "+c.Line)
	}
}

// ---- log ----

func printVoiceLog(out io.Writer, es []voice.LogEntry) {
	if len(es) == 0 {
		fmt.Fprintln(out, "nothing said yet")
		return
	}
	for _, e := range es {
		slug := e.Slug
		if slug == "" {
			slug = "-"
		}
		line := fmt.Sprintf("%s  %-16s %-8s %-11s", e.Time.Local().Format("01-02 15:04:05"), slug, e.Kind, e.Result)
		if e.Result == voice.ResultSpoken || e.Result == voice.ResultMerged {
			line += fmt.Sprintf(" %5dms", e.LatencyMS)
		}
		if e.Backend != "" {
			line += " " + e.Backend
		}
		line += " " + strconv.Quote(e.Text)
		fmt.Fprintln(out, line)
		if e.Reason != "" {
			fmt.Fprintln(out, "    "+e.Reason)
		}
	}
}

// ---- on / off: the one line [voice] enable in config.toml (D4) ----

// voiceOnOff sets [voice] enable in config.toml to on. Refused in an
// agent's window ($PEKOE_SESSION): a guard against a slip, not the
// protection, which is that neither sandbox can write ~/.config/pekoe.
func voiceOnOff(path string, on bool, session string, out io.Writer) error {
	word := map[bool]string{true: "on", false: "off"}[on]
	if session != "" {
		return fmt.Errorf("pekoe voice %s is for a person: run it from your own terminal, not in session %s's window", word, session)
	}
	target := path
	if t, err := filepath.EvalSymlinks(path); err == nil {
		target = t // a config.toml linked from dotfiles stays a link
	}
	orig, err := os.ReadFile(target)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	g, err := config.ParseGlobal(orig)
	if err != nil {
		return fmt.Errorf("%s does not read (%v): fix it first (pekoe config edit); nothing was changed", path, err)
	}
	if g.Voice.Enable == on {
		fmt.Fprintf(out, "voice is already %s ([voice] enable = %t in %s)\n", word, on, path)
		return nil
	}
	b := setVoiceEnable(orig, on)
	if ng, err := config.ParseGlobal(b); err != nil || ng.Voice.Enable != on {
		why := "it would still read enable = " + strconv.FormatBool(!on)
		if err != nil {
			why = err.Error()
		}
		return fmt.Errorf("cannot set [voice] enable in %s (%s): set it with pekoe config edit; nothing was changed", path, why)
	}
	if err := replaceConfig(target, b, exists); err != nil {
		return err
	}
	// read back what is there now: a file that does not read (or says
	// otherwise) goes back as it was
	now, err := os.ReadFile(target)
	if err == nil {
		var ng *config.Global
		if ng, err = config.ParseGlobal(now); err == nil && ng.Voice.Enable != on {
			err = errors.New("it reads enable = " + strconv.FormatBool(!on))
		}
	}
	if err != nil {
		if !exists {
			os.Remove(target)
		} else if rerr := writeReplace(target, orig, true); rerr != nil {
			return fmt.Errorf("%s does not read after the change (%v), and putting it back failed: %w", path, err, rerr)
		}
		return fmt.Errorf("%s did not read after the change (%v): put back as it was", path, err)
	}
	fmt.Fprintf(out, "voice %s: [voice] enable = %t in %s\n", word, on, path)
	if on && runtime.GOOS != "darwin" {
		fmt.Fprintln(out, "pekoe voice works on macOS only: nothing is said here")
	} else {
		fmt.Fprintln(out, "a running pekoed takes it up within a second")
	}
	return nil
}

var (
	reTable  = regexp.MustCompile(`^\s*\[`)
	reVoice  = regexp.MustCompile(`^\s*\[\s*(voice|"voice"|'voice')\s*\]\s*(#.*)?$`)
	reEnable = regexp.MustCompile(`^(\s*(?:enable|"enable"|'enable')\s*=\s*)(true|false)(\s*(?:#.*)?)$`)
)

// setVoiceEnable rewrites the enable line of the [voice] table, keeping
// every other line and comment as it is; without the line it goes right
// under [voice], and without the table both go at the end. The caller
// checks the result reads (a [voice] written some other way — an inline
// table, a dotted key — does not take a line edit).
func setVoiceEnable(b []byte, on bool) []byte {
	val := strconv.FormatBool(on)
	text := string(b)
	lines := strings.SplitAfter(text, "\n")
	in, header := false, -1
	for i, l := range lines {
		body := strings.TrimRight(l, "\r\n")
		if reTable.MatchString(body) {
			in = reVoice.MatchString(body)
			if in {
				header = i
			}
			continue
		}
		if in {
			if m := reEnable.FindStringSubmatch(body); m != nil {
				lines[i] = m[1] + val + m[3] + l[len(body):]
				return []byte(strings.Join(lines, ""))
			}
		}
	}
	if header >= 0 {
		nl := "\n"
		if strings.HasSuffix(lines[header], "\r\n") {
			nl = "\r\n"
		} else if !strings.HasSuffix(lines[header], "\n") {
			lines[header] += "\n"
		}
		lines[header] += "enable = " + val + nl
		return []byte(strings.Join(lines, ""))
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" {
		text += "\n"
	}
	return []byte(text + "[voice]\nenable = " + val + "\n")
}

// replaceConfig writes the changed config.toml (a test swaps it).
var replaceConfig = writeReplace

// writeReplace puts b in target with one rename from a file next to it
// (pekoed and the TUI, reading it for changes, never see half a file),
// keeping the file's mode.
func writeReplace(target string, b []byte, exists bool) error {
	if !exists {
		if err := paths.Ensure(filepath.Dir(target)); err != nil {
			return err
		}
	}
	perm := paths.FilePerm
	if fi, err := os.Stat(target); err == nil {
		perm = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".config-*.toml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, perm)
	}
	if err == nil {
		err = os.Rename(name, target)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}
