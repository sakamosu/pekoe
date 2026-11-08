package selfupdate

import (
	"runtime/debug"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Version
		ok   bool
	}{
		{"0.2.0", Version{0, 2, 0, "", 0}, true},
		{"v1.10.3", Version{1, 10, 3, "", 0}, true},
		{"0.2.0-rc.1", Version{0, 2, 0, "rc.1", 0}, true},
		{"0.1.0-59-g3761d74", Version{0, 1, 0, "", 59}, true},
		{"0.1.0-59-g3761d74-dirty", Version{0, 1, 0, "", 59}, true},
		{"0.2.0-rc.1-3-gabcdef0", Version{0, 2, 0, "rc.1", 3}, true},
		{"0.2.1-snapshot-3761d74", Version{0, 2, 1, "snapshot-3761d74", 0}, true},
		{"dev", Version{}, false},
		{"3761d74", Version{}, false},
		{"3761d74-dirty", Version{}, false},
		{"", Version{}, false},
		{"1.2", Version{}, false},
	} {
		got, ok := ParseVersion(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseVersion(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.0", 0},
		{"0.1.0", "0.2.0", -1},
		{"0.10.0", "0.9.9", 1},
		{"1.0.0", "0.99.99", 1},
		// git describe: commits past the tag are newer than the tag …
		{"0.1.0-59-g3761d74", "0.1.0", 1},
		// … but older than the next release
		{"0.1.0-59-g3761d74", "0.2.0", -1},
		{"0.2.0-1-gabcdef0", "0.2.0-3-gabcdef0", -1},
		{"0.2.0-3-gabcdef0-dirty", "0.2.0", 1},
		// prereleases (semver)
		{"0.2.0-rc.1", "0.2.0", -1},
		{"0.2.0-rc.1", "0.2.0-rc.2", -1},
		{"0.2.0-rc.10", "0.2.0-rc.9", 1},
		{"0.2.0-alpha", "0.2.0-alpha.1", -1},
		{"0.2.0-1", "0.2.0-alpha", -1},
		{"0.2.0-rc.1-3-gabcdef0", "0.2.0-rc.1", 1},
		{"0.2.0-rc.1-3-gabcdef0", "0.2.0", -1},
	} {
		a, ok1 := ParseVersion(c.a)
		b, ok2 := ParseVersion(c.b)
		if !ok1 || !ok2 {
			t.Fatalf("parse %q %q", c.a, c.b)
		}
		if got := Compare(a, b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Compare(b, a); got != -c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

func TestHasWebTag(t *testing.T) {
	for _, c := range []struct {
		tags string
		want bool
	}{{"web", true}, {"foo,web", true}, {"webx", false}, {"", false}} {
		s := []debug.BuildSetting{{Key: "GOOS", Value: "darwin"}, {Key: "-tags", Value: c.tags}}
		if got := hasWebTag(s); got != c.want {
			t.Errorf("tags %q: %v, want %v", c.tags, got, c.want)
		}
	}
	if hasWebTag([]debug.BuildSetting{{Key: "GOOS", Value: "web"}}) {
		t.Error("no -tags: not web")
	}
}
