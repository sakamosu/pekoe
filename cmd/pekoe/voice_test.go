package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/paths"
	"pekoe/internal/voice"
	"pekoe/internal/voice/backend"
)

// pekoe voice on / off rewrite only the enable line of [voice] (D4,
// docs/voice.md 12): every other line and comment stays as it was.
func TestVoiceOnOffRewrite(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		on   bool
		want string
	}{
		"no file content": {"", true, "[voice]\nenable = true\n"},
		"no table": {"[theme]\nmode = \"auto\"\n", true,
			"[theme]\nmode = \"auto\"\n\n[voice]\nenable = true\n"},
		"no table, no newline at the end": {"[theme]\nmode = \"auto\"", true,
			"[theme]\nmode = \"auto\"\n\n[voice]\nenable = true\n"},
		"no enable": {"[voice]\nlang = \"ja\"\n", true,
			"[voice]\nenable = true\nlang = \"ja\"\n"},
		"comments": {"# top\n[voice]  # the voice\n# enable = false  (an old note)\nenable = false   # off for now\nlang = \"ja\" # ja\n", true,
			"# top\n[voice]  # the voice\n# enable = false  (an old note)\nenable = true   # off for now\nlang = \"ja\" # ja\n"},
		"tables after": {"[voice]\nlang = \"ja\"\n\n[voice.backends]\nvv = \"/opt/x\"\n\n[web]\nenable = false\n", true,
			"[voice]\nenable = true\nlang = \"ja\"\n\n[voice.backends]\nvv = \"/opt/x\"\n\n[web]\nenable = false\n"},
		"enable of another table before": {"[gui]\nenable = true\n[voice]\nenable = true\n[web]\nenable = true\n", false,
			"[gui]\nenable = true\n[voice]\nenable = false\n[web]\nenable = true\n"},
		"only a subtable": {"[voice.say]\nja = \"Kyoko\"\n", true,
			"[voice.say]\nja = \"Kyoko\"\n\n[voice]\nenable = true\n"},
		"indented, CRLF":              {"[voice]\r\n  enable=false\r\n", true, "[voice]\r\n  enable=true\r\n"},
		"header with CRLF, no enable": {"[voice]\r\nlang = \"en\"\r\n", true, "[voice]\r\nenable = true\r\nlang = \"en\"\r\n"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			os.WriteFile(path, []byte(c.in), 0o600)
			var out bytes.Buffer
			if err := voiceOnOff(path, c.on, "", &out); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(path)
			if string(b) != c.want {
				t.Fatalf("got\n%q\nwant\n%q", b, c.want)
			}
			g, err := config.ParseGlobal(b)
			if err != nil || g.Voice.Enable != c.on {
				t.Fatalf("reads %v %v", g, err)
			}
			if !strings.Contains(out.String(), "within a second") && !strings.Contains(out.String(), "macOS only") {
				t.Fatalf("out: %s", out.String())
			}
		})
	}
}

