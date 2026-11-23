// Package player plays the WAV files of pekoe voice (docs/voice.md 9):
// /usr/bin/afplay, never looked up in PATH, stopped with SIGTERM and, when
// it has not ended a second later, SIGKILL. Where there is no afplay (any
// OS but macOS) Play says so and plays nothing.
package player

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Afplay is macOS's player. Never looked up in PATH.
const Afplay = "/usr/bin/afplay"

// Grace is how long a stopped player gets between SIGTERM and SIGKILL.
const Grace = time.Second

// ErrUnsupported: there is no player on this OS (pekoe voice is macOS
// only).
var ErrUnsupported = errors.New("pekoe voice plays sound on macOS only")

// Player plays one WAV file at a time per call; it is safe for concurrent
// use (the speech queue plays one at a time anyway). It satisfies the
// speech queue's Play(ctx, wavPath string) error.
type Player struct {
	// Bin is the player's absolute path; "" plays nothing (ErrUnsupported).
	Bin string
	// Grace between SIGTERM and SIGKILL (0 is Grace).
	Grace time.Duration
}

// New is the player of this OS: afplay on macOS, none elsewhere.
func New() *Player {
	if runtime.GOOS != "darwin" {
		return &Player{}
	}
	return &Player{Bin: Afplay}
}

// Play plays wav and returns when the sound ends. Cancelling ctx stops it
// (SIGTERM to its process group, SIGKILL after Grace) and returns once the
// process is gone, with ctx's error.
func (p *Player) Play(ctx context.Context, wav string) error {
	if p.Bin == "" {
		return ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	grace := p.Grace
	if grace <= 0 {
		grace = Grace
	}
	cmd := exec.Command(p.Bin, wav)
	// the system's directories only: afplay needs nothing else, and
	// nothing an agent could write is searched
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	var stderr strings.Builder
	cmd.Stderr = &limited{b: &stderr, max: 1024}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = grace
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", p.Bin, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if l := firstLine(stderr.String()); l != "" {
				return fmt.Errorf("%s: %s", p.Bin, l)
			}
			return fmt.Errorf("%s: %w", p.Bin, err)
		}
		return nil
	case <-ctx.Done():
	}
	pid := cmd.Process.Pid
	syscall.Kill(-pid, syscall.SIGTERM)
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		syscall.Kill(-pid, syscall.SIGKILL)
		<-done
	}
	return ctx.Err()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// limited keeps the first max bytes written to it.
type limited struct {
	b   *strings.Builder
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}
