package tui

import (
	"encoding/json"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/engine"
)

// These tests pin who eats a key first in onKey: modal → typing (chat, the
// prefix, a dead pane, the pty) → the spent prefix → the filter → the issue
// query → [ ] → global keys → the focused panel. The order is the behaviour.

// An open modal takes every key, even while typing: nothing reaches the pty
// and the prefix does not leave typing.
func TestKeyOrderModalBeforeTyping(t *testing.T) {
	m, f, _ := chatModel(t)
	m.focus, m.typing = pAgent, true
	m.modal = newHelpModal(m)
	for _, k := range []tea.KeyMsg{key("a"), key("w"), ctrlQ} {
		runCmd(m, m.onKey(k))
	}
	if !m.typing || m.prefix || m.prefixType {
		t.Fatalf("the modal should keep the keys: typing %v prefix %v", m.typing, m.prefix)
	}
	if n := len(f.find("session.keys")); n != 0 {
		t.Fatalf("keys reached the pty under a modal: %d", n)
	}
	// the filter waits behind the modal too
	m.typing, m.filterOn = false, true
	runCmd(m, m.onKey(key("x")))
	if m.filter != "" {
		t.Fatalf("filter took a key under a modal: %q", m.filter)
	}
}

// While typing, every key but the prefix goes to the pty: global keys,
// [ ], the filter, panel keys.
func TestKeyOrderTypingForwardsGlobals(t *testing.T) {
	m, f, _ := chatModel(t)
	m.focus, m.typing = pAgent, true
	m.filterOn = true // left on: typing still comes first
	for _, k := range []string{"q", "1", "?", ":", "]", "w", "t", "H", ".", "esc"} {
		runCmd(m, m.onKey(key(k)))
	}
	if m.modal != nil || m.focus != pAgent || m.bracket != "" || m.chat.open || !m.typing || m.filter != "" {
		t.Fatalf("a key escaped the pty: modal %v focus %d bracket %q chat %v typing %v filter %q",
			m.modal != nil, m.focus, m.bracket, m.chat.open, m.typing, m.filter)
	}
	ks := f.find("session.keys")
	if len(ks) != 10 || ks[0]["literal"] != "q" || ks[4]["literal"] != "]" {
		t.Fatalf("session.keys: %v", ks)
	}
	if esc, _ := json.Marshal(ks[9]["keys"]); string(esc) != `["Escape"]` {
		t.Fatalf("esc: %v", ks[9])
	}
}

// The prefix leaves typing: to [1] unless zoomed on a wide screen (the
// left column is hidden), and is remembered as coming from typing.
func TestKeyOrderTypingPrefix(t *testing.T) {
	for _, c := range []struct {
		width int
		zoom  bool
		focus int
	}{
		{140, false, pSessions},
		{140, true, pAgent},
		{90, true, pSessions}, // narrow: zoom does not hide anything
	} {
		m, f, _ := chatModel(t)
		m.width = c.width
		m.layout.Zoom = c.zoom
		m.focus, m.typing = pAgent, true
		if cmd := m.onKey(ctrlQ); cmd != nil {
			t.Errorf("%+v: the prefix returned a command", c)
		}
		if m.typing || m.focus != c.focus || !m.prefix || !m.prefixType {
			t.Errorf("%+v: typing %v focus %d prefix %v fromTyping %v", c, m.typing, m.focus, m.prefix, m.prefixType)
		}
		if n := len(f.find("session.keys")); n != 0 {
			t.Errorf("%+v: the prefix reached the pty", c)
		}
	}
}

