package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/voice"
	"pekoe/internal/voice/backend"
	"pekoe/internal/voice/voicetest"
)

// fakeVoiceEngine stands for backend.Engine: synth goes to a voicetest
// fake, nothing is run. It counts what pekoed asked of it.
type fakeVoiceEngine struct {
	syn *voicetest.Synth
	// fb, when set, is the fallback FallbackFor offers ("vv" failed, "say")
	fb *voicetest.Synth

	mu         sync.Mutex
	opts       backend.Options
	synths     []string
	selects    int
	failovers  int
	failoverOK bool
	released   []string
	maintained int
	listed     int
}

func (f *fakeVoiceEngine) Synth(ctx context.Context, text, v, lang string) (string, error) {
	f.mu.Lock()
	f.synths = append(f.synths, text)
	f.mu.Unlock()
	return f.syn.Synth(ctx, text, v, lang)
}

func (f *fakeVoiceEngine) Select(context.Context) error {
	f.mu.Lock()
	f.selects++
	f.mu.Unlock()
	return nil
}

func (f *fakeVoiceEngine) Failover() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failovers++
	return f.failoverOK
}

func (f *fakeVoiceEngine) CacheKeyTime() time.Duration { return 0 }

func (f *fakeVoiceEngine) FallbackFor(error) (string, string, func(context.Context, string, string, string) (string, error)) {
	if f.fb == nil {
		return "", "", nil
	}
	return "vv", "say", f.fb.Synth
}

func (f *fakeVoiceEngine) Release(p string) {
	f.mu.Lock()
	f.released = append(f.released, p)
	f.mu.Unlock()
}

func (f *fakeVoiceEngine) Status() backend.Status {
	return backend.Status{Backend: "say", Using: "say"}
}

func (f *fakeVoiceEngine) List(context.Context, string) ([]backend.Info, []backend.Candidate) {
	f.mu.Lock()
	f.listed++
	f.mu.Unlock()
	return []backend.Info{{Name: "say", Using: true, Check: "ok"}}, nil
}

func (f *fakeVoiceEngine) Maintain() {
	f.mu.Lock()
	f.maintained++
	f.mu.Unlock()
}

func (f *fakeVoiceEngine) count() (synths, selects, failovers, listed, maintained int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.synths), f.selects, f.failovers, f.listed, f.maintained
}

// countingPlayer is a player that only counts (the disabled tests).
type countingPlayer struct {
	mu sync.Mutex
	n  int
}

func (p *countingPlayer) Play(context.Context, string) error {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
	return nil
}

// voiceWorld is a pekoed whose voice runs on fakes: a clock that moves on
// Advance, a synth and a player the test finishes.
type voiceWorld struct {
	t       *testing.T
	d       *Daemon
	repo    string
	clk     *voicetest.Clock
	syn     *voicetest.Synth
	pl      *voicetest.Player
	eng     *fakeVoiceEngine
	engines int              // engines made
	fb      *voicetest.Synth // the fallback the engines offer (nil: none)
}

func newVoiceWorld(t *testing.T) *voiceWorld {
	t.Helper()
	d, _, repo := setup(t)
	w := &voiceWorld{t: t, d: d, repo: repo, clk: voicetest.NewClock(), syn: voicetest.NewSynth(), pl: voicetest.NewPlayer()}
	h := d.voice
	h.clock, h.player, h.supported, h.noWarm = w.clk, w.pl, true, true
	h.getenv = func(string) string { return "" }
	h.newEngine = func(o backend.Options) voiceEngine {
		w.engines++
		w.eng = &fakeVoiceEngine{syn: w.syn, fb: w.fb, opts: o}
		return w.eng
	}
	t.Cleanup(h.close)
	return w
}

// config writes config.toml and has pekoed take it up.
func (w *voiceWorld) config(toml string) {
	w.t.Helper()
	os.MkdirAll(filepath.Dir(paths.GlobalConfig()), 0o700)
	if err := os.WriteFile(paths.GlobalConfig(), []byte(toml), 0o600); err != nil {
		w.t.Fatal(err)
	}
	w.d.reloadConfig()
	w.settle()
}

// settle waits until what came in and went out so far has been handled.
func (w *voiceWorld) settle() {
	w.d.voice.in.flush()
	w.d.voice.out.flush()
}

func (w *voiceWorld) add(slug string, st state.State) {
	w.t.Helper()
	w.d.add(&state.Session{Slug: slug, Branch: slug, Repo: "app", RepoDir: w.repo, Path: w.repo, State: st, StateSince: time.Now(), CreatedAt: time.Now()})
	w.settle()
}

