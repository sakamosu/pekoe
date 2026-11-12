package theme

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"pekoe/internal/config"
)

// WebRoles are the roles the web UI (docs/web.md) paints, in CSS order.
var WebRoles = []Role{Bg, Surface, Border, Dim, Text, Primary, Secondary, Warn, Ok, Err, Select}

var hexRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// BubbleMeKey is the web chat's own-message bubble (docs/ui-guidelines.md
// のカラートークン bubble_me): secondary dropped to select's depth (L0.30,
// C0.07) with secondary's hue, as select is primary's. Derived, never set:
// it follows a [theme.colors] secondary.
const BubbleMeKey = "bubble_me"

// BubbleMe derives bubble_me from secondary (#rrggbb). L0.30 C0.07 is out
// of sRGB for cyan hues; each channel is clipped, as a browser paints
// oklch(0.30 0.07 h) (the default comes out #003643, effective L0.31
// C0.056 h220), rather than lowering C as toRGB8 does.
func BubbleMe(secondary string) string {
	rgb, ok := parseHex(secondary)
	if !ok {
		rgb, _ = parseHex(defaultHex[Secondary])
	}
	r, g, b := linearRGB(oklch{L: 0.30, C: 0.07, H: toOKLCH(rgb).H})
	q := func(x float64) uint8 {
		return uint8(math.Round(linearToSRGB(math.Min(math.Max(x, 0), 1)) * 255))
	}
	return fmt.Sprintf("#%02x%02x%02x", q(r), q(g), q(b))
}

// WebPalette is the truecolor palette as the TUI paints it: the defaults
// with the user's [theme.colors] on top, "primary_end" when the focus
// border is a ramp (absent ⇒ solid primary, as in Load), and "bubble_me"
// (BubbleMe of the secondary in effect). Only #rrggbb
// values are taken: the result goes into CSS.
func WebPalette(cfg *config.Theme) map[string]string {
	pick := func(key, def string) string {
		if cfg != nil && hexRe.MatchString(cfg.Colors[key]) {
			return strings.ToLower(cfg.Colors[key])
		}
		return def
	}
	out := make(map[string]string, len(WebRoles)+1)
	for _, r := range WebRoles {
		out[string(r)] = pick(string(r), defaultHex[r])
	}
	out[BubbleMeKey] = BubbleMe(out[string(Secondary)])
	end := defaultPrimaryEnd
	if cfg != nil && cfg.Colors[string(Primary)] != "" {
		end = ""
	}
	end = pick(PrimaryEndKey, end)
	if end != "" && !strings.EqualFold(end, out[string(Primary)]) {
		out[PrimaryEndKey] = end
	}
	return out
}

// WebIcons is the icon set the phone can draw: Safari has no Nerd Font.
func WebIcons() IconSet { return Icons("unicode") }
