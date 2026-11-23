package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// MaxCache is how big the WAV cache may grow before the oldest files go.
const MaxCache = 50 << 20

// Cache keeps synthesized WAV files under <data>/voice/cache, named by the
// sha256 of what made them (Key). The data directory is out of reach of
// both sandboxes, so a cached file cannot be swapped.
type Cache struct {
	Dir string
}

// Key is the cache key of one sentence: the backend's name, what its
// `version` printed, the voice, the language and the text.
func Key(backend, version, voice, lang, text string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{backend, version, voice, lang, text}, "\x00")))
	return hex.EncodeToString(h[:])
}

func (c *Cache) path(key string) string { return filepath.Join(c.Dir, key+".wav") }

// Get is the cached file of key, if there is one; it is marked as used
// now, so Prune keeps it longer.
func (c *Cache) Get(key string) (string, bool) {
	p := c.path(key)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return "", false
	}
	now := time.Now()
	os.Chtimes(p, now, now)
	return p, true
}

// Put moves src (a checked WAV under the same data directory) into the
// cache as key and returns its path.
func (c *Cache) Put(key, src string) (string, error) {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(src, 0o600); err != nil {
		return "", err
	}
	p := c.path(key)
	if err := os.Rename(src, p); err != nil {
		return "", err
	}
	return p, nil
}

// Prune removes the least recently used files until the cache is at most
// max bytes. The daemon calls it when it starts and every hour.
func (c *Cache) Prune(max int64) error {
	ents, err := os.ReadDir(c.Dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	for _, en := range ents {
		if !strings.HasSuffix(en.Name(), ".wav") {
			continue
		}
		fi, err := en.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		files = append(files, file{filepath.Join(c.Dir, en.Name()), fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	slices.SortFunc(files, func(a, b file) int { return a.mod.Compare(b.mod) })
	for _, f := range files {
		if total <= max {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
	return nil
}