func (w *voiceWorld) set(slug string, st state.State) {
	w.t.Helper()
	if _, err := w.d.update(slug, func(s *state.Session) { s.State = st }); err != nil {
		w.t.Fatal(err)
	}
	w.settle()
}

// advance moves the clock and lets what it set off settle.
func (w *voiceWorld) advance(d time.Duration) {
	w.clk.Advance(d)
	w.settle()
}

// logs are voice.log's entries, oldest first.
func (w *voiceWorld) logs() []voice.LogEntry {
	w.settle()
	es := w.d.voice.readLog(1000)
	for i, j := 0, len(es)-1; i < j; i, j = i+1, j-1 {
		es[i], es[j] = es[j], es[i]
	}
	return es
}

func (w *voiceWorld) logLine() string {
	var out []string
	for _, e := range w.logs() {
		out = append(out, e.Slug+" "+string(e.Kind)+" "+string(e.Result)+" "+e.Text)
	}
	return strings.Join(out, "\n")
}

// waitLog waits for voice.log to hold want (what comes back from goroutines
// of the queue's lands there a moment later).
func (w *voiceWorld) waitLog(want string) {
	w.t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if got := w.logLine(); strings.Contains(got, want) {
			return
		} else if time.Now().After(end) {
			w.t.Fatalf("voice.log has no %q:\n%s", want, got)
		}
	}
}

func (w *voiceWorld) status() VoiceStatus {
	w.t.Helper()
	w.settle()
	return call[VoiceStatus](w.t, w.d, "voice.status", nil)
}

const voiceOn = "[voice]\nenable = true\nlang = \"en\"\n"

// Two sessions waiting within the gather delay are said in one sentence
// (Q3, Q4) once the state changes reach pekoed through update; the second
// is logged as merged; the wav goes back to the engine once played.
func TestVoiceUpdateGathersSameKind(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-csv-export", state.Running)
	w.add("fix-ci", state.Running)
	w.config(voiceOn)
	if w.engines != 1 {
		t.Fatalf("engines made: %d", w.engines)
	}
	w.set("feat-csv-export", state.Waiting)
	w.advance(100 * time.Millisecond)
	w.set("fix-ci", state.Waiting)
	w.advance(200 * time.Millisecond) // the first one's 300ms
	c := w.syn.Next(t)
	if c.Text != "csv export and ci need you" || c.Voice != "tapi" || c.Lang != "en" {
		t.Fatalf("synth %+v", c)
	}
	c.OK("/data/voice/cache/a.wav")
	p := w.pl.Next(t)
	if p.Wav != "/data/voice/cache/a.wav" {
		t.Fatalf("played %s", p.Wav)
	}
	if st := w.status(); st.Speaking == nil || st.Speaking.Text != "csv export and ci need you" || st.Using != "say" || !st.Enabled {
		t.Fatalf("status while speaking: %+v", st)
	}
	p.Finish()
	w.waitLog("fix-ci wait merged csv export and ci need you")
	if got := w.logLine(); !strings.HasPrefix(got, "feat-csv-export wait spoken csv export and ci need you") {
		t.Fatalf("log:\n%s", got)
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		w.eng.mu.Lock()
		rel := append([]string(nil), w.eng.released...)
		w.eng.mu.Unlock()
		if len(rel) == 1 && rel[0] == "/data/voice/cache/a.wav" {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("released %v (Config.Release not wired to Engine.Release)", rel)
		}
	}
	fi, err := os.Stat(paths.VoiceLog())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("voice.log: %v %v", fi, err)
	}
}

// A state that changed again before it was said is stale (Q5): nothing is
// synthesized.
func TestVoiceUpdateDropsStale(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	w.config(voiceOn)
	w.set("feat-a", state.Waiting)
	w.advance(100 * time.Millisecond)
	w.set("feat-a", state.Running) // answered already
	w.advance(300 * time.Millisecond)
	w.waitLog("feat-a wait stale a needs you")
	if n, _, _, _, _ := w.eng.count(); n != 0 {
		t.Fatalf("a stale item was synthesized (%d)", n)
	}
}

