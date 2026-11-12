// Package gitwatch takes a fingerprint of what in a repository's git
// directory names commands that git runs later — the hooks, and the
// settings in its config files that name a command (core.hooksPath,
// core.fsmonitor, alias.*, include.path, …) — so pekoed can tell a person
// when a command it ran from a worktree ([hooks] test, setup, dev …)
// changed them.
//
// pekoed's hooks sandbox does not block writing .git/hooks or .git/config:
// husky, pre-commit and the like write them from `npm install` or a setup
// step, and blocking them would break those (docs/api.md「hooks の
// sandbox」). A command an agent wrote can therefore leave a hook or a
// core.fsmonitor that the git you run yourself in the main checkout runs
// later, outside any sandbox. This package only watches: it neither undoes
// nor removes anything; a person decides.
package gitwatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Snapshot maps what is watched to a description of it:
//
//	"hooks/pre-commit"                    → "file, executable, sha256 0a1b…"
//	"config: core.hookspath"              → ".husky/_"
//	"modules/sub/config: core.fsmonitor"  → "true"
//
// Two snapshots of the same git directory are equal when nothing watched
// changed.
type Snapshot map[string]string

// Change is one entry that was added (Before ""), removed (After "") or
// changed between two snapshots.
type Change struct {
	Key    string `json:"key"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

const (
	maxEntries = 2000 // files looked at per git directory
	maxDepth   = 8    // nesting of modules/ and of directories in hooks/
	maxHash    = 16 << 20
)

// ReadConfig lists a config file as `git config --file <f> --no-includes
// -z --list` does (key NUL… is "key\nvalue\x00"). It is a variable so tests
// can run without git; pekoed sets it to its own guarded git.
var ReadConfig = func(path string) ([]byte, error) {
	return nil, fmt.Errorf("gitwatch: no config reader")
}

// Take fingerprints the git directory commonDir (the repository's own
// .git, shared by its worktrees): hooks/, the risky settings of config and
// config.worktree, each worktrees/<id>/config.worktree, and the same for
// each submodule's git directory below modules/ (nested ones included).
func Take(commonDir string) Snapshot {
	s := Snapshot{}
	n := 0
	takeGitDir(s, commonDir, "", &n, 0)
	if ents, err := os.ReadDir(filepath.Join(commonDir, "worktrees")); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				rel := "worktrees/" + e.Name() + "/config.worktree"
				takeConfig(s, filepath.Join(commonDir, filepath.FromSlash(rel)), rel)
			}
		}
	}
	return s
}

// takeGitDir adds gitDir's hooks and config under the prefix rel ("" for
// the repository), then each submodule's ("modules/x/").
func takeGitDir(s Snapshot, gitDir, rel string, n *int, depth int) {
	takeConfig(s, filepath.Join(gitDir, "config"), rel+"config")
	takeConfig(s, filepath.Join(gitDir, "config.worktree"), rel+"config.worktree")
	takeHooks(s, filepath.Join(gitDir, "hooks"), rel+"hooks", n, 0)
	for _, m := range ModuleDirs(gitDir) {
		r, err := filepath.Rel(gitDir, m)
		if err != nil {
			continue
		}
		pre := rel + filepath.ToSlash(r) + "/"
		takeConfig(s, filepath.Join(m, "config"), pre+"config")
		takeConfig(s, filepath.Join(m, "config.worktree"), pre+"config.worktree")
		takeHooks(s, filepath.Join(m, "hooks"), pre+"hooks", n, 0)
	}
}

// ModuleDirs are the git directories of gitDir's submodules below
// gitDir/modules, nested ones (modules/a/modules/b) and ones whose name has
// a slash (modules/a/b) included. Symlinks are not followed.
func ModuleDirs(gitDir string) []string {
	var out []string
	n := 0
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil || depth > maxDepth {
			return
		}
		for _, e := range ents {
			if n >= maxEntries {
				return
			}
			if !e.IsDir() {
				continue
			}
			n++
			p := filepath.Join(dir, e.Name())
			if IsGitDir(p) {
				out = append(out, p)
				walk(filepath.Join(p, "modules"), depth+1)
			} else {
				walk(p, depth+1)
			}
		}
	}
	walk(filepath.Join(gitDir, "modules"), 0)
	return out
}

// IsGitDir reports whether dir looks like a git directory (HEAD and config).
func IsGitDir(dir string) bool {
	if fi, err := os.Lstat(filepath.Join(dir, "HEAD")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	fi, err := os.Lstat(filepath.Join(dir, "config"))
	return err == nil && fi.Mode().IsRegular()
}

// takeHooks adds every entry below dir but git's own *.sample files (git
// never runs them).
func takeHooks(s Snapshot, dir, rel string, n *int, depth int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if *n >= maxEntries {
			s[rel+"/…"] = "more files than pekoe looks at"
			return
		}
		*n++
		name := e.Name()
		p := filepath.Join(dir, name)
		key := rel + "/" + name
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			t, _ := os.Readlink(p)
			s[key] = "symlink to " + t
		case fi.IsDir():
			s[key] = "directory"
			if depth < maxDepth {
				takeHooks(s, p, key, n, depth+1)
			}
		case strings.HasSuffix(name, ".sample"):
			continue
		default:
			x := "not executable"
			if fi.Mode().Perm()&0o111 != 0 {
				x = "executable"
			}
			s[key] = "file, " + x + ", sha256 " + fileHash(p)
		}
	}
}

