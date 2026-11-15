package tui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

// macOS: a real PNG that tells Launch Services itself what opens it (a
// per-file Open With, a resource fork, the Finder's alias flag, type /
// creator codes) is not handed to open: only shown where it is. The other
// Finder flags (a screenshot's hidden extension) change nothing.
func TestEvidenceMacOpensItsOwnWay(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
			t.Fatal(err)
		}
		return p
	}
	set := func(p, attr string, v []byte) {
		t.Helper()
		if err := unix.Setxattr(p, attr, v, 0); err != nil {
			t.Skipf("cannot set %s here: %v", attr, err)
		}
	}
	finder := func(typ string, flagsHi, flagsLo byte) []byte {
		b := make([]byte, 32)
		copy(b, typ)
		b[8], b[9] = flagsHi, flagsLo
		return b
	}
	plain := mk("plain.png")
	hidden := mk("screenshot.png")
	set(hidden, "com.apple.FinderInfo", finder("", 0x00, 0x10)) // kIsExtensionHidden
	for _, p := range []string{plain, hidden} {
		if it := classifyEvidence(p, ""); it.kind != evImage || !it.inline() {
			t.Errorf("%s: kind %d why %q, want an image drawn in place", filepath.Base(p), it.kind, it.why)
		}
	}
	openWith := mk("openwith.png")
	set(openWith, "com.apple.LaunchServices.OpenWith", []byte("bplist00 Terminal"))
	fork := mk("fork.png")
	set(fork, "com.apple.ResourceFork", []byte("usro"))
	alias := mk("alias-flag.png")
	set(alias, "com.apple.FinderInfo", finder("", 0x80, 0x00)) // kIsAlias
	typed := mk("typed.png")
	set(typed, "com.apple.FinderInfo", finder("APPLtrmx", 0, 0))
	for _, p := range []string{openWith, fork, alias, typed} {
		it := classifyEvidence(p, "")
		if it.kind != evFile || it.inline() || it.why == "" {
			t.Errorf("%s: kind %d inline %v why %q, want a file that is not opened", filepath.Base(p), it.kind, it.inline(), it.why)
		}
		name, args, _, err := evidenceOpener("darwin", it)
		if got := append([]string{name}, args...); err != nil || !slices.Equal(got, []string{"open", "-R", p}) {
			t.Errorf("%s would run %v (%v)", filepath.Base(p), got, err)
		}
	}
}
