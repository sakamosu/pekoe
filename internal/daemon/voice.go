package daemon

// pekoe voice in pekoed (docs/voice.md 4): the state changes, the inbox,
// tapi chat and the TUI's focus go to internal/voice (a Tracker that says
// what is worth saying, a Queue that says it), which synthesizes with
// internal/voice/backend and plays with internal/voice/player.
//
// Off by default ([voice] enable = false). While off, nothing here runs: no
// backend is resolved or run, nothing plays, the events are not even
// queued; voice.focus and voice.mute are only remembered.
//
// Ordering and locks: what comes in (Daemon.update under evMu, emit) is
// handed to one goroutine in order (in) and the caller returns at once.
// What goes out (voice.log lines, voice.updated / voice.logged events) is
// handed to another (out): the Queue calls Log and OnUpdate with its lock
// held, so they must neither write files nor take pekoed's locks there.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"pekoe/internal/config"
	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/voice"
	"pekoe/internal/voice/backend"
	"pekoe/internal/voice/player"
)

// voice's kv key in state.db: the end of a global mute (RFC3339, or
// voiceMuteForever for "until unmute").
const (
	kvVoiceMute      = "voice.mute_until"
	voiceMuteForever = "forever"
	// kvVoiceMode + slug: the mode set with pekoe voice mode (7). No key is
	// auto; pekoe rm deletes it.
	kvVoiceMode = "voice.mode."
)

// voiceLogMax is the size of voice.log before it is rotated to voice.log.1
// (one generation, docs/voice.md 14.4).
const voiceLogMax = 1 << 20

// voiceMaintainEvery: the WAV cache and voice/tmp are looked at when voice
// turns on and this often after (docs/voice.md 9).
const voiceMaintainEvery = time.Hour

// voiceEngine is what pekoed uses of a backend.Engine (tests swap in a
// fake: nothing is run).
type voiceEngine interface {
	Synth(ctx context.Context, text, voice, lang string) (string, error)
	Select(ctx context.Context) error
	Failover() bool
	// FallbackFor offers the fallback for one more try of a failed synth
	// (backend.Engine.FallbackFor); synth nil is nothing to try.
	FallbackFor(err error) (failed, name string, synth func(ctx context.Context, text, voice, lang string) (string, error))
	// CacheKeyTime is added to a synth's time: an external backend runs
	// cache-key first within it (backend.Engine.CacheKeyTime).
	CacheKeyTime() time.Duration
	Release(path string)
	Status() backend.Status
	List(ctx context.Context, pathEnv string) ([]backend.Info, []backend.Candidate)
	// Maintain prunes the WAV cache and voice/tmp.
	Maintain()
}

type realVoiceEngine struct{ *backend.Engine }

func (e realVoiceEngine) Maintain() {
	if err := e.Cache().Prune(backend.MaxCache); err != nil {
		log.Printf("voice: cache: %v", err)
	}
	e.CleanTmp(voiceMaintainEvery)
}

func newRealVoiceEngine(o backend.Options) voiceEngine { return realVoiceEngine{backend.New(o)} }

// VoiceStatus is voice.status and the voice.updated event (docs/voice.md 13).
type VoiceStatus struct {
	Enabled   bool   `json:"enabled"`
	Supported bool   `json:"supported"` // macOS only
	Backend   string `json:"backend"`   // configured
	Using     string `json:"using"`     // in use (the fallback's name on the fallback), "" for none
	// Error is why nothing can be said: no backend can be used, or synthesis
	// is paused after repeated failures (Q11; PausedUntil says till when).
	Error       string `json:"error"`
	PausedUntil string `json:"paused_until,omitempty"`
	// BackendError is why the configured backend is not the one in use.
	BackendError string `json:"backend_error,omitempty"`
	MutedUntil   string `json:"muted_until"` // "", "forever" or RFC3339
	Focus        string `json:"focus"`
	// Modes are the modes set with pekoe voice mode, by slug (auto is left
	// out).
	Modes    map[string]voice.Mode `json:"modes"`
	Queue    int                   `json:"queue"`
	Speaking *voice.Speaking       `json:"speaking"`
}

type voiceFocus struct {
	slug        string
	termFocused bool
	at          uint64 // order of sending
}

