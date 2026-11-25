package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Options configure an Engine: config.toml's [voice] backend, fallback and
// [voice.backends], and where things are.
type Options struct {
	Backend  string            // "say" or a [voice.backends] name ("" is "say")
	Fallback string            // the same, or "" for none
	Backends map[string]string // name → absolute or ~/ path

	// Self is pekoe's own executable, the bundled say. "" is
	// os.Executable with its links followed.
	Self string
	// Dir is pekoe's data directory: the backends' working directory, and
	// voice/tmp and voice/cache under it.
	Dir string

	// Variant is what else makes the same backend and version sound
	// different (the bundled say's [voice.say] voices): part of the cache
	// key, so a changed voice is not served from the cache.
	Variant string

	Timeouts Timeouts
	Now      func() time.Time
	// Logf writes one line to pekoed.log (nil: nowhere).
	Logf func(format string, args ...any)
}

// RecheckAfter is how long the engine stays on the fallback before it asks
// the configured backend again (docs/voice.md 10.5).
const RecheckAfter = 5 * time.Minute

// MaxSynth is how many synth calls run at once (the one playing next is
// synthesized while one plays).
const MaxSynth = 2

// ReaderVoice is the logical voice of read: its sentences are an agent's
// words and are never kept in the cache (docs/voice.md 15「残すもの」).
const ReaderVoice = "reader"

// Status is what the engine is using.
type Status struct {
	Backend string `json:"backend"` // configured
	Using   string `json:"using"`   // in use ("" when none can be used)
	Error   string `json:"error"`   // why the configured one (or both) are not used
}

// Engine picks the backend to use, falls back, synthesizes with a cache.
// It is safe for concurrent use. It satisfies the speech queue's
//
//	Synth(ctx, text, voice, lang string) (wavPath string, err error)
//
// and the daemon wires Failover to the queue's Config.Failover.
type Engine struct {
	o     Options
	cache *Cache
	sem   chan struct{}

	mu         sync.Mutex
	using      *Backend
	usingVer   string // what its version printed: part of the cache key
	onFallback bool
	switchedAt time.Time
	errText    string
	// the fallback checked for retries (FallbackFor) while the configured
	// backend is in use; nil until needed, and after it failed
	retryFB    *Backend
	retryFBVer string
	// rechecking: a recheck of the configured backend is running (one at
	// a time); selecting: a Select started by Synth is running, closed
	// when it ends (the Synths that came meanwhile wait for it)
	rechecking bool
	selecting  chan struct{}
	bg         sync.WaitGroup // the recheck and Select running on their own
}

// New makes an Engine. Nothing runs until Select (or the first Synth).
func New(o Options) *Engine {
	if o.Backend == "" {
		o.Backend = Builtin
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Self == "" {
		o.Self = SelfExecutable()
	}
	o.Timeouts = o.Timeouts.orDefault()
	return &Engine{o: o, cache: &Cache{Dir: filepath.Join(o.Dir, "voice", "cache")}, sem: make(chan struct{}, MaxSynth)}
}

// SelfExecutable is pekoe's own executable with its symbolic links
// followed (the same pekoe the hooks name), or "" when it cannot be found.
func SelfExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe
}

// Cache is the engine's WAV cache.
func (e *Engine) Cache() *Cache { return e.cache }

// Resolve turns a configured name into a Backend, checking its path. The
// name "say" is always the bundled one; any other must be in
// [voice.backends] with an absolute (or ~/) path to an executable file.
// PATH is never searched.
func Resolve(name string, o Options) (*Backend, error) {
	o.Timeouts = o.Timeouts.orDefault()
	bad := func(format string, a ...any) error {
		return &Error{Backend: name, Op: "path", Reason: fmt.Sprintf(format, a...)}
	}
	var b *Backend
	switch {
	case name == Builtin:
		self := o.Self
		if self == "" {
			self = SelfExecutable()
		}
		if self == "" || !filepath.IsAbs(self) {
			return nil, bad("pekoe's own executable could not be found")
		}
		b = &Backend{Name: name, Path: self, Prefix: []string{BuiltinCommand}}
	case !ValidName(name):
		return nil, bad("%q is not a backend name (lower-case letters, digits and -)", name)
	default:
		p, ok := o.Backends[name]
		if !ok {
			return nil, bad("%q is not in [voice.backends] (PATH is not searched)", name)
		}
		abs, err := ExpandPath(p)
		if err != nil {
			return nil, bad("%s", err)
		}
		b = &Backend{Name: name, Path: abs}
	}
	b.Dir = o.Dir
	b.Timeouts = o.Timeouts
	if err := b.Check(); err != nil {
		return nil, err
	}
	return b, nil
}

