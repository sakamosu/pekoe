package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"boba/internal/client"
	"boba/internal/config"
	"boba/internal/daemon"
	"boba/internal/gitx"
	"boba/internal/state"
	"boba/internal/theme"
)

type reviewMsg struct {
	rv  *daemon.ReviewView
	s   *state.Session
	err error
}

type reviewAnswerMsg struct{ text string }

// drow is one display row of the diff panel.
type drow struct {
	kind   string // header | line | comment | hidden
	file   int
	hunk   int
	line   *gitx.Line
	text   string
	cid    int64
	hunkID string
}

type reviewModel struct {
	cl            *client.Client
	slug          string
	th            *theme.Theme
	ic            theme.IconSet
	m             *model // for shared drawing helpers
	width, height int

	rv          *daemon.ReviewView
	sess        *state.Session
	file        int
	cursor      int
	scroll      int
	focus       int // 1 files, 2 comments, 3 session, 4 diff
	showApprove bool
	rows        []drow
	input       *input
	inputKind   string // comment | reject | ask
	answer      string
	asking      bool
	standalone  bool
	msg         string
	msgErr      bool
	br          bool // a ] or [ was just pressed: swallow the following h
}

func newReviewModel(cl *client.Client, slug string, th *theme.Theme, ic theme.IconSet, w, h int, square bool) *reviewModel {
	g, _ := config.LoadGlobal()
	if g == nil {
		g = &config.Global{}
	}
	shell := &model{cl: cl, th: th, ic: ic, g: g, width: w, height: h, square: square}
	return &reviewModel{cl: cl, slug: slug, th: th, ic: ic, m: shell, width: w, height: h, focus: 4}
}

// RunReview opens the review surface for a session from the CLI:
// nvim (+ boba.nvim) when configured, otherwise the built-in screen.
func RunReview(cl *client.Client, slug string) error {
	g, _ := config.LoadGlobal()
	var s state.Session
	if err := cl.Call("session.get", map[string]any{"slug": slug}, &s); err != nil {
		return err
	}
	_ = cl.Call("review.start", map[string]any{"slug": slug}, nil)
	if g != nil && g.Review.Surface == "nvim" {
		if nv, err := exec.LookPath("nvim"); err == nil {
			args := []string{"nvim"}
			if rtp := NvimPluginDir(); rtp != "" {
				args = append(args, "-c", "set rtp+="+rtp+" | runtime plugin/boba.lua")
			}
			env := append(os.Environ(), "BOBA_SESSION="+slug, "BOBA_REVIEW=1")
			if err := os.Chdir(s.Path); err != nil {
				return err
			}
			return syscall.Exec(nv, args, env)
		}
	}
	if err := cl.Subscribe("review.", "session."); err != nil {
		return err
	}
	w, h := TermSize()
	th := theme.Load(&g.Theme)
	rm := newReviewModel(cl, slug, th, theme.Icons(theme.IconsFromEnv()), w, h, os.Getenv("BOBA_SQUARE") != "")
	rm.standalone = true
	p := tea.NewProgram(&standaloneReview{rm}, tea.WithAltScreen())
	go pump(cl, p)
	_, err := p.Run()
	return err
}