// voiceHub is pekoed's voice. The zero value is not usable: newVoiceHub.
type voiceHub struct {
	d *Daemon

	// what it runs on; tests swap them before voiceApply
	clock     voice.Clock
	newEngine func(backend.Options) voiceEngine
	player    voice.Player
	getenv    func(string) string
	supported bool
	noWarm    bool // tests: no synthesis ahead of time

	in, out serial

	mu       sync.Mutex
	on       bool
	key      string // the settings the running parts were made with
	eng      voiceEngine
	q        *voice.Queue
	tr       *voice.Tracker
	rd       *voice.Reader // read's text (6.1-6.3)
	phrases  voice.Phrases
	stopRun  chan struct{} // closed when this run (on with these settings) ends
	tapiSt   state.State
	tapiAt   time.Time
	lastEmit string // the last voice.updated, to send only changes

	// policy, read by the Queue under its lock: its own lock
	polMu      sync.Mutex
	mutedUntil time.Time // zero: not muted
	mutedEver  bool      // muted until unmute
	focusMode  voice.Mode
	bgMode     voice.Mode
	modes      map[string]voice.Mode // pekoe voice mode, by slug (kv voice.mode.<slug>)
	focus      map[any]voiceFocus    // by connection
	focusSeq   uint64

	logMu sync.Mutex // voice.log

	warmMu   sync.Mutex
	warmed   map[string]bool
	warmTodo []string
	warmKick chan struct{}
	idle     chan struct{} // the queue went idle (warm waits for it)
}

func newVoiceHub(d *Daemon) *voiceHub {
	h := &voiceHub{
		d: d, clock: voice.RealClock{}, newEngine: newRealVoiceEngine, player: player.New(),
		getenv: os.Getenv, supported: runtime.GOOS == "darwin",
		focus: map[any]voiceFocus{}, warmed: map[string]bool{},
		warmKick: make(chan struct{}, 1), idle: make(chan struct{}, 1),
	}
	h.in.init()
	h.out.init()
	h.loadModes()
	return h
}

// serial runs functions one at a time in the order they were given, on a
// goroutine of its own; Do never blocks.
type serial struct {
	mu   sync.Mutex
	fs   []func()
	kick chan struct{}
	once sync.Once
}

func (s *serial) init() { s.kick = make(chan struct{}, 1) }

func (s *serial) Do(f func()) {
	s.once.Do(func() { go s.loop() })
	s.mu.Lock()
	s.fs = append(s.fs, f)
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *serial) loop() {
	for range s.kick {
		for {
			s.mu.Lock()
			if len(s.fs) == 0 {
				s.mu.Unlock()
				break
			}
			f := s.fs[0]
			s.fs = s.fs[1:]
			s.mu.Unlock()
			f()
		}
	}
}

// flush waits until what was given before it has run.
func (s *serial) flush() {
	done := make(chan struct{})
	s.Do(func() { close(done) })
	<-done
}

// ---- turning on and off ----

func (h *voiceHub) settingsKey(v config.Voice) string {
	b, _ := json.Marshal(struct {
		V  config.Voice
		FB string
	}{v, v.FallbackName()})
	return string(b)
}

// apply takes [voice] as config.toml has it now: turns voice on (or
// rebuilds it when its settings changed) or off. Called at start and after
// every reload of config.toml.
func (h *voiceHub) apply(g *config.Global) {
	v := g.Voice
	want := v.Enable && h.supported
	key := h.settingsKey(v)
	h.mu.Lock()
	if h.on == want && (!want || key == h.key) {
		h.mu.Unlock()
		return
	}
	old := h.q
	if h.stopRun != nil {
		close(h.stopRun)
		h.stopRun = nil
	}
	h.on, h.key, h.q, h.tr, h.rd, h.eng = false, "", nil, nil, nil, nil
	h.mu.Unlock()
	if old != nil {
		old.Close() // stops the sound, empties the queue
	}
	if !want {
		if old != nil {
			log.Printf("voice: off")
		}
		h.out.Do(h.emitStatus)
		return
	}
	h.start(v, key)
}

