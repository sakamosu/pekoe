package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"pekoe/internal/paths"
)

// ErrAlreadyRunning: another pekoed owns this data directory.
var ErrAlreadyRunning = errors.New("pekoed is already running")

// lockWait bounds how long a starting pekoed waits for the lock while its
// holder is not serving (still starting, or shutting down after
// `pekoe daemon stop`). A variable so tests can shorten it.
var lockWait = 5 * time.Second

// acquireLock takes an exclusive flock on pekoed.lock in the data directory
// and keeps it for the daemon's lifetime: one pekoed per state.db, however
// many clients autostart one at the same moment. The kernel drops the lock
// when the process dies, so a crash never leaves a stale lock behind.
//
// When the holder serves the socket, fail at once (it is running); when it
// does not, it is starting or stopping, so wait for it up to lockWait.
func acquireLock() (*os.File, error) {
	f, err := os.OpenFile(paths.LockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", paths.LockFile(), err)
		}
		if socketServes() || time.Now().After(deadline) {
			f.Close()
			return nil, ErrAlreadyRunning
		}
		time.Sleep(50 * time.Millisecond)
	}
	// informational only (the lock, not this text, decides)
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	return f, nil
}

func socketServes() bool {
	c, err := net.DialTimeout("unix", paths.Socket(), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// fileID identifies a file by device and inode, to tell "the socket I
// created" from one another daemon created later at the same path.
type fileID struct{ dev, ino uint64 }

func statID(p string) (fileID, bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return fileID{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}
	return fileID{uint64(st.Dev), uint64(st.Ino)}, true
}

// removeIfOwned removes p only while it is still the file identified by id.
func removeIfOwned(p string, id fileID) {
	if cur, ok := statID(p); ok && cur == id {
		_ = os.Remove(p)
	}
}

// removePidFileIfOwned removes the pid file only while it holds our pid.
func removePidFileIfOwned() {
	b, err := os.ReadFile(paths.PidFile())
	if err != nil {
		return
	}
	if string(b) == fmt.Sprint(os.Getpid()) {
		_ = os.Remove(paths.PidFile())
	}
}
