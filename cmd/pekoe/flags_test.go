package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"pekoe/internal/client"
	"pekoe/internal/testenv"
)

// fakeDaemon answers every request on $PEKOE_SOCK with a null result and
// records the methods, so a test can see what a command would have done.
// Being reachable, it also keeps client.Connect from starting a pekoed.
type fakeDaemon struct {
	mu      sync.Mutex
	methods []string
	results map[string]any    // method -> result (default null)
	errs    map[string]string // method -> error
}

// note records something that is not a request (a test's stub), in order.
func (f *fakeDaemon) note(s string) {
	f.mu.Lock()
	f.methods = append(f.methods, s)
	f.mu.Unlock()
}

func (f *fakeDaemon) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

func (f *fakeDaemon) reset() {
	f.mu.Lock()
	f.methods = nil
	f.mu.Unlock()
}

func startFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	t.Setenv("PEKOE_HOME", t.TempDir())
	t.Setenv("PEKOE_TMUX_SOCKET", "pekoe-test-unused")
	// in memory: an agent's sandbox refuses binding a unix socket
	sock := filepath.Join(t.TempDir(), "s")
	t.Setenv("PEKOE_SOCK", sock)
	ln := testenv.Listen(sock)
	t.Cleanup(func() { ln.Close() })
	oldDial := client.DialSocket
	client.DialSocket = ln.Dial
	t.Cleanup(func() { client.DialSocket = oldDial })
	fd := &fakeDaemon{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					fd.mu.Lock()
					fd.methods = append(fd.methods, req.Method)
					resp := map[string]any{"id": req.ID, "result": fd.results[req.Method]}
					if e := fd.errs[req.Method]; e != "" {
						resp["error"] = e
					}
					fd.mu.Unlock()
					b, _ := json.Marshal(resp)
					c.Write(append(b, '\n'))
				}
			}()
		}
	}()
	return fd
}

// dispatch runs a subcommand as main would, without exiting.
func dispatch(args ...string) error {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "daemon":
		return cmdDaemon(rest)
	case "rm":
		return cmdRm(rest)
	case "land":
		return cmdSimple("land", "land", rest)
	case "dev":
		return cmdSimple("dev", "dev.toggle", rest)
	case "send":
		return cmdSend(rest)
	case "new":
		return cmdNew(rest)
	case "open":
		return cmdOpen(rest)
	case "adopt":
		return cmdAdopt(rest)
	case "init":
		return cmdInit(rest)
	case "undo":
		return cmdUndo(rest)
	case "answer":
		return cmdAnswer(rest)
	case "attach":
		return cmdAttach(rest)
	case "hooks":
		return cmdHooks(rest)
	case "ls":
		return cmdLs(rest)
	case "issues":
		return cmdIssues(rest)
	case "conflicts":
		return cmdConflicts(rest)
	case "cleanup":
		return cmdCleanup(rest)
	case "review":
		return cmdReview(rest)
	case "tip":
		return cmdTip(rest)
	case "web":
		if _, _, err := parseCmd("web", rest); err != nil {
			return err
		}
		return cmdWeb()
	}
	panic("dispatch: " + cmd)
}