// NvimPluginDir finds boba's nvim/ directory next to the binary or source.
func NvimPluginDir() string {
	if d := os.Getenv("BOBA_NVIM_RTP"); d != "" {
		return d
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	for _, c := range []string{
		filepath.Join(filepath.Dir(exe), "..", "nvim"),
		filepath.Join(filepath.Dir(exe), "..", "share", "boba", "nvim"),
		filepath.Join(filepath.Dir(exe), "nvim"),
	} {
		if _, err := os.Stat(filepath.Join(c, "lua", "boba", "init.lua")); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

type standaloneReview struct{ r *reviewModel }

func (s *standaloneReview) Init() tea.Cmd { return s.r.init() }
func (s *standaloneReview) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		s.r.width, s.r.height = ws.Width, ws.Height
		s.r.m.width, s.r.m.height = ws.Width, ws.Height
		return s, nil
	}
	cmd, done := s.r.update(msg)
	if done {
		return s, tea.Quit
	}
	return s, cmd
}
func (s *standaloneReview) View() string { return s.r.view() }

func (r *reviewModel) init() tea.Cmd { return r.load() }

func (r *reviewModel) load() tea.Cmd {
	slug := r.slug
	return func() tea.Msg {
		var rv daemon.ReviewView
		if err := r.cl.Call("review.files", map[string]any{"slug": slug, "show_approved": true}, &rv); err != nil {
			return reviewMsg{err: err}
		}
		var s state.Session
		_ = r.cl.Call("session.get", map[string]any{"slug": slug}, &s)
		return reviewMsg{rv: &rv, s: &s}
	}
}

func (r *reviewModel) setMsg(s string, isErr bool) { r.msg, r.msgErr = s, isErr }

func (r *reviewModel) call(method string, params map[string]any, ok string) tea.Cmd {
	params["slug"] = r.slug
	return func() tea.Msg {
		if err := r.cl.Call(method, params, nil); err != nil {
			return errMsg{err}
		}
		if ok != "" {
			return infoMsg(ok)
		}
		return nil
	}
}

func (r *reviewModel) update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case reviewMsg:
		if msg.err != nil {
			r.setMsg(msg.err.Error(), true)
			return nil, false
		}
		keepHunk := r.curHunkID()
		r.rv, r.sess = msg.rv, msg.s
		r.file = clampi(r.file, 0, max(len(r.rv.Files)-1, 0))
		r.build()
		if keepHunk != "" {
			r.gotoHunkID(keepHunk)
		}
		return nil, false
	case errMsg:
		r.setMsg(msg.err.Error(), true)
		return nil, false
	case infoMsg:
		r.setMsg(string(msg), false)
		return nil, false
	case evMsg:
		ev := client.Event(msg)
		switch ev.Event {
		case "review.updated":
			var p struct {
				Slug string `json:"slug"`
			}
			json.Unmarshal(ev.Data, &p)
			if p.Slug == r.slug {
				return r.load(), false
			}
		case "review.answer":
			var a struct {
				Slug string `json:"slug"`
				Text string `json:"text"`
			}
			json.Unmarshal(ev.Data, &a)
			if a.Slug == r.slug {
				r.answer, r.asking = a.Text, false
			}
		case "session.updated", "session.done":
			var s state.Session
			if json.Unmarshal(ev.Data, &s) == nil && s.Slug == r.slug {
				r.sess = &s
			}
		}
		return nil, false
	case tea.KeyMsg:
		return r.key(msg)
	}
	return nil, false
}

// ---- rows ----

func (r *reviewModel) build() {
	r.rows = nil
	if r.rv == nil || len(r.rv.Files) == 0 {
		return
	}
	f := r.rv.Files[r.file]
	comments := map[int][]state.Comment{}
	for _, c := range r.rv.Comments {
		if c.Path == f.Path {
			comments[c.Line] = append(comments[c.Line], c)
		}
	}
	for hi := range f.Hunks {
		h := &f.Hunks[hi]
		r.rows = append(r.rows, drow{kind: "header", file: r.file, hunk: hi, hunkID: h.ID})
		if h.Approved && !r.showApprove {
			continue
		}
		for li := range h.Lines {
			l := &h.Lines[li]
			r.rows = append(r.rows, drow{kind: "line", file: r.file, hunk: hi, line: l, hunkID: h.ID})
			ln := l.New
			if l.Kind == "-" {
				ln = l.Old
			}
			for _, c := range comments[ln] {
				if c.HunkID == "" || c.HunkID == h.ID {
					r.rows = append(r.rows, drow{kind: "comment", file: r.file, hunk: hi, text: c.Text, cid: c.ID, hunkID: h.ID})
				}
			}
			delete(comments, ln)
		}
	}
	r.cursor = clampi(r.cursor, 0, max(len(r.rows)-1, 0))
}

