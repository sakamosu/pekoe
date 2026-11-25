package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"pekoe/internal/paths"
)

// hasMessagesWithin runs claudeHasMessages, failing when it does not
// return in time (a FIFO nobody writes to blocks the open).
func hasMessagesWithin(t *testing.T, p string) bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() { done <- claudeHasMessages(p) }()
	select {
	case v := <-done:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: claudeHasMessages did not return", p)
		return false
	}
}

// ~/.claude/projects is writable from the hooks' sandbox: a FIFO, a device
// or a huge file there must not hang pekoed or eat its memory (open,
// restart, tapi, and restore before pekoed accepts a connection all read
// these transcripts).
func TestClaudeHasMessagesBounded(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if hasMessagesWithin(t, fifo) {
		t.Errorf("a FIFO counted as a conversation")
	}
	zero := filepath.Join(dir, "zero.jsonl")
	os.Symlink("/dev/zero", zero)
	if hasMessagesWithin(t, zero) {
		t.Errorf("/dev/zero counted as a conversation")
	}

	// a sparse file far bigger than the cap: read up to the cap only. Too
	// long to look into counts as a conversation, so that
	// tapiDropEmptyTranscripts does not remove it.
	big := filepath.Join(dir, "big.jsonl")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(512 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := hasMessagesWithin(t, big)
	runtime.ReadMemStats(&after)
	if !got {
		t.Errorf("a line too long to look into: not a conversation")
	}
	if a := after.TotalAlloc - before.TotalAlloc; a > 4*transcriptScanMax {
		t.Errorf("allocated %d MiB for a sparse file", a>>20)
	}

	// more than the cap without a message: cut off, so a conversation
	many := filepath.Join(dir, "many.jsonl")
	os.WriteFile(many, []byte(strings.Repeat(queueOnly, transcriptScanMax/len(queueOnly)+1)), 0o600)
	if !hasMessagesWithin(t, many) {
		t.Errorf("cut off at the cap: not a conversation")
	}
	// a long first line (a pasted image) under the cap is still read
	long := filepath.Join(dir, "long.jsonl")
	os.WriteFile(long, []byte(`{"type":"user","message":{"role":"user","content":"`+strings.Repeat("x", 512<<10)+`"}}`+"\n"), 0o600)
	if !hasMessagesWithin(t, long) {
		t.Errorf("a long message line: not a conversation")
	}
	// and a short file with no message is still none
	empty := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(empty, []byte(queueOnly), 0o600)
	if hasMessagesWithin(t, empty) {
		t.Errorf("queue-operation lines counted as a conversation")
	}
}

// pastClaudeSessions lists regular files only: a symlink is not followed,
// even to a real transcript, and a FIFO is skipped without blocking.
func TestPastClaudeSessionsRegularOnly(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "app")
	proj := claudeProjectDir(path)
	os.MkdirAll(proj, 0o755)
	real := filepath.Join(t.TempDir(), "real.jsonl")
	os.WriteFile(real, []byte(convLine), 0o600)
	os.Symlink(real, filepath.Join(proj, "linked.jsonl"))
	if err := syscall.Mkfifo(filepath.Join(proj, "fifo.jsonl"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(proj, "conv-1.jsonl"), []byte(convLine), 0o600)
	done := make(chan []pastSession, 1)
	go func() { done <- pastClaudeSessions(path) }()
	select {
	case ps := <-done:
		if len(ps) != 1 || ps[0].ID != "conv-1" {
			t.Fatalf("past = %+v, want conv-1 only", ps)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pastClaudeSessions did not return")
	}
}

// tapi reset removes transcripts with no message, but not one too long to
// look into: it may hold a conversation.
func TestTapiDropKeepsTooLongTranscript(t *testing.T) {
	setup(t)
	proj := claudeProjectDir(paths.TapiDir())
	os.MkdirAll(proj, 0o755)
	long := filepath.Join(proj, "long.jsonl")
	os.WriteFile(long, []byte(strings.Repeat("x", transcriptLineMax+1)+"\n"+convLine), 0o600)
	empty := filepath.Join(proj, "empty.jsonl")
	os.WriteFile(empty, []byte(queueOnly), 0o600)
	tapiDropEmptyTranscripts()
	if _, err := os.Stat(long); err != nil {
		t.Errorf("removed a transcript too long to look into: %v", err)
	}
	if _, err := os.Stat(empty); err == nil {
		t.Errorf("kept a transcript with no message")
	}
}
