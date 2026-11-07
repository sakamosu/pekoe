// Package mascot draws tapi, boba's tapioca pearl (docs/mascot.md).
// The cup is boba, each pearl is an agent session. tapi is a state display,
// not decoration: sprites are cells of [glyph, fg role, bg role] resolved
// against a palette (hi / bd / sh / eye / tea) and painted with lipgloss.
package mascot

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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

// Palette holds the four body roles plus the splash tea, as hex.
type Palette struct {
	Hi, Bd, Sh, Eye, Tea string
}

var (
	// D ミルキー＋影: dark themes (the default)
	PaletteDark = Palette{Hi: "#f8ecd9", Bd: "#e4c49a", Sh: "#c7a57c", Eye: "#2b201c", Tea: "#4a2e22"}
	// E キャラメル: light themes
	PaletteLight = Palette{Hi: "#b9825e", Bd: "#8a5538", Sh: "#6e432c", Eye: "#f6e7cf", Tea: "#e4c49a"}
)

type accents struct{ work, wait, done, err, straw string }

var (
	accentDark  = accents{work: "#e8a85c", wait: "#f2d06b", done: "#93d39b", err: "#ef6f78", straw: "#f08cb0"}
	accentLight = accents{work: "#c2711c", wait: "#b08400", done: "#3a8a3f", err: "#c93a46", straw: "#d6487e"}
)

// ---- renderer ----

// Options come from [mascot] in config.toml plus terminal detection.
type Options struct {
	Shape   string            // round | block (resolve "auto" with DetectShape first)
	Glyphs  string            // unicode | ascii
	Light   bool              // light theme: palette E and light accents
	Colors  map[string]string // overrides: hi bd sh eye tea
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
		case "tea":
			r.pal.Tea = v
		}
	}
	if r.shape != "block" {
		r.shape = "round"
	}
	if r.glyphs != "ascii" {
		r.glyphs = "unicode"
	}
	if r.dim == nil {
		r.dim = lipgloss.Color("#8d8b86")
	}
	return r
}

func (r *Renderer) ASCII() bool { return r.glyphs == "ascii" }

// color resolves a role: body roles, accents (w q d x i) and straw.
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
	case "tea":
		return lipgloss.Color(r.pal.Tea)
	case "w":
		return lipgloss.Color(r.acc.work)
	case "q":
		return lipgloss.Color(r.acc.wait)
	case "d":
		return lipgloss.Color(r.acc.done)
	case "x":
		return lipgloss.Color(r.acc.err)
	case "straw":
		return lipgloss.Color(r.acc.straw)
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

// Tea paints s with the tea color as background (the cup's inside).
func (r *Renderer) Tea(s string) string { return r.paint(s, "", "tea") }

// Straw paints s in the straw color.
func (r *Renderer) Straw(s string) string { return r.paint(s, "straw", "") }

var asciiEyes = map[Mood]string{Idle: "o o", Work: "o o", Wait: "0 0", Done: "^ ^", Error: "x x", Sleep: "- -"}
var asciiCell = map[Mood]string{Idle: ".", Work: "*", Wait: "?", Done: "*", Error: "x", Sleep: "."}

// Eyes returns the three eye cells for a mood ("● ●").
func (r *Renderer) Eyes(m Mood) string {
	if r.ASCII() {
		return asciiEyes[m]
	}
	return spec.States[m].Eyes
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
	return spec.States[m].Cell
}

// Cell renders the colored 1-cell dot (session list, statuslines).
func (r *Renderer) Cell(m Mood) string { return r.Paint(m, r.CellGlyph(m)) }

// Gap is the blank cell to put right after a dot. Ghostty draws a symbol
// (● ◉ × ○) up to two cells wide when a space follows it; the braille
// blank looks empty but is not whitespace, so the dot keeps one cell.
func (r *Renderer) Gap() string {
	if r.ASCII() {
		return " "
	}
	return "\u2800"
}

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
// bg fills cells whose bg is null (the cup's tea behind a pearl).
func (r *Renderer) draw(name, eyes, bg string) []string {
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
			cbg := c.BG
			if cbg == "" {
				cbg = bg
			}
			fg := c.FG
			// Ghostty draws a symbol (● ◕ ×) up to two cells wide when a
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
func (r *Renderer) Line(eyes string) string { return r.draw("pearl1", eyes, "")[0] }

// Big is the 3-line face (5×3): only the empty main pane.
func (r *Renderer) Big(eyes string) []string { return r.draw("pearl3", eyes, "") }

// Small is the 4×3 pearl inside the splash cup; eyes are adjacent ("●●").
func (r *Renderer) Small(eyes string) []string {
	return r.draw("pearl4", strings.ReplaceAll(eyes, " ", ""), "tea")
}

// Cup draws the splash cup with one small pearl per eyes entry (max 5),
// and text lines to its right. The cup is the only place two faces meet.
func (r *Renderer) Cup(pearls []string, text []string, border func(string) string) []string {
	if len(pearls) > 5 {
		pearls = pearls[:5]
	}
	if border == nil {
		border = func(s string) string { return r.paint(s, "i", "") }
	}
	inner := max(16, 5*len(pearls)+1)
	mid := inner / 2
	h, v := "─", "│"
	tl, tr, bl, br, cap, strawTop := "╭", "╮", "╰", "╯", "┸", "╻"
	if r.ASCII() {
		h, v, tl, tr, bl, br, cap, strawTop = "-", "|", ".", ".", "'", "'", "+", "|"
	}
	// 7 rows: straw, rim, 4 rows of tea (pearls on rows 3–5)
	rows := make([]string, 7)
	rows[0] = strings.Repeat(" ", mid+1) + r.Straw(strawTop) + strings.Repeat(" ", inner-mid)
	rows[1] = border(tl+strings.Repeat(h, mid)) + r.Straw(cap) + border(strings.Repeat(h, inner-mid-1)+tr)
	var small [][]string
	for _, e := range pearls {
		small = append(small, r.Small(e))
	}
	for y := 2; y <= 5; y++ {
		var b strings.Builder
		col := 0
		if y >= 3 {
			for _, sp := range small {
				b.WriteString(r.Tea(" "))
				b.WriteString(sp[y-3])
				col += 5
			}
		}
		b.WriteString(r.Tea(strings.Repeat(" ", inner-col)))
		rows[y] = border(v) + b.String() + border(v)
	}
	rows[6] = border(bl + strings.Repeat(h, inner) + br)
	for i := range rows {
		if i-2 >= 0 && i-2 < len(text) && text[i-2] != "" {
			rows[i] += "    " + text[i-2]
		}
	}
	return rows
}
