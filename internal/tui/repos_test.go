package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/state"
	"pekoe/internal/testenv"
	"pekoe/internal/theme"
)

// fakePekoed answers the TUI's calls over a real socket and records them.
type fakePekoed struct {
	mu    sync.Mutex
	calls []fakeCall
	reply func(method string) any
}

type fakeCall struct {
	method string
	params map[string]any
}

func (f *fakePekoed) find(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c.params)
		}
	}
	return out
}

func startFakePekoed(t *testing.T) (*fakePekoed, *client.Client) {
	t.Helper()
	// in memory: an agent's sandbox refuses binding a unix socket
	sock := filepath.Join(t.TempDir(), "s")
	l := testenv.Listen(sock)
	t.Cleanup(func() { l.Close() })
	oldDial := client.DialSocket
	client.DialSocket = l.Dial
	t.Cleanup(func() { client.DialSocket = oldDial })
	f := &fakePekoed{reply: func(string) any { return nil }}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := bufio.NewScanner(c)
				sc.Buffer(make([]byte, 1<<20), 1<<20)
				for sc.Scan() {
					var req struct {
						ID     int64          `json:"id"`
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					f.mu.Lock()
					f.calls = append(f.calls, fakeCall{req.Method, req.Params})
					reply := f.reply
					f.mu.Unlock()
					res := map[string]any{"id": req.ID, "result": reply(req.Method)}
					if err, ok := res["result"].(error); ok {
						res = map[string]any{"id": req.ID, "error": err.Error()}
					}
					b, _ := json.Marshal(res)
					c.Write(append(b, '\n'))
				}
			}()
		}
	}()
	t.Setenv("PEKOE_SOCK", sock)
	cl, err := client.Dial()
	if err != nil {
		t.Fatal(err)
	}
	return f, cl
}

// run executes a command tree (batches and sequences), feeding every
// message back into the model, like the tea runtime does.
func runCmd(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(300 * time.Millisecond):
		return // a tick: never answers in time
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runCmd(m, c)
		}
	case nil:
	default:
		if strings.HasPrefix(fmt.Sprintf("%T", msg), "tea.") {
			return // window title, sequences of terminal writes …
		}
		_, c := m.Update(msg)
		runCmd(m, c)
	}
}

var (
	appA = daemon.KnownRepo{ID: "github.com_me_api", Name: "api", Dir: "/src/api", Sessions: 2}
	appB = daemon.KnownRepo{ID: "github.com_me_web", Name: "web", Dir: "/src/web", Sessions: 1}
)

func sess(slug string, r daemon.KnownRepo, st state.State, at time.Time) *state.Session {
	return &state.Session{Slug: slug, Branch: slug, State: st, Repo: r.Name, RepoID: r.ID, RepoDir: r.Dir, CreatedAt: at, StateSince: at}
}

// two repositories, created interleaved: api-1, web-1, api-2
func repoModel(t *testing.T, color string, width int) *model {
	t.Helper()
	t.Setenv("PEKOE_COLOR", color)
	m := newModel(nil, "/src/api")
	m.tapi.splash = false
	m.width, m.height = width, 40
	now := time.Now()
	m.sessions = []*state.Session{
		sess("api-1", appA, state.Running, now.Add(-3*time.Minute)),
		sess("web-1", appB, state.Waiting, now.Add(-2*time.Minute)),
		sess("api-2", appA, state.Idle, now.Add(-1*time.Minute)),
	}
	m.sortSessions()
	home := appA
	home.Cwd = true
	m.repos = []daemon.KnownRepo{home, appB}
	m.reposLoaded = true
	m.sessionsIn, m.homePicked = true, true // past start-up (pickHome)
	return m
}

