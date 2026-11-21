package theme

import (
	"testing"

	"pekoe/internal/config"
)

func TestWebPalette(t *testing.T) {
	p := WebPalette(nil)
	if p["bg"] != "#151421" || p["primary"] != "#ff87da" || p[PrimaryEndKey] != defaultPrimaryEnd {
		t.Fatalf("defaults: %v", p)
	}
	// primary alone: solid border (no ramp), as in the TUI
	p = WebPalette(&config.Theme{Colors: map[string]string{"primary": "#FF0000"}})
	if p["primary"] != "#ff0000" || p[PrimaryEndKey] != "" {
		t.Fatalf("primary override: %v", p)
	}
	p = WebPalette(&config.Theme{Colors: map[string]string{"primary": "#ff0000", PrimaryEndKey: "#0000ff"}})
	if p[PrimaryEndKey] != "#0000ff" {
		t.Fatalf("primary_end: %v", p)
	}
	// anything but #rrggbb stays out of the CSS
	p = WebPalette(&config.Theme{Colors: map[string]string{"bg": "red;} body{display:none", "ok": "#12345"}})
	if p["bg"] != "#151421" || p["ok"] != "#51d75e" {
		t.Fatalf("bad values taken: %v", p)
	}
}

// bubble_me is primary_end at select's depth (docs/gui-design.md 決まったこと
// 9): #1c2c51 for the default; a [theme.colors] primary_end carries it
// along (same hue, L0.30); a solid border (primary alone) keeps the
// default end's hue, never primary's.
func TestBubbleMeFollowsPrimaryEnd(t *testing.T) {
	if p := WebPalette(nil); p[BubbleMeKey] != "#1c2c51" {
		t.Fatalf("default bubble_me: %q", p[BubbleMeKey])
	}
	// secondary no longer moves it
	if p := WebPalette(&config.Theme{Colors: map[string]string{"secondary": "#ff8800"}}); p[BubbleMeKey] != "#1c2c51" {
		t.Fatalf("bubble_me follows secondary: %q", p[BubbleMeKey])
	}
	p := WebPalette(&config.Theme{Colors: map[string]string{"primary": "#ff0000", PrimaryEndKey: "#ff8800"}})
	got := p[BubbleMeKey]
	if got == "#1c2c51" || !hexRe.MatchString(got) {
		t.Fatalf("bubble_me did not follow primary_end: %q", got)
	}
	rgb, _ := parseHex(got)
	o, s := toOKLCH(rgb), toOKLCH(must("#ff8800"))
	if d := o.H - s.H; d > 6 || d < -6 || o.L < 0.28 || o.L > 0.33 {
		t.Fatalf("bubble_me %s: L %.3f h %.1f, primary_end h %.1f", got, o.L, o.H, s.H)
	}
	// solid border, or a value that is not #rrggbb: the default end's hue
	for _, c := range []map[string]string{{"primary": "#ff0000"}, {PrimaryEndKey: "cyan"}} {
		if p := WebPalette(&config.Theme{Colors: c}); p[BubbleMeKey] != "#1c2c51" {
			t.Fatalf("%v: bubble_me %q", c, p[BubbleMeKey])
		}
	}
}

// code is primary_end's hue at primary_end's default depth (docs/
// ui-guidelines.md のカラートークン code): #9bb7f2 for the default; a
// [theme.colors] primary_end carries the hue along at L0.78; a solid border
// keeps the default end's hue. The light value is fixed.
func TestCodeFollowsPrimaryEnd(t *testing.T) {
	if p := WebPalette(nil); p[CodeKey] != "#9bb7f2" {
		t.Fatalf("default code: %q", p[CodeKey])
	}
	if p := WebLightPalette(); p[CodeKey] != "#3e5fad" {
		t.Fatalf("light code: %q", p[CodeKey])
	}
	p := WebPalette(&config.Theme{Colors: map[string]string{"primary": "#ff0000", PrimaryEndKey: "#ff8800"}})
	got := p[CodeKey]
	rgb, ok := parseHex(got)
	if !ok {
		t.Fatalf("code: %q", got)
	}
	o, s := toOKLCH(rgb), toOKLCH(must("#ff8800"))
	if d := o.H - s.H; d > 6 || d < -6 || o.L < 0.76 || o.L > 0.80 {
		t.Fatalf("code %s: L %.3f h %.1f, primary_end h %.1f", got, o.L, o.H, s.H)
	}
	for _, c := range []map[string]string{{"primary": "#ff0000"}, {PrimaryEndKey: "cyan"}, {"secondary": "#ff8800"}} {
		if p := WebPalette(&config.Theme{Colors: c}); p[CodeKey] != "#9bb7f2" {
			t.Fatalf("%v: code %q", c, p[CodeKey])
		}
	}
}

// chrome and raised: the GUI's grounds, bg's hue a step lighter; a
// [theme.colors] bg carries them along.
func TestWebGrounds(t *testing.T) {
	p := WebPalette(nil)
	if p[ChromeKey] != "#1a1927" || p[RaisedKey] != "#2a2939" {
		t.Fatalf("defaults: %s %s", p[ChromeKey], p[RaisedKey])
	}
	p = WebPalette(&config.Theme{Colors: map[string]string{"bg": "#102030"}})
	bg := toOKLCH(must("#102030"))
	for _, k := range []string{ChromeKey, RaisedKey} {
		o := toOKLCH(must(p[k]))
		if o.L <= bg.L || o.H-bg.H > 6 || bg.H-o.H > 6 {
			t.Fatalf("%s %s from bg #102030: L %.3f h %.1f", k, p[k], o.L, o.H)
		}
	}
	// the light palette is fixed: every role the CSS writes, as #rrggbb
	l := WebLightPalette()
	for _, k := range append([]string{ChromeKey, RaisedKey, PrimaryEndKey, BubbleMeKey}, roleNames()...) {
		if !hexRe.MatchString(l[k]) {
			t.Fatalf("light %s: %q", k, l[k])
		}
	}
	l["bg"] = "#000000"
	if WebLightPalette()["bg"] != "#f4f4fa" {
		t.Fatal("WebLightPalette is not a copy")
	}
}

func roleNames() []string {
	var out []string
	for _, r := range WebRoles {
		out = append(out, string(r))
	}
	return out
}
