package daemon

// repos/*.toml while pekoed runs: tickLoop looks at the directory every few
// seconds (names, sizes and modification times only — no file is read
// here) and, when something changed, emits repos.updated so the TUI reads
// repo.list again (its [1] heading's watch mark comes from [watch] enable,
// issue #336). The issue watch itself (watch.go) reads the files on each
// round and needs no signal; the GUI lists the repositories each time its
// New session sheet opens.

import (
	"fmt"
	"os"
	"strings"

	"pekoe/internal/paths"
)

// repoConfFingerprint is repos/*.toml's names, sizes and modification
// times ("" when the directory is missing or empty).
func repoConfFingerprint() string {
	es, err := os.ReadDir(paths.ReposConfigDir())
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range es { // ReadDir sorts by name
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue // removed since ReadDir
		}
		fmt.Fprintf(&b, "%s %d %d\n", e.Name(), fi.Size(), fi.ModTime().UnixNano())
	}
	return b.String()
}

// checkRepoConfigs emits repos.updated when repos/*.toml changed since the
// last look (New takes the first look).
func (d *Daemon) checkRepoConfigs() {
	sig := repoConfFingerprint()
	if sig == d.repoConf {
		return
	}
	d.repoConf = sig
	d.emit("repos.updated", map[string]any{})
}
