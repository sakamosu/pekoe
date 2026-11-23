package tui

// pekoe voice in the TUI (docs/voice.md 14): <prefix> m mutes and unmutes,
// the bar shows a voice / muted pill, what [4] shows goes to pekoed as
// voice.focus (so the session in front of you is not narrated), and :voice
// lists what was said.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/theme"
	"pekoe/internal/voice"
)

// muteKey follows the prefix: <prefix> m mutes voice, or unmutes it. It
// works while typing too (the prefix does), and goes straight back.
const muteKey = "m"

// voiceFocusWait: what [4] shows is sent once it has stayed for this long,
// so j / k down the list does not send every session on the way.
const voiceFocusWait = 200 * time.Millisecond

// voiceLogN is how many lines :voice reads, newest first.
const voiceLogN = 50

type voiceFocus struct {
	slug    string // what [4] shows: a session, daemon.TapiSlug, "" for none
	focused bool   // the terminal window has focus
}

type voiceState struct {
	st   *daemon.VoiceStatus // nil until voice.status answers (or when it cannot: no token)
	want voiceFocus          // the last focus scheduled to be sent
	sent *voiceFocus         // the last focus sent; nil for none yet
	gen  int                 // the debounce: only the newest tick sends
}

type voiceStatusMsg struct{ st *daemon.VoiceStatus }

// voiceToggledMsg: <prefix> m did this (st nil: voice is off).
type voiceToggledMsg struct {
	st    *daemon.VoiceStatus
	muted bool
}

type voiceFocusMsg struct{ gen int }

type voiceLogMsg struct {
	log []voice.LogEntry
	err error
}

func (m *model) loadVoice() tea.Cmd {
	if m.cl == nil {
		return nil
	}
	return func() tea.Msg {
		var st daemon.VoiceStatus
		if m.cl.Call("voice.status", nil, &st) != nil {
			return nil // an older pekoed, or no human token: no pill, no focus
		}
		return voiceStatusMsg{&st}
	}
}

// voiceOn: voice is enabled, as far as the TUI knows.
func (m *model) voiceOn() bool { return m.voice.st != nil && m.voice.st.Enabled }

func (m *model) setVoice(st *daemon.VoiceStatus) {
	was := m.voiceOn()
	m.voice.st = st
	if !was && m.voiceOn() { // just turned on: send the focus again (pekoed took none while off)
		m.voiceResend()
	}
}

// voiceResend forgets what was sent, so the next sync sends the focus even
// if it did not change.
func (m *model) voiceResend() {
	m.voice.sent = nil
	m.voice.want = voiceFocus{slug: "\x00"}
}

// voiceMuted: muted now (a timed mute that ran out counts as not muted).
func voiceMuted(st *daemon.VoiceStatus) bool {
	if st == nil || st.MutedUntil == "" {
		return false
	}
	if t, err := time.Parse(time.RFC3339, st.MutedUntil); err == nil {
		return time.Now().Before(t)
	}
	return true // "forever"
}

// toggleMute is <prefix> m: reads the status afresh (another TUI or the CLI
// may have changed it), then mutes or unmutes.
func (m *model) toggleMute() tea.Cmd {
	if m.cl == nil {
		return nil
	}
	return func() tea.Msg {
		var st daemon.VoiceStatus
		if err := m.cl.Call("voice.status", nil, &st); err != nil {
			return errMsg{err}
		}
		if !st.Enabled {
			return voiceToggledMsg{st: &st}
		}
		p := map[string]any{}
		mute := !voiceMuted(&st)
		if !mute {
			p["off"] = true
		}
		var after daemon.VoiceStatus
		if err := m.cl.Call("voice.mute", p, &after); err != nil {
			return errMsg{err}
		}
		return voiceToggledMsg{st: &after, muted: mute}
	}
}

func (m *model) onVoiceToggled(msg voiceToggledMsg) {
	m.setVoice(msg.st)
	switch {
	case !msg.st.Enabled:
		m.setMsg(tx("voice is off · pekoe voice on"), false)
	case msg.muted:
		m.setMsg(tx("voice muted · ")+m.prefixLabel()+" "+muteKey, false)
	default:
		m.setMsg(tx("voice on · ")+m.prefixLabel()+" "+muteKey, false)
	}
}