// An item that waited 30 seconds behind a long sentence is old (Q6) and not
// said; done is said for running → done only.
func TestVoiceUpdateDropsOld(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	w.add("feat-b", state.Running)
	w.config(voiceOn)
	w.set("feat-a", state.Failed)
	w.advance(300 * time.Millisecond)
	w.syn.Next(t).OK("a.wav")
	p := w.pl.Next(t) // a long sentence: not finished
	w.set("feat-b", state.Done)
	w.advance(300 * time.Millisecond)
	c := w.syn.Next(t) // the next one is made while a plays (Q10)
	if c.Text != "b is done" {
		t.Fatalf("synth %q", c.Text)
	}
	c.OK("b.wav")
	w.advance(31 * time.Second)
	w.waitLog("feat-b done old b is done")
	p.Finish()
	if got := w.logLine(); !strings.Contains(got, "feat-a error spoken a hit an error") {
		t.Fatalf("log:\n%s", got)
	}
}

// [voice] enable = false (the default): whatever happens, no engine is
// made (no backend resolved or run) and nothing plays.
func TestVoiceOffRunsNothing(t *testing.T) {
	d, _, repo := setup(t)
	pl := &countingPlayer{}
	engines := 0
	h := d.voice
	h.clock, h.player, h.supported = voicetest.NewClock(), pl, true
	h.newEngine = func(o backend.Options) voiceEngine { engines++; return newRealVoiceEngine(o) }
	t.Cleanup(h.close)
	os.MkdirAll(filepath.Dir(paths.GlobalConfig()), 0o700)
	for _, toml := range []string{"", "[voice]\nenable = false\nbackend = \"say\"\n", "[voice]\nlang = \"ja\"\n"} {
		os.WriteFile(paths.GlobalConfig(), []byte(toml), 0o600)
		d.reloadConfig()
	}
	h.apply(d.cfg())
	d.add(&state.Session{Slug: "feat-a", Branch: "a", Repo: "app", RepoDir: repo, Path: repo, State: state.Running, CreatedAt: time.Now()})
	for _, st := range []state.State{state.Waiting, state.Running, state.Done, state.Failed} {
		d.update("feat-a", func(s *state.Session) { s.State = st })
	}
	event(t, d, "UserPromptSubmit", "feat-a", map[string]any{"prompt": "x"})
	call[any](t, d, "voice.focus", map[string]any{"slug": "feat-a", "term_focused": true})
	call[any](t, d, "voice.mute", map[string]any{"for": "1m"})
	call[any](t, d, "voice.mute", map[string]any{"off": true})
	call[any](t, d, "voice.stop", nil)
	call[any](t, d, "voice.log", nil)
	bs := call[VoiceBackends](t, d, "voice.backends", nil)
	if _, err := d.Call("voice.say", json.RawMessage(`{"text":"hello"}`)); err == nil || !strings.Contains(err.Error(), "voice is off") {
		t.Fatalf("voice.say while off: %v", err)
	}
	d.forget("feat-a")
	h.in.flush()
	h.out.flush()
	if engines != 0 || pl.n != 0 {
		t.Fatalf("voice off: %d engines made, %d plays", engines, pl.n)
	}
	if len(bs.Backends) != 1 || bs.Backends[0].Name != "say" || !strings.Contains(bs.Note, "voice is off") || bs.Backends[0].Version != "" {
		t.Fatalf("voice.backends while off: %+v", bs)
	}
	if st := call[VoiceStatus](t, d, "voice.status", nil); st.Enabled || st.Using != "" || st.Focus != "feat-a" {
		t.Fatalf("status: %+v", st)
	}
	if _, err := os.Stat(paths.VoiceLog()); !os.IsNotExist(err) {
		t.Fatalf("voice.log written while off: %v", err)
	}
}

