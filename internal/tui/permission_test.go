package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/state"
)

// The permission modal is global: any session's prompt and tapi chat's come
// up on top of whatever shows, one at a time, without typing breaking in.

func permSess(slug string, since time.Time, tool string) *state.Session {
	s := sess(slug, appA, state.Waiting, since)
	s.WaitKind = "permission"
	s.PendingTool = &state.Tool{Name: "Bash", Input: tool}
	return s
}

func sessionEvent(t *testing.T, m *model, ev string, s *state.Session) {
	t.Helper()
	data, _ := json.Marshal(s)
	m.Update(evMsg(client.Event{Event: ev, Data: data}))
}

func tapiEvent(t *testing.T, m *model, v daemon.Tapi) {
	t.Helper()
	data, _ := json.Marshal(v)
	m.Update(evMsg(client.Event{Event: "tapi.updated", Data: data}))
}

func tapiPermission(since time.Time) daemon.Tapi {
	return daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Waiting, WaitKind: "permission",
		PendingTool: &state.Tool{Name: "Bash", Input: "gh pr list"}, StateSince: since}
}

func shownPerm(m *model) string {
	if p, ok := m.modal.(*permissionModal); ok {
		return p.slug
	}
	return ""
}

// A session that is not selected waits for a permission: its modal comes
// up over the selected one, names it, and y answers it without moving the
// selection.
func TestPermissionModalForUnselectedSession(t *testing.T) {
	m, f, _ := chatModel(t)
	other := permSess("api-2", time.Now(), "rm -rf build")
	other.Repo, other.Branch = "api", "feat/other"
	sessionEvent(t, m, "session.waiting", other)
	if got := shownPerm(m); got != "api-2" {
		t.Fatalf("modal for %q (%T)", got, m.modal)
	}
	out := ansi.Strip(strings.Join(m.modal.view(), "\n"))
	if !strings.Contains(out, "api · feat/other") || !strings.Contains(out, "session api-2") {
		t.Fatalf("modal doesn't say whose:\n%s", out)
	}
	runCmd(m, m.onKey(key("y")))
	got := f.find("session.answer")
	if len(got) != 1 || got[0]["slug"] != "api-2" || got[0]["answer"] != "allow" {
		t.Fatalf("session.answer %v", got)
	}
	if s := m.current(); s == nil || s.Slug != "api-1" {
		t.Fatalf("selection moved to %v", s)
	}
	if m.modal != nil {
		t.Fatalf("modal %T after answering", m.modal)
	}
}

// tapi chat's permission prompt comes up as well, with tapi chat closed or
// open; y answers it with tapi.answer (naming the tool shown: pekoed checks
// it, TestTapiAnswer) and nothing goes to a session.
func TestPermissionModalForTapi(t *testing.T) {
	for _, chatOpen := range []bool{false, true} {
		m, f, _ := chatModel(t)
		m.chat.open = chatOpen
		tapiEvent(t, m, tapiPermission(time.Now()))
		if got := shownPerm(m); got != daemon.TapiSlug {
			t.Fatalf("chat open %v: modal for %q (%T)", chatOpen, got, m.modal)
		}
		if out := ansi.Strip(strings.Join(m.modal.view(), "\n")); !strings.Contains(out, "tapi chat") || !strings.Contains(out, "gh pr list") {
			t.Fatalf("chat open %v: modal:\n%s", chatOpen, out)
		}
		if m.tapi.toast != nil && m.tapi.toast.slug == daemon.TapiSlug {
			t.Fatalf("chat open %v: toast and modal both say it", chatOpen)
		}
		runCmd(m, m.onKey(key("y")))
		got := f.find("tapi.answer")
		if len(got) != 1 || got[0]["answer"] != "allow" {
			t.Fatalf("chat open %v: tapi.answer %v", chatOpen, got)
		}
		if tool, _ := got[0]["expect"].(map[string]any)["tool"].(map[string]any); tool["input"] != "gh pr list" {
			t.Fatalf("chat open %v: expect %v", chatOpen, got[0]["expect"])
		}
		if len(f.find("session.answer"))+len(f.find("tapi.keys")) != 0 {
			t.Fatalf("chat open %v: answered elsewhere too", chatOpen)
		}
		if m.chat.open != chatOpen || m.typing {
			t.Fatalf("chat open %v: screen switched: open %v typing %v", chatOpen, m.chat.open, m.typing)
		}
		// the state event has not come yet: the same prompt stays down
		m.Update(tickMsg{})
		if m.modal != nil {
			t.Fatalf("chat open %v: answered prompt came back: %T", chatOpen, m.modal)
		}
	}
}

