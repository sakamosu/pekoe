package tui

// The tutorial: coach marks over the TUI. One step puts one element of the
// screen in focus (a panel, the state column, a dialog) and explains it in a
// card next to it; ↵ / → / l / space go on, ← / h / backspace back, esc / q
// skip (L / H / Q too: Caps Lock on), tab switches between English and
// Japanese and stays on the step. It starts by itself the first time pekoe
// runs (paths.TutorialFile records that it was seen, skipped or not, and the
// language picked with tab) and
// again with H or :tutorial.
//
// The language: the one recorded, else the environment (LC_ALL, then
// LC_MESSAGES, then LANG; the first that is set decides): ja… is Japanese,
// anything else English. The step text is in tour_sample.go, both
// languages side by side; the card's frame (next/back/skip, sample · n/m)
// stays English.
//
// The tour runs on sample data (tour_sample.go), swapped in inside the model
// only: nothing is created in pekoed, nothing is written to state.db and no
// key reaches a pty or the daemon while it runs. Messages that arrive
// meanwhile (session lists, events, screens) are kept and replayed onto the
// real model when the tour ends, followed by a fresh load of everything.
//
// What is shown follows the guidelines' tutorial exceptions
// (docs/ui-guidelines.md「チュートリアル」): the element explained keeps its
// focus frame (the panel itself, or the dialog), cells inside a panel get the
// select fill, and everything else recedes — colors stripped, redrawn in the
// border role (faint without colors), the background left alone. The card
// has a heavy frame with the focus ramp (primary → primary_end, solid primary
// below truecolor), so the weight of the line tells it from the target's
// frame, and a primary title headed by tapi's one-line face: tapi tells the
// tour. Nothing moves.

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
	// tourLangKey switches the tour's language, in the tour only; not a
	// letter: Caps Lock turns l (next) into L
	tourLangKey = "tab"
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

// the tour's languages
const (
	tourJa = "ja"
	tourEn = "en"
)

type tourState struct {
	step    int       // index into tourSteps
	lang    string    // tourJa or tourEn
	picked  bool      // lang was switched with the key: recorded at the end
	saved   model     // the real model, put back when the tour ends
	pending []tea.Msg // arrived during the tour, replayed at the end
}

// rect is a cell area of the screen (the body above the bottom bar).
type rect struct{ x, y, w, h int }

// tourCopy is a step's text in one language.
type tourCopy struct {
	title string
	// body: {k} is a key (secondary, bold), {@running} a state's icon in
	// its color, ^q the prefix as configured
	body string
	// narrow replaces body below 100 columns, where the left column is one
	// tabbed panel without Browser and Picks
	narrow string
}

// tourStep is one coach mark: its text in each language, and one sample
// screen for both.
type tourStep struct {
	ja, en tourCopy
	setup  func(m *model) // puts the sample screen into shape (after tourBase)
	// target is what the step is about; ok=false centers the card
	target func(m *model) (r rect, ok bool)
	cells  func(m *model) []rect // cells painted with the select fill
}

// tourOther is the language the language key switches to.
func tourOther(lang string) string {
	if lang == tourJa {
		return tourEn
	}
	return tourJa
}

// text is the step's text in lang (tourJa, else English).
func (s tourStep) text(lang string) tourCopy {
	if lang == tourJa {
		return s.ja
	}
	return s.en
}

// tourEnvLang is the language the environment asks for: the first of
// LC_ALL, LC_MESSAGES and LANG that is set; ja… is Japanese, anything else
// (and none at all) English.
func tourEnvLang(getenv func(string) string) string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(k); v != "" {
			if strings.HasPrefix(v, "ja") {
				return tourJa
			}
			return tourEn
		}
	}
	return tourEn
}

// ---- record ----

type tourRecord struct {
	SeenAt time.Time `json:"seen_at"`
	Lang   string    `json:"lang,omitempty"` // the language picked with the key; none: the environment decides
}

// initTour reads the record at path ("" keeps none: the tour never starts
// by itself, as in tests): whether the tour was seen, and in which language.
func (m *model) initTour(path string) {
	m.tourPath = path
	m.tourAuto = false
	m.tourLang = ""
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		m.tourAuto = true
		return
	}
	var r tourRecord
	if err == nil && json.Unmarshal(b, &r) == nil && (r.Lang == tourJa || r.Lang == tourEn) {
		m.tourLang = r.Lang
	}
}

