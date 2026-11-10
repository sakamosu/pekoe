package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
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
// directories with an empty tmpfs. Paths that do not exist are left out
// (bwrap would have to create them on the host); pekoed makes its data and
// config directories and the tokens before it runs anything.
//
// The command gets a pid namespace and a /proc of its own, so it cannot
// read pekoed's memory or environment through /proc/<pid>, and it dies
// with pekoed.
func bwrapArgs(tool string, p Policy, argv []string, h host) []string {
	kind, real := h.kind, func(l []string) []string { return resolved(l, h.real) }
	out := []string{tool, "--dev-bind", "/", "/", "--die-with-parent", "--unshare-pid", "--proc", "/proc"}
	for _, d := range real(p.DenyWrite) {
		if _, ok := kind(d); ok {
			out = append(out, "--ro-bind", d, d)
		}
	}
	for _, d := range real(p.DenyRead) {
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
}

var hostFS = host{kind: statKind, real: paths.Real}

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
