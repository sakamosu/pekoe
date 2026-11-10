package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/mascot"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// The preview window: a screenshot of the session's dev server
// (http://localhost:<port><path>, taken by pekoed with headless Chrome) drawn
// in the main panel with Kitty graphics, so checking a UI change needs no
// trip to Chrome. It is the counterpart of the Chrome pick: the pick asks for
// a change, the preview checks the result. pekoed retakes it when the agent
// stops; the TUI only shows it.

// winPreview is drawn by the TUI itself: no tmux window behind it.
const winPreview = "preview"

// side-by-side before/after needs at least this many columns per image
const minCompareCols = 24

type previewState struct {
	view     *daemon.Preview // the shown session's preview
	compare  bool            // c: before/after
	watching string          // slug that preview.watch is on for
	asked    map[string]bool // first capture already requested on open
	live     map[gfxKey]int  // images living in the terminal: what -> image id
	nextID   int
	cellW    int // cell size in pixels (TIOCGWINSZ); 0 = unknown
	cellH    int
}

func newPreviewState() *previewState {
	p := &previewState{asked: map[string]bool{}, live: map[gfxKey]int{}}
	p.cellW, p.cellH = cellPixels()
	return p
}

type previewMsg struct {
	slug string
	p    *daemon.Preview
	err  error
}

// cellPixels asks the terminal how many pixels a cell is (0, 0 if unknown).
func cellPixels() (int, int) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row)
}

// fitCells is the largest cols×rows box (≤ maxCols×maxRows) holding an
// imgW×imgH pixel image at its aspect ratio, given the cell size in pixels.
func fitCells(imgW, imgH, maxCols, maxRows, cellW, cellH int) (int, int) {
	if imgW <= 0 || imgH <= 0 || maxCols <= 0 || maxRows <= 0 {
		return 0, 0
	}
	if cellW <= 0 || cellH <= 0 {
		cellW, cellH = 1, 2 // the usual terminal cell is about twice as tall as wide
	}
	wc := float64(imgW) / float64(cellW) // image size in cells at 1:1
	hc := float64(imgH) / float64(cellH)
	s := math.Min(float64(maxCols)/wc, float64(maxRows)/hc)
	cols := clampi(int(math.Round(wc*s)), 1, maxCols)
	rows := clampi(int(math.Round(hc*s)), 1, maxRows)
	return cols, rows
}

// gfxKey identifies one transmitted image: a file shown at a size.
type gfxKey struct {
	file       string
	cols, rows int
}

// pvSlot is one image placed in the preview body (cell coordinates).
type pvSlot struct {
	shot       *daemon.Shot
	label      string // "before" / "after" in compare mode
	x, y       int
	cols, rows int
}

func (s pvSlot) key() gfxKey { return gfxKey{s.shot.File, s.cols, s.rows} }

// previewLayout places the screenshot(s) in a cw×ch body, centered, aspect
// kept. compare shows before and after side by side when both fit at
// minCompareCols, else only before (c toggles back to after).
func previewLayout(p *daemon.Preview, compare bool, cw, ch, cellW, cellH int) []pvSlot {
	if p == nil || p.After == nil || cw < 4 || ch < 3 {
		return nil
	}
	place := func(sh *daemon.Shot, label string, x0, w, y0, h int) pvSlot {
		cols, rows := fitCells(sh.Width, sh.Height, w, h, cellW, cellH)
		return pvSlot{shot: sh, label: label, x: x0 + (w-cols)/2, y: y0 + (h-rows)/2, cols: cols, rows: rows}
	}
	if !compare || p.Before == nil {
		return []pvSlot{place(p.After, "", 0, cw, 0, ch)}
	}
	if half := (cw - 2) / 2; half >= minCompareCols {
		return []pvSlot{place(p.Before, "before", 0, half, 1, ch-1), place(p.After, "after", cw-half, half, 1, ch-1)}
	}
	return []pvSlot{place(p.Before, "before", 0, cw, 1, ch-1)}
}

// gfxSync turns the images the terminal holds into the wanted set: stale
// ones are deleted (so nothing is left behind after a resize or a session
// switch), new ones transmitted. Unchanged ones are left alone.
func gfxSync(live map[gfxKey]int, want []gfxKey, alloc func() int) (string, map[gfxKey]int) {
	next := map[gfxKey]int{}
	wanted := map[gfxKey]bool{}
	for _, k := range want {
		wanted[k] = true
	}
	var b strings.Builder
	var stale []int
	for k, id := range live {
		if !wanted[k] {
			stale = append(stale, id)
		}
	}
	sort.Ints(stale)
	for _, id := range stale {
		b.WriteString(deleteImage(id))
	}
	for _, k := range want {
		if _, ok := next[k]; ok {
			continue
		}
		if id, ok := live[k]; ok {
			next[k] = id
			continue
		}
		id := alloc()
		next[k] = id
		b.WriteString(transmitImage(id, k.file, k.cols, k.rows))
	}
	return b.String(), next
}