// A missing config.toml is created (on); the file's mode, and a link to
// it, are kept; already as asked writes nothing.
func TestVoiceOnOffFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf", "config.toml")
	var out bytes.Buffer
	if err := voiceOnOff(path, true, "", &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "[voice]\nenable = true\n" {
		t.Fatalf("created %q", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != paths.FilePerm {
		t.Fatalf("mode %v", fi.Mode())
	}

	// off on a missing file: already off, nothing created
	missing := filepath.Join(dir, "none", "config.toml")
	out.Reset()
	if err := voiceOnOff(missing, false, "", &out); err != nil || !strings.Contains(out.String(), "already off") {
		t.Fatalf("%v %s", err, out.String())
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("created a config.toml for off")
	}

	// a link from dotfiles stays a link; the mode of the file is kept
	real := filepath.Join(dir, "dotfiles.toml")
	os.WriteFile(real, []byte("[voice]\nenable = true\n"), 0o640)
	link := filepath.Join(dir, "link.toml")
	os.Symlink(real, link)
	if err := voiceOnOff(link, false, "", &out); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a file")
	}
	if b, _ := os.ReadFile(real); string(b) != "[voice]\nenable = false\n" {
		t.Fatalf("target %q", b)
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// already off: not written (the time stays)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(real, old, old)
	out.Reset()
	if err := voiceOnOff(link, false, "", &out); err != nil || !strings.Contains(out.String(), "already off") {
		t.Fatalf("%v %s", err, out.String())
	}
	if fi, _ := os.Stat(real); !fi.ModTime().Equal(old) {
		t.Fatal("rewritten although already off")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 3 { // conf, dotfiles.toml, link.toml: no temp file left
		t.Fatalf("left behind: %d entries", len(ents))
	}
}

// A link whose file is not there yet (dotfiles linked before the file is
// made) stays a link: on writes the file where the link points, through
// relative links and links to links too. A loop of links is refused.
func TestVoiceOnOffDanglingLink(t *testing.T) {
	dir := t.TempDir()
	for name, mk := range map[string]func(link, real string){
		"absolute": func(link, real string) { os.Symlink(real, link) },
		"relative": func(link, real string) {
			rel, _ := filepath.Rel(filepath.Dir(link), real)
			os.Symlink(rel, link)
		},
		"link to a link": func(link, real string) {
			mid := link + ".mid"
			os.Symlink(real, mid)
			os.Symlink(mid, link)
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
			os.MkdirAll(filepath.Join(d, "conf"), 0o700)
			real := filepath.Join(d, "dotfiles", "pekoe", "config.toml")
			link := filepath.Join(d, "conf", "config.toml")
			mk(link, real)
			var out bytes.Buffer
			if err := voiceOnOff(link, true, "", &out); err != nil {
				t.Fatal(err)
			}
			if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the link was replaced by a file (%v)", err)
			}
			if b, err := os.ReadFile(real); err != nil || string(b) != "[voice]\nenable = true\n" {
				t.Fatalf("target %q %v", b, err)
			}
		})
	}

	loop := filepath.Join(dir, "loop.toml")
	os.Symlink(loop, loop)
	var out bytes.Buffer
	if err := voiceOnOff(loop, true, "", &out); err == nil || !strings.Contains(err.Error(), "too many links") {
		t.Fatalf("loop: %v", err)
	}
	if fi, err := os.Lstat(loop); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the loop link was replaced (%v)", err)
	}
}

// What does not read after the change is not kept: a config.toml that
// does not read now, a [voice] the line edit cannot reach (an inline
// table), and a file that does not read once written are left (or put
// back) as they were.
func TestVoiceOnOffUnreadable(t *testing.T) {
	for name, in := range map[string]string{
		"broken already": "[voice\nenable = false\n",
		"inline table":   "voice = { lang = \"ja\" }\n",
		"dotted key":     "voice.enable = false\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			os.WriteFile(path, []byte(in), 0o600)
			var out bytes.Buffer
			err := voiceOnOff(path, true, "", &out)
			if err == nil || !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "pekoe config edit") {
				t.Fatalf("err %v", err)
			}
			if b, _ := os.ReadFile(path); string(b) != in {
				t.Fatalf("changed to %q", b)
			}
			if ents, _ := os.ReadDir(dir); len(ents) != 1 {
				t.Fatalf("left behind: %d files", len(ents))
			}
		})
	}

	t.Run("does not read once written", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		in := "[voice]\nenable = false # off\n"
		os.WriteFile(path, []byte(in), 0o600)
		old := replaceConfig
		t.Cleanup(func() { replaceConfig = old })
		replaceConfig = func(target string, b []byte, exists bool) error {
			return writeReplace(target, []byte("[voice\n"), exists) // e.g. a sync tool wrote over it
		}
		err := voiceOnOff(path, true, "", &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "put back as it was") {
			t.Fatalf("err %v", err)
		}
		if b, _ := os.ReadFile(path); string(b) != in {
			t.Fatalf("not put back: %q", b)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 1 {
			t.Fatalf("left behind: %d files", len(ents))
		}

		// a missing file that does not read once written is removed again
		missing := filepath.Join(dir, "new", "config.toml")
		if err := voiceOnOff(missing, true, "", &bytes.Buffer{}); err == nil {
			t.Fatal("no error")
		}
		if _, err := os.Stat(missing); err == nil {
			t.Fatal("the unreadable file was kept")
		}
	})
}

