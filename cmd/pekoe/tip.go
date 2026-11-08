package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"pekoe"
	"pekoe/internal/config"
	"pekoe/internal/paths"
	"pekoe/internal/tips"
)

// cmdTip prints a tip (internal/tips). Without arguments it is the next one
// not shown yet, recorded in paths.TipsFile() like the TUI's (tapi chat's
// greeting uses this). It never talks to pekoed.
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
	prefix := strings.ReplaceAll(g.Keys.Prefix, "ctrl+", "^")
	switch {
	case f["doc"] != "":
		return printDoc(os.Stdout, f["doc"])
	case f["list"] != "":
		ts := tips.All()
		if jsonOut {
			printJSON(ts)
			return nil
		}
		for _, t := range ts {
			fmt.Printf("%-16s %s\n", t.ID, withPrefix(t.Short, prefix))
		}
		return nil
	case len(pos) == 1:
		t, ok := tips.ByID(pos[0])
		if !ok {
			return fmt.Errorf("no tip %q (pekoe tip --list)", pos[0])
		}
		printTip(os.Stdout, t, prefix)
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
	t, err := tips.Open(paths.TipsFile()).Next()
	if err != nil { // still a tip: only the record failed (a sandbox, say)
		fmt.Fprintln(os.Stderr, "pekoe: tips: not recorded:", err)
	}
	if jsonOut {
		printJSON(map[string]any{"enabled": true, "tip": t})
		return nil
	}
	printTip(os.Stdout, t, prefix)
	return nil
}

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