// voiceEvent handles voice.updated and voice.logged.
func (m *model) voiceEvent(ev client.Event) (tea.Cmd, bool) {
	switch ev.Event {
	case "voice.updated":
		var st daemon.VoiceStatus
		if json.Unmarshal(ev.Data, &st) == nil {
			m.setVoice(&st)
		}
		return nil, true
	case "voice.logged":
		var e voice.LogEntry
		if vm, ok := m.modal.(*voiceLogModal); ok && json.Unmarshal(ev.Data, &e) == nil {
			vm.add(e)
		}
		return nil, true
	}
	return nil, false
}

// voiceShown is what [4] shows, as voice.focus wants it: a session's slug
// (its agent, review, dev, PR or preview), tapi chat, or "" (the dashboard,
// an issue, nothing, the review screen, the tutorial).
func (m *model) voiceShown() string {
	if m.tour != nil || m.review != nil {
		return ""
	}
	if m.chat.open {
		return daemon.TapiSlug
	}
	if m.issueShown() {
		return ""
	}
	if s := m.current(); s != nil {
		return s.Slug
	}
	return ""
}

// voiceFocusSync schedules voice.focus when what [4] shows or the terminal
// focus changed; voiceFocusWait later, the newest one is sent. Nothing is
// sent while voice is off (docs/voice.md 15).
func (m *model) voiceFocusSync() tea.Cmd {
	if !m.voiceOn() || m.cl == nil {
		return nil
	}
	f := voiceFocus{slug: m.voiceShown(), focused: m.tapi.focused}
	if f == m.voice.want {
		return nil
	}
	m.voice.want = f
	m.voice.gen++
	gen := m.voice.gen
	return after(voiceFocusWait, func(time.Time) tea.Msg { return voiceFocusMsg{gen} })
}

func (m *model) onVoiceFocus(msg voiceFocusMsg) tea.Cmd {
	if msg.gen != m.voice.gen || !m.voiceOn() || m.cl == nil {
		return nil
	}
	f := m.voice.want
	if m.voice.sent != nil && *m.voice.sent == f {
		return nil
	}
	m.voice.sent = &f
	return func() tea.Msg {
		_ = m.cl.Call("voice.focus", map[string]any{"slug": f.slug, "term_focused": f.focused}, nil)
		return nil
	}
}

// voicePill is the bar's voice pill: "voice" while something sounds,
// "muted <prefix> m" while muted, "" otherwise (tapi's face stays as it is:
// docs/voice.md 14.3).
func (m *model) voicePill() string {
	st := m.voice.st
	if st == nil || !st.Enabled {
		return ""
	}
	if voiceMuted(st) {
		return m.pill("muted "+m.th.FG(theme.Dim, m.prefixLabel()+" "+muteKey), theme.Surface, theme.Text, false)
	}
	if st.Speaking != nil {
		return m.pill("voice", theme.Surface, theme.Text, false)
	}
	return ""
}

// ---- :voice: what was said (docs/voice.md 14.4) ----

func (m *model) openVoiceLog() tea.Cmd {
	vm := &voiceLogModal{m: m, loading: true}
	m.modal = vm
	if m.cl == nil {
		return nil
	}
	return func() tea.Msg {
		var log []voice.LogEntry
		err := m.cl.Call("voice.log", map[string]any{"n": voiceLogN}, &log)
		return voiceLogMsg{log, err}
	}
}

// voiceMuteCmd is :voice mute / :voice unmute (while voice is off it only
// says so, like <prefix> m).
func (m *model) voiceMuteCmd(mute bool) tea.Cmd {
	if m.cl == nil {
		return nil
	}
	return func() tea.Msg {
		var cur daemon.VoiceStatus
		if err := m.cl.Call("voice.status", nil, &cur); err != nil {
			return errMsg{err}
		}
		if !cur.Enabled {
			return voiceToggledMsg{st: &cur}
		}
		p := map[string]any{}
		if !mute {
			p["off"] = true
		}
		var st daemon.VoiceStatus
		if err := m.cl.Call("voice.mute", p, &st); err != nil {
			return errMsg{err}
		}
		return voiceToggledMsg{st: &st, muted: mute}
	}
}

type voiceLogModal struct {
	m       *model
	log     []voice.LogEntry // newest first
	sel     int
	top     int
	loading bool
	err     string
}

func (vm *voiceLogModal) mode() (string, string) { return "VOICE", "" }

