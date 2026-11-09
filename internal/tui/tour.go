package tui

// The tutorial: coach marks over the TUI. One step puts one element of the
// screen in focus (a panel, the state column, a dialog) and explains it in a
// card next to it; ↵ / → / l go on, ← / h back, esc / q skip. It starts by
// itself the first time pekoe runs (paths.TutorialFile records that it was
// seen, skipped or not) and again with H or :tutorial.
//
// The tour runs on sample data (tour_sample.go), swapped in inside the model
// only: nothing is created in pekoed, nothing is written to state.db and no
// key reaches a pty or the daemon while it runs. Messages that arrive
// meanwhile (session lists, events, screens) are kept and replayed onto the
// real model when the tour ends, followed by a fresh load of everything.
//
// Focus follows the guidelines: the element in focus gets the focus frame
// (the panel itself, or the dialog), cells inside a panel get the select
// fill, the card is framed like the in-pane cards (border color), headed by
// tapi's one-line face instead of an icon: tapi tells the tour.
// The rest of the screen is not darkened (docs/ui-guidelines.md: no dimming
// behind modals); the other borders are already plain border.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/client"
	"pekoe/internal/mascot"
	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

const (
	tourKey = "H" // replays the tutorial from any panel
	// tourKeep bounds the messages kept while the tour runs; past it they
	// are dropped (the reload at the end brings the state back anyway)
	tourKeep = 2000
	// tourCardW is the card's width when there is room; a card beside its
	// target may be narrower: tourSideWide is preferred to going below or
	// above it, tourSideMin is the least
	tourCardW    = 56
	tourSideWide = 44 // beside the target first only when this wide
	tourSideMin  = 28
)

type tourState struct {
	step    int       // index into tourSteps
	saved   model     // the real model, put back when the tour ends
	pending []tea.Msg // arrived during the tour, replayed at the end
}

// rect is a cell area of the screen (the body above the bottom bar).
type rect struct{ x, y, w, h int }

// tourStep is one coach mark.
type tourStep struct {
	mood  mascot.Mood // tapi's face at the head of the card
	title string
	// body: {k} is a key (secondary, bold), {@running} a state's icon in
	// its color, ^q the prefix as configured
	body string
	// narrow replaces body below 100 columns, where the left column is one
	// tabbed panel without Browser and Picks
	narrow string
	setup  func(m *model) // puts the sample screen into shape (after tourBase)
	// target is what the step is about; ok=false centers the card
	target func(m *model) (r rect, ok bool)
	cells  func(m *model) []rect // cells painted with the select fill
}

// ---- record ----

type tourRecord struct {
	SeenAt time.Time `json:"seen_at"`
}

// initTour reads the record at path ("" keeps none: the tour never starts
// by itself, as in tests).
func (m *model) initTour(path string) {
	m.tourPath = path
	m.tourAuto = false
	if path == "" {
		return
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		m.tourAuto = true
	}
}

func saveTourSeen(path string) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(tourRecord{SeenAt: time.Now()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), paths.DirPerm); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, paths.FilePerm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- start / end ----

// startTour asks for the tour; it begins at the end of this update
// (maybeAutoTour), after the syncs that follow every update have run on the
// real model.
func (m *model) startTour() tea.Cmd {
	m.tourAuto = true
	return nil
}

// maybeAutoTour starts the tour when asked (first run, H, :tutorial) and the
// screen is ready: not during the splash, not in the review surface.
func (m *model) maybeAutoTour() {
	if !m.tourAuto || m.tour != nil || m.tapi.splash || m.review != nil || m.width == 0 {
		return
	}
	m.tourAuto = false
	m.tour = &tourState{saved: *m} // m.tour is still nil in the copy
	m.loadSample()
	m.tourGo(0)
}