// voice.backends (pekoe voice backends) lists the pekoe-voice-* in
// pekoed's PATH as candidates, with the line to write, and never starts
// them; while off the [voice.backends] entries are not run either.
func TestVoiceBackendsListsPathCandidates(t *testing.T) {
	d, _, _ := setup(t)
	h := d.voice
	h.supported = true
	t.Cleanup(h.close)
	bin, marks := t.TempDir(), t.TempDir()
	for _, n := range []string{"pekoe-voice-say", "pekoe-voice-voicevox"} {
		script := "#!/bin/sh\necho ran > '" + filepath.Join(marks, n) + "'\n"
		if err := os.WriteFile(filepath.Join(bin, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	os.MkdirAll(filepath.Dir(paths.GlobalConfig()), 0o700)
	os.WriteFile(paths.GlobalConfig(), []byte("[voice.backends]\nmine = \""+filepath.Join(bin, "pekoe-voice-voicevox")+"\"\n"), 0o600)
	d.reloadConfig()
	bs := call[VoiceBackends](t, d, "voice.backends", nil)
	got := map[string]backend.Candidate{}
	for _, c := range bs.Candidates {
		got[c.Name] = c
	}
	vv := filepath.Join(bin, "pekoe-voice-voicevox")
	if got["voicevox"].Path != vv || got["voicevox"].Line != `voicevox = "`+vv+`"` || got["say"].Path != filepath.Join(bin, "pekoe-voice-say") {
		t.Fatalf("candidates: %+v", bs.Candidates)
	}
	if len(bs.Backends) != 2 || bs.Backends[1].Name != "mine" || bs.Backends[1].Path != vv || bs.Backends[1].Version != "" {
		t.Fatalf("backends: %+v", bs.Backends)
	}
	if ents, _ := os.ReadDir(marks); len(ents) > 0 {
		t.Fatalf("voice.backends ran %d of them", len(ents))
	}
}

// Off another OS: enable = true does nothing either.
func TestVoiceUnsupportedRunsNothing(t *testing.T) {
	w := newVoiceWorld(t)
	w.d.voice.supported = false
	w.config(voiceOn)
	if w.engines != 0 {
		t.Fatal("an engine on an unsupported OS")
	}
	if st := w.status(); st.Supported || !st.Enabled {
		t.Fatalf("status: %+v", st)
	}
	if _, err := w.d.Call("voice.say", json.RawMessage(`{"text":"x"}`)); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("voice.say: %v", err)
	}
}

// Turning voice off stops the sound and the queue; on again takes the
// states as they are without saying anything (4.2).
func TestVoiceOnOffBaseline(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	w.config(voiceOn)
	w.set("feat-a", state.Waiting)
	w.advance(300 * time.Millisecond)
	w.syn.Next(t).OK("a.wav")
	p := w.pl.Next(t)
	w.config("[voice]\nenable = false\n")
	p.WaitStopped(t)
	if st := w.status(); st.Enabled || st.Speaking != nil {
		t.Fatalf("off: %+v", st)
	}
	// while off, a change is not even looked at
	w.set("feat-a", state.Done)
	w.config(voiceOn)
	if w.engines != 2 {
		t.Fatalf("engines: %d", w.engines)
	}
	w.advance(time.Minute)
	if n, _, _, _, _ := w.eng.count(); n != 0 {
		t.Fatal("something was said about the states voice found when turned on")
	}
	// a change of a setting rebuilds it (the phrases)
	w.config(voiceOn + "[voice.phrases]\ndone = \"{name} finished\"\n")
	if w.engines != 3 {
		t.Fatalf("engines after a settings change: %d", w.engines)
	}
	w.set("feat-a", state.Running)
	w.set("feat-a", state.Done)
	w.advance(300 * time.Millisecond)
	if c := w.syn.Next(t); c.Text != "a finished" {
		t.Fatalf("synth %q", c.Text)
	}
}

// The session the TUI shows in a focused terminal says no wait (D2); with
// the terminal not focused it does. A connection that goes takes its focus.
func TestVoiceFocusedSessionQuiet(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	w.config(voiceOn)
	c := dialPipe(t, w.d)
	if e := c.call("voice.focus", map[string]any{"slug": "feat-a", "term_focused": true}, w.d.humanToken); e != "" {
		t.Fatal(e)
	}
	w.settle()
	if st := w.status(); st.Focus != "feat-a" {
		t.Fatalf("focus %q", st.Focus)
	}
	w.set("feat-a", state.Waiting)
	w.advance(time.Second)
	if n, _, _, _, _ := w.eng.count(); n != 0 {
		t.Fatal("the session in front was narrated")
	}
	if e := c.call("voice.focus", map[string]any{"slug": "feat-a", "term_focused": false}, w.d.humanToken); e != "" {
		t.Fatal(e)
	}
	w.settle()
	w.set("feat-a", state.Running)
	w.set("feat-a", state.Waiting)
	w.advance(300 * time.Millisecond)
	if got := w.syn.Next(t).Text; got != "a needs you" {
		t.Fatalf("synth %q", got)
	}
	c.nc.Close()
	for end := time.Now().Add(5 * time.Second); w.status().Focus != ""; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the focus of a closed connection stays")
		}
	}
}

