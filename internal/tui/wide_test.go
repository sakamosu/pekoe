package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/width"

	"pekoe/internal/ambiguous"
	"pekoe/internal/config"
	"pekoe/internal/glyphguard"
	"pekoe/internal/theme"
)

// wideTable draws with the wide table ([theme] ambiguous = "wide") until
// the test ends.
func wideTable(t *testing.T) {
	t.Helper()
	was := ambiguous.IsWide()
	ambiguous.SetWide(true)
	t.Cleanup(func() { ambiguous.SetWide(was) })
}

// wideUses is every line of a drawn screen, for glyphguard.CheckWide.
func wideUses(where, out string) []glyphguard.Use {
	var uses []glyphguard.Use
	for i, l := range strings.Split(ansi.Strip(out), "\n") {
		uses = append(uses, glyphguard.Use{Where: where + " line " + strconv.Itoa(i), Glyph: l})
	}
	return uses
}

// With the wide table nothing pekoe draws itself is ambiguous-width: the
// icons (ascii), tapi (ascii), the frames (+ - |), the body symbols
// (... -> <- x) and fit's ellipsis (docs/ambiguous-width.md「段階 4 で
// 決めたこと」). The tutorial walks every sample screen (sessions, the
// dashboard, the inbox, the PR view, review …); the main screen, the help
// and the narrow layout come on top.
func TestWideScreens(t *testing.T) {
	wideTable(t)
	var uses []glyphguard.Use
	for _, size := range [][2]int{{150, 45}, {100, 30}, {80, 24}} {
		for _, lang := range []string{tourJa, tourEn} {
			m := tourModel(t, "truecolor", size[0], size[1])
			m.g.Mascot = config.Mascot{} // auto: ascii with the wide table
			m.buildTapi()
			if !m.tapi.r.ASCII() || m.ic.Name != "ascii" {
				t.Fatalf("wide table: tapi ascii %v, icons %q", m.tapi.r.ASCII(), m.ic.Name)
			}
			m.tourLang = lang
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			for seen := 0; m.tour != nil && seen < 50; seen++ {
				out := m.View()
				keepsSize(t, m, out)
				uses = append(uses, wideUses(lang+" "+tourStepTitle(m), out)...)
				m.Update(key("enter"))
			}
		}
	}
	m := forecastModel(t, "truecolor")
	m.ic = theme.Icons(theme.IconsName(""))
	uses = append(uses, wideUses("main", m.View())...)
	m.Update(key("?"))
	uses = append(uses, wideUses("help", m.View())...)
	m.Update(key("esc"))
	m.width = 80
	uses = append(uses, wideUses("narrow", m.View())...)
	m.width = 30 // fit cuts nearly everything: its ellipsis shows
	uses = append(uses, wideUses("cut", m.View())...)
	glyphguard.CheckWide(t, uses)
}

// fit's ellipsis is part of the wide table: it cuts with "..." there.
func TestWideFit(t *testing.T) {
	if got := fit("abcdefgh", 6); got != "abcde…" {
		t.Fatalf("default table: %q", got)
	}
	wideTable(t)
	if got := fit("abcdefgh", 6); got != "abc..." {
		t.Errorf("wide table: fit = %q, want abc...", got)
	}
}

// wideLiteralExceptions are literals with an ambiguous-width glyph that do
// not go through tx where they are written, with where they do instead.
var wideLiteralExceptions = map[string]string{
	"barFull":      "a const: tx where the bars are drawn",
	"barEmpty":     "a const: tx where the bars are drawn",
	"tapiNeedsYou": "a const: tx where the toast and the bar show it",
	"noRepoHint":   "a const: tx where it is shown",
	"sparks":       "a package var: spark runs tx on the line",
	"workBubbles":  "tapi's bubbles: ascii (. o O) with the wide table, like the rest of tapi",
	"tourSteps":    "the tutorial's copy: tourStep.text runs tx on it",
	"placeholder":  "kitty's image placeholder: the terminal draws the image in its cells, not the glyph",
}

