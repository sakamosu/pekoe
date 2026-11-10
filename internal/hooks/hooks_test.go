package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if left, _ := filepath.Glob(filepath.Join(dots, "*.pekoe-tmp")); len(left) > 0 {
		t.Fatalf("tmp left behind: %v", left)
	}
}