func (h *voiceHub) start(v config.Voice, key string) {
	lang := voice.ResolveLang(v.LangSetting(), h.getenv)
	phrases := voice.NewPhrases(lang, v.Phrases)
	eng := h.newEngine(backend.Options{
		Backend: v.BackendName(), Fallback: v.FallbackName(), Backends: v.Backends,
		Dir:     paths.DataDir(),
		Variant: "ja=" + v.Say.JA + "\x00en=" + v.Say.EN,
		Logf:    func(f string, a ...any) { log.Printf(f, a...) },
	})
	h.polMu.Lock()
	h.focusMode, h.bgMode = voice.Mode(v.Focus()), voice.Mode(v.Background())
	h.polMu.Unlock()
	h.loadMute()

	var tr *voice.Tracker
	q := voice.NewQueue(voice.Config{
		Clock: h.clock, Synth: eng, Player: h.player, Phrases: phrases,
		Mode:     h.modeFor,
		Fresh:    func(it voice.Item) bool { return tr.Fresh(it) },
		Failover: eng.Failover,
		Fallback: func(err error) (string, string, voice.Synth) {
			failed, name, f := eng.FallbackFor(err)
			if f == nil {
				return "", "", nil
			}
			return failed, name, voice.SynthFunc(f)
		},
		SynthExtra:   eng.CacheKeyTime,
		Logf:         log.Printf,
		Release:      eng.Release,
		Backend:      func() string { return eng.Status().Using },
		MaxSentences: v.MaxSentences(),
		Log:          func(e voice.LogEntry) { h.out.Do(func() { h.writeLog(e) }) },
		OnUpdate:     func(voice.Status) { h.out.Do(h.emitStatus) },
	})
	tr = voice.NewTracker(h.clock, func(it voice.Item) { q.Push(it) })
	skip := v.Read.Skip
	if len(skip) == 0 {
		skip = voice.DefaultSkip
	}
	rd := voice.NewReader(voice.ReadConfig{
		Clock: h.clock, Skip: skip, Lang: lang, Mode: h.modeFor, Unread: h.logUnread, Due: h.readDue,
		MaxSentences: v.MaxSentences(), MaxTurnSentences: v.MaxTurnSentences(),
	})
	stop := make(chan struct{})
	h.warmMu.Lock()
	h.warmed, h.warmTodo = map[string]bool{}, nil
	h.warmMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.on, h.key, h.eng, h.q, h.tr, h.rd, h.phrases, h.stopRun = true, key, eng, q, tr, rd, phrases, stop
	log.Printf("voice: on (backend %s, fallback %q, lang %s)", v.BackendName(), v.FallbackName(), lang)

	// the states now are the starting point: nothing is said about them.
	// Queued under mu with on: no change can be queued before it
	h.in.Do(func() {
		if !h.current(tr) {
			return
		}
		sessions := h.d.sessionList()
		ts := h.d.tapiView()
		h.mu.Lock()
		h.tapiSt, h.tapiAt = ts.State, ts.StateSince
		h.mu.Unlock()
		sessions = append(sessions, state.Session{Slug: TapiSlug, State: ts.State, StateSince: ts.StateSince})
		inbox, _ := h.d.DB.InboxList(state.InboxFilter{Status: []string{state.InboxOpen}})
		tr.Baseline(sessions, inbox)
		h.mu.Lock()
		f := h.focusLocked()
		h.mu.Unlock()
		tr.Focus(f.slug, f.termFocused)
		for _, s := range sessions {
			h.warmSession(s.Slug)
		}
		h.warm(phrases.Render(voice.KindIdleAll, nil))
	})
	go func() {
		// turning voice off (or rebuilding it) stops the check of the backend
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		if err := eng.Select(ctx); err != nil {
			log.Printf("voice: %v", err)
		}
		select {
		case <-stop:
			return
		default:
		}
		h.out.Do(h.emitStatus)
		eng.Maintain()
		t := time.NewTicker(voiceMaintainEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				eng.Maintain()
			}
		}
	}()
	if !h.noWarm {
		go h.warmLoop(stop, eng)
	}
	h.out.Do(h.emitStatus)
}

// current: tr is still the running Tracker (voice was not turned off or
// rebuilt meanwhile).
func (h *voiceHub) current(tr *voice.Tracker) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.on && h.tr == tr
}

// reader is the running Reader (nil when off), used on in after current
// said the Tracker is the running one.
func (h *voiceHub) reader() *voice.Reader {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rd
}

// readParts are the running Reader and Queue, nil when off.
func (h *voiceHub) readParts() (*voice.Reader, *voice.Queue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.on {
		return nil, nil
	}
	return h.rd, h.q
}

// parts are the running Tracker and Queue, nil when off.
func (h *voiceHub) parts() (*voice.Tracker, *voice.Queue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.on {
		return nil, nil
	}
	return h.tr, h.q
}

func (h *voiceHub) isOn() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.on
}

// close stops the sound for good (pekoed stops).
func (h *voiceHub) close() {
	h.mu.Lock()
	q := h.q
	if h.stopRun != nil {
		close(h.stopRun)
		h.stopRun = nil
	}
	h.on, h.q, h.tr, h.rd, h.eng = false, nil, nil, nil, nil
	h.mu.Unlock()
	if q != nil {
		q.Close()
	}
}

// ---- what comes in ----

// session: one state change, from Daemon.update under evMu (before nil for
// a new session).
func (h *voiceHub) session(before, after *state.Session) {
	tr, _ := h.parts()
	if tr == nil || after == nil {
		return
	}
	var b *state.Session
	if before != nil {
		c := *before
		b = &c
	}
	a := *after
	h.in.Do(func() {
		if h.current(tr) {
			tr.Session(b, &a)
			if b == nil {
				h.warmSession(a.Slug)
			}
		}
	})
}

func (h *voiceHub) removed(slug string) {
	tr, q := h.parts()
	if tr == nil {
		return
	}
	h.in.Do(func() {
		if h.current(tr) {
			tr.Removed(slug)
			q.Removed(slug)
			if rd := h.reader(); rd != nil {
				rd.Removed(slug)
			}
		}
	})
}

// inboxChanged: the inbox changed; the questions agents wait on are read
// again.
func (h *voiceHub) inboxChanged() {
	tr, _ := h.parts()
	if tr == nil {
		return
	}
	h.in.Do(func() {
		if !h.current(tr) {
			return
		}
		items, err := h.d.DB.InboxList(state.InboxFilter{Status: []string{state.InboxOpen}})
		if err != nil {
			return
		}
		tr.Inbox(items)
	})
}

