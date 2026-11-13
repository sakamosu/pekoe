package tui

import (
	"bufio"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/client"
	"pekoe/internal/state"
	"pekoe/internal/testenv"
)

// callServer is a pekoed stand-in that records the calls and answers each
// with a null result (inbox.get with the item it was given).
type callServer struct {
	mu    sync.Mutex
	calls []string // method + " " + params as JSON
	item  state.InboxItem
}

func (cs *callServer) called(method string) []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var out []string
	for _, c := range cs.calls {
		if strings.HasPrefix(c, method+" ") {
			out = append(out, strings.TrimPrefix(c, method+" "))
		}
	}
	return out
}

func startCallServer(t *testing.T, it state.InboxItem) (*callServer, *client.Client) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s") // in memory (testenv.Listen)
	l := testenv.Listen(sock)
	t.Cleanup(func() { l.Close() })
	oldDial := client.DialSocket
	client.DialSocket = l.Dial
	t.Cleanup(func() { client.DialSocket = oldDial })
	cs := &callServer{item: it}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req struct {
						ID     int64           `json:"id"`
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					cs.mu.Lock()
					cs.calls = append(cs.calls, req.Method+" "+string(req.Params))
					var res any
					if req.Method == "inbox.get" {
						res = cs.item
					}
					cs.mu.Unlock()
					b, _ := json.Marshal(map[string]any{"id": req.ID, "result": res})
					c.Write(append(b, '\n'))
				}
			}()
		}
	}()
	t.Setenv("PEKOE_SOCK", sock)
	cl, err := client.Dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cs, cl
}

// mergeItem is pekoed's "evidence ready to merge" question.
func mergeItem(id int64) state.InboxItem {
	return state.InboxItem{ID: id, Rev: 1, Kind: "approve", From: "pekoed", About: "feat-x", Status: state.InboxOpen,
		Question: "evidence ready to merge: PR #42", Options: []string{"merge", "wait", "send back"},
		Facts:     &state.InboxFacts{Slug: "feat-x", State: state.Landed, Branch: "feat/x", Head: "0123456789abcdef", PR: 42},
		CreatedAt: time.Now()}
}

// popupModel is a model that saw no key for a while.
func popupModel(t *testing.T) *model {
	m := wrapModel(t, "feat-x")
	m.onInboxList(nil)
	m.lastKeyAt = time.Now().Add(-time.Minute)
	return m
}

// past puts the popup's grace (and the beat after choosing) behind it.
func past(im *inboxModal) {
	im.shownAt = time.Now().Add(-time.Minute)
	if !im.selAt.IsZero() {
		im.selAt = time.Now().Add(-time.Minute)
	}
}

func popupOf(t *testing.T, m *model) *inboxModal {
	t.Helper()
	im, ok := m.modal.(*inboxModal)
	if !ok || !im.popup {
		t.Fatalf("no popup: %T %+v", m.modal, m.modal)
	}
	return im
}

// A new question an agent waits on comes up in front with skip chosen,
// and is answered there through inbox.answer.
func TestInboxPopupAnswers(t *testing.T) {
	cs, cl := startCallServer(t, mergeItem(7))
	m := popupModel(t)
	m.cl = cl
	inboxEv(t, m, mergeItem(7))
	if m.modal != nil {
		t.Fatal("came up before the tick")
	}
	m.maybeInboxPopup(time.Now())
	im := popupOf(t, m)
	view := ansi.Strip(strings.Join(im.view(), "\n"))
	if !strings.Contains(view, "› -  skip") || strings.Contains(view, "› 1") {
		t.Fatalf("skip is not the first choice:\n%s", view)
	}
	past(im)
	im.key(key("1"))
	past(im)
	cmd := im.key(key("enter"))
	if cmd == nil || !im.sending {
		t.Fatalf("Enter after choosing merge does not answer: %q", im.err)
	}
	m.onInboxAnswer(cmd().(inboxAnswerMsg))
	if m.modal != nil {
		t.Fatal("popup left open after the answer")
	}
	got := cs.called("inbox.answer")
	if len(got) != 1 || !strings.Contains(got[0], `"choice":0`) || !strings.Contains(got[0], `"id":7`) || !strings.Contains(got[0], `"rev":1`) {
		t.Fatalf("inbox.answer: %v", got)
	}
}

