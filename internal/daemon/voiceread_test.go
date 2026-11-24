package daemon

// read in pekoed (docs/voice.md 6, P3): the hooks (MessageDisplay, Stop,
// UserPromptSubmit) go to voice.Reader and its sentences to the Queue; an
// interrupt through pekoe, the focus moving and pekoe voice mode stop it.

import (
	"bytes"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/state"
	"pekoe/internal/voice"
)

// readWorld is a voiceWorld with voice on and the session slug in front of
// a focused terminal (focus_mode read; no narrate of it, D2).
func newReadWorld(t *testing.T, slug, agent string) *voiceWorld {
	t.Helper()
	w := newVoiceWorld(t)
	if slug != "" {
		w.d.add(&state.Session{Slug: slug, Branch: slug, Repo: "app", RepoDir: w.repo, Path: w.repo, Agent: agent, State: state.Idle, StateSince: time.Now(), CreatedAt: time.Now()})
	}
	w.config(voiceOn)
	w.focus(slug)
	return w
}

func (w *voiceWorld) focus(slug string) {
	w.t.Helper()
	call[any](w.t, w.d, "voice.focus", map[string]any{"slug": slug, "term_focused": true})
	w.settle()
}

// display sends one MessageDisplay batch as the hook does.
func (w *voiceWorld) display(slug, turn, msg string, index int, final bool, delta string) {
	w.t.Helper()
	res, err := w.d.Call("session.event", mustJSON(map[string]any{"hook": "MessageDisplay", "session": slug, "payload": map[string]any{
		"session_id": "c0ffee", "hook_event_name": "MessageDisplay", "turn_id": turn, "message_id": msg, "index": index, "final": final, "delta": delta,
	}}))
	if err != nil || res != nil {
		w.t.Fatalf("MessageDisplay: %v %v", res, err)
	}
	w.settle()
}

// message sends a whole message batch by batch (the last one final).
func (w *voiceWorld) message(slug, turn, msg string, parts ...string) {
	w.t.Helper()
	for i, p := range parts {
		w.display(slug, turn, msg, i, i == len(parts)-1, p)
	}
}

func (w *voiceWorld) hook(hook, slug string, payload map[string]any) {
	w.t.Helper()
	event(w.t, w.d, hook, slug, payload)
	w.settle()
}

// read says what was queued, in order: each synth is answered and each
// sound finished; it returns the reader's sentences played once nothing
// more comes. Narrate (tapi's voice) is said too but left out.
func (w *voiceWorld) read() []string {
	w.t.Helper()
	var got []string
	reader := map[string]string{} // wav → text, the reader's
	for {
		w.advance(time.Second) // past any narrate's gather delay
		idle := time.Now().Add(150 * time.Millisecond)
		for w.syn.Pending() == 0 && w.pl.Pending() == 0 && time.Now().Before(idle) {
			time.Sleep(5 * time.Millisecond)
			w.settle()
		}
		switch {
		case w.syn.Pending() > 0:
			c := w.syn.Next(w.t)
			wav := c.Text + ".wav"
			if c.Voice == voice.VoiceReader {
				reader[wav] = c.Text
			}
			c.OK(wav)
		case w.pl.Pending() > 0:
			p := w.pl.Next(w.t)
			if text, ok := reader[p.Wav]; ok {
				got = append(got, text)
			}
			p.Finish()
		default:
			return got
		}
	}
}

func wantRead(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: read %q, want %q", what, got, want)
	}
}

// A reply with code and a table, in batches that split the fence (as the
// pty sends them, docs/voice.md 3.2), is read without the code and the
// table; a reply of another turn after a tool reads on (6.1, 6.2).
func TestVoiceReadMessageDisplay(t *testing.T) {
	w := newReadWorld(t, "feat-a", "claude")
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "explain"})
	w.message("feat-a", "t1", "m1",
		"Here is the fix.\n```go\n",
		"x := 1\n",
		"```\n| a | b |\n|---|---|\n",
		"| 1 | 2 |\n\nIt sets x.",
	)
	wantRead(t, "code and table", w.read(), "Here is the fix.", "It sets x.")
	// a tool between: the summary is another message of the same turn
	w.message("feat-a", "t1", "m2", "Done, see `main.go` for it.")
	wantRead(t, "after a tool", w.read(), "Done, see main.go for it.")
	// the Stop of a turn whose text came by MessageDisplay is not read
	// again (6.1)
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "Done, see `main.go` for it."})
	wantRead(t, "Stop after MessageDisplay", w.read())

	// The Stop way: no MessageDisplay (hook not installed), the Stop's
	// last_assistant_message is read at the end of the turn.
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "and?"})
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "All good.\n```\nrm -rf x\n```\nBye."})
	wantRead(t, "the Stop way", w.read(), "All good.", "Bye.")
}