// Several at once: the oldest first, the next as soon as one is answered.
func TestPermissionModalQueue(t *testing.T) {
	m, _, _ := chatModel(t)
	now := time.Now()
	sessionEvent(t, m, "session.waiting", permSess("api-3", now.Add(-time.Minute), "make"))
	// the older one arrived second: it still goes first once the shown one is done
	sessionEvent(t, m, "session.waiting", permSess("api-2", now.Add(-2*time.Minute), "ls"))
	tapiEvent(t, m, tapiPermission(now))
	if got := shownPerm(m); got != "api-3" {
		t.Fatalf("first: %q", got)
	}
	if out := ansi.Strip(strings.Join(m.modal.view(), "\n")); !strings.Contains(out, "3 waiting") {
		t.Fatalf("count missing:\n%s", out)
	}
	runCmd(m, m.onKey(key("n")))
	if got := shownPerm(m); got != "api-2" {
		t.Fatalf("second: %q", got)
	}
	runCmd(m, m.onKey(key("esc")))
	if got := shownPerm(m); got != daemon.TapiSlug {
		t.Fatalf("third: %q", got)
	}
}

// Typing into [4] holds the modal back (y and n would be typed into it);
// leaving the pty brings it up at once.
func TestPermissionModalWaitsForTyping(t *testing.T) {
	m, f, _ := chatModel(t)
	m.focus, m.typing = pAgent, true
	sessionEvent(t, m, "session.waiting", permSess("api-2", time.Now(), "ls"))
	m.Update(tickMsg{})
	if m.modal != nil {
		t.Fatalf("modal %T while typing", m.modal)
	}
	runCmd(m, m.onKey(key("y")))
	if len(f.find("session.answer")) != 0 {
		t.Fatal("a typed y answered the prompt")
	}
	m.Update(key("ctrl+q"))
	if m.typing {
		t.Fatal("still typing after the prefix")
	}
	if got := shownPerm(m); got != "api-2" {
		t.Fatalf("after leaving: modal %q (%T)", got, m.modal)
	}
}

// esc dismisses a wait until it waits again; the others still come up.
func TestPermissionModalDismiss(t *testing.T) {
	m, _, _ := chatModel(t)
	at := time.Now().Add(-time.Minute)
	sessionEvent(t, m, "session.waiting", permSess("api-2", at, "ls"))
	runCmd(m, m.onKey(key("esc")))
	m.Update(tickMsg{})
	sessionEvent(t, m, "session.updated", permSess("api-2", at, "ls"))
	if m.modal != nil {
		t.Fatalf("dismissed wait came back: %T", m.modal)
	}
	// tapi's dismissed the same way
	tapiEvent(t, m, tapiPermission(at))
	if got := shownPerm(m); got != daemon.TapiSlug {
		t.Fatalf("tapi: %q", got)
	}
	runCmd(m, m.onKey(key("esc")))
	tapiEvent(t, m, tapiPermission(at))
	if m.modal != nil {
		t.Fatalf("dismissed tapi wait came back: %T", m.modal)
	}
	// a new wait of the same session comes up again
	sessionEvent(t, m, "session.waiting", permSess("api-2", time.Now().Add(time.Second), "make"))
	if got := shownPerm(m); got != "api-2" {
		t.Fatalf("new wait: %q", got)
	}
}