func saveTourSeen(path, lang string) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(tourRecord{SeenAt: time.Now(), Lang: lang})
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
	lang := m.tourLang
	if lang == "" {
		lang = tourEnvLang(os.Getenv)
	}
	m.tour = &tourState{saved: *m, lang: lang} // m.tour is still nil in the copy
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
	if t.picked { // only a language picked is kept; otherwise the locale decides next time
		m.tourLang = t.lang
	}
	if err := saveTourSeen(m.tourPath, m.tourLang); err != nil {
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
		case "enter", "right", "l", "L", " ": // L, H, Q: Caps Lock is on
			if m.tour.step == len(tourSteps)-1 {
				return m.endTour() // Done
			}
			m.tourGo(m.tour.step + 1)
		case "left", "h", "H", "backspace":
			if m.tour.step > 0 {
				m.tourGo(m.tour.step - 1)
			}
		case tourLangKey: // the other language, same step
			m.tour.picked = true
			m.tour.lang = tourOther(m.tour.lang)
		case "esc", "q", "Q":
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

// tourHints are the card's keys; the language key comes last (the first to
// go when the card is narrow) and is named by the code of the language it
// switches to (en / ja).
func (m *model) tourHints() [][2]string {
	lang := [2]string{tourLangKey, tourOther(m.tour.lang)}
	if m.tour.step == len(tourSteps)-1 {
		return [][2]string{{"↵", "done"}, {"←", "back"}, lang}
	}
	if m.tour.step == 0 {
		return [][2]string{{"↵", "next"}, {"esc", "skip"}, lang}
	}
	return [][2]string{{"↵", "next"}, {"←", "back"}, {"esc", "skip"}, lang}
}

// tourCopy is the current step's text in the tour's language.
func (m *model) tourCopy() tourCopy {
	return tourSteps[m.tour.step].text(m.tour.lang)
}

// tourBody is the text the card shows: narrow below 100 columns when the
// step has one, the prefix as configured.
func (m *model) tourBody() string {
	c := m.tourCopy()
	body := c.body
	if narrow(m.width) && c.narrow != "" {
		body = c.narrow
	}
	return strings.ReplaceAll(body, "^q", m.prefixLabel())
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
	// everything but the target recedes (the card is drawn over it next)
	for y := range body {
		if ok && y >= tgt.y && y < tgt.y+tgt.h {
			body[y] = m.dimCells(body[y], 0, tgt.x) + "\x1b[0m" + cutCells(body[y], tgt.x, tgt.x+tgt.w) + "\x1b[0m" + m.dimCells(body[y], tgt.x+tgt.w, m.width)
		} else {
			body[y] = m.dimCells(body[y], 0, m.width)
		}
	}
	x, y, card := m.tourPlace(tgt, ok, len(body))
	return placeAt(body, y, x, card)
}

// cutCells is the cells [a, b) of line with their colors, padded to b-a.
func cutCells(line string, a, b int) string {
	if b <= a {
		return ""
	}
	s := ansi.Cut(line, a, b)
	if d := b - a - w(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

// dimCells is the cells [a, b) of line receded: the text without its colors,
// in the border role (bright black without truecolor), or faint without
// colors. The background is left alone (the terminal's own, or bg), so the
// dimming looks the same everywhere.
func (m *model) dimCells(line string, a, b int) string {
	s := ansi.Strip(cutCells(line, a, b))
	if strings.TrimSpace(s) == "" {
		return s
	}
	if m.th.Mode == theme.None {
		return "\x1b[2m" + s + "\x1b[22m"
	}
	return m.th.FG(theme.Border, s)
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
	inner := max(width-4, 1)
	right := th.FG(theme.Dim, fmt.Sprintf("sample · %d/%d", m.tour.step+1, len(tourSteps)))
	// tapi tells the tour: its face heads the card (the bar keeps only its
	// speech meanwhile, docs/mascot.md: one face at a time). Always the
	// done eyes, ^ ^: plain ASCII, one cell in every font and glyph setting.
	// The tour is the first screen a new user sees, before any [mascot]
	// glyphs is set, and ● (work) or • (idle) are East Asian Ambiguous:
	// fonts like Moralerspace draw them two cells wide and the face breaks.
	head := m.tapi.r.Line(m.tapi.r.Eyes(mascot.Done)) + " " + th.Bold(theme.Primary, m.tourCopy().title)
	if gap := inner - w(head) - w(right); gap >= 1 {
		head += strings.Repeat(" ", gap) + right
	}
	text := m.tourText(m.tourBody(), inner)
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
	// the frame is the focus ramp (primary → primary_end along the
	// diagonal, solid primary without truecolor), so the eye finds the card
	// at once; static, like every focus frame
	height := len(lines) + 2
	b := m.borderPainter(theme.Primary, width, height)
	// a heavy frame: the target's focus frame has the same ramp, so the
	// weight of the line is what tells the card from it
	tl, tr, bl, br, vt := "┏", "┓", "┗", "┛", "┃"
	hz := strings.Repeat("━", max(width-2, 0))
	surf := func(s string) string {
		s = " " + fit(s, inner) + " "
		if th.Mode == theme.Truecolor {
			return lipgloss.NewStyle().Background(th.Color(theme.Surface)).Render(strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Surface)))
		}
		return s
	}
	out := []string{b(0, 0, tl+hz+tr)}
	for i, l := range lines {
		out = append(out, b(0, i+1, vt)+surf(l)+b(width-1, i+1, vt))
	}
	out = append(out, b(0, height-1, bl+hz+br))
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

// tourLook is the icon of {@name}: a session state or "overlap" (⚠).
func (m *model) tourLook(name string) theme.StateLook {
	switch name {
	case "overlap":
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
// edge and width, the first row's y, the rows and what each selects
// (sessionRowsSel: headings are rowNone); ok=false when they do not all fit
// (the panel scrolls).
func (m *model) sessionRowsAt() (x, cw, y int, rows []row, sels []int, ok bool) {
	r := m.panelRect(pSessions)
	cw = r.w - 4
	rows, sels, footer := m.sessionRowsSel(cw)
	avail := r.h - 2
	if footer != "" && !narrow(m.width) {
		avail--
	}
	return r.x + 2, cw, r.y + 1, rows, sels, len(rows) <= avail && m.width > 0
}
