package daemon

// config.toml while pekoed runs: tickLoop reads it every second and, when
// its contents changed, takes it up without a restart. The file is only
// read here — writing it is `pekoe config edit`'s, outside pekoed (an agent
// cannot reach ~/.config/pekoe, and no RPC writes it). A file that does not
// read leaves the settings as they were. Settings that only take effect at
// start ([http] port, [web], [sandbox]: config.RestartNeeded) keep their
// running value and are named in the event until pekoed restarts.

import (
	"bytes"
	"errors"
	"io/fs"
	"log"
	"os"
	"slices"

	"pekoe/internal/config"
	"pekoe/internal/paths"
)

// ConfigReload is the config.reloaded event: config.toml changed and was
// taken up (Error empty), or did not read and the settings stay as they
// were (Error set; the file's text is not in it, the TUI reads the file
// itself). Restart names the changed settings that wait for a restart.
type ConfigReload struct {
	Error   string   `json:"error,omitempty"`
	Restart []string `json:"restart,omitempty"`
}

// cfg is config.toml as pekoed uses it now. Never nil after New.
func (d *Daemon) cfg() *config.Global {
	d.cfgMu.RLock()
	defer d.cfgMu.RUnlock()
	return d.Global
}

// reloadConfig takes config.toml up again when its contents changed since
// the last look.
func (d *Daemon) reloadConfig() {
	b, err := os.ReadFile(paths.GlobalConfig())
	if errors.Is(err, fs.ErrNotExist) {
		b, err = nil, nil // removed: the defaults
	}
	if err != nil {
		return // unreadable for now: keep what we have, look again later
	}
	d.cfgMu.Lock()
	if bytes.Equal(b, d.cfgRaw) {
		d.cfgMu.Unlock()
		return
	}
	d.cfgRaw = b
	running := d.started
	if running == nil {
		running = d.Global
	}
	next, err := config.ParseGlobal(b)
	if err != nil {
		d.cfgMu.Unlock()
		log.Printf("config: %s: %v (keeping the settings in use)", paths.GlobalConfig(), err)
		d.emit("config.reloaded", ConfigReload{Error: "config.toml does not read; pekoed keeps the settings in use"})
		return
	}
	restart := config.RestartNeeded(running, next)
	d.Global = config.KeepRunning(running, next)
	changed := !slices.Equal(restart, d.cfgRestart)
	d.cfgRestart = restart
	d.cfgMu.Unlock()
	if len(restart) > 0 {
		if changed {
			log.Printf("config: reloaded; %v change after a restart (pekoe daemon restart)", restart)
		}
	} else {
		log.Printf("config: reloaded")
	}
	d.emit("config.reloaded", ConfigReload{Restart: restart})
}

// configRestart names the changed settings that wait for a restart.
func (d *Daemon) configRestart() []string {
	d.cfgMu.RLock()
	defer d.cfgMu.RUnlock()
	return slices.Clone(d.cfgRestart)
}
