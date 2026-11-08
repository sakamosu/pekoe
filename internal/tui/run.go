package tui

// The run window (`!` in Sessions): one command the agent asked the user to
// run, run in the session's worktree in a pty of its own — so a confirmation
// prompt or a password can be answered — then back to the agent, optionally
// telling it how the command ended. Not a resident shell: one command, then
// the window is closed.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"boba/internal/daemon"
	"boba/internal/engine"
	"boba/internal/theme"
)

// runPromptMsg answers `!`: the session's run window state and, when none
// is open, the command suggested from the agent's screen.
type runPromptMsg struct {
	slug    string
	st      daemon.RunState
	suggest string
}

type runStartedMsg struct {
	slug string
	st   daemon.RunState
}

// openRun is `!`: show the session's run window when one is open, otherwise
// ask for the command (pre-filled with what the agent's screen suggests).
func (m *model) openRun() tea.Cmd {
	s := m.current()
	if s == nil || m.cl == nil {
		return nil
	}
	slug := s.Slug
	return func() tea.Msg {
		var st daemon.RunState
		if err := m.cl.Call("run.start", map[string]any{"slug": slug}, &st); err != nil {
			return errMsg{err}
		}
		msg := runPromptMsg{slug: slug, st: st}
		if !st.Running && !st.Finished {
			var sc engine.Screen
			if m.cl.Call("session.screen", map[string]any{"slug": slug, "window": engine.WinAgent}, &sc) == nil {
				msg.suggest = runSuggest(sc.Lines)
			}
		}
		return msg
	}
}

func (m *model) onRunPrompt(msg runPromptMsg) tea.Cmd {
	s := m.current()
	if s == nil || s.Slug != msg.slug || m.modal != nil {
		return nil
	}
	if msg.st.Running || msg.st.Finished {
		return m.showRun()
	}
	m.modal = newRunModal(m, msg.slug, msg.suggest)
	return nil
}

func (m *model) startRun(slug, command string) tea.Cmd {
	if m.runs == nil {
		m.runs = map[string]string{}
	}
	m.runs[slug] = command
	aw, ah := m.agentSize()
	return func() tea.Msg {
		var st daemon.RunState
		if err := m.cl.Call("run.start", map[string]any{"slug": slug, "command": command, "width": aw, "height": ah}, &st); err != nil {
			return errMsg{err}
		}
		return runStartedMsg{slug: slug, st: st}
	}
}

func (m *model) onRunStarted(msg runStartedMsg) tea.Cmd {
	if s := m.current(); s == nil || s.Slug != msg.slug {
		return nil
	}
	if !msg.st.Started && !msg.st.Running && !msg.st.Finished {
		return nil
	}
	return m.showRun()
}

// showRun puts the run window in the main panel, typing into it.
func (m *model) showRun() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	m.window = engine.WinRun
	m.screen = nil
	m.focus = pAgent
	m.typing = true
	return m.fetchScreen()
}

// runDone reports whether the run window is shown and its command exited.
func (m *model) runDone() bool {
	return m.window == engine.WinRun && m.screen != nil && m.screen.Dead
}

// finishRun closes a finished run window (↵): back to the agent, which is
// asked whether to tell the agent how the command ended.
func (m *model) finishRun() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	slug := s.Slug
	cmd := m.runs[slug]
	code := -1
	if m.screen != nil {
		code = m.screen.ExitStatus
		if cmd == "" {
			cmd = runCommandOnScreen(m.screen)
		}
	}
	delete(m.runs, slug)
	m.window = engine.WinAgent
	m.screen = nil
	m.focus = pAgent
	m.typing = false
	m.modal = &runReportModal{m: m, slug: slug, text: runReport(cmd, code)}
	return tea.Batch(m.act("window.kill", map[string]any{"slug": slug, "window": engine.WinRun}, ""), m.fetchScreen())
}

// runReport is the line sent to the agent after a run.
func runReport(cmd string, code int) string {
	what := "頼まれたコマンド"
	if cmd != "" {
		what = "`" + cmd + "`"
	}
	if code < 0 {
		return what + " を実行しました"
	}
	return fmt.Sprintf("%s を実行しました(exit %d)", what, code)
}

// runCommandOnScreen recovers the command from the "$ cmd" line the run
// window starts with (the TUI was restarted meanwhile).
func runCommandOnScreen(sc *engine.Screen) string {
	if sc.History > 0 {
		return ""
	}
	for _, l := range sc.Lines {
		l = strings.TrimSpace(ansi.Strip(l))
		if l == "" {
			continue
		}
		if c, ok := strings.CutPrefix(l, "$ "); ok {
			return strings.TrimSpace(c)
		}
		return ""
	}
	return ""
}