// read follows Claude Code only (P3, isClaude) and the session in front
// only (focus_mode read, background narrate).
func TestVoiceReadOnlyClaudeInFront(t *testing.T) {
	w := newReadWorld(t, "feat-codex", "codex")
	w.hook("UserPromptSubmit", "feat-codex", map[string]any{"prompt": "x"})
	w.message("feat-codex", "t1", "m1", "Codex says hi.")
	w.hook("Stop", "feat-codex", map[string]any{"last_assistant_message": "Codex says hi."})
	wantRead(t, "codex", w.read())

	w.d.add(&state.Session{Slug: "feat-b", Branch: "feat-b", Repo: "app", RepoDir: w.repo, Path: w.repo, Agent: "claude", State: state.Idle, StateSince: time.Now(), CreatedAt: time.Now()})
	w.settle()
	w.message("feat-b", "t1", "m1", "Not in front.")
	wantRead(t, "background", w.read())
	w.focus("feat-b")
	w.message("feat-b", "t2", "m1", "In front.")
	wantRead(t, "in front", w.read(), "In front.")
	// pekoe voice mode narrate wins over the focus
	call[VoiceStatus](t, w.d, "voice.mode", map[string]any{"slug": "feat-b", "mode": "narrate"})
	w.message("feat-b", "t3", "m1", "Narrate only.")
	wantRead(t, "mode narrate", w.read())
}

// A prompt typed ahead while running (6.3): the batches of the turn that
// keep coming and the Stop that ends it are not read; the typed-ahead turn
// (no UserPromptSubmit of its own) is. Running is the state before the
// prompt updates it: a prompt while idle does not stop anything.
func TestVoiceReadTypedAhead(t *testing.T) {
	w := newReadWorld(t, "feat-a", "claude")
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "count"}) // idle → running
	w.display("feat-a", "t1", "m1", 0, false, "One.\n")
	w.display("feat-a", "t1", "m1", 1, false, "Two.\n")
	wantRead(t, "before typing ahead", w.read(), "One.", "Two.")
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "then this"}) // while running
	w.display("feat-a", "t1", "m1", 2, true, "Three.")
	w.message("feat-a", "t1", "m2", "A message after the prompt.")
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "A message after the prompt."})
	wantRead(t, "the typed-over turn", w.read())
	// a late batch of the stopped turn after its Stop (3.2)
	w.message("feat-a", "t1", "m3", "Late.")
	w.message("feat-a", "t2", "m1", "The typed-ahead turn.")
	wantRead(t, "the typed-ahead turn", w.read(), "The typed-ahead turn.")

	// typed ahead before the turn showed any text: the turn is not read
	// either, up to its Stop
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "The typed-ahead turn."})
	waitReadState(t, w.d, "feat-a", func(s state.State) bool { return s != state.Running })
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "next"}) // not running: nothing stops
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "and ahead"})
	w.message("feat-a", "t3", "m1", "Thinking done.")
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "Thinking done."})
	w.message("feat-a", "t4", "m1", "Ahead.")
	wantRead(t, "typed ahead before any text", w.read(), "Ahead.")

	// The Stop way: the Stop after a prompt typed ahead is not read
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "Ahead."})
	waitReadState(t, w.d, "feat-a", func(s state.State) bool { return s != state.Running })
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "p1"})
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "p2"})
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "The typed-over reply."})
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "The p2 reply."})
	wantRead(t, "the Stop way, typed ahead", w.read(), "The p2 reply.")
}