// prepare resolves name and asks it `version` and `check`.
func (e *Engine) prepare(ctx context.Context, name string) (*Backend, string, error) {
	b, err := Resolve(name, e.o)
	if err != nil {
		return nil, "", err
	}
	v, err := b.Version(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := b.CheckUsable(ctx); err != nil {
		return nil, "", err
	}
	return b, v, nil
}

// Select checks the configured backend (path, version, check) and uses
// it; when it cannot be used, the fallback, checked the same way. With
// neither usable it returns the reason and nothing is used. The daemon calls
// it when voice is turned on and when the settings change.
func (e *Engine) Select(ctx context.Context) error {
	b, v, err := e.prepare(ctx, e.o.Backend)
	if err == nil {
		e.mu.Lock()
		e.using, e.usingVer, e.onFallback, e.errText = b, v, false, ""
		e.mu.Unlock()
		return nil
	}
	e.o.Logf("voice: backend %s cannot be used: %v", e.o.Backend, err)
	reason := err.Error()
	if e.o.Fallback == "" || e.o.Fallback == e.o.Backend {
		e.setNone(reason)
		return err
	}
	fb, fv, ferr := e.prepare(ctx, e.o.Fallback)
	if ferr != nil {
		e.o.Logf("voice: fallback %s cannot be used either: %v", e.o.Fallback, ferr)
		e.setNone(reason + "; fallback: " + ferr.Error())
		return fmt.Errorf("%w; fallback: %v", err, ferr)
	}
	e.o.Logf("voice: using the fallback %s", e.o.Fallback)
	e.mu.Lock()
	e.using, e.usingVer, e.onFallback, e.switchedAt, e.errText = fb, fv, true, e.o.Now(), reason
	e.mu.Unlock()
	return nil
}

func (e *Engine) setNone(reason string) {
	e.mu.Lock()
	e.using, e.usingVer, e.onFallback, e.errText = nil, "", false, reason
	e.mu.Unlock()
}

// Failover switches to the fallback after the backend in use kept failing
// (the speech queue counts the failures). It returns true when it switched;
// false when already on the fallback, when there is none, or when the
// fallback cannot be used.
func (e *Engine) Failover() bool {
	e.mu.Lock()
	on := e.onFallback
	e.mu.Unlock()
	if on || e.o.Fallback == "" || e.o.Fallback == e.o.Backend {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.o.Timeouts.Version+e.o.Timeouts.Check)
	defer cancel()
	fb, fv, err := e.prepare(ctx, e.o.Fallback)
	if err != nil {
		e.o.Logf("voice: cannot fall back to %s: %v", e.o.Fallback, err)
		return false
	}
	e.o.Logf("voice: %s kept failing; using the fallback %s", e.o.Backend, e.o.Fallback)
	e.mu.Lock()
	e.using, e.usingVer, e.onFallback, e.switchedAt = fb, fv, true, e.o.Now()
	e.errText = e.o.Backend + " kept failing"
	e.mu.Unlock()
	return true
}

// recheck: on the fallback for RecheckAfter, ask the configured backend
// again and go back to it when it passes. It does not wait: the check runs
// on its own with its own time (Version+Check), not in the sentence's
// synth, whose few seconds it would use up (and then fail, and the
// sentence with it). The sentence that started it is synthesized with the
// fallback; the next ones with the configured backend once it passed.
func (e *Engine) recheck() {
	e.mu.Lock()
	due := e.onFallback && !e.rechecking && !e.o.Now().Before(e.switchedAt.Add(RecheckAfter))
	if due {
		e.rechecking = true
		e.switchedAt = e.o.Now() // the next try is RecheckAfter from now
	}
	e.mu.Unlock()
	if !due {
		return
	}
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), e.o.Timeouts.Version+e.o.Timeouts.Check)
		defer cancel()
		b, v, err := e.prepare(ctx, e.o.Backend)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.rechecking = false
		if err != nil {
			e.o.Logf("voice: %s still cannot be used: %v", e.o.Backend, err)
			return
		}
		if !e.onFallback {
			return // Select ran meanwhile and decided
		}
		e.o.Logf("voice: back to %s", e.o.Backend)
		e.using, e.usingVer, e.onFallback, e.errText = b, v, false, ""
	}()
}