func (r *reviewModel) curHunk() *daemon.HunkView {
	if r.rv == nil || r.cursor >= len(r.rows) || len(r.rows) == 0 {
		return nil
	}
	d := r.rows[r.cursor]
	return &r.rv.Files[d.file].Hunks[d.hunk]
}

func (r *reviewModel) curHunkID() string {
	if h := r.curHunk(); h != nil {
		return h.ID
	}
	return ""
}

func (r *reviewModel) gotoHunkID(id string) {
	for i, d := range r.rows {
		if d.kind == "header" && d.hunkID == id {
			r.cursor = i
			return
		}
	}
}

func (r *reviewModel) jumpHunk(dir int) {
	if r.rv == nil {
		return
	}
	cur := -1
	if len(r.rows) > 0 {
		cur = r.rows[r.cursor].hunk
	}
	for i := r.cursor + dir; i >= 0 && i < len(r.rows); i += dir {
		if r.rows[i].kind == "header" && r.rows[i].hunk != cur {
			r.cursor = i
			return
		}
	}
	// cross into the next/previous file
	nf := r.file + dir
	if nf >= 0 && nf < len(r.rv.Files) {
		r.file = nf
		r.build()
		if dir > 0 {
			r.cursor = 0
		} else {
			for i := len(r.rows) - 1; i >= 0; i-- {
				if r.rows[i].kind == "header" {
					r.cursor = i
					break
				}
			}
		}
	}
}

// nextUnapproved moves to the next hunk still needing a decision.
func (r *reviewModel) nextUnapproved() {
	start := r.cursor
	for tries := 0; tries < 200; tries++ {
		before := r.file*100000 + r.cursor
		r.jumpHunk(1)
		if h := r.curHunk(); h != nil && !h.Approved {
			return
		}
		if r.file*100000+r.cursor == before {
			r.cursor = start
			return
		}
	}
}

func (r *reviewModel) curLine() (path string, line int, hunkID string) {
	if r.rv == nil || len(r.rows) == 0 {
		return "", 0, ""
	}
	d := r.rows[r.cursor]
	f := r.rv.Files[d.file]
	h := f.Hunks[d.hunk]
	switch {
	case d.line != nil && d.line.Kind == "-":
		return f.Path, d.line.Old, h.ID
	case d.line != nil:
		return f.Path, d.line.New, h.ID
	}
	return f.Path, h.NewStart, h.ID
}

// ---- keys ----

