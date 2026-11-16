package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
// open; y types [agents.claude] allow into tapi chat (after checking the
// prompt is still the one shown) and nothing goes to a session.
func TestPermissionModalForTapi(t *testing.T) {
	for _, chatOpen := range []bool{false, true} {
		m, f, cr := chatModel(t)
		since := time.Now()
		f.reply = func(method string) any {
			if method == "tapi.get" {
				return tapiPermission(since)
			}
			return cr.reply(method)
		}
		m.chat.open = chatOpen
		tapiEvent(t, m, tapiPermission(since))
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
		keys := f.find("tapi.keys")
		if len(keys) != 1 || keys[0]["literal"] != "1" {
			t.Fatalf("chat open %v: tapi.keys %v", chatOpen, keys)
		}
		if len(f.find("session.answer")) != 0 {
			t.Fatalf("chat open %v: answered a session", chatOpen)
		}
		if m.chat.open != chatOpen || m.typing {
			t.Fatalf("chat open %v: screen switched: open %v typing %v", chatOpen, m.chat.open, m.typing)
		}
		// tapi's hooks have not said it moved on yet: the same prompt stays down
		m.Update(tickMsg{})
		if m.modal != nil {
			t.Fatalf("chat open %v: answered prompt came back: %T", chatOpen, m.modal)
		}
	}

	// the prompt changed meanwhile: no keys
	m, f, cr := chatModel(t)
	since := time.Now()
	f.reply = func(method string) any {
		if method == "tapi.get" {
			v := tapiPermission(since)
			v.PendingTool.Input = "rm -rf /"
			return v
		}
		return cr.reply(method)
	}
	tapiEvent(t, m, tapiPermission(since))
	runCmd(m, m.onKey(key("y")))
	if keys := f.find("tapi.keys"); len(keys) != 0 {
		t.Fatalf("changed prompt answered: %v", keys)
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