// tapi: tapi chat's view went out (tapi.updated); a change of its state is
// a state change of the session +tapi.
func (h *voiceHub) tapi(st state.State, since time.Time) {
	tr, _ := h.parts()
	if tr == nil {
		return
	}
	h.in.Do(func() {
		if !h.current(tr) {
			return
		}
		h.mu.Lock()
		prev, prevAt := h.tapiSt, h.tapiAt
		if since.Before(prevAt) {
			// a view made before the one taken, sent after it:
			// tapi.updated has no order among its senders, and
			// StateSince only moves on
			h.mu.Unlock()
			return
		}
		h.tapiSt, h.tapiAt = st, since
		h.mu.Unlock()
		if prev == st {
			return
		}
		tr.Session(&state.Session{Slug: TapiSlug, State: prev, StateSince: prevAt}, &state.Session{Slug: TapiSlug, State: st, StateSince: since})
	})
}

// ---- read (docs/voice.md 6) ----

// readIn hands f the running Reader and Queue on in, in order; nothing
// while voice is off or after it was rebuilt meanwhile.
func (h *voiceHub) readIn(f func(*voice.Reader, *voice.Queue)) {
	rd, q := h.readParts()
	if rd == nil {
		return
	}
	h.in.Do(func() {
		h.mu.Lock()
		cur := h.on && h.rd == rd
		h.mu.Unlock()
		if cur {
			f(rd, q)
		}
	})
}

func pushAll(q *voice.Queue, items []voice.Item) {
	for _, it := range items {
		q.Push(it)
	}
}

// logUnread writes to pekoed.log why some of a session's reply was not
// read (voice.ReadConfig.Unread): a reply that goes unheard is otherwise
// in no log, since nothing of it reached the queue. A mode is told with
// the focus it came from.
func (h *voiceHub) logUnread(u voice.Unread) {
	why := u.Why
	if strings.HasPrefix(why, voice.UnreadMode) {
		h.polMu.Lock()
		f := h.focusLocked()
		h.polMu.Unlock()
		why += fmt.Sprintf(" (focus %q)", f.slug)
	}
	where := ""
	if u.Turn != "" {
		where = " in turn " + shortID(u.Turn)
	}
	if u.Message != "" {
		where += " message " + shortID(u.Message)
	}
	log.Printf("voice: read of %s: not read%s: %s", u.Slug, where, why)
}

// shortID is an id's first 8 characters (Claude Code's ids are UUIDs).
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// display: one MessageDisplay batch of the session (6.1, 6.2).
func (h *voiceHub) display(slug string, d voice.Display) {
	h.readIn(func(rd *voice.Reader, q *voice.Queue) { pushAll(q, rd.Display(slug, d)) })
}

// stopHook: the session's Stop, with last_assistant_message (6.1: read
// when no MessageDisplay of the turn came) and stop_hook_active (again: a
// Stop hook blocked the last Stop and the turn went on). The text may wait
// GapWait for the turn's batches (readDue).
func (h *voiceHub) stopHook(slug, text string, again bool) {
	h.readIn(func(rd *voice.Reader, q *voice.Queue) { pushAll(q, rd.Stop(slug, "", text, again)) })
}

// readDue: a Stop's text has waited GapWait (voice.ReadConfig.Due).
func (h *voiceHub) readDue(slug string) {
	h.readIn(func(rd *voice.Reader, q *voice.Queue) { pushAll(q, rd.Due(slug)) })
}

// userPrompt: the person typed into the session (Q8: its read stops).
// running is the session's hooks state before this prompt updates it: a
// prompt while running is typed ahead, and the turn goes unread up to its
// Stop (6.3).
func (h *voiceHub) userPrompt(slug string, running bool) {
	h.readIn(func(rd *voice.Reader, q *voice.Queue) {
		rd.UserPrompt(slug, running)
		q.UserPrompt(slug)
	})
}

// interrupted: an interrupt through pekoe (session.interrupt,
// tapi.interrupt, an Escape in session.keys / tapi.keys): the session's
// read stops and the turn is not read on (6.3).
func (h *voiceHub) interrupted(slug string) {
	h.readIn(func(rd *voice.Reader, q *voice.Queue) {
		rd.Interrupt(slug)
		q.StopRead(slug, "interrupted")
	})
}

// discardRead: the session's mode may no longer be read (pekoe voice
// mode); "" is every session (the focus moved, a mute). The messages begun
// are not read on (6.2).
func (h *voiceHub) discardRead(slug string) {
	h.readIn(func(rd *voice.Reader, _ *voice.Queue) {
		if slug == "" {
			rd.DiscardAll()
		} else {
			rd.Discard(slug)
		}
	})
}

