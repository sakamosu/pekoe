package daemon

import (
	"pekoe/internal/paths"
	"pekoe/internal/tips"
)

// tipsNext is tips.next: the next tip, recorded in paths.TipsFile() like the
// TUI's empty pane. `pekoe tip` asks pekoed for it because an agent's
// sandbox (tapi chat's) cannot write pekoe's data directory; pekoed writes
// only that file, with ids from the built-in list.
//
// Prefix is [keys] prefix as pekoed read it: the tips write ^q for it, and
// the same sandbox hides config.toml from `pekoe tip`, which would show the
// default ^q.
type tipsNextR struct {
	Enabled bool      `json:"enabled"`
	Tip     *tips.Tip `json:"tip,omitempty"`
	Prefix  string    `json:"prefix,omitempty"`
}

func (d *Daemon) hTipsNext(struct{}) (any, error) {
	g := d.cfg()
	if !g.TipsOn() {
		return tipsNextR{}, nil
	}
	t, err := tips.Open(paths.TipsFile()).Next()
	if err != nil {
		return nil, err
	}
	return tipsNextR{Enabled: true, Tip: &t, Prefix: g.Keys.Prefix}, nil
}

// tipsPrefix is tips.prefix: [keys] prefix as pekoed read it, for
// `pekoe tip --list` and `pekoe tip <id>` in a sandbox that hides
// config.toml (they show tips without recording one).
type tipsPrefixR struct {
	Prefix string `json:"prefix"`
}

func (d *Daemon) hTipsPrefix(struct{}) (any, error) {
	return tipsPrefixR{Prefix: d.cfg().Keys.Prefix}, nil
}