func (r *reviewModel) key(k tea.KeyMsg) (tea.Cmd, bool) {
	if r.input != nil {
		switch k.String() {
		case "esc":
			r.input = nil
		case "enter":
			text := strings.TrimSpace(r.input.String())
			kind := r.inputKind
			r.input = nil
			path, line, hid := r.curLine()
			switch kind {
			case "comment":
				if text == "" {
					return nil, false
				}
				return r.call("review.comment", map[string]any{"path": path, "line": line, "hunk_id": hid, "text": text}, "comment saved (unsent)"), false
			case "reject":
				return r.call("review.reject", map[string]any{"hunk_id": hid, "text": text}, "hunk sent back"), false
			case "ask":
				r.asking, r.answer = true, ""
				return r.call("review.ask", map[string]any{"hunk_id": hid, "question": text}, "asked the agent"), false
			}
		default:
			r.input.key(k)
		}
		return nil, false
	}
	if r.answer != "" && (k.String() == "esc" || k.String() == "q") {
		r.answer = ""
		return nil, false
	}
	key := k.String()
	if r.br {
		r.br = false
		if key == "h" {
			return nil, false
		}
	}
	switch key {
	case "q", "esc":
		return nil, true
	case "1":
		r.focus = 1
	case "2":
		r.focus = 2
	case "3":
		r.focus = 3
	case "4", "enter", "l":
		r.focus = 4
	case "tab":
		r.focus = r.focus%4 + 1
	case "h":
		r.focus = 1
	case "j", "down":
		if r.focus == 1 {
			if r.rv != nil && r.file < len(r.rv.Files)-1 {
				r.file++
				r.cursor = 0
				r.build()
			}
		} else {
			r.cursor = clampi(r.cursor+1, 0, max(len(r.rows)-1, 0))
		}
	case "k", "up":
		if r.focus == 1 {
			if r.file > 0 {
				r.file--
				r.cursor = 0
				r.build()
			}
		} else {
			r.cursor = clampi(r.cursor-1, 0, max(len(r.rows)-1, 0))
		}
	case "]", "n":
		r.br = key == "]"
		r.jumpHunk(1)
	case "[", "p":
		r.br = key == "["
		r.jumpHunk(-1)
	case "a":
		h := r.curHunk()
		if h == nil {
			return nil, false
		}
		if h.Approved {
			return r.call("review.unapprove", map[string]any{"hunk_id": h.ID}, ""), false
		}
		h.Approved = true // optimistic; refreshed by review.updated
		r.rv.Approved++
		r.nextUnapproved()
		return r.call("review.approve", map[string]any{"hunk_id": h.ID}, ""), false
	case "x":
		if r.curHunk() != nil {
			r.input, r.inputKind = &input{}, "reject"
		}
	case "c":
		if r.curHunk() != nil {
			r.input, r.inputKind = &input{}, "comment"
		}
	case "?":
		if r.curHunk() != nil {
			r.input, r.inputKind = &input{}, "ask"
		}
	case "A":
		r.showApprove = !r.showApprove
		id := r.curHunkID()
		r.build()
		r.gotoHunkID(id)
	case "d":
		// delete the unsent comment under the cursor
		if r.cursor < len(r.rows) && r.rows[r.cursor].kind == "comment" {
			return r.call("review.comment.delete", map[string]any{"id": r.rows[r.cursor].cid}, "comment deleted"), false
		}
	case "S":
		slug := r.slug
		return func() tea.Msg {
			var res map[string]any
			if err := r.cl.Call("review.flush", map[string]any{"slug": slug}, &res); err != nil {
				return errMsg{err}
			}
			return infoMsg(fmt.Sprintf("review #%v recorded · %v comments sent", res["review"], res["sent"]))
		}, false
	case "R":
		return r.load(), false
	}
	return nil, false
}

// ---- view (02 in the mock) ----

