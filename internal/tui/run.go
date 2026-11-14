package tui

// The run window (`!` in Sessions): one command the agent asked the user to
// run, run in the session's worktree in a pty of its own — so a confirmation
// prompt or a password can be answered — then back to the agent, optionally
// telling it how the command ended. Not a resident shell: one command, then
// the window is closed. The daemon keeps the command and its exit (RunState,
// Session.Run); the TUI keeps nothing of it.

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/termstyle"
	"pekoe/internal/theme"
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

// runClosedMsg: run.close answered; ask is set when closed with ↵ (tell
// the agent?), not with x / Esc.
type runClosedMsg struct {
	slug string
	st   daemon.RunState
	ask  bool
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
	if !msg.st.Started { // someone else's run was open: that one is shown
		m.setMsg("a run is already open: "+oneLine(msg.st.Command), false)
	}
	return m.showRun()
}

// showRun puts the run window in the main panel, typing into it. tapi chat
// goes: left open, it would keep [4] (and the typing) while the run window
// waited unseen behind it.
func (m *model) showRun() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	m.chat.open = false
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
	m.focus = pAgent
	return m.closeRun(true)
}

// closeRun closes the run window and shows the agent again. Its dead screen
// must not stay as the agent's: ↵ on it would restart the agent.
func (m *model) closeRun(ask bool) tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	slug := s.Slug
	m.window = engine.WinAgent
	m.screen = nil
	m.typing = false
	return tea.Batch(func() tea.Msg {
		var st daemon.RunState
		if err := m.cl.Call("run.close", map[string]any{"slug": slug}, &st); err != nil {
			return errMsg{err}
		}
		return runClosedMsg{slug: slug, st: st, ask: ask}
	}, m.fetchScreen())
}

func (m *model) onRunClosed(msg runClosedMsg) tea.Cmd {
	if !msg.ask || m.modal != nil {
		return nil
	}
	if s := m.current(); s == nil || s.Slug != msg.slug {
		return nil
	}
	m.modal = &runReportModal{m: m, slug: msg.slug, text: runReport(msg.st)}
	return nil
}

// oneLine shows a (pasted, multi-line) command on one line.
func oneLine(cmd string) string {
	return strings.ReplaceAll(strings.TrimSpace(cmd), "\n", " ⏎ ")
}

// runReport is the line sent to the agent after a run.
func runReport(st daemon.RunState) string {
	what := "頼まれたコマンド"
	if st.Command != "" {
		what = "`" + oneLine(st.Command) + "`"
	}
	switch {
	case st.Signal != "":
		return fmt.Sprintf("%s を実行しましたが、中断されました(%s)", what, st.Signal)
	case st.Running:
		return what + " を実行中に閉じました"
	case st.ExitStatus >= 0:
		return fmt.Sprintf("%s を実行しました(exit %d)", what, st.ExitStatus)
	}
	return what + " を実行しました(終了コード不明)"
}

// runExitLine is drawn under a finished run's output.
func (m *model) runExitLine() string {
	th := m.th
	status := th.FG(theme.Dim, "exited")
	if sc := m.screen; sc != nil {
		switch {
		case sc.Signal != "":
			status = th.FG(theme.Warn, "interrupted ("+sc.Signal+")")
		case sc.ExitStatus == 0:
			status = th.FG(theme.Ok, "exit 0")
		case sc.ExitStatus > 0:
			status = th.FG(theme.Err, fmt.Sprintf("exit %d", sc.ExitStatus))
		}
	}
	return status + th.FG(theme.Dim, " · ") + th.Bold(theme.Secondary, "↵") + th.FG(theme.Dim, " back to the agent  ") + th.Bold(theme.Secondary, "x") + th.FG(theme.Dim, " close")
}

// dropPaneDead removes tmux's "Pane is dead (status N, <date>)" line (and
// the blank rows before it): the run window says how it ended itself.
// (pekoed turns the line off; tmux before 3.3 still writes it.)
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
	sent bool // ↵ pressed: a second ↵ before the modal closes runs nothing
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
	// a pasted multi-line command keeps its newlines; ⏎ marks them here
	field := strings.ReplaceAll(r.in.render(th, true), "\n", "⏎")
	lines := []string{"", th.FG(theme.Secondary, "$ ") + field, "",
		th.FG(theme.Dim, "runs once in the worktree with the dev server's env; answer prompts in the window")}
	return r.m.modalFrame(r.m.modalWidth(min(r.m.width*70/100, 100)), th.Bold(theme.Primary, "Run "+r.slug), lines, r.hints(), false)
}

