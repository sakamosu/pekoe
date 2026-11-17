package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"pekoe"
	"pekoe/internal/client"
	"pekoe/internal/config"
	"pekoe/internal/paths"
	"pekoe/internal/tips"
)

// cmdTip prints a tip (internal/tips). Without arguments it is the next one
// not shown yet, recorded in paths.TipsFile() like the TUI's (tapi chat's
// greeting uses this): pekoed picks and records it (tips.next), since an
// agent's sandbox cannot write pekoe's data directory; without a pekoed
// (or an older one) the CLI does it itself. It never starts pekoed.
func cmdTip(args []string) error {
	f, pos, err := parseCmd("tip", args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return &usageError{use: specs["tip"].use, msg: "unexpected argument " + pos[1]}
	}
	g, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	switch {
	case f["doc"] != "":
		return printDoc(os.Stdout, f["doc"])
	case f["list"] != "":
		ts := tips.All()
		if jsonOut {
			printJSON(ts)
			return nil
		}
		prefix := tipPrefix(g)
		for _, t := range ts {
			fmt.Printf("%-16s %s\n", t.ID, withPrefix(t.Short, prefix))
		}
		return nil
	case len(pos) == 1:
		t, ok := tips.ByID(pos[0])
		if !ok {
			return fmt.Errorf("no tip %q (pekoe tip --list)", pos[0])
		}
		printTip(os.Stdout, t, tipPrefix(g))
		return nil
	}
	if !g.TipsOn() {
		if jsonOut {
			printJSON(map[string]any{"enabled": false})
			return nil
		}
		fmt.Println("tips are off ([tips] enable = false)")
		return nil
	}
	t, prefix, on := nextTip()
	if !on { // pekoed read config.toml, which a sandbox may hide from us
		if jsonOut {
			printJSON(map[string]any{"enabled": false})
			return nil
		}
		fmt.Println("tips are off ([tips] enable = false)")
		return nil
	}
	if jsonOut {
		printJSON(map[string]any{"enabled": true, "tip": t})
		return nil
	}
	if prefix == "" { // an older pekoed, or none
		prefix = g.Keys.Prefix
	}
	printTip(os.Stdout, t, shortPrefix(prefix))
	return nil
}

// nextTip asks pekoed for the next tip (tips.next) and falls back to
// recording it here when pekoed cannot be reached or does not know the
// method. on is false when pekoed says [tips] enable = false. prefix is
// [keys] prefix as pekoed read it ("" when it did not say).
func nextTip() (t tips.Tip, prefix string, on bool) {
	if cl, err := client.Dial(); err == nil {
		defer cl.Close()
		var r struct {
			Enabled bool      `json:"enabled"`
			Tip     *tips.Tip `json:"tip"`
			Prefix  string    `json:"prefix"`
		}
		err := cl.CallTimeout("tips.next", nil, &r, 5*time.Second)
		switch {
		case err == nil && !r.Enabled:
			return tips.Tip{}, "", false
		case err == nil && r.Tip != nil:
			return *r.Tip, r.Prefix, true
		}
	}
	t, err := tips.Open(paths.TipsFile()).Next()
	if err != nil { // still a tip: only the record failed (a sandbox, say)
		fmt.Fprintln(os.Stderr, "pekoe: tips: not recorded:", err)
	}
	return t, "", true
}

// tipPrefix is [keys] prefix as the tips should show it (^a for ctrl+a).
// pekoed's reading comes first (tips.prefix): an agent's sandbox (tapi
// chat's) hides config.toml from this process, which then reads the
// default; without a pekoed (or an older one) it is g's.
func tipPrefix(g *config.Global) string {
	if cl, err := client.Dial(); err == nil {
		defer cl.Close()
		var r struct {
			Prefix string `json:"prefix"`
		}
		if cl.CallTimeout("tips.prefix", nil, &r, 5*time.Second) == nil && r.Prefix != "" {
			return shortPrefix(r.Prefix)
		}
	}
	return shortPrefix(g.Keys.Prefix)
}

// shortPrefix writes a key the way the tips do: ctrl+a is ^a.
func shortPrefix(p string) string { return strings.ReplaceAll(p, "ctrl+", "^") }

func withPrefix(s, prefix string) string {
	if prefix == "" || prefix == "^q" {
		return s
	}
	return strings.ReplaceAll(s, "^q", prefix)
}

func printTip(w io.Writer, t tips.Tip, prefix string) {
	fmt.Fprintf(w, "[%s] %s\n%s\n", t.ID, withPrefix(t.Short, prefix), withPrefix(t.Detail, prefix))
	keys := make([]string, len(t.Keys))
	for i, k := range t.Keys {
		keys[i] = withPrefix(k, prefix)
	}
	fmt.Fprintf(w, "keys: %s\n", strings.Join(keys, " · "))
	fmt.Fprintf(w, "docs: %s (pekoe tip --doc <path>)\n", strings.Join(t.Docs, " · "))
}

// printDoc prints one of the documents built into the binary (README.md,
// docs/*.md …); a "#heading" after the path is ignored.
func printDoc(w io.Writer, p string) error {
	p, _, _ = strings.Cut(p, "#")
	p = path.Clean(strings.TrimPrefix(p, "./"))
	b, err := fs.ReadFile(pekoe.Docs, p)
	if err != nil {
		var names []string
		_ = fs.WalkDir(pekoe.Docs, ".", func(n string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				names = append(names, n)
			}
			return nil
		})
		return fmt.Errorf("no document %s (have: %s)", p, strings.Join(names, ", "))
	}
	_, err = w.Write(b)
	return err
}
