package theme

import (
	"fmt"
	"math"
	"testing"

	"boba/internal/config"
)

func sgr(hex string) string {
	c, _ := parseHex(hex)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", int(math.Round(c[0]*255)), int(math.Round(c[1]*255)), int(math.Round(c[2]*255)))
}

func relLum(hex string) float64 {
	c, _ := parseHex(hex)
	return 0.2126*srgbToLinear(c[0]) + 0.7152*srgbToLinear(c[1]) + 0.0722*srgbToLinear(c[2])
}

func contrast(a, b string) float64 {
	x, y := relLum(a), relLum(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

func hueDist(a, b float64) float64 {
	d := math.Abs(a - b)
	return math.Min(d, 360-d)
}

func TestFocusRampEndsAtPrimaryAndBlueEnd(t *testing.T) {
	t.Setenv("BOBA_COLOR", "truecolor")
	th := Load(nil)
	for _, n := range []int{2, 7, 40, 160} {
		r := th.FocusRamp(n)
		if len(r) != n {
			t.Fatalf("n=%d: len %d", n, len(r))
		}
		if r[0] != sgr(defaultHex[Primary]) {
			t.Errorf("n=%d: first %q, want primary %q", n, r[0], sgr(defaultHex[Primary]))
		}
		if r[n-1] != sgr(defaultPrimaryEnd) {
			t.Errorf("n=%d: last %q, want %q", n, r[n-1], sgr(defaultPrimaryEnd))
		}
	}
	if th.FocusRamp(1)[0] != sgr(defaultHex[Primary]) {
		t.Error("a one-step ramp is primary")
	}
	a, b := th.FocusRamp(40), th.FocusRamp(40)
	if &a[0] != &b[0] {
		t.Error("ramps are cached per size")
	}
}

// The end is primary's derivative (guidelines 配色の原理 1/6): same L, a hue
// distinct from secondary, contrast at least primary's, inside sRGB.
func TestPrimaryEndFollowsAccentRules(t *testing.T) {
	p, _ := parseHex(defaultHex[Primary])
	e, _ := parseHex(defaultPrimaryEnd)
	s, _ := parseHex(defaultHex[Secondary])
	po, eo, so := toOKLCH(p), toOKLCH(e), toOKLCH(s)
	if math.Abs(po.L-eo.L) > 0.005 {
		t.Errorf("end L %.3f, primary L %.3f", eo.L, po.L)
	}
	if eo.H < 255 || eo.H > 280 {
		t.Errorf("end hue %.1f is not a Serenity-like blue", eo.H)
	}
	if d := hueDist(eo.H, so.H); d < 40 {
		t.Errorf("end hue %.1f too close to secondary %.1f", eo.H, so.H)
	}
	if c := contrast(defaultPrimaryEnd, defaultHex[Bg]); c < contrast(defaultHex[Primary], defaultHex[Bg]) {
		t.Errorf("end contrast %.2f below primary", c)
	}
}

// Interpolated in OKLCH: every step keeps primary's lightness and contrast,
// and the middle passes through purple (not a muddy sRGB midpoint).
func TestFocusRampStaysOnAccentLightness(t *testing.T) {
	cols := rampRGB(toOKLCH(must(defaultHex[Primary])), toOKLCH(must(defaultPrimaryEnd)), 21)
	for i, c := range cols {
		hex := fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2])
		o := toOKLCH(must(hex))
		if math.Abs(o.L-0.78) > 0.01 {
			t.Errorf("step %d %s: L %.3f", i, hex, o.L)
		}
		if cr := contrast(hex, defaultHex[Bg]); cr < 8.0 {
			t.Errorf("step %d %s: contrast %.2f", i, hex, cr)
		}
		if i == 10 && (o.H < 290 || o.H > 315) {
			t.Errorf("midpoint %s hue %.1f is not purple", hex, o.H)
		}
	}
}

func must(hex string) [3]float64 {
	c, ok := parseHex(hex)
	if !ok {
		panic(hex)
	}
	return c
}

func TestFocusRampFallsBackToSolidPrimary(t *testing.T) {
	for _, mode := range []string{"ansi", "none"} {
		t.Setenv("BOBA_COLOR", mode)
		if Load(nil).FocusRamp(10) != nil {
			t.Errorf("%s mode must keep a solid border", mode)
		}
	}
	t.Setenv("BOBA_COLOR", "truecolor")
	cases := []struct {
		name   string
		colors map[string]string
		ramp   bool
	}{
		{"primary overridden", map[string]string{"primary": "#ff0000"}, false},
		{"other role overridden", map[string]string{"secondary": "#00ffff"}, true},
		{"primary_end set", map[string]string{"primary": "#ff0000", PrimaryEndKey: "#0000ff"}, true},
		{"primary_end = primary", map[string]string{PrimaryEndKey: "#FF87DA"}, false},
		{"primary_end unparsable", map[string]string{PrimaryEndKey: "blue"}, false},
	}
	for _, c := range cases {
		r := Load(&config.Theme{Colors: c.colors}).FocusRamp(10)
		if (r != nil) != c.ramp {
			t.Errorf("%s: ramp = %v, want %v", c.name, r != nil, c.ramp)
		}
	}
	r := Load(&config.Theme{Colors: map[string]string{"primary": "#ff0000", PrimaryEndKey: "#0000ff"}}).FocusRamp(5)
	if r[0] != sgr("#ff0000") || r[4] != sgr("#0000ff") {
		t.Errorf("user ramp ends: %q … %q", r[0], r[4])
	}
}
