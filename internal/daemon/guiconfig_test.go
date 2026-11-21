package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"pekoe/internal/config"
	"pekoe/internal/paths"
)

// GET /api/config is config.show on the GUI only, with a session token:
// the settings pekoed uses (defaults filled in, the auth key hidden)
// section by section with docs, the keys config.toml has marked, what
// waits for a restart and what config check finds. A file that stops
// reading keeps the settings in use and says so. The phone's Web UI has
// no such route.
func TestGUIConfig(t *testing.T) {
	d, _, _, h, _ := guiSetup(t)
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(paths.GlobalConfig(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		d.reloadConfig()
	}
	if w := doGUI(t, h, guiReq{path: "/api/config"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	s := guiSignIn(t, d, h)
	get := func() (configShow, string) {
		t.Helper()
		w := doGUI(t, h, guiReq{path: "/api/config", bearer: s})
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("config: %d %v %s", w.Code, w.Header(), w.Body)
		}
		var v configShow
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v, w.Body.String()
	}
	key := func(v configShow, sec, k string) config.ShownKey {
		t.Helper()
		for _, s := range v.Sections {
			if s.Name != sec {
				continue
			}
			for _, x := range s.Keys {
				if x.Key == k {
					return x
				}
			}
		}
		t.Fatalf("no %s.%s in %+v", sec, k, v.Sections)
		return config.ShownKey{}
	}

	// no file: every setting at its default, nothing set, empty lists
	v, body := get()
	if v.Exists || v.Path != paths.GlobalConfig() || len(v.Problems) != 0 || len(v.Restart) != 0 ||
		!strings.Contains(body, `"restart":[]`) || !strings.Contains(body, `"problems":[]`) {
		t.Fatalf("no file: %s", body)
	}
	if k := key(v, "http", "port"); k.Value != float64(47777) || k.Set || !k.Restart || k.Doc == "" {
		t.Fatalf("http.port: %+v", k)
	}

	// pekoed started with an auth key (as after a restart), then the file
	// changed [http] port and [cleanup]
	start := "[web]\nauth_key = \"tskey-auth-SECRET\"\n"
	write(start)
	g, err := config.ParseGlobal([]byte(start))
	if err != nil {
		t.Fatal(err)
	}
	d.cfgMu.Lock()
	d.started, d.Global, d.cfgRestart = g, g, nil
	d.cfgMu.Unlock()
	port := g.HTTP.Port
	write(start + "[cleanup]\nstale_after = \"5h\"\nbogus = 1\n[http]\nport = 1\n")
	v, body = get()
	if strings.Contains(body, "SECRET") {
		t.Fatalf("the auth key shows: %s", body)
	}
	if k := key(v, "web", "auth_key"); k.Value != config.Hidden || !k.Set || !k.Restart {
		t.Errorf("auth_key: %+v", k)
	}
	if k := key(v, "cleanup", "stale_after"); k.Value != "5h0m0s" && k.Value != "5h" || !k.Set {
		t.Errorf("stale_after: %+v", k)
	}
	// [http] port waits for a restart: the running one is what shows
	if k := key(v, "http", "port"); k.Value != float64(port) || !k.Set {
		t.Errorf("http.port: %+v (running %d)", k, port)
	}
	if !v.Exists || !slices.Equal(v.Restart, []string{"http.port"}) {
		t.Errorf("restart: %+v", v.Restart)
	}
	if len(v.Problems) != 1 || v.Problems[0].Key != "cleanup.bogus" || v.Problems[0].Fatal {
		t.Errorf("problems: %+v", v.Problems)
	}

	// broken: pekoed keeps what it had, and the problem says the file does not read
	write("[cleanup\n")
	v, _ = get()
	if len(v.Problems) != 1 || !v.Problems[0].Fatal {
		t.Errorf("broken: %+v", v.Problems)
	}
	if k := key(v, "cleanup", "stale_after"); k.Value != "5h0m0s" && k.Value != "5h" || k.Set {
		t.Errorf("broken stale_after: %+v", k)
	}

	ph := testWebHandler(d)
	if w := doWeb(t, ph, webReq{path: "/api/config", from: ipPhone}); w.Code == 200 && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("phone /api/config: %d %s", w.Code, w.Body)
	}
}

// The Settings view (gui/views/settings.js) drawn from what config.show
// returns for a real config.toml, in a small fake DOM (node; skipped
// without node): the path, what waits for a restart, config check's
// warnings, each section as config.toml spells it with its keys, values,
// docs and marks (set, restart, waits for restart), the secret hidden, the
// title bar's box filtering, a failed read said as such, and a read that
// comes back after the view went is dropped. Values are text, never HTML.
func TestGUISettingsView(t *testing.T) {
	d, _, _ := setup(t)
	start := "[web]\nauth_key = \"tskey-auth-SECRET\"\n"
	if err := os.WriteFile(paths.GlobalConfig(), []byte(start), 0o600); err != nil {
		t.Fatal(err)
	}
	d.reloadConfig()
	g, _ := config.ParseGlobal([]byte(start))
	d.cfgMu.Lock()
	d.started, d.Global, d.cfgRestart = g, g, nil
	d.cfgMu.Unlock()
	evil := `<img src=x onerror=alert(1)>`
	if err := os.WriteFile(paths.GlobalConfig(), []byte(start+"[http]\nport = 1\n[gui]\nbrowser = \""+evil+"\"\nbogus = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.reloadConfig()
	res, err := d.hConfigShow(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	out := runWebModule(t, fakeDOM+`
import view, { fmtValue } from "gui/views/settings.js";
const data = `+string(raw)+`;
let answer = () => ({ ok: true, status: 200, json: async () => data, text: async () => "" });
let calls = 0;
globalThis.fetch = async (p) => { calls++; if (p !== "/api/config") throw new Error("path " + p); return answer(); };
const tick = () => new Promise((r) => setTimeout(r, 0));
const r = {};
const box = el0("div");
view.mount(box, { go: () => {} });
r.loading = box.textContent;
await tick(); await tick();
r.text = box.textContent;
r.heads = find(box, (n) => /view-settings-sname/.test(n.className)).map((n) => n.textContent);
const key = (name) => find(box, (n) => /\bview-settings-key\b/.test(n.className) && n.children[0].children[0].textContent === name);
r.port = key("port")[0].textContent;
r.portSet = key("port")[0].className;
r.browser = key("browser")[0].children[1].textContent;
r.authKey = key("auth_key")[0].textContent;
r.ttl = key("session_ttl")[0].textContent + "|" + key("session_ttl")[0].className;
r.imgs = find(box, (n) => n.tag === "img").length;
view.update({}); // read 5 s ago at most: not again
r.calls = calls;
view.query("stale");
r.filtered = find(box, (n) => /\bview-settings-key\b/.test(n.className)).map((n) => n.children[0].children[0].textContent);
view.query("restart");
r.restartKeys = find(box, (n) => /\bview-settings-key\b/.test(n.className)).length;
view.query("zzz-nothing");
r.none = box.textContent;
view.query("");
view.unmount();
answer = () => ({ ok: false, status: 500, statusText: "x", text: async () => "pekoed is busy" });
const box2 = el0("div");
view.mount(box2, { go: () => {} });
await tick(); await tick();
r.failed = box2.textContent;
// a read on its way when the view goes is dropped
let release;
globalThis.fetch = () => new Promise((res) => { release = () => res({ ok: true, status: 200, json: async () => data }); });
const box3 = el0("div");
view.unmount();
view.mount(box3, {});
view.unmount();
release();
await tick(); await tick();
r.after = box3.textContent;
r.fmt = [fmtValue(true), fmtValue(0), fmtValue(""), fmtValue("(hidden)"), fmtValue([]), fmtValue(["a", "b"]), fmtValue({}), fmtValue({ b: 2, a: "x" }), fmtValue(null)];
console.log(JSON.stringify(r));
`)
	var got struct {
		Loading, Text, Port, PortSet, Browser, AuthKey, TTL, None, Failed, After string
		Heads, Filtered, Fmt                                                     []string
		Imgs, Calls, RestartKeys                                                 int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	if !strings.Contains(got.Loading, "SETTINGS") || !strings.Contains(got.Loading, "Loading") {
		t.Errorf("loading: %q", got.Loading)
	}
	for _, want := range []string{"SETTINGS", "read only", paths.GlobalConfig(), "pekoe config edit", "! http.port changed · applies after pekoe daemon restart",
		"! gui.bogus: unknown key (ignored)", "THIS TAB", "Tailscale auth key"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("view lacks %q: %s", want, got.Text)
		}
	}
	if strings.Contains(got.Text, "SECRET") {
		t.Fatalf("the auth key shows: %s", got.Text)
	}
	if !slices.Equal(got.Heads[:4], []string{"[http]", "[web]", "[gui]", "[keys]"}) || !slices.Contains(got.Heads, "[agents.claude]") || !slices.Contains(got.Heads, "animate") {
		t.Errorf("heads: %v", got.Heads)
	}
	if !strings.Contains(got.Port, "! waits for restart") || !strings.Contains(got.Port, "set") || !strings.Contains(got.Port, "47777") || !strings.Contains(got.PortSet, "set") {
		t.Errorf("port: %q %q", got.Port, got.PortSet)
	}
	if got.Browser != `"`+evil+`"` || got.Imgs != 0 {
		t.Errorf("browser: %q, %d img", got.Browser, got.Imgs)
	}
	if !strings.Contains(got.AuthKey, "(hidden)") || !strings.Contains(got.AuthKey, "restart") {
		t.Errorf("auth_key: %q", got.AuthKey)
	}
	if !strings.Contains(got.TTL, `"12h"`) || strings.Contains(got.TTL, " set") || strings.Contains(got.TTL, "restart") {
		t.Errorf("session_ttl: %q", got.TTL)
	}
	if got.Calls != 1 {
		t.Errorf("read %d times", got.Calls)
	}
	if !slices.Equal(got.Filtered, []string{"stale_after"}) {
		t.Errorf("filter stale: %v", got.Filtered)
	}
	if got.RestartKeys != 9 { // http.port, 7 of [web], sandbox.enable; confirm_tapi_send by its doc
		t.Errorf("filter restart: %d keys", got.RestartKeys)
	}
	if !strings.Contains(got.None, "Nothing matches") {
		t.Errorf("no match: %q", got.None)
	}
	if !strings.Contains(got.Failed, "✗ settings unavailable · pekoed is busy") {
		t.Errorf("failed: %q", got.Failed)
	}
	if strings.Contains(got.After, "[http]") {
		t.Errorf("a read after unmount drew: %q", got.After)
	}
	if want := []string{"true", "0", `""`, "(hidden)", "[]", `["a", "b"]`, "{}", `{ a = "x", b = 2 }`, "(unset)"}; !slices.Equal(got.Fmt, want) {
		t.Errorf("fmt: %v", got.Fmt)
	}
}