func (r *reviewModel) view() string {
	m := r.m
	m.width, m.height = r.width, r.height
	th := r.th
	if r.rv == nil {
		return th.FG(theme.Dim, "loading review…")
	}
	lw := clampi(r.width*30/100, 30, 48)
	rw := r.width - lw - 1
	h := r.height - 1
	// [1] Files
	var frows []row
	for i, f := range r.rv.Files {
		icon := th.FG(theme.Warn, m.ic.Unsent)
		name := f.Path
		if f.Total > 0 && f.Approved == f.Total {
			icon = th.FG(theme.Ok, m.ic.Check)
			name = th.FG(theme.Dim, name)
		}
		cnt := th.FG(theme.Dim, fmt.Sprintf("%d hunks ", f.Total)) + th.FG(theme.Ok, fmt.Sprint(f.Approved))
		frows = append(frows, row{Text: icon + " " + fit(name, lw-4-2-12) + " " + cnt, Sel: i == r.file})
	}
	if len(frows) == 0 {
		frows = append(frows, row{Text: th.FG(theme.Dim, "no changes since "+r.rv.BaseLabel)})
	}
	test := ""
	switch r.rv.Test.Status {
	case "pass":
		test = " · " + th.FG(theme.Ok, m.ic.Check) + th.FG(theme.Dim, " test")
	case "fail":
		test = " · " + th.FG(theme.Err, m.ic.Fail) + th.FG(theme.Dim, " test")
	}
	footer := th.FG(theme.Dim, "since ") + th.FG(theme.Text, r.rv.BaseLabel) + th.FG(theme.Dim, fmt.Sprintf(" · %d hunks · ", r.rv.Total)) + th.FG(theme.Ok, fmt.Sprint(r.rv.Approved)) + th.FG(theme.Dim, " approved")
	footer2 := th.FG(theme.Ok, fmt.Sprintf("+%d", r.rv.Diffstat.Added)) + " " + th.FG(theme.Err, fmt.Sprintf("-%d", r.rv.Diffstat.Deleted)) + test
	frows = append(frows, row{}, row{Text: footer2})
	branch := r.slug
	if r.sess != nil {
		branch = r.sess.Branch
	}
	// [2] Comments
	var crows []row
	for _, c := range r.rv.Comments {
		tag := ""
		if c.Kind == "reject" {
			tag = th.FG(theme.Err, "✗ ")
		}
		crows = append(crows, row{Text: th.FG(theme.Dim, fit(fmt.Sprintf("%s:%d", filepath.Base(c.Path), c.Line), 14)) + tag + c.Text})
	}
	for _, rk := range r.rv.Risks {
		crows = append(crows, row{Text: th.FG(theme.Warn, m.ic.Warning) + " " + th.FG(theme.Dim, fmt.Sprintf("%s:%d ", filepath.Base(rk.Path), rk.Line)) + rk.Text})
	}
	if len(crows) == 0 {
		crows = append(crows, row{Text: th.FG(theme.Dim, "none · c to comment")})
	}
	// [3] Session
	var srows []row
	if s := r.sess; s != nil {
		lk := theme.Look(m.ic, s.State, 0, false)
		srows = append(srows, row{Text: th.FG(lk.Role, lk.Icon) + " " + lk.Word + th.FG(theme.Dim, fmt.Sprintf(" · %s · %d prompts since %s", theme.Ago(time.Since(s.CreatedAt)), r.rv.Prompts, r.rv.BaseLabel))})
		if hk := r.curHunk(); hk != nil && hk.Prompt != nil {
			srows = append(srows, row{Text: th.FG(theme.Dim, fmt.Sprintf("hunk ← prompt #%d ", hk.Prompt.N)) + th.FG(theme.Text, "\""+hk.Prompt.Text+"\"")})
		}
		if s.PRReview {
			srows = append(srows, row{Text: th.FG(theme.Dim, fmt.Sprintf("comments go to GitHub PR #%d", s.PR))})
		}
	}
	filesH := max(h-(len(crows)+2)-(len(srows)+2), 6)
	left := m.box(lw, filesH, boxOpt{title: m.panelTitle(1, m.ic.Changes, fmt.Sprintf("Files %d", len(r.rv.Files)), "· "+branch, r.focus == 1), rows: frows, footer: footer, focused: r.focus == 1})
	left = append(left, m.box(lw, len(crows)+2, boxOpt{title: m.panelTitle(2, m.ic.Comments, fmt.Sprintf("Comments %d", len(r.rv.Comments)), "· unsent", r.focus == 2), rows: crows, focused: r.focus == 2})...)
	left = append(left, m.box(lw, max(h-len(left), 3), boxOpt{title: m.panelTitle(3, m.ic.Sessions, "Session", "· "+branch, r.focus == 3), rows: srows, focused: r.focus == 3})...)
	right := r.diffPanel(rw, h)
	body := joinH(left, right, 1)
	body = append(body, r.bar())
	for i := range body {
		body[i] = fit(body[i], r.width)
	}
	out := strings.Join(body, "\n")
	if th.Mode == theme.Truecolor {
		out = "\x1b[0m" + strings.ReplaceAll(out, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Bg))
	}
	return out
}

