package theme

import (
	"testing"

	"boba/internal/config"
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
