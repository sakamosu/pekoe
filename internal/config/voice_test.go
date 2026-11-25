package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// [voice] unset is off, with the bundled say as backend and fallback.
func TestVoiceDefaults(t *testing.T) {
	g, err := ParseGlobal(nil)
	if err != nil {
		t.Fatal(err)
	}
	v := g.Voice
	if v.Enable || v.BackendName() != "say" || v.FallbackName() != "say" || v.LangSetting() != "auto" ||
		v.Focus() != "read" || v.Background() != "narrate" || v.MaxSentences() != 5 || v.MaxTurnSentences() != 10 {
		t.Fatalf("defaults: %+v", v)
	}
	g, _ = ParseGlobal([]byte("[voice]\nfallback = \"\"\nlang = \"fr\"\nfocus_mode = \"loud\"\n[voice.read]\nmax_sentences = 99\nmax_turn_sentences = 101\n"))
	if g.Voice.FallbackName() != "" || g.Voice.LangSetting() != "auto" || g.Voice.Focus() != "read" || g.Voice.MaxSentences() != 5 || g.Voice.MaxTurnSentences() != 10 {
		t.Fatalf("fallback \"\" is none, bad values the defaults: %+v", g.Voice)
	}
	e := Effective(g).Voice
	if e.Fallback == nil || *e.Fallback != "" || e.Backend != "say" || e.Lang != "auto" || e.Read.MaxSentences != 5 || e.Read.MaxTurnSentences != 10 || len(e.Read.Skip) != 4 {
		t.Fatalf("effective: %+v", e)
	}
}

// exe writes an executable file (mode 0755, or 0644 when not exec).
func exe(t *testing.T, dir, name string, exec bool) string {
	t.Helper()
	p := filepath.Join(dir, name)
	mode := os.FileMode(0o755)
	if !exec {
		mode = 0o644
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// A good [voice] has no problem; ~/ is expanded and only noted as under
// the home directory.
func TestCheckVoiceClean(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := exe(t, t.TempDir(), "pekoe-voice-voicevox", true)
	exe(t, home, "pekoe-voice-mine", true)
	src := `
[voice]
enable = true
lang = "ja"
backend = "voicevox"
fallback = ""
focus_mode = "narrate"
background_mode = "mute"
[voice.read]
max_sentences = 10
max_turn_sentences = 100
skip = ["code", "url"]
[voice.phrases]
wait = "{name} が待ってる"
done_many = "{names} 終わり"
idle_all = "しずか"
[voice.backends]
voicevox = "` + out + `"
[voice.say]
ja = "Kyoko"
en = ""
`
	if ps := Check([]byte(src)); len(ps) != 0 {
		t.Fatalf("problems in a good [voice]:\n%s", problems(ps))
	}
	ps := problems(Check([]byte("[voice]\nbackend = \"mine\"\n[voice.backends]\nmine = \"~/pekoe-voice-mine\"\n")))
	if !strings.Contains(ps, "voice.backends.mine: note:") || !strings.Contains(ps, "under the home directory") || strings.Count(ps, "\n") != 0 {
		t.Fatalf("a ~/ backend: want only the home note, got:\n%s", ps)
	}
}

// Every mistake docs/voice.md 11 names is a warning, keyed by the setting;
// no backend is run (the files are bare #!/bin/sh).
func TestCheckVoiceMistakes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	noexec := exe(t, dir, "noexec", false)
	src := `
[voice]
lang = "fr"
backend = "nope"
fallback = "gone"
focus_mode = "loud"
background_mode = "x"
[voice.read]
max_sentences = 0
max_turn_sentences = 0
skip = ["code", "emoji"]
[voice.phrases]
wait = "no placeholder"
done_many = "{name} only"
bogus = "{name}"
error = "  "
[voice.backends]
say = "/usr/bin/true"
Bad_Name = "/usr/bin/true"
rel = "bin/pekoe-voice-rel"
tilde = "~bob/pekoe-voice-x"
missing = "` + filepath.Join(dir, "missing") + `"
noexec = "` + noexec + `"
empty = ""
`
	ps := problems(Check([]byte(src)))
	for _, want := range []string{
		`voice.lang: "fr" is not one of auto | ja | en`,
		`voice.focus_mode: "loud" is not one of read | narrate | mute`,
		`voice.background_mode: "x" is not one of`,
		`voice.read.max_sentences: 0 is not between 1 and 50`,
		`voice.read.max_turn_sentences: 0 is not between 1 and 100`,
		`voice.read.skip: "emoji" is not one of code | table | url | path`,
		`voice.phrases.wait: has no {name}`,
		`voice.phrases.done_many: has no {names}`,
		`voice.phrases.bogus: unknown key`,
		`voice.phrases.error: empty`,
		`voice.backends.say: "say" is the bundled backend's name`,
		`voice.backends.Bad_Name: "Bad_Name" is not a backend name`,
		`voice.backends.rel: bin/pekoe-voice-rel: not an absolute path`,
		`voice.backends.tilde: ~bob/pekoe-voice-x: only ~/ is expanded`,
		`voice.backends.missing: ` + filepath.Join(dir, "missing") + `: no such file`,
		`voice.backends.noexec: ` + noexec + `: not executable`,
		`voice.backends.empty: the path is empty`,
		`voice.backend: "nope" is neither "say" nor in [voice.backends]`,
		`voice.fallback: "gone" is neither "say" nor in [voice.backends]`,
	} {
		if !strings.Contains(ps, want) {
			t.Errorf("missing %q in:\n%s", want, ps)
		}
	}
	if ps := problems(Check([]byte("[voice.read]\nmax_sentences = 51\n"))); !strings.Contains(ps, "51 is not between 1 and 50") {
		t.Errorf("51: %s", ps)
	}
	if ps := problems(Check([]byte("[voice.read]\nmax_turn_sentences = 101\n"))); !strings.Contains(ps, "101 is not between 1 and 100") {
		t.Errorf("101: %s", ps)
	}
	// the backend is in [voice.backends] but unusable: the entry says why,
	// the name is not reported twice
	if ps := problems(Check([]byte("[voice]\nbackend = \"rel\"\n[voice.backends]\nrel = \"bin/x\"\n"))); strings.Contains(ps, "voice.backend:") {
		t.Errorf("backend named in [voice.backends] reported as missing:\n%s", ps)
	}
}

// Blanks around [voice] backend are dropped as check drops them: what
// check passes is the name used, like fallback.
func TestVoiceBackendTrimmed(t *testing.T) {
	bin := exe(t, t.TempDir(), "pekoe-voice-vv", true)
	src := "[voice]\nbackend = \" vv \"\nfallback = \" say \"\n[voice.backends]\nvv = \"" + bin + "\"\n"
	if ps := problems(Check([]byte(src))); ps != "" {
		t.Fatalf("check: %s", ps)
	}
	g, err := ParseGlobal([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if b, f := g.Voice.BackendName(), g.Voice.FallbackName(); b != "vv" || f != "say" {
		t.Fatalf("backend %q, fallback %q", b, f)
	}
	g, _ = ParseGlobal([]byte("[voice]\nbackend = \"  \"\n"))
	if b := g.Voice.BackendName(); b != "say" {
		t.Fatalf("blank backend %q", b)
	}
}