// endTour puts the real model back, records the tour as seen and replays
// what arrived meanwhile.
func (m *model) endTour() tea.Cmd {
	t := m.tour
	if t == nil {
		return nil
	}
	live := *m
	*m = t.saved
	// what moved on while the tour ran
	m.width, m.height = live.width, live.height
	m.frame, m.spinning = live.frame, live.spinning
	m.guard = live.guard
	m.msg, m.msgErr, m.msgAt = live.msg, live.msgErr, live.msgAt
	if err := saveTourSeen(m.tourPath); err != nil {
		m.setMsg("tutorial: "+err.Error(), true)
	}
	var cmds []tea.Cmd
	for _, msg := range t.pending {
		_, c := m.Update(msg)
		cmds = append(cmds, c)
	}
	// and a fresh look at everything, in case something was dropped
	m.screen = nil
	cmds = append(cmds, m.loadSessions(), m.loadRepos(), m.loadTabs(), m.loadPicks(), m.loadErrors(), m.loadWorktrees(), m.fetchScreen())
	if s := m.current(); s != nil {
		cmds = append(cmds, m.loadChanges(s.Slug))
	}
	return tea.Batch(cmds...)
}

// ---- update ----

// tourUpdate handles every message while the tour runs: its keys, the
// clocks, and keeps the rest for the end.
func (m *model) tourUpdate(msg tea.Msg) tea.Cmd {
	t := m.tour
	switch msg := msg.(type) {
	case tea.KeyMsg:
		keys, wait := m.guard.feed(msg)
		cmd := m.tourKeys(keys)
		if wait && m.tour != nil {
			cmd = tea.Batch(cmd, guardTick(m.guard.gen))
		}
		return cmd
	case guardFlushMsg:
		return m.tourKeys(m.guard.flush(int(msg)))
	case tea.MouseMsg, kittyKeyMsg:
		return nil // nothing to click on: it is a picture of pekoe
	case tickMsg:
		if time.Since(m.msgAt) > 6*time.Second {
			m.msg = ""
		}
		return tick()
	case spinMsg:
		_, cmd := m.updateMain(msg)
		return cmd
	case tapiTickMsg, splashDoneMsg, tea.FocusMsg, tea.BlurMsg:
		cmd, _ := m.tapiUpdate(msg)
		return cmd
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.tourApply() // narrow ↔ wide shows the left panels differently
	case evMsg:
		if client.Event(msg).Event == "session.output" {
			return nil // only says "fetch the screen": the end fetches it anyway
		}
	}
	if len(t.pending) < tourKeep {
		t.pending = append(t.pending, msg)
	}
	return nil
}

// tourKeys: only the tour's keys do something; every other key is dropped,
// so nothing reaches the sample sessions (or a real one).
func (m *model) tourKeys(keys []tea.KeyMsg) tea.Cmd {
	for _, k := range keys {
		switch k.String() {
		case "enter", "right", "l", " ":
			if m.tour.step == len(tourSteps)-1 {
				return m.endTour() // Done
			}
			m.tourGo(m.tour.step + 1)
		case "left", "h", "backspace":
			if m.tour.step > 0 {
				m.tourGo(m.tour.step - 1)
			}
		case "esc", "q":
			return m.endTour()
		case "ctrl+c":
			cmd := m.endTour()
			return tea.Batch(cmd, m.quit())
		}
	}
	return nil
}

func (m *model) tourGo(i int) {
	m.tour.step = clampi(i, 0, len(tourSteps)-1)
	m.tourApply()
}

// tourApply sets the sample screen up for the current step.
func (m *model) tourApply() {
	m.tourBase()
	if f := tourSteps[m.tour.step].setup; f != nil {
		f(m)
	}
}

// ---- view ----

func (m *model) tourHints() [][2]string {
	if m.tour.step == len(tourSteps)-1 {
		return [][2]string{{"↵", "done"}, {"←", "back"}}
	}
	if m.tour.step == 0 {
		return [][2]string{{"↵", "next"}, {"esc", "skip"}}
	}
	return [][2]string{{"↵", "next"}, {"←", "back"}, {"esc", "skip"}}
}

// tourView paints the step's cells and puts the card next to its target.
func (m *model) tourView(body []string) []string {
	st := tourSteps[m.tour.step]
	if st.cells != nil {
		for _, c := range st.cells(m) {
			if c.y >= 0 && c.y < len(body) {
				body[c.y] = m.selectCells(body[c.y], c.x, c.w)
			}
		}
	}
	tgt, ok := rect{}, false
	if st.target != nil {
		tgt, ok = st.target(m)
	}
	x, y, card := m.tourPlace(tgt, ok, len(body))
	return placeAt(body, y, x, card)
}