// lineIndex finds sub in a body row of [1] (left column, or the tabbed
// panel when narrow): not in a title, not in the main panel.
func lineIndex(lines []string, sub string, leftW int) int {
	for i, l := range lines {
		r := []rune(l)
		if len(r) > leftW {
			l = string(r[:leftW])
		}
		if strings.HasPrefix(l, "│") && strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func sessionsWidth(m *model) int {
	if narrow(m.width) {
		return m.width
	}
	return m.leftWidth()
}

func TestSessionsGroupedByRepo(t *testing.T) {
	for _, color := range []string{"truecolor", "ansi", "none"} {
		for _, icons := range []string{"nerd", "unicode", "ascii"} {
			for _, width := range []int{180, 90} { // 90: the tabbed narrow layout
				m := repoModel(t, color, width)
				m.ic = theme.Icons(icons)
				label := fmt.Sprintf("%s/%s/%d", color, icons, width)
				out := checkSize(t, m, label)
				lines := strings.Split(out, "\n")
				lw := sessionsWidth(m)
				hA, hB := lineIndex(lines, " api ─", lw), lineIndex(lines, " web ─", lw)
				a1, a2, w1 := lineIndex(lines, "api-1", lw), lineIndex(lines, "api-2", lw), lineIndex(lines, "web-1", lw)
				if hA < 0 || hB < 0 {
					t.Fatalf("%s: repository headings missing:\n%s", label, out)
				}
				// api's sessions stay together under its heading, web follows
				if !(hA < a1 && a1 < a2 && a2 < hB && hB < w1) {
					t.Fatalf("%s: order heading api %d, api-1 %d, api-2 %d, heading web %d, web-1 %d:\n%s", label, hA, a1, a2, hB, w1, out)
				}
				// the current repository's heading carries the current marker
				if mark := m.ic.Current; !strings.Contains(lines[hA], mark+" api") || strings.Contains(lines[hB], mark+" web") {
					t.Errorf("%s: current marker: %q / %q", label, lines[hA], lines[hB])
				}
				// it is not a state: text, never done's secondary in the same column
				if color != "none" {
					mark, head := fit(m.ic.Current, 1), m.repoHeading(m.sessions[0], 40)
					if !strings.HasPrefix(head, m.th.FG(theme.Text, mark)) || strings.Contains(head, m.th.FG(theme.Secondary, mark)) {
						t.Errorf("%s: current marker should be text, not secondary: %q", label, head)
					}
				}
			}
		}
	}
}

// One repository still gets its heading: it closes the tapi section above
// (orchestrator ━━━, tapi, api ───, its sessions).
func TestSingleRepoHasHeading(t *testing.T) {
	for _, width := range []int{180, 90} {
		m := repoModel(t, "truecolor", width)
		m.sessions = m.sessions[:1]
		m.sessions = append(m.sessions, sess("api-2", appA, state.Idle, time.Now()))
		out := checkSize(t, m, "single")
		lines, lw := strings.Split(out, "\n"), sessionsWidth(m)
		o, tp, h, a1 := lineIndex(lines, "orchestrator ━", lw), lineIndex(lines, "tapi ", lw), lineIndex(lines, " api ─", lw), lineIndex(lines, "api-1", lw)
		if !(o >= 0 && o < tp && tp < h && h < a1) {
			t.Fatalf("%d: orchestrator %d, tapi %d, api %d, api-1 %d:\n%s", width, o, tp, h, a1, out)
		}
	}
}

// The orchestrator heading has the shape of a repository heading and stands
// out a little: its name is never dim and its rule is heavy, in every color
// mode and icon set.
func TestTapiHeadingStandsOut(t *testing.T) {
	for _, color := range []string{"truecolor", "ansi", "none"} {
		for _, icons := range []string{"nerd", "unicode", "ascii"} {
			label := color + "/" + icons
			m := repoModel(t, color, 180)
			m.ic = theme.Icons(icons)
			for _, cw := range []int{60, 20, 6} {
				h := m.tapiHeading(cw)
				if ansi.StringWidth(h) != cw {
					t.Errorf("%s/%d: heading %d wide: %q", label, cw, ansi.StringWidth(h), ansi.Strip(h))
				}
				if !strings.Contains(ansi.Strip(h), "━") {
					t.Errorf("%s/%d: no heavy rule: %q", label, cw, ansi.Strip(h))
				}
			}
			h, r := ansi.Strip(m.tapiHeading(40)), ansi.Strip(m.repoHeading(m.sessions[2], 40)) // web: not current
			if h != "  orchestrator "+strings.Repeat("━", 25) || strings.Contains(r, "━") {
				t.Errorf("%s: %q / %q", label, h, r)
			}
			// the name lines up with the repository names
			if strings.Index(h, "orchestrator") != strings.Index(r, "web") {
				t.Errorf("%s: names out of line: %q / %q", label, h, r)
			}
			if color != "none" && !strings.Contains(m.tapiHeading(40), m.th.FG(theme.Text, "orchestrator")) {
				t.Errorf("%s: the name should be text: %q", label, m.tapiHeading(40))
			}
		}
	}
	// a narrow panel cuts the name, keeping one rule cell
	if h := ansi.Strip(repoModel(t, "none", 180).tapiHeading(10)); h != "  orche… ━" {
		t.Errorf("narrow: %q", h)
	}
}

func TestSelectionSkipsHeadings(t *testing.T) {
	m := repoModel(t, "none", 180)
	if got := m.current().Slug; got != "api-1" {
		t.Fatalf("start: %s", got)
	}
	// wraps from the bottom to the tapi row, then to the first session
	want := []string{"api-2", "web-1", "tapi", "api-1"}
	for _, w := range want {
		m.onKey(key("j"))
		got := "tapi"
		if s := m.current(); s != nil {
			got = s.Slug
		}
		if got != w || (w == "tapi") != m.onTapiRow() {
			t.Fatalf("j: %s, want %s", got, w)
		}
		rows, _ := m.sessionRows(50)
		if i := selRow(rows); i < 0 || !strings.Contains(rows[i].Text, w) {
			t.Fatalf("selected row is not %s: %d", w, i)
		}
	}
	m.onKey(key("k"))
	if !m.onTapiRow() {
		t.Fatalf("k from the first session: the tapi row, got %+v", m.current())
	}
	m.onKey(key("k"))
	if got := m.current().Slug; got != "web-1" {
		t.Fatalf("k wraps from the top to the bottom: %s", got)
	}
	m.onKey(key("k"))
	if got := m.current().Slug; got != "api-2" {
		t.Fatalf("k back over the heading: %s", got)
	}
	// the filter keeps the grouped order; a repository name matches too
	m.onKey(key("/"))
	for _, r := range "web" {
		m.onKey(key(string(r)))
	}
	m.onKey(key("enter"))
	if v := m.visible(); len(v) != 1 || v[0].Slug != "web-1" || m.current().Slug != "web-1" {
		t.Fatalf("filter web: %+v", v)
	}
}

// many sessions: the selected row stays in view below the headings
func TestSelectedSessionStaysInView(t *testing.T) {
	m := repoModel(t, "none", 180)
	m.height = 20
	now := time.Now()
	for i := 0; i < 30; i++ {
		m.sessions = append(m.sessions, sess("web-x"+string(rune('a'+i%26))+string(rune('a'+i/26)), appB, state.Idle, now.Add(time.Duration(i)*time.Second)))
	}
	m.sortSessions()
	m.sel = len(m.visible()) - 1
	last := m.current().Slug
	if out := checkSize(t, m, "scroll"); !strings.Contains(out, last) {
		t.Fatalf("selected %s not shown:\n%s", last, out)
	}
	m.width = 90
	if out := checkSize(t, m, "scroll narrow"); !strings.Contains(out, last) {
		t.Fatalf("narrow: selected %s not shown:\n%s", last, out)
	}
}

func TestCurrentRepoFollowsSelection(t *testing.T) {
	m := repoModel(t, "none", 180)
	if r := m.curRepo(); r == nil || r.Dir != appA.Dir {
		t.Fatalf("api-1 selected: %+v", r)
	}
	m.repoSync()
	m.worktrees = []daemon.WorktreeView{{Path: "/src/api", Branch: "main", Main: true}}
	m.onKey(key("j"))
	m.onKey(key("j")) // web-1
	if r := m.curRepo(); r == nil || r.Dir != appB.Dir {
		t.Fatalf("web-1 selected: %+v", r)
	}
	if cmd := m.repoSync(); cmd == nil || m.worktrees != nil || m.repoKey != appB.Dir {
		t.Fatalf("switching repository reloads worktrees: key %q, wt %v", m.repoKey, m.worktrees)
	}
	if m.repoSync() != nil {
		t.Fatal("same repository: nothing to reload")
	}
	// a stale answer for the repository we left is dropped
	m.updateMain(worktreesMsg{dir: appA.Dir, list: []daemon.WorktreeView{{Path: "/src/api"}}})
	if m.worktrees != nil {
		t.Fatal("stale worktrees accepted")
	}
	// the bar and the scoped titles name it
	out := checkSize(t, m, "bar")
	bar := strings.Split(out, "\n")[m.height-1]
	if !strings.Contains(bar, "web web-1") {
		t.Fatalf("bar: %q", bar)
	}
	m.focus = pSecond
	if out := checkSize(t, m, "worktrees"); !strings.Contains(out, "Worktrees 0 web") {
		t.Fatalf("worktrees title:\n%s", out)
	}

	// w: a repository without sessions becomes current on its own …
	empty := daemon.KnownRepo{ID: "github.com_me_docs", Name: "docs", Dir: "/src/docs"}
	m.repos = append(m.repos, empty)
	m.chooseRepo(empty)
	if r := m.curRepo(); r == nil || r.Dir != empty.Dir || m.current().Slug != "web-1" {
		t.Fatalf("chosen docs: %+v", r)
	}
	if bar := strings.Split(checkSize(t, m, "chosen"), "\n")[m.height-1]; !strings.Contains(bar, "docs") || strings.Contains(bar, "web web-1") {
		t.Fatalf("bar for a chosen repository: %q", bar)
	}
	// … until the selection moves to a session
	m.focus = pSessions
	m.onKey(key("k"))
	if r := m.curRepo(); r == nil || r.Dir != appA.Dir {
		t.Fatalf("moving drops the choice: %+v", r)
	}
	// w on a repository with sessions selects its first one
	m.chooseRepo(appB)
	if m.chosen != nil || m.current().Slug != "web-1" {
		t.Fatalf("w web: chosen %v, current %s", m.chosen, m.current().Slug)
	}
}

func TestRepoPickerModal(t *testing.T) {
	for _, color := range []string{"truecolor", "ansi", "none"} {
		m := repoModel(t, color, 180)
		m.repos = append(m.repos, daemon.KnownRepo{ID: "github.com_me_ghost", Name: "ghost", Config: true})
		m.onKey(key("w"))
		p, ok := m.modal.(*repoPicker)
		if !ok {
			t.Fatalf("w: modal %T", m.modal)
		}
		out := checkSize(t, m, "picker "+color)
		for _, want := range []string{"Repository 3", "api", "2 sessions", "web", "path unknown"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s: picker lacks %q:\n%s", color, want, out)
			}
		}
		if p.sel != 0 {
			t.Fatalf("starts on the current repository: %d", p.sel)
		}
		m.onKey(key("j")) // ghost: no path, cannot be chosen
		m.onKey(key("enter"))
		if m.modal == nil {
			t.Fatal("ghost has no path: the picker stays")
		}
		m.onKey(key("j"))
		m.onKey(key("enter"))
		if m.modal != nil {
			runCmd(m, func() tea.Msg { return modalDoneMsg{} })
		}
		if r := m.curRepo(); r == nil || r.Name != "web" {
			t.Fatalf("%s: picked web: %+v", color, r)
		}
	}
}

// n, Issues, Worktrees and picks go to the current repository, not the
// directory pekoe was started in.
func TestCommandsTargetCurrentRepo(t *testing.T) {
	f, cl := startFakePekoed(t)
	m := repoModel(t, "none", 180)
	listed := append([]*state.Session{sess("web-2", appB, state.Creating, time.Now())}, m.sessions...)
	f.reply = func(method string) any {
		switch method {
		case "session.list":
			return listed
		case "session.new":
			return map[string]any{"slug": "web-2"}
		case "issues.list":
			return map[string]any{"repo_id": appB.ID, "filter": "mine", "issues": []any{}, "fetched_at": time.Now()}
		}
		return nil
	}
	m.cl = cl
	m.onKey(key("j"))
	m.onKey(key("j")) // web-1
	runCmd(m, m.repoSync())
	if wl := f.find("worktree.list"); len(wl) == 0 || wl[len(wl)-1]["repo"] != appB.Dir {
		t.Fatalf("worktree.list: %v", wl)
	}

	m.onKey(key("n"))
	nm, ok := m.modal.(*newModal)
	if !ok || nm.repo == nil || nm.repo.Dir != appB.Dir {
		t.Fatalf("n: %T %+v", m.modal, nm)
	}
	if out := checkSize(t, m, "new"); !strings.Contains(out, "New session · web") {
		t.Fatalf("new modal names the repository:\n%s", out)
	}
	for _, r := range "feat/y" {
		m.onKey(key(string(r)))
	}
	runCmd(m, m.onKey(key("enter")))
	if sn := f.find("session.new"); len(sn) != 1 || sn[0]["repo"] != appB.Dir || sn[0]["branch"] != "feat/y" {
		t.Fatalf("session.new: %v", sn)
	}

	m.modal = nil
	m.focus = pSecond
	m.sub2 = 0
	runCmd(m, m.subtab(true)) // browser
	runCmd(m, m.subtab(true)) // issues
	if il := f.find("issues.list"); len(il) == 0 || il[len(il)-1]["repo"] != appB.Dir {
		t.Fatalf("issues.list: %v", il)
	}

	// a pick sent to a new session
	m.focus = pThird
	m.sub3 = 1
	m.picks = []*state.Pick{{ID: 7}}
	m.psel = 0
	m.onKey(key("n"))
	nm = m.modal.(*newModal)
	for _, r := range "feat/z" {
		m.onKey(key(string(r)))
	}
	runCmd(m, m.onKey(key("enter")))
	if ps := f.find("pick.send"); len(ps) != 1 || ps[0]["repo"] != appB.Dir || ps[0]["new_branch"] != "feat/z" {
		t.Fatalf("pick.send: %v", ps)
	}
}

// Started outside every repository: nothing targets the start directory;
// w offers the known repositories, or one line says there are none.
func TestOutsideRepo(t *testing.T) {
	m := repoModel(t, "none", 180)
	m.cwd = "/Users/me"
	m.sessions = nil
	m.sel = 0
	m.repos = []daemon.KnownRepo{appA, appB} // known, none is cwd
	m.homePicked = false
	m.pickHome()
	if m.curRepo() != nil {
		t.Fatalf("no current repository outside: %+v", m.curRepo())
	}
	out := checkSize(t, m, "outside")
	if !strings.Contains(out, "not in a git repository · w pick one") {
		t.Fatalf("hint:\n%s", out)
	}
	m.focus = pSecond
	m.sub2 = subIssues
	if out := checkSize(t, m, "outside issues"); !strings.Contains(out, "no repository · w pick one") {
		t.Fatalf("issues hint:\n%s", out)
	}
	if m.loadIssues(false) != nil {
		t.Fatal("no issues.list without a repository")
	}
	// n asks for the repository first, then opens the new-session modal for it
	m.focus = pSessions
	m.onKey(key("n"))
	if _, ok := m.modal.(*repoPicker); !ok {
		t.Fatalf("n outside: %T", m.modal)
	}
	m.onKey(key("j"))
	m.onKey(key("enter"))
	nm, ok := m.modal.(*newModal)
	if !ok || nm.repo == nil || nm.repo.Dir != appB.Dir {
		t.Fatalf("after picking web: %T %+v", m.modal, nm)
	}

	// nothing known at all: one line, no modal
	m = repoModel(t, "none", 180)
	m.sessions, m.repos = nil, nil
	m.onKey(key("n"))
	if m.modal != nil || m.msg != noRepoHint {
		t.Fatalf("no repositories: modal %T msg %q", m.modal, m.msg)
	}
	if out := checkSize(t, m, "none known"); !strings.Contains(out, "not in a git repository") {
		t.Fatalf("hint:\n%s", out)
	}
}

// Started in a repository without sessions while others have some: that
// repository is current, so n does not go to the first listed session's.
func TestStartRepoWithoutSessions(t *testing.T) {
	m := repoModel(t, "none", 180)
	docs := daemon.KnownRepo{ID: "github.com_me_docs", Name: "docs", Dir: "/src/docs", Cwd: true}
	m.repos = []daemon.KnownRepo{appA, appB, docs}
	m.homePicked = false
	m.pickHome()
	if r := m.curRepo(); r == nil || r.Dir != docs.Dir {
		t.Fatalf("start repository: %+v", r)
	}
	// started in web: its first session is selected
	m = repoModel(t, "none", 180)
	web := appB
	web.Cwd = true
	m.repos = []daemon.KnownRepo{appA, web}
	m.homePicked = false
	m.pickHome()
	if m.current().Slug != "web-1" || m.chosen != nil {
		t.Fatalf("start in web: %s", m.current().Slug)
	}
}

// The real start-up: session.list and repo.list answer in either order.
func TestStartUpFollowsStartRepo(t *testing.T) {
	for _, reposFirst := range []bool{false, true} {
		t.Setenv("PEKOE_COLOR", "none")
		m := newModel(nil, "/src/web/sub")
		m.tapi.splash = false
		m.width, m.height = 180, 40
		now := time.Now()
		ss := sessionsMsg{sess("api-1", appA, state.Running, now.Add(-2*time.Minute)), sess("web-1", appB, state.Idle, now)}
		web := appB
		web.Cwd = true
		rs := reposMsg{appA, web}
		if reposFirst {
			m.Update(rs)
			m.Update(ss)
		} else {
			m.Update(ss)
			m.Update(rs)
		}
		if s := m.current(); s == nil || s.Slug != "web-1" || m.repoKey != appB.Dir {
			t.Fatalf("reposFirst=%v: current %v, repoKey %q", reposFirst, s, m.repoKey)
		}
		// later reloads keep the user's selection
		m.onKey(key("k"))
		m.Update(ss)
		if m.current().Slug != "api-1" {
			t.Fatalf("reload moved the selection: %s", m.current().Slug)
		}
	}
}

func TestRepoLabels(t *testing.T) {
	kr := func(id, name, dir string) daemon.KnownRepo { return daemon.KnownRepo{ID: id, Name: name, Dir: dir} }
	cases := []struct {
		name string
		in   []daemon.KnownRepo
		want map[string]string
	}{
		{"no overlap: short names as before",
			[]daemon.KnownRepo{appA, appB, kr("path_src_tool", "tool", "/src/tool")},
			map[string]string{appA.ID: "api", appB.ID: "web", "path_src_tool": "tool"}},
		{"the owner tells them apart",
			[]daemon.KnownRepo{kr("github.com_a_api", "api", ""), kr("github.com_b_api", "api", ""), appB},
			map[string]string{"github.com_a_api": "a/api", "github.com_b_api": "b/api", appB.ID: "web"}},
		{"same owner on two hosts: up to the host",
			[]daemon.KnownRepo{kr("github.com_a_api", "api", ""), kr("gitlab.com_a_api", "api", ""), kr("github.com_b_api", "api", "")},
			map[string]string{"github.com_a_api": "github.com/a/api", "gitlab.com_a_api": "gitlab.com/a/api", "github.com_b_api": "github.com/b/api"}},
		{"a path id adds its parent directory",
			[]daemon.KnownRepo{kr("github.com_sakamosu_pekoe", "pekoe", "/Users/me/repos/pekoe"), kr("path_Users_me_my_repos_pekoe", "pekoe", "/Users/me/my_repos/pekoe")},
			map[string]string{"github.com_sakamosu_pekoe": "sakamosu/pekoe", "path_Users_me_my_repos_pekoe": "my_repos/pekoe"}},
		{"two paths: parents until they differ",
			[]daemon.KnownRepo{kr("path_x_src_app", "app", "/x/src/app"), kr("path_y_src_app", "app", "/y/src/app")},
			map[string]string{"path_x_src_app": "x/src/app", "path_y_src_app": "y/src/app"}},
	}
	for _, c := range cases {
		got := repoLabels(c.in)
		for id, want := range c.want {
			if got[id] != want {
				t.Errorf("%s: %s = %q, want %q", c.name, id, got[id], want)
			}
		}
	}
}

// Two repositories named api: the headings, the w modal, the bar, the [2]
// title and the window title all show the same longer name, and every line
// keeps the terminal width in every mode.
func TestSameShortNameIsDisambiguated(t *testing.T) {
	a := daemon.KnownRepo{ID: "github.com_a_api", Name: "api", Dir: "/src/a/api", Sessions: 1}
	b := daemon.KnownRepo{ID: "github.com_b_api", Name: "api", Dir: "/src/b/api", Sessions: 1}
	for _, color := range []string{"truecolor", "ansi", "none"} {
		for _, icons := range []string{"nerd", "unicode", "ascii"} {
			for _, width := range []int{180, 90} {
				label := fmt.Sprintf("%s/%s/%d", color, icons, width)
				m := repoModel(t, color, width)
				m.ic = theme.Icons(icons)
				now := time.Now()
				m.sessions = []*state.Session{sess("x-1", a, state.Running, now.Add(-2*time.Minute)), sess("y-1", b, state.Idle, now.Add(-time.Minute))}
				m.sortSessions()
				m.repos = []daemon.KnownRepo{a, b}
				m.sel = 0
				out := checkSize(t, m, label)
				lines := strings.Split(out, "\n")
				lw := sessionsWidth(m)
				if lineIndex(lines, " a/api ─", lw) < 0 || lineIndex(lines, " b/api ─", lw) < 0 {
					t.Fatalf("%s: headings:\n%s", label, out)
				}
				if bar := lines[m.height-1]; !strings.Contains(bar, "a/api x-1") {
					t.Fatalf("%s: bar %q", label, bar)
				}
				if m.repoScope() != "a/api" {
					t.Fatalf("%s: scope %q", label, m.repoScope())
				}
				m.repoSynced = false
				m.repoSync()
				if m.repoTitle != "a/api" {
					t.Fatalf("%s: window title %q", label, m.repoTitle)
				}
				m.onKey(key("w"))
				if _, ok := m.modal.(*repoPicker); !ok {
					t.Fatalf("%s: w: %T", label, m.modal)
				}
				out = checkSize(t, m, label+" picker")
				if !strings.Contains(out, "a/api") || !strings.Contains(out, "b/api") {
					t.Fatalf("%s: picker:\n%s", label, out)
				}
				m.modal = nil
			}
		}
	}
	// a repository with no twin keeps its short name, also in the title
	m := repoModel(t, "none", 180)
	m.repoSynced = false
	m.repoSync()
	if m.repoTitle != "api" || m.repoScope() != "api" {
		t.Fatalf("no twin: %q %q", m.repoTitle, m.repoScope())
	}
	// a twin appearing later (repo.list) retitles the window without reloading
	m.repoSync()
	m.repos = append(m.repos, daemon.KnownRepo{ID: "github.com_other_api", Name: "api", Dir: "/other/api"})
	if cmd := m.repoSync(); cmd == nil || m.repoTitle != "me/api" || m.repoKey != appA.Dir {
		t.Fatalf("twin later: %q", m.repoTitle)
	}
}

// A heading name too long for the column gives way with … and the line
// keeps its width.
func TestLongRepoHeadingFits(t *testing.T) {
	name := strings.Repeat("very-long-repository-name-", 8)
	long := daemon.KnownRepo{ID: "github.com_me_" + name, Name: name, Dir: "/src/" + name}
	for _, width := range []int{180, 90} {
		m := repoModel(t, "truecolor", width)
		m.sessions = append(m.sessions, sess("z-1", long, state.Idle, time.Now()))
		m.repos = append(m.repos, long)
		m.sortSessions()
		cw := sessionsWidth(m) - 4
		if h := m.repoHeading(m.sessions[len(m.sessions)-1], cw); ansi.StringWidth(h) != cw || !strings.Contains(ansi.Strip(h), "…") {
			t.Fatalf("%d: heading %q (%d)", width, ansi.Strip(h), ansi.StringWidth(h))
		}
		checkSize(t, m, fmt.Sprintf("long %d", width))
	}
}
