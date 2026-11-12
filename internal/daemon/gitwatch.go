package daemon

// Watching the repository's git directory around the commands pekoed runs
// in its hooks sandbox ([hooks] setup / test / dev / teardown, land's
// tests, the gatekeeper). The sandbox lets them write the repository's
// .git/hooks and .git/config (husky and pre-commit install themselves
// there from `npm install` or a setup step), and what is written there
// runs later in the git a person uses in the main checkout, outside any
// sandbox. So pekoed fingerprints them (internal/gitwatch) before and
// after each run, and when they changed puts an item for a person in the
// inbox (from pekoed): what changed, in which session, during which hook.
// It never undoes or removes anything.
//
// Runs that overlap in one repository see the same change: the first to
// finish reports it, and the others stay quiet when they find the state
// that was reported after they started. A dev server keeps running: its
// window is looked at every 10 seconds (tickLoop) and once more when it
// is stopped.

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"pekoe/internal/engine"
	"pekoe/internal/gitwatch"
	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

func init() {
	gitwatch.ReadConfig = gitwatch.GitReader(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		return gitx.RunRaw(ctx, dir, nil, nil, args...)
	})
}

type gitWatches struct {
	mu       sync.Mutex
	seq      int64
	runs     map[int64]*gitRun
	dev      map[string]*gitRun        // slug → its dev server's watch
	reported map[string]gitWatchReport // git directory → the last state reported
	ticking  atomic.Bool               // checkDevWatches is running
}

type gitWatchReport struct {
	digest string
	at     time.Time
}

type gitRun struct {
	id     int64
	dir    string // the repository's git directory
	slug   string
	hook   string
	before gitwatch.Snapshot
	start  time.Time
}

// gitDirOf is the session's repository's own git directory ("" unknown).
func gitDirOf(s *state.Session) string {
	if s.RepoDir == "" {
		return ""
	}
	if cd, err := gitx.CommonDir(s.RepoDir); err == nil && cd != "" {
		return cd
	}
	if d := filepath.Join(s.RepoDir, ".git"); gitwatch.IsGitDir(d) {
		return d
	}
	return ""
}

// startGitWatch fingerprints s's repository before hook runs (nil: no
// git directory to watch).
func (d *Daemon) startGitWatch(s *state.Session, hook string) *gitRun {
	dir := gitDirOf(s)
	if dir == "" {
		return nil
	}
	r := &gitRun{dir: dir, slug: s.Slug, hook: hook, start: time.Now(), before: gitwatch.Take(dir)}
	w := &d.gitw
	w.mu.Lock()
	w.seq++
	r.id = w.seq
	if w.runs == nil {
		w.runs = map[int64]*gitRun{}
	}
	w.runs[r.id] = r
	w.mu.Unlock()
	return r
}

// watchGit is startGitWatch with the check to call when the run is over:
//
//	defer d.watchGit(s, "test")()
func (d *Daemon) watchGit(s *state.Session, hook string) func() {
	r := d.startGitWatch(s, hook)
	return func() {
		if r != nil {
			d.checkGitRun(r, true)
		}
	}
}

// checkGitRun compares r's repository with r.before and reports what
// changed. done: r is over (forgotten); else (a dev server still running)
// what was found becomes r's new before.
func (d *Daemon) checkGitRun(r *gitRun, done bool) {
	w := &d.gitw
	w.mu.Lock()
	before := r.before
	w.mu.Unlock()
	after := gitwatch.Take(r.dir)
	changes := gitwatch.Diff(before, after)
	w.mu.Lock()
	start := r.start
	if done {
		delete(w.runs, r.id)
	} else {
		r.before, r.start = after, time.Now()
	}
	if len(changes) == 0 {
		w.mu.Unlock()
		return
	}
	dig := after.Digest()
	if prev, ok := w.reported[r.dir]; ok && prev.digest == dig && prev.at.After(start) {
		w.mu.Unlock()
		return // an overlapping run reported this already
	}
	if w.reported == nil {
		w.reported = map[string]gitWatchReport{}
	}
	w.reported[r.dir] = gitWatchReport{digest: dig, at: time.Now()}
	var others []string
	for _, o := range w.runs {
		if o != r && o.dir == r.dir {
			others = append(others, fmt.Sprintf("[hooks] %s of %s", o.hook, o.slug))
		}
	}
	w.mu.Unlock()
	sort.Strings(others)
	d.reportGitChange(r, changes, others)
}