// tourPlace picks where the card goes: beside the target when the card
// keeps a comfortable width there (right, then left), below, above, beside
// it narrower, or over the target's top right as a last resort. Without a
// target it is centered.
func (m *model) tourPlace(t rect, has bool, bodyH int) (x, y int, card []string) {
	W := m.width
	want := max(min(tourCardW, W-2), 1)
	if !has {
		card = m.tourCard(want, bodyH)
		return max((W-want)/2, 0), max((bodyH-len(card))/2, 0), card
	}
	right, left := W-(t.x+t.w)-1, t.x-1
	// beside the target, the card starts a row below its top and stays
	// above the bottom border row when it can
	beside := func(least int) (int, int, []string, bool) {
		for _, room := range []int{right, left} {
			if room < least {
				continue
			}
			cw := min(want, room)
			c := m.tourCard(cw, bodyH)
			cy := clampi(t.y+1, 0, max(bodyH-len(c)-1, 0))
			if room == right {
				return t.x + t.w + 1, cy, c, true
			}
			return t.x - 1 - cw, cy, c, true
		}
		return 0, 0, nil, false
	}
	if x, y, c, ok := beside(min(tourSideWide, want)); ok {
		return x, y, c
	}
	card = m.tourCard(want, bodyH)
	cx := clampi(t.x+2, 0, max(W-want, 0))
	if t.y+t.h+len(card) <= bodyH { // below
		return cx, t.y + t.h, card
	}
	if t.y-len(card) >= 0 { // above
		return cx, t.y - len(card), card
	}
	if x, y, c, ok := beside(tourSideMin); ok {
		return x, y, c
	}
	// over the target: its top right (an agent's output sits at the bottom)
	return clampi(t.x+t.w-want-2, 0, max(W-want, 0)), clampi(t.y+1, 0, max(bodyH-len(card), 0)), card
}

// tourCard draws the card width cells wide and at most maxH rows tall:
// tapi's face and the title with the position, the text, the keys.
func (m *model) tourCard(width, maxH int) []string {
	th := m.th
	st := tourSteps[m.tour.step]
	inner := max(width-4, 1)
	right := th.FG(theme.Dim, fmt.Sprintf("sample · %d/%d", m.tour.step+1, len(tourSteps)))
	// tapi tells the tour: its face heads the card (the bar keeps only its
	// speech meanwhile, docs/mascot.md: one face at a time)
	mood := st.mood
	if mood == "" {
		mood = mascot.Idle
	}
	head := m.tapi.r.Line(m.tapi.r.Eyes(mood)) + " " + th.FG(theme.Text, st.title)
	if gap := inner - w(head) - w(right); gap >= 1 {
		head += strings.Repeat(" ", gap) + right
	}
	body := st.body
	if narrow(m.width) && st.narrow != "" {
		body = st.narrow
	}
	text := m.tourText(strings.ReplaceAll(body, "^q", m.prefixLabel()), inner)
	keys := m.keyHints(m.tourHints(), inner)
	// rows: border, head, blank, text…, blank, keys, border
	lines := append([]string{head, ""}, text...)
	lines = append(lines, "", keys)
	if room := maxH - 2; len(lines) > room { // short screen: blanks go first, then text
		lines = append(append([]string{head}, text...), keys)
		if len(lines) > room {
			cut := max(room-1, 0)
			lines = append(lines[:min(cut, len(lines)-1)], keys)
			if len(lines) > room {
				lines = lines[:max(room, 0)]
			}
		}
	}
	b := func(s string) string { return th.FG(theme.Border, s) }
	tl, tr, bl, br := "╭", "╮", "╰", "╯"
	if m.square {
		tl, tr, bl, br = "┌", "┐", "└", "┘"
	}
	hz := strings.Repeat("─", max(width-2, 0))
	surf := func(s string) string {
		s = " " + fit(s, inner) + " "
		if th.Mode == theme.Truecolor {
			return lipgloss.NewStyle().Background(th.Color(theme.Surface)).Render(strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Surface)))
		}
		return s
	}
	out := []string{b(tl + hz + tr)}
	for _, l := range lines {
		out = append(out, b("│")+surf(l)+b("│"))
	}
	out = append(out, b(bl+hz+br))
	if len(out) > maxH {
		out = out[:max(maxH, 0)]
	}
	for i := range out {
		out[i] = fit(out[i], width)
	}
	return out
}

