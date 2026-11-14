package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `pekoe --help` builds tapi from the theme too (#46): [theme] mode = "none"
// leaves it colorless, truecolor paints it.
func TestHelpTapiFollowsTheme(t *testing.T) {
	for _, c := range []struct {
		mode  string
		color bool
	}{{"none", false}, {"truecolor", true}} {
		home := t.TempDir()
		t.Setenv("PEKOE_HOME", home)
		t.Setenv("PEKOE_COLOR", "")
		dir := filepath.Join(home, "config")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[theme]\nmode = \""+c.mode+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(helpTapi(), "\x1b["); got != c.color {
			t.Errorf("mode %s: colored = %v, want %v", c.mode, got, c.color)
		}
	}
}
