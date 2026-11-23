package player

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"pekoe/internal/testenv"
)

// TestMain keeps the tests away from the real data directory. Started
// through a link named fake-afplay-<mode>, the test binary is a fake
// player instead (fakeMain): no sound, no afplay.
func TestMain(m *testing.M) {
	if mode, ok := strings.CutPrefix(filepath.Base(os.Args[0]), "fake-afplay-"); ok {
		os.Exit(fakeMain(mode, os.Args[1:]))
	}
	testenv.Main(m)
}

// fakeMain: ok writes its arguments and environment next to itself and
// exits 0; fail says why on stderr and exits 1; hang waits to be stopped;
// stubborn ignores SIGTERM too.
func fakeMain(mode string, args []string) int {
	switch mode {
	case "ok":
		os.WriteFile(os.Args[0]+".called", []byte(strings.Join(args, " ")+"\n"+os.Getenv("PATH")), 0o600)
		return 0
	case "fail":
		fmt.Fprintln(os.Stderr, "AudioQueueStart failed (no device)\nsecond line")
		return 1
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
		return 0
	}
	return 2
}

func fake(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "fake-afplay-"+mode)
	if err := os.Symlink(self, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlayRunsThePlayerWithTheFile(t *testing.T) {
	bin := fake(t, "ok")
	if err := (&Player{Bin: bin}).Play(context.Background(), "/data/voice/x.wav"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(bin + ".called")
	if got := string(b); got != "/data/voice/x.wav\n/usr/bin:/bin:/usr/sbin:/sbin" {
		t.Fatalf("called with %q", got)
	}
}

func TestPlayFailureSaysWhy(t *testing.T) {
	err := (&Player{Bin: fake(t, "fail")}).Play(context.Background(), "x.wav")
	if err == nil || !strings.Contains(err.Error(), "AudioQueueStart failed (no device)") || strings.Contains(err.Error(), "second line") {
		t.Fatalf("err = %v", err)
	}
}

// Stopping sends SIGTERM and returns once the player is gone.
func TestStopTerminates(t *testing.T) {
	p := &Player{Bin: fake(t, "hang"), Grace: 20 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Play(ctx, "x.wav") }()
	time.Sleep(100 * time.Millisecond) // let it start (either way it stops)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not stop the player (the 20s grace would have)")
	}
}

// A player that ignores SIGTERM gets SIGKILL after the grace.
func TestStopKillsAfterGrace(t *testing.T) {
	p := &Player{Bin: fake(t, "stubborn"), Grace: 300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Play(ctx, "x.wav") }()
	time.Sleep(300 * time.Millisecond) // signal.Ignore must have run
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("not killed")
	}
}

func TestNoPlayerIsUnsupported(t *testing.T) {
	if err := (&Player{}).Play(context.Background(), "x.wav"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if runtime.GOOS != "darwin" && New().Bin != "" {
		t.Fatal("a player off macOS")
	}
	if runtime.GOOS == "darwin" && New().Bin != Afplay {
		t.Fatalf("player %q", New().Bin)
	}
}

// silentWAV is d of silence: 8 kHz mono 16-bit PCM.
func silentWAV(t *testing.T, d time.Duration) string {
	t.Helper()
	const rate = 8000
	data := make([]byte, 2*int(d.Seconds()*rate))
	var b []byte
	le32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	le16 := func(v uint16) { b = binary.LittleEndian.AppendUint16(b, v) }
	b = append(b, "RIFF"...)
	le32(uint32(36 + len(data)))
	b = append(b, "WAVEfmt "...)
	le32(16)
	le16(1)
	le16(1)
	le32(rate)
	le32(2 * rate)
	le16(2)
	le16(16)
	b = append(b, "data"...)
	le32(uint32(len(data)))
	b = append(b, data...)
	p := filepath.Join(t.TempDir(), "silence.wav")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// slowAfplay measures this Mac's afplay itself (not Player: a hang in
// Player must still fail): a tenth of a second of silence, twice. Where it
// cannot play it (no audio device, as on a headless CI runner) or takes
// seconds for it, the real-afplay tests cannot tell Player's faults from
// the machine's; the reason says so. "" when afplay is fine.
func slowAfplay(t *testing.T, wav string) string {
	t.Helper()
	var d time.Duration
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		start := time.Now()
		out, err := exec.CommandContext(ctx, Afplay, wav).CombinedOutput()
		d = time.Since(start)
		cancel()
		if err != nil {
			return fmt.Sprintf("%s cannot play here (%v after %s: %s)", Afplay, err, d.Round(time.Millisecond), firstLine(string(out)))
		}
	}
	if d > 2*time.Second {
		return fmt.Sprintf("%s takes %s for 0.1s of sound here (limit 2s)", Afplay, d.Round(time.Millisecond))
	}
	t.Logf("%s: %s for 0.1s of silence", Afplay, d.Round(time.Millisecond))
	return ""
}

func needAfplay(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("afplay is macOS only")
	}
	if _, err := os.Stat(Afplay); err != nil {
		t.Skipf("no %s: %v", Afplay, err)
	}
	if why := slowAfplay(t, silentWAV(t, 100*time.Millisecond)); why != "" {
		t.Skip(why)
	}
}

// The real afplay plays a file (silence: nothing is heard) and stops when
// told to.
func TestRealAfplay(t *testing.T) {
	needAfplay(t)
	p := New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Play(ctx, silentWAV(t, 100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	long := silentWAV(t, 30*time.Second)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Play(ctx, long) }()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		t.Logf("stopped in %s", time.Since(start).Round(time.Millisecond))
	case <-time.After(10 * time.Second):
		t.Fatal("afplay did not stop")
	}
}