func waitReadState(t *testing.T, d *Daemon, slug string, ok func(state.State) bool) {
	t.Helper()
	eventually(t, slug+"'s state", func() bool {
		s, err := d.snapshot(slug)
		return err == nil && ok(s.State)
	})
}

// session.interrupt stops the read of the session: the sentence being said
// stops, and what comes of the turn after it is not read (6.3).
func TestVoiceReadSessionInterrupt(t *testing.T) {
	w := newVoiceWorld(t)
	runningSession(t, w.d, w.repo) // "main", Claude Code, running
	w.config(voiceOn)
	w.focus("main")
	w.display("main", "t1", "m1", 0, false, "First.\nSecond.\n")
	c := w.syn.Next(t)
	if c.Text != "First." {
		t.Fatalf("synth %q", c.Text)
	}
	c.OK("first.wav")
	p := w.pl.Next(t)
	call[state.Session](t, w.d, "session.interrupt", map[string]any{"slug": "main"})
	p.WaitStopped(t)
	w.settle()
	w.display("main", "t1", "m1", 1, false, "Third.\n") // sent before the Escape landed
	w.display("main", "t1", "m2", 0, true, "Another.")
	wantRead(t, "after the interrupt", w.read())
	w.waitLog("main read interrupted")
	// the turn is gone from the screen: pekoed moves it to idle
	// (settleInterrupt), and the next turn is read
	showBusy(w.d.Eng.(*fakeEngine), false)
	waitReadState(t, w.d, "main", func(s state.State) bool { return s == state.Idle })
	event(t, w.d, "UserPromptSubmit", "main", map[string]any{"prompt": "again"})
	w.message("main", "t2", "m1", "Next turn.")
	wantRead(t, "next turn", w.read(), "Next turn.")
}

// tapi.interrupt stops tapi chat's read the same way.
func TestVoiceReadTapiInterrupt(t *testing.T) {
	w := newVoiceWorld(t)
	showBusy(w.d.Eng.(*fakeEngine), true)
	call[Tapi](t, w.d, "tapi.open", nil)
	w.config(voiceOn)
	w.focus(TapiSlug)
	w.hook("UserPromptSubmit", TapiSlug, map[string]any{"prompt": "hi"})
	w.display(TapiSlug, "t1", "m1", 0, false, "Hello.\n")
	wantRead(t, "tapi", w.read(), "Hello.")
	call[Tapi](t, w.d, "tapi.interrupt", nil)
	w.settle()
	w.display(TapiSlug, "t1", "m1", 1, true, "More.")
	wantRead(t, "after tapi.interrupt", w.read())
	showBusy(w.d.Eng.(*fakeEngine), false)
	eventually(t, "tapi idle", func() bool { st, _ := w.d.tapiNow(); return st == state.Idle })
	// an Escape through tapi.keys (the TUI's esc while typing into it)
	w.hook("UserPromptSubmit", TapiSlug, map[string]any{"prompt": "again"})
	w.display(TapiSlug, "t2", "m1", 0, false, "Again.\n")
	wantRead(t, "tapi again", w.read(), "Again.")
	call[any](t, w.d, "tapi.keys", map[string]any{"keys": []string{"Escape"}})
	w.settle()
	w.display(TapiSlug, "t2", "m1", 1, true, "Not this.")
	wantRead(t, "after tapi.keys Escape", w.read())
}

