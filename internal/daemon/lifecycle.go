package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/gitx"
	"pekoe/internal/initdetect"
	"pekoe/internal/paths"
	"pekoe/internal/sandbox"
	"pekoe/internal/state"
)

// ---- worktree list ----

type WorktreeView struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Slug    string `json:"slug,omitempty"`
	Port    int    `json:"port,omitempty"`
	Managed bool   `json:"managed"`
	Main    bool   `json:"main"`
	State   string `json:"state,omitempty"`
}

type wtListP struct {
	RepoID string `json:"repo_id"`
	Repo   string `json:"repo"` // a path inside the repository
}

func (d *Daemon) hWorktreeList(p wtListP) (any, error) {
	repoDirs := map[string]bool{}
	if p.Repo != "" {
		if r, err := resolveRepo(p.Repo); err == nil {
			repoDirs[r.Dir] = true
		}
	}
	d.mu.Lock()
	bySlugPath := map[string]*state.Session{}
	for _, s := range d.sessions {
		if p.RepoID == "" || s.RepoID == p.RepoID {
			if p.Repo == "" {
				repoDirs[s.RepoDir] = true
			}
			c := *s
			bySlugPath[s.Path] = &c
		}
	}
	d.mu.Unlock()
	out := []WorktreeView{}
	for dir := range repoDirs {
		wts, err := gitx.Worktrees(dir)
		if err != nil {
			continue
		}
		for i, w := range wts {
			if w.Bare {
				continue
			}
			v := WorktreeView{Path: w.Path, Branch: w.Branch, Main: i == 0, Managed: paths.UnderWorktrees(w.Path)}
			if s := bySlugPath[w.Path]; s != nil {
				v.Slug, v.Port, v.State = s.Slug, s.Port, string(s.State)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// ---- rm: teardown → remove worktree (only under ~/worktrees) ----

type rmP struct {
	Slug  string `json:"slug"`
	Force bool   `json:"force"`
}

func (d *Daemon) teardown(s *state.Session) error {
	cfg := d.repoConfig(s)
	v := d.vars(s)
	for _, c := range cfg.Hooks.Teardown {
		cmd := v.ExpandShell(c)
		if out, err := d.shell(context.Background(), s.Path, s.Env, cmd, 10*time.Minute); err != nil {
			return fmt.Errorf("teardown %q: %v\n%s", cmd, err, tail(out, 10))
		}
	}
	return nil
}

func (d *Daemon) hRm(p rmP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	_, statErr := os.Stat(s.Path)
	exists := statErr == nil
	// the directory may belong to another repository of the same name (a
	// fork shares ~/worktrees/<name>): then it is not ours to run hooks in or
	// to delete
	owner := error(nil)
	if exists {
		owner = gitx.CheckWorktreeOf(s.RepoDir, s.Path)
	}
	foreign := errors.Is(owner, gitx.ErrForeignWorktree)
	if !p.Force && s.Managed && !foreign {
		if out, _ := gitx.Run(s.Path, "status", "--porcelain"); out != "" {
			return nil, fmt.Errorf("%s has uncommitted changes (use force)", s.Path)
		}
	}
	_ = d.Eng.KillSession(p.Slug)
	deleted := false
	if exists && foreign {
		log.Printf("rm %s: %s is a worktree of another repository; left as is", p.Slug, s.Path)
	} else if exists {
		if err := d.teardown(s); err != nil && !p.Force {
			return nil, err
		}
		gitx.DeleteRefs(s.RepoDir, "refs/pekoe/"+p.Slug+"/") // shared by all worktrees
		if s.Managed && paths.UnderWorktrees(s.Path) {
			if owner == nil {
				deleted = gitx.RemoveWorktree(s.RepoDir, s.Path, true) == nil
			}
			if !deleted && s.OwnDir {
				// not registered (half-created): delete the directory this
				// session made, and nothing else
				deleted = os.RemoveAll(s.Path) == nil
				_, _ = gitx.Run(s.RepoDir, "worktree", "prune")
			}
		}
	}
	d.purge(s)
	d.emit("session.removed", s)
	return map[string]any{"removed": p.Slug, "worktree_deleted": deleted}, nil
}

// hAgentRm is rm without the human token (call routes it here), for the
// hub agent cleaning up after land. Only a landed session: pekoed has
// already torn it down and removed its worktree when it saw the merge, so
// all that is left is pekoe's record of it (the row, its prompts and
// reviews, the files kept for it), and the code is on the base branch.
// rm of any other session removes a worktree and its uncommitted changes,
// which stays a person's decision; so does force. A landed session whose
// worktree is still there (the removal failed) is refused too: deleting
// that directory is rm's job, with the token.
//
// The state is checked again in forgetIf, under the lock that removes the
// session: a session.new of the same slug replaces a landed session, and a
// check made before that must not let rm through to the new one.
func (d *Daemon) hAgentRm(p rmP) (any, error) {
	if p.Force {
		return nil, fmt.Errorf("rm with force: %w", ErrHumanOnly)
	}
	s, err := d.get(p.Slug)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	st, path, managed := s.State, s.Path, s.Managed
	d.mu.Unlock()
	if st != state.Landed {
		return nil, fmt.Errorf("rm of a %s session removes its worktree (an agent may remove only landed sessions): %w", st, ErrHumanOnly)
	}
	if path != "" && managed {
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("%s has landed but its worktree %s is still there (removing it takes rm with the human token): %w", p.Slug, path, ErrHumanOnly)
		}
	}
	var snap *state.Session
	if !d.forgetIf(p.Slug, func(x *state.Session) bool {
		if x != s || x.State != state.Landed {
			return false
		}
		snap = d.copyLocked(x)
		return true
	}) {
		return nil, fmt.Errorf("%s changed while being removed (no longer the landed session): %w", p.Slug, ErrHumanOnly)
	}
	d.purgeFiles(snap)
	d.emit("session.removed", snap)
	return map[string]any{"removed": p.Slug, "worktree_deleted": false}, nil
}

// purge drops everything pekoe keeps about a session (not its worktree):
// the row and its prompts, reviews, comments and risks, the picks, errors,
// attachments and preview addressed to it.
func (d *Daemon) purge(s *state.Session) {
	// out of memory and state.db together, before anything else can save it
	// again (an update racing with rm used to write it back: see persist)
	d.forget(s.Slug)
	d.purgeFiles(s)
}

// purgeFiles is purge after the session is forgotten: the picks, errors,
// attachments, preview and agent settings kept outside its row.
func (d *Daemon) purgeFiles(s *state.Session) {
	d.recordSession(s, nil, true) // what stats keeps of it (stats.go)
	// picks addressed to this session go with it (no leftovers)
	if picks, err := d.DB.Picks(1000); err == nil {
		for _, pk := range picks {
			if pk.Slug == s.Slug && pk.RepoID == s.RepoID {
				os.Remove(pk.File)
				if pk.Image != "" {
					os.Remove(pk.Image)
				}
				_ = d.DB.DeletePick(pk.ID)
			}
		}
	}
	d.catch.Forget(s.Slug)
	removeAttachments(s.RepoID, s.Slug)
	d.dropPreview(s)
	os.Remove(agentSettingsFile(s.Slug))
}

// ---- dev server ----

func (d *Daemon) hDevToggle(p slugP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if d.Eng.Alive(p.Slug, engine.WinDev) {
		_ = d.Eng.Kill(p.Slug, engine.WinDev)
		return d.update(p.Slug, func(x *state.Session) { x.Dev = false })
	}
	cfg := d.repoConfig(s)
	if cfg.Hooks.Dev == "" {
		return nil, fmt.Errorf("no [hooks] dev command in %s", paths.RepoConfig(s.RepoID))
	}
	cmd := d.vars(s).ExpandShell(cfg.Hooks.Dev)
	full := cmd + `; printf '\n[pekoe] dev server exited (%s)\n' "$?"`
	// the dev server runs the worktree's code (package.json …) and keeps
	// running: in the sandbox, or not at all
	full, err = d.sbx.ShellLine(userShell(), full)
	if err != nil {
		return nil, fmt.Errorf("dev server %w", err)
	}
	if err := d.Eng.Spawn(engine.Spawn{Slug: p.Slug, Window: engine.WinDev, Dir: s.Path, Env: append(s.Env, "PEKOE_SESSION="+p.Slug), Command: full, Width: 120, Height: 40}); err != nil {
		return nil, err
	}
	return d.update(p.Slug, func(x *state.Session) { x.Dev = true })
}

// hAgentDevToggle is dev.toggle without the human token (call routes it
// here): an agent starts or stops the dev server of its own session only,
// to look at its UI (its own sandbox cannot listen on a port). The command
// is the repository's [hooks] dev, run in pekoed's hooks sandbox like
// [hooks] test, which any Stop runs: no new way to run something. The
// session is the caller's window (caller.go), never the agent's word; a
// process pekoed cannot place, tapi chat and another session are refused.
func (d *Daemon) hAgentDevToggle(who *Caller, p slugP) (any, error) {
	pane := who.Pane()
	switch {
	case pane == "":
		return nil, errUnknownCaller("dev.toggle")
	case pane == TapiSlug:
		return nil, fmt.Errorf("dev.toggle: tapi chat has no dev server; a session starts its own")
	case p.Slug != "" && p.Slug != pane:
		return nil, fmt.Errorf("dev.toggle: a session starts or stops only its own dev server (%s, not %s): %w", pane, p.Slug, ErrHumanOnly)
	}
	return d.hDevToggle(slugP{Slug: pane})
}

// ---- run: one command in the worktree (TUI `!`) ----

type runP struct {
	Slug    string `json:"slug"`
	Command string `json:"command"` // empty: only report the run window's state
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// RunState is what run.start and run.close answer: whether the session's
// run window is still running, or holds a finished command waiting to be
// closed, which command it is (the daemon keeps it: every TUI, and one
// restarted meanwhile, names the same) and how it ended. Started is set
// when this call spawned it.
type RunState struct {
	Running    bool   `json:"running"`
	Finished   bool   `json:"finished"`
	Started    bool   `json:"started"`
	Command    string `json:"command,omitempty"`
	ExitStatus int    `json:"exit_status"`      // -1: still running, unknown, or killed by Signal
	Signal     string `json:"signal,omitempty"` // "SIGINT" when Ctrl-C ended it
}

// runState reads the session's run window. Callers hold runMu.
func (d *Daemon) runState(s *state.Session) RunState {
	st := RunState{Command: s.Run, ExitStatus: -1}
	if d.Eng.Alive(s.Slug, engine.WinRun) {
		st.Running = true
		return st
	}
	if sc, err := d.Eng.Capture(s.Slug, engine.WinRun); err == nil && sc.Dead {
		st.Finished = true
		st.ExitStatus, st.Signal = sc.ExitStatus, sc.Signal
	}
	return st
}

// hRunStart runs one command in a pty of its own, in the worktree with the
// dev server's environment, through the user's shell. A session has at most
// one run window: while one is running (or finished but not closed), the
// call starts nothing and says so, and the TUI shows that window instead.
// Calls are serialized (runMu): Enter pressed twice must not let the second
// Spawn kill the first run and start the command again.
func (d *Daemon) hRunStart(p runP) (any, error) {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if st := d.runState(s); st.Running || st.Finished {
		return st, nil
	}
	cmd := strings.TrimSpace(p.Command)
	if cmd == "" {
		return RunState{ExitStatus: -1}, nil
	}
	// never outside the worktree: tmux starts in $HOME when -c is missing
	switch s.State {
	case state.Creating:
		return nil, fmt.Errorf("%s is still being created", p.Slug)
	case state.Landed:
		return nil, fmt.Errorf("%s has landed; its worktree is gone", p.Slug)
	}
	if fi, err := os.Stat(s.Path); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("worktree missing: %s", s.Path)
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	w, h := d.spawnSize(p.Width, p.Height)
	full := "/bin/sh -c " + shellQuote(runScript(sh, cmd, w, h))
	env := append(append([]string{}, s.Env...), "PEKOE_SESSION="+p.Slug)
	if err := d.Eng.Spawn(engine.Spawn{Slug: p.Slug, Window: engine.WinRun, Dir: s.Path, Env: env, Command: full, Width: w, Height: h, Quiet: true}); err != nil {
		return nil, err
	}
	d.update(p.Slug, func(x *state.Session) { x.Run = cmd })
	return RunState{Running: true, Started: true, Command: cmd, ExitStatus: -1}, nil
}

// hRunClose closes the run window (finished or not) and answers how it
// ended, for the TUI's "tell the agent?".
func (d *Daemon) hRunClose(p slugP) (any, error) {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	st := d.runState(s)
	_ = d.Eng.Kill(p.Slug, engine.WinRun)
	if s.Run != "" {
		d.update(p.Slug, func(x *state.Session) { x.Run = "" })
	}
	return st, nil
}

// runScript is the run window's sh script. It waits (up to 1 s) for the pty
// to get its size: tmux makes the window at the session's size and resizes
// it (and sets Quiet's option) right after, and a command over before that
// (echo) would have its output pushed into the scrollback by the shrink.
// Then it echoes the command (dim) so the window says what ran, and hands
// the pty to the user's shell, whose exit status becomes the pane's.
func runScript(sh, cmd string, w, h int) string {
	return fmt.Sprintf(`i=0; while [ "$(stty size 2>/dev/null)" != "%d %d" ] && [ $i -lt 20 ]; do sleep 0.05; i=$((i+1)); done; `, h, w) +
		`printf '\033[2m$ %s\033[0m\n' ` + shellQuote(cmd) + "; exec " + shellQuote(sh) + " -c " + shellQuote(cmd)
}

// ---- land: rebase → test → push → gh pr create; teardown after merge ----

func (d *Daemon) hLand(p slugP) (any, error) { return d.land(p, false) }

// hAgentLand is land without the human token (call routes it here). An
// agent whose sandbox allows github.com could push and run gh pr create
// itself, so pushing the session's branch and opening its PR is no more
// than it can do; merging stays a person's decision, and the teardown
// waits for that merge (the PR poller or watchMerges sees it). A repository without origin is
// different: land fast-forwards the base branch in the main checkout and
// removes the worktree right away, with nobody merging, so that stays
// human-only. The check sits in land itself, where it reads origin, so
// removing origin after a check cannot slip past it.
func (d *Daemon) hAgentLand(p slugP) (any, error) { return d.land(p, true) }

func (d *Daemon) land(p slugP, agent bool) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.PRReview {
		return nil, errors.New("this is someone else's PR; nothing to land")
	}
	if out, err := gitx.Run(s.Path, "status", "--porcelain"); err != nil {
		return nil, err
	} else if out != "" {
		return nil, errors.New("uncommitted changes in the worktree: ask the agent to commit first")
	}
	base := s.Base
	if base == "" {
		base = gitx.DefaultBase(s.Path)
	}
	if err := gitx.CheckRefArg("base", base); err != nil {
		return nil, err
	}
	if err := gitx.CheckRefArg("branch", s.Branch); err != nil {
		return nil, err
	}
	hasOrigin := gitx.Origin(s.Path) != ""
	if !hasOrigin && agent {
		return nil, fmt.Errorf("land in a repository without origin fast-forwards %s and removes the worktree with nobody merging: %w", base, ErrHumanOnly)
	}
	steps := []string{}
	onto := base
	if hasOrigin {
		if _, err := gitx.Run(s.Path, "fetch", "--end-of-options", "origin", base); err == nil {
			onto = "origin/" + base
		}
	}
	if _, err := gitx.Run(s.Path, "rebase", "--end-of-options", onto); err != nil {
		_, _ = gitx.Run(s.Path, "rebase", "--abort")
		return nil, fmt.Errorf("rebase onto %s failed (aborted): %v", onto, err)
	}
	steps = append(steps, "rebased onto "+onto)
	cfg := d.repoConfig(s)
	if cfg.Hooks.Test != "" {
		cmd := d.vars(s).ExpandShell(cfg.Hooks.Test)
		out, err := d.shell(context.Background(), s.Path, s.Env, cmd, 30*time.Minute)
		if errors.Is(err, sandbox.ErrUnavailable) {
			return nil, fmt.Errorf("tests %w", err)
		}
		t := state.Test{Status: "pass", Summary: summarize(out, err == nil), At: time.Now()}
		if err != nil {
			t.Status = "fail"
		}
		d.update(p.Slug, func(x *state.Session) { x.Test = t })
		if err != nil {
			return nil, fmt.Errorf("tests failed: %s", t.Summary)
		}
		steps = append(steps, "tests passed")
	}
	if !hasOrigin {
		// local-only repository: fast-forward the base branch in the main worktree
		mainBranch := gitx.CurrentBranch(s.RepoDir)
		if mainBranch == base {
			if _, err := gitx.Run(s.RepoDir, "merge", "--ff-only", "--end-of-options", s.Branch); err != nil {
				return nil, err
			}
		} else if _, err := gitx.Run(s.RepoDir, "branch", "-f", "--end-of-options", base, s.Branch); err != nil {
			return nil, err
		}
		steps = append(steps, "fast-forwarded "+base)
		if err := d.finishLanding(p.Slug); err != nil {
			return nil, err
		}
		return map[string]any{"steps": steps, "landed": true}, nil
	}
	if _, err := gitx.Run(s.Path, "push", "-u", "--force-with-lease", "--end-of-options", "origin", "HEAD:"+s.Branch); err != nil {
		return nil, err
	}
	steps = append(steps, "pushed "+s.Branch)
	pr := s.PR
	if pr == 0 {
		c, err := ghCommand(context.Background(), s.Path, "pr", "create", "--fill", "--head", s.Branch, "--base", base)
		if err != nil {
			return nil, err
		}
		out, err := c.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "already exists") {
			return nil, fmt.Errorf("gh pr create: %s", strings.TrimSpace(string(out)))
		}
		pr = prNumber(string(out))
		if pr == 0 {
			pr = prForBranch(s.Path, s.Branch)
		}
		steps = append(steps, fmt.Sprintf("PR #%d", pr))
	}
	d.update(p.Slug, func(x *state.Session) { x.PR = pr; x.Landing = true })
	return map[string]any{"steps": steps, "pr": pr}, nil
}

var prURLRe = regexp.MustCompile(`/pull/(\d+)`)

func prNumber(s string) int {
	if m := prURLRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func prForBranch(dir, branch string) int {
	c, err := ghCommand(context.Background(), dir, "pr", "view", branch, "--json", "number")
	if err != nil {
		return 0
	}
	out, err := c.Output()
	if err != nil {
		return 0
	}
	var v struct {
		Number int `json:"number"`
	}
	json.Unmarshal(out, &v)
	return v.Number
}

func (d *Daemon) finishLanding(slug string) error {
	// one at a time per session: watchMerges (at start and every 2 minutes)
	// and the PR poller seeing MERGED (applyPRStatus) both start it, and a
	// slow teardown must not run twice in the same worktree
	if _, busy := d.finishing.LoadOrStore(slug, true); busy {
		return nil
	}
	defer d.finishing.Delete(slug)
	s, err := d.snapshot(slug)
	if err != nil {
		return err
	}
	if s.State == state.Landed { // finished by a run that started earlier
		return nil
	}
	merged := time.Now() // when pekoe saw it merged, before the teardown's minutes
	_ = d.Eng.KillSession(slug)
	if errors.Is(gitx.CheckWorktreeOf(s.RepoDir, s.Path), gitx.ErrForeignWorktree) {
		log.Printf("land %s: %s is a worktree of another repository; left as is", slug, s.Path)
	} else {
		if err := d.teardown(s); err != nil {
			log.Printf("teardown %s: %v", slug, err)
		}
		gitx.DeleteRefs(s.RepoDir, "refs/pekoe/"+slug+"/") // shared by all worktrees
	}
	if s.Managed && paths.UnderWorktrees(s.Path) {
		if err := gitx.RemoveWorktree(s.RepoDir, s.Path, false); err != nil {
			log.Printf("remove worktree %s: %v", s.Path, err)
		}
	}
	landed, _ := d.update(slug, func(x *state.Session) {
		x.State, x.Landing, x.Dev, x.Stopped = state.Landed, false, false, true
		landedPRMerged(x)
	})
	if landed != nil {
		d.recordSession(landed, &merged, false) // stats.go
	}
	return nil
}

// watchPRs fills in the PR number of sessions whose branch got a PR outside
// pekoe (e.g. the agent ran `gh pr create`), so the list can show it.
func (d *Daemon) watchPRs() {
	d.mu.Lock()
	var check []state.Session
	for _, s := range d.sessions {
		if s.PR == 0 && !s.PRReview && s.State != state.Landed &&
			s.Branch != "" && s.Branch != s.Base && s.Path != "" {
			check = append(check, *s)
		}
	}
	d.mu.Unlock()
	for _, s := range check {
		if pr := prForBranch(s.Path, s.Branch); pr > 0 {
			d.update(s.Slug, func(x *state.Session) {
				if x.PR == 0 {
					x.PR = pr
				}
			})
		}
	}
}

// landMerged finishes the landing of a session whose PR was seen merged.
func (d *Daemon) landMerged(slug string) {
	if err := d.finishLanding(slug); err != nil {
		log.Printf("land %s: %v", slug, err)
	}
}

// watchMerges detects merged PRs of landing sessions and tears them down.
func (d *Daemon) watchMerges() {
	d.mu.Lock()
	var landing []*state.Session
	for _, s := range d.sessions {
		if s.Landing && s.PR > 0 {
			c := *s
			landing = append(landing, &c)
		}
	}
	d.mu.Unlock()
	for _, s := range landing {
		c, err := ghCommand(context.Background(), s.RepoDir, "pr", "view", strconv.Itoa(s.PR), "--json", "state")
		if err != nil {
			continue
		}
		out, err := c.Output()
		if err != nil {
			continue
		}
		var v struct {
			State string `json:"state"`
		}
		json.Unmarshal(out, &v)
		if v.State == "MERGED" {
			d.landMerged(s.Slug)
		}
	}
}

// ---- editor.open (MCP → nvim) ----

type editorP struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Note string `json:"note"`
	Slug string `json:"slug"`
}

func (d *Daemon) hEditorOpen(p editorP) (any, error) {
	if p.Path == "" {
		return nil, errors.New("path required")
	}
	if !filepath.IsAbs(p.Path) && p.Slug != "" {
		if s, err := d.snapshot(p.Slug); err == nil {
			p.Path = filepath.Join(s.Path, p.Path)
		}
	}
	d.emit("editor.open", p)
	return map[string]any{"ok": true}, nil
}

// ---- pekoe init ----

type initP struct {
	Path string `json:"path"`
}

func (d *Daemon) hInitDetect(p initP) (any, error) {
	repo, err := resolveRepo(p.Path)
	if err != nil {
		return nil, err
	}
	d.resolved(repo)
	return initdetect.Detect(repo.Dir, repo.ID)
}

type initWriteP struct {
	Path string `json:"path"`
	TOML string `json:"toml"`
	Over bool   `json:"overwrite"`
}

func (d *Daemon) hInitWrite(p initWriteP) (any, error) {
	repo, err := resolveRepo(p.Path)
	if err != nil {
		return nil, err
	}
	d.resolved(repo)
	dst := paths.RepoConfig(repo.ID)
	if _, err := os.Stat(dst); err == nil && !p.Over {
		return nil, fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), paths.DirPerm); err != nil {
		return nil, err
	}
	return map[string]any{"path": dst}, os.WriteFile(dst, []byte(p.TOML), paths.FilePerm)
}

// ---- review surface (nvim in the session's review window) ----

type surfaceP struct {
	Slug   string `json:"slug"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// hReviewSurface swaps the right pane to nvim (claude keeps running in its
// own window). nvim gets PEKOE_SESSION and opens :PekoeReview by itself.
func (d *Daemon) hReviewSurface(p surfaceP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("nvim"); err != nil {
		return nil, errors.New("nvim not found; set [review] surface = \"tui\"")
	}
	d.hReviewStart(slugP{Slug: p.Slug})
	if d.Eng.Alive(p.Slug, engine.WinReview) {
		return nil, nil
	}
	cmd := "nvim"
	env := append(append([]string{}, s.Env...), "PEKOE_SESSION="+p.Slug, "PEKOE_REVIEW=1", "PEKOE_SOCK="+paths.Socket())
	// The plugin directory is pekoed's own, never the caller's: nvim reads
	// "|" in an Ex command as a separator ("set rtp+=/x | !cmd" would run
	// cmd outside the agent's sandbox). It reaches nvim through the
	// environment, so no path is spliced into the command. After the
	// user's config: plugin managers like lazy.nvim reset rtp at startup.
	if rtp := nvimPluginDir(); rtp != "" {
		env = append(env, "PEKOE_NVIM_PLUGIN="+rtp)
		cmd += " -c " + shellQuote(`lua vim.opt.rtp:append(vim.env.PEKOE_NVIM_PLUGIN); vim.cmd.runtime("plugin/pekoe.lua")`)
	}
	return nil, d.Eng.Spawn(engine.Spawn{Slug: p.Slug, Window: engine.WinReview, Dir: s.Path, Env: env, Command: cmd, Width: p.Width, Height: p.Height})
}

func nvimPluginDir() string {
	if v := os.Getenv("PEKOE_NVIM_RTP"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	for _, c := range []string{filepath.Join(filepath.Dir(exe), "..", "nvim"), filepath.Join(filepath.Dir(exe), "..", "share", "pekoe", "nvim")} {
		if _, err := os.Stat(filepath.Join(c, "lua", "pekoe", "init.lua")); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

type windowP struct {
	Slug   string `json:"slug"`
	Window string `json:"window"`
}

func (d *Daemon) hWindowKill(p windowP) (any, error) {
	if p.Window == engine.WinAgent {
		return nil, errors.New("use session.kill for the agent window")
	}
	_ = d.Eng.Kill(p.Slug, p.Window)
	if p.Window == engine.WinReview {
		// leaving the review surface: back to done unless comments are pending
		_, _ = d.hReviewEnd(slugP{Slug: p.Slug})
	}
	if p.Window == engine.WinDev {
		d.update(p.Slug, func(s *state.Session) { s.Dev = false })
	}
	if p.Window == engine.WinRun {
		_, err := d.hRunClose(slugP{Slug: p.Slug})
		return nil, err
	}
	return nil, nil
}
