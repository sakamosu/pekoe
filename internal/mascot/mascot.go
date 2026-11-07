// Package mascot draws tapi, boba's tapioca pearl (docs/mascot.md).
// tapi is a state display, not decoration: sprites are cells of
// [glyph, fg role, bg role] resolved against a palette (hi / bd / sh / eye)
// and painted with lipgloss.
package mascot

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

//go:embed tapi.json
var specJSON []byte

// Mood is one of the six faces. Higher priority wins when aggregating.
type Mood string

const (
	Error Mood = "error"
	Wait  Mood = "wait"
	Work  Mood = "work"
	Done  Mood = "done"
	Idle  Mood = "idle"
	Sleep Mood = "sleep"
)

// Moods in priority order (1 = error … 6 = sleep).
var Moods = []Mood{Error, Wait, Work, Done, Idle, Sleep}

// Priority: smaller is more urgent.
func Priority(m Mood) int {
	for i, x := range Moods {
		if x == m {
			return i + 1
		}
	}
	return len(Moods) + 1
}

// Cell is one terminal cell of a sprite. BG "" means the terminal background.
type Cell struct {
	G, FG, BG string
}

func (c *Cell) UnmarshalJSON(b []byte) error {
	var a []*string
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if len(a) != 3 || a[0] == nil || a[1] == nil {
		return fmt.Errorf("mascot: cell must be [glyph, fg, bg|null]: %s", b)
	}
	c.G, c.FG = *a[0], *a[1]
	if a[2] != nil {
		c.BG = *a[2]
	}
	return nil
}

type Sprite [][]Cell

type StateDef struct {
	Eyes   string `json:"eyes"`
	Cell   string `json:"cell"`
	Accent string `json:"accent"`
}

// Spec is the sprite definition (tapi.json, copied from the mascot lab).
type Spec struct {
	Name    string            `json:"name"`
	Shape   string            `json:"shape"`
	Light   string            `json:"light"`
	Palette map[string]string `json:"palette"`
	Sprites map[string]Sprite `json:"sprites"`
	States  map[Mood]StateDef `json:"states"`
	// Reactions: faces and lines for PR / CI changes (pr.go)
	Reactions map[PREvent]ReactionDef `json:"reactions"`
}

var spec = func() *Spec {
	s := &Spec{}
	if err := json.Unmarshal(specJSON, s); err != nil {
		panic(err)
	}
	return s
}()

// Sextant returns the sextant-block glyph for value v
// (bit1 top-left, 2 top-right, 4 mid-left, 8 mid-right, 16 bottom-left, 32 bottom-right).
func Sextant(v int) string {
	switch v {
	case 0:
		return " "
	case 21:
		return "▌"
	case 42:
		return "▐"
	case 63:
		return "█"
	}
	cp := 0x1FB00 + v - 1
	if v > 21 {
		cp--
	}
	if v > 42 {
		cp--
	}
	return string(rune(cp))
}

// ---- palettes ----

// Palette holds the four body roles, as hex.
type Palette struct {
	Hi, Bd, Sh, Eye string
}

var (
	// D ミルキー＋影: dark themes (the default)
	PaletteDark = Palette{Hi: "#f8ecd9", Bd: "#e4c49a", Sh: "#c7a57c", Eye: "#2b201c"}
	// E キャラメル: light themes
	PaletteLight = Palette{Hi: "#b9825e", Bd: "#8a5538", Sh: "#6e432c", Eye: "#f6e7cf"}
)

type accents struct{ work, wait, done, err string }

var (
	accentDark  = accents{work: "#e8a85c", wait: "#f2d06b", done: "#93d39b", err: "#ef6f78"}
	accentLight = accents{work: "#c2711c", wait: "#b08400", done: "#3a8a3f", err: "#c93a46"}
)

// ---- renderer ----

// Options come from [mascot] in config.toml plus terminal detection.
type Options struct {
	Shape   string            // round | block (resolve "auto" with DetectShape first)
	Glyphs  string            // unicode | narrow | ascii
	Light   bool              // light theme: palette E and light accents
	Colors  map[string]string // overrides: hi bd sh eye
	Dim     lipgloss.TerminalColor
	NoColor bool
}

type Renderer struct {
	shape, glyphs string
	pal           Palette
	acc           accents
	dim           lipgloss.TerminalColor
	noColor       bool
}