// previewImageID: 0x0C…… (picks use 0x0B……); the id travels as a 24-bit color.
func (p *previewState) alloc() int {
	p.nextID = p.nextID%0xFFFF + 1
	return 0x0C0000 | p.nextID
}

// deleteAll frees every preview image (on quit).
func (p *previewState) deleteAll() string {
	seq, _ := gfxSync(p.live, nil, nil)
	p.live = map[gfxKey]int{}
	return seq
}

// previewBody is the cell area the images get: the panel's content minus
// the footer row.
func (m *model) previewBody() (int, int) {
	cw, ch := m.agentSize()
	return cw, ch - 1
}

// previewSync runs after every update: it keeps preview.watch on exactly
// the session whose preview is shown, and the terminal's images in step
// with what View draws.
func (m *model) previewSync() tea.Cmd {
	pv := m.pv
	if pv == nil {
		return nil
	}
	var cmds []tea.Cmd
	want := ""
	if s := m.current(); s != nil && m.window == winPreview {
		want = s.Slug
	}
	if want != pv.watching {
		old := pv.watching
		pv.watching = want
		if pv.view != nil && pv.view.Slug != want {
			pv.view = nil
		}
		cl := m.cl
		cmds = append(cmds, func() tea.Msg {
			if cl == nil {
				return nil
			}
			if old != "" {
				_ = cl.Call("preview.watch", map[string]any{"slug": old, "on": false}, nil)
			}
			if want == "" {
				return nil
			}
			var p daemon.Preview
			err := cl.Call("preview.watch", map[string]any{"slug": want, "on": true}, &p)
			return previewMsg{slug: want, p: &p, err: err}
		})
	}
	if m.graphics {
		var keys []gfxKey
		if want != "" && m.review == nil && !m.issueShown() { // the issue view covers the preview
			cw, ch := m.previewBody()
			for _, sl := range previewLayout(pv.view, pv.compare, cw, ch, pv.cellW, pv.cellH) {
				keys = append(keys, sl.key())
			}
		}
		seq, live := gfxSync(pv.live, keys, pv.alloc)
		pv.live = live
		if seq != "" {
			cmds = append(cmds, writeTerm(seq))
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) onPreviewMsg(msg previewMsg) tea.Cmd {
	if msg.err != nil {
		m.setMsg(msg.err.Error(), true)
		return nil
	}
	if msg.slug != m.pv.watching || msg.p == nil {
		return nil
	}
	m.setPreview(msg.p)
	p := m.pv.view
	// nothing taken yet: take the first one right away (once per session)
	if p.After == nil && !p.Busy && p.Error == "" && !m.pv.asked[msg.slug] {
		m.pv.asked[msg.slug] = true
		return m.capturePreview(nil)
	}
	return nil
}

// setPreview keeps the newest state (events are sent concurrently).
func (m *model) setPreview(p *daemon.Preview) {
	if p.Slug != m.pv.watching {
		return
	}
	if cur := m.pv.view; cur != nil && cur.Slug == p.Slug && p.Rev < cur.Rev {
		return
	}
	m.pv.view = p
}

func (m *model) onPreviewEvent(ev client.Event) {
	var p daemon.Preview
	if json.Unmarshal(ev.Data, &p) == nil {
		m.setPreview(&p)
	}
}

// ---- actions ----

func (m *model) openPreview() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	if s.Port == 0 {
		return func() tea.Msg { return errMsg{errors.New("this session has no port")} }
	}
	m.window = winPreview
	m.focus = pAgent
	m.typing = false
	m.screen = nil
	return nil // previewSync turns preview.watch on
}

func (m *model) closePreview() tea.Cmd {
	m.window = engine.WinAgent
	m.screen = nil
	return m.fetchScreen()
}

// capturePreview asks pekoed for a screenshot now (path / mobile optional).
func (m *model) capturePreview(params map[string]any) tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	p := map[string]any{"slug": s.Slug}
	for k, v := range params {
		p[k] = v
	}
	if v := m.pv.view; v != nil && v.Slug == s.Slug {
		v.Busy, v.Error = true, ""
	}
	slug, cl := s.Slug, m.cl
	return func() tea.Msg {
		if cl == nil {
			return nil
		}
		var res daemon.Preview
		err := cl.Call("preview.capture", p, &res)
		return previewMsg{slug: slug, p: &res, err: err}
	}
}

