package theme

import (
	"regexp"
	"strings"

	"pekoe/internal/config"
)

// WebRoles are the roles the web UI (docs/web.md) paints, in CSS order.
var WebRoles = []Role{Bg, Surface, Border, Dim, Text, Primary, Secondary, Warn, Ok, Err, Select}

var hexRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// WebPalette is the truecolor palette as the TUI paints it: the defaults
// with the user's [theme.colors] on top, and "primary_end" when the focus
// border is a ramp (absent ⇒ solid primary, as in Load). Only #rrggbb
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