// --help / -h print the usage and do nothing: above all for commands that
// change state (`daemon stop --help` used to stop the real daemon).
func TestHelpDoesNothing(t *testing.T) {
	fd := startFakeDaemon(t)
	for _, args := range [][]string{
		{"daemon", "stop", "--help"},
		{"daemon", "stop", "-h"},
		{"daemon", "stop", "--kill-agents", "--yes", "--help"},
		{"daemon", "--help"},
		{"daemon", "start", "--help"},
		{"daemon", "run", "--help"},
		{"daemon", "restart", "--help"},
		{"daemon", "restart", "-h"},
		{"rm", "feat-x", "--yes", "--force", "--help"},
		{"rm", "-h", "feat-x"},
		{"cleanup", "--rm", "feat-x", "--yes", "--force", "--help"},
		{"land", "feat-x", "--help"},
		{"dev", "feat-x", "-h"},
		{"send", "feat-x", "hello", "--help"},
		{"new", "feat/x", "--prompt", "go", "--help"},
		{"open", "--new", "-h"},
		{"adopt", "--yes", "--help"},
		{"init", "--yes", "--help"},
		{"undo", "feat-x", "--help"},
		{"answer", "feat-x", "allow", "-h"},
		{"attach", "feat-x", "--help"},
		{"hooks", "install", "--yes", "--help"},
		{"ls", "--help"},
		{"issues", "--help"},
		{"conflicts", "-h"},
		{"review", "--help"},
		{"web", "--help"},
		{"tip", "--help"},
	} {
		fd.reset()
		err := dispatch(args...)
		if !helpRequested(err) {
			t.Errorf("%v: err = %v, want the usage", args, err)
		}
		if c := fd.calls(); len(c) > 0 {
			t.Errorf("%v: called pekoed: %v", args, c)
		}
	}
}

// Unknown flags are refused before anything happens, with the usage.
func TestUnknownFlagRefused(t *testing.T) {
	fd := startFakeDaemon(t)
	for _, args := range [][]string{
		{"daemon", "stop", "--kill-agent"},
		{"daemon", "stop", "--bogus"},
		{"daemon", "stop", "extra"},
		{"daemon", "restart", "--kill-agents"}, // restart keeps the agents
		{"daemon", "restart", "extra"},
		{"daemon", "--bogus"},
		{"daemon", "frobnicate"},
		{"rm", "feat-x", "--forse"},
		{"rm", "feat-x", "-f"},
		{"rm", "feat-x", "--yes=1"},
		{"land", "feat-x", "--yes"},
		{"send", "feat-x", "--bogus", "hello"},
		{"new", "feat/x", "--repo"},           // value missing
		{"new", "feat/x", "--promt", "hello"}, // typo must not eat "hello" into a flag
		{"undo", "feat-x", "--n", "2"},
		{"hooks", "install", "--force"},
		{"ls", "--mine"},
		{"issues", "--label"},
		{"tip", "--doc"},
		{"tip", "zoom", "extra"},
	} {
		fd.reset()
		err := dispatch(args...)
		var ue *usageError
		if !errors.As(err, &ue) || ue.help || ue.use == "" {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
		if c := fd.calls(); len(c) > 0 {
			t.Errorf("%v: called pekoed: %v", args, c)
		}
	}
}

// The right usage still reaches pekoed.
func TestDaemonStopStillStops(t *testing.T) {
	fd := startFakeDaemon(t)
	if err := dispatch("daemon", "stop"); err != nil {
		t.Fatal(err)
	}
	if c := fd.calls(); !reflect.DeepEqual(c, []string{"daemon.status", "daemon.stop"}) {
		t.Fatalf("calls = %v", c)
	}
}

func TestParseFlags(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		args []string
		f    map[string]string
		pos  []string
	}{
		{"new", []string{"feat/x", "--repo", "app", "--prompt", "fix it"}, map[string]string{"repo": "app", "prompt": "fix it"}, []string{"feat/x"}},
		{"new", []string{"--repo=app", "feat/x", "fix", "it"}, map[string]string{"repo": "app"}, []string{"feat/x", "fix", "it"}},
		{"new", []string{"feat/x", "--prompt", "-v is broken"}, map[string]string{"prompt": "-v is broken"}, []string{"feat/x"}},
		{"ls", []string{"--all"}, map[string]string{"all": "true"}, nil},
		{"rm", []string{"feat-x", "--force", "--yes"}, map[string]string{"force": "true", "yes": "true"}, []string{"feat-x"}},
		{"send", []string{"feat-x", "-"}, map[string]string{}, []string{"feat-x", "-"}},
		{"send", []string{"feat-x", "--no-enter", "--", "--help", "-x"}, map[string]string{"no-enter": "true"}, []string{"feat-x", "--help", "-x"}},
		{"send", []string{"feat-x", "- item"}, map[string]string{}, []string{"feat-x", "- item"}},
		{"undo", []string{"feat-x", "-1"}, map[string]string{}, []string{"feat-x", "-1"}},
		{"issues", []string{"12", "--repo", "app", "--refresh"}, map[string]string{"repo": "app", "refresh": "true"}, []string{"12"}},
		{"daemon stop", []string{"--kill-agents", "--yes"}, map[string]string{"kill-agents": "true", "yes": "true"}, nil},
	} {
		f, pos, err := parseCmd(c.cmd, c.args)
		if err != nil {
			t.Errorf("%s %v: %v", c.cmd, c.args, err)
			continue
		}
		if !reflect.DeepEqual(f, c.f) || !reflect.DeepEqual(pos, c.pos) {
			t.Errorf("%s %v: f=%v pos=%q, want f=%v pos=%q", c.cmd, c.args, f, pos, c.f, c.pos)
		}
	}
}