// tourWord is one unbreakable piece of the card's text: plain text, keys
// and state icons glued together ("{j}/{k}", "{↵},").
type tourWord []tourSeg

type tourSeg struct {
	text string
	kind byte // 0 text, 'k' key, '@' state icon
}

// tourLook is the icon of {@name}: a session state, or "overlap" (⚠).
func (m *model) tourLook(name string) theme.StateLook {
	if name == "overlap" {
		return theme.StateLook{Icon: m.ic.Warning, Role: theme.Warn}
	}
	return theme.Look(m.ic, state.State(name), 0, false)
}

func (m *model) tourSegText(s tourSeg) string {
	if s.kind == '@' {
		return m.tourLook(s.text).Icon
	}
	return s.text
}

func (m *model) tourWordW(wd tourWord) int {
	n := 0
	for _, s := range wd {
		n += w(m.tourSegText(s))
	}
	return n
}

func (m *model) tourWordRender(wd tourWord) string {
	th := m.th
	var b strings.Builder
	for _, s := range wd {
		switch s.kind {
		case 'k':
			b.WriteString(th.Bold(theme.Secondary, s.text))
		case '@':
			lk := m.tourLook(s.text)
			b.WriteString(th.FG(lk.Role, lk.Icon))
		default:
			b.WriteString(th.FG(theme.Text, s.text))
		}
	}
	return b.String()
}

// tourTok is a piece of the card's text that a line may break before: an
// ASCII word (with the keys and icons glued to it), a single full-width
// character, a key. sp: a space precedes it.
type tourTok struct {
	wd tourWord
	sp bool
}

// tourWide: a character that stands alone in wrapping (Japanese has no
// spaces: a line may break between any two full-width characters), as in
// wrapTip.
func tourWide(r rune) bool { return ansi.StringWidth(string(r)) > 1 || r > 0x2000 }

// tourStartsClosing: the piece begins with a character that must not
// begin a line (noLineStart).
func (m *model) tourStartsClosing(t tourTok) bool {
	if len(t.wd) == 0 || t.wd[0].kind != 0 || t.wd[0].text == "" {
		return false
	}
	return strings.ContainsRune(noLineStart, []rune(t.wd[0].text)[0])
}

// tourText wraps the body to width cells, counting full-width characters as
// two; "\n" starts a new paragraph (an empty row between). A line breaks at
// spaces and between full-width characters, never inside an ASCII word or a
// key, and never before closing punctuation (noLineStart, as wrapTip).
func (m *model) tourText(body string, width int) []string {
	var out []string
	for pi, para := range strings.Split(body, "\n") {
		if pi > 0 {
			out = append(out, "")
		}
		var toks []tourTok
		var cur tourWord
		var plain strings.Builder
		sp := false
		flushText := func() {
			if plain.Len() > 0 {
				cur = append(cur, tourSeg{text: plain.String()})
				plain.Reset()
			}
		}
		flush := func() {
			flushText()
			if len(cur) > 0 {
				toks = append(toks, tourTok{wd: cur, sp: sp})
				cur, sp = nil, false
			}
		}
		rs := []rune(para)
		for i := 0; i < len(rs); i++ {
			r := rs[i]
			switch {
			case r == '{':
				j := i + 1
				for j < len(rs) && rs[j] != '}' {
					j++
				}
				flushText()
				in := string(rs[i+1 : min(j, len(rs))])
				if strings.HasPrefix(in, "@") {
					cur = append(cur, tourSeg{text: in[1:], kind: '@'})
				} else {
					cur = append(cur, tourSeg{text: in, kind: 'k'})
				}
				i = j
			case r == ' ':
				flush()
				sp = true
			case tourWide(r):
				flush()
				toks = append(toks, tourTok{wd: tourWord{{text: string(r)}}, sp: sp})
				sp = false
			default:
				plain.WriteRune(r)
			}
		}
		flush()
		var line []tourTok
		lw := 0
		tokW := func(i int, t tourTok) int { // with its space, unless it starts the line
			n := m.tourWordW(t.wd)
			if i > 0 && t.sp {
				n++
			}
			return n
		}
		emit := func() {
			var b strings.Builder
			for i, t := range line {
				if i > 0 && t.sp {
					b.WriteString(" ")
				}
				b.WriteString(m.tourWordRender(t.wd))
			}
			if len(line) > 0 {
				out = append(out, b.String())
			}
			line, lw = nil, 0
		}
		for _, t := range toks {
			if len(line) > 0 && lw+tokW(len(line), t) > width {
				// closing punctuation does not start a line: it takes the
				// pieces glued before it along, as many as it takes
				// (ビ ュ ー → all three)
				var carry []tourTok
				head := t
				for len(line) > 1 && !head.sp && m.tourStartsClosing(head) {
					head = line[len(line)-1]
					carry = append([]tourTok{head}, carry...)
					line = line[:len(line)-1]
				}
				emit()
				for _, c := range carry {
					lw += tokW(len(line), c)
					line = append(line, c)
				}
			}
			lw += tokW(len(line), t)
			line = append(line, t)
		}
		emit()
	}
	return out
}