// voice.mute stops the sound and empties the queue (Q8), is kept in
// state.db across a restart of voice, and unmute lets speech through again.
func TestVoiceMute(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	w.add("feat-b", state.Running)
	w.config(voiceOn)
	w.set("feat-a", state.Waiting)
	w.advance(300 * time.Millisecond)
	w.syn.Next(t).OK("a.wav")
	p := w.pl.Next(t)
	w.set("feat-b", state.Done)
	st := call[VoiceStatus](t, w.d, "voice.mute", map[string]any{"for": "30m"})
	p.WaitStopped(t)
	want := w.clk.Now().Add(30 * time.Minute).Format(time.RFC3339)
	if st.MutedUntil != want || w.d.DB.Get(kvVoiceMute) != want {
		t.Fatalf("muted_until %q, kv %q, want %q", st.MutedUntil, w.d.DB.Get(kvVoiceMute), want)
	}
	w.waitLog("feat-b done muted")
	// kept: voice made again (a pekoed restart) is still muted
	w.config(voiceOn + "fallback = \"\"\n")
	if st := w.status(); st.MutedUntil != want {
		t.Fatalf("after a rebuild: %+v", st)
	}
	w.set("feat-a", state.Running)
	w.set("feat-a", state.Done)
	w.advance(time.Second)
	if n, _, _, _, _ := w.eng.count(); n != 0 {
		t.Fatal("said while muted")
	}
	call[VoiceStatus](t, w.d, "voice.mute", map[string]any{}) // till unmute
	if w.d.DB.Get(kvVoiceMute) != voiceMuteForever || w.status().MutedUntil != voiceMuteForever {
		t.Fatal("mute without for is not forever")
	}
	if st := call[VoiceStatus](t, w.d, "voice.mute", map[string]any{"off": true}); st.MutedUntil != "" || w.d.DB.Get(kvVoiceMute) != "" {
		t.Fatalf("unmute: %+v", st)
	}
	w.set("feat-a", state.Running)
	w.set("feat-a", state.Waiting)
	w.advance(300 * time.Millisecond)
	if got := w.syn.Next(t).Text; got != "a needs you" {
		t.Fatalf("after unmute: %q", got)
	}
	for _, bad := range []map[string]any{{"for": "soon"}, {"for": "-1m"}, {"until": "tomorrow"}, {"until": "2000-01-01T00:00:00Z"}} {
		b, _ := json.Marshal(bad)
		if _, err := w.d.Call("voice.mute", b); err == nil {
			t.Errorf("voice.mute %s: no error", b)
		}
	}
}

// voice.say (pekoe voice test) is said even while muted, with the voice
// and language asked for; voice.stop stops it.
func TestVoiceSayAndStop(t *testing.T) {
	w := newVoiceWorld(t)
	w.config(voiceOn)
	call[VoiceStatus](t, w.d, "voice.mute", map[string]any{})
	call[VoiceStatus](t, w.d, "voice.say", map[string]any{"text": " テスト\n", "voice": "reader", "lang": "ja"})
	c := w.syn.Next(t)
	if c.Text != "テスト" || c.Voice != "reader" || c.Lang != "ja" {
		t.Fatalf("synth %+v", c)
	}
	c.OK("t.wav")
	p := w.pl.Next(t)
	call[any](t, w.d, "voice.stop", nil)
	p.WaitStopped(t)
	w.waitLog(" test spoken テスト")
	for _, bad := range []string{`{"text":""}`, `{"text":"x","voice":"bob"}`, `{"text":"x","lang":"fr"}`, `{"text":"` + strings.Repeat("a", backend.MaxText+1) + `"}`} {
		if _, err := w.d.Call("voice.say", json.RawMessage(bad)); err == nil {
			t.Errorf("voice.say %.40s: no error", bad)
		}
	}
}

// Three synth failures in a row call the engine's Failover (Q11); when it
// cannot switch, synthesis pauses and voice.status says why and till when.
func TestVoiceFailoverAndPause(t *testing.T) {
	w := newVoiceWorld(t)
	for _, s := range []string{"feat-a", "feat-b", "feat-c"} {
		w.add(s, state.Running)
	}
	w.config(voiceOn)
	for i, s := range []string{"feat-a", "feat-b", "feat-c"} {
		w.set(s, state.Failed)
		w.advance(300 * time.Millisecond)
		w.syn.Next(t).Fail(errors.New("engine down\nmore"))
		if i < 2 {
			w.waitLog(s + " error failed")
		}
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		st := w.status()
		if st.PausedUntil != "" {
			if _, _, f, _, _ := w.eng.count(); f != 1 {
				t.Fatalf("Failover called %d times", f)
			}
			if !strings.Contains(st.Error, "engine down") || st.PausedUntil != w.clk.Now().Add(voice.PauseAfterFail).Format(time.RFC3339) {
				t.Fatalf("paused status: %+v", st)
			}
			break
		}
		if time.Now().After(end) {
			t.Fatalf("not paused: %+v", st)
		}
	}
	if e := w.logs()[0]; e.Reason != "engine down" || e.Backend != "say" {
		t.Fatalf("log keeps %+v", e)
	}
}

