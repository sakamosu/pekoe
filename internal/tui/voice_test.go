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
	"pekoe/internal/voice"
)

func voiceEv(t *testing.T, name string, data any) client.Event {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return client.Event{Event: name, Data: b}
}

// barLine is the status bar (the last line), plain.
func barLine(t *testing.T, m *model) string {
	t.Helper()
	out := strings.Split(checkSize(t, m, "bar"), "\n")
	return ansi.Strip(out[len(out)-1])
}

// The bar shows "voice" while something sounds and "muted ^q m" while
// muted (docs/voice.md 14.3); nothing while voice is off or quiet.
func TestVoicePill(t *testing.T) {
	m, _, _ := voiceModel(t, true)
	if b := barLine(t, m); strings.Contains(b, "voice") || strings.Contains(b, "muted") {
		t.Fatalf("quiet: %q", b)
	}
	st := *m.voice.st
	st.Speaking = &voice.Speaking{Slug: "api-1", Kind: voice.KindWait, Text: "api 1、確認待ちだよ"}
	runCmd(m, m.onEvent(voiceEv(t, "voice.updated", st)))
	b := barLine(t, m)
	if !strings.Contains(b, " voice ") || strings.Contains(b, "確認待ち") {
		t.Fatalf("speaking (the sentence stays off the bar): %q", b)
	}
	st.Speaking, st.MutedUntil = nil, "forever"
	runCmd(m, m.onEvent(voiceEv(t, "voice.updated", st)))
	if b := barLine(t, m); !strings.Contains(b, "muted ^q m") {
		t.Fatalf("muted: %q", b)
	}
	// a timed mute that ran out is not shown
	st.MutedUntil = time.Now().Add(-time.Minute).Format(time.RFC3339)
	runCmd(m, m.onEvent(voiceEv(t, "voice.updated", st)))
	if b := barLine(t, m); strings.Contains(b, "muted") {
		t.Fatalf("mute ran out: %q", b)
	}
	st.MutedUntil = time.Now().Add(time.Hour).Format(time.RFC3339)
	runCmd(m, m.onEvent(voiceEv(t, "voice.updated", st)))
	if b := barLine(t, m); !strings.Contains(b, "muted ^q m") {
		t.Fatalf("muted for an hour: %q", b)
	}
	// off: no pill whatever the rest says
	st.Enabled = false
	runCmd(m, m.onEvent(voiceEv(t, "voice.updated", st)))
	if b := barLine(t, m); strings.Contains(b, "muted") {
		t.Fatalf("off: %q", b)
	}
}

// voice.focus goes out voiceFocusWait after [4] or the terminal focus
// changed, only the newest one, and nothing while voice is off.
func TestVoiceFocus(t *testing.T) {
	m, f, _ := voiceModel(t, true)
	// voice found on: what [4] shows went out at once
	if fs := f.find("voice.focus"); len(fs) != 1 || fs[0]["slug"] != "api-1" || fs[0]["term_focused"] != true {
		t.Fatalf("at the start: %v", fs)
	}
	m.sessions = append(m.sessions, sess("api-2", appA, state.Idle, time.Now()))
	m.sortSessions()
	m.sel, _ = m.find("api-2")
	first := m.voiceFocusSync()
	if first == nil {
		t.Fatal("no focus scheduled")
	}
	if m.voiceFocusSync() != nil {
		t.Fatal("the same focus was scheduled twice")
	}
	stale := m.voice.gen
	m.sel = tapiSel // k: on to the tapi row before api-2 was sent
	if m.voiceFocusSync() == nil {
		t.Fatal("the move was not scheduled")
	}
	runCmd(m, m.onVoiceFocus(voiceFocusMsg{stale}))
	if n := len(f.find("voice.focus")); n != 1 {
		t.Fatalf("a stale tick sent: %d", n)
	}
	runCmd(m, m.onVoiceFocus(voiceFocusMsg{m.voice.gen}))
	runCmd(m, m.onVoiceFocus(voiceFocusMsg{m.voice.gen})) // twice: sent once
	fs := f.find("voice.focus")
	if len(fs) != 2 || fs[1]["slug"] != "" || fs[1]["term_focused"] != true {
		t.Fatalf("voice.focus: %v", fs)
	}
	m.sel, _ = m.find("api-1")
	// the tick waits voiceFocusWait
	if tm, ok := first().(timerMsg); !ok || tm.d != voiceFocusWait {
		t.Fatalf("the tick: %#v", tm)
	}

	// through Update: the terminal loses focus, tapi chat opens
	_, cmd := m.Update(tea.BlurMsg{})
	runCmd(m, cmd)
	fs = f.find("voice.focus")
	if last := fs[len(fs)-1]; len(fs) != 3 || last["term_focused"] != false || last["slug"] != "api-1" {
		t.Fatalf("blur: %v", fs)
	}
	m.Update(tea.FocusMsg{}) // its command starts tapi's animation: not run
	runCmd(m, m.onKey(key("t")))
	_, cmd = m.Update(infoMsg("")) // any message: the sync runs after it
	runCmd(m, cmd)
	fs = f.find("voice.focus")
	if last := fs[len(fs)-1]; last["slug"] != daemon.TapiSlug || last["term_focused"] != true {
		t.Fatalf("tapi chat: %v", fs)
	}
	// the dashboard shows no session
	runCmd(m, m.goDash())
	_, cmd = m.Update(infoMsg(""))
	runCmd(m, cmd)
	fs = f.find("voice.focus")
	if last := fs[len(fs)-1]; last["slug"] != "" {
		t.Fatalf("dashboard: %v", fs)
	}

	// off: nothing is sent
	m, f, _ = voiceModel(t, false)
	_, cmd = m.Update(tea.BlurMsg{})
	runCmd(m, cmd)
	if n := len(f.find("voice.focus")); n != 0 || m.voiceFocusSync() != nil {
		t.Fatalf("sent while off: %d", n)
	}
	// turned on: the focus goes out at once, though it did not change
	st := *m.voice.st
	st.Enabled = true
	runCmd(m, m.onEvent(voiceEv(t, "voice.updated", st)))
	_, cmd = m.Update(infoMsg(""))
	runCmd(m, cmd)
	if fs := f.find("voice.focus"); len(fs) != 1 || fs[0]["slug"] != "api-1" || fs[0]["term_focused"] != false {
		t.Fatalf("turned on: %v", fs)
	}
}