// selectCells paints n cells of line from column x with the select fill
// (reverse video without truecolor), keeping their colors.
func (m *model) selectCells(line string, x, n int) string {
	if n <= 0 || x < 0 {
		return line
	}
	th := m.th
	pre := ansi.Truncate(line, x, "")
	if pw := w(pre); pw < x {
		pre += strings.Repeat(" ", x-pw)
	}
	mid := ansi.Cut(line, x, x+n)
	if d := n - w(mid); d > 0 {
		mid += strings.Repeat(" ", d)
	}
	post := ansi.TruncateLeft(line, x+n, "")
	if th.Mode == theme.Truecolor {
		mid = lipgloss.NewStyle().Background(th.Color(theme.Select)).Render(strings.ReplaceAll(mid, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Select)))
	} else {
		mid = lipgloss.NewStyle().Reverse(true).Render(ansi.Strip(mid))
	}
	return pre + "\x1b[0m" + mid + "\x1b[0m" + post
}

// ---- where things are on screen ----

// panelRect is panel n (pSessions … pAgent) on screen. Below 100 columns
// the left panels share the one tabbed panel at the top.
func (m *model) panelRect(n int) rect {
	bodyH := m.height - 1
	if narrow(m.width) {
		top := m.narrowTopH()
		if n == pAgent {
			return rect{0, top, m.width, max(bodyH-top, 0)}
		}
		return rect{0, 0, m.width, top}
	}
	lw := m.leftWidth()
	if n == pAgent {
		return rect{lw + 1, 0, m.width - lw - 1, bodyH}
	}
	hs := m.leftHeights(lw, bodyH)
	y := 0
	for i := 0; i < n-1; i++ {
		y += hs[i]
	}
	return rect{0, y, lw, max(min(hs[n-1], bodyH-y), 0)}
}

// modalRect is where overlay puts the open modal.
func (m *model) modalRect() (rect, bool) {
	if m.modal == nil {
		return rect{}, false
	}
	block := m.modal.view()
	bw := 0
	for _, l := range block {
		bw = max(bw, w(l))
	}
	bodyH := m.height - 1
	return rect{max((m.width-bw)/2, 0), max((bodyH-len(block))/2-1, 0), bw, len(block)}, true
}

// sessionRowsAt is where the session rows of [1] are: the content's left
// edge and width, the first row's y, and the rows; ok=false when they do
// not all fit (the panel scrolls).
func (m *model) sessionRowsAt() (x, cw, y int, rows []row, ok bool) {
	r := m.panelRect(pSessions)
	cw = r.w - 4
	rows, footer := m.sessionRows(cw)
	avail := r.h - 2
	if footer != "" && !narrow(m.width) {
		avail--
	}
	return r.x + 2, cw, r.y + 1, rows, len(rows) <= avail && m.width > 0
}
