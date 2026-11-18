package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// pushFromElsewhere commits file to origin's main from another clone, so
// the repository's own main is left behind (a checkout nobody pulled).
func pushFromElsewhere(t *testing.T, bare, file string) {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(bare), "git", "clone", "-q", bare, other)
	os.WriteFile(filepath.Join(other, file), []byte("x\n"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", file)
	run(t, other, "git", "push", "-q", "origin", "main")
}

// commitLocal commits file on the repository's own main without pushing.
func commitLocal(t *testing.T, repo, file string) {
	t.Helper()
	os.WriteFile(filepath.Join(repo, file), []byte("x\n"), 0o644)
	run(t, repo, "git", "add", file)
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", file)
}

// worktreeStep is the worktree step of slug's creation.
func worktreeStep(t *testing.T, d *Daemon, slug string) Step {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, st := range d.progress[slug] {
		if st.Name == "worktree" {
			return st
		}
	}
	t.Fatalf("%s: no worktree step", slug)
	return Step{}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// A plain session.new (no --base) starts from origin/<base> when the
// repository's own base is behind it, and says so on the worktree step.
func TestNewStartsFromFetchedBaseWhenLocalBehind(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	pushFromElsewhere(t, bare, "merged.txt")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/fresh", "prompt": "go"})
	s = waitState(t, d, s.Slug, state.Idle, state.Running)
	if !exists(filepath.Join(s.Path, "merged.txt")) {
		t.Fatal("started from the local main, which is behind origin/main")
	}
	if s.Base != "main" {
		t.Errorf("base: %q, want main (land still rebases onto main)", s.Base)
	}
	st := worktreeStep(t, d, s.Slug)
	if !strings.Contains(st.Detail, "from origin/main (fetched") || !strings.Contains(st.Detail, "1 behind") {
		t.Errorf("worktree step detail: %q", st.Detail)
	}
	// no upstream: a plain git push must not go to main
	if up, err := gitx.Run(s.Path, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		t.Errorf("branch tracks %s", up)
	}
	waitCreations(t, d)
}

// Commits only the local base has are kept: it starts from the local base.
func TestNewKeepsLocalBaseWhenAhead(t *testing.T) {
	d, _, repo := setup(t)
	withOrigin(t, repo)
	commitLocal(t, repo, "local.txt")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/ahead", "prompt": "go"})
	s = waitState(t, d, s.Slug, state.Idle, state.Running)
	if !exists(filepath.Join(s.Path, "local.txt")) {
		t.Fatal("dropped the commit only the local main has")
	}
	st := worktreeStep(t, d, s.Slug)
	if !strings.Contains(st.Detail, "from main (1 commit not on origin/main)") {
		t.Errorf("worktree step detail: %q", st.Detail)
	}
	waitCreations(t, d)
}

// Local and origin both moved: the local base is kept, with a warning.
func TestNewKeepsLocalBaseWhenDiverged(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	pushFromElsewhere(t, bare, "merged.txt")
	commitLocal(t, repo, "local.txt")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/div", "prompt": "go"})
	s = waitState(t, d, s.Slug, state.Idle, state.Running)
	if !exists(filepath.Join(s.Path, "local.txt")) || exists(filepath.Join(s.Path, "merged.txt")) {
		t.Fatal("did not start from the local main")
	}
	if len(s.Warnings) == 0 || !strings.Contains(strings.Join(s.Warnings, "\n"), "diverged") {
		t.Errorf("warnings: %v", s.Warnings)
	}
	waitCreations(t, d)
}

// An explicit --base is used as given (no fetch), as before.
func TestNewExplicitBaseNotFetched(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	pushFromElsewhere(t, bare, "merged.txt")
	real := fetchOrigin
	t.Cleanup(func() { fetchOrigin = real })
	fetchOrigin = func(context.Context, string, string) error {
		t.Error("fetched for an explicit --base")
		return errors.New("no")
	}
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/expl", "base": "main", "prompt": "go"})
	s = waitState(t, d, s.Slug, state.Idle, state.Running)
	if exists(filepath.Join(s.Path, "merged.txt")) {
		t.Fatal("--base main started from origin/main")
	}
	if st := worktreeStep(t, d, s.Slug); !strings.HasSuffix(st.Detail, " · from main") {
		t.Errorf("worktree step detail: %q", st.Detail)
	}
	waitCreations(t, d)
}

// A fetch that fails starts from the local base, with a warning.
func TestNewFetchFailsStartsFromLocal(t *testing.T) {
	d, _, repo := setup(t)
	withOrigin(t, repo)
	real := fetchOrigin
	t.Cleanup(func() { fetchOrigin = real })
	fetchOrigin = func(context.Context, string, string) error { return errors.New("network down") }
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/off", "prompt": "go"})
	s = waitState(t, d, s.Slug, state.Idle, state.Running)
	st := worktreeStep(t, d, s.Slug)
	if !strings.Contains(st.Detail, "from main (fetch failed)") {
		t.Errorf("worktree step detail: %q", st.Detail)
	}
	if len(s.Warnings) == 0 || !strings.Contains(strings.Join(s.Warnings, "\n"), "could not fetch") {
		t.Errorf("warnings: %v", s.Warnings)
	}
	waitCreations(t, d)
}

// Without origin nothing is fetched: from the local base, as before.
func TestNewNoOriginFromLocal(t *testing.T) {
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/local", "prompt": "go"})
	s = waitState(t, d, s.Slug, state.Idle, state.Running)
	st := worktreeStep(t, d, s.Slug)
	if !strings.Contains(st.Detail, "from main (no origin)") {
		t.Errorf("worktree step detail: %q", st.Detail)
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings: %v", s.Warnings)
	}
	waitCreations(t, d)
}