func (r *reviewModel) diffPanel(width, height int) []string {
	m := r.m
	th := r.th
	f := r.focus == 4
	if len(r.rv.Files) == 0 {
		return m.box(width, height, boxOpt{title: m.panelTitle(4, m.ic.Diff, "Diff", "", f), rows: m.logoRows(width-4, height-2, "nothing to review"), focused: f})
	}
	file := r.rv.Files[r.file]
	cur := r.curHunk()
	hi := 0
	if len(r.rows) > 0 {
		hi = r.rows[r.cursor].hunk
	}
	title := m.panelTitle(4, m.ic.Diff, "Diff "+file.Path, fmt.Sprintf("· hunk %d/%d", hi+1, len(file.Hunks)), f)
	right := th.Bold(theme.Secondary, "A") + th.FG(theme.Dim, map[bool]string{true: " hide approved", false: " show approved"}[r.showApprove])
	cw := width - 4
	var rows []row
	for i, d := range r.rows {
		h := file.Hunks[d.hunk]
		var t string
		switch d.kind {
		case "header":
			mark := th.FG(theme.Dim, "○")
			switch {
			case h.Approved:
				mark = th.FG(theme.Ok, m.ic.Check)
			case h.Rejected:
				mark = th.FG(theme.Err, m.ic.Fail)
			case cur != nil && h.ID == cur.ID:
				mark = th.FG(theme.Primary, m.ic.Here)
			}
			hdr := strings.TrimSpace(strings.SplitN(h.Header, "@@", 3)[min(1, len(strings.SplitN(h.Header, "@@", 3))-1)])
			label := th.FG(theme.Dim, "@@ "+hdr+" @@")
			if h.Approved && !r.showApprove {
				label += th.FG(theme.Dim, "  approved · hidden (A to show)")
			} else if h.Prompt != nil {
				label += th.FG(theme.Dim, fmt.Sprintf("  ← 指示 #%d: ", h.Prompt.N)) + th.FG(theme.Text, h.Prompt.Text)
			}
			t = mark + " " + label
		case "line":
			l := d.line
			gutter, sign, role := " ", " ", theme.Dim
			oldN, newN := "", ""
			switch l.Kind {
			case "+":
				gutter, sign, role = th.FG(theme.Ok, "▎"), th.FG(theme.Ok, "+"), theme.Text
				newN = fmt.Sprint(l.New)
			case "-":
				gutter, sign, role = th.FG(theme.Err, "▎"), th.FG(theme.Err, "-"), theme.Text
				oldN = fmt.Sprint(l.Old)
			default:
				oldN, newN = fmt.Sprint(l.Old), fmt.Sprint(l.New)
			}
			if th.Mode != theme.Truecolor {
				gutter = " "
			}
			// diff text stays plain (no icons, no decoration)
			t = gutter + " " + th.FG(theme.Dim, fmt.Sprintf("%4s %4s ", oldN, newN)) + sign + " " + th.FG(role, ansi.Strip(strings.ReplaceAll(l.Text, "\t", "    ")))
		case "comment":
			t = strings.Repeat(" ", 13) + th.FG(theme.Secondary, theme.Prefix(m.ic.Comments)+d.text) + th.FG(theme.Dim, " — you, unsent")
		}
		rows = append(rows, row{Text: t, Sel: i == r.cursor && f})
	}
	// pinned bottom area: input / answer
	var tail []row
	if r.input != nil {
		label := map[string]string{"comment": "comment", "reject": "send back (why?)", "ask": "ask the agent (empty: ここの意図は?)"}[r.inputKind]
		tail = append(tail, row{Text: th.FG(theme.Border, strings.Repeat("─", cw))}, row{Text: th.FG(theme.Dim, label)}, row{Text: th.FG(theme.Secondary, "› ") + r.input.render(th, true)}, row{Text: m.keyHints([][2]string{{"↵", "save"}, {"esc", "cancel"}}, cw)})
	} else if r.answer != "" || r.asking {
		tail = append(tail, row{Text: th.FG(theme.Border, strings.Repeat("─", cw))})
		tail = append(tail, row{Text: th.FG(theme.Secondary, theme.Prefix(m.ic.Agent)) + th.FG(theme.Dim, "agent")})
		if r.asking {
			tail = append(tail, row{Text: th.FG(theme.Dim, "waiting for the answer…")})
		}
		for _, l := range strings.Split(ansi.Wordwrap(r.answer, cw, " "), "\n") {
			if len(tail) > 10 {
				break
			}
			tail = append(tail, row{Text: l})
		}
		tail = append(tail, row{Text: m.keyHints([][2]string{{"esc", "close"}}, cw)})
	}
	bodyH := height - 2 - 1 - len(tail)
	// keep the cursor visible
	if r.cursor < r.scroll {
		r.scroll = r.cursor
	}
	if r.cursor >= r.scroll+bodyH {
		r.scroll = r.cursor - bodyH + 1
	}
	r.scroll = clampi(r.scroll, 0, max(len(rows)-bodyH, 0))
	end := min(r.scroll+bodyH, len(rows))
	vis := append([]row(nil), rows[r.scroll:end]...)
	for len(vis) < bodyH {
		vis = append(vis, row{})
	}
	vis = append(vis, tail...)
	// footer: scrollbar track + position
	track := 20
	thumb := track
	pos := 0
	if len(rows) > bodyH {
		thumb = max(track*bodyH/len(rows), 1)
		pos = (track - thumb) * r.scroll / max(len(rows)-bodyH, 1)
	}
	bar := th.FG(theme.Border, strings.Repeat("│", pos)) + th.FG(theme.Dim, strings.Repeat("┃", thumb)) + th.FG(theme.Border, strings.Repeat("│", track-pos-thumb))
	unsent := 0
	if cur != nil {
		for _, c := range r.rv.Comments {
			if c.HunkID == cur.ID {
				unsent++
			}
		}
	}
	footer := bar + th.FG(theme.Dim, fmt.Sprintf("  hunk %d of %d · %d unsent comment on this hunk", hi+1, len(file.Hunks), unsent))
	return m.box(width, height, boxOpt{title: title, right: right, rows: vis, footer: footer, focused: f})
}

