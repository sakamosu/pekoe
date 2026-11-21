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
// のカラートークン bubble_me, docs/gui-design.md 決まったこと 9):
// primary_end dropped to select's depth (L0.30, C0.07) with primary_end's
// hue, as select is primary's. Derived, never set.
const BubbleMeKey = "bubble_me"

// ChromeKey and RaisedKey are the local GUI's two extra grounds
// (docs/gui-design.md のカラートークン): chrome under the title bar, the
// sidebar, the tabs and the status line; raised under the open row and the
// chosen chip. Both are bg's hue, lighter.
const (
	ChromeKey = "chrome"
	RaisedKey = "raised"
)

// The dark defaults of chrome and raised (docs/gui-design.md: L0.22 and
// L0.29 of bg's hue). A [theme.colors] bg carries them along (webGround).
const (
	defaultChrome = "#1a1927"
	defaultRaised = "#2a2939"
)

// defaultEndHue is defaultPrimaryEnd's hue: bubble_me's when the focus
// border is solid (no primary_end), so the bubble does not take primary's
// hue (it would read as something to press).
const defaultEndHue = 265

// clipHex paints c as a browser paints oklch(): out-of-gamut channels are
// clipped (toRGB8 lowers C instead).
func clipHex(c oklch) string {
	r, g, b := linearRGB(c)
	q := func(x float64) uint8 {
		return uint8(math.Round(linearToSRGB(math.Min(math.Max(x, 0), 1)) * 255))
	}
	return fmt.Sprintf("#%02x%02x%02x", q(r), q(g), q(b))
}

// BubbleMe derives bubble_me from primary_end (#rrggbb): L0.30 C0.07 at its
// hue (the default comes out #1c2c51). "" (a solid border) or a bad value
// takes the default primary_end's hue.
func BubbleMe(primaryEnd string) string {
	h := float64(defaultEndHue)
	if rgb, ok := parseHex(primaryEnd); ok {
		h = toOKLCH(rgb).H
	}
	return clipHex(oklch{L: 0.30, C: 0.07, H: h})
}

// webGround is chrome or raised for a bg the user set: bg's chroma and hue,
// dL lighter.
func webGround(bg string, dL float64) string {
	rgb, _ := parseHex(bg)
	o := toOKLCH(rgb)
	o.L += dL
	return clipHex(o)
}

// WebPalette is the truecolor palette as the TUI paints it: the defaults
// with the user's [theme.colors] on top, "primary_end" when the focus
// border is a ramp (absent ⇒ solid primary, as in Load), "bubble_me"
// (BubbleMe of the primary_end in effect) and the GUI's "chrome" and
// "raised". Only #rrggbb values are taken: the result goes into CSS.
func WebPalette(cfg *config.Theme) map[string]string {
	pick := func(key, def string) string {
		if cfg != nil && hexRe.MatchString(cfg.Colors[key]) {
			return strings.ToLower(cfg.Colors[key])
		}
		return def
	}
	out := make(map[string]string, len(WebRoles)+4)
	for _, r := range WebRoles {
		out[string(r)] = pick(string(r), defaultHex[r])
	}
	end := defaultPrimaryEnd
	if cfg != nil && cfg.Colors[string(Primary)] != "" {
		end = ""
	}
	end = pick(PrimaryEndKey, end)
	if end != "" && !strings.EqualFold(end, out[string(Primary)]) {
		out[PrimaryEndKey] = end
	}
	out[BubbleMeKey] = BubbleMe(out[PrimaryEndKey])
	out[ChromeKey], out[RaisedKey] = defaultChrome, defaultRaised
	if bg := out[string(Bg)]; bg != defaultHex[Bg] {
		out[ChromeKey], out[RaisedKey] = webGround(bg, 0.0224), webGround(bg, 0.0901)
	}
	return out
}

// webLight is the local GUI's light palette (docs/gui-design.md のカラー
// トークン, 決まったこと 11): fixed, [theme.colors] does not reach it (that
// is the user's dark terminal). The phone's page stays dark.
var webLight = map[string]string{
	string(Bg): "#f4f4fa", ChromeKey: "#ececf5", string(Surface): "#fbfbfe", RaisedKey: "#e0e0ed",
	string(Border): "#ccccdb", string(Dim): "#616174", string(Text): "#202030",
	string(Primary): "#ba2997", PrimaryEndKey: "#4c6ebd", string(Secondary): "#0e6f81",
	string(Warn): "#796500", string(Ok): "#06791f", string(Err): "#cc1331",
	string(Select): "#fdd8ef", BubbleMeKey: "#d9e5fd",
}

// WebLightPalette is a copy of the light palette.
func WebLightPalette() map[string]string {
	out := make(map[string]string, len(webLight))
	for k, v := range webLight {
		out[k] = v
	}
	return out
}

// WebIcons is the icon set the phone can draw: Safari has no Nerd Font.
func WebIcons() IconSet { return Icons("unicode") }
