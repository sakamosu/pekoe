package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/config"
)

// editor writes each of contents in turn, one per time it is opened.
func editor(t *testing.T, contents ...string) (func(string) error, *int) {
	n := 0
	return func(file string) error {
		if n >= len(contents) {
			t.Fatalf("editor opened %d times, want %d", n+1, len(contents))
		}
		if err := os.WriteFile(file, []byte(contents[n]), 0o600); err != nil {
			t.Fatal(err)
		}
		n++
		return nil
	}, &n
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	ms, _ := filepath.Glob(filepath.Join(dir, ".config-*.toml"))
	return ms
}

func TestConfigEditCreatesFromTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "config.toml")
	var seen string
	var out bytes.Buffer
	err := editConfig(path, strings.NewReader(""), &out, func(f string) error {
		b, _ := os.ReadFile(f)
		seen = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != config.Template {
		t.Errorf("the editor got %q", seen)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != config.Template {
		t.Fatalf("config.toml: %q %v", b, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if l := leftovers(t, filepath.Dir(path)); len(l) > 0 {
		t.Errorf("left %v", l)
	}
}

func TestConfigEditAsksOnProblems(t *testing.T) {
	good := "[review]\nsurface = \"tui\"\n"
	for _, c := range []struct {
		name, answers string
		edits         []string
		want          string // config.toml afterwards
		out           string
	}{
		{"edit again", "e\n", []string{"[review]\nsurface = \"vim\"\n", good}, good, `review.surface: "vim"`},
		{"save anyway", "s\n", []string{"[review]\nsurfac = \"tui\"\n"}, "[review]\nsurfac = \"tui\"\n", "review.surfac: unknown key"},
		{"quit", "q\n", []string{"[review\n"}, "animate = true\n", "syntax error: line 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			os.WriteFile(path, []byte("animate = true\n"), 0o600)
			ed, n := editor(t, c.edits...)
			var out bytes.Buffer
			if err := editConfig(path, strings.NewReader(c.answers), &out, ed); err != nil {
				t.Fatal(err)
			}
			if *n != len(c.edits) {
				t.Errorf("editor opened %d times", *n)
			}
			if b, _ := os.ReadFile(path); string(b) != c.want {
				t.Errorf("config.toml = %q, want %q", b, c.want)
			}
			if !strings.Contains(out.String(), c.out) {
				t.Errorf("output lacks %q:\n%s", c.out, out.String())
			}
			if l := leftovers(t, dir); len(l) > 0 {
				t.Errorf("left %v", l)
			}
		})
	}
}

func TestConfigEditNoChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("animate = true\n"), 0o600)
	var out bytes.Buffer
	if err := editConfig(path, strings.NewReader(""), &out, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no changes") {
		t.Errorf("out: %s", out.String())
	}
}

// stdin closed while asking: nothing is saved, the edit is kept and named
func TestConfigEditKeepsEditWithoutAnswer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("animate = true\n"), 0o600)
	ed, _ := editor(t, "animate = 3\n")
	err := editConfig(path, strings.NewReader(""), &bytes.Buffer{}, ed)
	if err == nil || !strings.Contains(err.Error(), "your edit is in") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "animate = true\n" {
		t.Errorf("config.toml = %q", b)
	}
	if l := leftovers(t, dir); len(l) != 1 {
		t.Errorf("kept %v", l)
	}
}

// a config.toml linked from elsewhere (dotfiles) stays a link
func TestConfigEditFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "pekoe.toml")
	os.MkdirAll(filepath.Dir(real), 0o700)
	os.WriteFile(real, []byte("animate = true\n"), 0o644)
	path := filepath.Join(dir, "cfg", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	ed, _ := editor(t, "animate = false\n")
	if err := editConfig(path, strings.NewReader(""), &bytes.Buffer{}, ed); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(path); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a file")
	}
	if b, _ := os.ReadFile(real); string(b) != "animate = false\n" {
		t.Errorf("target = %q", b)
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode changed: %v", fi.Mode())
	}
}

// config.toml changed while the editor was open: asked before overwriting
func TestConfigEditChangedMeanwhile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("animate = true\n"), 0o600)
	ed := func(f string) error {
		os.WriteFile(path, []byte("animate = false\n"), 0o600) // someone else
		return os.WriteFile(f, []byte("[tips]\nenable = false\n"), 0o600)
	}
	var out bytes.Buffer
	err := editConfig(path, strings.NewReader("n\n"), &out, ed)
	if err == nil || !strings.Contains(out.String(), "changed while you were editing") {
		t.Fatalf("err = %v, out = %s", err, out.String())
	}
	if b, _ := os.ReadFile(path); string(b) != "animate = false\n" {
		t.Errorf("overwritten: %q", b)
	}
}

func TestConfigCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	var out bytes.Buffer
	if err := checkConfig(path, &out); err != nil || !strings.Contains(out.String(), "no config.toml") {
		t.Fatalf("missing: %v %s", err, out.String())
	}
	os.WriteFile(path, []byte("[theme]\nmode = \"dark\"\n"), 0o600)
	out.Reset()
	if err := checkConfig(path, &out); err == nil || !strings.Contains(out.String(), `theme.mode: "dark"`) {
		t.Fatalf("bad: %v %s", err, out.String())
	}
	os.WriteFile(path, []byte("[theme]\nmode = \"ansi\"\n"), 0o600)
	out.Reset()
	if err := checkConfig(path, &out); err != nil || !strings.HasPrefix(out.String(), "ok:") {
		t.Fatalf("good: %v %s", err, out.String())
	}
}
