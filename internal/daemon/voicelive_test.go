package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/voice"
	"pekoe/internal/voice/backend"
	"pekoe/internal/voice/player"
)

// TestVoiceLive is the by-hand check of P1: a pekoed (on the fake terminal
// engine, in a temp PEKOE_HOME) with the real bundled say and afplay says
// one session's wait, two sessions' wait in one sentence and one's done
// aloud, and voice.log shows how long
// after each state change the sound started. It makes sound, so it runs
// only when asked: PEKOE_VOICE_LIVE=<a pekoe built from this tree> (the
// bundled say is `pekoe voice-backend-say`). The log goes to the test's
// output (-v).
func TestVoiceLive(t *testing.T) {
	bin := os.Getenv("PEKOE_VOICE_LIVE")
	if bin == "" {
		t.Skip("makes sound: set PEKOE_VOICE_LIVE to a pekoe built from this tree to run it")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	d, _, repo := setup(t)
	h := d.voice
	h.newEngine = func(o backend.Options) voiceEngine {
		o.Self = bin
		return newRealVoiceEngine(o)
	}
	h.player = player.New()
	t.Cleanup(h.close)
	lang := os.Getenv("PEKOE_VOICE_LIVE_LANG")
	if lang == "" {
		lang = "ja"
	}
	for _, s := range []string{"feat-csv-export", "fix-ci", "docs-voice-spec"} {
		d.add(&state.Session{Slug: s, Branch: s, Repo: "app", RepoDir: repo, Path: repo, State: state.Running, StateSince: time.Now(), CreatedAt: time.Now()})
	}
	writeConfig(t, d, "[voice]\nenable = true\nlang = \""+lang+"\"\n")
	// the one-name sentences are made ahead (docs/voice.md 9): wait for them
	cache := filepath.Join(paths.DataDir(), "voice", "cache")
	for end := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		ents, _ := os.ReadDir(cache)
		if len(ents) >= 11 && h.queueIdle() {
			t.Logf("made ahead: %d sentences in %s", len(ents), cache)
			break
		}
		if time.Now().After(end) {
			t.Fatalf("not made ahead in 60s (%d in the cache); status %+v", len(ents), h.status())
		}
	}
	say := func(slug string, st state.State) {
		if _, err := d.update(slug, func(s *state.Session) { s.State = st }); err != nil {
			t.Fatal(err)
		}
	}
	say("feat-csv-export", state.Waiting) // alone: from the cache
	waitSpoken(t, h, 1)
	time.Sleep(6 * time.Second)  // the sentence ends (afplay takes ~1s more)
	say("fix-ci", state.Waiting) // two within 300ms: one sentence, made now
	say("docs-voice-spec", state.Waiting)
	waitSpoken(t, h, 2)
	time.Sleep(6 * time.Second)
	say("fix-ci", state.Running)
	say("fix-ci", state.Done) // from the cache
	waitSpoken(t, h, 3)
	time.Sleep(4 * time.Second)
	h.out.flush()
	for _, e := range h.readLog(50) {
		t.Logf("voice.log: %s %-16s %-5s %-7s %4dms %q %s", e.Time.Format("15:04:05.000"), e.Slug, e.Kind, e.Result, e.LatencyMS, e.Text, e.Backend)
	}
}

func waitSpoken(t *testing.T, h *voiceHub, n int) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		h.out.flush()
		got := 0
		for _, e := range h.readLog(50) {
			if e.Result == voice.ResultSpoken {
				got++
			}
		}
		if got >= n {
			return
		}
		if time.Now().After(end) {
			var lines []string
			for _, e := range h.readLog(50) {
				lines = append(lines, string(e.Kind)+" "+string(e.Result)+" "+e.Reason)
			}
			t.Fatalf("%d spoken, want %d; status %+v\n%s", got, n, h.status(), strings.Join(lines, "\n"))
		}
	}
}

// writeConfig writes config.toml and has d take it up.
func writeConfig(t *testing.T, d *Daemon, toml string) {
	t.Helper()
	w := &voiceWorld{t: t, d: d}
	w.config(toml)
}
