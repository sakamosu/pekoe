//go:build !web

package daemon

// The default build: no tsnet, no Tailscale code in the binary. [web]
// enable = true (e.g. a config shared through dotfiles) only gets a warning;
// the web UI needs a build with -tags web (make install WEB=1, docs/web.md).

import (
	"errors"
	"log"
)

const webBuilt = false

var errNoWebBuild = errors.New("this boba is built without the web UI (Tailscale); rebuild with: make install WEB=1")

func (d *Daemon) startWeb() {
	if !d.Global.Web.Enable {
		d.web.set(func(s *webStatus) { *s = webStatus{State: "off"} })
		return
	}
	log.Printf("web: [web] enable = true is ignored: %v", errNoWebBuild)
	d.web.set(func(s *webStatus) {
		*s = webStatus{Enabled: true, State: "unavailable", Error: errNoWebBuild.Error()}
	})
}

func (d *Daemon) stopWeb() {}