func (m *model) previewURL(s *state.Session) string {
	path := "/"
	if v := m.pv.view; v != nil && v.Slug == s.Slug && v.Path != "" {
		path = v.Path
	}
	return fmt.Sprintf("http://localhost:%d%s", s.Port, path)
}

// openChrome opens url in Google Chrome on macOS (where the extension
// sees it), else in the default browser (openURL: open / xdg-open).
func openChrome(url string) error {
	if runtime.GOOS == "darwin" && exec.Command("open", "-a", "Google Chrome", url).Run() == nil {
		return nil
	}
	return openURL(url)
}

func (m *model) keyPreview(key string) tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	v := m.pv.view
	switch key {
	case "R":
		return m.capturePreview(nil)
	case "m":
		mobile := v == nil || !v.Mobile
		return m.capturePreview(map[string]any{"mobile": mobile})
	case "c":
		m.pv.compare = !m.pv.compare
		return nil
	case "g":
		path := "/"
		if v != nil {
			path = v.Path
		}
		m.modal = newPathModal(m, s, path)
		return nil
	case "o":
		url := m.previewURL(s)
		return func() tea.Msg {
			if err := openChrome(url); err != nil {
				return errMsg{err}
			}
			return infoMsg("opened " + url)
		}
	case "esc":
		return m.closePreview()
	case "v": // agent → preview → dev → agent
		if s.Dev {
			m.window = engine.WinDev
			m.screen = nil
			return m.fetchScreen()
		}
		return m.closePreview()
	case "h", "left":
		m.focus = pSessions
		return nil
	case "i", "enter", "a":
		m.typing = true
		delete(m.unreadAt, s.Slug)
		return m.closePreview()
	case "r", "n":
		cmd, _ := m.common(key)
		return cmd
	}
	return m.sessionAction(key, s)
}

func (m *model) previewHints() [][2]string {
	mode := "mobile"
	if v := m.pv.view; v != nil && v.Mobile {
		mode = "desktop"
	}
	return [][2]string{{"R", "retake"}, {"m", mode}, {"c", "compare"}, {"g", "path"}, {"o", "chrome"}, {"?", "help"}}
}

func (m *model) previewKeysFull() [][3]string {
	return [][3]string{
		{"R", "retake the screenshot", "R"},
		{"m", "desktop / mobile (390×844)", "m"},
		{"c", "before / after", "c"},
		{"g", "go to a path (/settings …)", "g"},
		{"o", "open the page in Chrome", "o"},
		{"v", "next window (dev / agent)", "v"},
		{"esc", "back to the agent", "esc"},
		{"i", "type into the agent", "i"},
		{"d", "dev server start/stop", "d"},
		{"r", "review", "r"},
	}
}

// ---- view ----

func (m *model) viewPreview(width, height int, s *state.Session) []string {
	th := m.th
	f := m.focus == pAgent
	p := m.pv.view
	if p != nil && p.Slug != s.Slug {
		p = nil
	}
	path := "/"
	if p != nil && p.Path != "" {
		path = p.Path
	}
	extra := fmt.Sprintf(":%d%s", s.Port, path)
	if p != nil && p.Mobile {
		extra += " · mobile"
	}
	if m.pv.compare {
		extra += " · before/after"
	}
	if p != nil && p.Busy {
		extra += " · " + th.FG(theme.Secondary, "capturing")
	}
	title := m.panelTitle(4, m.ic.Browser, "Preview "+s.Slug, extra, f)
	cw, ch := width-4, height-2
	if p == nil || p.After == nil {
		var rows []row
		switch {
		case p == nil:
			rows = []row{{Text: th.FG(theme.Dim, "…")}}
		case p.Busy:
			rows = m.bigRows(cw, ch, mascot.Work, "capturing "+strings.TrimPrefix(m.previewURL(s), "http://"), "headless Chrome")
		case p.Error != "":
			mood := mascot.Error
			if strings.HasPrefix(p.Error, "dev server") {
				mood = mascot.Idle
			}
			rows = m.bigRows(cw, ch, mood, p.Error, "R retry")
		default:
			rows = m.bigRows(cw, ch, mascot.Idle, "no screenshot yet", "R take one")
		}
		return m.box(width, height, boxOpt{title: title, rows: rows, focused: f})
	}
	right := th.FG(theme.Dim, theme.Ago(time.Since(p.After.At))+" ago")
	return m.box(width, height, boxOpt{title: title, right: right, rows: m.previewRows(p, cw, ch-1), footer: m.previewFooter(p), focused: f})
}