func (vm *voiceLogModal) hints() [][2]string {
	return [][2]string{{"j/k", "move"}, {"esc", "close"}}
}

// other takes the log as it arrives.
func (vm *voiceLogModal) other(msg tea.Msg) tea.Cmd {
	if l, ok := msg.(voiceLogMsg); ok {
		vm.loading = false
		if l.err != nil {
			vm.err = l.err.Error()
			return nil
		}
		// lines logged while it loaded came first: keep them, newest first
		// (one logged as the file was read is in both: once is enough)
		for _, e := range l.log {
			if !slices.ContainsFunc(vm.log, func(x voice.LogEntry) bool {
				return x.Time.Equal(e.Time) && x.Text == e.Text && x.Result == e.Result
			}) {
				vm.log = append(vm.log, e)
			}
		}
		if len(vm.log) > voiceLogN {
			vm.log = vm.log[:voiceLogN]
		}
	}
	return nil
}

// add puts a line just logged on top; the selection stays on its line
// (unless that line is the oldest of voiceLogN and drops out: then the
// last line).
func (vm *voiceLogModal) add(e voice.LogEntry) {
	had := len(vm.log) > 0
	vm.log = append([]voice.LogEntry{e}, vm.log...)
	if len(vm.log) > voiceLogN {
		vm.log = vm.log[:voiceLogN]
	}
	if had {
		vm.sel = min(vm.sel+1, len(vm.log)-1)
		vm.top++
	}
}

func (vm *voiceLogModal) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "j", "down":
		vm.sel = clampi(vm.sel+1, 0, max(len(vm.log)-1, 0))
	case "k", "up":
		vm.sel = clampi(vm.sel-1, 0, max(len(vm.log)-1, 0))
	case "esc", "q":
		return done(nil)
	}
	return nil
}

// rows is how many log lines the modal shows at once.
func (vm *voiceLogModal) rows() int { return clampi(vm.m.height-12, 3, 20) }

func (vm *voiceLogModal) view() []string {
	m := vm.m
	th := m.th
	width := m.modalWidth(100)
	inner := width - 4
	var lines []string
	switch {
	case vm.err != "":
		lines = append(lines, "", th.FG(theme.Err, vm.err))
	case vm.loading:
		lines = append(lines, "", th.FG(theme.Dim, tx("loading…")))
	case len(vm.log) == 0:
		lines = append(lines, "", th.FG(theme.Dim, tx("nothing said yet · pekoe voice test <text>")))
	default:
		n := vm.rows()
		vm.top = clampi(vm.top, max(vm.sel-n+1, 0), vm.sel)
		lines = append(lines, "")
		for i := vm.top; i < len(vm.log) && i < vm.top+n; i++ {
			lines = append(lines, vm.line(vm.log[i], inner, i == vm.sel))
		}
		if e := vm.log[vm.sel]; e.Reason != "" || e.Backend != "" {
			var d []string
			if e.Backend != "" {
				d = append(d, e.Backend)
			}
			if e.LatencyMS > 0 {
				d = append(d, fmt.Sprintf("%dms", e.LatencyMS))
			}
			if e.Reason != "" {
				d = append(d, e.Reason)
			}
			lines = append(lines, "", th.FG(theme.Dim, clip(strings.Join(d, tx(" · ")), inner)))
		}
	}
	title := th.Bold(theme.Primary, "Voice") + th.FG(theme.Dim, fmt.Sprintf(tx("  newest %d · pekoe voice log"), voiceLogN))
	return m.modalFrame(width, title, lines, vm.hints(), false)
}

// line: time, result, kind, the sentence.
func (vm *voiceLogModal) line(e voice.LogEntry, width int, sel bool) string {
	th := vm.m.th
	role := theme.Dim
	switch e.Result {
	case voice.ResultSpoken, voice.ResultMerged:
		role = theme.Ok
	case voice.ResultFailed:
		role = theme.Err
	}
	head := e.Time.Local().Format("15:04:05") + " " + th.FG(role, fmt.Sprintf("%-11s", e.Result)) + " " + fmt.Sprintf("%-8s", e.Kind) + " "
	s := head + clip(e.Text, max(width-w(head)-2, 1))
	if sel {
		return th.FG(theme.Primary, tx("▌")) + " " + s
	}
	return "  " + s
}
