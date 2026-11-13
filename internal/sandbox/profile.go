package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pekoe/internal/paths"
)

// seatbeltArgs builds a sandbox-exec command line. Paths go in as -D
// parameters, never spliced into the profile, so no quoting is needed.
//
// Seatbelt matches the path the kernel resolved, so each path is named as
// given and with its symlinks resolved (/tmp is /private/tmp). The parent
// directories of everything denied cannot be renamed or removed either:
// moving ~/.local/share aside would bring the token out from under the
// rule (creating files in them stays allowed).
//
// lsopen and appleevent-send are denied too: through LaunchServices or
// another app (Terminal's do script) a command would run outside the
// sandbox. launchd refuses jobs from a sandboxed process by itself.
func seatbeltArgs(tool string, p Policy, argv []string) []string {
	var prof strings.Builder
	prof.WriteString("(version 1)\n(allow default)\n")
	var params []string
	n := 0
	param := func(v string) string {
		k := fmt.Sprintf("P%d", n)
		n++
		params = append(params, "-D", k+"="+v)
		return k
	}
	rule := func(op string, filter func(k string) string, list []string) {
		if len(list) == 0 {
			return
		}
		fs := make([]string, len(list))
		for i, v := range list {
			fs[i] = filter(param(v))
		}
		fmt.Fprintf(&prof, "(deny %s %s)\n", op, strings.Join(fs, " "))
	}
	subpath := func(k string) string { return `(subpath (param "` + k + `"))` }
	literal := func(k string) string { return `(literal (param "` + k + `"))` }
	rule("file-read*", subpath, variants(p.DenyRead))
	rule("file-write*", subpath, variants(append(append([]string{}, p.DenyWrite...), p.DenyRead...)))
	rule("file-write*", literal, ancestors(variants(append(append([]string{}, p.DenyWrite...), p.DenyRead...))))
	rule("network-outbound", func(k string) string { return "(remote unix-socket " + subpath(k) + ")" }, variants(p.DenyConnect))
	prof.WriteString("(deny lsopen)\n(deny appleevent-send)\n")
	out := append([]string{tool, "-p", prof.String()}, params...)
	return append(out, argv...)
}

// bwrapArgs builds a bwrap command line: the host's / as it is, then
// DenyWrite bound read-only over itself, DenyRead files covered with
// /dev/null and directories with an empty tmpfs, and the socket
// directories with an empty tmpfs.
//
// bwrap can only bind over a path that exists (it would create the mount
// point on the host, through the / it shows). For a DenyWrite or DenyRead
// path that is missing:
//
//   - a Stub for it is made first, when its parent exists (an empty
//     directory, an empty CLAUDE.md, a settings file of {}), and bound
//     like the rest
//   - otherwise its nearest existing ancestor becomes read-only, with what
//     is in that directory now (directories and files, not symlinks)
//     bound back writable: nothing new can be made in it, the missing
//     path included, and nothing in it renamed or removed. Not for / (its
//     entries are /proc and /dev) nor inside a directory already bound
//     read-only.
//
// pekoed makes its data and config directories and the tokens before it
// runs anything, so those are always there.
//
// The command gets a pid namespace and a /proc of its own, so it cannot
// read pekoed's memory or environment through /proc/<pid>, and it dies
// with pekoed.
func bwrapArgs(tool string, p Policy, argv []string, h host) []string {
	kind, real := h.kind, func(l []string) []string { return resolved(l, h.real) }
	for _, st := range p.Stubs {
		if !filepath.IsAbs(st.Path) {
			continue
		}
		st.Path = h.real(st.Path)
		if _, ok := kind(st.Path); ok {
			continue
		}
		if dir, ok := kind(filepath.Dir(st.Path)); ok && dir {
			h.create(st)
		}
	}
	writes, reads := real(p.DenyWrite), real(p.DenyRead)
	there := map[string]bool{}
	var roDirs []string
	for _, d := range writes {
		if dir, ok := kind(d); ok {
			there[d] = true
			if dir {
				roDirs = append(roDirs, d)
			}
		}
	}
	for _, d := range reads {
		if _, ok := kind(d); ok {
			there[d] = true
		}
	}
	guard := map[string]bool{}
	var guards []string
	for _, d := range append(append([]string{}, writes...), reads...) {
		if there[d] {
			continue
		}
		a := existingAncestor(d, kind)
		if a == "" || a == "/" || guard[a] || within(a, roDirs) {
			continue
		}
		guard[a] = true
		guards = append(guards, a)
	}
	// shallow first: a deeper guard goes over the entry bound back above it
	sort.Slice(guards, func(i, j int) bool {
		di, dj := strings.Count(guards[i], "/"), strings.Count(guards[j], "/")
		if di != dj {
			return di < dj
		}
		return guards[i] < guards[j]
	})
	out := []string{tool, "--dev-bind", "/", "/", "--die-with-parent", "--unshare-pid", "--proc", "/proc"}
	for _, g := range guards {
		out = append(out, "--ro-bind", g, g)
		for _, name := range h.entries(g) {
			e := filepath.Join(g, name)
			if guard[e] || there[e] {
				continue
			}
			out = append(out, "--dev-bind", e, e)
		}
	}
	for _, d := range writes {
		if there[d] {
			out = append(out, "--ro-bind", d, d)
		}
	}
	for _, d := range reads {
		if dir, ok := kind(d); ok && dir {
			out = append(out, "--tmpfs", d, "--remount-ro", d)
		} else if ok {
			out = append(out, "--ro-bind", "/dev/null", d)
		}
	}
	for _, d := range real(p.DenyConnect) {
		if dir, ok := kind(d); ok && dir {
			out = append(out, "--tmpfs", d)
		}
	}
	out = append(out, "--")
	return append(out, argv...)
}