// --json is global: before or after the subcommand, but not after "--".
func TestSplitGlobal(t *testing.T) {
	for _, c := range []struct {
		in   []string
		rest []string
		json bool
	}{
		{[]string{"--json", "ls", "--all"}, []string{"ls", "--all"}, true},
		{[]string{"ls", "--json", "--repo", "app"}, []string{"ls", "--repo", "app"}, true},
		{[]string{"daemon", "status", "--json"}, []string{"daemon", "status"}, true},
		{[]string{"send", "x", "--", "--json"}, []string{"send", "x", "--", "--json"}, false},
		{[]string{"ls"}, []string{"ls"}, false},
	} {
		rest, j := splitGlobal(c.in)
		if !reflect.DeepEqual(rest, c.rest) || j != c.json {
			t.Errorf("%v: rest=%v json=%v", c.in, rest, j)
		}
	}
}

// Every subcommand main dispatches has a declared spec.
func TestEverySubcommandHasSpec(t *testing.T) {
	for _, name := range []string{"init", "new", "open", "adopt", "ls", "issues", "conflicts", "cleanup", "send", "event", "review", "land", "rm", "dev", "undo", "answer", "attach", "hooks", "ext", "web", "mcp", "daemon", "daemon start", "daemon stop", "daemon restart", "daemon status", "daemon run", "tapi", "tapi reset", "tip", "skills", "report", "reports", "wait", "ask-human", "inbox", "inbox answer", "inbox withdraw", "inbox dismiss", "ledger", "ledger done", "ledger drop", "ledger close", "ledger link", "ledger reopen", "ledger rm", "stats"} {
		if _, ok := specs[name]; !ok {
			t.Errorf("no spec for %q", name)
		}
	}
}

// --evidence may be given more than once; other value flags keep the last.
func TestMultiValueFlag(t *testing.T) {
	f, pos, err := parseCmd("report", []string{"fixed it", "--status", "done", "--evidence", "https://x/pull/1", "--evidence=/tmp/a.png"})
	if err != nil {
		t.Fatal(err)
	}
	if got := multiVal(f, "evidence"); len(got) != 2 || got[0] != "https://x/pull/1" || got[1] != "/tmp/a.png" {
		t.Fatalf("evidence: %q", got)
	}
	if len(pos) != 1 || f["status"] != "done" {
		t.Fatalf("pos %q status %q", pos, f["status"])
	}
	if got := multiVal(map[string]string{}, "evidence"); got != nil {
		t.Fatalf("none: %q", got)
	}
}

func TestWaitTimeout(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 30 * time.Minute, "90s": 90 * time.Second, "120": 2 * time.Minute, "1h": time.Hour} {
		if got, err := waitTimeout(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"2h", "0", "x"} {
		if _, err := waitTimeout(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