// voiceInterrupted: an interrupt of slug's agent went through pekoe.
func (d *Daemon) voiceInterrupted(slug string) {
	if d.voice != nil && d.readsAgent(slug) {
		d.voice.interrupted(slug)
	}
}

// readsAgent: slug's agent is Claude Code, the one read follows (6.1;
// codex is a later stage).
func (d *Daemon) readsAgent(slug string) bool {
	g := d.cfg()
	name := g.Tapi.Agent
	if slug != TapiSlug {
		d.mu.Lock()
		s := d.sessions[slug]
		if s != nil {
			name = s.Agent
		}
		d.mu.Unlock()
		if s == nil {
			return false
		}
	}
	ag, ok := g.Agents[name]
	return ok && isClaude(ag.Command)
}

// readRunning: slug works by the hooks now (before a UserPromptSubmit
// updates it).
func (d *Daemon) readRunning(slug string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if slug == TapiSlug {
		return d.tapi.state == state.Running
	}
	s := d.sessions[slug]
	return s != nil && s.State == state.Running
}

// messageDisplayP is a MessageDisplay hook's input (docs/voice.md 3.2).
type messageDisplayP struct {
	TurnID    string `json:"turn_id"`
	MessageID string `json:"message_id"`
	Index     int    `json:"index"`
	Final     bool   `json:"final"`
	Delta     string `json:"delta"`
}

// voiceEmitted is emit's look at the events voice follows.
func (d *Daemon) voiceEmitted(ev string, data any) {
	if d.voice == nil {
		return
	}
	switch ev {
	case "session.created":
		if s, ok := data.(*state.Session); ok && s != nil {
			d.voice.session(nil, s)
		}
	case "session.removed":
		if s, ok := data.(*state.Session); ok && s != nil {
			d.voice.forgetMode(s.Slug)
			d.voice.removed(s.Slug)
		}
	case "inbox.updated":
		d.voice.inboxChanged()
	case "tapi.updated":
		if t, ok := data.(*Tapi); ok && t != nil {
			d.voice.tapi(t.State, t.StateSince)
		}
	}
}

// sessionList is a copy of every session.
func (d *Daemon) sessionList() []state.Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]state.Session, 0, len(d.sessions))
	for _, s := range d.sessions {
		out = append(out, *s)
	}
	return out
}

// ---- policy ----

func (h *voiceHub) modeFor(slug string) voice.Mode {
	h.polMu.Lock()
	defer h.polMu.Unlock()
	f := h.focusLocked()
	return voice.Policy{
		Muted: h.mutedLocked(), Modes: h.modes, Focus: f.slug,
		FocusMode: h.focusMode, BackgroundMode: h.bgMode,
	}.ModeFor(slug)
}

// loadModes reads the modes kept in state.db (they outlast a restart). A
// value that is not a mode is skipped.
func (h *voiceHub) loadModes() {
	modes := map[string]voice.Mode{}
	if h.d.DB != nil {
		for _, kv := range h.d.DB.Prefixed(kvVoiceMode) {
			if m, ok := voice.ParseMode(kv[1]); ok && m != voice.ModeAuto {
				modes[strings.TrimPrefix(kv[0], kvVoiceMode)] = m
			}
		}
	}
	h.polMu.Lock()
	h.modes = modes
	h.polMu.Unlock()
}

// forgetMode: the session is gone (pekoe rm); so is its mode.
func (h *voiceHub) forgetMode(slug string) {
	h.polMu.Lock()
	_, had := h.modes[slug]
	delete(h.modes, slug)
	h.polMu.Unlock()
	if err := h.d.DB.Delete(kvVoiceMode + slug); err != nil {
		log.Printf("voice: forget the mode of %s: %v", slug, err)
	}
	if had {
		h.out.Do(h.emitStatus)
	}
}

func (h *voiceHub) mutedLocked() bool {
	return h.mutedEver || (!h.mutedUntil.IsZero() && h.clock.Now().Before(h.mutedUntil))
}

// focusLocked is what the client that last sent voice.focus shows (polMu
// or mu held: focus is only changed under both).
func (h *voiceHub) focusLocked() voiceFocus {
	var f voiceFocus
	for _, x := range h.focus {
		if x.at > f.at {
			f = x
		}
	}
	return f
}

// loadMute reads the mute kept in state.db (it outlasts a restart).
func (h *voiceHub) loadMute() {
	v := h.d.DB.Get(kvVoiceMute)
	h.polMu.Lock()
	defer h.polMu.Unlock()
	h.mutedEver, h.mutedUntil = false, time.Time{}
	switch {
	case v == voiceMuteForever:
		h.mutedEver = true
	case v != "":
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			h.mutedUntil = t
		}
	}
}

func (h *voiceHub) mutedUntilText() string {
	h.polMu.Lock()
	defer h.polMu.Unlock()
	switch {
	case h.mutedEver:
		return voiceMuteForever
	case !h.mutedUntil.IsZero() && h.clock.Now().Before(h.mutedUntil):
		return h.mutedUntil.Format(time.RFC3339)
	}
	return ""
}