// Every literal in the TUI with an ambiguous-width glyph goes through tx,
// so the wide table replaces it, and every such glyph is in the table: a
// new · or ─ written without tx fails here (docs/ambiguous-width.md「段階
// 4 で決めたこと」). Literals the TUI compares with or searches for are not
// drawn and may stay; none has an ambiguous glyph today.
func TestWideLiteralsWrapped(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	table := ambiguous.Table()
	fs := token.NewFileSet()
	var bad []string
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fs, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		wrapped := map[*ast.BasicLit]bool{}
		excepted := map[*ast.BasicLit]string{}
		markAll := func(n ast.Node, name string) {
			ast.Inspect(n, func(x ast.Node) bool {
				if bl, ok := x.(*ast.BasicLit); ok {
					excepted[bl] = name
				}
				return true
			})
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, sp := range gd.Specs {
				if vs, ok := sp.(*ast.ValueSpec); ok {
					for _, n := range vs.Names {
						markAll(vs, n.Name)
					}
				}
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "tx" && len(c.Args) == 1 {
					if bl, ok := c.Args[0].(*ast.BasicLit); ok {
						wrapped[bl] = true
					}
				}
			}
			return true
		})
		ast.Inspect(af, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING && bl.Kind != token.CHAR {
				return true
			}
			s := bl.Value
			if bl.Kind == token.STRING {
				s, _ = strconv.Unquote(s)
			} else {
				r, _, _, _ := strconv.UnquoteChar(s[1:len(s)-1], '\'')
				s = string(r)
			}
			if !utf8.ValidString(s) {
				return true // bytes (a file's magic number), not text
			}
			pos := fs.Position(bl.Pos())
			at := pos.Filename + ":" + strconv.Itoa(pos.Line)
			name, isExc := excepted[bl]
			isExc = isExc && wideLiteralExceptions[name] != ""
			amb := false
			for _, r := range s {
				if r < 0x80 || width.LookupRune(r).Kind() != width.EastAsianAmbiguous {
					continue
				}
				amb = true
				if _, ok := table[r]; !ok && !isExc {
					bad = append(bad, at+": "+strconv.QuoteRune(r)+" is not in internal/ambiguous's table")
				}
			}
			if !amb || wrapped[bl] {
				return true
			}
			if isExc {
				used[name] = true
				return true
			}
			bad = append(bad, at+": "+bl.Value+" does not go through tx")
			return true
		})
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("ambiguous-width glyphs the wide table would not replace:\n  %s", strings.Join(bad, "\n  "))
	}
	for name, why := range wideLiteralExceptions {
		if !used[name] {
			t.Errorf("wideLiteralExceptions: %s (%s) has no ambiguous literal any more; remove it", name, why)
		}
	}
}

// What cuts to n cells with an ellipsis stays within n cells when the
// ellipsis is three ("..."): the status bar's path (clipLeft) and the
// progress / last-tool texts (truncRunes) count it.
func TestWideCutsKeepWidth(t *testing.T) {
	long := "~/worktrees/pekoe/feat-ambiguous-width-setting"
	for _, wide := range []bool{false, true} {
		was := ambiguous.IsWide()
		ambiguous.SetWide(wide)
		for n := 0; n <= 12; n++ {
			if got := clipLeft(long, n); w(got) > n {
				t.Errorf("wide=%v clipLeft(%d) = %q, %d cells", wide, n, got, w(got))
			}
			if got := truncRunes(long, n); len([]rune(got)) > n {
				t.Errorf("wide=%v truncRunes(%d) = %q, %d runes", wide, n, got, len([]rune(got)))
			}
		}
		if got := clipLeft(long, 10); !strings.HasSuffix(got, "setting") || w(got) != 10 {
			t.Errorf("wide=%v clipLeft keeps the tail: %q", wide, got)
		}
		ambiguous.SetWide(was)
	}
	if got := truncRunes("abcdefghijklmnop", 8); got != "abcdefg…" {
		t.Errorf("default table: %q", got)
	}
	wideTable(t)
	if got := truncRunes("abcdefghijklmnop", 8); got != "abcde..." {
		t.Errorf("wide table: %q", got)
	}
}
