package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ArchiveName is the release archive for a platform, as .goreleaser.yaml
// names it: pekoe_<version>_<os>_<arch>.tar.gz, with _web before .tar.gz
// for the -tags web build.
func ArchiveName(tag, goos, goarch string, web bool) string {
	suffix := ""
	if web {
		suffix = "_web"
	}
	return fmt.Sprintf("pekoe_%s_%s_%s%s.tar.gz", strings.TrimPrefix(tag, "v"), goos, goarch, suffix)
}

// ChecksumsName is the release's list of SHA-256 sums.
const ChecksumsName = "checksums.txt"

// ErrNoWebBuild: this pekoe has the web UI but the release has no -tags web
// archive for the platform (releases before web archives were added).
var ErrNoWebBuild = errors.New("no web build")

// Pick chooses the archive (and checksums.txt) for goos/goarch: the web
// build when web, the default build otherwise. A web pekoe is not quietly
// given a build without its web UI: that is ErrNoWebBuild, and the caller
// decides (pekoe update --no-web).
func Pick(r *Release, goos, goarch string, web bool) (archive, sums *Asset, err error) {
	name := ArchiveName(r.TagName, goos, goarch, web)
	archive = r.Asset(name)
	if archive == nil {
		if web && r.Asset(ArchiveName(r.TagName, goos, goarch, false)) != nil {
			return nil, nil, fmt.Errorf("%w: release %s has no %s", ErrNoWebBuild, r.TagName, name)
		}
		return nil, nil, fmt.Errorf("release %s has no %s (no build for %s/%s)", r.TagName, name, goos, goarch)
	}
	sums = r.Asset(ChecksumsName)
	if sums == nil {
		return nil, nil, fmt.Errorf("release %s has no %s; not installing an archive that cannot be checked", r.TagName, ChecksumsName)
	}
	return archive, sums, nil
}

// Checksum finds name's SHA-256 (hex) in checksums.txt ("<hex>  <name>").
func Checksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not in %s", name, ChecksumsName)
}

// What Extract takes from an archive, under its top directory.
const (
	archiveBin   = "bin/pekoe"
	archiveShare = "share/pekoe/"
)

// ShareDirs are the directories next to the binary that update replaces
// (<prefix>/share/pekoe/<name>). Nothing else there is touched: with
// prefix ~/.local, share/pekoe is also pekoe's data directory.
var ShareDirs = []string{"nvim", "extension"}

// Limits on what an archive may hold.
const (
	maxFile  = 256 << 20
	maxTotal = 512 << 20
)

// Extract unpacks a release archive (tar.gz) into dst: the binary as
// dst/pekoe and the ShareDirs as dst/<name>/. Everything else (README,
// contrib/, the pekoed link) is skipped. Entries that would land outside
// dst, links and devices are refused.
func Extract(r io.Reader, dst string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("reading the archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var total int64
	haveBin := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the archive: %w", err)
		}
		name := h.Name
		if strings.Contains(name, `\`) || path.IsAbs(name) {
			return fmt.Errorf("archive entry %q: not a relative path", name)
		}
		clean := path.Clean(name)
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("archive entry %q: outside the archive", name)
		}
		// drop the top directory (wrap_in_directory)
		_, rel, ok := strings.Cut(clean, "/")
		if !ok {
			continue
		}
		var out string
		switch {
		case rel == archiveBin:
			out = "pekoe"
		case strings.HasPrefix(rel, archiveShare):
			sub := strings.TrimPrefix(rel, archiveShare)
			top, _, _ := strings.Cut(sub, "/")
			if !isShareDir(top) {
				continue
			}
			out = sub
		default:
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(out))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if h.Size > maxFile {
				return fmt.Errorf("archive entry %q: %w", name, ErrTooLarge)
			}
			if total += h.Size; total > maxTotal {
				return fmt.Errorf("archive: %w", ErrTooLarge)
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 || out == "pekoe" {
				mode = 0o755
			}
			if err := writeFile(target, tr, mode); err != nil {
				return err
			}
			if out == "pekoe" {
				haveBin = true
			}
		default:
			return fmt.Errorf("archive entry %q: not a file or a directory", name)
		}
	}
	if !haveBin {
		return fmt.Errorf("the archive has no %s", archiveBin)
	}
	return nil
}

func isShareDir(name string) bool {
	for _, d := range ShareDirs {
		if d == name {
			return true
		}
	}
	return false
}

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(r, maxFile)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