func fileHash(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxHash)); err != nil {
		return "unreadable"
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// configs caches the risky settings of a config file by its content: the
// same file is not handed to git again (dev servers are looked at every
// few seconds).
var configs struct {
	mu sync.Mutex
	m  map[[32]byte]map[string]string
}

func takeConfig(s Snapshot, path, rel string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return // missing: nothing to run
	}
	sum := sha256.Sum256(b)
	configs.mu.Lock()
	kv, ok := configs.m[sum]
	configs.mu.Unlock()
	if !ok {
		kv = riskyConfig(path, b)
		configs.mu.Lock()
		if configs.m == nil || len(configs.m) > 256 {
			configs.m = map[[32]byte]map[string]string{}
		}
		configs.m[sum] = kv
		configs.mu.Unlock()
	}
	for k, v := range kv {
		s[rel+": "+k] = v
	}
}

// riskyConfig reads the risky settings of the config file at path (b is
// its content). A file git cannot read is kept as one entry with its hash:
// what git makes of it later is unknown.
func riskyConfig(path string, b []byte) map[string]string {
	out := map[string]string{}
	if len(bytes.TrimSpace(b)) == 0 {
		return out
	}
	list, err := ReadConfig(path)
	if err != nil {
		sum := sha256.Sum256(b)
		out["(git cannot read this file)"] = "sha256 " + hex.EncodeToString(sum[:])[:16]
		return out
	}
	vals := map[string][]string{}
	var keys []string
	for _, ent := range strings.Split(string(list), "\x00") {
		if ent == "" {
			continue
		}
		k, v, _ := strings.Cut(ent, "\n")
		if !Risky(k) {
			continue
		}
		if _, ok := vals[k]; !ok {
			keys = append(keys, k)
		}
		vals[k] = append(vals[k], v)
	}
	for _, k := range keys {
		out[k] = strings.Join(vals[k], " | ")
	}
	return out
}

// riskyExact are settings that name a command git (or a tool git starts)
// runs, or that change what git reads or where it looks for hooks.
var riskyExact = map[string]bool{
	"core.hookspath": true, "core.fsmonitor": true, "core.sshcommand": true,
	"core.editor": true, "core.pager": true, "core.askpass": true,
	"core.gitproxy": true, "core.alternaterefscommand": true, "core.worktree": true,
	"core.attributesfile": true,
	"sequence.editor":     true, "diff.external": true,
	"gpg.program": true, "credential.helper": true, "interactive.difffilter": true,
	"web.browser": true, "ssh.variant": true, "uploadpack.packobjectshook": true,
	"extensions.worktreeconfig": true, "include.path": true, "protocol.allow": true,
	"receive.procreceiverefs": true, "sendemail.sendmailcmd": true, "sendemail.smtpserver": true,
	"sendemail.tocmd": true, "sendemail.cccmd": true,
}

// riskySub are section.*.name settings (any subsection) that name a
// command; "" stands for every name in the section.
var riskySub = map[string][]string{
	"alias":      {""},
	"pager":      {""},
	"includeif":  {"path"},
	"filter":     {"clean", "smudge", "process"},
	"diff":       {"textconv", "command"},
	"merge":      {"driver"},
	"gpg":        {"program"},
	"credential": {"helper"},
	"mergetool":  {"cmd", "path"},
	"difftool":   {"cmd", "path"},
	"browser":    {"cmd", "path"},
	"man":        {"cmd", "path"},
	"remote":     {"uploadpack", "receivepack", "url", "pushurl", "vcs"},
	"submodule":  {"update"},
	"protocol":   {"allow"},
	"url":        {"insteadof", "pushinsteadof"},
	"trailer":    {"command", "cmd"},
}

// Risky reports whether a key as `git config --list` prints it (section
// and name lowercased, subsection as written) is watched.
func Risky(key string) bool {
	if riskyExact[strings.ToLower(key)] {
		return true
	}
	sec, rest, ok := strings.Cut(key, ".")
	if !ok {
		return false
	}
	sec = strings.ToLower(sec)
	names, ok := riskySub[sec]
	if !ok {
		return false
	}
	name := rest
	if i := strings.LastIndex(rest, "."); i >= 0 {
		name = rest[i+1:]
	} else if sec != "alias" && sec != "pager" {
		return false // section.name without a subsection: riskyExact's
	}
	name = strings.ToLower(name)
	for _, n := range names {
		if n == "" || n == name {
			return true
		}
	}
	return false
}

// Diff lists what differs between before and after, sorted by key.
func Diff(before, after Snapshot) []Change {
	var out []Change
	for k, a := range after {
		if b, ok := before[k]; !ok || b != a {
			out = append(out, Change{Key: k, Before: before[k], After: a})
		}
	}
	for k, b := range before {
		if _, ok := after[k]; !ok {
			out = append(out, Change{Key: k, Before: b})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Digest is a short fingerprint of the whole snapshot.
func (s Snapshot) Digest() string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, s[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Describe writes changes as lines for a person, at most max of them.
func Describe(changes []Change, max int) string {
	var b strings.Builder
	for i, c := range changes {
		if i == max {
			fmt.Fprintf(&b, "… and %d more\n", len(changes)-max)
			break
		}
		switch {
		case c.Before == "":
			fmt.Fprintf(&b, "+ %s = %s\n", c.Key, cut(c.After))
		case c.After == "":
			fmt.Fprintf(&b, "- %s (was %s)\n", c.Key, cut(c.Before))
		default:
			fmt.Fprintf(&b, "~ %s: %s → %s\n", c.Key, cut(c.Before), cut(c.After))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func cut(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:119]) + "…"
	}
	return s
}

// GitReader is ReadConfig through a function that runs git (pekoed's
// guarded git) with a timeout.
func GitReader(run func(ctx context.Context, dir string, args ...string) ([]byte, error)) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return run(ctx, filepath.Dir(path), "config", "--file", path, "--no-includes", "-z", "--list")
	}
}