// ---- status, log, events ----

func (h *voiceHub) status() VoiceStatus {
	h.mu.Lock()
	on, eng, q := h.on, h.eng, h.q
	h.mu.Unlock()
	g := h.d.cfg()
	st := VoiceStatus{Enabled: g.Voice.Enable, Supported: h.supported, Backend: g.Voice.BackendName(), MutedUntil: h.mutedUntilText()}
	h.polMu.Lock()
	st.Focus = h.focusLocked().slug
	st.Modes = make(map[string]voice.Mode, len(h.modes))
	for k, m := range h.modes {
		st.Modes[k] = m
	}
	h.polMu.Unlock()
	if !on {
		return st
	}
	es := eng.Status()
	st.Using, st.BackendError = es.Using, es.Error
	if es.Using == "" && es.Error != "" {
		st.Error = es.Error
	}
	qs := q.Status()
	st.Queue, st.Speaking = qs.Queue, qs.Speaking
	if qs.Error != "" {
		st.Error = qs.Error
		if !qs.PausedUntil.IsZero() {
			st.PausedUntil = qs.PausedUntil.Format(time.RFC3339)
		}
	}
	if qs.Queue == 0 && qs.Speaking == nil && qs.Synthesizing == 0 {
		select {
		case h.idle <- struct{}{}:
		default:
		}
	}
	return st
}

// emitStatus sends voice.updated when the status changed (on out).
func (h *voiceHub) emitStatus() {
	st := h.status()
	b, _ := json.Marshal(st)
	h.mu.Lock()
	same := string(b) == h.lastEmit
	h.lastEmit = string(b)
	h.mu.Unlock()
	if !same {
		h.d.emit("voice.updated", st)
	}
}

// writeLog appends one line to voice.log (0600, rotated past 1MB) and
// sends voice.logged (on out).
func (h *voiceHub) writeLog(e voice.LogEntry) {
	h.d.emit("voice.logged", e)
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	h.logMu.Lock()
	defer h.logMu.Unlock()
	p := paths.VoiceLog()
	if fi, err := os.Stat(p); err == nil && fi.Size()+int64(len(b)) > voiceLogMax {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("voice: %v", err)
		return
	}
	_ = f.Chmod(0o600)
	f.Write(append(b, '\n'))
	f.Close()
}

// readLog is the last n lines of voice.log (and voice.log.1 before it),
// newest first.
func (h *voiceHub) readLog(n int) []voice.LogEntry {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	var all []voice.LogEntry
	for _, p := range []string{paths.VoiceLog() + ".1", paths.VoiceLog()} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var e voice.LogEntry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				all = append(all, e)
			}
		}
		f.Close()
	}
	slices.Reverse(all)
	if len(all) > n {
		all = all[:n]
	}
	return all
}

// ---- synthesis ahead of time (docs/voice.md 9) ----

// warmSession queues the one-name sentences of the session: wait, done,
// error (tapi chat: wait). The _many sentences are not made ahead (too many of them).
func (h *voiceHub) warmSession(slug string) {
	h.mu.Lock()
	p := h.phrases
	h.mu.Unlock()
	kinds := []voice.Kind{voice.KindWait, voice.KindDone, voice.KindError}
	if slug == TapiSlug {
		kinds = kinds[:1] // tapi chat waits; it is never done or failed
	}
	for _, k := range kinds {
		h.warm(p.Render(k, []voice.Named{{Slug: slug}}))
	}
}

func (h *voiceHub) warm(text string) {
	if h.noWarm {
		return
	}
	h.warmMu.Lock()
	if !h.warmed[text] {
		h.warmed[text] = true
		h.warmTodo = append(h.warmTodo, text)
	}
	h.warmMu.Unlock()
	select {
	case h.warmKick <- struct{}{}:
	default:
	}
}

// warmLoop synthesizes the queued sentences one at a time, only while the
// queue says nothing, until this run stops.
func (h *voiceHub) warmLoop(stop <-chan struct{}, eng voiceEngine) {
	for {
		h.warmMu.Lock()
		var text string
		if len(h.warmTodo) > 0 {
			text, h.warmTodo = h.warmTodo[0], h.warmTodo[1:]
		}
		h.warmMu.Unlock()
		if text == "" {
			select {
			case <-stop:
				return
			case <-h.warmKick:
				continue
			}
		}
		for !h.queueIdle() {
			select {
			case <-stop:
				return
			case <-h.idle:
			case <-time.After(time.Second):
			}
		}
		h.mu.Lock()
		lang := h.phrases.Lang()
		h.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), voice.SynthTimeout+eng.CacheKeyTime())
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		if p, err := eng.Synth(ctx, text, voice.VoiceTapi, lang); err != nil && ctx.Err() == nil {
			log.Printf("voice: synthesizing ahead %q: %v", text, err)
		} else if err == nil {
			eng.Release(p) // not kept (cache-key failed): nothing to play it
		}
		cancel()
	}
}