// Enter alone, Enter over and over, and Esc skip: nothing is answered, the
// item stays in the inbox (the bar still asks for it, ]a still goes to it)
// and it never comes up again.
func TestInboxPopupSkip(t *testing.T) {
	for _, k := range []string{"enter", "esc"} {
		cs, cl := startCallServer(t, mergeItem(7))
		m := popupModel(t)
		m.cl = cl
		inboxEv(t, m, mergeItem(7))
		m.maybeInboxPopup(time.Now())
		im := popupOf(t, m)
		past(im)
		cmd := im.key(key(k))
		if cmd == nil || im.sending {
			t.Fatalf("%s: does not skip", k)
		}
		if _, ok := cmd().(modalDoneMsg); !ok {
			t.Fatalf("%s: not closed", k)
		}
		m.Update(modalDoneMsg{})
		for range 3 { // Enter mashed on: still nothing answered
			m.Update(key("enter"))
		}
		if len(cs.called("inbox.answer")) != 0 || len(cs.called("inbox.dismiss")) != 0 {
			t.Fatalf("%s: answered: %v", k, cs.calls)
		}
		if len(m.inbox) != 1 || !strings.Contains(m.msg, "stays in the inbox") {
			t.Fatalf("%s: %+v %q", k, m.inbox, m.msg)
		}
		if text, _, ok := m.orchWait(); !ok || !strings.Contains(text, "asks · ]a") {
			t.Fatalf("%s: the bar: %q", k, text)
		}
		m.lastKeyAt = time.Now().Add(-time.Minute)
		m.maybeInboxPopup(time.Now())
		if m.modal != nil {
			t.Fatalf("%s: came up again", k)
		}
		// a later update of the same item does not bring it back either
		it := mergeItem(7)
		it.Rev = 2
		inboxEv(t, m, it)
		m.maybeInboxPopup(time.Now())
		if m.modal != nil {
			t.Fatalf("%s: came up again on an update", k)
		}
		if it := m.nextInbox(); it == nil || it.ID != 7 {
			t.Fatalf("%s: ]a does not go to it", k)
		}
	}
}

// For a moment after it comes up the popup takes no key; a key it does not
// know (typing meant for an agent) sends it back to skip and waits again;
// "1⏎" in one go does not answer.
func TestInboxPopupGrace(t *testing.T) {
	m := popupModel(t)
	inboxEv(t, m, mergeItem(7))
	m.maybeInboxPopup(time.Now())
	im := popupOf(t, m)
	for _, k := range []string{"1", "enter", "esc", "x"} {
		if cmd := im.key(key(k)); cmd != nil || im.sel != -1 || im.sending || im.dismiss {
			t.Fatalf("%s during the grace was taken", k)
		}
	}
	if m.modal != im {
		t.Fatal("closed during the grace")
	}
	if !strings.Contains(ansi.Strip(strings.Join(im.view(), "\n")), "keys wait a moment") {
		t.Fatal("the grace is not shown")
	}
	past(im)
	im.key(key("1"))
	if im.sel != 0 {
		t.Fatalf("1 after the grace: %d", im.sel)
	}
	im.key(runes("a")) // typing for an agent: back to skip, wait again
	if im.sel != -1 || !im.grace(time.Now()) {
		t.Fatalf("stray key: sel %d grace %v", im.sel, im.grace(time.Now()))
	}
	if cmd := im.key(key("enter")); cmd != nil {
		t.Fatal("Enter in the new grace was taken")
	}
	past(im)
	im.key(key("1"))
	if cmd := im.key(key("enter")); cmd != nil || im.sending || !strings.Contains(im.err, "again") {
		t.Fatalf("1⏎ in one go answered: %v %q", im.sending, im.err)
	}
	if cmd := im.key(key("enter")); cmd == nil || !im.sending {
		t.Fatal("the second Enter does not answer")
	}
	// k goes up from the first option to skip
	m2 := popupModel(t)
	inboxEv(t, m2, mergeItem(8))
	m2.maybeInboxPopup(time.Now())
	im2 := popupOf(t, m2)
	past(im2)
	im2.key(key("j"))
	im2.key(key("k"))
	if im2.sel != -1 {
		t.Fatalf("k from the first option: %d", im2.sel)
	}
}