// A failed synth is tried once more with the fallback the engine offers
// (10.5): the utterance sounds, voice.log names the fallback and why, and
// voice.status shows the failure before any switch.
func TestVoiceRetryWithFallback(t *testing.T) {
	w := newVoiceWorld(t)
	w.fb = voicetest.NewSynth()
	w.add("feat-a", state.Running)
	w.config(voiceOn)
	w.set("feat-a", state.Failed)
	w.advance(300 * time.Millisecond)
	c := w.syn.Next(t)
	c.Fail(errors.New("connection refused\nmore"))
	f := w.fb.Next(t)
	if f.Text != c.Text {
		t.Fatalf("fallback synth %q, want %q", f.Text, c.Text)
	}
	if st := w.status(); !strings.Contains(st.Error, "vv failed: connection refused") || st.PausedUntil != "" {
		t.Fatalf("status after the first failure: %+v", st)
	}
	f.OK("/fake/a.wav")
	p := w.pl.Next(t)
	if p.Wav != "/fake/a.wav" {
		t.Fatalf("played %q", p.Wav)
	}
	p.Finish()
	w.waitLog("feat-a error spoken")
	if e := w.logs()[0]; e.Backend != "say" || e.Reason != "vv failed: connection refused" {
		t.Fatalf("log keeps %+v", e)
	}
	if _, _, fo, _, _ := w.eng.count(); fo != 0 {
		t.Fatalf("Failover after one failure: %d", fo)
	}
}

// A removed session's queued items go (session.removed); tapi chat's wait
// and an agent's question in the inbox are said too (D3).
func TestVoiceRemovedTapiAndAsk(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	w.config(voiceOn)
	w.set("feat-a", state.Waiting)
	w.d.forget("feat-a")
	w.d.emit("session.removed", &state.Session{Slug: "feat-a"})
	w.settle()
	w.advance(300 * time.Millisecond)
	w.waitLog("feat-a wait stale")

	w.d.mu.Lock()
	w.d.tapi.state, w.d.tapi.since = state.Waiting, time.Now()
	w.d.mu.Unlock()
	w.d.emit("tapi.updated", w.d.tapiView())
	w.settle()
	w.advance(300 * time.Millisecond)
	c := w.syn.Next(t)
	if c.Text != "tapi needs you" {
		t.Fatalf("synth %q", c.Text)
	}
	c.OK("t.wav")
	w.pl.Next(t).Finish()

	w.add("feat-q", state.Running)
	ask(t, w.d, "feat-q", map[string]any{"question": "which way?", "kind": "decision"})
	w.settle()
	w.advance(300 * time.Millisecond)
	if got := w.syn.Next(t).Text; got != "q has a question" {
		t.Fatalf("synth %q", got)
	}
}

// MessageDisplay is taken and nothing answered (read takes its text,
// voiceread_test.go); the hook changes no state.
func TestVoiceMessageDisplayTaken(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	res, err := w.d.Call("session.event", json.RawMessage(`{"hook":"MessageDisplay","session":"feat-a","payload":{"delta":"hi","final":true}}`))
	if err != nil || res != nil {
		t.Fatalf("MessageDisplay: %v %v", res, err)
	}
	if s := w.d.sessions["feat-a"]; s.State != state.Running {
		t.Fatalf("state %s", s.State)
	}
}

// Every voice.* needs the human token over the socket.
func TestVoiceMethodsNeedToken(t *testing.T) {
	w := newVoiceWorld(t)
	w.config(voiceOn)
	c := dialPipe(t, w.d)
	for _, m := range []string{"voice.status", "voice.mute", "voice.mode", "voice.stop", "voice.say", "voice.focus", "voice.log", "voice.backends"} {
		params := map[string]any{"text": "hi", "slug": "x", "for": "1m", "mode": "mute"}
		if e := c.call(m, params, ""); !strings.Contains(e, "only a person can do this") {
			t.Errorf("%s without the token: %q", m, e)
		}
	}
	if w.d.DB.Get(kvVoiceMute) != "" {
		t.Fatal("voice.mute ran without the token")
	}
	if e := c.call("voice.status", nil, w.d.humanToken); e != "" {
		t.Fatal(e)
	}
}

