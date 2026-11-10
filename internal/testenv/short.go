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
func ShortDir(t testing.TB, prefix string) string {
	t.Helper()
	d, err := shortDir(prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func shortDir(prefix string) (string, error) {
	base := os.TempDir()
	if len(base) > maxBase {
		base = "/tmp"
	}
	return os.MkdirTemp(base, prefix)
}