// A dead pane while typing: ↵ restarts (agent) or restarts the dev server,
// x / esc end the session or close the window, other keys do nothing.
func TestKeyOrderTypingDeadPane(t *testing.T) {
	type want struct {
		method string
		typing bool
		window string
	}
	cases := []struct {
		window string
		key    string
		want   want
	}{
		{engine.WinAgent, "enter", want{"session.restart", true, engine.WinAgent}},
		{engine.WinAgent, "x", want{"session.kill", false, engine.WinAgent}},
		{engine.WinAgent, "esc", want{"session.kill", false, engine.WinAgent}},
		{engine.WinAgent, "a", want{"", true, engine.WinAgent}},
		{engine.WinDev, "enter", want{"dev.toggle", true, engine.WinAgent}},
		{engine.WinDev, "esc", want{"window.kill", false, engine.WinAgent}},
		{engine.WinDev, "q", want{"", true, engine.WinDev}},
	}
	for _, c := range cases {
		m, f, _ := chatModel(t)
		m.focus, m.typing = pAgent, true
		m.window = c.window
		m.screen = &engine.Screen{Dead: true}
		runCmd(m, m.onKey(key(c.key)))
		if c.want.method != "" && len(f.find(c.want.method)) != 1 {
			t.Errorf("%s %s: no %s", c.window, c.key, c.want.method)
		}
		for _, mt := range []string{"session.restart", "session.kill", "dev.toggle", "window.kill", "session.keys"} {
			if mt != c.want.method && len(f.find(mt)) != 0 {
				t.Errorf("%s %s: unexpected %s", c.window, c.key, mt)
			}
		}
		if m.typing != c.want.typing || m.window != c.want.window || m.modal != nil {
			t.Errorf("%s %s: typing %v window %q modal %v", c.window, c.key, m.typing, m.window, m.modal != nil)
		}
	}
}

// After the prefix (not from typing) l / h move focus, ahead of the filter;
// any other key falls through to the filter, then to the global keys.
func TestKeyOrderPrefixBeforeFilter(t *testing.T) {
	m, _, _ := chatModel(t)
	m.focus = pSessions
	m.onKey(ctrlQ) // not typing: the global prefix check
	if !m.prefix || m.prefixType {
		t.Fatalf("prefix %v fromTyping %v", m.prefix, m.prefixType)
	}
	m.filterOn = true
	m.onKey(key("l"))
	if m.focus != pAgent || m.filter != "" || m.prefix {
		t.Fatalf("<prefix> l: focus %d filter %q prefix %v", m.focus, m.filter, m.prefix)
	}
	m.onKey(ctrlQ) // the filter is on: it eats ^q (a control key, ignored)
	if m.prefix {
		t.Fatal("with the filter on, the prefix is not reached")
	}
	m.prefix = true
	m.onKey(key("h"))
	if m.focus != pSessions || m.filter != "" {
		t.Fatalf("<prefix> h: focus %d filter %q", m.focus, m.filter)
	}
	m.prefix = true
	m.onKey(key("x"))
	if m.filter != "x" || m.prefix {
		t.Fatalf("<prefix> x with the filter on: filter %q prefix %v", m.filter, m.prefix)
	}

	// from typing, l is a plain key: it reaches the panel, not "focus [4]"
	m, _, _ = chatModel(t)
	m.focus, m.typing = pAgent, true
	m.onKey(ctrlQ)
	m.filterOn = true
	m.onKey(key("l"))
	if m.filter != "l" || m.prefix || m.prefixType {
		t.Fatalf("^q l from typing: filter %q prefix %v", m.filter, m.prefix)
	}

	// a spent prefix falls through to the global keys
	m, _, _ = chatModel(t)
	m.focus = pSessions
	m.onKey(ctrlQ)
	m.onKey(key("?"))
	if _, ok := m.modal.(*helpModal); !ok || m.prefix {
		t.Fatalf("<prefix> ?: modal %T prefix %v", m.modal, m.prefix)
	}
}

// The filter, then the issue query, take printable keys ahead of [ ] and the
// global keys.
func TestKeyOrderFilterAndQueryBeforeGlobals(t *testing.T) {
	m, _, _ := chatModel(t)
	m.focus = pSessions
	m.filterOn = true
	m.iss.queryOn = true
	for _, k := range []string{"q", "1", "?", "]", "t", "w"} {
		m.onKey(key(k))
	}
	if m.filter != "q1?]tw" || m.iss.query != "" || m.modal != nil || m.bracket != "" || m.chat.open || m.focus != pSessions {
		t.Fatalf("filter %q query %q modal %v bracket %q chat %v focus %d", m.filter, m.iss.query, m.modal != nil, m.bracket, m.chat.open, m.focus)
	}
	m.onKey(key("esc"))
	if m.filterOn || m.filter != "" || !m.iss.queryOn {
		t.Fatalf("esc ends the filter only: filterOn %v filter %q queryOn %v", m.filterOn, m.filter, m.iss.queryOn)
	}
	for _, k := range []string{"q", "2", ":", "["} {
		m.onKey(key(k))
	}
	if m.iss.query != "q2:[" || m.modal != nil || m.bracket != "" || m.focus != pSessions {
		t.Fatalf("query %q modal %v bracket %q focus %d", m.iss.query, m.modal != nil, m.bracket, m.focus)
	}
}

