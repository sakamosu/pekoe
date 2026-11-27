package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// fakeWatch answers watch mode's GitHub reads from memory.
type fakeWatch struct {
	mu      sync.Mutex
	issues  map[string][]ghIssue // label -> open issues
	events  map[int][]labelEvent // issue -> events
	perms   map[string]string    // login -> permission
	permErr error                // every permission call fails
	listErr error                // every list fails
	calls   map[string]int       // "list" "events" "perm"
}

func newFakeWatch() *fakeWatch {
	return &fakeWatch{issues: map[string][]ghIssue{}, events: map[int][]labelEvent{}, perms: map[string]string{}, calls: map[string]int{}}
}

func (f *fakeWatch) openIssues(_ context.Context, repo, label string) ([]ghIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["list"]++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]ghIssue(nil), f.issues[label]...), nil
}

func (f *fakeWatch) labelEvents(_ context.Context, repo string, n int) ([]labelEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["events"]++
	return append([]labelEvent(nil), f.events[n]...), nil
}

func (f *fakeWatch) permission(_ context.Context, repo, login string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["perm"]++
	if f.permErr != nil {
		return "", f.permErr
	}
	if p, ok := f.perms[login]; ok {
		return p, nil
	}
	return "none", nil
}

func (f *fakeWatch) count(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[k]
}

var watchT0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// label puts label on issue n (a new labeled event by actor, the issue's
// updatedAt moved on) and lists it as open with that label.
func (f *fakeWatch) label(n int, title, label, actor string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := int64(n*1000 + len(f.events[n]) + 1)
	at := watchT0.Add(time.Duration(id) * time.Second)
	f.events[n] = append(f.events[n], labelEvent{ID: id, Event: "labeled", Label: label, Actor: actor, At: at})
	is := ghIssue{Number: n, Title: title, UpdatedAt: at}
	is.Labels = append(is.Labels, struct {
		Name string `json:"name"`
	}{label})
	list := f.issues[label][:0:0]
	for _, x := range f.issues[label] {
		if x.Number != n {
			list = append(list, x)
		}
	}
	f.issues[label] = append(list, is)
}

// unlabel takes label off issue n.
func (f *fakeWatch) unlabel(n int, label, actor string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := int64(n*1000 + len(f.events[n]) + 1)
	f.events[n] = append(f.events[n], labelEvent{ID: id, Event: "unlabeled", Label: label, Actor: actor, At: watchT0.Add(time.Duration(id) * time.Second)})
	var list []ghIssue
	for _, x := range f.issues[label] {
		if x.Number != n {
			list = append(list, x)
		}
	}
	f.issues[label] = list
}

// touch moves issue n's updatedAt on (a comment) without a label event.
func (f *fakeWatch) touch(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for l, list := range f.issues {
		for i := range list {
			if list[i].Number == n {
				f.issues[l][i].UpdatedAt = f.issues[l][i].UpdatedAt.Add(time.Hour)
			}
		}
	}
}