// runExitLine is drawn under a finished run's output.
func (m *model) runExitLine() string {
	th := m.th
	code := -1
	if m.screen != nil {
		code = m.screen.ExitStatus
	}
	status := th.FG(theme.Dim, "exited")
	switch {
	case code == 0:
		status = th.FG(theme.Ok, "exit 0")
	case code > 0:
		status = th.FG(theme.Err, "exit "+strconv.Itoa(code))
	}
	return status + th.FG(theme.Dim, " · ") + th.Bold(theme.Secondary, "↵") + th.FG(theme.Dim, " back to the agent  ") + th.Bold(theme.Secondary, "x") + th.FG(theme.Dim, " close")
}

// dropPaneDead removes tmux's "Pane is dead (status N, <date>)" line (and
// the blank rows before it): the run window says how it ended itself.
func dropPaneDead(lines []string) []string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(ansi.Strip(lines[i]))
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "Pane is dead") {
			lines = lines[:i]
			for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
				lines = lines[:len(lines)-1]
			}
		}
		break
	}
	return lines
}

// ---- the command line ----

type runModal struct {
	m    *model
	slug string
	in   *input
}

func newRunModal(m *model, slug, suggest string) *runModal {
	r := []rune(suggest)
	return &runModal{m: m, slug: slug, in: &input{text: r, pos: len(r)}}
}

func (r *runModal) mode() (string, string) { return "RUN", r.m.ic.Agent }
func (r *runModal) hints() [][2]string {
	return [][2]string{{"↵", "run"}, {"esc", "cancel"}, {"^u", "clear"}}
}

func (r *runModal) view() []string {
	th := r.m.th
	lines := []string{"", th.FG(theme.Secondary, "$ ") + r.in.render(th, true), "",
		th.FG(theme.Dim, "runs once in the worktree with the dev server's env; answer prompts in the window")}
	return r.m.modalFrame(r.m.modalWidth(min(r.m.width*70/100, 100)), th.Bold(theme.Primary, "Run "+r.slug), lines, r.hints(), false)
}

func (r *runModal) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		return done(nil)
	case "enter":
		cmd := strings.TrimSpace(r.in.String())
		if cmd == "" {
			return done(nil)
		}
		return done(r.m.startRun(r.slug, cmd))
	}
	if k.Paste { // one line: a pasted newline would run half of it
		k.Runes = []rune(strings.Join(strings.Fields(strings.ReplaceAll(string(k.Runes), "\n", " ")), " "))
	}
	r.in.key(k)
	return nil
}

// ---- "tell the agent?" after a run ----

type runReportModal struct {
	m    *model
	slug string
	text string
}

func (r *runReportModal) mode() (string, string) { return "RUN", r.m.ic.Agent }
func (r *runReportModal) hints() [][2]string {
	return [][2]string{{"y", "send"}, {"n", "don't"}}
}

func (r *runReportModal) view() []string {
	th := r.m.th
	lines := []string{"", "tell the agent?", "", sunk(r.text)}
	return r.m.modalFrame(r.m.modalWidth(min(r.m.width*60/100, 90)), th.Bold(theme.Primary, "Run finished"), lines, r.hints(), false)
}

func (r *runReportModal) key(k tea.KeyMsg) tea.Cmd {
	m := r.m
	switch k.String() {
	case "y", "enter":
		m.typing = true
		return done(m.act("session.send", map[string]any{"slug": r.slug, "text": r.text, "submit": true}, "told the agent"))
	case "n", "esc":
		m.typing = true
		return done(nil)
	}
	return nil
}

// ---- the suggestion ----

var (
	// "! cmd": Claude Code's way of handing the user a command for its shell mode
	bangRe = regexp.MustCompile("(?:^|[\\s`(「:：])! ?([^\\s!`」][^`」]*)")
	tickRe = regexp.MustCompile("`([^`]+)`")
	// what a command line starts with (not a bullet, box line or prose symbol)
	cmdStartRe = regexp.MustCompile(`^[A-Za-z0-9_./~$-]`)
)