// In an agent's window ($PEKOE_SESSION) on / off are refused before the
// file is touched (a guard against a slip; the sandbox is the protection).
func TestVoiceOnOffRefusedInSession(t *testing.T) {
	fd := startFakeDaemon(t)
	path := paths.GlobalConfig()
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("[voice]\nenable = false\n"), 0o600)
	t.Setenv("PEKOE_SESSION", "feat-x")
	for _, sub := range []string{"on", "off"} {
		err := cmdVoice([]string{sub})
		if err == nil || !strings.Contains(err.Error(), "is for a person") || !strings.Contains(err.Error(), "feat-x") {
			t.Fatalf("%s: %v", sub, err)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "[voice]\nenable = false\n" {
		t.Fatalf("changed: %q", b)
	}
	if c := fd.calls(); len(c) > 0 {
		t.Fatalf("on / off went to pekoed: %v", c)
	}

	// from a person's terminal it goes through, still without pekoed
	t.Setenv("PEKOE_SESSION", "")
	if _, err := capture(t, func() error { return cmdVoice([]string{"on"}) }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "[voice]\nenable = true\n" {
		t.Fatalf("not on: %q", b)
	}
	if c := fd.calls(); len(c) > 0 {
		t.Fatalf("on went to pekoed: %v", c)
	}
}

// pekoe voice backends shows the pekoe-voice-* in PATH as candidates, with
// the line to write, and nothing runs them (docs/voice.md 12, P1-4).
func TestVoiceBackendsCandidatesNotRun(t *testing.T) {
	fd := startFakeDaemon(t)
	bin := t.TempDir()
	marks := t.TempDir()
	for _, n := range []string{"pekoe-voice-say", "pekoe-voice-voicevox"} {
		script := "#!/bin/sh\necho ran > '" + filepath.Join(marks, n) + "'\n"
		if err := os.WriteFile(filepath.Join(bin, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// pekoed's answer while voice is off: the candidates in its PATH
	// (this process's, as pekoed would list them; never run)
	fd.results = map[string]any{"voice.backends": daemon.VoiceBackends{
		Backends:   []backend.Info{{Name: "say", Path: "/usr/local/bin/pekoe", Fallback: true, Check: "not run (voice is off)"}},
		Candidates: backend.Candidates(os.Getenv("PATH")),
		Note:       "voice is off; the backends were not run",
	}}
	out, err := capture(t, func() error { return cmdVoice([]string{"backends"}) })
	if err != nil {
		t.Fatal(err)
	}
	vv := filepath.Join(bin, "pekoe-voice-voicevox")
	for _, want := range []string{
		"say ", "(fallback)", "check: not run (voice is off)", "note: voice is off",
		"candidates in pekoed's PATH (not run", vv, "[voice.backends]", `voicevox = "` + vv + `"`,
		`say = "` + filepath.Join(bin, "pekoe-voice-say") + `"`, "sends the text out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if ents, _ := os.ReadDir(marks); len(ents) > 0 {
		t.Fatalf("a candidate was run: %d marks", len(ents))
	}
	if c := fd.calls(); len(c) != 1 || c[0] != "voice.backends" {
		t.Fatalf("calls %v", c)
	}
}

// The status says why nothing is said: synthesis paused after repeated
// failures (Q11) for how much longer, and the last failure (P1-1's note).
func TestVoiceStatusPaused(t *testing.T) {
	now := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	st := daemon.VoiceStatus{
		Enabled: true, Supported: true, Backend: "voicevox", Using: "say",
		BackendError: "voicevox: not executable",
		Error:        "synth failed 3 times in a row (say: exit status 1); paused until " + now.Add(42*time.Second).Format(time.RFC3339),
		PausedUntil:  now.Add(42 * time.Second).Format(time.RFC3339),
		MutedUntil:   now.Add(28*time.Minute + 10*time.Second).Format(time.RFC3339),
		Focus:        "feat-x", Queue: 2,
		Speaking: &voice.Speaking{Slug: "feat-y", Kind: voice.KindWait, Text: "y、確認待ちだよ"},
	}
	var b bytes.Buffer
	printVoiceStatus(&b, st, now)
	out := b.String()
	for _, want := range []string{
		"voice: on · backend voicevox · using the fallback say", "backend: voicevox: not executable",
		"error: synth failed 3 times in a row (say: exit status 1)", "synthesis paused for another 42s (until 15:30:42)",
		"muted: until 15:58 (28m left)", "focus: feat-x", `queue: 2 · speaking: feat-y wait "y、確認待ちだよ"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	b.Reset()
	printVoiceStatus(&b, daemon.VoiceStatus{Supported: true, MutedUntil: "forever"}, now)
	if out := b.String(); !strings.Contains(out, "voice: off ([voice] enable = false) · pekoe voice on") || !strings.Contains(out, "until pekoe voice unmute") || strings.Contains(out, "queue") {
		t.Errorf("off:\n%s", out)
	}
	b.Reset()
	printVoiceStatus(&b, daemon.VoiceStatus{Enabled: true}, now)
	if out := b.String(); !strings.Contains(out, "unsupported") {
		t.Errorf("unsupported:\n%s", out)
	}
	for d, want := range map[time.Duration]string{400 * time.Millisecond: "1s", 59 * time.Second: "59s", 59*time.Second + 600*time.Millisecond: "1m", 90 * time.Minute: "1h30m", 2 * time.Hour: "2h", 30 * time.Minute: "30m"} {
		if got := voiceDur(d); got != want {
			t.Errorf("%v: %s, want %s", d, got, want)
		}
	}
}

// fakeVoice answers voice.say, voice.log and voice.status for voiceTest.
type fakeVoice struct {
	say    daemon.VoiceStatus
	sayErr error
	log    func(n int) []voice.LogEntry
	status daemon.VoiceStatus
	calls  []string
}

func (f *fakeVoice) Call(method string, params, out any) error {
	f.calls = append(f.calls, method)
	var v any
	switch method {
	case "voice.say":
		if f.sayErr != nil {
			return f.sayErr
		}
		v = f.say
	case "voice.log":
		v = f.log(len(f.calls))
	case "voice.status":
		v = f.status
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

// pekoe voice test says why a sentence was not said: synthesis paused
// (Q11: it fails at once), or the failure itself; and how it was said.
func TestVoiceTest(t *testing.T) {
	now := time.Now()
	paused := daemon.VoiceStatus{Enabled: true, Supported: true, Error: "synth failed 3 times in a row (say: exit status 1); paused until …",
		PausedUntil: now.Add(50 * time.Second).Format(time.RFC3339)}
	f := &fakeVoice{say: paused}
	err := voiceTest(f, "テスト", "", "", time.Second, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "synthesis paused for another") || !strings.Contains(err.Error(), "say: exit status 1") {
		t.Fatalf("paused: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls %v", f.calls)
	}

	entry := func(r voice.Result, reason string) func(int) []voice.LogEntry {
		return func(call int) []voice.LogEntry {
			old := voice.LogEntry{Time: now.Add(-time.Minute), Kind: voice.KindTest, Text: "テスト", Result: voice.ResultSpoken}
			if call < 3 { // not there at the first look
				return []voice.LogEntry{old}
			}
			return []voice.LogEntry{{Time: time.Now(), Kind: voice.KindTest, Text: "テスト", Result: r, Reason: reason, LatencyMS: 640, Backend: "say"}, old}
		}
	}
	f = &fakeVoice{log: entry(voice.ResultFailed, "say: exit status 1"), status: paused}
	err = voiceTest(f, " テスト\n", "tapi", "ja", 5*time.Second, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "failed: say: exit status 1") || !strings.Contains(err.Error(), "paused for another") {
		t.Fatalf("failed: %v", err)
	}

	var out bytes.Buffer
	f = &fakeVoice{log: entry(voice.ResultSpoken, "")}
	if err := voiceTest(f, "テスト", "", "", 5*time.Second, &out); err != nil || !strings.Contains(out.String(), "said (say, 640ms") {
		t.Fatalf("spoken: %v %s", err, out.String())
	}

	// the player failed after the sound started: a failed line comes after
	// the spoken one, and "said" is not printed
	out.Reset()
	t0 := time.Now()
	spoken := voice.LogEntry{Time: t0, Kind: voice.KindTest, Text: "テスト", Result: voice.ResultSpoken, LatencyMS: 640, Backend: "say"}
	f = &fakeVoice{status: daemon.VoiceStatus{Speaking: &voice.Speaking{Kind: voice.KindTest, Text: "テスト"}}, log: func(call int) []voice.LogEntry {
		if call < 5 {
			return []voice.LogEntry{spoken}
		}
		return []voice.LogEntry{{Time: t0.Add(5 * time.Millisecond), Kind: voice.KindTest, Text: "テスト", Result: voice.ResultFailed, Reason: "play failed: afplay: exit status 1", Backend: "say"}, spoken}
	}}
	if err := voiceTest(f, "テスト", "", "", 5*time.Second, &out); err == nil || !strings.Contains(err.Error(), "play failed: afplay") || strings.Contains(out.String(), "said") {
		t.Fatalf("play failed: %v %s", err, out.String())
	}
	// the sound ended and no failed line came: said
	f = &fakeVoice{status: daemon.VoiceStatus{}, log: func(int) []voice.LogEntry { return []voice.LogEntry{spoken} }}
	if err := voiceTest(f, "テスト", "", "", 5*time.Second, &out); err != nil || !strings.Contains(out.String(), "said (say, 640ms") {
		t.Fatalf("ended: %v %s", err, out.String())
	}
	if n := strings.Count(strings.Join(f.calls, " "), "voice.log"); n != 2 {
		t.Fatalf("voice.log looked at %d times, want 2 (one more after the end): %v", n, f.calls)
	}

	out.Reset()
	f = &fakeVoice{log: func(int) []voice.LogEntry { return nil }}
	if err := voiceTest(f, "テスト", "", "", 300*time.Millisecond, &out); err != nil || !strings.Contains(out.String(), "pekoe voice log") {
		t.Fatalf("not yet: %v %s", err, out.String())
	}

	f = &fakeVoice{sayErr: errors.New("voice.say: voice is off: pekoe voice on")}
	if err := voiceTest(f, "テスト", "", "", time.Second, &out); err == nil || !strings.Contains(err.Error(), "voice is off") {
		t.Fatalf("off: %v", err)
	}
}

// The flags reach pekoed as voice.* params; bad values are refused first.
func TestVoiceCommands(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{
		"voice.mute": daemon.VoiceStatus{Enabled: true, Supported: true, MutedUntil: "forever"},
		"voice.log":  []voice.LogEntry{{Time: time.Now(), Slug: "feat-x", Kind: voice.KindWait, Text: "x、確認待ちだよ", Result: voice.ResultSpoken, LatencyMS: 410, Backend: "say"}, {Time: time.Now(), Kind: voice.KindTest, Text: "t", Result: voice.ResultFailed, Reason: "say: exit status 1"}},
	}
	params := func(m string) string {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return string(fd.params[m])
	}
	out, err := capture(t, func() error { return cmdVoice([]string{"mute", "--for", "30m"}) })
	if err != nil || params("voice.mute") != `{"for":"30m"}` || !strings.Contains(out, "voice muted") {
		t.Fatalf("mute: %v %s %s", err, params("voice.mute"), out)
	}
	if _, err := capture(t, func() error { return cmdVoice([]string{"unmute"}) }); err != nil || params("voice.mute") != `{"off":true}` {
		t.Fatalf("unmute: %v %s", err, params("voice.mute"))
	}
	if _, err := capture(t, func() error { return cmdVoice([]string{"mute"}) }); err != nil || params("voice.mute") != `{}` {
		t.Fatalf("mute forever: %v %s", err, params("voice.mute"))
	}
	out, err = capture(t, func() error { return cmdVoice([]string{"log", "-n", "5"}) })
	if err != nil || params("voice.log") != `{"n":5}` || !strings.Contains(out, "feat-x") || !strings.Contains(out, "410ms") || !strings.Contains(out, "    say: exit status 1") {
		t.Fatalf("log: %v %s\n%s", err, params("voice.log"), out)
	}
	if _, err := capture(t, func() error { return cmdVoice([]string{"stop"}) }); err != nil {
		t.Fatal(err)
	}
	fd.reset()
	for _, args := range [][]string{
		{"mute", "--for", "soon"}, {"mute", "--for", "-1m"}, {"log", "-n", "0"}, {"log", "-n", "x"},
		{"test"}, {"test", "x", "--voice", "robot"}, {"test", "x", "--lang", "fr"},
		{"on", "now"}, {"frob"}, {"mute", "--until", "x"}, {"stop", "-n", "1"},
	} {
		if err := cmdVoice(args); err == nil {
			t.Errorf("%v accepted", args)
		} else if _, ok := err.(*usageError); !ok {
			t.Errorf("%v: %v, want a usage error", args, err)
		}
	}
	if c := fd.calls(); len(c) > 0 {
		t.Fatalf("refused commands reached pekoed: %v", c)
	}
}

// pekoe voice mode sends {slug, mode} to voice.mode; a wrong number of
// arguments or an unknown mode is refused before pekoed, and pekoed's "no
// session" comes back as the error.
func TestVoiceModeCommand(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{
		"voice.mode": daemon.VoiceStatus{Enabled: true, Supported: true, Modes: map[string]voice.Mode{"feat-x": voice.ModeMute}},
	}
	params := func() string {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return string(fd.params["voice.mode"])
	}
	out, err := capture(t, func() error { return cmdVoice([]string{"mode", "feat-x", "mute"}) })
	if err != nil || params() != `{"mode":"mute","slug":"feat-x"}` || !strings.Contains(out, "feat-x: mute") {
		t.Fatalf("mode: %v %s %q", err, params(), out)
	}
	out, err = capture(t, func() error { return cmdVoice([]string{"mode", "+tapi", "auto"}) })
	if err != nil || params() != `{"mode":"auto","slug":"+tapi"}` || !strings.Contains(out, "+tapi: no mode of its own") {
		t.Fatalf("auto: %v %s %q", err, params(), out)
	}
	jsonOut = true
	out, err = capture(t, func() error { return cmdVoice([]string{"mode", "feat-x", "mute"}) })
	jsonOut = false
	if err != nil || !strings.Contains(out, `"modes": {`) || !strings.Contains(out, `"feat-x": "mute"`) {
		t.Fatalf("--json: %v %q", err, out)
	}
	fd.reset()
	for _, args := range [][]string{
		{"mode"}, {"mode", "feat-x"}, {"mode", "feat-x", "loud"}, {"mode", "feat-x", "read", "extra"}, {"mode", "feat-x", "READ"},
		{"mode", "feat-x", "read", "--for", "1m"},
	} {
		if err := cmdVoice(args); err == nil {
			t.Errorf("%v accepted", args)
		} else if _, ok := err.(*usageError); !ok {
			t.Errorf("%v: %v, want a usage error", args, err)
		}
	}
	if c := fd.calls(); len(c) > 0 {
		t.Fatalf("refused commands reached pekoed: %v", c)
	}
	fd.errs = map[string]string{"voice.mode": `no session "nope"`}
	if _, err := capture(t, func() error { return cmdVoice([]string{"mode", "nope", "read"}) }); err == nil || !strings.Contains(err.Error(), `no session "nope"`) {
		t.Fatalf("unknown slug: %v", err)
	}
}

// pekoe voice lists the sessions' own modes, sorted; none, no line.
func TestVoiceStatusModes(t *testing.T) {
	var b bytes.Buffer
	printVoiceStatus(&b, daemon.VoiceStatus{Enabled: true, Supported: true, Backend: "say", Using: "say",
		Modes: map[string]voice.Mode{"feat-b": voice.ModeMute, "feat-a": voice.ModeRead}}, time.Now())
	if !strings.Contains(b.String(), "\nmodes: feat-a read · feat-b mute\n") {
		t.Fatalf("status:\n%s", b.String())
	}
	b.Reset()
	printVoiceStatus(&b, daemon.VoiceStatus{Enabled: true, Supported: true, Modes: map[string]voice.Mode{}}, time.Now())
	if strings.Contains(b.String(), "modes:") {
		t.Fatalf("no modes:\n%s", b.String())
	}
}
