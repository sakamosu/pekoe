package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"boba/internal/daemon"
	"boba/internal/state"
	"boba/internal/theme"
)

func TestLsScope(t *testing.T) {
	app := &daemon.KnownRepo{ID: "path_app", Name: "app"}
	web := &daemon.KnownRepo{ID: "path_web", Name: "web"}
	resolve := func(q string) (*daemon.KnownRepo, error) {
		if q == "web" {
			return web, nil
		}
		return nil, errors.New("unknown repo: " + q)
	}
	inApp := func() *daemon.KnownRepo { return app }
	outside := func() *daemon.KnownRepo { return nil }
	cases := []struct {
		name string
		args []string
		here func() *daemon.KnownRepo
		want *daemon.KnownRepo
		err  bool
	}{
		{"inside a repository: that one", nil, inApp, app, false},
		{"--all inside", []string{"--all"}, inApp, nil, false},
		{"outside: everything", nil, outside, nil, false},
		{"--repo wins", []string{"--repo", "web"}, inApp, web, false},
		{"--repo with --all", []string{"--all", "--repo", "web"}, outside, web, false},
		{"--repo unknown", []string{"--repo", "nope"}, inApp, nil, true},
	}
	for _, c := range cases {
		f, _, err := parseCmd("ls", c.args)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := lsScope(f, resolve, c.here)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s: got %+v, %v", c.name, got, err)
		}
	}
}

func TestLsLinesGroupsByRepo(t *testing.T) {
	th := theme.Load(nil) // not a terminal: no color
	ic := theme.Icons("ascii")
	now := time.Now()
	at := func(m int) time.Time { return now.Add(time.Duration(m) * time.Minute) }
	ss := []*state.Session{
		{Slug: "web-feat-x", Branch: "feat/x", Repo: "web", RepoID: "path_web", State: state.Running, CreatedAt: at(2), StateSince: now},
		{Slug: "feat-x", Branch: "feat/x", Repo: "app", RepoID: "path_app", State: state.Running, CreatedAt: at(1), StateSince: now},
		{Slug: "fix-y", Branch: "fix/y", Repo: "app", RepoID: "path_app", State: state.Done, CreatedAt: at(3), StateSince: now},
	}
	got := strings.Join(lsLines(th, ic, ss, true, now), "\n")
	lines := strings.Split(got, "\n")
	if len(lines) != 6 || lines[0] != "app" || lines[3] != "" || lines[4] != "web" {
		t.Fatalf("grouped:\n%s", got)
	}
	if !strings.Contains(lines[1], "feat/x") || !strings.Contains(lines[2], "fix/y") {
		t.Fatalf("creation order within a group:\n%s", got)
	}
	if strings.Contains(lines[1], "slug") || !strings.Contains(lines[5], "slug web-feat-x") {
		t.Fatalf("a slug that is not the branch is shown:\n%s", got)
	}

	// filtered (or a single repository): no headings
	flat := lsLines(th, ic, ss, false, now)
	if len(flat) != 3 || !strings.Contains(flat[0], "feat/x") || strings.Contains(flat[0], "slug") {
		t.Fatalf("flat:\n%s", strings.Join(flat, "\n"))
	}
	one := lsLines(th, ic, ss[1:], true, now)
	if len(one) != 2 {
		t.Fatalf("one repository has no heading:\n%s", strings.Join(one, "\n"))
	}

	// two repositories with the same short name: the heading shows the id
	ss[0].Repo = "app"
	dup := lsLines(th, ic, ss, true, now)
	if dup[0] != "app  path_app" || dup[4] != "app  path_web" {
		t.Fatalf("same name:\n%s", strings.Join(dup, "\n"))
	}
}
