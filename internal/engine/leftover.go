package engine

// Leftovers: what a session's windows started and left running after the
// windows closed (docs/design.md "片付けで止めるプロセス"). An agent's
// shell commands run in a session of their own (Claude Code's Bash tool
// calls setsid), and `cmd &` there outlives the shell and the window,
// reparented to launchd / init: neither the window's session id nor its
// process tree finds it.
//
// What it keeps is the environment: tmux puts $TMUX ("<socket path>,
// <server pid>,<session>") in every pane, pekoe puts $PEKOE_SESSION=<slug>
// in every window, and children inherit both through setsid and nohup. A
// process with both, for this server's socket and this slug, was born in
// one of the session's windows. macOS does not show another process's
// environment when it runs a system binary (/bin/sh, /bin/zsh, /bin/sleep
// …: platform binaries); for those the place stands in: started in the
// session's own worktree after the session was made, in a session (setsid)
// nobody holds any more. Its leader is gone (the Bash tool call's shell
// has ended), or is itself such a process left to launchd (the agent
// ended, its tool call's shell runs on). A person's shell, editor or
// terminal holds its session: the leader is alive and is not one of these
// (a login shell has a parent; an app launchd started is not in the
// worktree, or its environment can be read), so what it started there is
// not taken.

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// LeftoverScope is the session whose leftovers KillLeftovers stops.
type LeftoverScope struct {
	Slug string
	// Dir is the session's own worktree ("": none to go by, e.g. a
	// checkout pekoe did not make, or one another session shares). Only a
	// process whose environment cannot be read is judged by it.
	Dir string
	// Since is when the session was made: a process judged by Dir must
	// have started after it.
	Since time.Time
}

// Leftover is one process KillLeftovers stopped.
type Leftover struct {
	PID     int
	Command string
}

type proc struct {
	pid, ppid int
	start     time.Time // zero: unknown
	cmd       string
}

// procInfo is what pick reads about processes beyond the table (fakes in
// tests).
type procInfo struct {
	env  func(pid int) []string          // nil: cannot be read
	cwds func(pids []int) map[int]string // working directories, where known
	sid  func(pid int) int               // session id (0: unknown)
	// gone reports whether no process pid exists (of any user)
	gone func(pid int) bool
}

// leftoverRule picks a session's leftovers from the process table.
type leftoverRule struct {
	LeftoverScope
	socket string // the tmux server's socket path, as in $TMUX
	// keepTree: these processes and all under them stay (pekoed and what
	// it runs, the root of every live pane: a new session of the same
	// slug, the other sessions). keepOnly: only these (the tmux server,
	// pekoed's own parents).
	keepTree, keepOnly []int
}

// bornIn reports whether env (NAME=value lines) is a process born in a
// window of slug on the tmux server at socket.
func bornIn(env []string, slug, socket string) bool {
	if slug == "" || socket == "" {
		return false
	}
	var sess, tmux string
	var haveSess, haveTmux bool
	for _, kv := range env { // the first of each, as getenv(3) reads them
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case k == "PEKOE_SESSION" && !haveSess:
			sess, haveSess = v, true
		case k == "TMUX" && !haveTmux:
			tmux, haveTmux = v, true
		}
	}
	sock, _, _ := strings.Cut(tmux, ",")
	return haveSess && haveTmux && sess == slug && sock == socket
}

// within reports whether path is dir or under it (dirs: dir as given and
// with its symlinks resolved).
func within(path string, dirs []string) bool {
	if path == "" {
		return false
	}
	for _, d := range dirs {
		if d != "" && (path == d || strings.HasPrefix(path, strings.TrimSuffix(d, "/")+"/")) {
			return true
		}
	}
	return false
}

