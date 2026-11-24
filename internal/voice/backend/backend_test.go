package backend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fast are short timeouts, so the timeout cases end quickly.
var fast = Timeouts{Version: 5 * time.Second, Check: 5 * time.Second, Voices: 5 * time.Second, Synth: 5 * time.Second, Grace: 200 * time.Millisecond}

func newFake(t *testing.T, mode string) (*Backend, string) {
	t.Helper()
	dir := t.TempDir()
	p := fakeBackend(t, filepath.Join(dir, "bin"), "fake-"+mode)
	data := filepath.Join(dir, "data")
	return &Backend{Name: mode, Path: p, Dir: data, Timeouts: fast}, data
}

func outPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "out.wav")
}

func wantErr(t *testing.T, err error, op, reason string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want a *Error (%s), got %v", op, err)
	}
	if !strings.HasPrefix(e.Op, op) || !strings.Contains(e.Reason, reason) {
		t.Fatalf("want op %q reason containing %q, got op %q reason %q", op, reason, e.Op, e.Reason)
	}
	return e
}

func TestProtocolSuccess(t *testing.T) {
	b, _ := newFake(t, "ok")
	ctx := context.Background()
	if v, err := b.Version(ctx); err != nil || v != "1" {
		t.Fatalf("version %q %v (the output is trimmed)", v, err)
	}
	if err := b.CheckUsable(ctx); err != nil {
		t.Fatal(err)
	}
	vs, err := b.Voices(ctx)
	if err != nil || len(vs) != 2 || vs[0] != (Voice{"tapi", "ja", "Fake"}) {
		t.Fatalf("voices %v %v", vs, err)
	}
	out := outPath(t)
	if err := b.Synth(ctx, Request{Text: "こんにちは", Voice: "tapi", Lang: "ja"}, out); err != nil {
		t.Fatal(err)
	}
	if got := marked(b.Path); !strings.Contains(got, "synth --voice tapi --lang ja --out "+out) {
		t.Fatalf("argv: %q", got)
	}
}

// A non-zero exit fails, with the first line of stderr as the reason.
func TestNonZeroExit(t *testing.T) {
	b, _ := newFake(t, "fail")
	ctx := context.Background()
	_, err := b.Version(ctx)
	wantErr(t, err, "version", "boom: the engine is down")
	if strings.Contains(err.Error(), "second line") {
		t.Fatalf("more than one line of stderr: %v", err)
	}
	wantErr(t, b.CheckUsable(ctx), "check", "boom")
	_, err = b.Voices(ctx)
	wantErr(t, err, "voices", "boom")
	wantErr(t, b.Synth(ctx, Request{Text: "x", Voice: "tapi", Lang: "en"}, outPath(t)), "synth", "boom")
}

func TestVersionNotOne(t *testing.T) {
	b, _ := newFake(t, "v2")
	v, err := b.Version(context.Background())
	wantErr(t, err, "version", `speaks protocol "2"`)
	if v != "2" {
		t.Fatalf("version %q", v)
	}
}