// previewRows draws the body: Kitty placeholder cells where the images go,
// or dashed placeholders with size and format on terminals without graphics.
func (m *model) previewRows(p *daemon.Preview, cw, ch int) []row {
	th := m.th
	slots := previewLayout(p, m.pv.compare, cw, ch, m.pv.cellW, m.pv.cellH)
	type seg struct {
		x    int
		text string
		w    int
	}
	lines := make([][]seg, ch)
	add := func(y, x int, text string, w int) {
		if y >= 0 && y < ch {
			lines[y] = append(lines[y], seg{x, text, w})
		}
	}
	for _, sl := range slots {
		if sl.label != "" {
			l := sl.label + " · " + theme.Ago(time.Since(sl.shot.At)) + " ago"
			add(sl.y-1, sl.x, th.FG(theme.Dim, clip(l, sl.cols)), min(w(l), sl.cols))
		}
		id, ok := m.pv.live[sl.key()]
		if m.graphics && ok {
			for r := 0; r < sl.rows; r++ {
				add(sl.y+r, sl.x, placeholderRow(id, r, sl.cols), sl.cols)
			}
			continue
		}
		for r, l := range m.dashedShot(sl) {
			add(sl.y+r, sl.x, l, sl.cols)
		}
	}
	rows := make([]row, ch)
	for y, segs := range lines {
		sort.Slice(segs, func(i, j int) bool { return segs[i].x < segs[j].x })
		var b strings.Builder
		at := 0
		for _, sg := range segs {
			if sg.x > at {
				b.WriteString(strings.Repeat(" ", sg.x-at))
				at = sg.x
			}
			b.WriteString(sg.text)
			at += sg.w
		}
		rows[y] = row{Text: b.String()}
	}
	return rows
}

// dashedShot is the placeholder for an image the terminal cannot draw:
// a dashed frame of the image's size with "after 1280×800 · png" inside.
func (m *model) dashedShot(sl pvSlot) []string {
	th := m.th
	name := sl.label
	if name == "" {
		name = "after"
	}
	text := fmt.Sprintf("%s %d×%d · png", name, sl.shot.Width, sl.shot.Height)
	if sl.rows < 3 || sl.cols < 6 {
		return []string{th.FG(theme.Dim, fit(text, sl.cols))}
	}
	in := sl.cols - 2
	out := []string{th.FG(theme.Border, strings.Repeat("┄", sl.cols))}
	mid := (sl.rows - 2) / 2
	for r := 0; r < sl.rows-2; r++ {
		t := ""
		if r == mid {
			t = text
		}
		pad := max(in-w(t), 0)
		cell := strings.Repeat(" ", pad/2) + th.FG(theme.Dim, clip(t, in)) + strings.Repeat(" ", pad-pad/2)
		out = append(out, th.FG(theme.Border, "┆")+cell+th.FG(theme.Border, "┆"))
	}
	return append(out, th.FG(theme.Border, strings.Repeat("┄", sl.cols)))
}

func (m *model) previewFooter(p *daemon.Preview) string {
	th := m.th
	shot := func(name string, s *daemon.Shot) string {
		return fmt.Sprintf("%s %d×%d · %s ago", name, s.Width, s.Height, theme.Ago(time.Since(s.At)))
	}
	parts := []string{shot("after", p.After)}
	switch {
	case p.Before != nil:
		parts = append(parts, shot("before", p.Before))
	case m.pv.compare:
		parts = append(parts, "no earlier shot yet")
	}
	out := th.FG(theme.Dim, strings.Join(parts, "   "))
	if p.Error != "" {
		out = th.FG(theme.Warn, theme.Prefix(m.ic.Warning)) + th.FG(theme.Dim, p.Error) + "   " + out
	}
	return out
}

// ---- g: path input ----

type pathModal struct {
	m    *model
	slug string
	port int
	in   *input
}

func newPathModal(m *model, s *state.Session, path string) *pathModal {
	return &pathModal{m: m, slug: s.Slug, port: s.Port, in: &input{text: []rune(path), pos: len([]rune(path))}}
}

func (p *pathModal) mode() (string, string) { return "PATH", p.m.ic.Browser }
func (p *pathModal) hints() [][2]string {
	return [][2]string{{"↵", "capture"}, {"esc", "cancel"}}
}

func (p *pathModal) view() []string {
	th := p.m.th
	lines := []string{
		"",
		th.FG(theme.Dim, fmt.Sprintf("localhost:%d", p.port)) + p.in.render(th, true),
		"",
		th.FG(theme.Dim, "a path like /settings · a pasted URL keeps its path"),
	}
	return p.m.modalFrame(p.m.modalWidth(64), th.Bold(theme.Primary, "Preview path"), lines, p.hints(), false)
}

func (p *pathModal) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		return done(nil)
	case "enter":
		path := strings.TrimSpace(p.in.String())
		if path == "" {
			path = "/"
		}
		return done(p.m.capturePreview(map[string]any{"path": path}))
	}
	p.in.key(k)
	return nil
}
