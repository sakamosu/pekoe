package daemon

import (
	"pekoe/internal/paths"
	"pekoe/internal/tips"
)

// tipsNext is tips.next: the next tip, recorded in paths.TipsFile() like the
// TUI's empty pane. `pekoe tip` asks pekoed for it because an agent's
// sandbox (tapi chat's) cannot write pekoe's data directory; pekoed writes
// only that file, with ids from the built-in list.
type tipsNextR struct {
	Enabled bool      `json:"enabled"`
	Tip     *tips.Tip `json:"tip,omitempty"`
}

func (d *Daemon) hTipsNext(struct{}) (any, error) {
	if !d.cfg().TipsOn() {
		return tipsNextR{}, nil
	}
	t, err := tips.Open(paths.TipsFile()).Next()
	if err != nil {
		return nil, err
	}
	return tipsNextR{Enabled: true, Tip: &t}, nil
}