// It waits while another modal is open, while keys come (typing, an IME
// committing), while the terminal is not focused, and comes up after;
// several come one at a time, oldest first.
func TestInboxPopupWaits(t *testing.T) {
	m := popupModel(t)
	inboxEv(t, m, mergeItem(7))
	q := openItem(8)
	inboxEv(t, m, q)

	other := &confirmModal{}
	m.modal = other
	m.maybeInboxPopup(time.Now())
	if m.modal != other {
		t.Fatal("came up over another modal")
	}
	m.Update(modalDoneMsg{})

	m.typing = true
	m.Update(runes("あ")) // typed into the agent (an IME's commit too)
	m.maybeInboxPopup(time.Now())
	if m.modal != nil {
		t.Fatal("came up while keys come")
	}
	m.maybeInboxPopup(time.Now().Add(popupIdle / 2))
	if m.modal != nil {
		t.Fatal("came up before the pause")
	}

	m.tapi.focused = false
	m.maybeInboxPopup(time.Now().Add(time.Minute))
	if m.modal != nil {
		t.Fatal("came up while the terminal is not focused")
	}
	m.tapi.focused = true
	m.Update(tea.FocusMsg{})
	m.maybeInboxPopup(time.Now())
	if m.modal != nil {
		t.Fatal("came up the moment focus came back")
	}

	m.maybeInboxPopup(time.Now().Add(popupIdle))
	im := popupOf(t, m)
	if im.it.ID != 7 {
		t.Fatalf("not the oldest first: #%d", im.it.ID)
	}
	if !m.typing {
		t.Fatal("the popup left typing (Esc should go back to the agent)")
	}
	m.maybeInboxPopup(time.Now().Add(popupIdle))
	if popupOf(t, m) != im {
		t.Fatal("two at a time")
	}
	past(im)
	m.Update(key("esc"))
	m.Update(modalDoneMsg{})
	m.lastKeyAt = time.Now().Add(-time.Minute)
	m.maybeInboxPopup(time.Now())
	if popupOf(t, m).it.ID != 8 {
		t.Fatal("the next one does not follow")
	}
}

// Only questions an agent waits on come up: not fyi, not a person's own,
// not the ones there when the TUI started; one answered elsewhere before
// it came up does not either.
func TestInboxPopupWhich(t *testing.T) {
	m := wrapModel(t, "feat-x")
	m.onInboxList([]state.InboxItem{mergeItem(1)})
	m.lastKeyAt = time.Now().Add(-time.Minute)
	fyi := openItem(2)
	fyi.Kind = "fyi"
	mine := openItem(3)
	mine.From = "human"
	inboxEv(t, m, fyi)
	inboxEv(t, m, mine)
	gone := mergeItem(4)
	inboxEv(t, m, gone)
	gone.Status = state.InboxWithdrawn
	inboxEv(t, m, gone)
	m.maybeInboxPopup(time.Now())
	if m.modal != nil {
		t.Fatalf("came up: %+v", m.modal.(*inboxModal).it)
	}
	if len(m.popupDue) != 0 {
		t.Fatalf("left due: %v", m.popupDue)
	}
}

// [orchestrator] inbox_popup = false, or notify = "quiet": nothing comes
// up (the toast / message line and ]a stay as they were).
func TestInboxPopupOff(t *testing.T) {
	off := false
	for name, set := range map[string]func(m *model){
		"inbox_popup = false": func(m *model) { m.g.Orchestrator.InboxPopup = &off },
		`notify = "quiet"`:    func(m *model) { m.g.Orchestrator.Notify = "quiet" },
	} {
		m := popupModel(t)
		set(m)
		inboxEv(t, m, mergeItem(7))
		m.maybeInboxPopup(time.Now())
		if m.modal != nil {
			t.Fatalf("%s: came up", name)
		}
		if it := m.nextInbox(); it == nil || it.ID != 7 {
			t.Fatalf("%s: not in the inbox", name)
		}
	}
	// turned off while one was due: it does not come up when turned on again
	m := popupModel(t)
	inboxEv(t, m, mergeItem(7))
	m.g.Orchestrator.InboxPopup = &off
	m.maybeInboxPopup(time.Now())
	m.g.Orchestrator.InboxPopup = nil
	m.maybeInboxPopup(time.Now())
	if m.modal != nil {
		t.Fatal("came up after being turned off and on")
	}
}