// selectForSynth runs Select for Synth when nothing is in use, on its own
// time (both backends' Version+Check), not the sentence's: a Select cut
// short by the sentence's few seconds would record a reason that is only
// the sentence's timeout. ctx ending stops the waiting, not the Select
// (its result is kept for the next sentence). Synths that come while it
// runs wait for the same one.
func (e *Engine) selectForSynth(ctx context.Context) error {
	e.mu.Lock()
	done := e.selecting
	var errp *error
	if done == nil {
		done = make(chan struct{})
		e.selecting = done
		errp = new(error)
		e.bg.Add(1)
		go func() {
			defer e.bg.Done()
			t := e.o.Timeouts
			sctx, cancel := context.WithTimeout(context.Background(), 2*(t.Version+t.Check))
			defer cancel()
			*errp = e.Select(sctx)
			e.mu.Lock()
			e.selecting = nil
			e.mu.Unlock()
			close(done)
		}()
	}
	e.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if errp != nil && *errp != nil {
		return *errp
	}
	return nil
}

// wait waits for the recheck and Select running on their own (tests).
func (e *Engine) wait() { e.bg.Wait() }

// Status is the configured backend, the one in use and why not the other.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{Backend: e.o.Backend, Error: e.errText}
	if e.using != nil {
		s.Using = e.using.Name
	}
	return s
}

// ErrUnavailable: no backend can be used (Status has the reason).
var ErrUnavailable = errors.New("no voice backend can be used")

// Synth turns one sentence into a WAV file and returns its path. Sentences
// of the tapi voice are kept in the cache (the path is the cache's: do not
// remove it); sentences of the reader voice are not (the path is a
// temporary file: Release it once played), nor tapi sentences of an
// external backend whose `cache-key` failed. Release every path once
// played: it leaves the cache's alone. ctx ending stops the backend.
func (e *Engine) Synth(ctx context.Context, text, voice, lang string) (string, error) {
	text = CleanText(text)
	if text == "" {
		return "", errors.New("no text to say")
	}
	e.recheck()
	e.mu.Lock()
	b, ver, reason := e.using, e.usingVer, e.errText
	e.mu.Unlock()
	if b == nil {
		if err := e.selectForSynth(ctx); err != nil {
			return "", err
		}
		e.mu.Lock()
		b, ver, reason = e.using, e.usingVer, e.errText
		e.mu.Unlock()
		if b == nil {
			return "", fmt.Errorf("%w: %s", ErrUnavailable, reason)
		}
	}
	return e.synthWith(ctx, b, ver, text, voice, lang)
}

// synthWith synthesizes text (already cleaned) with b, whose version
// printed ver, through the cache.
func (e *Engine) synthWith(ctx context.Context, b *Backend, ver, text, voice, lang string) (string, error) {
	keep := voice != ReaderVoice
	if b.Name == Builtin && e.o.Variant != "" {
		ver += "\x00" + e.o.Variant
	}
	if keep && b.Name != Builtin {
		// asked every time: the backend's settings may change at any moment
		// and the next sentence must sound like them (docs/voice.md 10.3)
		fp, err := b.CacheKey(ctx)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				// the caller's time ran out (or it gave up) in cache-key:
				// this backend's failure all the same, so that the queue
				// counts it and tries the fallback (10.5)
				var be *Error
				if !errors.As(err, &be) {
					be = &Error{Backend: b.Name, Op: "cache-key", Reason: ctx.Err().Error(), Timeout: true}
				}
				return "", be
			}
			e.o.Logf("voice: not using the cache for this sentence: %v", err)
			keep = false
		case fp != "":
			ver += "\x00cache-key\x00" + fp
		}
	}
	key := Key(b.Name, ver, voice, lang, text)
	if keep {
		if p, ok := e.cache.Get(key); ok {
			return p, nil
		}
	}
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-e.sem }()
	tmp, err := e.tmpDir()
	if err != nil {
		return "", err
	}
	out := filepath.Join(tmp, "out.wav")
	if err := b.Synth(ctx, Request{Text: text, Voice: voice, Lang: lang}, out); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if !keep {
		return out, nil
	}
	p, err := e.cache.Put(key, out)
	os.RemoveAll(tmp)
	if err != nil {
		return "", err
	}
	return p, nil
}