func New(o Options) *Renderer {
	r := &Renderer{shape: o.Shape, glyphs: o.Glyphs, pal: PaletteDark, acc: accentDark, dim: o.Dim, noColor: o.NoColor}
	if o.Light {
		r.pal, r.acc = PaletteLight, accentLight
	}
	for k, v := range o.Colors {
		if v == "" {
			continue
		}
		switch k {
		case "hi", "highlight":
			r.pal.Hi = v
		case "bd", "body":
			r.pal.Bd = v
		case "sh", "shadow":
			r.pal.Sh = v
		case "eye":
			r.pal.Eye = v
		}
	}
	if r.shape != "block" {
		r.shape = "round"
	}
	if r.glyphs != "ascii" && r.glyphs != "narrow" {
		r.glyphs = "unicode"
	}
	if r.dim == nil {
		r.dim = lipgloss.Color("#8d8b86")
	}
	return r
}

func (r *Renderer) ASCII() bool { return r.glyphs == "ascii" }

// color resolves a role: body roles and accents (w q d x i).
func (r *Renderer) color(role string) lipgloss.TerminalColor {
	switch role {
	case "hi":
		return lipgloss.Color(r.pal.Hi)
	case "bd":
		return lipgloss.Color(r.pal.Bd)
	case "sh":
		return lipgloss.Color(r.pal.Sh)
	case "eye":
		return lipgloss.Color(r.pal.Eye)
	case "w":
		return lipgloss.Color(r.acc.work)
	case "q":
		return lipgloss.Color(r.acc.wait)
	case "d":
		return lipgloss.Color(r.acc.done)
	case "x":
		return lipgloss.Color(r.acc.err)
	case "i", "dim":
		return r.dim
	}
	return lipgloss.NoColor{}
}

func (r *Renderer) paint(g, fg, bg string) string {
	if r.noColor || g == "" {
		return g
	}
	st := lipgloss.NewStyle()
	if fg != "" {
		st = st.Foreground(r.color(fg))
	}
	if bg != "" {
		st = st.Background(r.color(bg))
	}
	return st.Render(g)
}

// Paint renders s in the mood's accent color.
func (r *Renderer) Paint(m Mood, s string) string {
	return r.paint(s, spec.States[m].Accent, "")
}

// Color returns the mood's accent color.
func (r *Renderer) Color(m Mood) lipgloss.TerminalColor { return r.color(spec.States[m].Accent) }

var asciiEyes = map[Mood]string{Idle: "o o", Work: "o o", Wait: "0 0", Done: "^ ^", Error: "x x", Sleep: "- -"}
var asciiCell = map[Mood]string{Idle: ".", Work: "*", Wait: "?", Done: "*", Error: "x", Sleep: "."}

// narrowGlyph swaps ● (East Asian Ambiguous) for ⬤ (Neutral) in narrow
// mode. Fonts like Moralerspace JPDOC draw ambiguous glyphs two cells wide
// while the terminal gives them one, so ● spills into the next cell; a CPR
// can't see that, hence an explicit setting. ⬤ isn't the default because
// Menlo / SF Mono lack it and the STIX fallback is about two cells wide.
func (r *Renderer) narrowGlyph(s string) string {
	if r.glyphs == "narrow" {
		return strings.ReplaceAll(s, "●", "⬤")
	}
	return s
}

// Eyes returns the three eye cells for a mood ("● ●").
func (r *Renderer) Eyes(m Mood) string {
	if r.ASCII() {
		return asciiEyes[m]
	}
	return r.narrowGlyph(spec.States[m].Eyes)
}

// Blink is the closed-eye frame.
func (r *Renderer) Blink() string { return "- -" }

// Peek is wait's glance upward (˙ ˙); the puppy eyes in ascii.
func (r *Renderer) Peek() string {
	if r.ASCII() {
		return asciiEyes[Wait]
	}
	return "˙ ˙"
}

// CellGlyph is the plain 1-cell state symbol.
func (r *Renderer) CellGlyph(m Mood) string {
	if r.ASCII() {
		return asciiCell[m]
	}
	return r.narrowGlyph(spec.States[m].Cell)
}

// Cell renders the colored 1-cell dot (session list, statuslines).
func (r *Renderer) Cell(m Mood) string { return r.Paint(m, r.CellGlyph(m)) }