func (r *runModal) key(k tea.KeyMsg) tea.Cmd {
	if r.sent {
		return nil
	}
	switch k.String() {
	case "esc":
		return done(nil)
	case "enter":
		cmd := strings.TrimSpace(r.in.String())
		if cmd == "" {
			return done(nil)
		}
		r.sent = true
		return done(r.m.startRun(r.slug, cmd))
	}
	if k.Paste {
		// the shell gets the lines as pasted (continuations, quoting and
		// all); only the line ending is normalised and a final one dropped
		t := strings.ReplaceAll(string(k.Runes), "\r\n", "\n")
		k.Runes = []rune(strings.TrimRight(strings.ReplaceAll(t, "\r", "\n"), "\n"))
	}
	r.in.key(k)
	return nil
}

// ---- "tell the agent?" after a run ----

// runReportModal takes y / n only: ↵ answering the command's last prompt
// must not also send the report.
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
	case "y":
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
	// "! cmd" (Claude Code's way of handing the user a command for its shell
	// mode) or "`!cmd`". The space keeps != and !ok out.
	bangRe = regexp.MustCompile("(?:^|[\\s(「:：])! ([^`」]+)|`! ?([^`\\s][^`]*)`")
	tickRe = regexp.MustCompile("`([^`]+)`")
	// what a command line starts with (not a bullet, box line or prose symbol)
	cmdStartRe = regexp.MustCompile(`^[A-Za-z0-9_./~$-]`)
	urlRe      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
)

// runSuggest picks the command the agent's screen most likely asks the user
// to run: the last "! cmd", else the bottom-most inline code with arguments,
// code-block line or "$ " line. Loose on purpose; the user edits it before
// it runs.
func runSuggest(lines []string) string {
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = ansi.Strip(l)
	}
	for i := len(plain) - 1; i >= 0; i-- {
		ms := bangRe.FindAllStringSubmatch(plain[i], -1)
		for k := len(ms) - 1; k >= 0; k-- {
			c := cleanCmd(ms[k][1] + ms[k][2])
			if cmdStartRe.MatchString(c) && !urlRe.MatchString(c) && !strings.HasPrefix(c, "for bash mode") {
				return c
			}
		}
	}
	fence := fencedLines(plain)
	for i := len(lines) - 1; i >= 0; i-- {
		if fence[i] {
			if c := cleanCmd(plain[i]); cmdStartRe.MatchString(c) && !urlRe.MatchString(c) {
				return c
			}
			continue
		}
		ms := tickRe.FindAllStringSubmatch(plain[i], -1)
		for k := len(ms) - 1; k >= 0; k-- {
			if c := cleanCmd(ms[k][1]); commandLike(c) {
				return c
			}
		}
		if c, ok := strings.CutPrefix(strings.TrimSpace(plain[i]), "$ "); ok {
			if c = cleanCmd(c); cmdStartRe.MatchString(c) {
				return c
			}
		}
		if c := codeSpan(lines[i]); c != "" {
			return c
		}
	}
	return ""
}

// commandLike: a command with arguments (one word is more often a file
// name than a command), not a bare URL (a URL argument is fine).
func commandLike(c string) bool {
	return strings.Contains(c, " ") && cmdStartRe.MatchString(c) && !urlRe.MatchString(c)
}

// fencedLines marks the lines between ``` fences. With an odd number of
// fences on screen the opening one has scrolled off the top, so the screen
// starts inside a block.
func fencedLines(plain []string) []bool {
	n := 0
	for _, l := range plain {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			n++
		}
	}
	in := n%2 == 1
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
	segs := termstyle.Line(line)
	plainText := false
	for _, sg := range segs {
		if sg.Fg == "" && strings.TrimSpace(sg.Text) != "" {
			plainText = true
		}
	}
	if !plainText {
		// a whole coloured line: only an indented code-block line counts;
		// its text is whole (the spaces between highlighted tokens are
		// uncoloured)
		full := ansi.Strip(line)
		if len(segs) == 0 || !strings.HasPrefix(full, "  ") {
			return ""
		}
		if c := cleanCmd(full); commandLike(c) {
			return c
		}
		return ""
	}
	// neighbouring coloured runs make one span (syntax highlighting)
	var spans []string
	cur := ""
	for _, sg := range segs {
		if sg.Fg == "" {
			if cur != "" {
				spans = append(spans, cur)
				cur = ""
			}
			continue
		}
		cur += sg.Text
	}
	if cur != "" {
		spans = append(spans, cur)
	}
	for i := len(spans) - 1; i >= 0; i-- {
		if c := cleanCmd(spans[i]); commandLike(c) {
			return c
		}
	}
	return ""
}
