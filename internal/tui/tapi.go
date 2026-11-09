package tui

// tapi, the pearl (docs/mascot.md): one face in the status bar, a dot per
// session, a 3-line face only in an empty main pane, toasts for unseen
// wait/error, and pekoe's own y/n prompts. Animations run at most every
// 400 ms and stop while the terminal window is not focused.

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"pekoe/internal/daemon"
	"pekoe/internal/mascot"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

const (
	tapiFrame = 400 * time.Millisecond
	toastFor  = 5 * time.Second
	splashFor = time.Second
)

type tapiTickMsg struct{}
type splashDoneMsg struct{}

type toast struct {
	slug   string
	mood   mascot.Mood
	text   string
	until  time.Time
	react  *mascot.Reaction // a PR reaction (tapi_pr.go); nil for wait / error
	frame0 int              // tapi's frame when it came up (the confetti counts from it)
}

type tapiState struct {
	r          *mascot.Renderer
	focused    bool // the terminal window has focus (focus reports)
	ticking    bool
	frame      int // advances every 400 ms while focused
	seen       map[string]time.Time
	lastActive time.Time
	nextBlink  time.Time
	blinkFrame int
	toast      *toast
	toasted    map[string]bool
	notified   map[string]bool
	splash     bool
	// PR reactions (tapi_pr.go)
	prs         map[string]*mascot.PR // the summary tapi saw last, per session
	prToasted   map[string]time.Time
	prQueue     []queuedReaction
	demoPR      map[string]*mascot.PR // PEKOE_TAPI_DEMO=pr
	demoSession *state.Session
	demoStep    int
	demoAt      time.Time
}

func (m *model) newTapi() *tapiState {
	t := &tapiState{
		focused:    true,
		seen:       map[string]time.Time{},
		toasted:    map[string]bool{},
		notified:   map[string]bool{},
		prs:        map[string]*mascot.PR{},
		prToasted:  map[string]time.Time{},
		demoPR:     map[string]*mascot.PR{},
		lastActive: time.Now(),
		nextBlink:  time.Now().Add(blinkGap()),
		blinkFrame: -1,
		splash:     m.g.Mascot.SplashOn(),
	}
	m.tapi = t
	m.buildTapi(nil)
	return t
}

// buildTapi resolves shape / glyphs / palette. With a tty, "auto" glyphs
// measure the width of ● first (before the TUI takes the terminal).
func (m *model) buildTapi(tty *os.File) {
	c := m.g.Mascot
	m.tapi.r = mascot.New(mascot.Options{
		Shape:   mascot.DetectShape(c.Shape),
		Glyphs:  mascot.DetectGlyphs(c.Glyphs, m.ic.Name, tty),
		Light:   c.LightPalette(),
		Colors:  c.Colors,
		Dim:     m.th.Color(theme.Dim),
		NoColor: m.th.Mode == theme.None,
	})
}

// measureTTY returns stdin when both ends are a terminal.
func measureTTY() *os.File {
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		return os.Stdin
	}
	return nil
}

func blinkGap() time.Duration { return 4*time.Second + rand.N(2*time.Second) }

func tapiTick() tea.Cmd {
	return tea.Tick(tapiFrame, func(time.Time) tea.Msg { return tapiTickMsg{} })
}

func (m *model) animating() bool { return m.g.AnimateOn() && m.tapi.focused }

func (m *model) tapiInit() []tea.Cmd {
	var cmds []tea.Cmd
	if m.animating() {
		m.tapi.ticking = true
		cmds = append(cmds, tapiTick())
	}
	if m.tapi.splash {
		cmds = append(cmds, tea.Tick(splashFor, func(time.Time) tea.Msg { return splashDoneMsg{} }))
	}
	if demoOn() {
		cmds = append(cmds, demoTick())
	}
	return cmds
}

// tapiUpdate handles the mascot's own messages; ok=false passes msg on.
func (m *model) tapiUpdate(msg tea.Msg) (tea.Cmd, bool) {
	t := m.tapi
	switch msg.(type) {
	case tapiTickMsg:
		if !m.animating() {
			t.ticking = false
			return nil, true
		}
		t.frame++
		if now := time.Now(); now.After(t.nextBlink) {
			t.blinkFrame, t.nextBlink = t.frame, now.Add(blinkGap())
		}
		m.tapiExpire()
		return tapiTick(), true
	case tapiDemoMsg:
		return m.demoStep(), true
	case splashDoneMsg:
		t.splash = false
		return nil, true
	case tea.FocusMsg:
		t.focused = true
		var cmds []tea.Cmd
		if m.g.AnimateOn() && !t.ticking {
			t.ticking = true
			cmds = append(cmds, tapiTick())
		}
		if m.g.AnimateOn() && !m.spinning {
			m.spinning = true
			cmds = append(cmds, spin(true))
		}
		return tea.Batch(cmds...), true
	case tea.BlurMsg:
		t.focused = false // ticks stop themselves at the next frame
		return nil, true
	case tea.KeyMsg:
		t.lastActive = time.Now()
		t.toast = nil
		if t.splash {
			t.splash = false
			return nil, true // the key only dismisses the splash
		}
	case tea.MouseMsg:
		if t.splash {
			t.splash = false
			return nil, true
		}
	}
	return nil, false
}

