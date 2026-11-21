package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/state"
)

// eventually waits for ok (settleInterrupt reads the screen every
// interruptPoll).
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after 3s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sessionState(t *testing.T, d *Daemon) state.State {
	t.Helper()
	return call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}).State
}

// Claude Code sends no Stop for a turn interrupted with esc. After an
// Interrupt pekoed reads the screen: while the turn is on it the session
// stays running, once it is gone (the idle input box) it is idle, so it
// no longer counts against max_running and its Interrupt is not offered.
func TestInterruptSettlesSession(t *testing.T) {
	d, fe, repo := setup(t)
	runningSession(t, d, repo)
	call[state.Session](t, d, "session.interrupt", map[string]any{"slug": "main"})
	time.Sleep(2 * interruptPoll) // the spinner is still there: still running
	if st := sessionState(t, d); st != state.Running {
		t.Fatalf("state %s with the turn on the screen, want running", st)
	}
	showBusy(fe, false) // "Interrupted · What should Claude do instead?"
	eventually(t, "idle after the interrupt", func() bool { return sessionState(t, d) == state.Idle })
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if working(&s) {
		t.Fatalf("an interrupted session counts against max_running: %+v", s.State)
	}
	if !d.typedRecently("main") {
		t.Fatalf("an interrupt is not a person's key: inbox answers would go at once")
	}

	// a hook after the Escape wins: the agent works again (another
	// prompt) and its screen is not read for the old turn
	d.interrupted = interrupts{}
	showBusy(fe, true)
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "go on"})
	call[state.Session](t, d, "session.interrupt", map[string]any{"slug": "main"})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "and then"})
	showBusy(fe, false)
	time.Sleep(3 * interruptPoll)
	if st := sessionState(t, d); st != state.Running {
		t.Fatalf("state %s after a prompt that came after the Escape, want running", st)
	}

	// a permission prompt the screen shows after the Escape is the hooks'
	// to report: not idle
	d.interrupted = interrupts{}
	showBusy(fe, true)
	call[state.Session](t, d, "session.interrupt", map[string]any{"slug": "main"})
	fe.mu.Lock()
	fe.lines = []string{"Bash(rm -rf x)", "Do you want to proceed?", "❯ 1. Yes", "  2. No"}
	fe.mu.Unlock()
	time.Sleep(3 * interruptPoll)
	if st := sessionState(t, d); st != state.Running {
		t.Fatalf("state %s with a permission prompt on the screen, want running", st)
	}
	showBusy(fe, false)
	eventually(t, "idle once the prompt is gone", func() bool { return sessionState(t, d) == state.Idle })
}

// After an Interrupt of tapi, tapi goes idle once its screen says the
// turn is over, and pekoed's deliveries go on (they wait for an idle
// tapi: deliverHeld) — not at once: the Escape is a person's key, and a
// delivery right after it would start the agent they just stopped.
func TestTapiInterruptSettlesAndDelivers(t *testing.T) {
	d, fe := deliverSetup(t, "idle")
	showBusy(fe, true)
	event(t, d, "UserPromptSubmit", TapiSlug, map[string]any{"prompt": "hi"})
	noPause(d)
	tapiChange(d, kindReport, "feat-x", TapiSlug, "report done")
	call[Tapi](t, d, "tapi.interrupt", nil)
	showBusy(fe, false)
	eventually(t, "tapi idle after the interrupt", func() bool {
		st, _ := d.tapiNow()
		return st == state.Idle
	})
	d.deliverTapi()
	if got := delivered(fe, 0); len(got) != 0 {
		t.Fatalf("delivered right after the interrupt: %q", got)
	}
	if v := call[Tapi](t, d, "tapi.get", nil); v.Deliver.Held != heldTyping {
		t.Fatalf("held %q after the interrupt, want %q", v.Deliver.Held, heldTyping)
	}
	noPause(d) // inboxTypedQuiet later
	eventually(t, "the report delivered", func() bool {
		d.deliverTapi()
		return len(delivered(fe, 0)) == 1
	})
}

// interruptDuringPaste runs send and, once its text is typed (before its
// Enter), interrupt from another goroutine; it answers what reached the
// agent window, in order.
func interruptDuringPaste(t *testing.T, fe *fakeEngine, send func(), interrupt func() error) []string {
	t.Helper()
	before := sentCount(fe)
	var wg sync.WaitGroup
	var once sync.Once
	fe.mu.Lock()
	fe.onText = func(string) {
		once.Do(func() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := interrupt(); err != nil {
					t.Errorf("interrupt: %v", err)
				}
			}()
			time.Sleep(30 * time.Millisecond) // the interrupt's turn, if nothing holds it
		})
	}
	fe.mu.Unlock()
	send()
	wg.Wait()
	fe.mu.Lock()
	fe.onText = nil
	fe.mu.Unlock()
	return sentSince(fe, before)
}