// pick lists the processes of procs (pid -> proc) that the session's
// windows left behind: born in them by the environment, or (environment
// unreadable) abandoned in the worktree, and what runs under either.
func (r leftoverRule) pick(procs map[int]proc, in procInfo) []proc {
	tree := map[int]bool{}
	for _, p := range r.keepTree {
		tree[p] = true
	}
	only := map[int]bool{}
	for _, p := range r.keepOnly {
		only[p] = true
	}
	// kept: pid or one of its parents is a keepTree root
	kept := func(pid int) bool {
		for n := 0; pid > 1 && n < 4096; n++ {
			if tree[pid] {
				return true
			}
			p, ok := procs[pid]
			if !ok {
				return false
			}
			pid = p.ppid
		}
		return false
	}
	var dirs []string
	if r.Dir != "" {
		dirs = append(dirs, filepath.Clean(r.Dir))
		if real, err := filepath.EvalSymlinks(r.Dir); err == nil {
			dirs = append(dirs, real)
		}
	}
	since := r.Since.Truncate(time.Second)
	// hidden: a process judged by the place (its environment cannot be
	// read), started since the session was made
	hidden := map[int]bool{}
	match := map[int]bool{}
	for pid, p := range procs {
		if pid <= 1 || only[pid] || kept(pid) {
			continue
		}
		if env := in.env(pid); len(env) > 0 {
			if bornIn(env, r.Slug, r.socket) {
				match[pid] = true
			}
			continue
		}
		if len(dirs) > 0 && !p.start.IsZero() && !p.start.Before(since) {
			hidden[pid] = true
		}
	}
	// a hidden process counts when it runs in the worktree and its
	// session's leader is gone, or is a hidden process of init's that
	// runs in the worktree too. (A leader of another user's is alive:
	// kill(2) says EPERM, not ESRCH.)
	type cand struct{ pid, leader int }
	var cands []cand
	var look []int
	for pid := range hidden {
		l := in.sid(pid)
		switch {
		case l <= 1:
			continue
		case in.gone(l):
			cands = append(cands, cand{pid, 0})
			look = append(look, pid)
		case hidden[l] && procs[l].ppid == 1:
			cands = append(cands, cand{pid, l})
			look = append(look, pid, l)
		}
	}
	if len(cands) > 0 {
		cwd := in.cwds(look)
		for _, c := range cands {
			if within(cwd[c.pid], dirs) && (c.leader == 0 || within(cwd[c.leader], dirs)) {
				match[c.pid] = true
			}
		}
	}
	// late: p is a pane another tmux server (one a window started) opened
	// after it started: a client from outside the window asked for it (a
	// person's `tmux -L x new`, a new window there). Such a pane and what
	// runs in it are not taken, nor the server while it has one (stopping
	// it would close the pane); the panes it opened as it started are.
	late := func(p proc) bool {
		s, ok := procs[p.ppid]
		if !ok || !isTmux(s.cmd) {
			return false
		}
		return s.start.IsZero() || p.start.IsZero() || p.start.After(s.start.Add(muxSlack))
	}
	busy := map[int]bool{} // tmux servers with a late pane
	for _, p := range procs {
		if late(p) {
			busy[p.ppid] = true
		}
	}
	// under: one of pid's parents matched (a child that went elsewhere,
	// or runs a system binary outside the worktree), not through a late
	// pane
	under := func(pid int) bool {
		for n := 0; pid > 1 && n < 4096; n++ {
			p, ok := procs[pid]
			if !ok || late(p) {
				return false
			}
			if match[p.ppid] {
				return true
			}
			pid = p.ppid
		}
		return false
	}
	var out []proc
	for pid, p := range procs {
		if pid <= 1 || only[pid] || kept(pid) || busy[pid] {
			continue
		}
		if match[pid] || under(pid) {
			out = append(out, p)
		}
	}
	return out
}

// muxSlack: a pane a tmux server opened within this of its own start
// (ps shows whole seconds) came with the command that started it.
const muxSlack = time.Second

// isTmux reports whether cmd (a process's command line) runs tmux.
func isTmux(cmd string) bool {
	f, _, _ := strings.Cut(cmd, " ")
	return filepath.Base(f) == "tmux"
}

// listProcs is this user's processes: pid -> proc.
func listProcs() (map[int]proc, error) {
	c := exec.Command("ps", "-A", "-ww", "-o", "pid=,ppid=,uid=,lstart=,command=")
	c.Env = append(os.Environ(), "LC_ALL=C")
	out, err := c.Output()
	if err != nil {
		return nil, err
	}
	return parseProcs(out, os.Getuid()), nil
}

