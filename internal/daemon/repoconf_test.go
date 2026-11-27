package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pekoe/internal/paths"
)

// Writing, rewriting and removing repos/<id>.toml emits repos.updated once
// per change; a look with nothing changed emits nothing (issue #336).
func TestRepoConfigsUpdated(t *testing.T) {
	d, _, _ := setup(t)
	n := 0
	d.testHook = func(ev string, data any) {
		if ev == "repos.updated" {
			n++
		}
	}
	check := func(want int, what string) {
		t.Helper()
		d.checkRepoConfigs()
		if n != want {
			t.Fatalf("%s: %d repos.updated, want %d", what, n, want)
		}
	}
	check(0, "nothing written")
	dir := paths.ReposConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "github.com_me_web.toml")
	write := func(s string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().Add(-time.Hour)
	write("[watch]\nenable = true\n", at)
	check(1, "enable = true")
	check(1, "unchanged")
	write("[watch]\nenable = false\n", at.Add(time.Second))
	check(2, "enable = false")
	// the same size: the modification time tells
	write("[watch]\nenable = falsE\n", at.Add(2*time.Second))
	check(3, "same size")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	check(3, "not a .toml")
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	check(4, "removed")
}