func processGone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		// a zombie answers kill 0 until its new parent reaps it
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// A synth past its time is stopped with its whole process group: what it
// started goes too.
func TestTimeoutStopsProcessGroup(t *testing.T) {
	b, _ := newFake(t, "hang")
	b.Timeouts.Synth = 500 * time.Millisecond
	start := time.Now()
	err := b.Synth(context.Background(), Request{Text: "x", Voice: "tapi", Lang: "ja"}, outPath(t))
	e := wantErr(t, err, "synth", "timed out")
	if !e.Timeout {
		t.Fatal("not marked as a timeout")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
	raw, rerr := os.ReadFile(b.Path + ".child")
	if rerr != nil {
		t.Fatal("the fake did not start its child:", rerr)
	}
	pid, _ := strconv.Atoi(string(raw))
	if !processGone(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the backend's child %d outlived the timeout", pid)
	}
}

// One that ignores SIGTERM gets SIGKILL after the grace period.
func TestTimeoutKillsStubborn(t *testing.T) {
	b, _ := newFake(t, "stubborn")
	b.Timeouts.Synth = 500 * time.Millisecond
	err := b.Synth(context.Background(), Request{Text: "x", Voice: "tapi", Lang: "ja"}, outPath(t))
	wantErr(t, err, "synth", "timed out")
	raw, _ := os.ReadFile(b.Path + ".child")
	pid, _ := strconv.Atoi(string(raw))
	if pid == 0 || !processGone(pid) {
		if pid != 0 {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Fatalf("the backend (%d) survived", pid)
	}
}

// Cancelling ctx stops synth the same way.
func TestContextCancelStops(t *testing.T) {
	b, _ := newFake(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(b.Path + ".child"); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	err := b.Synth(ctx, Request{Text: "x", Voice: "tapi", Lang: "ja"}, outPath(t))
	wantErr(t, err, "synth", "context canceled")
	raw, _ := os.ReadFile(b.Path + ".child")
	pid, _ := strconv.Atoi(string(raw))
	if !processGone(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("the child outlived the cancel")
	}
}

// exit 0 is not enough: the file must be there, not empty, a RIFF file.
func TestSynthChecksTheWAV(t *testing.T) {
	for mode, reason := range map[string]string{
		"empty":   "empty file",
		"notriff": "not a WAV (RIFF)",
		"nofile":  "wrote no file",
	} {
		t.Run(mode, func(t *testing.T) {
			b, _ := newFake(t, mode)
			wantErr(t, b.Synth(context.Background(), Request{Text: "x", Voice: "tapi", Lang: "en"}, outPath(t)), "synth", reason)
		})
	}
}

// Two synth calls run side by side (the fake fails unless another one
// has started meanwhile).
func TestSynthTwoInParallel(t *testing.T) {
	b, _ := newFake(t, "pair")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	outs := []string{outPath(t), outPath(t)}
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = b.Synth(context.Background(), Request{Text: "x" + strconv.Itoa(i), Voice: "tapi", Lang: "ja"}, outs[i])
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("synth %d: %v", i, err)
		}
	}
}

// The backend runs with PATH = system directories + its own, the
// protocol's variable, the data directory as cwd, and the text on stdin as
// one line (never on the command line).
func TestBackendEnvironment(t *testing.T) {
	b, data := newFake(t, "env")
	t.Setenv("PATH", "/agent/writable/bin:/usr/bin")
	t.Setenv("PEKOE_VOICE_PROTOCOL", "99")
	text := "rm -rf ~; $(echo hi)\nnext line"
	if err := b.Synth(context.Background(), Request{Text: text, Voice: "tapi", Lang: "en"}, outPath(t)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(b.Path + ".env")
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]string
	json.Unmarshal(raw, &rec)
	if want := SystemPATH + ":" + filepath.Dir(b.Path); rec["PATH"] != want {
		t.Errorf("PATH %q, want %q", rec["PATH"], want)
	}
	if rec["PEKOE_VOICE_PROTOCOL"] != "1" {
		t.Errorf("PEKOE_VOICE_PROTOCOL %q", rec["PEKOE_VOICE_PROTOCOL"])
	}
	wd, _ := filepath.EvalSymlinks(rec["cwd"])
	dd, _ := filepath.EvalSymlinks(data)
	if wd != dd {
		t.Errorf("cwd %q, want the data directory %q", rec["cwd"], data)
	}
	if rec["stdin"] != "rm -rf ~; $(echo hi) next line" {
		t.Errorf("stdin %q", rec["stdin"])
	}
	if strings.Contains(marked(b.Path), "echo") {
		t.Errorf("the text reached argv: %q", marked(b.Path))
	}
}

func TestEnvDropsOldValues(t *testing.T) {
	got := Env([]string{"HOME=/h", "PATH=/evil", "PEKOE_VOICE_PROTOCOL=7", "LANG=ja_JP.UTF-8"}, "/opt/x/bin/pekoe-voice-x")
	want := []string{"HOME=/h", "LANG=ja_JP.UTF-8", "PEKOE_VOICE_PROTOCOL=1", "PATH=" + SystemPATH + ":/opt/x/bin"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}

func TestCleanText(t *testing.T) {
	if got := CleanText("a\n b\t\r\nc \xff"); got != "a b c" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("あ", 1000) // 3000 bytes
	got := CleanText(long)
	if len(got) > MaxText || !strings.HasPrefix(long, got) || len(got) != 1998 {
		t.Errorf("cut to %d bytes", len(got))
	}
}

// The path is checked again before every call: a file removed after
// Resolve is not run.
func TestPathCheckedEveryCall(t *testing.T) {
	b, _ := newFake(t, "ok")
	if _, err := b.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	os.Remove(b.Path)
	_, err := b.Version(context.Background())
	wantErr(t, err, "version (path)", "no such file")
}

func TestExpandPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for in, want := range map[string]string{
		"/opt/homebrew/bin/x":   "/opt/homebrew/bin/x",
		"/opt//homebrew/../b/x": "/opt/b/x",
		"~/bin/x":               filepath.Join(home, "bin", "x"),
	} {
		if got, err := ExpandPath(in); err != nil || got != want {
			t.Errorf("%q → %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "bin/x", "./x", "~", "~root/x", "pekoe-voice-x"} {
		if got, err := ExpandPath(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"voicevox", "say", "a-1"} {
		if !ValidName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{"", "Voice", "a_b", "a/b", "a b", "../x"} {
		if ValidName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}

// cache-key: one line of printable ASCII is the fingerprint; exit 2 (a
// backend that does not know it) is none; anything else is an error.
func TestCacheKey(t *testing.T) {
	ctx := context.Background()
	if b, _ := newFake(t, "ok"); true {
		if fp, err := b.CacheKey(ctx); err != nil || fp != "" {
			t.Fatalf("an older backend (exit 2): %q %v", fp, err)
		}
	}
	for _, c := range []struct {
		out, want, reason string
	}{
		{"abc123\n", "abc123", ""},
		{"engine=aivis speaker=mao", "engine=aivis speaker=mao", ""},
		{strings.Repeat("x", MaxCacheKey) + "\n", strings.Repeat("x", MaxCacheKey), ""},
		{"exit2", "", ""},
		{"exit1", "", "ckey: cannot read the settings"},
		{"\n", "", "printed nothing"},
		{strings.Repeat("x", MaxCacheKey+1), "", "more than 256 bytes"},
		{"a\nb\n", "", "printable ASCII"},
		{"まお\n", "", "printable ASCII"},
		{"a\tb\n", "", "printable ASCII"},
	} {
		b, _ := newFake(t, "ckey")
		if err := os.WriteFile(b.Path+".ckey", []byte(c.out), 0o600); err != nil {
			t.Fatal(err)
		}
		fp, err := b.CacheKey(ctx)
		if c.reason != "" {
			wantErr(t, err, "cache-key", c.reason)
			continue
		}
		if err != nil || fp != c.want {
			t.Errorf("%q: %q %v", c.out, fp, err)
		}
	}
	b, _ := newFake(t, "ckey")
	b.Timeouts.CacheKey = 200 * time.Millisecond
	os.WriteFile(b.Path+".ckey", []byte("hang"), 0o600)
	if e := wantErr(t, func() error { _, err := b.CacheKey(ctx); return err }(), "cache-key", "timed out"); !e.Timeout {
		t.Fatal("not a timeout")
	}
}
