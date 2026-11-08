package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

// entry is one file in a test archive: Link makes a symbolic link, Dir a
// directory.
type entry struct {
	Name, Body, Link string
	Dir              bool
	Mode             int64
}

func tarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.Name, Mode: 0o644, Size: int64(len(e.Body)), Typeflag: tar.TypeReg}
		if e.Mode != 0 {
			h.Mode = e.Mode
		}
		switch {
		case e.Link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.Link, 0
		case e.Dir:
			h.Typeflag, h.Size, h.Mode = tar.TypeDir, 0, 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.Body))
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// releaseArchive is an archive laid out as .goreleaser.yaml makes it.
func releaseArchive(t *testing.T, top, bin string) []byte {
	return tarGz(t, []entry{
		{Name: top + "/README.md", Body: "readme"},
		{Name: top + "/bin/pekoed", Link: "pekoe"},
		{Name: top + "/bin/pekoe", Body: bin, Mode: 0o755},
		{Name: top + "/contrib/launchd/dev.pekoe.pekoed.plist", Body: "plist"},
		{Name: top + "/share/pekoe/nvim/plugin/pekoe.lua", Body: "-- new plugin"},
		{Name: top + "/share/pekoe/nvim/lua/pekoe/init.lua", Body: "-- new init"},
		{Name: top + "/share/pekoe/extension/manifest.json", Body: `{"new":true}`},
	})
}
