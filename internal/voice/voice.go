// Package voice is the pure logic of pekoe voice (docs/voice.md): which
// mode a session speaks in (policy, 7), what tapi calls a session and what
// it says (names and phrases, 5.2 and 5.3), when a state change is worth
// saying (5.1 and 5.4), the speech queue (8) and what of the assistant's
// text is read (shape and Reader, 6.2 and 6.3).
//
// It runs no programs, touches no files and opens no sockets. The clock,
// the synthesizer and the player come in as interfaces so that the rules
// are tested with fakes (package voicetest), never with real sound or time.
//
// Wiring (internal/daemon/voice.go): a Tracker watches state changes and
// pushes narrate items into a Queue; the Queue asks the Tracker whether an
// item is still fresh (Q5) right before it synthesizes and plays it. A
// Reader takes the read hooks (MessageDisplay, Stop, UserPromptSubmit) and
// interrupts and returns the read items to push.
package voice

import (
	"context"
	"time"
)

// Kind is what an utterance is about. The narrate kinds come from state
// changes (5.1); read is assistant text (P3); test is pekoe voice test.
type Kind string

const (
	KindWait    Kind = "wait"
	KindDone    Kind = "done"
	KindError   Kind = "error"
	KindAsk     Kind = "ask"
	KindIdleAll Kind = "idle_all"
	KindRead    Kind = "read"
	KindTest    Kind = "test"
)

// Narrate: the kind is said by tapi on a state change.
func (k Kind) Narrate() bool {
	switch k {
	case KindWait, KindDone, KindError, KindAsk, KindIdleAll:
		return true
	}
	return false
}

// Logical voice names a backend maps to real voices (10.3).
const (
	VoiceTapi   = "tapi"
	VoiceReader = "reader"
)

// Languages.
const (
	LangJa = "ja"
	LangEn = "en"
)

// ResolveLang turns the [voice] lang setting into ja or en: auto (and
// anything unknown) reads LC_ALL, LC_MESSAGES then LANG; the first that is
// set decides, ja… is Japanese and anything else English (the tour does the
// same).
func ResolveLang(setting string, getenv func(string) string) string {
	switch setting {
	case LangJa, LangEn:
		return setting
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(k); v != "" {
			if len(v) >= 2 && v[:2] == "ja" {
				return LangJa
			}
			return LangEn
		}
	}
	return LangEn
}

// Synth turns one sentence into a WAV file and returns its path. It blocks
// until the file is written; cancelling ctx stops it. The first line of the
// error is what the log keeps as the reason. internal/voice/backend
// implements it (10).
type Synth interface {
	Synth(ctx context.Context, text, voice, lang string) (wavPath string, err error)
}

// SynthFunc is a function that is a Synth.
type SynthFunc func(ctx context.Context, text, voice, lang string) (string, error)

func (f SynthFunc) Synth(ctx context.Context, text, voice, lang string) (string, error) {
	return f(ctx, text, voice, lang)
}

// Player plays a WAV file. It blocks until the sound ends; cancelling ctx
// stops it. internal/voice/player implements it (9).
type Player interface {
	Play(ctx context.Context, wavPath string) error
}

// Clock is the time the rules read and the timers they set.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending AfterFunc. An alias, so that a fake clock can satisfy
// Clock without importing this package.
type Timer = interface {
	Stop() bool
}

// RealClock is the wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
