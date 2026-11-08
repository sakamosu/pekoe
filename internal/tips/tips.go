// Package tips is pekoe's list of tips: one feature at a time, shown in
// the empty main pane of the TUI and in tapi chat's greeting. The list is
// tips.toml (embedded); which tips were shown is kept in paths.TipsFile()
// so a tip comes back only after every other one has been shown.
package tips

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/BurntSushi/toml"

	"pekoe/internal/paths"
)

// Tip is one entry of tips.toml. Short and Detail are Japanese (the one
// exception to English UI text, docs/ui-guidelines.md).
type Tip struct {
	ID     string   `toml:"id" json:"id"`
	Short  string   `toml:"short" json:"short"`
	Detail string   `toml:"detail" json:"detail"`
	Keys   []string `toml:"keys" json:"keys"`
	Docs   []string `toml:"docs" json:"docs"`
}

//go:embed tips.toml
var tipsTOML string

var all = mustLoad(tipsTOML)

// All is every tip in the order of tips.toml.
func All() []Tip { return slices.Clone(all) }

// ByID finds a tip.
func ByID(id string) (Tip, bool) {
	for _, t := range all {
		if t.ID == id {
			return t, true
		}
	}
	return Tip{}, false
}

func mustLoad(s string) []Tip {
	ts, err := parse(s)
	if err != nil {
		panic("tips.toml: " + err.Error())
	}
	return ts
}

func parse(s string) ([]Tip, error) {
	var f struct {
		Tip []Tip `toml:"tip"`
	}
	if _, err := toml.Decode(s, &f); err != nil {
		return nil, err
	}
	return f.Tip, nil
}

var idRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Limits on a tip's text, in terminal cells (a full-width character is 2).
const (
	ShortMax  = 56  // one line in a 60-column pane
	DetailMax = 200 // two or three lines
)

// Validate checks a list: unique ids, every field filled, text within the
// limits. width measures terminal cells (the caller's, so the package does
// not pick a width table of its own).
func Validate(ts []Tip, width func(string) int) error {
	var errs []error
	seen := map[string]bool{}
	for i, t := range ts {
		name := t.ID
		if name == "" {
			name = fmt.Sprintf("#%d", i)
		}
		switch {
		case !idRe.MatchString(t.ID):
			errs = append(errs, fmt.Errorf("%s: id must be lower-case words joined by -", name))
		case seen[t.ID]:
			errs = append(errs, fmt.Errorf("%s: duplicate id", name))
		}
		seen[t.ID] = true
		if t.Short == "" || t.Detail == "" || len(t.Keys) == 0 || len(t.Docs) == 0 {
			errs = append(errs, fmt.Errorf("%s: short, detail, keys and docs are all required", name))
		}
		if n := width(t.Short); n > ShortMax {
			errs = append(errs, fmt.Errorf("%s: short is %d cells (max %d)", name, n, ShortMax))
		}
		if n := width(t.Detail); n > DetailMax {
			errs = append(errs, fmt.Errorf("%s: detail is %d cells (max %d)", name, n, DetailMax))
		}
	}
	return errors.Join(errs...)
}

// record is the file at paths.TipsFile(): the ids shown in this round.
type record struct {
	Seen []string `json:"seen"`
}

// Shown keeps which tips were shown, in memory and (with a path) in a file
// that the TUI and `pekoe tip` share. A missing or broken file is an empty
// record; a failed write keeps the record in memory and is reported.
type Shown struct {
	path string
	seen []string
	rand func(n int) int
}

// Open reads the record at path; "" keeps it in memory only (tests).
func Open(path string) *Shown {
	s := &Shown{path: path, rand: rand.IntN}
	s.load()
	return s
}

func (s *Shown) load() {
	if s.path == "" {
		return
	}
	var r record
	if b, err := os.ReadFile(s.path); err == nil && json.Unmarshal(b, &r) == nil {
		s.seen = r.Seen
	}
}

// Next picks a random tip not shown in this round, records it and returns
// it. When every tip has been shown a new round starts, never with the tip
// shown last. The file is read again first: another TUI or tapi may have
// shown one since.
func (s *Shown) Next() (Tip, error) {
	s.load()
	known := map[string]bool{}
	for _, id := range s.seen {
		known[id] = true
	}
	var left []Tip
	for _, t := range all {
		if !known[t.ID] {
			left = append(left, t)
		}
	}
	if len(left) == 0 {
		last := ""
		if len(s.seen) > 0 {
			last = s.seen[len(s.seen)-1]
		}
		s.seen = nil
		for _, t := range all {
			if t.ID != last || len(all) == 1 {
				left = append(left, t)
			}
		}
	}
	t := left[s.rand(len(left))]
	s.seen = append(s.seen, t.ID)
	return t, s.save()
}

func (s *Shown) save() error {
	if s.path == "" {
		return nil
	}
	// ids no longer in tips.toml are dropped, so a round still ends
	var keep []string
	for _, id := range s.seen {
		if _, ok := ByID(id); ok {
			keep = append(keep, id)
		}
	}
	s.seen = keep
	b, err := json.Marshal(record{Seen: keep})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), paths.DirPerm); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, paths.FilePerm); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