// Any text being typed holds the modal back, not only the pty: a pick's
// instruction in [4], the [1] filter, the Issues search. Leaving it brings
// the modal up at once.
func TestPermissionModalWaitsForTextEntry(t *testing.T) {
	cases := []struct {
		name  string
		enter func(m *model)
		leave tea.KeyMsg
	}{
		{"pick instruction", func(m *model) {
			m.picks = []*state.Pick{{ID: 7, Slug: "api-1", File: "x.json"}}
			m.focus = pAgent
			m.ensurePickInput()
		}, tea.KeyMsg{}},
		{"filter", func(m *model) { runCmd(m, m.onKey(key("/"))) }, key("enter")},
	}
	for _, c := range cases {
		m, f, _ := chatModel(t)
		c.enter(m)
		if !m.textEntry() {
			t.Fatalf("%s: not text entry", c.name)
		}
		sessionEvent(t, m, "session.waiting", permSess("api-2", time.Now(), "ls"))
		m.Update(tickMsg{})
		if m.modal != nil {
			t.Fatalf("%s: modal %T while typing", c.name, m.modal)
		}
		m.Update(key("y"))
		m.Update(key("n"))
		if len(f.find("session.answer")) != 0 {
			t.Fatalf("%s: a typed y / n answered the prompt", c.name)
		}
		if c.name == "pick instruction" {
			if got := m.pickInput.String(); got != "yn" {
				t.Fatalf("pick instruction %q", got)
			}
			m.focus = pSessions // leave the card
			m.Update(tickMsg{})
		} else {
			m.Update(c.leave)
		}
		if got := shownPerm(m); got != "api-2" {
			t.Fatalf("%s: after leaving: modal %q (%T)", c.name, got, m.modal)
		}
	}
}

// A prompt put down stays down only while it is the same prompt: the
// session moving on to another tool in the same wait (an answer refused as
// the prompt changed) comes up again.
func TestPermissionModalDismissedPromptChanged(t *testing.T) {
	m, _, _ := chatModel(t)
	at := time.Now().Add(-time.Minute)
	sessionEvent(t, m, "session.waiting", permSess("api-2", at, "ls"))
	runCmd(m, m.onKey(key("y")))
	m.Update(tickMsg{})
	if m.modal != nil {
		t.Fatalf("answered prompt came back: %T", m.modal)
	}
	sessionEvent(t, m, "session.updated", permSess("api-2", at, "make deploy"))
	if got := shownPerm(m); got != "api-2" {
		t.Fatalf("another prompt in the same wait: modal %q", got)
	}
}

// The prefix that left typing brings a held-back prompt up at once; the
// modal takes the key after it, so the prefix is spent: after esc an = is
// not a layout key (it would reset the saved pane width), and tapi chat
// closed by the prefix does not come back on z.
func TestPermissionModalDisarmsPrefix(t *testing.T) {
	m, _, _ := chatModel(t)
	m.focus, m.typing = pAgent, true
	m.layout = Layout{Left: 0.5} // a saved width
	sessionEvent(t, m, "session.waiting", permSess("api-2", time.Now(), "ls"))
	upd(m, ctrlQ)
	if shownPerm(m) != "api-2" {
		t.Fatalf("modal %T", m.modal)
	}
	upd(m, key("esc"))
	if m.modal != nil || m.prefix || m.prefixType || m.chat.reopen {
		t.Fatalf("after esc: modal %T prefix %v prefixType %v reopen %v", m.modal, m.prefix, m.prefixType, m.chat.reopen)
	}
	upd(m, key("="))
	if m.layout != (Layout{Left: 0.5}) {
		t.Errorf("= after the modal reset the layout: %+v", m.layout)
	}

	// from tapi chat: z after the modal does not open it again
	m, _, _ = chatModel(t)
	runCmd(m, m.onKey(key("t")))
	if !m.chat.open || !m.typing {
		t.Fatalf("chat %v typing %v", m.chat.open, m.typing)
	}
	sessionEvent(t, m, "session.waiting", permSess("api-2", time.Now(), "ls"))
	upd(m, ctrlQ)
	if shownPerm(m) != "api-2" {
		t.Fatalf("tapi chat: modal %T", m.modal)
	}
	upd(m, key("esc"))
	upd(m, key("z"))
	if m.chat.open {
		t.Error("z after the modal opened tapi chat again")
	}
}

// upd is a key or message through Update, its command run.
func upd(m *model, msg tea.Msg) {
	_, cmd := m.Update(msg)
	runCmd(m, cmd)
}