// markSeen: the session in the main pane counts as seen while focused.
func (m *model) markSeen() {
	if m.tapi == nil || !m.tapi.focused {
		return
	}
	if s := m.current(); s != nil {
		m.tapi.seen[s.Slug] = time.Now()
		// a PR toast stays: the agent pane doesn't show the PR
		if m.tapi.toast != nil && m.tapi.toast.slug == s.Slug && m.tapi.toast.react == nil {
			m.tapi.toast = nil
		}
	}
}

// tapiOnSession runs for every session event: wakes tapi up and shows a
// toast when a session that is not on screen starts waiting or fails.
func (m *model) tapiOnSession(prev state.State, s *state.Session, curSlug string) {
	t := m.tapi
	t.lastActive = time.Now()
	m.tapiOnPR(s)
	if s.State == prev || s.Slug == curSlug {
		return
	}
	var mood mascot.Mood
	switch s.State {
	case state.Waiting:
		mood = mascot.Wait
	case state.Failed:
		mood = mascot.Error
	default:
		return
	}
	key := s.Slug + "|" + string(s.State) + "|" + s.StateSince.String()
	if t.toasted[key] {
		return
	}
	t.toasted[key] = true
	if c := t.toast; c != nil && c.react != nil && time.Now().Before(c.until) {
		// a PR toast steps aside for wait / error and comes back later
		m.queueReaction(queuedReaction{r: *c.react, at: time.Now(), dur: time.Until(c.until)})
	}
	t.toast = &toast{slug: s.Slug, mood: mood, text: mascot.SessionSpeech(s), until: time.Now().Add(toastFor)}
}

// escalate: a wait left alone for 2 minutes becomes an OS notification,
// only while the terminal is not focused (OSC 777, or OSC 9).
func (m *model) escalate() tea.Cmd {
	if m.tapi.focused {
		return nil
	}
	now := time.Now()
	var b strings.Builder
	for _, s := range m.sessions {
		if s.State != state.Waiting || now.Sub(s.StateSince) < mascot.WaitNotifyAfter {
			continue
		}
		key := s.Slug + "|" + s.StateSince.String()
		if m.tapi.notified[key] {
			continue
		}
		m.tapi.notified[key] = true
		b.WriteString(notifySeq("pekoe", mascot.SessionSpeech(s)))
	}
	if b.Len() == 0 {
		return nil
	}
	return writeTerm(b.String())
}

func notifySeq(title, body string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f || r == ';' {
				return ' '
			}
			return r
		}, s)
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm":
		return "\x1b]777;notify;" + clean(title) + ";" + clean(body) + "\x07"
	}
	return "\x1b]9;" + clean(title+": "+body) + "\x07"
}

// ---- frames ----

func (m *model) summary() mascot.Summary {
	sum := mascot.Aggregate(m.sessions, m.tapi.seen, time.Now(), m.tapi.lastActive)
	sum.Cloudy = m.cloudy()
	return sum
}

func (m *model) blinking() bool { return m.animating() && m.tapi.frame == m.tapi.blinkFrame }

// eyes for a mood at the current frame: blinks for idle/work, a glance up
// for wait (peek=true, not in the bar).
func (m *model) eyesFor(mood mascot.Mood, peek bool) string {
	r := m.tapi.r
	switch mood {
	case mascot.Idle, mascot.Work:
		if m.blinking() {
			return r.Blink()
		}
	case mascot.Wait:
		if peek && m.animating() && m.tapi.frame%6 == 5 {
			return r.Peek()
		}
	}
	return r.Eyes(mood)
}

// secondStep is a 1 fps counter derived from 400 ms frames (bar: ≤ 1 fps).
func (m *model) secondStep() int { return m.tapi.frame * int(tapiFrame/time.Millisecond) / 1000 }

func (m *model) zGlyph(i int) string {
	return []string{"z", "z", "Z", " "}[i%4]
}

// speechRole: wait / error / done speak in their accent, the rest dim.
func (m *model) speech(mood mascot.Mood, s string) string {
	switch mood {
	case mascot.Wait, mascot.Error, mascot.Done:
		return m.tapi.r.Paint(mood, s)
	}
	return m.th.FG(theme.Dim, s)
}

