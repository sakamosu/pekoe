package daemon

// config.show and GET /api/config: the local GUI's Settings view
// (docs/gui.md「Settings」). Reading only: config.toml is written by
// `pekoe config edit` alone, outside pekoed (config.go), and nothing here
// writes it. The method is a person's (human.go): ~/.config/pekoe is out of
// an agent's sandbox on purpose, so pekoed does not read it back to one.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"slices"

	"pekoe/internal/config"
	"pekoe/internal/paths"
)

// configShow is what config.show returns.
type configShow struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	// Sections: the settings pekoed uses now, defaults filled in and
	// secrets hidden (`pekoe config show`), section by section with their
	// docs, restart marks and whether config.toml has each key
	Sections []config.ShownSection `json:"sections"`
	// Restart: settings config.toml changed that keep their running value
	// until pekoe daemon restart (config.RestartNeeded: "http.port",
	// "web", "sandbox")
	Restart []string `json:"restart"`
	// Waiting: the same key by key, dotted ("http.port",
	// "web.allow_devices", "sandbox.enable": config.RestartKeys), for the
	// marks on the keys themselves ("web" names the section, not which of
	// its keys changed)
	Waiting []string `json:"waiting"`
	// Problems: config check on the file as pekoed last read it. A fatal
	// one means the file does not read and pekoed keeps what it had
	Problems []configProblem `json:"problems"`
}

type configProblem struct {
	Key   string `json:"key,omitempty"`
	Msg   string `json:"message"`
	Fatal bool   `json:"fatal,omitempty"`
}

func (d *Daemon) hConfigShow(json.RawMessage) (any, error) {
	d.cfgMu.RLock()
	g, raw := d.Global, d.cfgRaw
	restart, waiting := slices.Clone(d.cfgRestart), slices.Clone(d.cfgWaiting)
	d.cfgMu.RUnlock()
	path := paths.GlobalConfig()
	_, err := os.Stat(path)
	out := configShow{Path: path, Exists: err == nil || !errors.Is(err, fs.ErrNotExist), Sections: config.Shown(g), Restart: restart, Waiting: waiting, Problems: []configProblem{}}
	if out.Restart == nil {
		out.Restart = []string{}
	}
	if out.Waiting == nil {
		out.Waiting = []string{}
	}
	config.MarkSet(out.Sections, raw)
	if raw != nil {
		for _, p := range config.Check(raw) {
			out.Problems = append(out.Problems, configProblem{p.Key, p.Msg, p.Fatal})
		}
	}
	return out, nil
}

// guiConfig is GET /api/config: config.show, as the person.
func (d *Daemon) guiConfig(w http.ResponseWriter, r *http.Request) {
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "config.show", nil)
	if err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, res)
}
