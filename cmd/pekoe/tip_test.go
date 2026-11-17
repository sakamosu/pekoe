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

// With a pekoed, `pekoe tip` takes the tip pekoed picked and recorded
// (tips.next): tapi chat's sandbox cannot write tips.json itself. pekoed's
// [tips] enable wins (the sandbox may hide config.toml from the CLI), and a
// pekoed without tips.next leaves it to the CLI as before.
func TestTipAsksPekoed(t *testing.T) {
	fd := startFakeDaemon(t)
	jsonOut = false
	zoom, _ := tips.ByID("zoom")
	set := func(res any, e string) {
		fd.mu.Lock()
		fd.results = map[string]any{"tips.next": res}
		fd.errs = map[string]string{"tips.next": e}
		fd.mu.Unlock()
	}
	set(map[string]any{"enabled": true, "tip": zoom}, "")
	out, err := capture(t, func() error { return cmdTip(nil) })
	if err != nil || !strings.HasPrefix(out, "[zoom] ^q z") {
		t.Fatalf("pekoe tip: %v\n%s", err, out)
	}
	if c := fd.calls(); len(c) != 1 || c[0] != "tips.next" {
		t.Fatalf("calls %v", c)
	}
	if _, err := os.Stat(paths.TipsFile()); !os.IsNotExist(err) {
		t.Fatalf("the CLI wrote tips.json itself: %v", err)
	}

	set(map[string]any{"enabled": false}, "")
	if out, _ = capture(t, func() error { return cmdTip(nil) }); out != "tips are off ([tips] enable = false)\n" {
		t.Fatalf("off: %q", out)
	}

	set(nil, "unknown method: tips.next")
	out, err = capture(t, func() error { return cmdTip(nil) })
	b, _ := os.ReadFile(paths.TipsFile())
	var rec struct{ Seen []string }
	if err != nil || json.Unmarshal(b, &rec) != nil || len(rec.Seen) != 1 || !strings.HasPrefix(out, "["+rec.Seen[0]+"]") {
		t.Fatalf("an older pekoed: %v\n%s\nrecord %s", err, out, b)
	}
}

// In an agent's sandbox (tapi chat) config.toml is hidden from the CLI,
// which then reads the default ctrl+q: the tips write ^q as [keys] prefix
// as pekoed read it (tips.next's prefix, tips.prefix for --list and <id>).
// An older pekoed that does not say leaves the CLI's own reading.
func TestTipPrefixFromPekoed(t *testing.T) {
	fd := startFakeDaemon(t)
	jsonOut = false
	zoom, _ := tips.ByID("zoom")
	set := func(next, prefix any) {
		fd.mu.Lock()
		fd.results = map[string]any{"tips.next": next, "tips.prefix": prefix}
		fd.mu.Unlock()
	}
	set(map[string]any{"enabled": true, "tip": zoom, "prefix": "ctrl+a"}, map[string]any{"prefix": "ctrl+a"})
	if out, _ := capture(t, func() error { return cmdTip(nil) }); !strings.HasPrefix(out, "[zoom] ^a z") {
		t.Fatalf("pekoe tip: %s", out)
	}
	if out, _ := capture(t, func() error { return cmdTip([]string{"zoom"}) }); !strings.HasPrefix(out, "[zoom] ^a z") {
		t.Fatalf("pekoe tip zoom: %s", out)
	}
	if out, _ := capture(t, func() error { return cmdTip([]string{"--list"}) }); !strings.Contains(out, "^a z") || strings.Contains(out, "^q") {
		t.Fatalf("pekoe tip --list: %s", out)
	}

	set(map[string]any{"enabled": true, "tip": zoom}, nil) // an older pekoed
	if out, _ := capture(t, func() error { return cmdTip(nil) }); !strings.HasPrefix(out, "[zoom] ^q z") {
		t.Fatalf("older pekoed, pekoe tip: %s", out)
	}
	if out, _ := capture(t, func() error { return cmdTip([]string{"zoom"}) }); !strings.HasPrefix(out, "[zoom] ^q z") {
		t.Fatalf("older pekoed, pekoe tip zoom: %s", out)
	}
}