// tapi.interrupt waits for a tapi.send's Enter, as session.interrupt does
// for session.send: the Escape never lands between a paste and its Enter.
func TestInterruptWaitsForSend(t *testing.T) {
	d, fe, repo := setup(t)
	runningSession(t, d, repo)
	got := interruptDuringPaste(t, fe, func() {
		call[any](t, d, "session.send", map[string]any{"slug": "main", "text": "also do y", "submit": true})
	}, func() error {
		_, err := d.Call("session.interrupt", mustJSON(map[string]any{"slug": "main"}))
		return err
	})
	if strings.Join(got, "|") != "text:also do y|keys:Enter|keys:Escape" {
		t.Fatalf("session: %v", got)
	}
	call[Tapi](t, d, "tapi.open", nil)
	event(t, d, "UserPromptSubmit", TapiSlug, map[string]any{"prompt": "hi"})
	got = interruptDuringPaste(t, fe, func() {
		call[any](t, d, "tapi.send", map[string]any{"text": "also do y", "submit": true})
	}, func() error {
		_, err := d.Call("tapi.interrupt", nil)
		return err
	})
	if strings.Join(got, "|") != "text:also do y|keys:Enter|keys:Escape" {
		t.Fatalf("tapi: %v (an Escape between the paste and its Enter)", got)
	}
}

// pekoed's delivery line and a person's tapi.send are one at a time: the
// person's text never lands in the input box between the line and its
// Enter (one prompt that starts with "[pekoe]", which tapi reads as
// untrusted data and the ledger does not take for a request).
func TestDeliverAndTapiSendDoNotMix(t *testing.T) {
	d, fe := deliverSetup(t, "idle")
	tapiChange(d, kindReport, "feat-x", TapiSlug, "report done")
	noPause(d)
	n := sentCount(fe)
	var wg sync.WaitGroup
	var once sync.Once
	fe.mu.Lock()
	fe.onText = func(text string) {
		if !strings.HasPrefix(text, "[pekoe] ") {
			return
		}
		once.Do(func() {
			// the person presses send on the Web UI / GUI right now
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := d.Call("tapi.send", mustJSON(tapiSendP{Text: "README を直して", Submit: true})); err != nil {
					t.Errorf("tapi.send: %v", err)
				}
			}()
			time.Sleep(30 * time.Millisecond)
		})
	}
	fe.mu.Unlock()
	d.deliverTapi()
	wg.Wait()
	fe.mu.Lock()
	fe.onText = nil
	fe.mu.Unlock()
	got := sentSince(fe, n)
	var box, prompts []string
	for _, s := range got {
		switch {
		case strings.HasPrefix(s, "text:"):
			box = append(box, strings.TrimPrefix(s, "text:"))
		case s == "keys:Enter":
			prompts = append(prompts, strings.Join(box, ""))
			box = nil
		}
	}
	if len(prompts) != 2 || !strings.HasPrefix(prompts[0], "[pekoe] ") || prompts[1] != "README を直して" {
		t.Fatalf("submitted %q (typed %q), want the delivery line, then the person's text", prompts, got)
	}
}

// A delivery that finds a person's send under way (it checked before the
// person took tapi's send lock) types nothing: it asks again under the
// lock, and the person's text is the only prompt.
func TestDeliverAsksAgainUnderSendLock(t *testing.T) {
	d, fe := deliverSetup(t, "idle")
	tapiChange(d, kindReport, "feat-x", TapiSlug, "report done")
	noPause(d)
	n := sentCount(fe)
	mu, _ := d.sendLocks.LoadOrStore(TapiSlug, &sync.Mutex{})
	mu.(*sync.Mutex).Lock() // a person's tapi.send typing
	done := make(chan struct{})
	go func() { defer close(done); d.deliverTapi() }()
	time.Sleep(50 * time.Millisecond)
	d.noteTyped(TapiSlug) // what the person's tapi.send noted
	mu.(*sync.Mutex).Unlock()
	<-done
	if got := sentSince(fe, n); len(got) != 0 {
		t.Fatalf("the delivery typed %v after a person's send", got)
	}
	if v := call[Tapi](t, d, "tapi.get", nil); v.Deliver.Held != heldTyping {
		t.Fatalf("held %q, want %q", v.Deliver.Held, heldTyping)
	}
}