// reportGitChange puts the item for a person in the inbox.
func (d *Daemon) reportGitChange(r *gitRun, changes []gitwatch.Change, others []string) {
	log.Printf("%s: [hooks] %s changed %d watched entries of %s", r.slug, r.hook, len(changes), r.dir)
	now := time.Now()
	it := &state.InboxItem{
		Kind: "decision", From: callerPekoed,
		Question: fmt.Sprintf("git hooks / config of %s changed while pekoed ran [hooks] %s of %s", r.dir, r.hook, r.slug),
		Options:  append([]string(nil), gitChangeOptions...),
		Links:    []string{},
		Rev:      1, Status: state.InboxOpen, Delivery: state.DeliveryNone, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := d.snapshot(r.slug); err == nil {
		it.About = r.slug
		it.Facts = d.inboxFacts(r.slug)
	}
	var b strings.Builder
	b.WriteString("What git runs later (hooks, core.hooksPath, core.fsmonitor, alias.*, include.path, …) in the repository's git directory changed while pekoed ran a command from the worktree in its sandbox. ")
	b.WriteString("The git you run yourself in the main checkout (and editors' git plugins) will run it outside any sandbox. ")
	b.WriteString("Expected when the hook installs git hooks (husky, pre-commit, lefthook); if not, look at it before using git there. pekoed changed and removed nothing.\n")
	if len(others) > 0 {
		b.WriteString("also running in this repository then: " + strings.Join(others, ", ") + "\n")
	}
	b.WriteString("\n")
	b.WriteString(gitwatch.Describe(changes, 40))
	ctx := cleanInbox(b.String(), true)
	if utf8.RuneCountInString(ctx) > inboxContextMax {
		ctx = string([]rune(ctx)[:inboxContextMax-1]) + "…"
	}
	it.Context = ctx
	it.Question = cleanInbox(it.Question, false)
	d.inboxMu.Lock()
	err := d.DB.AddInbox(it)
	d.inboxMu.Unlock()
	if err != nil {
		log.Printf("git watch %s: %v", r.slug, err)
		return
	}
	d.emit("inbox.updated", it)
}

// gitChangeOptions: pekoed does nothing with the answer; it records what
// the person made of it.
var gitChangeOptions = []string{"expected (git hook installer): keep it", "not expected: I'll look and undo it"}

// ---- dev servers ----

// startDevWatch watches s's dev server from now on (replacing a watch it
// had).
func (d *Daemon) startDevWatch(s *state.Session) {
	r := d.startGitWatch(s, "dev")
	if r == nil {
		return
	}
	w := &d.gitw
	w.mu.Lock()
	if w.dev == nil {
		w.dev = map[string]*gitRun{}
	}
	if old := w.dev[s.Slug]; old != nil {
		delete(w.runs, old.id)
	}
	w.dev[s.Slug] = r
	w.mu.Unlock()
}

// endDevWatch looks at slug's dev server's repository once more and stops
// watching it.
func (d *Daemon) endDevWatch(slug string) {
	w := &d.gitw
	w.mu.Lock()
	r := w.dev[slug]
	delete(w.dev, slug)
	w.mu.Unlock()
	if r != nil {
		d.checkGitRun(r, true)
	}
}

// checkDevWatches (tickLoop): each running dev server's repository is
// compared with what it was; a dev server that stopped is looked at a last
// time; one running without a watch (pekoed restarted) gets one.
func (d *Daemon) checkDevWatches() {
	if !d.gitw.ticking.CompareAndSwap(false, true) {
		return
	}
	defer d.gitw.ticking.Store(false)
	d.mu.Lock()
	var devs []state.Session
	for _, s := range d.sessions {
		if s.Dev {
			devs = append(devs, *s)
		}
	}
	d.mu.Unlock()
	running := map[string]bool{}
	for i := range devs {
		s := &devs[i]
		if !d.Eng.Alive(s.Slug, engine.WinDev) {
			continue
		}
		running[s.Slug] = true
		d.gitw.mu.Lock()
		_, ok := d.gitw.dev[s.Slug]
		d.gitw.mu.Unlock()
		if !ok {
			d.startDevWatch(s)
		}
	}
	d.gitw.mu.Lock()
	var check []*gitRun
	var ended []string
	for slug, r := range d.gitw.dev {
		if running[slug] {
			check = append(check, r)
		} else {
			ended = append(ended, slug)
		}
	}
	d.gitw.mu.Unlock()
	for _, r := range check {
		d.checkGitRun(r, false)
	}
	for _, slug := range ended {
		d.endDevWatch(slug)
	}
}
