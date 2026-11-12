package daemon

import (
	"os"
	"slices"
	"testing"
	"time"

	"pekoe/internal/paths"
)

// config.toml changes are taken up without a restart; [http] port, [web]
// and [sandbox] keep their running value and are named as waiting for a
// restart; a file that does not read keeps the settings in use.
func TestReloadConfig(t *testing.T) {
	d, _, _ := setup(t)
	var events []ConfigReload
	d.testHook = func(ev string, data any) {
		if r, ok := data.(ConfigReload); ok && ev == "config.reloaded" {
			events = append(events, r)
		}
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(paths.GlobalConfig(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	port := d.cfg().HTTP.Port

	d.reloadConfig() // nothing changed (no file, as at start)
	if len(events) != 0 {
		t.Fatalf("an unchanged file was reloaded: %+v", events)
	}

	write("[cleanup]\nstale_after = \"5h\"\n[review]\nsurface = \"tui\"\n")
	d.reloadConfig()
	if g := d.cfg(); g.Cleanup.Stale() != 5*time.Hour || g.Review.Surface != "tui" {
		t.Fatalf("not taken up: %+v %+v", g.Cleanup, g.Review)
	}
	if len(events) != 1 || events[0].Error != "" || len(events[0].Restart) != 0 {
		t.Fatalf("events: %+v", events)
	}
	d.reloadConfig()
	if len(events) != 1 {
		t.Fatal("the same contents reloaded twice")
	}

	write("[http]\nport = 1\n[sandbox]\nenable = false\n[cleanup]\nstale_after = \"6h\"\n")
	d.reloadConfig()
	g := d.cfg()
	if g.HTTP.Port != port || !g.Sandbox.On() || g.Cleanup.Stale() != 6*time.Hour {
		t.Fatalf("running settings changed or the rest not taken up: port %d sandbox %v stale %v", g.HTTP.Port, g.Sandbox.On(), g.Cleanup.Stale())
	}
	if want := []string{"http.port", "sandbox"}; !slices.Equal(events[1].Restart, want) || !slices.Equal(d.configRestart(), want) {
		t.Fatalf("restart: %+v / %v", events[1], d.configRestart())
	}
	st := call[map[string]any](t, d, "daemon.status", nil)
	if st["config_restart"] == nil {
		t.Errorf("daemon.status lacks config_restart: %v", st)
	}

	write("[cleanup\n")
	d.reloadConfig()
	if g := d.cfg(); g.Cleanup.Stale() != 6*time.Hour {
		t.Fatal("a broken file replaced the settings")
	}
	if len(events) != 3 || events[2].Error == "" {
		t.Fatalf("no error event: %+v", events)
	}

	os.Remove(paths.GlobalConfig()) // back to the defaults
	d.reloadConfig()
	if g := d.cfg(); g.Cleanup.Stale() != 72*time.Hour || len(d.configRestart()) != 0 {
		t.Fatalf("removing the file: %v %v", g.Cleanup.Stale(), d.configRestart())
	}
}
