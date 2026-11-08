package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWebStateDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PEKOE_HOME", root)
	t.Setenv("HOME", filepath.Join(root, "home"))
	g, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.Web.Enable || g.Web.Hostname != "pekoe" || g.Web.StateDir != filepath.Join(root, "share", "tsnet") {
		t.Fatalf("defaults: %+v", g.Web)
	}
	os.MkdirAll(filepath.Join(root, "config"), 0o755)
	os.WriteFile(filepath.Join(root, "config", "config.toml"), []byte("[web]\nstate_dir = \"~/ts\"\n"), 0o644)
	if g, err = LoadGlobal(); err != nil {
		t.Fatal(err)
	}
	if g.Web.StateDir != filepath.Join(root, "home", "ts") {
		t.Fatalf("~ not expanded: %s", g.Web.StateDir)
	}
}
