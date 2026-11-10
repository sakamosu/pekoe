package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A broken config.toml is reported before the review starts (it used to
// dereference a nil config after review.start, leaving the session in
// review).
func TestRunReviewBrokenConfig(t *testing.T) {
	f, cl := startFakePekoed(t)
	home := t.TempDir()
	t.Setenv("PEKOE_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "config.toml"), []byte("[review\nsurface = "), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RunReview(cl, "api-1")
	if err == nil || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("err %v", err)
	}
	if n := len(f.find("review.start")); n != 0 {
		t.Fatalf("review.start called %d times", n)
	}
}