// watchSetup is a daemon whose repository is on GitHub (me/app), with the
// fake gh for the issue text session.new reads, watched as conf says.
func watchSetup(t *testing.T, conf string) (*Daemon, *fakeEngine, *fakeWatch, watchedRepo) {
	t.Helper()
	d, fe, repo := setup(t)
	withOrigin(t, repo)
	onGitHub(t, d, repo)
	fakeGH(t, "OPEN")
	ri, err := resolveRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	d.resolved(ri)
	if err := paths.Ensure(paths.ReposConfigDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.RepoConfig(ri.ID), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	fw := newFakeWatch()
	d.watchGH = fw
	rs := d.watchedRepos()
	if strings.Contains(conf, "enable = true") && (len(rs) != 1 || rs[0].ID != ri.ID) {
		t.Fatalf("watched: %+v", rs)
	}
	return d, fe, fw, watchedRepo{ID: ri.ID, Dir: ri.Dir}
}

func watchedOf(t *testing.T, d *Daemon) watchedRepo {
	t.Helper()
	rs := d.watchedRepos()
	if len(rs) != 1 {
		t.Fatalf("watched: %+v", rs)
	}
	return rs[0]
}

const watchOn = "[watch]\nenable = true\n"

// Off (the default): nothing is asked of GitHub, nothing starts.
func TestWatchOffDoesNothing(t *testing.T) {
	for _, conf := range []string{"", "[watch]\nlabels = [\"agent-ready\"]\n", "[watch]\nenable = false\n"} {
		d, _, fw, _ := watchSetup(t, conf)
		fw.label(12, "Login fails", "agent-ready", "alice")
		fw.perms["alice"] = "write"
		d.watchTick()
		if fw.count("list") != 0 || len(d.watchedRepos()) != 0 {
			t.Fatalf("%q: asked GitHub (%v)", conf, fw.calls)
		}
		for _, r := range d.knownRepos() {
			if r.Watch {
				t.Fatalf("%q: repo.list says watched: %+v", conf, r)
			}
		}
	}
}

// A label by someone with write starts one session, as pekoe new --from
// gh#N: the issue's text in its untrusted fence, the watch note after it,
// parent pekoed; the next rounds start no second one.
func TestWatchStartsSessionOnce(t *testing.T) {
	d, fe, fw, _ := watchSetup(t, watchOn)
	fw.label(12, "Login fails on Safari", "agent-ready", "alice")
	fw.perms["alice"] = "write"
	r := watchedOf(t, d)
	if r.Watch.Interval() != 5*time.Minute || r.Watch.PerPoll() != 1 || r.Watch.Running() != 2 {
		t.Fatalf("defaults: %+v", r.Watch)
	}
	res := d.watchRound(r)
	if len(res.Started) != 1 {
		t.Fatalf("round: %+v", res)
	}
	slug := res.Started[0]
	s := waitState(t, d, slug, state.Running)
	if s.Issue != 12 || s.Parent != callerPekoed || !strings.Contains(s.Branch, "issue-12") {
		t.Fatalf("session: %+v", s)
	}
	fe.mu.Lock()
	cmd := fe.spawn[slug+"~"+engine.WinAgent].Command
	fe.mu.Unlock()
	if !strings.Contains(cmd, "GitHub issue #12 に対応してください") || !strings.Contains(cmd, "信頼できないデータ") ||
		!strings.Contains(cmd, "issue 監視モード") || !strings.Contains(cmd, "agent-ready") {
		t.Fatalf("prompt: %s", cmd)
	}
	// the repository shows as watched
	found := false
	for _, k := range d.knownRepos() {
		if k.ID == r.ID {
			found = k.Watch && len(k.WatchLabels) == 1 && k.WatchLabels[0] == "agent-ready"
		}
	}
	if !found {
		t.Fatal("repo.list does not say watched")
	}

	// nothing new: no GitHub call beyond the list, no second session
	ev := fw.count("events")
	if res := d.watchRound(r); len(res.Started) != 0 || fw.count("events") != ev {
		t.Fatalf("second round: %+v (events %d → %d)", res, ev, fw.count("events"))
	}
	// a comment moved updatedAt on: the events are read, the same one
	fw.touch(12)
	if res := d.watchRound(r); len(res.Started) != 0 || res.Skipped[12] != "done" {
		t.Fatalf("after a comment: %+v", res)
	}
	// rm: not made again for the same labeled event
	if _, err := d.hRm(rmP{Slug: slug, Force: true}); err != nil {
		t.Fatal(err)
	}
	fw.touch(12)
	if res := d.watchRound(r); len(res.Started) != 0 {
		t.Fatalf("after rm: %+v", res)
	}
	// the label taken off and put on again: a new event, a new session
	fw.unlabel(12, "agent-ready", "alice")
	fw.label(12, "Login fails on Safari", "agent-ready", "alice")
	if res := d.watchRound(r); len(res.Started) != 1 {
		t.Fatalf("relabeled: %+v", res)
	}
}

// Only a labeler with write or admin starts a session; one that cannot be
// checked (a bot, GitHub not answering) never does.
func TestWatchChecksLabeler(t *testing.T) {
	d, _, fw, _ := watchSetup(t, watchOn)
	r := watchedOf(t, d)
	fw.label(3, "Read only", "agent-ready", "rita")
	fw.perms["rita"] = "read"
	fw.label(4, "From a bot", "agent-ready", "github-actions[bot]")
	fw.label(5, "Outsider", "agent-ready", "olaf") // none
	res := d.watchRound(r)
	if len(res.Started) != 0 || res.Skipped[3] != "labeler" || res.Skipped[4] != "labeler" || res.Skipped[5] != "labeler" {
		t.Fatalf("round: %+v", res)
	}
	if rec := d.watchRecordOf(r.ID, 3); rec == nil || rec.Outcome != "labeler" || !strings.Contains(rec.Note, `"read"`) {
		t.Fatalf("record: %+v", rec)
	}
	// later given write: the same event does not fire (a new label would)
	fw.perms["rita"] = "write"
	fw.touch(3)
	if res := d.watchRound(r); len(res.Started) != 0 {
		t.Fatalf("same event: %+v", res)
	}

	// GitHub does not answer the permission: not started, asked again next time
	fw.label(6, "Admin", "agent-ready", "ada")
	fw.perms["ada"] = "admin"
	fw.permErr = errors.New("HTTP 502")
	if res := d.watchRound(r); len(res.Started) != 0 || res.Skipped[6] != "labeler unknown" || d.watchRecordOf(r.ID, 6) != nil {
		t.Fatalf("unanswered: %+v", res)
	}
	fw.permErr = nil
	if res := d.watchRound(r); len(res.Started) != 1 {
		t.Fatalf("answered: %+v", res)
	}
}

// max_per_poll a round; none while max_running of watch mode's sessions
// work; a person's session for the issue is left alone.
func TestWatchCaps(t *testing.T) {
	d, _, fw, _ := watchSetup(t, watchOn+"max_per_poll = 2\nmax_running = 3\nlabels = [\"agent-ready\", \"ai\"]\n")
	r := watchedOf(t, d)
	fw.perms["alice"] = "write"
	for _, n := range []int{21, 22, 23, 24} {
		fw.label(n, "Task", "agent-ready", "alice")
	}
	fw.label(25, "Other label", "ai", "alice")
	fw.label(20, "Already on it", "ai", "alice")
	// a person's session for #20, from its branch
	call[state.Session](t, d, "session.new", map[string]any{"repo": r.Dir, "branch": "fix/20-on-it"})

	res := d.watchRound(r)
	if len(res.Started) != 2 || !strings.HasPrefix(res.Skipped[20], "session ") {
		t.Fatalf("first: %+v", res)
	}
	for _, s := range res.Started {
		waitState(t, d, s, state.Running)
	}
	res = d.watchRound(r)
	if len(res.Started) != 1 {
		t.Fatalf("second (max_running 3): %+v", res)
	}
	if n := d.watchRunning(r.ID); n != 3 {
		t.Fatalf("running %d", n)
	}
	if res := d.watchRound(r); len(res.Started) != 0 || res.Skipped[24] != "cap" && res.Skipped[25] != "cap" {
		t.Fatalf("at the cap: %+v", res)
	}
	// one stops working: the next starts
	d.mu.Lock()
	var one string
	for _, s := range d.sessions {
		if s.Parent == callerPekoed {
			one = s.Slug
			break
		}
	}
	d.mu.Unlock()
	d.update(one, func(x *state.Session) { x.State = state.Idle })
	if res := d.watchRound(r); len(res.Started) != 1 {
		t.Fatalf("after one stopped: %+v", res)
	}
}

// A round is due every [watch] every; a failing list starts nothing.
func TestWatchTickInterval(t *testing.T) {
	d, _, fw, _ := watchSetup(t, watchOn+"every = \"10m\"\n")
	fw.listErr = errors.New("gh: rate limited")
	d.watchTick()
	d.watchTick()
	if fw.count("list") != 1 {
		t.Fatalf("lists %d, want 1 (every 10m)", fw.count("list"))
	}
	d.mu.Lock()
	for k := range d.watchPolled {
		d.watchPolled[k] = time.Now().Add(-11 * time.Minute)
	}
	d.mu.Unlock()
	d.watchTick()
	if fw.count("list") != 2 {
		t.Fatalf("lists %d, want 2", fw.count("list"))
	}
}

func TestLastLabeled(t *testing.T) {
	at := func(s int) time.Time { return watchT0.Add(time.Duration(s) * time.Second) }
	evs := []labelEvent{
		{ID: 1, Event: "labeled", Label: "agent-ready", Actor: "a", At: at(1)},
		{ID: 2, Event: "labeled", Label: "bug", Actor: "b", At: at(2)},
		{ID: 3, Event: "unlabeled", Label: "agent-ready", Actor: "a", At: at(3)},
		{ID: 4, Event: "labeled", Label: "ai", Actor: "c", At: at(4)},
	}
	if e, ok := lastLabeled(evs, []string{"agent-ready"}); ok {
		t.Fatalf("unlabeled: %+v", e)
	}
	if e, ok := lastLabeled(evs, []string{"agent-ready", "ai"}); !ok || e.ID != 4 || e.Actor != "c" {
		t.Fatalf("ai: %+v", e)
	}
}

func TestParseLabelEvents(t *testing.T) {
	out := `{"id":7,"event":"labeled","label":"agent-ready","actor":"alice","created_at":"2026-11-20T10:00:05Z"}
{"id":5,"event":"labeled","label":"bug","actor":"bob","created_at":"2026-11-20T10:00:01Z"}
`
	evs, err := parseLabelEvents([]byte(out))
	if err != nil || len(evs) != 2 || evs[0].ID != 5 || evs[1].Actor != "alice" {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs, err := parseLabelEvents(nil); err != nil || len(evs) != 0 {
		t.Fatalf("empty: %+v %v", evs, err)
	}
	if _, err := parseLabelEvents([]byte("{")); err == nil {
		t.Fatal("broken output parsed")
	}
}

func TestWatchBranch(t *testing.T) {
	for _, c := range []struct {
		n      int
		title  string
		labels []string
		want   string
	}{
		{12, "Login fails on Safari", []string{"bug"}, "fix/issue-12-login-fails-on-safari"},
		{7, "CSV export", nil, "feat/issue-7-csv-export"},
		{9, "ログインで落ちる", nil, "feat/issue-9"},
		{10, "$(rm -rf ~); `x`", nil, "feat/issue-10-rm-rf-x"},
	} {
		if got := watchBranch(c.n, c.title, c.labels); got != c.want || BranchIssue(got) != c.n {
			t.Errorf("%q: %q (issue %d)", c.title, got, BranchIssue(got))
		}
	}
}

func TestWatchLabelerLogin(t *testing.T) {
	for _, l := range []string{"alice", "a-b", "A1"} {
		if !loginRe.MatchString(l) {
			t.Errorf("%q refused", l)
		}
	}
	for _, l := range []string{"", "-a", "a--b", "x[bot]", "../a", "a/b", strings.Repeat("a", 40)} {
		if loginRe.MatchString(l) {
			t.Errorf("%q taken", l)
		}
	}
}

// ghWatch's gh calls: issue list by label, the events through --jq, the
// permission; with --hostname off github.com.
func TestGHWatchCalls(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	log := dir + "/gh.log"
	script := `#!/bin/sh
echo "$@" >> ` + log + `
case "$*" in
  *"issue list"*) echo '[{"number":12,"title":"T","labels":[{"name":"agent-ready"}],"updatedAt":"2026-11-20T10:00:00Z","url":"u"}]' ;;
  *events*) echo '{"id":1,"event":"labeled","label":"agent-ready","actor":"alice","created_at":"2026-11-20T10:00:00Z"}' ;;
  *permission*) echo write ;;
esac
`
	os.WriteFile(dir+"/gh", []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	g := ghWatch{}
	is, err := g.openIssues(ctx, "github.com/me/app", "agent-ready")
	if err != nil || len(is) != 1 || is[0].Number != 12 || is[0].UpdatedAt.IsZero() {
		t.Fatalf("list: %+v %v", is, err)
	}
	evs, err := g.labelEvents(ctx, "ghe.example.com/me/app", 12)
	if err != nil || len(evs) != 1 || evs[0].Actor != "alice" {
		t.Fatalf("events: %+v %v", evs, err)
	}
	p, err := g.permission(ctx, "github.com/me/app", "alice")
	if err != nil || p != "write" {
		t.Fatalf("permission: %q %v", p, err)
	}
	b, _ := os.ReadFile(log)
	for _, want := range []string{
		"issue list --repo github.com/me/app --state open --label agent-ready --limit 100 --json number,title,labels,updatedAt,url",
		"api --hostname ghe.example.com repos/me/app/issues/12/events?per_page=100 --paginate --jq " + labelEventsJQ,
		"api repos/me/app/collaborators/alice/permission --jq .permission",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("no %q in\n%s", want, b)
		}
	}
}