// outline maps a round sprite to the block or ascii shape by position.
func (r *Renderer) outline(sp Sprite) Sprite {
	if r.shape == "round" && !r.ASCII() {
		return sp
	}
	out := make(Sprite, len(sp))
	rows := len(sp)
	for y, row := range sp {
		out[y] = make([]Cell, len(row))
		last := len(row) - 1
		for x, c := range row {
			if strings.HasPrefix(c.G, "$") {
				out[y][x] = c
				continue
			}
			edge := x == 0 || x == last
			g := c.G
			switch {
			case rows == 1 && r.ASCII(): // pill
				g = map[bool]string{true: "(", false: ")"}[x == 0]
			case rows == 1:
				// ▐ ▌ are already quadrant blocks
			case r.ASCII() && y == 0:
				g = map[bool]string{true: ".", false: "-"}[edge]
			case r.ASCII() && y == rows-1:
				g = map[bool]string{true: "'", false: "-"}[edge]
			case r.ASCII():
				g = "|"
			case y == 0:
				g = map[bool]string{true: map[bool]string{true: "▗", false: "▖"}[x == 0], false: "▄"}[edge]
			case y == rows-1:
				g = map[bool]string{true: map[bool]string{true: "▝", false: "▘"}[x == 0], false: "▀"}[edge]
			default:
				g = map[bool]string{true: "▐", false: "▌"}[x == 0]
			}
			out[y][x] = Cell{G: g, FG: c.FG, BG: c.BG}
		}
	}
	return out
}

// draw renders a sprite with eyes substituted for $0 $1 $2.
func (r *Renderer) draw(name, eyes string) []string {
	sp := r.outline(spec.Sprites[name])
	ev := []rune(eyes)
	lines := make([]string, len(sp))
	for y, row := range sp {
		var b strings.Builder
		for _, c := range row {
			g := c.G
			if strings.HasPrefix(g, "$") {
				i := int(g[1] - '0')
				g = " "
				if i < len(ev) {
					g = string(ev[i])
				}
			}
			cbg, fg := c.BG, c.FG
			// Ghostty draws a symbol (● ◕ ⨯) up to two cells wide when a
			// space follows it, so the gap between the eyes is a solid
			// block in the face color instead of a space.
			if g == " " && cbg != "" && !r.noColor && !r.ASCII() {
				g, fg = "█", cbg
			}
			b.WriteString(r.paint(g, fg, cbg))
		}
		lines[y] = b.String()
	}
	return lines
}

// Line is the 1-line face (5 cells): status bar, prompts, toasts.
func (r *Renderer) Line(eyes string) string { return r.draw("pearl1", eyes)[0] }

// Big is the 3-line face (5×3): the empty main pane, the splash, --help.
func (r *Renderer) Big(eyes string) []string { return r.draw("pearl3", eyes) }

// Banner is the 3-line face with text lines to its right (splash, --help).
// Every row is padded to the face's width so the text column lines up.
func (r *Renderer) Banner(eyes string, text []string) []string {
	face := r.Big(eyes)
	rows := make([]string, max(len(face), len(text)))
	for i := range rows {
		rows[i] = "     "
		if i < len(face) {
			rows[i] = face[i]
		}
		if i < len(text) && text[i] != "" {
			rows[i] += "    " + text[i]
		}
	}
	return rows
}

// minTextRoom is the narrowest text column FitBanner keeps beside tapi.
const minTextRoom = 8

// FitBanner is Banner made to fit width × height cells (0 = no limit). When
// the text column doesn't fit it is clipped with "…", then dropped; when
// even tapi alone doesn't fit, only the clipped text is left. Every row is
// at most width cells, so nothing runs past the pane edge.
func (r *Renderer) FitBanner(eyes string, text []string, width, height int) []string {
	widest := func(rows []string) int {
		n := 0
		for _, l := range rows {
			n = max(n, ansi.StringWidth(l))
		}
		return n
	}
	fits := func(rows []string) bool {
		return (width <= 0 || widest(rows) <= width) && (height <= 0 || len(rows) <= height)
	}
	if rows := r.Banner(eyes, text); fits(rows) {
		return rows
	}
	bare := r.Banner(eyes, nil)
	if fits(bare) {
		// Banner puts the text 4 cells right of tapi
		if room := width - widest(bare) - 4; room >= minTextRoom {
			if rows := r.Banner(eyes, clipAll(text, room)); fits(rows) {
				return rows
			}
		}
		return bare
	}
	if width > 0 {
		text = clipAll(text, width)
	}
	if height > 0 && len(text) > height {
		text = text[:height]
	}
	return text
}

func clipAll(text []string, n int) []string {
	out := make([]string, len(text))
	for i, t := range text {
		if ansi.StringWidth(t) > n {
			t = ansi.Truncate(t, n, "…")
		}
		out[i] = t
	}
	return out
}
