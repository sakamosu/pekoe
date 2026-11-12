package config

import (
	"bytes"
	"strings"

	"github.com/BurntSushi/toml"
)

// Hidden replaces a secret in Effective's output.
const Hidden = "(hidden)"

// Effective is g (as LoadGlobal returned it) with every default spelled
// out, as `pekoe config show` prints it: unset switches as their value,
// durations as the one in use, and secrets ([web] auth_key) hidden.
func Effective(g *Global) *Global {
	c := *g
	on := func(p *bool) *bool {
		v := p == nil || *p
		return &v
	}
	c.Animate = on(g.Animate)
	c.Picks.AutoSend = on(g.Picks.AutoSend)
	c.Tips.Enable = on(g.Tips.Enable)
	c.Sandbox.Enable = on(g.Sandbox.Enable)
	c.Mascot.Splash = on(g.Mascot.Splash)
	c.Cleanup.StaleAfter = fmtDur(g.Cleanup.Stale())
	if c.Orchestrator.Notify == "" || !c.Orchestrator.QuietNotify() {
		c.Orchestrator.Notify = "escalate"
	}
	if c.Theme.Mode == "" {
		c.Theme.Mode = "auto"
	}
	if c.Theme.Icons == "" {
		c.Theme.Icons = "nerd"
	}
	if c.Mascot.Shape == "" {
		c.Mascot.Shape = "auto"
	}
	if c.Mascot.Glyphs == "" {
		c.Mascot.Glyphs = "auto"
	}
	if c.Mascot.Palette == "" {
		c.Mascot.Palette = "dark"
	}
	if strings.TrimSpace(c.Web.AuthKey) != "" {
		c.Web.AuthKey = Hidden
	}
	c.Agents = map[string]Agent{}
	for k, a := range g.Agents {
		a.Sandbox = on(a.Sandbox)
		c.Agents[k] = a
	}
	return &c
}

// EncodeTOML prints g as config.toml.
func EncodeTOML(g *Global) ([]byte, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(g); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Template is what `pekoe config edit` starts a missing config.toml with:
// comments only, so every setting stays at its default.
const Template = `# pekoe's settings. Every key is optional: what is not here keeps its default.
#   pekoe config show    every setting with the value in use
#   pekoe config check   what is wrong in this file
# The keys are listed in the README (Configuration).
#
# [theme]
# mode = "auto"        # truecolor | ansi | none | auto
`