// CacheKeyTime is how much longer than its synth one Synth may take: an
// external backend runs cache-key before synth (10.3) within the caller's
// time. It is the cache-key's time-out when the configured backend or the
// fallback is external (either may synthesize the next sentence), else 0.
func (e *Engine) CacheKeyTime() time.Duration {
	for _, n := range []string{e.o.Backend, e.o.Fallback} {
		if n != "" && n != Builtin {
			return e.o.Timeouts.CacheKey
		}
	}
	return 0
}

// FallbackFor is what the speech queue tries after Synth failed with err
// (docs/voice.md 10.5): when the configured backend failed and a fallback
// is configured, the backend that failed, the fallback's name and a synth
// with the fallback, for the same sentence once more. synth is nil when
// there is nothing to try: the failure was not the configured backend's
// (it was the fallback's own, or no backend's), there is no fallback, or
// nothing can be used (Select found neither usable: the fallback was just
// checked and failed).
// It runs nothing; synth checks the fallback (path, version, check) the
// first time it is needed and keeps it while it works.
func (e *Engine) FallbackFor(err error) (failed, name string, synth func(ctx context.Context, text, voice, lang string) (string, error)) {
	var be *Error
	if !errors.As(err, &be) || be.Backend != e.o.Backend || e.o.Fallback == "" || e.o.Fallback == e.o.Backend {
		return "", "", nil
	}
	e.mu.Lock()
	none := e.using == nil
	e.mu.Unlock()
	if none {
		return "", "", nil
	}
	return be.Backend, e.o.Fallback, e.synthFallback
}

func (e *Engine) synthFallback(ctx context.Context, text, voice, lang string) (string, error) {
	text = CleanText(text)
	if text == "" {
		return "", errors.New("no text to say")
	}
	e.mu.Lock()
	b, ver := e.retryFB, e.retryFBVer
	if e.onFallback && e.using != nil {
		b, ver = e.using, e.usingVer // switched meanwhile: the same thing
	}
	e.mu.Unlock()
	if b == nil {
		var err error
		if b, ver, err = e.prepare(ctx, e.o.Fallback); err != nil {
			return "", err
		}
		e.mu.Lock()
		e.retryFB, e.retryFBVer = b, ver
		e.mu.Unlock()
	}
	p, err := e.synthWith(ctx, b, ver, text, voice, lang)
	if err != nil {
		e.mu.Lock()
		if e.retryFB == b {
			e.retryFB, e.retryFBVer = nil, "" // checked again next time
		}
		e.mu.Unlock()
	}
	return p, err
}

// tmpDir is a new empty directory under <data>/voice/tmp for one synth.
func (e *Engine) tmpDir() (string, error) {
	base := filepath.Join(e.o.Dir, "voice", "tmp")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "synth")
}

// Release removes a file Synth returned for the reader voice once it has
// been played. Paths in the cache (and anything else) are left alone.
func (e *Engine) Release(path string) {
	base := filepath.Join(e.o.Dir, "voice", "tmp")
	dir := filepath.Dir(path)
	if filepath.Dir(dir) == base && filepath.Base(path) == "out.wav" {
		os.RemoveAll(dir)
	}
}

// CleanTmp removes what is left in <data>/voice/tmp (a pekoed that stopped
// mid-synth, a reader file never released) older than age.
func (e *Engine) CleanTmp(age time.Duration) {
	base := filepath.Join(e.o.Dir, "voice", "tmp")
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, en := range ents {
		if fi, err := en.Info(); err == nil && e.o.Now().Sub(fi.ModTime()) >= age {
			os.RemoveAll(filepath.Join(base, en.Name()))
		}
	}
}