func (h *voiceHub) queueIdle() bool {
	_, q := h.parts()
	if q == nil {
		return false
	}
	s := q.Status()
	return s.Queue == 0 && s.Speaking == nil && s.Synthesizing == 0
}

// ---- RPC (docs/voice.md 13; every voice.* needs the human token) ----

var errVoiceOff = errors.New("voice is off ([voice] enable = false in config.toml): nothing is synthesized or played while off")

func (h *voiceHub) needOn() error {
	if !h.supported {
		return errors.New("unsupported: pekoe voice works on macOS only")
	}
	if !h.isOn() {
		return errVoiceOff
	}
	return nil
}

type voiceMuteP struct {
	For   string `json:"for"`
	Until string `json:"until"`
	Off   bool   `json:"off"`
}

// hMute mutes (for a while, until a time, or till unmute) or unmutes. A
// mute stops the sound and empties the queue (Q8). Kept in state.db.
func (h *voiceHub) hMute(p voiceMuteP) (any, error) {
	now := h.clock.Now()
	var kv string
	switch {
	case p.Off:
	case p.For != "":
		d, err := time.ParseDuration(p.For)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("for: %q is not a positive duration like 30m", p.For)
		}
		kv = now.Add(d).Format(time.RFC3339)
	case p.Until != "":
		t, err := time.Parse(time.RFC3339, p.Until)
		if err != nil {
			return nil, fmt.Errorf("until: %q is not an RFC3339 time", p.Until)
		}
		if !t.After(now) {
			return nil, fmt.Errorf("until: %s has passed", p.Until)
		}
		kv = t.Format(time.RFC3339)
	default:
		kv = voiceMuteForever
	}
	var err error
	if kv == "" {
		err = h.d.DB.Delete(kvVoiceMute)
	} else {
		err = h.d.DB.Set(kvVoiceMute, kv)
	}
	if err != nil {
		return nil, err
	}
	h.loadMute()
	if _, q := h.parts(); q != nil && kv != "" {
		q.Mute()
		h.discardRead("")
	}
	h.out.Do(h.emitStatus)
	h.out.flush()
	return h.status(), nil
}

func (h *voiceHub) hStop() (any, error) {
	if _, q := h.parts(); q != nil {
		q.Stop()
	}
	return nil, nil
}

type voiceSayP struct {
	Text  string `json:"text"`
	Voice string `json:"voice"`
	Lang  string `json:"lang"`
}

// hSay is pekoe voice test: the sentence as it is, first in the queue,
// even while muted. Not while voice is off: nothing runs then.
func (h *voiceHub) hSay(p voiceSayP) (any, error) {
	if err := h.needOn(); err != nil {
		return nil, err
	}
	text := backend.CleanText(p.Text)
	switch {
	case text == "":
		return nil, errors.New("text: nothing to say")
	case len(p.Text) > backend.MaxText:
		return nil, fmt.Errorf("text: longer than %d bytes", backend.MaxText)
	case p.Voice != "" && p.Voice != voice.VoiceTapi && p.Voice != voice.VoiceReader:
		return nil, fmt.Errorf("voice: %q is not tapi or reader", p.Voice)
	case p.Lang != "" && p.Lang != voice.LangJa && p.Lang != voice.LangEn:
		return nil, fmt.Errorf("lang: %q is not ja or en", p.Lang)
	}
	_, q := h.parts()
	if q == nil {
		return nil, errVoiceOff
	}
	q.Push(voice.Item{Kind: voice.KindTest, Text: text, Voice: p.Voice, Lang: p.Lang})
	return h.status(), nil
}

type voiceFocusP struct {
	Slug        string `json:"slug"`
	TermFocused bool   `json:"term_focused"`
}

// hFocus: what a client shows in [4] and whether its terminal has focus.
// Kept per connection; the last one that sent counts; a connection that
// goes takes its focus with it. Only remembered while voice is off.
func (h *voiceHub) hFocus(who *Caller, p voiceFocusP) (any, error) {
	var key any = "in-process"
	if who != nil && who.done != nil {
		key = who.done
	}
	h.mu.Lock()
	h.polMu.Lock()
	before := h.focusLocked()
	_, known := h.focus[key]
	h.focusSeq++
	h.focus[key] = voiceFocus{slug: p.Slug, termFocused: p.TermFocused, at: h.focusSeq}
	h.polMu.Unlock()
	h.mu.Unlock()
	if !known && who != nil && who.done != nil {
		go func(done <-chan struct{}) {
			<-done
			h.dropFocus(done)
		}(who.done)
	}
	h.focusChanged(before)
	return nil, nil
}

func (h *voiceHub) dropFocus(key any) {
	h.mu.Lock()
	h.polMu.Lock()
	before := h.focusLocked()
	delete(h.focus, key)
	h.polMu.Unlock()
	h.mu.Unlock()
	h.focusChanged(before)
}