// The engine gets [voice] as written: backend, fallback, [voice.backends],
// the data directory, [voice.say] in the cache variant. voice.backends
// asks it while on.
func TestVoiceEngineOptions(t *testing.T) {
	w := newVoiceWorld(t)
	w.config("[voice]\nenable = true\nbackend = \"vv\"\nfallback = \"\"\n[voice.backends]\nvv = \"/opt/vv\"\n[voice.say]\nja = \"Otoya\"\n")
	o := w.eng.opts
	if o.Backend != "vv" || o.Fallback != "" || o.Backends["vv"] != "/opt/vv" || o.Dir != paths.DataDir() || !strings.Contains(o.Variant, "Otoya") {
		t.Fatalf("options %+v", o)
	}
	bs := call[VoiceBackends](t, w.d, "voice.backends", nil)
	if _, _, _, listed, _ := w.eng.count(); listed != 1 || len(bs.Backends) != 1 || bs.Note != "" {
		t.Fatalf("voice.backends: %+v", bs)
	}
}

// Turning voice on synthesizes ahead, one at a time, each session's
// one-name sentences and idle_all; a new session's follow (docs/voice.md 9).
func TestVoiceWarmsAhead(t *testing.T) {
	w := newVoiceWorld(t)
	w.d.voice.noWarm = false
	w.add("feat-a", state.Running)
	w.config(voiceOn)
	take := func(n int) map[string]bool {
		got := map[string]bool{}
		for range n {
			c := w.syn.Next(t)
			if c.Voice != "tapi" || c.Lang != "en" {
				t.Fatalf("warm synth %+v", c)
			}
			got[c.Text] = true
			c.OK("/cache/" + c.Text + ".wav")
		}
		return got
	}
	got := take(5)
	for _, want := range []string{"a needs you", "a is done", "a hit an error", "tapi needs you", "everyone's idle"} {
		if !got[want] {
			t.Errorf("not made ahead: %q (made %v)", want, got)
		}
	}
	w.add("fix-b", state.Running)
	got = take(3)
	for _, want := range []string{"b needs you", "b is done", "b hit an error"} {
		if !got[want] {
			t.Errorf("new session: not made ahead: %q (made %v)", want, got)
		}
	}
	if got := w.logLine(); got != "" {
		t.Fatalf("made-ahead sentences went through the queue:\n%s", got)
	}
}

// voice.mode (pekoe voice mode, rule 7.2): kept in state.db as
// voice.mode.<slug> and in voice.status; auto deletes it; an unknown mode
// or session is refused; it is taken while voice is off too.
func TestVoiceModeKept(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	st := call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": "feat-a", "mode": "mute"})
	if st.Modes["feat-a"] != voice.ModeMute || w.d.DB.Get(kvVoiceMode+"feat-a") != "mute" {
		t.Fatalf("while off: modes %v, kv %q", st.Modes, w.d.DB.Get(kvVoiceMode+"feat-a"))
	}
	call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": TapiSlug, "mode": "read"})
	w.config(voiceOn)
	if st := w.status(); len(st.Modes) != 2 || st.Modes["feat-a"] != voice.ModeMute || st.Modes[TapiSlug] != voice.ModeRead {
		t.Fatalf("modes %v", st.Modes)
	}
	st = call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": TapiSlug, "mode": "auto"})
	if _, ok := st.Modes[TapiSlug]; ok || w.d.DB.Get(kvVoiceMode+TapiSlug) != "" {
		t.Fatalf("auto: modes %v, kv %q", st.Modes, w.d.DB.Get(kvVoiceMode+TapiSlug))
	}
	for _, bad := range []string{`{"slug":"feat-a","mode":"loud"}`, `{"slug":"feat-a","mode":""}`, `{"slug":"nope","mode":"read"}`, `{"slug":"","mode":"read"}`} {
		if _, err := w.d.Call("voice.mode", json.RawMessage(bad)); err == nil {
			t.Errorf("voice.mode %s: no error", bad)
		}
	}
	if w.d.DB.Get(kvVoiceMode+"nope") != "" || w.d.DB.Get(kvVoiceMode) != "" {
		t.Fatal("a refused voice.mode was kept")
	}

	// a pekoed restart: a new voice reads state.db again
	h := newVoiceHub(w.d)
	if m := h.modeFor("feat-a"); m != voice.ModeMute {
		t.Fatalf("after a restart: %s", m)
	}
	if m := h.modeFor(TapiSlug); m != voice.ModeNarrate {
		t.Fatalf("auto after a restart: %s", m)
	}
	if st := h.status(); len(st.Modes) != 1 || st.Modes["feat-a"] != voice.ModeMute {
		t.Fatalf("status after a restart: %v", st.Modes)
	}
}

