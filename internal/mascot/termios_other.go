//go:build !(darwin || linux || freebsd || netbsd || openbsd)

package mascot

import (
	"errors"
	"os"
	"time"
)

func rawWithTimeout(*os.File, time.Duration) (func(), error) {
	return nil, errors.New("mascot: no termios")
}
