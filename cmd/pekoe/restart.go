package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/client"
	"pekoe/internal/paths"
)

// daemonInfo is what restart needs from daemon.status.
type daemonInfo struct {
	Pid     int    `json:"pid"`
	Version string `json:"version"`
}

// Seams for tests: starting a pekoed (detached, or through launchd) and
// asking launchd which pid its job runs.
var (
	startDaemon  = client.Connect
	launchdStart = kickstartLaunchd
	launchdPid   = launchdJobPid
)

// launchdLabel is the job `make install-launchd` loads
// (contrib/launchd/dev.pekoe.pekoed.plist).
const launchdLabel = "dev.pekoe.pekoed"

// restartDaemon is `pekoe daemon restart`: stop pekoed, wait until it is
// gone, start it again (from the binary installed now). Agents keep running
// in tmux and are picked up, as with stop. When pekoed is not running it just
// starts one. When stopping fails or times out, it does not start.
//
// A pekoed that launchd runs is started again through launchd, so it stays
// under the job (KeepAlive). The plist relaunches only on a failed exit
// (SuccessfulExit = false) and a stopped pekoed exits 0, so launchd does not
// race us between the stop and the start; and should two start anyway, the
// lock (internal/daemon/lock.go) keeps one.
func restartDaemon() error {
	var before daemonInfo
	running := false
	if cl, err := client.Dial(); err == nil {
		running = true
		err = cl.Call("daemon.status", nil, &before)
		cl.Close()
		if err != nil {
			return err
		}
	}
	viaLaunchd := running && before.Pid > 0 && launchdPid() == before.Pid
	if running {
		// daemon.stop is for people only: an agent is refused here, before
		// anything has changed
		r, err := client.StopDaemon(nil, daemonStopTimeout)
		if err != nil {
			return err
		}
		running = r
	}
	start := startDaemon
	if viaLaunchd && running {
		start = launchdStart
	}
	cl, err := start()
	if err != nil {
		return err
	}
	defer cl.Close()
	var after daemonInfo
	if err := cl.Call("daemon.status", nil, &after); err != nil {
		return err
	}
	fmt.Println(restartLine(running, before, after, viaLaunchd && running))
	return nil
}

// restartLine reports the restart, with both versions when they differ.
func restartLine(wasRunning bool, before, after daemonInfo, viaLaunchd bool) string {
	if !wasRunning {
		return fmt.Sprintf("pekoed was not running; started · %s · log: %s", after.Version, paths.DaemonLog())
	}
	v := after.Version
	if before.Version != after.Version {
		v = before.Version + " → " + after.Version
	}
	how := ""
	if viaLaunchd {
		how = " (launchd)"
	}
	return fmt.Sprintf("pekoed restarted%s · %s · log: %s", how, v, paths.DaemonLog())
}

func launchdTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)
}

// launchdJobPid is the pid launchd runs dev.pekoe.pekoed as, or 0 (not
// macOS, the job is not loaded, or it is not running).
func launchdJobPid() int {
	if runtime.GOOS != "darwin" {
		return 0
	}
	out, err := exec.Command("launchctl", "print", launchdTarget()).Output()
	if err != nil {
		return 0
	}
	return parseLaunchdPid(out)
}

// parseLaunchdPid reads the job's "pid = N" line from `launchctl print`.
func parseLaunchdPid(out []byte) int {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "pid = "); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return 0
}

// kickstartLaunchd asks launchd to start the (stopped) job again and waits
// for the new pekoed to answer.
func kickstartLaunchd() (*client.Client, error) {
	if out, err := exec.Command("launchctl", "kickstart", launchdTarget()).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("launchctl kickstart %s: %v %s", launchdTarget(), err, strings.TrimSpace(string(out)))
	}
	return client.WaitDial(10 * time.Second)
}