func (r *reviewModel) bar() string {
	m := r.m
	th := r.th
	branch := r.slug
	if r.sess != nil {
		branch = r.sess.Branch
	}
	segs := []string{
		m.pill(theme.Prefix(m.ic.Diff)+"REVIEW", theme.Primary, theme.Bg, true),
		m.pill(theme.Prefix(m.ic.Worktrees)+branch+" "+th.FG(theme.Dim, "since "+r.rv.BaseLabel), theme.Select, theme.Text, false),
		m.pill(th.FG(theme.Ok, fmt.Sprint(r.rv.Approved))+th.FG(theme.Dim, fmt.Sprintf("/%d approved  ", r.rv.Total))+th.FG(theme.Secondary, fmt.Sprint(len(r.rv.Comments)))+th.FG(theme.Dim, " comments"), theme.Surface, theme.Dim, false),
	}
	left := strings.Join(segs, " ")
	var right string
	if r.msg != "" {
		role := theme.Dim
		if r.msgErr {
			role = theme.Err
		}
		right = th.FG(role, r.msg)
	} else {
		right = m.keyHints([][2]string{{"a", "approve"}, {"x", "revert"}, {"c", "comment"}, {"?", "ask"}, {"]h", "next"}, {"S", "send"}}, max(r.width-w(left)-2, 0))
	}
	return left + strings.Repeat(" ", max(r.width-w(left)-w(right), 1)) + right
}
