package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

func barModel(t *testing.T, width int) *model {
	t.Helper()
	t.Setenv("HOME", "/home/u")
	m := tapiModel(t, "truecolor", config.Mascot{Shape: "round"})
	m.width = width
	m.sessions = []*state.Session{
		{Slug: "a", Branch: "feat/a", RepoID: "r1", Path: "/home/u/worktrees/pekoe/feat-statusbar-worktree-path", State: state.Running, StateSince: time.Now()},
	}
	m.sel = 0
	return m
}

func TestBarShowsWorktreePath(t *testing.T) {
	m := barModel(t, 200)
	bar := m.viewBar()
	plain := ansi.Strip(bar)
	if !strings.Contains(plain, " ~/worktrees/pekoe/feat-statusbar-worktree-path ") {
		t.Fatalf("path missing: %q", plain)
	}
	if !strings.Contains(plain, "? help") {
		t.Errorf("hints pushed out: %q", plain)
	}
	if w(bar) != m.width {
		t.Errorf("width %d, want %d", w(bar), m.width)
	}
	// dim, a neutral role: never a hue
	if !strings.Contains(bar, m.th.FG(theme.Dim, "~/worktrees/pekoe/feat-statusbar-worktree-path")) {
		t.Errorf("path is not dim: %q", bar)
	}
}

func TestBarPathGivesWayToHints(t *testing.T) {
	for _, width := range []int{60, 80, 100, 120, 140} {
		m := barModel(t, width)
		bar := m.viewBar()
		plain := ansi.Strip(bar)
		if w(bar) != width {
			t.Errorf("%d: width %d", width, w(bar))
		}
		// the hints are exactly what fits without the path
		m.sessions[0].Path = ""
		bare := ansi.Strip(m.viewBar())
		hints := func(s string) string { return strings.TrimLeft(s[strings.LastIndex(s, "\ue0b4")+len("\ue0b4"):], " ") } // after the last pill cap
		if got, want := hints(plain), hints(bare); !strings.HasSuffix(got, want) {
			t.Errorf("%d: hints %q, want %q", width, got, want)
		}
		if strings.Contains(plain, "worktree-path ") && !strings.Contains(plain, "worktree-path  ") {
			t.Errorf("%d: path touches the hints: %q", width, plain)
		}
		if strings.Contains(plain, "~/worktrees") && !strings.Contains(plain, "feat-statusbar-worktree-path") {
			t.Errorf("%d: path cut at the tail: %q", width, plain)
		}
		if width <= 120 && strings.Contains(plain, "/") && strings.Contains(plain, "worktree-path") && !strings.Contains(plain, "…") {
			t.Errorf("%d: a cut path needs a leading …: %q", width, plain)
		}
	}
	// wide enough: the tail survives with a leading …
	m := barModel(t, 140)
	if plain := ansi.Strip(m.viewBar()); !strings.Contains(plain, "…") || !strings.Contains(plain, "worktree-path ") {
		t.Errorf("140: want a left-cut path: %q", plain)
	}
}

func TestBarPathHiddenWithoutSession(t *testing.T) {
	m := barModel(t, 200)
	m.sessions = nil
	if strings.Contains(ansi.Strip(m.viewBar()), "~/") {
		t.Error("no session, no path")
	}
	// a repository picked with w that is not the session's: the bar names
	// the repository, not a worktree
	m = barModel(t, 200)
	m.chosen = &daemon.KnownRepo{ID: "r2", Name: "other", Dir: "/home/u/src/other"}
	if strings.Contains(ansi.Strip(m.viewBar()), "~/") {
		t.Error("picked repository should hide the session path")
	}
}

func TestClipLeft(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"~/a/b", 10, "~/a/b"},
		{"~/worktrees/pekoe/x", 9, "…/pekoe/x"},
		{"~/ほげ/ふが", 6, "…/ふが"},
		{"~/ほげ/ふが", 5, "…ふが"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
	} {
		got := clipLeft(c.in, c.n)
		if got != c.want || w(got) > c.n {
			t.Errorf("clipLeft(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestHomeTilde(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	for in, want := range map[string]string{
		"/home/u":         "~",
		"/home/u/w/x":     "~/w/x",
		"/home/user2/w/x": "/home/user2/w/x",
		"/tmp/x":          "/tmp/x",
	} {
		if got := homeTilde(in); got != want {
			t.Errorf("homeTilde(%q) = %q, want %q", in, got, want)
		}
	}
}