// existingAncestor is the nearest parent directory of p that exists, or ""
// when that is not a directory (nothing can be made below it then).
func existingAncestor(p string, kind func(string) (dir, ok bool)) string {
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		if dir, ok := kind(d); ok {
			if dir {
				return d
			}
			return ""
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

// within reports whether p is one of dirs or below one.
func within(p string, dirs []string) bool {
	for _, d := range dirs {
		if p == d || strings.HasPrefix(p, strings.TrimSuffix(d, "/")+"/") {
			return true
		}
	}
	return false
}

func statKind(p string) (dir, ok bool) {
	fi, err := os.Stat(p)
	if err != nil {
		return false, false
	}
	return fi.IsDir(), true
}

// variants is each absolute path as given and with its symlinks resolved,
// without duplicates.
func variants(list []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range list {
		if !filepath.IsAbs(p) {
			continue
		}
		add(filepath.Clean(p))
		add(paths.Real(p))
	}
	return out
}

// host is what bwrapArgs asks of the file system.
type host struct {
	kind func(string) (dir, ok bool)
	real func(string) string
	// entries: the names of the directories and regular files in dir
	// (not symlinks, sockets …)
	entries func(dir string) []string
	// create makes a stub; a failure leaves the path missing (guarded)
	create func(Stub)
}

var hostFS = host{kind: statKind, real: paths.Real, entries: dirEntries, create: makeStub}

func dirEntries(dir string) []string {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, de := range des {
		if t := de.Type(); t.IsDir() || t.IsRegular() {
			out = append(out, de.Name())
		}
	}
	return out
}

// makeStub makes st, never replacing anything (O_EXCL, Mkdir).
func makeStub(st Stub) {
	if st.Dir {
		os.Mkdir(st.Path, 0o700)
		return
	}
	f, err := os.OpenFile(st.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	_, werr := f.WriteString(st.Content)
	if cerr := f.Close(); werr != nil || cerr != nil {
		os.Remove(st.Path)
	}
}

// resolved is each absolute path through real (symlinks resolved), without
// duplicates.
func resolved(list []string, real func(string) string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range list {
		if !filepath.IsAbs(p) {
			continue
		}
		r := real(p)
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// ancestors are the parent directories of the paths, up to /, without
// duplicates and without the paths themselves.
func ancestors(list []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range list {
		seen[p] = true
	}
	for _, p := range list {
		for d := filepath.Dir(p); ; d = filepath.Dir(d) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return out
}
