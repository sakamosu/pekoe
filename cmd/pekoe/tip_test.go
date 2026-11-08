package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/paths"
	"pekoe/internal/tips"
)

// capture runs f with stdout going to a buffer.
func capture(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	ferr := f()
	w.Close()
	os.Stdout = old
	return <-done, ferr
}

func TestTipCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PEKOE_HOME", root)
	jsonOut = false
	out, err := capture(t, func() error { return cmdTip(nil) })
	if err != nil || !strings.HasPrefix(out, "[") || !strings.Contains(out, "\nkeys: ") || !strings.Contains(out, "\ndocs: ") {
		t.Fatalf("pekoe tip: %v\n%s", err, out)
	}
	b, err := os.ReadFile(paths.TipsFile())
	if err != nil {
		t.Fatal(err)
	}
	var rec struct{ Seen []string }
	if json.Unmarshal(b, &rec) != nil || len(rec.Seen) != 1 || !strings.HasPrefix(out, "["+rec.Seen[0]+"]") {
		t.Fatalf("record %s for\n%s", b, out)
	}

	// --list, <id> and --doc show without recording
	if out, err = capture(t, func() error { return cmdTip([]string{"--list"}) }); err != nil || strings.Count(out, "\n") != len(tips.All()) {
		t.Fatalf("--list: %v\n%s", err, out)
	}
	if out, err = capture(t, func() error { return cmdTip([]string{"zoom"}) }); err != nil || !strings.HasPrefix(out, "[zoom] ^q z") {
		t.Fatalf("zoom: %v\n%s", err, out)
	}
	if out, err = capture(t, func() error { return cmdTip([]string{"--doc", "docs/tapi-chat.md#キー"}) }); err != nil || !strings.HasPrefix(out, "# tapi chat") {
		t.Fatalf("--doc: %v\n%.80s", err, out)
	}
	if _, err = capture(t, func() error { return cmdTip([]string{"--doc", "../go.mod"}) }); err == nil {
		t.Fatal("--doc outside the built-in documents")
	}
	if b2, _ := os.ReadFile(paths.TipsFile()); !bytes.Equal(b, b2) {
		t.Fatalf("recorded by a read-only form: %s", b2)
	}

	// [keys] prefix is applied; [tips] enable = false prints no tip
	os.MkdirAll(filepath.Join(root, "config"), 0o700)
	os.WriteFile(paths.GlobalConfig(), []byte("[keys]\nprefix = \"ctrl+g\"\n[tips]\nenable = false\n"), 0o600)
	if out, _ = capture(t, func() error { return cmdTip([]string{"zoom"}) }); !strings.HasPrefix(out, "[zoom] ^g z") {
		t.Fatalf("prefix: %s", out)
	}
	if out, _ = capture(t, func() error { return cmdTip(nil) }); out != "tips are off ([tips] enable = false)\n" {
		t.Fatalf("off: %q", out)
	}
}
