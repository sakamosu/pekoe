package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pathWorld is a PATH holding pekoe-voice-say and pekoe-voice-voicevox
// that leave a mark when run, and a fake pekoe (the bundled say).
type pathWorld struct {
	pathDir, say, voicevox, self, data string
	logs                               []string
	mu                                 sync.Mutex
}

func newPathWorld(t *testing.T) *pathWorld {
	t.Helper()
	dir := t.TempDir()
	w := &pathWorld{pathDir: filepath.Join(dir, "path"), data: filepath.Join(dir, "data")}
	w.say = fakeBackend(t, w.pathDir, CandidatePrefix+"say")
	w.voicevox = fakeBackend(t, w.pathDir, CandidatePrefix+"voicevox")
	w.self = fakeBackend(t, filepath.Join(dir, "pekoe-bin"), "fake-pekoe")
	t.Setenv("PATH", w.pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return w
}

func (w *pathWorld) opts(backend, fallback string, backends map[string]string) Options {
	return Options{Backend: backend, Fallback: fallback, Backends: backends, Self: w.self, Dir: w.data, Timeouts: fast,
		Logf: func(f string, a ...any) {
			w.mu.Lock()
			w.logs = append(w.logs, strings.TrimSpace(fmt.Sprintf(f, a...)))
			w.mu.Unlock()
		}}
}

func (w *pathWorld) noPathRun(t *testing.T) {
	t.Helper()
	for _, p := range []string{w.say, w.voicevox} {
		if m := marked(p); m != "" {
			t.Errorf("%s in PATH was run: %q", filepath.Base(p), m)
		}
	}
}

// (1) backend = "say" runs the bundled one (pekoe voice-backend-say), never
// the pekoe-voice-say in PATH.
func TestPathSayIsBundled(t *testing.T) {
	w := newPathWorld(t)
	e := New(w.opts("say", "", nil))
	if err := e.Select(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Synth(context.Background(), "テスト", "tapi", "ja"); err != nil {
		t.Fatal(err)
	}
	if got := marked(w.self); !strings.Contains(got, BuiltinCommand+" version") || !strings.Contains(got, BuiltinCommand+" synth") {
		t.Fatalf("the bundled say was not run: %q", got)
	}
	if st := e.Status(); st.Using != "say" || st.Error != "" {
		t.Fatalf("%+v", st)
	}
	w.noPathRun(t)
	// a [voice.backends] say = … is not used either
	e = New(w.opts("say", "", map[string]string{"say": w.say}))
	if err := e.Select(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.noPathRun(t)
}

// (2) backend = "voicevox" missing from [voice.backends]: PATH's
// pekoe-voice-voicevox is not run, the fallback is used, and why is kept.
func TestPathNameNotConfigured(t *testing.T) {
	w := newPathWorld(t)
	e := New(w.opts("voicevox", "say", nil))
	if err := e.Select(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := e.Status()
	if st.Using != "say" || !strings.Contains(st.Error, `"voicevox" is not in [voice.backends]`) {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(strings.Join(w.logs, "\n"), "voicevox cannot be used") {
		t.Errorf("not logged: %q", w.logs)
	}
	if _, err := e.Synth(context.Background(), "x", "tapi", "en"); err != nil {
		t.Fatal(err)
	}
	w.noPathRun(t)
	if !strings.Contains(marked(w.self), BuiltinCommand+" synth") {
		t.Fatal("the fallback did not synthesize")
	}
}

// (3) a relative path, ~user/, a missing file, a file that cannot be
// executed: all fall back the same way, PATH untouched.
func TestPathBadPathsFallBack(t *testing.T) {
	w := newPathWorld(t)
	plain := filepath.Join(t.TempDir(), "pekoe-voice-voicevox")
	os.WriteFile(plain, []byte("#!/bin/sh\ntouch "+plain+".called\n"), 0o644)
	dir := t.TempDir()
	for name, c := range map[string]struct{ path, reason string }{
		"relative":     {"pekoe-voice-voicevox", "not an absolute path"},
		"relative dot": {"./pekoe-voice-voicevox", "not an absolute path"},
		"tilde user":   {"~root/pekoe-voice-voicevox", "only ~/ is expanded"},
		"missing":      {filepath.Join(dir, "nope"), "no such file"},
		"not exec":     {plain, "not executable"},
		"directory":    {dir, "not a regular file"},
		"empty":        {"", "the path is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			w.logs = nil
			e := New(w.opts("voicevox", "say", map[string]string{"voicevox": c.path}))
			if err := e.Select(context.Background()); err != nil {
				t.Fatal(err)
			}
			if st := e.Status(); st.Using != "say" || !strings.Contains(st.Error, c.reason) {
				t.Fatalf("%+v, want using say and %q", st, c.reason)
			}
			w.noPathRun(t)
			if marked(plain) != "" {
				t.Fatal("the non-executable file ran")
			}
		})
	}
}

// (4) ~/ is expanded and the backend runs.
func TestPathTildeExpanded(t *testing.T) {
	w := newPathWorld(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	vv := fakeBackend(t, filepath.Join(home, "bin"), "fake-ok")
	e := New(w.opts("voicevox", "say", map[string]string{"voicevox": "~/bin/fake-ok"}))
	if err := e.Select(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := e.Status(); st.Using != "voicevox" || st.Error != "" {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(marked(vv), "version") {
		t.Fatal("not run")
	}
	w.noPathRun(t)
}

// (5) listing the candidates in PATH never starts them, and List runs only
// the bundled say and [voice.backends].
func TestPathCandidatesNotRun(t *testing.T) {
	w := newPathWorld(t)
	c := Candidates(os.Getenv("PATH"))
	got := map[string]Candidate{}
	for _, x := range c {
		got[x.Name] = x
	}
	if got["voicevox"].Path != w.voicevox || got["voicevox"].Line != `voicevox = "`+w.voicevox+`"` || got["say"].Path != w.say {
		t.Fatalf("candidates %+v", c)
	}
	w.noPathRun(t)

	e := New(w.opts("say", "", nil))
	infos, cands := e.List(context.Background(), os.Getenv("PATH"))
	if len(infos) != 1 || infos[0].Name != "say" || infos[0].Check != "ok" || len(infos[0].Voices) == 0 {
		t.Fatalf("infos %+v", infos)
	}
	if len(cands) != len(c) {
		t.Fatalf("cands %+v", cands)
	}
	w.noPathRun(t)
}

// Candidates: the first of a name in PATH order wins; non-executables,
// relative directories and bad names are left out.
func TestCandidatesFilter(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(a, "pekoe-voice-x"), []byte{}, 0o755)
	os.WriteFile(filepath.Join(b, "pekoe-voice-x"), []byte{}, 0o755)
	os.WriteFile(filepath.Join(b, "pekoe-voice-plain"), []byte{}, 0o644)
	os.WriteFile(filepath.Join(b, "pekoe-voice-Bad_Name"), []byte{}, 0o755)
	os.Mkdir(filepath.Join(b, "pekoe-voice-dir"), 0o755)
	c := Candidates(strings.Join([]string{"relative", a, b}, string(os.PathListSeparator)))
	if len(c) != 1 || c[0].Path != filepath.Join(a, "pekoe-voice-x") {
		t.Fatalf("%+v", c)
	}
}

func TestListWarnings(t *testing.T) {
	w := newPathWorld(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	inHome := fakeBackend(t, filepath.Join(home, "bin"), "fake-ok")
	broken := fakeBackend(t, t.TempDir(), "fake-fail")
	e := New(w.opts("mine", "say", map[string]string{"mine": inHome, "broken": broken, "say": w.say, "Bad": "/x"}))
	infos, _ := e.List(context.Background(), "")
	by := map[string]Info{}
	for _, in := range infos {
		by[in.Name] = in
	}
	if len(infos) != 4 || infos[0].Name != "say" {
		t.Fatalf("%+v", infos)
	}
	if in := by["mine"]; in.Check != "ok" || len(in.Warnings) != 1 || !strings.Contains(in.Warnings[0], "under the home directory") {
		t.Errorf("mine %+v", in)
	}
	if in := by["broken"]; !strings.Contains(in.Check, "boom") {
		t.Errorf("broken %+v", in)
	}
	if in := by["Bad"]; len(in.Warnings) == 0 || !strings.Contains(in.Check, "not a backend name") {
		t.Errorf("Bad %+v", in)
	}
	if !by["say"].Fallback {
		t.Errorf("say is the fallback: %+v", by["say"])
	}
	w.noPathRun(t)
}

func TestHomeWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if HomeWarning("/opt/homebrew/bin/pekoe-voice-x") != "" {
		t.Error("warned outside home")
	}
	if HomeWarning(filepath.Join(home, ".local", "bin", "x")) == "" {
		t.Error("no warning under home")
	}
	// a link outside home pointing into it
	target := filepath.Join(home, "real")
	os.WriteFile(target, nil, 0o755)
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(target, link)
	if HomeWarning(link) == "" {
		t.Error("no warning for a link into home")
	}
}

// Neither the backend nor the fallback usable: nothing is used and the
// reason names both; Synth says so without running anything else.
func TestBothUnusable(t *testing.T) {
	w := newPathWorld(t)
	o := w.opts("voicevox", "other", map[string]string{"voicevox": fakeBackend(t, t.TempDir(), "fake-v2")})
	e := New(o)
	if err := e.Select(context.Background()); err == nil {
		t.Fatal("no error")
	}
	st := e.Status()
	if st.Using != "" || !strings.Contains(st.Error, `speaks protocol "2"`) || !strings.Contains(st.Error, `fallback: voice backend other path: "other" is not in [voice.backends]`) {
		t.Fatalf("%+v", st)
	}
	_, err := e.Synth(context.Background(), "x", "tapi", "ja")
	if err == nil {
		t.Fatal("synth without a backend")
	}
	// the fallback was just checked and failed: nothing to try again
	if _, _, s := e.FallbackFor(err); s != nil {
		t.Fatal("offered the fallback with neither usable")
	}
	// no fallback configured
	e = New(w.opts("voicevox", "", nil))
	if err := e.Select(context.Background()); err == nil || e.Status().Using != "" {
		t.Fatalf("%v %+v", err, e.Status())
	}
	w.noPathRun(t)
}

// Failover (called by the speech queue after repeated failures) switches
// to the fallback once; after RecheckAfter the configured backend is asked
// again and used when it passes.
func TestFailoverAndRecheck(t *testing.T) {
	w := newPathWorld(t)
	flaky := fakeBackend(t, t.TempDir(), "fake-flaky")
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	o := w.opts("vv", "say", map[string]string{"vv": flaky})
	o.Now = func() time.Time { return now }
	e := New(o)
	ctx := context.Background()
	if err := e.Select(ctx); err != nil || e.Status().Using != "vv" {
		t.Fatalf("%v %+v", err, e.Status())
	}
	os.WriteFile(flaky+".broken", nil, 0o600)
	if _, err := e.Synth(ctx, "x", "tapi", "ja"); err == nil || !strings.Contains(err.Error(), "flaky: broken") {
		t.Fatalf("want the backend's reason, got %v", err)
	}
	if !e.Failover() {
		t.Fatal("did not switch")
	}
	if st := e.Status(); st.Using != "say" || st.Error == "" {
		t.Fatalf("%+v", st)
	}
	if e.Failover() {
		t.Fatal("switched twice (already on the fallback: the queue pauses)")
	}
	if _, err := e.Synth(ctx, "y", "tapi", "ja"); err != nil {
		t.Fatal(err)
	}
	// still broken after 5 minutes: stays on the fallback
	now = now.Add(RecheckAfter)
	e.Synth(ctx, "z", "tapi", "ja")
	if e.Status().Using != "say" {
		t.Fatal("went back to a broken backend")
	}
	// mended: 4 minutes is not yet, 5 more is
	os.Remove(flaky + ".broken")
	now = now.Add(4 * time.Minute)
	e.Synth(ctx, "a", "tapi", "ja")
	if e.Status().Using != "say" {
		t.Fatal("asked again too soon")
	}
	now = now.Add(time.Minute)
	if _, err := e.Synth(ctx, "b", "tapi", "ja"); err != nil {
		t.Fatal(err)
	}
	if st := e.Status(); st.Using != "vv" || st.Error != "" {
		t.Fatalf("did not go back: %+v", st)
	}
	w.noPathRun(t)
}

// FallbackFor (10.5): a failure of the configured backend is offered to
// the fallback for the same sentence; a failure of the fallback itself, or
// with no fallback, is not. After Failover the configured backend is not
// run for synth.
func TestFallbackFor(t *testing.T) {
	w := newPathWorld(t)
	flaky := fakeBackend(t, t.TempDir(), "fake-flaky")
	e := New(w.opts("vv", "say", map[string]string{"vv": flaky}))
	ctx := context.Background()
	if err := e.Select(ctx); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(flaky+".broken", nil, 0o600)
	_, err := e.Synth(ctx, "x", "tapi", "ja")
	if err == nil {
		t.Fatal("no error")
	}
	failed, name, synth := e.FallbackFor(err)
	if failed != "vv" || name != "say" || synth == nil {
		t.Fatalf("FallbackFor = %q %q %v", failed, name, synth != nil)
	}
	before := marked(w.self)
	p, err := synth(ctx, "x", "tapi", "ja")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
		t.Fatalf("no wav at %s: %v", p, err)
	}
	if got := strings.TrimPrefix(marked(w.self), before); !strings.Contains(got, BuiltinCommand+" check") || !strings.Contains(got, BuiltinCommand+" synth --voice tapi --lang ja") {
		t.Fatalf("the fallback was not checked and run: %q", got)
	}
	// kept: a second retry does not check it again
	before = marked(w.self)
	if _, err := synth(ctx, "y", "tapi", "ja"); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimPrefix(marked(w.self), before); strings.Contains(got, "check") || !strings.Contains(got, "synth") {
		t.Fatalf("second retry ran %q", got)
	}
	if st := e.Status(); st.Using != "vv" {
		t.Fatalf("a retry switched the backend: %+v", st)
	}
	// not offered: an error that is no backend's, the fallback's own
	if _, _, s := e.FallbackFor(errors.New("other")); s != nil {
		t.Error("offered for a plain error")
	}
	if _, _, s := e.FallbackFor(&Error{Backend: "say", Op: "synth", Reason: "x"}); s != nil {
		t.Error("offered for the fallback's own failure")
	}
	// switched: synth runs only the fallback
	if !e.Failover() {
		t.Fatal("did not switch")
	}
	before = marked(flaky)
	if _, err := e.Synth(ctx, "z", "tapi", "ja"); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimPrefix(marked(flaky), before); got != "" {
		t.Fatalf("the configured backend ran after the switch: %q", got)
	}
	// no fallback configured: nothing to offer
	e = New(w.opts("vv", "", map[string]string{"vv": flaky}))
	if _, _, s := e.FallbackFor(&Error{Backend: "vv", Op: "synth", Reason: "x"}); s != nil {
		t.Error("offered with no fallback")
	}
	w.noPathRun(t)
}

func TestFailoverWithoutFallback(t *testing.T) {
	w := newPathWorld(t)
	e := New(w.opts("say", "", nil))
	e.Select(context.Background())
	if e.Failover() {
		t.Fatal("switched to nothing")
	}
	// a fallback that cannot be used
	e = New(w.opts("say", "gone", nil))
	e.Select(context.Background())
	if e.Failover() || e.Status().Using != "say" {
		t.Fatalf("%+v", e.Status())
	}
}

// tapi sentences are cached (the second Synth runs nothing); reader
// sentences are not, and Release removes them.
func TestSynthCache(t *testing.T) {
	w := newPathWorld(t)
	e := New(w.opts("say", "", nil))
	ctx := context.Background()
	p1, err := e.Synth(ctx, "csv export、確認待ちだよ", "tapi", "ja")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p1) != filepath.Join(w.data, "voice", "cache") {
		t.Fatalf("not in the cache: %s", p1)
	}
	calls := strings.Count(marked(w.self), "synth")
	p2, err := e.Synth(ctx, "csv export、確認待ちだよ", "tapi", "ja")
	if err != nil || p2 != p1 || strings.Count(marked(w.self), "synth") != calls {
		t.Fatalf("cache missed: %s %v", p2, err)
	}
	if p3, _ := e.Synth(ctx, "csv export、確認待ちだよ", "tapi", "en"); p3 == p1 {
		t.Fatal("the language is not in the key")
	}
	r, err := e.Synth(ctx, "an agent's words", ReaderVoice, "en")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r, filepath.Join(w.data, "voice", "tmp")+string(filepath.Separator)) {
		t.Fatalf("reader sentence at %s", r)
	}
	ents, _ := os.ReadDir(filepath.Join(w.data, "voice", "cache"))
	if len(ents) != 2 {
		t.Fatalf("cache holds %d files (the reader sentence must not be kept)", len(ents))
	}
	e.Release(p1) // a cached file is left alone
	if _, err := os.Stat(p1); err != nil {
		t.Fatal("Release removed a cached file")
	}
	e.Release(r)
	if _, err := os.Stat(filepath.Dir(r)); !os.IsNotExist(err) {
		t.Fatal("Release left the reader file")
	}
	// nothing is left in voice/tmp
	if ents, _ := os.ReadDir(filepath.Join(w.data, "voice", "tmp")); len(ents) != 0 {
		t.Fatalf("voice/tmp: %v", ents)
	}
}

// The bundled say's voices ([voice.say], Options.Variant) are part of the
// cache key: another voice is synthesized again, not served from the cache.
func TestSynthCacheVariant(t *testing.T) {
	w := newPathWorld(t)
	ctx := context.Background()
	o := w.opts("say", "", nil)
	o.Variant = "ja=Kyoko"
	p1, err := New(o).Synth(ctx, "ci、終わったよ", "tapi", "ja")
	if err != nil {
		t.Fatal(err)
	}
	o.Variant = "ja=Otoya"
	p2, err := New(o).Synth(ctx, "ci、終わったよ", "tapi", "ja")
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatal("another [voice.say] voice was served from the cache")
	}
}

// At most MaxSynth synth calls run at once through the engine.
func TestEngineLimitsParallelSynth(t *testing.T) {
	w := newPathWorld(t)
	e := New(w.opts("pair", "", map[string]string{"pair": fakeBackend(t, t.TempDir(), "fake-pair")}))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.Synth(context.Background(), "x"+string(rune('a'+i)), ReaderVoice, "ja")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if cap(e.sem) != 2 {
		t.Fatalf("limit %d", cap(e.sem))
	}
}

func TestCleanTmp(t *testing.T) {
	w := newPathWorld(t)
	now := time.Now()
	o := w.opts("say", "", nil)
	o.Now = func() time.Time { return now }
	e := New(o)
	base := filepath.Join(w.data, "voice", "tmp")
	os.MkdirAll(filepath.Join(base, "old"), 0o700)
	os.MkdirAll(filepath.Join(base, "new"), 0o700)
	os.Chtimes(filepath.Join(base, "old"), now.Add(-time.Hour), now.Add(-time.Hour))
	e.CleanTmp(10 * time.Minute)
	if _, err := os.Stat(filepath.Join(base, "old")); !os.IsNotExist(err) {
		t.Error("old left")
	}
	if _, err := os.Stat(filepath.Join(base, "new")); err != nil {
		t.Error("new removed")
	}
}

// The speech queue (internal/voice) takes a synthesizer of this shape and a
// failover function; the daemon hands it an Engine. Kept here so a change on
// either side shows up at compile time without importing internal/voice.
var _ interface {
	Synth(ctx context.Context, text, voice, lang string) (wavPath string, err error)
	Failover() bool
} = (*Engine)(nil)
