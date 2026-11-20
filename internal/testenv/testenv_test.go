package testenv

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) { Main(m) }

// TestStoppedChild is the test binary that TestStoppedRemovesTemp stops: it
// starts a process that keeps writing into its t.TempDir (as a headless
// Chrome does into its profile), notes its directories and that process,
// and waits.
func TestStoppedChild(t *testing.T) {
	out := os.Getenv("TESTENV_STOPPED_OUT")
	if out == "" {
		return // run by TestStoppedRemovesTemp
	}
	dir := t.TempDir()
	w := exec.Command("sh", "-c", `while :; do mkdir -p "$0/w" && : >"$0/w/f"; sleep 0.01; done`, dir)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "w", "f")); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the writer never wrote")
		}
	}
	// the writer, Main's directory (PEKOE_HOME is in it) and the t.TempDir
	note := fmt.Sprintf("%d\n%s\n%s\n", w.Process.Pid, filepath.Dir(os.Getenv("PEKOE_HOME")), dir)
	if err := os.WriteFile(out+".tmp", []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Rename(out+".tmp", out)
	time.Sleep(time.Hour) // until the parent stops it
}

// A test binary stopped by SIGTERM (make test interrupted) stops what its
// tests started, removes Main's directory and the t.TempDir ones in it, and
// says which tests were running.
func TestStoppedRemovesTemp(t *testing.T) {
	out := filepath.Join(t.TempDir(), "note")
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoppedChild$")
	cmd.Env = append(os.Environ(), "TESTENV_STOPPED_OUT="+out)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	var b []byte
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var err error
		if b, err = os.ReadFile(out); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the child test never got going: %s", stderr.String())
		}
	}
	note := strings.Fields(string(b))
	writer, _ := strconv.Atoi(note[0])
	t.Cleanup(func() { syscall.Kill(writer, syscall.SIGKILL) })
	cmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var err error
	select {
	case err = <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the child did not exit after SIGTERM")
	}
	if err == nil {
		t.Error("the stopped child exited 0")
	}
	// gone once its exit has been noticed (it was the child's child)
	for end := time.Now().Add(30 * time.Second); syscall.Kill(writer, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the writer the test started still runs")
		}
	}
	for _, p := range note[1:] {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind (stat: %v)", p, err)
		}
	}
	if s := stderr.String(); !strings.Contains(s, "terminated while running TestStoppedChild; removing the temporary directories") {
		t.Errorf("output: %q", s)
	}
}

// The ShortDir directories are removed with Main's (the stopped case), and
// forgotten once their test removed them.
func TestShortDirRemovedWhenStopped(t *testing.T) {
	var d string
	t.Run("short", func(t *testing.T) {
		d = ShortDir(t, "pkst")
		removeAll(t.TempDir())
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind (stat: %v)", d, err)
		}
	})
	shortDirs.Lock()
	defer shortDirs.Unlock()
	if shortDirs.m[d] {
		t.Errorf("%s still noted after its test", d)
	}
}
