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

	path, before, after, changed, err := Plan("/usr/local/bin/boba", false)
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
	if _, _, _, changed, _ := Plan("/usr/local/bin/boba", false); changed {
		t.Fatal("install is not idempotent")
	}
	// uninstall removes only ours
	path, before, after, _, _ = Plan("/usr/local/bin/boba", true)
	Apply(path, before, after)
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "boba event") || !strings.Contains(string(b), "say done") {
		t.Fatalf("uninstall: %s", b)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "settings.json.boba-backup-*"))
	if len(backups) == 0 {
		t.Fatal("no backup kept")
	}
}