// ] / [ wait for the next key: ]a ]r [a are their own; any other key turns
// the subtab, then is handled as itself. The bracket beats the global keys.
func TestKeyOrderBracket(t *testing.T) {
	m, _, _ := chatModel(t)
	m.focus = pSecond
	if m.onKey(key("]")) == nil || m.bracket != "]" {
		t.Fatalf("] should wait: bracket %q", m.bracket)
	}
	runCmd(m, m.onKey(key("1")))
	if m.bracket != "" || m.sub2 != 1 || m.focus != pSessions {
		t.Fatalf("]1: bracket %q sub2 %d focus %d", m.bracket, m.sub2, m.focus)
	}

	// the timeout turns the subtab on its own, only for the latest bracket
	m.focus = pThird
	m.onKey(key("["))
	seq := m.bracketSeq
	m.updateMain(prefixTimeoutMsg{seq - 1})
	if m.bracket != "[" {
		t.Fatal("a stale timeout spent the bracket")
	}
	_, cmd := m.updateMain(prefixTimeoutMsg{seq})
	runCmd(m, cmd)
	if m.bracket != "" || m.sub3 != 1 {
		t.Fatalf("[ timeout: bracket %q sub3 %d", m.bracket, m.sub3)
	}

	// ]? turns the subtab and opens the help: the key is not lost
	m.focus = pSecond
	m.sub2 = 0
	m.onKey(key("]"))
	runCmd(m, m.onKey(key("?")))
	if _, ok := m.modal.(*helpModal); !ok || m.sub2 != 1 {
		t.Fatalf("]?: modal %T sub2 %d", m.modal, m.sub2)
	}
}

// Global keys come before the focused panel, including tapi chat shown in
// [4] (not typing) and the tapi row.
func TestKeyOrderGlobalsBeforePanel(t *testing.T) {
	m, _, _ := chatModel(t)
	m.focus = pAgent
	m.chat.open = true
	m.onKey(key("?"))
	if _, ok := m.modal.(*helpModal); !ok {
		t.Fatalf("? over tapi chat: modal %T", m.modal)
	}
	m.modal = nil
	m.onKey(key("2"))
	if m.focus != pSecond {
		t.Fatalf("2 over tapi chat: focus %d", m.focus)
	}

	m, _, _ = chatModel(t)
	m.focus = pSessions
	m.sel = tapiSel
	m.onKey(key(":"))
	if _, ok := m.modal.(*commandModal); !ok {
		t.Fatalf(": on the tapi row: modal %T", m.modal)
	}
	m.modal = nil
	m.onKey(key("tab"))
	if m.focus != pSecond {
		t.Fatalf("tab on the tapi row: focus %d", m.focus)
	}
}

// Keys with no legacy encoding (kitty) reach tapi chat or the pty only
// while typing and without a modal.
func TestKittyKeyRouting(t *testing.T) {
	m, f, _ := chatModel(t)
	m.focus = pAgent
	_, cmd := m.updateMain(kittyKeyMsg{name: "S-Enter"})
	runCmd(m, cmd)
	if len(f.find("session.keys")) != 0 || len(f.find("tapi.keys")) != 0 {
		t.Fatal("not typing: the key should go nowhere")
	}
	m.typing = true
	m.modal = newHelpModal(m)
	_, cmd = m.updateMain(kittyKeyMsg{name: "S-Enter"})
	runCmd(m, cmd)
	if len(f.find("session.keys")) != 0 {
		t.Fatal("under a modal: the key should go nowhere")
	}
	m.modal = nil
	_, cmd = m.updateMain(kittyKeyMsg{name: "S-Enter"})
	runCmd(m, cmd)
	ks := f.find("session.keys")
	if b, _ := json.Marshal(ks); len(ks) != 1 || string(b) != `[{"keys":["S-Enter"],"slug":"api-1","window":"agent"}]` {
		t.Fatalf("session.keys: %s", b)
	}
	m.chat.open = true
	_, cmd = m.updateMain(kittyKeyMsg{name: "C-Enter"})
	runCmd(m, cmd)
	if tk := f.find("tapi.keys"); len(tk) != 1 || len(f.find("session.keys")) != 1 {
		t.Fatalf("tapi.keys %v", tk)
	}
}
