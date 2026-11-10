package main

// report / reports / wait (docs/orchestration.md): a session reports its
// work, the hub agent reads the reports and waits for its sessions.

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/state"
	"pekoe/internal/untrusted"
)

func cmdReport(args []string) error {
	f, pos, err := parseCmd("report", args)
	if err != nil {
		return err
	}
	if len(pos) == 0 || f["status"] == "" {
		return &usageError{use: specs["report"].use, msg: "summary and --status required"}
	}
	summary := strings.Join(pos, " ")
	if summary == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return err
		}
		summary = string(b)
	}
	cl := connect()
	defer cl.Close()
	p := map[string]any{"summary": summary, "status": f["status"], "evidence": multiVal(f, "evidence")}
	if f["verdict"] != "" {
		p["verdict"] = f["verdict"]
	}
	if f["reviewed-sha"] != "" {
		p["reviewed_sha"] = f["reviewed-sha"]
	}
	var r state.Report
	if err := cl.Call("report.post", p, &r); err != nil {
		return err
	}
	if jsonOut {
		printJSON(r)
		return nil
	}
	fmt.Printf("report #%d on %s (%s)\n", r.ID, r.Slug, r.Status)
	return nil
}

func cmdReports(args []string) error {
	f, pos, err := parseCmd("reports", args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return &usageError{use: specs["reports"].use, msg: "unexpected argument " + pos[1]}
	}
	p := map[string]any{}
	if len(pos) == 1 {
		p["slug"] = pos[0]
	}
	if f["limit"] != "" {
		n, err := strconv.Atoi(f["limit"])
		if err != nil || n < 1 {
			return &usageError{use: specs["reports"].use, msg: "--limit must be a number from 1"}
		}
		p["limit"] = n
	}
	cl := connect()
	defer cl.Close()
	var rs []state.Report
	if err := cl.Call("report.list", p, &rs); err != nil {
		return err
	}
	if jsonOut {
		printJSON(rs)
		return nil
	}
	if len(rs) == 0 {
		fmt.Println("no reports")
		return nil
	}
	for _, r := range rs {
		head := fmt.Sprintf("#%d %s %s %s", r.ID, r.Slug, r.Status, r.At.Local().Format("01-02 15:04"))
		if r.Verdict != "" {
			head += " verdict=" + r.Verdict + " reviewed=" + r.ReviewedSHA
		}
		if r.Head != "" {
			head += " head=" + short(r.Head)
			if r.Dirty {
				head += "+dirty"
			}
		}
		if r.Claimed != "" {
			head += fmt.Sprintf(" ⚠ claimed %s (by %s)", r.Claimed, r.By)
		}
		fmt.Println(head)
		// the agent's words: fenced, so they cannot pose as pekoe's
		fmt.Println(untrusted.Fence("text", r.Summary))
		for _, e := range r.Evidence {
			fmt.Println("  evidence: " + untrusted.Line(e))
		}
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}

type waitOut struct {
	Cursor  string `json:"cursor"`
	Epoch   string `json:"epoch"`
	Changes []struct {
		Seq     int64     `json:"seq"`
		Kind    string    `json:"kind"`
		Slug    string    `json:"slug"`
		At      time.Time `json:"at"`
		Summary string    `json:"summary"`
	} `json:"changes"`
	TimedOut bool `json:"timed_out,omitempty"`
	Reset    bool `json:"reset,omitempty"`
}

// waitTimeout reads --timeout: a duration (30m, 90s) or seconds; an hour
// at most, 30 minutes when not given.
func waitTimeout(s string) (time.Duration, error) {
	if s == "" {
		return 30 * time.Minute, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		n, nerr := strconv.Atoi(s)
		if nerr != nil {
			return 0, fmt.Errorf("--timeout: %q is not a duration (30m, 90s)", s)
		}
		d = time.Duration(n) * time.Second
	}
	if d < time.Second || d > time.Hour {
		return 0, fmt.Errorf("--timeout must be from 1s to 1h")
	}
	return d, nil
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func cmdWait(args []string) error {
	f, pos, err := parseCmd("wait", args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return &usageError{use: specs["wait"].use, msg: "unexpected argument " + pos[0]}
	}
	timeout, err := waitTimeout(f["timeout"])
	if err != nil {
		return &usageError{use: specs["wait"].use, msg: err.Error()}
	}
	p := map[string]any{"mine": f["mine"] != "", "slugs": splitList(f["slug"]), "on": splitList(f["on"]), "since": f["since"], "timeout_s": int(timeout / time.Second)}
	cl := connect()
	defer cl.Close()
	var w waitOut
	if err := cl.CallTimeout("wait", p, &w, timeout+time.Minute); err != nil {
		return err
	}
	if jsonOut {
		printJSON(w)
		return nil
	}
	for _, c := range w.Changes {
		fmt.Printf("%s %s\n", c.At.Local().Format("15:04:05"), c.Summary)
	}
	switch {
	case w.Reset:
		fmt.Println("reset: changes before this were lost (pekoed restarted?); read `pekoe ls` again")
	case w.TimedOut:
		fmt.Println("timed out: nothing happened")
	}
	fmt.Println("cursor: " + w.Cursor)
	return nil
}
