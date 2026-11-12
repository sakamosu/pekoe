package config

import "reflect"

// Taking config.toml up again while pekoed runs: most settings are read
// where they are used (the theme, the agents, [cleanup], [picks] …), so a
// new Global applies at once. A few are set up once when pekoed starts and
// stay as they were until it restarts.

// RestartNeeded names the settings next changes from running (what pekoed
// started with) that take effect only after pekoed restarts: [http] port
// (its listener), [web] (the tsnet node and the sandbox's deny list of its
// state; not confirm_tapi_send, which the page reads from GET /api/tapi)
// and [sandbox] (the wrapper of hooks and git). nil when none.
func RestartNeeded(running, next *Global) []string {
	var out []string
	if running.HTTP.Port != next.HTTP.Port {
		out = append(out, "http.port")
	}
	if !reflect.DeepEqual(webAtStart(running.Web), webAtStart(next.Web)) {
		out = append(out, "web")
	}
	if running.Sandbox.On() != next.Sandbox.On() {
		out = append(out, "sandbox")
	}
	return out
}

// KeepRunning is next with the settings RestartNeeded watches put back to
// running's: what pekoed reads must stay what it runs with.
func KeepRunning(running, next *Global) *Global {
	c := *next
	c.HTTP = running.HTTP
	c.Web = running.Web
	c.Web.ConfirmTapiSend = next.Web.ConfirmTapiSend
	c.Sandbox = running.Sandbox
	return &c
}

// webAtStart is w without what applies without a restart.
func webAtStart(w Web) Web {
	w.ConfirmTapiSend = nil
	return w
}