// runSuggest picks the command the agent's screen most likely asks the user
// to run: the last "! cmd", else the last inline code or code-block line.
// Loose on purpose; the user edits it before it runs.
func runSuggest(lines []string) string {
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = ansi.Strip(l)
	}
	for i := len(plain) - 1; i >= 0; i-- {
		if ms := bangRe.FindAllStringSubmatch(plain[i], -1); ms != nil {
			if c := cleanCmd(ms[len(ms)-1][1]); c != "" {
				return c
			}
		}
	}
	fence := fencedLines(plain)
	for i := len(lines) - 1; i >= 0; i-- {
		if fence[i] {
			if c := cleanCmd(plain[i]); c != "" && cmdStartRe.MatchString(c) {
				return c
			}
			continue
		}
		if ms := tickRe.FindAllStringSubmatch(plain[i], -1); ms != nil {
			if c := cleanCmd(ms[len(ms)-1][1]); c != "" {
				return c
			}
		}
		if c, ok := strings.CutPrefix(strings.TrimSpace(plain[i]), "$ "); ok && cleanCmd(c) != "" {
			return cleanCmd(c)
		}
		if c := codeSpan(lines[i]); c != "" {
			return c
		}
	}
	return ""
}

// fencedLines marks the lines between ``` fences.
func fencedLines(plain []string) []bool {
	in := false
	out := make([]bool, len(plain))
	for i, l := range plain {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			in = !in
			continue
		}
		out[i] = in
	}
	return out
}

func cleanCmd(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$ ")
	return strings.TrimSpace(s)
}

// codeSpan finds code the way Claude Code draws it, without backticks: a
// coloured run inside otherwise plain text (inline code), or an indented
// line drawn entirely in colour (a code block). It keeps the last one that
// reads like a command with arguments.
func codeSpan(line string) string {
	segs := sgrSegments(line)
	plainText := false
	for _, sg := range segs {
		if sg.fg == "" && strings.TrimSpace(sg.text) != "" {
			plainText = true
		}
	}
	// merge neighbouring coloured segments (syntax highlighting splits a command)
	var runs []string
	cur := ""
	for _, sg := range segs {
		if sg.fg == "" {
			if cur != "" {
				runs = append(runs, cur)
				cur = ""
			}
			continue
		}
		cur += sg.text
	}
	if cur != "" {
		runs = append(runs, cur)
	}
	if !plainText {
		// a whole coloured line: only an indented code-block line counts
		full := ansi.Strip(line)
		if !strings.HasPrefix(full, "  ") || len(runs) == 0 {
			return ""
		}
		runs = []string{full} // spaces between highlighted tokens are uncoloured
	}
	for i := len(runs) - 1; i >= 0; i-- {
		c := cleanCmd(runs[i])
		if strings.Contains(c, " ") && cmdStartRe.MatchString(c) && !strings.Contains(c, "://") {
			return c
		}
	}
	return ""
}

type sgrSeg struct {
	fg   string // "" = default foreground
	text string
}

// sgrSegments splits an ANSI line into runs of one foreground colour.
func sgrSegments(line string) []sgrSeg {
	var out []sgrSeg
	fg := ""
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, sgrSeg{fg: fg, text: b.String()})
			b.Reset()
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c != 0x1b || i+1 >= len(line) {
			b.WriteByte(c)
			continue
		}
		switch line[i+1] {
		case '[':
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j >= len(line) {
				i = len(line)
				continue
			}
			if line[j] == 'm' {
				nfg := applySGR(fg, line[i+2:j])
				if nfg != fg {
					flush()
					fg = nfg
				}
			}
			i = j
		case ']': // OSC (hyperlinks): up to BEL or ST
			j := i + 2
			for j < len(line) && line[j] != 0x07 && !(line[j] == 0x1b && j+1 < len(line) && line[j+1] == '\\') {
				j++
			}
			if j < len(line) && line[j] == 0x1b {
				j++
			}
			i = j
		default:
			i++
		}
	}
	flush()
	return out
}

func applySGR(fg, params string) string {
	if params == "" {
		return ""
	}
	ps := strings.Split(params, ";")
	for k := 0; k < len(ps); k++ {
		n, _ := strconv.Atoi(ps[k])
		switch {
		case n == 0 || n == 39:
			fg = ""
		case n >= 30 && n <= 37, n >= 90 && n <= 97:
			fg = ps[k]
		case n == 38 && k+1 < len(ps):
			if ps[k+1] == "5" && k+2 < len(ps) {
				fg = "5;" + ps[k+2]
				k += 2
			} else if ps[k+1] == "2" && k+4 < len(ps) {
				fg = "2;" + strings.Join(ps[k+2:k+5], ";")
				k += 4
			}
		case n == 48 && k+1 < len(ps): // background: skip its arguments
			if ps[k+1] == "5" {
				k += 2
			} else if ps[k+1] == "2" {
				k += 4
			}
		}
	}
	return fg
}
