package theme

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// The focus border is a static OKLCH ramp from primary to its blue end
// (docs/ui-guidelines.md: primary の派生). It is not a new role: drawing code
// still names theme.Primary, and only box borders ask for the ramp.

// PrimaryEndKey is the [theme.colors] key for the ramp's end color.
const PrimaryEndKey = "primary_end"

// defaultPrimaryEnd is OKLCH 0.78 / 0.090 / 265: primary's lightness, a
// Serenity-like periwinkle hue, 9.1:1 against bg.
const defaultPrimaryEnd = "#9bb7f2"

type ramp struct {
	from, to oklch
	mu       sync.Mutex
	cache    map[int][]string // n → SGR foreground prefixes
}

type oklch struct{ L, C, H float64 }

// FocusRamp returns n SGR foreground sequences ("\x1b[38;2;r;g;bm") stepping
// from primary (index 0) to the ramp end (index n-1), interpolated in OKLCH.
// It returns nil when the border must stay solid primary: ansi/none mode, or
// the user overrode primary without setting primary_end.
func (t *Theme) FocusRamp(n int) []string {
	if t.ramp == nil || n <= 0 {
		return nil
	}
	r := t.ramp
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.cache[n]; ok {
		return s
	}
	cols := rampRGB(r.from, r.to, n)
	s := make([]string, n)
	for i, c := range cols {
		s[i] = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
	}
	if len(r.cache) > 64 { // terminal resizes: keep the cache bounded
		r.cache = map[int][]string{}
	}
	r.cache[n] = s
	return s
}

// newRamp returns nil if either color does not parse.
func newRamp(fromHex, toHex string) *ramp {
	a, ok1 := parseHex(fromHex)
	b, ok2 := parseHex(toHex)
	if !ok1 || !ok2 {
		return nil
	}
	return &ramp{from: toOKLCH(a), to: toOKLCH(b), cache: map[int][]string{}}
}

func rampRGB(a, b oklch, n int) [][3]uint8 {
	out := make([][3]uint8, n)
	dh := b.H - a.H // shorter arc: 340 → 265 goes through purple, not green
	if dh > 180 {
		dh -= 360
	} else if dh < -180 {
		dh += 360
	}
	for i := range out {
		f := 0.0
		if n > 1 {
			f = float64(i) / float64(n-1)
		}
		c := oklch{a.L + (b.L-a.L)*f, a.C + (b.C-a.C)*f, math.Mod(a.H+dh*f+360, 360)}
		if i == 0 {
			c = a
		} else if i == n-1 {
			c = b
		}
		out[i] = toRGB8(c)
	}
	return out
}

func parseHex(h string) ([3]float64, bool) {
	h = strings.TrimPrefix(strings.TrimSpace(h), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return [3]float64{}, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return [3]float64{}, false
	}
	return [3]float64{float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255}, true
}

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSRGB(x float64) float64 {
	if x <= 0.0031308 {
		return 12.92 * x
	}
	return 1.055*math.Pow(x, 1/2.4) - 0.055
}

func toOKLCH(rgb [3]float64) oklch {
	r, g, b := srgbToLinear(rgb[0]), srgbToLinear(rgb[1]), srgbToLinear(rgb[2])
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	L := 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
	A := 1.9779984951*l - 2.4285922050*m + 0.4505937099*s
	B := 0.0259040371*l + 0.7827717662*m - 0.8086757660*s
	return oklch{L, math.Hypot(A, B), math.Mod(math.Atan2(B, A)*180/math.Pi+360, 360)}
}

func linearRGB(c oklch) (float64, float64, float64) {
	a := c.C * math.Cos(c.H*math.Pi/180)
	b := c.C * math.Sin(c.H*math.Pi/180)
	l := math.Pow(c.L+0.3963377774*a+0.2158037573*b, 3)
	m := math.Pow(c.L-0.1055613458*a-0.0638541728*b, 3)
	s := math.Pow(c.L-0.0894841775*a-1.2914855480*b, 3)
	return 4.0767416621*l - 3.3077115913*m + 0.2309699292*s,
		-1.2684380046*l + 2.6097574011*m - 0.3413193965*s,
		-0.0041960863*l - 0.7034186147*m + 1.7076147010*s
}

func inGamut(c oklch) bool {
	r, g, b := linearRGB(c)
	const e = 1e-6
	return r >= -e && r <= 1+e && g >= -e && g <= 1+e && b >= -e && b <= 1+e
}

// toRGB8 maps to sRGB, lowering C (never L or h) until the color fits the
// gamut, as the palette procedure in the guidelines does.
func toRGB8(c oklch) [3]uint8 {
	if !inGamut(c) {
		lo, hi := 0.0, c.C
		for range 24 {
			mid := (lo + hi) / 2
			if inGamut(oklch{c.L, mid, c.H}) {
				lo = mid
			} else {
				hi = mid
			}
		}
		c.C = lo
	}
	r, g, b := linearRGB(c)
	q := func(x float64) uint8 {
		v := linearToSRGB(math.Min(math.Max(x, 0), 1))
		return uint8(math.Round(math.Min(math.Max(v, 0), 1) * 255))
	}
	return [3]uint8{q(r), q(g), q(b)}
}