// :voice lists what was said, newest first; j / k move; voice.logged adds
// on top while it is open; esc / q close. :voice mute / unmute mute.
func TestVoiceLogModal(t *testing.T) {
	m, f, v := voiceModel(t, true)
	at := time.Date(2026, 11, 23, 15, 4, 5, 0, time.Local)
	v.log = []voice.LogEntry{
		{Time: at, Slug: "api-1", Kind: voice.KindWait, Text: "api 1、確認待ちだよ", Result: voice.ResultSpoken, LatencyMS: 420, Backend: "say"},
		{Time: at.Add(-time.Minute), Slug: "api-2", Kind: voice.KindDone, Text: "api 2、終わったよ", Result: voice.ResultMuted, Reason: "muted"},
	}
	m.focus = pSessions
	keys(m, key(":"), runes("voice"), kEnter)
	vm, ok := m.modal.(*voiceLogModal)
	if !ok {
		t.Fatalf("modal %T", m.modal)
	}
	if lg := f.find("voice.log"); len(lg) != 1 || lg[0]["n"] != float64(voiceLogN) {
		t.Fatalf("voice.log: %v", lg)
	}
	out := ansi.Strip(checkSize(t, m, ":voice"))
	if !strings.Contains(out, "15:04:05 spoken") || !strings.Contains(out, "確認待ちだよ") || !strings.Contains(out, "muted") || !strings.Contains(out, "say · 420ms") {
		t.Fatalf(":voice:\n%s", out)
	}
	if !strings.Contains(out, "VOICE") {
		t.Fatalf("mode:\n%s", out)
	}
	keys(m, key("j"))
	if vm.sel != 1 {
		t.Fatalf("j: sel %d", vm.sel)
	}
	runCmd(m, m.onEvent(voiceEv(t, "voice.logged", voice.LogEntry{Time: at.Add(time.Minute), Kind: voice.KindTest, Text: "テスト", Result: voice.ResultSpoken})))
	if len(vm.log) != 3 || vm.log[0].Text != "テスト" || vm.sel != 2 || vm.log[vm.sel].Text != "api 2、終わったよ" {
		t.Fatalf("logged: %d lines, top %q, sel %d (stays on its line)", len(vm.log), vm.log[0].Text, vm.sel)
	}
	keys(m, key("k"), key("k"), key("k"))
	if vm.sel != 0 {
		t.Fatalf("k: sel %d", vm.sel)
	}
	// on the top line too, the selection stays on its line
	runCmd(m, m.onEvent(voiceEv(t, "voice.logged", voice.LogEntry{Time: at.Add(2 * time.Minute), Kind: voice.KindTest, Text: "次", Result: voice.ResultSpoken})))
	if vm.sel != 1 || vm.log[1].Text != "テスト" {
		t.Fatalf("logged on the top line: sel %d", vm.sel)
	}
	keys(m, key("k"))
	if vm.sel != 0 {
		t.Fatalf("k: sel %d", vm.sel)
	}
	keys(m, key("q"))
	if m.modal != nil {
		t.Fatalf("q: modal %T", m.modal)
	}
	keys(m, key(":"), runes("voice"), kEnter, key("esc"))
	if m.modal != nil {
		t.Fatalf("esc: modal %T", m.modal)
	}

	keys(m, key(":"), runes("voice mute"), kEnter)
	if mu := f.find("voice.mute"); len(mu) != 1 || mu[0]["off"] != nil || m.modal != nil || m.msg != "voice muted · ^q m" {
		t.Fatalf(":voice mute: %v modal %T msg %q", mu, m.modal, m.msg)
	}
	keys(m, key(":"), runes("voice unmute"), kEnter)
	if mu := f.find("voice.mute"); len(mu) != 2 || mu[1]["off"] != true || m.msg != "voice on · ^q m" {
		t.Fatalf(":voice unmute: %v msg %q", mu, m.msg)
	}
	// off: :voice mute only says so
	v.mu.Lock()
	v.st.Enabled = false
	v.mu.Unlock()
	keys(m, key(":"), runes("voice mute"), kEnter)
	if mu := f.find("voice.mute"); len(mu) != 2 || m.msg != "voice is off · pekoe voice on" {
		t.Fatalf(":voice mute while off: %v msg %q", mu, m.msg)
	}
	v.mu.Lock()
	v.st.Enabled = true
	v.mu.Unlock()
	keys(m, key(":"), runes("voice loud"), kEnter)
	if c, ok := m.modal.(*commandModal); !ok || !strings.Contains(c.err, "voice:") {
		t.Fatalf(":voice loud: modal %T", m.modal)
	}
}