// barTapi is the left end of the status bar: aggregated face + speech.
func (m *model) barTapi(width int) string {
	sum := m.summary()
	text := sum.Speech()
	if m.chatWaits() && mascot.Priority(sum.Mood) > mascot.Priority(mascot.Wait) {
		// tapi chat waits behind its closed window: weaker than a session's wait
		sum.Mood, sum.Since = mascot.Wait, m.chat.info.StateSince
		text = "chat waits · t"
	}
	if m.chat.open || m.tour != nil { // tapi's face is in the chat window's title or the tutorial's card: one face at a time
		return m.speech(sum.Mood, clip(text, max(width/2-6, 4)))
	}
	eyes := m.eyesFor(sum.Mood, false)
	if sum.IsCloudy() && !m.blinking() {
		eyes = m.tapi.r.CloudyEyes()
	}
	face := m.tapi.r.Line(eyes)
	if sum.Mood == mascot.Wait && time.Since(sum.Since) >= mascot.WaitBlinkAfter && m.animating() && m.tapi.frame%4 == 3 {
		text = strings.Repeat(" ", w(text)) // 3 shown : 1 hidden, 1.6 s
	}
	text = clip(text, max(width/2-6, 4))
	if sum.Mood == mascot.Sleep {
		z := "z"
		if m.animating() {
			z = m.zGlyph(m.secondStep())
		}
		return face + " " + m.th.FG(theme.Dim, z) + " " + m.speech(sum.Mood, text)
	}
	return face + " " + m.speech(sum.Mood, text)
}

// bigRows centers the 3-line face with a caption: only for an empty main
// pane (no session, worktree being created, setup failed, session ended).
func (m *model) bigRows(width, height int, mood mascot.Mood, caption, sub string) []row {
	return m.bigRowsWith(width, height, mood, caption, sub, false)
}

// bigRowsTip is bigRows with a tip under the caption (tips.go), for the
// panes the user just waits in: no session selected, worktree being created.
// The tip only takes rows the face and the caption leave.
func (m *model) bigRowsTip(width, height int, mood mascot.Mood, caption, sub string) []row {
	return m.bigRowsWith(width, height, mood, caption, sub, true)
}

func (m *model) bigRowsWith(width, height int, mood mascot.Mood, caption, sub string, withTip bool) []row {
	r := m.tapi.r
	anim := m.animating()
	f := m.tapi.frame
	face := r.Big(m.eyesFor(mood, true))
	// a 4×7 grid: row 0 above the head, column 6 right of it
	extra := [4]string{" ", " ", " ", " "}
	top := "       "
	paintAt := func(row int, g string, role mascot.Mood) {
		if row == 0 {
			top = "      " + r.Paint(role, g)
		} else {
			extra[row] = r.Paint(role, g)
		}
	}
	switch mood {
	case mascot.Work:
		seq := []struct {
			row int
			g   string
		}{{2, "·"}, {1, "°"}, {0, "°"}, {-1, ""}}
		if r.ASCII() {
			seq[0].g, seq[1].g, seq[2].g = ".", "o", "O"
		}
		i := 1
		if anim {
			i = f % 4
		}
		if seq[i].row >= 0 {
			paintAt(seq[i].row, seq[i].g, mascot.Work)
		}
	case mascot.Wait:
		if !anim || f%4 != 3 {
			top = "   " + r.Paint(mascot.Wait, "?") + "   "
		}
	case mascot.Sleep:
		seq := []int{1, 0, 0, -1}
		i := 0
		if anim {
			i = m.secondStep() % 4
		}
		if seq[i] >= 0 {
			paintAt(seq[i], m.zGlyph(i), mascot.Idle)
		}
	}
	grid := []string{top}
	for i, l := range face {
		grid = append(grid, " "+l+extra[i+1])
	}
	lines := 4 + 1 + 1
	if sub != "" {
		lines++
	}
	var tip []row
	if withTip {
		tip = m.tipRows(width, height-lines)
		lines += len(tip)
	}
	var rows []row
	for i := 0; i < (height-lines)/2; i++ {
		rows = append(rows, row{})
	}
	center := func(s string) row { return row{Text: strings.Repeat(" ", max((width-w(s))/2, 0)) + s} }
	gl := strings.Repeat(" ", max((width-7)/2, 0))
	for _, g := range grid {
		rows = append(rows, row{Text: gl + g})
	}
	rows = append(rows, row{}, center(m.th.FG(theme.Text, caption)))
	if sub != "" {
		rows = append(rows, center(m.th.FG(theme.Dim, sub)))
	}
	return append(rows, tip...)
}

