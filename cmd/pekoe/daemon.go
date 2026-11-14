package main

// daemon: start, stop, restart and status of pekoed.

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pekoe"
	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/skills"
)

// daemonStopTimeout bounds how long `pekoe daemon stop` waits for pekoed's
// process to exit (its shutdown closes HTTP within 1 s, then tmux and state.db).
var daemonStopTimeout = 10 * time.Second

func cmdDaemon(args []string) error {
	sub, name := "status", "daemon"
	if len(args) > 0 && !isFlag(args[0]) {
		sub, args = args[0], args[1:]
		name = "daemon " + sub
	}
	if _, ok := specs[name]; !ok {
		name = "daemon" // unknown subcommand: --help still answers
	}
	f, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return &usageError{use: specs[name].use, msg: "unexpected argument " + pos[0]}
	}
	switch sub {
	case "run":
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		d, err := daemon.New()
		if err != nil {
			return err
		}
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() { <-sig; d.Stop() }()
		signal.Ignore(syscall.SIGHUP)
		// a new pekoe brings its skills' text to an existing install
		if err := skills.Refresh(pekoe.Docs, skillDirs()); err != nil {
			log.Printf("skills: %v", err)
		}
		return d.Run()
	case "start":
		if cl, err := client.Dial(); err == nil {
			cl.Close()
			fmt.Println("pekoed is already running")
			return nil
		}
		cl := connect()
		cl.Close()
		fmt.Println("pekoed started · log:", paths.DaemonLog())
		return nil
	case "stop":
		if f["kill-agents"] != "" {
			return stopKillAgents(f["yes"] != "")
		}
		running, err := client.StopDaemon(nil, daemonStopTimeout)
		if err != nil {
			return err
		}
		if !running {
			fmt.Println("pekoed is not running")
			return nil
		}
		fmt.Println("pekoed stopped (agents keep running in tmux; they are picked up on next start)")
		return nil
	case "restart":
		return restartDaemon()
	case "status":
		cl, err := client.Dial()
		if err != nil {
			fmt.Println("pekoed is not running")
			return nil
		}
		defer cl.Close()
		var st map[string]any
		if err := cl.Call("daemon.status", nil, &st); err != nil {
			return err
		}
		if jsonOut {
			printJSON(st)
			return nil
		}
		fmt.Printf("pekoed %v · pid %v · %v sessions · socket %v · http :%v\n", st["version"], st["pid"], st["sessions"], st["socket"], st["http_port"])
		if r, ok := st["config_restart"].([]any); ok && len(r) > 0 {
			fmt.Printf("config.toml changed %v: they apply after pekoe daemon restart\n", r)
		}
		return nil
	}
	return &usageError{use: specs["daemon"].use, msg: "unknown subcommand " + sub}
}

// stopKillAgents stops pekoed and its tmux server ($PEKOE_TMUX_SOCKET), ending
// every agent on it. Meant for throwaway sandboxes; works when pekoed is
// already gone, too.
func stopKillAgents(yes bool) error {
	sock := paths.TmuxSocketName()
	if !yes {
		if !stdinIsTerminal() {
			return fmt.Errorf("--kill-agents ends every agent on tmux server %q; pass --yes to confirm", sock)
		}
		warn := ""
		if sock == "pekoe" {
			warn = " This is the default (real) server: ALL your agents will be killed."
		}
		if !confirm(fmt.Sprintf("stop pekoed and tmux server %q, killing its agents?%s", sock, warn)) {
			return nil
		}
	}
	// waits for pekoed to go, so it does not restart the server under us
	running, err := client.StopDaemon(map[string]any{"kill_agents": true}, daemonStopTimeout)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("pekoed stopped")
	} else {
		fmt.Println("pekoed is not running")
	}
	// pekoed killed the server on its way out; this catches what it could not
	// (pekoed already gone, or a server it never reached).
	tm := engine.NewTmux(sock)
	tm.PidFile = paths.TmuxPidFile(sock)
	if err := tm.KillServer(); err != nil {
		return err
	}
	fmt.Printf("tmux server %q stopped\n", sock)
	return nil
}
