package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanKeepsOtherHooksAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	orig := `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}]}}`
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(orig), 0o600)

	path, before, after, changed, err := Plan("/usr/local/bin/pekoe", false)
	if err != nil || !changed {
		t.Fatalf("plan: %v changed=%v", err, changed)
	}
	if _, err := Apply(path, before, after); err != nil {
		t.Fatal(err)
	}
	if !Installed() {
		t.Fatal("not installed after apply")
	}
	var root map[string]any
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &root)
	if root["model"] != "opus" {
		t.Fatal("unrelated settings lost")
	}
	if !strings.Contains(string(b), "say done") {
		t.Fatal("user's own Stop hook lost")
	}
	// second install changes nothing
	if _, _, _, changed, _ := Plan("/usr/local/bin/pekoe", false); changed {
		t.Fatal("install is not idempotent")
	}
	// uninstall removes only ours
	path, before, after, _, _ = Plan("/usr/local/bin/pekoe", true)
	Apply(path, before, after)
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "pekoe event") || !strings.Contains(string(b), "say done") {
		t.Fatalf("uninstall: %s", b)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "settings.json.pekoe-backup-*"))
	if len(backups) == 0 {
		t.Fatal("no backup kept")
	}
}

// settings.json kept in dotfiles as a symlink: install writes through the
// link instead of replacing it with a regular file.
func TestApplyKeepsSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	dots := filepath.Join(t.TempDir(), "dotfiles")
	os.MkdirAll(dots, 0o755)
	real := filepath.Join(dots, "settings.json")
	os.WriteFile(real, []byte(`{"model":"opus"}`), 0o644)
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	path, before, after, _, err := Plan("/usr/local/bin/pekoe", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(path, before, after); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings.json is no longer a symlink: %v", err)
	}
	b, _ := os.ReadFile(real)
	if !strings.Contains(string(b), "pekoe event") {
		t.Fatalf("hooks did not reach the link's target: %s", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dots, "*pekoe-tmp*")); len(left) > 0 {
		t.Fatalf("tmp left behind: %v", left)
	}
}

// A binary under a path with spaces is quoted in the command; install must
// still recognize the hooks as its own (no duplicates, uninstall removes
// them, Installed is true).
func TestQuotedBinaryPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	bin := "/Users/me/My Tools/bin/pekoe"
	for i := 0; i < 2; i++ {
		path, before, after, _, err := Plan(bin, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(path, before, after); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if n := strings.Count(string(b), "event Stop"); n != 1 {
		t.Fatalf("Stop hook installed %d times:\n%s", n, b)
	}
	if !Installed() {
		t.Fatal("Installed() false after install")
	}
	path, before, after, _, _ := Plan(bin, true)
	Apply(path, before, after)
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "event Stop") {
		t.Fatalf("uninstall left hooks: %s", b)
	}
}

func TestIsOurs(t *testing.T) {
	for c, want := range map[string]bool{
		"pekoe event Stop":                          true,
		"/usr/local/bin/pekoe event Stop":           true,
		"'/Users/me/My Tools/bin/pekoe' event Stop": true,
		`'/a/it'\''s/pekoe' event Stop`:             true,
		"say done":                                  false,
		"/bin/notpekoe event Stop":                  false,
		"pekoe ls":                                  false,
	} {
		if got := isOurs(map[string]any{"command": c}); got != want {
			t.Errorf("isOurs(%q) = %v, want %v", c, got, want)
		}
	}
}

// A symlink planted at the old fixed temporary name (or at the backup's
// name) is not written through: the temporary file is a new one
// (os.CreateTemp) and the backup must not exist yet.
func TestApplyIgnoresPlantedLinks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"model":"opus"}`), 0o600)
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0o600)
	if err := os.Symlink(victim, path+".pekoe-tmp"); err != nil {
		t.Fatal(err)
	}
	p, before, after, _, err := Plan("/usr/local/bin/pekoe", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, before, after); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("written through the planted link: %q", b)
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("settings.json is not a regular file: %v", err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "pekoe event") {
		t.Fatalf("hooks not written: %s", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".settings.json.pekoe-tmp-*")); len(left) > 0 {
		t.Fatalf("tmp left behind: %v", left)
	}

	// the backup's name taken by a link: written next to it, not through it
	os.WriteFile(path, []byte(`{"model":"opus"}`), 0o600)
	p, before, after, _, _ = Plan("/usr/local/bin/pekoe", false)
	stamp := path + ".pekoe-backup-" + time.Now().Format("20060102-150405")
	os.Symlink(victim, stamp)
	backup, err := Apply(p, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("backup written through the planted link: %q", b)
	}
	if backup == stamp {
		t.Fatalf("backup reused the planted name %s", backup)
	}
}