// A session's own mode decides over focus and background (7.2), when it is
// queued and again before it is said; the global mute beats it (7.1); auto
// goes back to the default.
func TestVoiceModeDecides(t *testing.T) {
	w := newVoiceWorld(t)
	w.add("feat-a", state.Running)
	w.add("feat-b", state.Running)
	w.config(voiceOn)
	call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": "feat-a", "mode": "mute"})
	w.set("feat-a", state.Waiting)
	w.advance(time.Second)
	w.waitLog("feat-a wait muted")
	if n, _, _, _, _ := w.eng.count(); n != 0 {
		t.Fatal("a session set to mute was said")
	}

	// auto: the default (background narrate) again
	call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": "feat-a", "mode": "auto"})
	w.set("feat-a", state.Running)
	w.set("feat-a", state.Waiting)
	w.advance(300 * time.Millisecond)
	c := w.syn.Next(t)
	if c.Text != "a needs you" {
		t.Fatalf("after auto: %q", c.Text)
	}
	c.OK("a.wav")
	p := w.pl.Next(t)

	// feat-b is queued (past the gather delay) while feat-a sounds, then
	// set to mute: decided again before it is said, it is not
	w.set("feat-b", state.Waiting)
	w.advance(300 * time.Millisecond)
	if w.status().Queue+w.syn.Pending() == 0 {
		t.Fatal("feat-b was not queued")
	}
	call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": "feat-b", "mode": "mute"})
	if w.syn.Pending() > 0 { // synthesized ahead while feat-a sounds
		w.syn.Next(t).OK("b.wav")
	}
	p.Finish()
	w.advance(time.Second)
	w.waitLog("feat-b wait muted")
	if l := w.logLine(); strings.Contains(l, "feat-b wait spoken") {
		t.Fatalf("queued before mute, said after:\n%s", l)
	}

	// the global mute beats a session's own narrate
	call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": "feat-a", "mode": "narrate"})
	call[VoiceStatus](t, w.d, "voice.mute", map[string]any{})
	if m := w.d.voice.modeFor("feat-a"); m != voice.ModeMute {
		t.Fatalf("muted: %s", m)
	}
	call[VoiceStatus](t, w.d, "voice.mute", map[string]any{"off": true})
	if m := w.d.voice.modeFor("feat-a"); m != voice.ModeNarrate {
		t.Fatalf("unmuted: %s", m)
	}
}

// pekoe rm (session.removed) deletes the session's mode: a new session of
// the same slug starts with none.
func TestVoiceModeGoesWithRm(t *testing.T) {
	w := newVoiceWorld(t)
	s := newSession(t, w.d, w.repo, "feat/x")
	call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": s.Slug, "mode": "read"})
	if w.d.DB.Get(kvVoiceMode+s.Slug) != "read" {
		t.Fatal("not kept")
	}
	call[any](t, w.d, "worktree.rm", map[string]any{"slug": s.Slug, "force": true})
	w.settle()
	if v := w.d.DB.Get(kvVoiceMode + s.Slug); v != "" {
		t.Fatalf("kv after rm: %q", v)
	}
	if st := w.status(); len(st.Modes) != 0 {
		t.Fatalf("modes after rm: %v", st.Modes)
	}
	nw := newSession(t, w.d, w.repo, "feat/x")
	if nw.Slug != s.Slug || w.d.voice.modeFor(nw.Slug) != voice.ModeNarrate {
		t.Fatalf("new %s: %s", nw.Slug, w.d.voice.modeFor(nw.Slug))
	}
}

// tapi.updated comes from many places with no order among them, and a view
// is made after the lock is let go: one made before a change can come
// after it. A view older than the last taken (StateSince only moves on)
// is dropped: tapi, idle now, is not said to need you.
func TestVoiceTapiStaleView(t *testing.T) {
	w := newVoiceWorld(t)
	w.config(voiceOn)
	emit := func(st state.State, since time.Time) {
		v := w.d.tapiView()
		v.State, v.StateSince = st, since
		w.d.emit("tapi.updated", v)
		w.settle()
	}
	t0 := time.Now()
	emit(state.Running, t0)
	emit(state.Idle, t0.Add(2*time.Second))
	emit(state.Waiting, t0.Add(time.Second)) // made before the idle, sent after it
	w.advance(time.Second)
	time.Sleep(100 * time.Millisecond)
	w.settle()
	if w.syn.Pending() != 0 {
		t.Fatalf("said of a stale view: %q", w.syn.Next(t).Text)
	}
	// a newer wait is said
	emit(state.Waiting, t0.Add(3*time.Second))
	w.advance(300 * time.Millisecond)
	if c := w.syn.Next(t); c.Text != "tapi needs you" {
		t.Fatalf("synth %q", c.Text)
	}
}
