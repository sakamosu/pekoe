package backend

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sayList = `Albert              en_US    # Hello! My name is Albert.
Bad News            en_US    # Hello! My name is Bad News.
Eddy (日本語（日本）)    ja_JP    # こんにちは、私の名前はEddyです。
Kyoko               ja_JP    # こんにちは、私の名前はKyokoです。
Otoya (Enhanced)    ja_JP    # こんにちは、私の名前はOtoyaです。
`

func TestParseSayVoices(t *testing.T) {
	vs := ParseSayVoices(sayList)
	if len(vs) != 5 || vs[1] != (SayVoice{"Bad News", "en_US"}) || vs[2] != (SayVoice{"Eddy (日本語（日本）)", "ja_JP"}) {
		t.Fatalf("%q", vs)
	}
}

// Japanese: the configured voice, else Kyoko, else the first ja_JP one.
func TestSayJA(t *testing.T) {
	vs := ParseSayVoices(sayList)
	if v := sayJA(SayOptions{}, vs); v != "Kyoko" {
		t.Errorf("%q", v)
	}
	if v := sayJA(SayOptions{JA: "Otoya (Enhanced)"}, vs); v != "Otoya (Enhanced)" {
		t.Errorf("%q", v)
	}
	if v := sayJA(SayOptions{}, ParseSayVoices(strings.ReplaceAll(sayList, "Kyoko  ", "Kyok0  "))); v != "Eddy (日本語（日本）)" {
		t.Errorf("%q", v)
	}
	if v := sayJA(SayOptions{}, ParseSayVoices("Albert   en_US  # hi\n")); v != "" {
		t.Errorf("%q", v)
	}
}

func runSay(args []string, stdin string) (int, string, string) {
	var out, errb bytes.Buffer
	code := SayMain(args, SayOptions{}, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

// What needs no say: version, unknown commands and bad arguments.
func TestSayMainArgs(t *testing.T) {
	if code, out, _ := runSay([]string{"version"}, ""); code != 0 || out != "1\n" {
		t.Errorf("version: %d %q", code, out)
	}
	for _, args := range [][]string{nil, {"frob"}, {"version", "x"}, {"synth"}, {"synth", "--voice"}, {"synth", "--pitch", "3", "--out", "/x"}} {
		code, _, errs := runSay(args, "")
		if code == 0 || errs == "" || (args != nil && strings.Contains(strings.TrimSpace(errs), "\n")) {
			t.Errorf("%q: exit %d, stderr %q (want non-zero and one line)", args, code, errs)
		}
	}
}

// Without macOS's say, check fails with a reason and synth refuses.
func TestSayMainWithoutSay(t *testing.T) {
	old := SayBin
	SayBin = filepath.Join(t.TempDir(), "say")
	t.Cleanup(func() { SayBin = old })
	code, _, errs := runSay([]string{"check"}, "")
	if code == 0 || errs == "" {
		t.Fatalf("check: %d %q", code, errs)
	}
	if runtime.GOOS != "darwin" && !strings.Contains(errs, "macOS only") {
		t.Errorf("%q", errs)
	}
	if code, _, _ := runSay([]string{"synth", "--voice", "tapi", "--lang", "en", "--out", filepath.Join(t.TempDir(), "o.wav")}, "hi"); code == 0 {
		t.Error("synth without say")
	}
}

// With a stand-in say (a script recording its arguments), the bundled
// backend passes the voice, the format and the output, and the text on
// stdin.
func TestSayMainArgvWithStandIn(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the bundled say only runs on macOS (" + runtime.GOOS + ")")
	}
	dir := t.TempDir()
	rec := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nif [ \"$1\" = -v ] && [ \"$2\" = '?' ]; then printf '%s' '" + sayList + "'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > " + rec + "\ncat > " + rec + ".stdin\n" +
		"while [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then printf 'RIFF....WAVE' > \"$2\"; fi; shift; done\n"
	stand := filepath.Join(dir, "say")
	os.WriteFile(stand, []byte(script), 0o755)
	old := SayBin
	SayBin = stand
	t.Cleanup(func() { SayBin = old })

	out := filepath.Join(dir, "o.wav")
	if code, _, errs := runSay([]string{"synth", "--voice", "tapi", "--lang", "ja", "--out", out}, "こんにちは"); code != 0 {
		t.Fatalf("synth: %s", errs)
	}
	argv, _ := os.ReadFile(rec)
	if want := "-v\nKyoko\n--file-format=WAVE\n--data-format=LEI16@24000\n-o\n" + out + "\n-f\n-\n"; string(argv) != want {
		t.Fatalf("argv\n%s\nwant\n%s", argv, want)
	}
	if in, _ := os.ReadFile(rec + ".stdin"); string(in) != "こんにちは" {
		t.Fatalf("stdin %q", in)
	}
	// en without a configured voice: no -v (the system's voice)
	runSay([]string{"synth", "--voice", "reader", "--lang", "en", "--out", filepath.Join(dir, "e.wav")}, "hi")
	if argv, _ := os.ReadFile(rec); strings.HasPrefix(string(argv), "-v") {
		t.Fatalf("en got a voice: %q", argv)
	}
	if code, _, _ := runSay([]string{"synth", "--voice", "tapi", "--lang", "fr", "--out", filepath.Join(dir, "f.wav")}, "salut"); code == 0 {
		t.Fatal("fr accepted")
	}
	if code, _, _ := runSay([]string{"synth", "--voice", "tapi", "--lang", "en", "--out", filepath.Join(dir, "l.wav")}, strings.Repeat("a", MaxText+1)); code == 0 {
		t.Fatal("too long a text accepted")
	}
	code, vout, _ := runSay([]string{"voices"}, "")
	var vs []Voice
	if code != 0 || json.Unmarshal([]byte(vout), &vs) != nil || len(vs) != 4 || vs[0] != (Voice{"tapi", "ja", "Kyoko"}) {
		t.Fatalf("voices %d %q", code, vout)
	}
	if code, _, errs := runSay([]string{"check"}, ""); code != 0 {
		t.Fatalf("check %q", errs)
	}
	// no Japanese voice: ja synth fails with how to add one; voices lists en only
	os.WriteFile(stand, []byte(strings.Replace(script, sayList, "Albert   en_US  # hi\n", 1)), 0o755)
	if code, _, errs := runSay([]string{"synth", "--voice", "tapi", "--lang", "ja", "--out", filepath.Join(dir, "j.wav")}, "x"); code == 0 || !strings.Contains(errs, "no Japanese voice") {
		t.Fatalf("%d %q", code, errs)
	}
	_, vout, _ = runSay([]string{"voices"}, "")
	if strings.Contains(vout, `"ja"`) {
		t.Fatalf("voices %q", vout)
	}
}
