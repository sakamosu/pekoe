package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// transcript_path comes from a hook payload, which an agent can send
// without the token. A device that never ends or a FIFO nobody writes to
// must not hang pekoed or eat its memory; a real transcript is still read,
// up to the limit.
func TestTailFileOnlyRegularFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, dir, "/dev/zero"} {
		done := make(chan []byte, 1)
		go func() { done <- tailFile(p, 1024) }()
		select {
		case b := <-done:
			if b != nil {
				t.Errorf("%s: read %d bytes", p, len(b))
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: tailFile did not return", p)
		}
	}
	tr := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tr, []byte(strings.Repeat("x", 100)+"\n"+`{"type":"assistant","message":{"role":"assistant","content":"done"}}`+"\n"), 0o600)
	if got := lastAssistantText(tr); got != "done" {
		t.Fatalf("transcript: %q", got)
	}
	if b := tailFile(tr, 10); len(b) > 10 {
		t.Fatalf("read past the limit: %d bytes", len(b))
	}
	// a symlink to a regular file (CLAUDE_CONFIG_DIR on another disk) is fine
	ln := filepath.Join(dir, "ln.jsonl")
	os.Symlink(tr, ln)
	if got := lastAssistantText(ln); got != "done" {
		t.Fatalf("through a symlink: %q", got)
	}
}
