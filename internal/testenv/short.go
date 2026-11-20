package testenv

import (
	"os"
	"testing"
)

// maxBase is the longest temporary directory ShortDir uses as is: a unix
// socket path is about 104 bytes at most on macOS, and the tests put
// "<prefix><10 digits>/<a socket name>" (some 30 bytes) under it.
const maxBase = 60

// ShortDir makes a directory with a short path, for unix sockets (tmux's
// TMUX_TMPDIR, a daemon's $PEKOE_SOCK), and removes it after the test. It
// is under $TMPDIR when that is short — an agent's sandbox lets tests write
// nowhere else — and under /tmp otherwise (macOS's /var/folders/… one).
// Under Main, that is the TMPDIR from before Main moved it into its own
// longer directory; Main removes these too when the binary is stopped.
func ShortDir(t testing.TB, prefix string) string {
	t.Helper()
	d, err := shortDir(prefix)
	if err != nil {
		t.Fatal(err)
	}
	shortDirs.Lock()
	shortDirs.m[d] = true
	shortDirs.Unlock()
	t.Cleanup(func() {
		os.RemoveAll(d)
		shortDirs.Lock()
		delete(shortDirs.m, d)
		shortDirs.Unlock()
	})
	return d
}

func shortDir(prefix string) (string, error) {
	base := shortBase
	if base == "" {
		base = os.TempDir()
	}
	if len(base) > maxBase {
		base = "/tmp"
	}
	return os.MkdirTemp(base, prefix)
}