// parseProcs reads `ps -o pid=,ppid=,uid=,lstart=,command=` lines
// (lstart: "Thu Nov 20 10:01:02 2026"), keeping uid's.
func parseProcs(out []byte, uid int) map[int]proc {
	procs := map[int]proc{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		u, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil || u != uid {
			continue
		}
		start, _ := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[3:8], " "), time.Local)
		procs[pid] = proc{pid: pid, ppid: ppid, start: start, cmd: strings.Join(f[8:], " ")}
	}
	return procs
}

// ancestors is pid's parents, up to (not including) init.
func ancestors(procs map[int]proc, pid int) []int {
	var out []int
	for n := 0; n < 4096; n++ {
		p, ok := procs[pid]
		if !ok || p.ppid <= 1 {
			break
		}
		out = append(out, p.ppid)
		pid = p.ppid
	}
	return out
}

// sysInfo reads the running system.
var sysInfo = procInfo{
	env:  procEnv,
	cwds: procCwds,
	sid:  procSid,
	gone: func(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) },
}

// killLeftovers stops what find finds: SIGTERM, then SIGKILL for what is
// still there after grace. find runs again before each SIGKILL (a pid
// that went and came back as another process is no longer found), and a
// round starts again for what the stopped ones started meanwhile.
func killLeftovers(find func() ([]proc, error), grace time.Duration) ([]Leftover, error) {
	var stopped []Leftover
	seen := map[int]bool{}
	for round := 0; round < 3; round++ {
		ps, err := find()
		if err != nil || len(ps) == 0 {
			return stopped, err
		}
		these := map[int]bool{}
		for _, p := range ps {
			these[p.pid] = true
			if !seen[p.pid] {
				seen[p.pid] = true
				stopped = append(stopped, Leftover{PID: p.pid, Command: p.cmd})
			}
			_ = syscall.Kill(p.pid, syscall.SIGTERM)
			_ = syscall.Kill(p.pid, syscall.SIGCONT) // a stopped one handles TERM once it runs
		}
		for end := time.Now().Add(grace); ; {
			time.Sleep(50 * time.Millisecond)
			left, err := find()
			if err != nil {
				return stopped, err
			}
			var still []int
			for _, p := range left {
				if these[p.pid] {
					still = append(still, p.pid)
				}
			}
			if len(still) == 0 {
				break
			}
			if time.Now().After(end) {
				for _, pid := range still {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				break
			}
		}
	}
	return stopped, nil
}

// leftoverGrace is how long a leftover has to end on SIGTERM.
const leftoverGrace = 2 * time.Second

// KillLeftovers stops the processes the session's windows started that
// are still running now that its windows are closed (call it after
// KillSession): pick says which. Never pekoed and what it runs, the tmux
// server, a live window's processes, another daemon's (another socket) or
// another session's, a pane another tmux server (one a window started)
// opened after it started, and that server while it has one. A process that cleared or rewrote $TMUX /
// $PEKOE_SESSION, or (environment unreadable) left the worktree or a
// session someone holds, is not found.
func (t *Tmux) KillLeftovers(scope LeftoverScope) ([]Leftover, error) {
	out, err := t.run("display-message", "-p", "#{socket_path}\t#{pid}")
	if err != nil {
		return nil, err
	}
	sock, spid, _ := strings.Cut(strings.TrimSpace(out), "\t")
	server, _ := strconv.Atoi(spid)
	find := func() ([]proc, error) {
		// the live panes first: a window that opens after the process
		// table was read is still found under its root
		panes, err := t.run("list-panes", "-a", "-F", "#{pane_pid}")
		if err != nil {
			return nil, err
		}
		procs, err := listProcs()
		if err != nil {
			return nil, err
		}
		self := os.Getpid()
		r := leftoverRule{LeftoverScope: scope, socket: sock, keepTree: []int{self}, keepOnly: append([]int{server}, ancestors(procs, self)...)}
		for _, l := range strings.Fields(panes) {
			if pid, err := strconv.Atoi(l); err == nil {
				r.keepTree = append(r.keepTree, pid)
			}
		}
		return r.pick(procs, sysInfo), nil
	}
	return killLeftovers(find, leftoverGrace)
}