// focusChanged tells the Tracker the focus now; when the focused session
// changed, read stops (Q8).
func (h *voiceHub) focusChanged(before voiceFocus) {
	h.polMu.Lock()
	now := h.focusLocked()
	h.polMu.Unlock()
	tr, q := h.parts()
	if tr != nil {
		h.in.Do(func() {
			if !h.current(tr) {
				return
			}
			tr.Focus(now.slug, now.termFocused)
			if now.slug != before.slug {
				q.FocusMoved()
				if rd := h.reader(); rd != nil {
					rd.DiscardAll()
				}
			}
		})
	}
	if now.slug != before.slug {
		h.out.Do(h.emitStatus)
	}
}

type voiceModeP struct {
	Slug string `json:"slug"`
	Mode string `json:"mode"`
}

// hMode is pekoe voice mode: the mode of one session (7, rule 2); auto
// takes it back to the policy. Kept in state.db (voice.mode.<slug>) until
// the session is removed. Remembered while voice is off too. What is queued
// is decided again before it is said; what is being said goes on.
func (h *voiceHub) hMode(p voiceModeP) (any, error) {
	m, ok := voice.ParseMode(p.Mode)
	if !ok {
		return nil, fmt.Errorf("mode: %q is not read, narrate, mute or auto", p.Mode)
	}
	if p.Slug == "" {
		return nil, errors.New("slug: required")
	}
	known := func() bool {
		if p.Slug == TapiSlug {
			return true
		}
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		_, ok := h.d.sessions[p.Slug]
		return ok
	}
	if !known() {
		return nil, fmt.Errorf("no session %q", p.Slug)
	}
	var err error
	if m == voice.ModeAuto {
		err = h.d.DB.Delete(kvVoiceMode + p.Slug)
	} else {
		err = h.d.DB.Set(kvVoiceMode+p.Slug, string(m))
	}
	if err != nil {
		return nil, err
	}
	h.polMu.Lock()
	if m == voice.ModeAuto {
		delete(h.modes, p.Slug)
	} else {
		h.modes[p.Slug] = m
	}
	h.polMu.Unlock()
	h.discardRead(p.Slug)
	if !known() {
		// removed while this was being set: its session.removed may have
		// come before the kv was written
		h.forgetMode(p.Slug)
		return nil, fmt.Errorf("no session %q", p.Slug)
	}
	h.out.Do(h.emitStatus)
	h.out.flush()
	return h.status(), nil
}

type voiceLogP struct {
	N int `json:"n"`
}

func (h *voiceHub) hLog(p voiceLogP) (any, error) {
	n := p.N
	if n <= 0 {
		n = 50
	}
	n = min(n, 1000)
	h.out.flush() // what was logged before the call is in the file
	out := h.readLog(n)
	if out == nil {
		out = []voice.LogEntry{}
	}
	return out, nil
}

// VoiceBackends is voice.backends.
type VoiceBackends struct {
	Backends   []backend.Info      `json:"backends"`
	Candidates []backend.Candidate `json:"candidates"`
	// Note says why the backends were not asked (voice is off).
	Note string `json:"note,omitempty"`
}

// hBackends: the bundled say and [voice.backends], each asked version,
// check and voices, and the pekoe-voice-* in pekoed's PATH as candidates
// (never run). While voice is off nothing is run: the entries carry their
// paths only.
func (h *voiceHub) hBackends() (any, error) {
	h.mu.Lock()
	eng := h.eng
	h.mu.Unlock()
	pathEnv := os.Getenv("PATH")
	if eng != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		bs, cs := eng.List(ctx, pathEnv)
		return VoiceBackends{Backends: nonNil(bs), Candidates: nonNil(cs)}, nil
	}
	v := h.d.cfg().Voice
	out := VoiceBackends{Candidates: nonNil(backend.Candidates(pathEnv)), Note: errVoiceOff.Error() + "; the backends were not run"}
	if !h.supported {
		out.Note = "unsupported: pekoe voice works on macOS only; the backends were not run"
	}
	names := []string{backend.Builtin}
	for n := range v.Backends {
		if n != backend.Builtin {
			names = append(names, n)
		}
	}
	slices.Sort(names[1:])
	for _, n := range names {
		in := backend.Info{Name: n, Fallback: n == v.FallbackName(), Check: "not run (voice is off)"}
		o := backend.Options{Backend: v.BackendName(), Backends: v.Backends, Dir: paths.DataDir()}
		if b, err := backend.Resolve(n, o); err != nil {
			in.Check = err.Error()
			in.Path = v.Backends[n]
		} else {
			in.Path = b.Path
			if n != backend.Builtin {
				if w := backend.HomeWarning(b.Path); w != "" {
					in.Warnings = append(in.Warnings, w)
				}
			}
		}
		out.Backends = append(out.Backends, in)
	}
	return out, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