// creatingSub is the running setup step under "creating worktree …".
func creatingSub(steps []daemon.Step) string {
	for _, st := range steps {
		if st.Status == "running" {
			if st.Detail != "" {
				return st.Detail + " …"
			}
			return st.Label + " …"
		}
	}
	return ""
}

// toastBlock: a small bordered card for an unseen wait / error.
func (m *model) toastBlock() []string {
	t := m.tapi.toast
	if t == nil {
		return nil
	}
	th := m.th
	r := m.tapi.r
	inner := " " + r.Line(m.eyesFor(t.mood, true)) + "  " + r.Paint(t.mood, t.text) + " "
	if t.react != nil {
		inner = m.reactionInner(t)
	}
	iw := w(inner)
	if th.Mode == theme.Truecolor {
		inner = lipgloss.NewStyle().Background(th.Color(theme.Select)).Render(strings.ReplaceAll(inner, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Select)))
	}
	tl, tr, bl, br := "╭", "╮", "╰", "╯"
	if m.square {
		tl, tr, bl, br = "┌", "┐", "└", "┘"
	}
	b := func(s string) string { return th.FG(theme.Border, s) }
	if t.react != nil && t.react.Event == mascot.PRMerged && m.g.AnimateOn() {
		top, bottom := m.confettiBorders(iw, t)
		return []string{
			b(tl) + top + b(tr),
			b("│") + inner + b("│"),
			b(bl) + bottom + b(br),
		}
	}
	return []string{
		b(tl + strings.Repeat("─", iw) + tr),
		b("│") + inner + b("│"),
		b(bl + strings.Repeat("─", iw) + br),
	}
}

// askLine is pekoe asking: the puppy leads a one-line y/n prompt that sits
// on the main pane's bottom row instead of a dialog.
func (m *model) askLine(c *confirmModal, width int) string {
	th := m.th
	r := m.tapi.r
	q := strings.ToLower(c.title)
	if c.subject != "" {
		q += " " + c.subject
	}
	q += "?"
	yes := mascot.Done
	if c.destructive {
		yes = mascot.Error
	}
	keys := th.FG(theme.Dim, "[") + r.Paint(yes, "y") + th.FG(theme.Dim, "/") + r.Paint(mascot.Error, "n") + th.FG(theme.Dim, "]")
	detail := c.text
	if c.target != "" {
		detail = c.target
	}
	head := r.Line(r.Eyes(mascot.Wait)) + " " + th.FG(theme.Text, q) + " " + keys
	if room := width - w(head) - 2; room > 6 && detail != "" {
		head += "  " + th.FG(theme.Dim, clip(detail, room))
	}
	line := fit(head, width)
	if th.Mode == theme.Truecolor {
		line = lipgloss.NewStyle().Background(th.Color(theme.Select)).Render(strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Select)))
	}
	return line
}

// splashView: tapi wearing the aggregated face, next to the live count.
func (m *model) splashView() string {
	r := m.tapi.r
	live := 0
	for _, s := range m.sessions {
		if s.State != state.Landed && !s.Stopped {
			live++
		}
	}
	alive := "no sessions yet"
	switch {
	case live == 1:
		alive = "1 session alive"
	case live > 1:
		alive = fmt.Sprintf("%d sessions alive", live)
	}
	text := []string{
		m.th.Bold(theme.Text, "pekoe") + " " + m.th.FG(theme.Dim, "v"+daemon.Version),
		m.th.FG(theme.Dim, "agents in worktrees"),
		"",
		m.th.FG(theme.Dim, alive),
	}
	// one blank column on each side: an ambiguous-width glyph that the
	// font draws two cells wide (Moralerspace JPDOC …) must not reach the edge
	banner := r.FitBanner(m.eyesFor(m.summary().Mood, false), text, max(m.width-2, 1), m.height)
	bw := 0
	for _, l := range banner {
		bw = max(bw, w(l))
	}
	lines := make([]string, 0, m.height)
	for i := 0; i < (m.height-len(banner))/2; i++ {
		lines = append(lines, "")
	}
	pad := strings.Repeat(" ", max((m.width-bw)/2, 0))
	for _, l := range banner {
		lines = append(lines, pad+l)
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	// every line is the full width, so paintBg covers the whole screen
	// (lipgloss only pads to the widest line)
	for i := range lines {
		lines[i] = fit(lines[i], m.width)
	}
	return strings.Join(lines[:m.height], "\n")
}

// emptyMood: with no session on screen the big face mirrors the bar
// (asleep when everything is quiet), otherwise it just idles.
func (m *model) emptyMood() mascot.Mood {
	if m.summary().Mood == mascot.Sleep {
		return mascot.Sleep
	}
	return mascot.Idle
}
