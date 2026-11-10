//go:build !darwin && !linux

package daemon

import (
	"errors"
	"net"
)

var errNoPeer = errors.New("telling the caller's process is not supported on this system")

func sockPeerPID(net.Conn) (int, error) { return 0, errNoPeer }
func parentPID(int) (int, error)        { return 0, errNoPeer }
