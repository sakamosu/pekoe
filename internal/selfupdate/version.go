// Package selfupdate replaces the running pekoe with a GitHub release
// (`pekoe update`): it finds the release, picks the archive for this
// platform and build (default or -tags web), checks it against
// checksums.txt and swaps the binary and the files next to it
// (<prefix>/share/pekoe/nvim and extension), undoing what it did when a
// step fails. The archive layout is .goreleaser.yaml's.
package selfupdate

import (
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

// Version is a pekoe version as `pekoe version` shows it: a release
// (0.2.0, from the tag v0.2.0), a release candidate (0.2.0-rc.1), or a
// build from source, which `git describe` names after the last tag
// (0.1.0-59-g3761d74: 59 commits past v0.1.0, so newer than 0.1.0).
type Version struct {
	Major, Minor, Patch int
	Pre                 string // semver prerelease ("rc.1"); "" for a release
	Ahead               int    // commits past the tag (git describe)
}

var (
	coreRe     = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)
	describeRe = regexp.MustCompile(`-(\d+)-g[0-9a-f]{4,}$`)
)

// ParseVersion reads "0.2.0", "v0.2.0", "0.2.0-rc.1" and git describe's
// "0.1.0-59-g3761d74" (with or without "-dirty"). ok is false for what has
// no version in it ("dev", a bare commit hash, "").
func ParseVersion(s string) (v Version, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s = strings.TrimSuffix(s, "-dirty")
	// git describe's "-N-gHASH" comes last; read it first, or it would be
	// taken for a prerelease (which sorts before the release instead of after)
	if m := describeRe.FindStringSubmatchIndex(s); m != nil {
		n, err := strconv.Atoi(s[m[2]:m[3]])
		if err != nil {
			return Version{}, false
		}
		v.Ahead = n
		s = s[:m[0]]
	}
	m := coreRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	v.Pre = m[4]
	return v, true
}

// Compare returns -1, 0 or 1 as a is older than, the same as, or newer
// than b. A prerelease is older than its release (semver); commits past a
// tag are newer than the tag.
func Compare(a, b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if c := cmpInt(d[0], d[1]); c != 0 {
			return c
		}
	}
	if c := comparePre(a.Pre, b.Pre); c != 0 {
		return c
	}
	return cmpInt(a.Ahead, b.Ahead)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// comparePre orders prereleases as semver does: none > any; identifiers
// one by one, numbers numerically and before words; a shorter list first.
func comparePre(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, y := as[i], bs[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if c := cmpInt(xn, yn); c != 0 {
				return c
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(as), len(bs))
}

// BuiltWithWeb reports whether this binary was built with -tags web (the
// web UI, docs/web.md), from the build settings Go records in it.
func BuiltWithWeb() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	return hasWebTag(bi.Settings)
}

func hasWebTag(settings []debug.BuildSetting) bool {
	for _, s := range settings {
		if s.Key != "-tags" {
			continue
		}
		for _, t := range strings.Split(s.Value, ",") {
			if strings.TrimSpace(t) == "web" {
				return true
			}
		}
	}
	return false
}
