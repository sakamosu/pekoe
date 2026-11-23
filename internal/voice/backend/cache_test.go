package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeyCoversEveryPart(t *testing.T) {
	base := Key("say", "1", "tapi", "ja", "x")
	for _, k := range []string{Key("vv", "1", "tapi", "ja", "x"), Key("say", "2", "tapi", "ja", "x"), Key("say", "1", "reader", "ja", "x"), Key("say", "1", "tapi", "en", "x"), Key("say", "1", "tapi", "ja", "y"), Key("say", "1", "tap", "ija", "x")} {
		if k == base {
			t.Error("a part is not in the key")
		}
	}
}

// Prune removes the least recently used files first until the cache fits.
func TestCachePrune(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	now := time.Now()
	put := func(name string, size int, age time.Duration) string {
		p := filepath.Join(c.Dir, name+".wav")
		os.WriteFile(p, make([]byte, size), 0o600)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
		return p
	}
	oldest := put("a", 400, 3*time.Hour)
	used := put("b", 400, 2*time.Hour)
	newest := put("c", 400, time.Hour)
	other := filepath.Join(c.Dir, "notes.txt")
	os.WriteFile(other, make([]byte, 5000), 0o600)
	if _, ok := c.Get("b"); !ok { // using b makes it the newest
		t.Fatal("miss")
	}
	if err := c.Prune(500); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{oldest: false, newest: false, used: true, other: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s kept=%v, want %v", filepath.Base(p), err == nil, want)
		}
	}
	if err := (&Cache{Dir: filepath.Join(c.Dir, "none")}).Prune(1); err != nil {
		t.Fatal(err)
	}
}

func TestCacheGetIgnoresEmpty(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	os.WriteFile(filepath.Join(c.Dir, "k.wav"), nil, 0o600)
	if _, ok := c.Get("k"); ok {
		t.Fatal("an empty file is a hit")
	}
}
