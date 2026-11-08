package selfupdate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func release(names ...string) *Release {
	r := &Release{TagName: "v0.2.0"}
	for i, n := range names {
		r.Assets = append(r.Assets, Asset{ID: int64(i), Name: n})
	}
	return r
}

func TestPick(t *testing.T) {
	both := release("checksums.txt", "pekoe_0.2.0_darwin_arm64.tar.gz", "pekoe_0.2.0_darwin_arm64_web.tar.gz", "pekoe_0.2.0_linux_amd64.tar.gz")
	a, s, err := Pick(both, "darwin", "arm64", false)
	if err != nil || a.Name != "pekoe_0.2.0_darwin_arm64.tar.gz" || s.Name != "checksums.txt" {
		t.Fatalf("default: %v %v %v", a, s, err)
	}
	a, _, err = Pick(both, "darwin", "arm64", true)
	if err != nil || a.Name != "pekoe_0.2.0_darwin_arm64_web.tar.gz" {
		t.Fatalf("web: %v %v", a, err)
	}
	// a web pekoe is not given the default build quietly
	if _, _, err := Pick(both, "linux", "amd64", true); !errors.Is(err, ErrNoWebBuild) {
		t.Errorf("no web build: %v", err)
	}
	if _, _, err := Pick(both, "linux", "arm64", false); err == nil || errors.Is(err, ErrNoWebBuild) {
		t.Errorf("no build for the platform: %v", err)
	}
	if _, _, err := Pick(release("pekoe_0.2.0_darwin_arm64.tar.gz"), "darwin", "arm64", false); err == nil {
		t.Error("no checksums.txt: no error")
	}
}

func TestChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := []byte(strings.Repeat("cd", 32) + "  pekoe_0.2.0_darwin_arm64_web.tar.gz\n" + sum + "  pekoe_0.2.0_darwin_arm64.tar.gz\n")
	got, err := Checksum(sums, "pekoe_0.2.0_darwin_arm64.tar.gz")
	if err != nil || got != sum {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Checksum(sums, "pekoe_0.2.0_linux_amd64.tar.gz"); err == nil {
		t.Error("missing: no error")
	}
}

func TestExtract(t *testing.T) {
	dst := t.TempDir()
	if err := Extract(bytes.NewReader(releaseArchive(t, "pekoe_0.2.0_darwin_arm64", "new binary")), dst); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{
		"pekoe":                   "new binary",
		"nvim/plugin/pekoe.lua":   "-- new plugin",
		"nvim/lua/pekoe/init.lua": "-- new init",
		"extension/manifest.json": `{"new":true}`,
	} {
		b, err := os.ReadFile(filepath.Join(dst, f))
		if err != nil || string(b) != want {
			t.Errorf("%s: %q %v", f, b, err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dst, "pekoe")); fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("pekoe mode %v", fi.Mode())
	}
	// only the binary and the share dirs
	ents, _ := os.ReadDir(dst)
	if len(ents) != 3 {
		t.Errorf("extracted %v", ents)
	}
}

func TestExtractRefuses(t *testing.T) {
	for name, entries := range map[string][]entry{
		"no binary":  {{Name: "top/share/pekoe/nvim/a.lua", Body: "x"}},
		"escape":     {{Name: "top/bin/pekoe", Body: "x"}, {Name: "../evil", Body: "x"}},
		"absolute":   {{Name: "/etc/evil", Body: "x"}},
		"share link": {{Name: "top/bin/pekoe", Body: "x"}, {Name: "top/share/pekoe/nvim/x", Link: "/etc/passwd"}},
	} {
		if err := Extract(bytes.NewReader(tarGz(t, entries)), t.TempDir()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := Extract(strings.NewReader("not gzip"), t.TempDir()); err == nil {
		t.Error("not gzip: no error")
	}
}
