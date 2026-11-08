package tips

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"pekoe"
)

func TestListIsValid(t *testing.T) {
	ts := All()
	if len(ts) < 20 {
		t.Errorf("only %d tips", len(ts))
	}
	if err := Validate(ts, ansi.StringWidth); err != nil {
		t.Error(err)
	}
}

func TestValidateCatches(t *testing.T) {
	ok := Tip{ID: "a", Short: "s", Detail: "d", Keys: []string{"k"}, Docs: []string{"README.md"}}
	long := ok
	long.ID, long.Short = "long", strings.Repeat("あ", ShortMax/2+1)
	bad := ok
	bad.ID = "Bad_ID"
	empty := Tip{ID: "empty"}
	err := Validate([]Tip{ok, ok, long, bad, empty}, ansi.StringWidth)
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"a: duplicate id", "long: short is 58 cells", "Bad_ID: id must", "empty: short, detail"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

// githubSlug is GitHub's anchor for a heading: lower-cased, punctuation
// dropped, spaces to hyphens.
func githubSlug(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

var heading = regexp.MustCompile(`(?m)^#{1,6} (.+)$`)

// every docs path is a document built into the binary (pekoe tip --doc),
// and a #heading names a heading in it
func TestDocsExist(t *testing.T) {
	for _, tp := range All() {
		for _, d := range tp.Docs {
			p, anchor, _ := strings.Cut(d, "#")
			b, err := fs.ReadFile(pekoe.Docs, p)
			if err != nil {
				t.Errorf("%s: %s is not in pekoe.Docs", tp.ID, p)
				continue
			}
			if anchor == "" {
				continue
			}
			found := false
			for _, m := range heading.FindAllStringSubmatch(string(b), -1) {
				if githubSlug(m[1]) == anchor {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: no heading #%s in %s", tp.ID, anchor, p)
			}
		}
	}
}

// pekoe subcommands named in keys are in the README's command table
func TestCommandsExist(t *testing.T) {
	readme, err := fs.ReadFile(pekoe.Docs, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range All() {
		for _, k := range tp.Keys {
			rest, ok := strings.CutPrefix(k, "pekoe ")
			if !ok {
				continue
			}
			sub := strings.Fields(rest)[0]
			if !strings.Contains(string(readme), "`pekoe "+sub) {
				t.Errorf("%s: pekoe %s is not in the README's commands", tp.ID, sub)
			}
		}
	}
}

func TestNoRepeatWithinRound(t *testing.T) {
	s := Open("")
	n := len(All())
	got := map[string]bool{}
	var last string
	for i := 0; i < n; i++ {
		tp, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		if got[tp.ID] {
			t.Fatalf("%s twice in one round (after %d)", tp.ID, i)
		}
		got[tp.ID], last = true, tp.ID
	}
	// the next round starts over, but never with the tip just shown
	for i := 0; i < 50; i++ {
		s2 := &Shown{seen: append([]string(nil), s.seen...), rand: s.rand}
		tp, _ := s2.Next()
		if tp.ID == last {
			t.Fatal("a new round began with the tip shown last")
		}
		if len(s2.seen) != 1 {
			t.Fatalf("new round seen=%v", s2.seen)
		}
	}
}

func TestShownFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "share")
	path := filepath.Join(dir, "tips.json")
	a, _ := Open(path).Next()
	// another process (tapi's `pekoe tip`) reads the same record
	b := Open(path)
	for i := 0; i < len(All())-1; i++ {
		tp, err := b.Next()
		if err != nil {
			t.Fatal(err)
		}
		if tp.ID == a.ID {
			t.Fatalf("%s shown again before the round ended", a.ID)
		}
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
	// a broken file is an empty record
	os.WriteFile(path, []byte("{"), 0o600)
	if _, err := Open(path).Next(); err != nil {
		t.Fatal(err)
	}
}

// every entry of keys is one of the kinds a coverage test checks; Neovim
// and Chrome keys are checked against the documents
func TestKeysClassified(t *testing.T) {
	var docs strings.Builder
	fs.WalkDir(pekoe.Docs, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(pekoe.Docs, p)
			docs.Write(b)
		}
		return nil
	})
	for _, tp := range All() {
		for _, k := range tp.Keys {
			refs := Classify(k)
			if len(refs) == 0 {
				t.Errorf("%s: key %q names nothing", tp.ID, k)
			}
			for _, r := range refs {
				if r.Kind == KindExternal && !strings.Contains(docs.String(), r.Name) {
					t.Errorf("%s: %q is in no document", tp.ID, r.Name)
				}
			}
		}
	}
	for name, why := range NoTip {
		if c := Classify(name); len(c) != 1 || c[0].Name != name || c[0].Kind == KindExternal || c[0].Kind == KindFlag {
			t.Errorf("NoTip %q is not a normalized key, \"pekoe <sub>\" or \"[section]\"", name)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("NoTip %q has no reason", name)
		}
	}
}

func TestExpand(t *testing.T) {
	for label, want := range map[string]string{
		"j/k": "j k", "1-4": "1 2 3 4", "]a [a": "]a [a", "^q < >": "^q < ^q >", "^q h l": "^q h ^q l",
		"[ ]": "[ ]", "/": "/", "Tab": "tab", "enter": "↵", "↵": "↵", "Esc": "esc", "space": "space", "ctrl+q": "^q", "^q z": "^q z",
	} {
		if got := strings.Join(Expand(label), " "); got != want {
			t.Errorf("Expand(%q) = %q, want %q", label, got, want)
		}
	}
	if c := Classify("pekoe send <slug> -"); c[0] != (Ref{KindCommand, "pekoe send"}) {
		t.Errorf("classify: %v", c)
	}
}
