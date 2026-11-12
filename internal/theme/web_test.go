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

// bubble_me is secondary at select's depth: #003643 for the default, and a
// [theme.colors] secondary carries it along (same hue, L0.30, C0.07).
func TestBubbleMeFollowsSecondary(t *testing.T) {
	if p := WebPalette(nil); p[BubbleMeKey] != "#003643" {
		t.Fatalf("default bubble_me: %q", p[BubbleMeKey])
	}
	p := WebPalette(&config.Theme{Colors: map[string]string{"secondary": "#ff8800"}})
	got := p[BubbleMeKey]
	if got == "#003643" || !hexRe.MatchString(got) {
		t.Fatalf("bubble_me did not follow secondary: %q", got)
	}
	rgb, _ := parseHex(got)
	o, s := toOKLCH(rgb), toOKLCH(must("#ff8800"))
	if d := o.H - s.H; d > 6 || d < -6 || o.L < 0.28 || o.L > 0.33 {
		t.Fatalf("bubble_me %s: L %.3f h %.1f, secondary h %.1f", got, o.L, o.H, s.H)
	}
	// a value that is not #rrggbb is not taken: the default's bubble
	p = WebPalette(&config.Theme{Colors: map[string]string{"secondary": "cyan"}})
	if p[BubbleMeKey] != "#003643" {
		t.Fatalf("bad secondary: %q", p[BubbleMeKey])
	}
}