// session.keys with Escape in keys stops the read (the TUI's esc while
// typing into [4]); hex 1b (the head of mouse sequences), other keys and
// other windows do not.
func TestVoiceReadKeysEscape(t *testing.T) {
	w := newReadWorld(t, "feat-a", "claude")
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "go"})
	w.display("feat-a", "t1", "m1", 0, false, "One.\n")
	wantRead(t, "start", w.read(), "One.")
	call[any](t, w.d, "session.keys", map[string]any{"slug": "feat-a", "hex": []string{"1b", "5b", "4d"}})
	call[any](t, w.d, "session.keys", map[string]any{"slug": "feat-a", "keys": []string{"Down"}})
	call[any](t, w.d, "session.keys", map[string]any{"slug": "feat-a", "window": "dev", "keys": []string{"Escape"}})
	w.settle()
	w.display("feat-a", "t1", "m1", 1, false, "Two.\n")
	wantRead(t, "hex 1b, other keys, the dev window", w.read(), "Two.")
	call[any](t, w.d, "session.keys", map[string]any{"slug": "feat-a", "keys": []string{"Escape"}})
	w.settle()
	w.display("feat-a", "t1", "m1", 2, true, "Three.")
	wantRead(t, "after Escape", w.read())

	// The error 6.3 accepts (17: (a)): an Escape leaves the hooks' state
	// running (no Stop comes), so the next prompt looks typed ahead and
	// its turn goes unread up to its Stop; the turn after that is read.
	if s, _ := w.d.snapshot("feat-a"); s.State != state.Running {
		t.Fatalf("state after Escape: %s", s.State)
	}
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "next"})
	w.message("feat-a", "t2", "m1", "Unread.")
	w.hook("Stop", "feat-a", map[string]any{"last_assistant_message": "Unread."})
	waitReadState(t, w.d, "feat-a", func(s state.State) bool { return s != state.Running })
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "after"})
	w.message("feat-a", "t3", "m1", "Read again.")
	wantRead(t, "after the accepted error", w.read(), "Read again.")
}

// The focus moving away stops the read and drops what was begun; coming
// back reads new messages, not the rest of the old one (6.1, 6.3).
func TestVoiceReadFocusMoves(t *testing.T) {
	w := newReadWorld(t, "feat-a", "claude")
	w.hook("UserPromptSubmit", "feat-a", map[string]any{"prompt": "go"})
	w.display("feat-a", "t1", "m1", 0, false, "One.\nTwo.\n")
	c := w.syn.Next(t)
	c.OK("one.wav")
	p := w.pl.Next(t)
	w.focus("feat-other")
	p.WaitStopped(t)
	w.waitLog("feat-a read interrupted")
	wantRead(t, "after the focus moved", w.read())
	w.focus("feat-a")
	w.display("feat-a", "t1", "m1", 1, true, "Three.")
	w.message("feat-a", "t1", "m2", "New message.")
	wantRead(t, "back in front", w.read(), "New message.")

}

// lockedBuf is a bytes.Buffer for log output written from many goroutines.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// tapi chat's reply while the TUI shows something else is not read, and
// pekoed.log says why, with the focus (a reply that goes unheard is in no
// other log: nothing of it reaches the queue, so voice.log has no line).
// The MessageDisplay comes as pekoe event MessageDisplay sends it: from a
// connection that is gone before pekoed looks at its window (no caller
// window), naming +tapi. Back in front, tapi's next turn is read.
func TestVoiceReadUnreadLogged(t *testing.T) {
	var buf lockedBuf
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	w := newVoiceWorld(t)
	call[Tapi](t, w.d, "tapi.open", nil)
	w.config(voiceOn)
	w.focus("feat-other")
	w.hook("UserPromptSubmit", TapiSlug, map[string]any{"prompt": "hi"})
	w.message(TapiSlug, "turn-one-0000", "msg-one-0000", "おはよ。\n", "今日はなにからやろっか。")
	w.message(TapiSlug, "turn-one-0000", "msg-two-0000", "もう一つ。")
	w.hook("Stop", TapiSlug, map[string]any{"last_assistant_message": "もう一つ。"})
	wantRead(t, "tapi not in front", w.read())
	line := `voice: read of +tapi: not read in turn turn-one: mode narrate (focus "feat-other")`
	eventually(t, "the reason in pekoed.log", func() bool { return strings.Contains(buf.String(), line) })
	if n := strings.Count(buf.String(), "voice: read of +tapi"); n != 1 {
		t.Errorf("%d lines for the turn, want 1:\n%s", n, buf.String())
	}

	w.focus(TapiSlug)
	w.hook("UserPromptSubmit", TapiSlug, map[string]any{"prompt": "again"})
	w.message(TapiSlug, "turn-two-0000", "msg-1", "聞こえる。")
	wantRead(t, "tapi in front", w.read(), "聞こえる。")
	if n := strings.Count(buf.String(), "voice: read of +tapi"); n != 1 {
		t.Errorf("a line for a turn that was read:\n%s", buf.String())
	}
}
