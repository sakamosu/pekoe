package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"boba/internal/client"
	"boba/internal/daemon"
	"boba/internal/state"
	"boba/internal/theme"
)

// fakeBobad answers the TUI's calls over a real socket and records them.
type fakeBobad struct {
	mu    sync.Mutex
	calls []fakeCall
	reply func(method string) any
}

type fakeCall struct {
	method string
	params map[string]any
}

func (f *fakeBobad) find(method string) []map[string]any {
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

func startFakeBobad(t *testing.T) (*fakeBobad, *client.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bb") // unix socket paths are short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f := &fakeBobad{reply: func(string) any { return nil }}
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
					b, _ := json.Marshal(map[string]any{"id": req.ID, "result": reply(req.Method)})
					c.Write(append(b, '\n'))
				}
			}()
		}
	}()
	t.Setenv("BOBA_SOCK", sock)
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
	t.Setenv("BOBA_COLOR", color)
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
	return leftWidth(m.width)
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
			}
		}
	}
}

func TestSingleRepoHasNoHeadings(t *testing.T) {
	for _, width := range []int{180, 90} {
		m := repoModel(t, "truecolor", width)
		m.sessions = m.sessions[:1]
		m.sessions = append(m.sessions, sess("api-2", appA, state.Idle, time.Now()))
		out := checkSize(t, m, "single")
		if lineIndex(strings.Split(out, "\n"), " api ─", sessionsWidth(m)) >= 0 || lineIndex(strings.Split(out, "\n"), "api-1", sessionsWidth(m)) < 0 {
			t.Fatalf("one repository: no heading expected:\n%s", out)
		}
	}
}

func TestSelectionSkipsHeadings(t *testing.T) {
	m := repoModel(t, "none", 180)
	if got := m.current().Slug; got != "api-1" {
		t.Fatalf("start: %s", got)
	}
	want := []string{"api-2", "web-1", "web-1"}
	for _, w := range want {
		m.onKey(key("j"))
		if got := m.current().Slug; got != w {
			t.Fatalf("j: %s, want %s", got, w)
		}
		rows, _ := m.sessionRows(50)
		if i := selRow(rows); i < 0 || !strings.Contains(rows[i].Text, w) {
			t.Fatalf("selected row is not %s: %d", w, i)
		}
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
// directory boba was started in.
func TestCommandsTargetCurrentRepo(t *testing.T) {
	f, cl := startFakeBobad(t)
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
		t.Setenv("BOBA_COLOR", "none")
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
