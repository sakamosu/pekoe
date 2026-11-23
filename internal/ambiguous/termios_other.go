//go:build !(darwin || linux || freebsd || netbsd || openbsd)

package ambiguous

import (
	"errors"
	"os"
	"time"
)

func rawWithTimeout(*os.File, time.Duration) (func(), error) {
	return nil, errors.New("ambiguous: no termios")
}