// Nothing said yet, or the log cannot be read: the modal says so.
func TestVoiceLogModalEmpty(t *testing.T) {
	m, _, _ := voiceModel(t, true)
	keys(m, key(":"), runes("voice"), kEnter)
	if out := ansi.Strip(checkSize(t, m, "empty")); !strings.Contains(out, "nothing said yet") {
		t.Fatalf("empty:\n%s", out)
	}
	keys(m, key("j"), key("k")) // no lines: no crash
}

// A line logged while :voice loads is kept, once even if the read has it.
func TestVoiceLogModalLoggedWhileLoading(t *testing.T) {
	m, _, _ := voiceModel(t, true)
	at := time.Date(2026, 11, 23, 15, 4, 5, 0, time.Local)
	early := voice.LogEntry{Time: at, Kind: voice.KindTest, Text: "a", Result: voice.ResultSpoken}
	both := voice.LogEntry{Time: at.Add(time.Second), Kind: voice.KindTest, Text: "b", Result: voice.ResultSpoken}
	vm := &voiceLogModal{m: m, loading: true}
	m.modal = vm
	runCmd(m, m.onEvent(voiceEv(t, "voice.logged", both)))
	vm.other(voiceLogMsg{log: []voice.LogEntry{both, early}})
	if len(vm.log) != 2 || vm.log[0].Text != "b" || vm.log[1].Text != "a" || vm.loading {
		t.Fatalf("log: %+v", vm.log)
	}
}

// The tutorial shows no session: voice.focus "" while it runs, and the real
// focus again when it ends.
func TestVoiceFocusTour(t *testing.T) {
	m, f, _ := voiceModel(t, true)
	if fs := f.find("voice.focus"); len(fs) != 1 || fs[0]["slug"] != "api-1" {
		t.Fatalf("at the start: %v", fs)
	}
	m.focus = pSessions
	_, cmd := m.Update(key("H"))
	runCmd(m, cmd)
	if m.tour == nil {
		t.Fatal("H started no tour")
	}
	_, cmd = m.Update(tickMsg(time.Now())) // any message while the tour runs
	runCmd(m, cmd)
	fs := f.find("voice.focus")
	if last := fs[len(fs)-1]; last["slug"] != "" {
		t.Fatalf("during the tour: %v", fs)
	}
	m.endTour() // its reloads are not run: the fake pekoed lists no sessions
	if m.tour != nil {
		t.Fatal("the tour did not end")
	}
	_, cmd = m.Update(infoMsg(""))
	runCmd(m, cmd)
	fs = f.find("voice.focus")
	if last := fs[len(fs)-1]; last["slug"] != "api-1" {
		t.Fatalf("after the tour: %v", fs)
	}
}
