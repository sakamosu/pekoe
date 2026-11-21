package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// The images waiting for a send (gui/attach.js, run in node; skipped
// without node): one store for every composer, a key each. The tapi
// panel's (a key that is never a slug) and tapi's own tab (TAPI) and a
// session's never mix; at most max per key, the rest a toast; what is not
// an image is left out; a send drops only the images it took.
func TestGUIAttachPerComposer(t *testing.T) {
	out := runWebModule(t, `import { attachments } from "gui/attach.js";
const toasts = [], changed = [];
const PANEL = Symbol("tapi panel");
const att = attachments({
  max: 4, toast: (m) => toasts.push(m), changed: (k) => changed.push(typeof k === "symbol" ? "panel" : k),
  prepare: async (f) => { if (f.bad) throw new Error("not an image this browser can open"); return { name: f.name }; },
});
const img = (name, extra) => ({ name, type: "image/png", ...extra });
const names = (k) => att.get(k).map((im) => im.name).join(",");
await att.add(PANEL, [img("a"), { name: "notes.txt", type: "text/plain" }, img("b")]);
await att.add("+tapi", [img("t")]);
await att.add("main", [img("m1"), img("m2"), img("m3"), img("m4"), img("m5")]);
const r = { panel: names(PANEL), tapi: names("+tapi"), main: names("main"), toasts: toasts.splice(0) };
await att.add("main", [img("m6")]);
r.full = toasts.splice(0);
await att.add(PANEL, [img("bad", { bad: true }), img("c")]);
r.bad = toasts.splice(0); r.panel2 = names(PANEL);
const sent = att.get(PANEL).slice(0, 2);
att.drop(PANEL, sent);
r.afterSend = names(PANEL); r.tapiAfter = names("+tapi");
att.drop(PANEL, att.get(PANEL));
r.empty = att.get(PANEL).length; r.none = att.get(null).length; r.reading = att.preparing(PANEL);
r.changed = [...new Set(changed)].join(",");
console.log(JSON.stringify(r));`)
	want := `{"panel":"a,b","tapi":"t","main":"m1,m2,m3,m4","toasts":["at most 4 images in one send: took the first 4"],` +
		`"full":["at most 4 images in one send"],"bad":["not an image this browser can open"],"panel2":"a,b,c",` +
		`"afterSend":"c","tapiAfter":"t","empty":0,"none":0,"reading":false,"changed":"panel,+tapi,main"}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("attachments:\n got %s\nwant %s", got, want)
	}
}

// The tapi panel's send takes its own images (no longer none), and the
// page has the + and the thumbs for them; a send with images still goes
// through the send sheet (seen once more before it leaves), even where
// tapi's sends need no sheet (the GUI).
func TestGUITapiPanelAttaches(t *testing.T) {
	js, _ := webFiles.ReadFile("webui/gui/gui.js")
	s := string(js)
	for _, want := range []string{
		`const key = isPanel ? PANEL : slug;`,
		`const images = att.get(key);`,
		`if (images.length) return openSendSheet(slug, text, images, ta);`,
		`att.drop(ta === $("c-text") ? PANEL : slug, images);`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("gui.js: missing %s", want)
		}
	}
	if strings.Contains(s, "isPanel ? []") {
		t.Error("gui.js: the panel's send still drops its images")
	}
	html, _ := webFiles.ReadFile("webui/gui/index.html")
	for _, id := range []string{`id="c-thumbs"`, `id="c-attach"`, `id="c-file"`} {
		if !strings.Contains(string(html), id) {
			t.Errorf("gui/index.html: missing %s", id)
		}
	}
}

// What the panel sends: images to tapi through the GUI, saved under
// images/+tapi and typed as a line each after the text.
func TestGUITapiSendImages(t *testing.T) {
	d, fe, _, h, _ := guiSetup(t)
	call[Tapi](t, d, "tapi.open", nil)
	s := guiSignIn(t, d, h)
	w := doGUI(t, h, guiReq{method: "POST", path: "/api/tapi/send", bearer: s,
		body: map[string]any{"text": "このスクショを見て", "images": imgs(testPNG(t, 5, 5), testJPEG(t))}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"images":2`) {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	typed := lastText(fe)
	files := typedImages(typed)
	if len(files) != 2 || filepath.Dir(files[0]) != filepath.Join(paths.WebImagesDir(), TapiSlug) || !strings.HasPrefix(typed, "このスクショを見て\n\n") {
		t.Fatalf("typed: %q", typed)
	}
	if rs := tapiSends(t, d); len(rs) != 1 || rs[0].Via != viaGUI || rs[0].Text != typed || !rs[0].OK {
		t.Fatalf("send log: %+v", rs)
	}
}

// A session's send answers what was typed, as tapi's does: the chat's "not
// sent yet" bubble (●) waits for those words, the image lines in them, to
// come back from the transcript. Without them it waited for the words
// alone, which never came, and stayed five minutes beside the real one.
func TestGUISessionSendImagesAnswersTyped(t *testing.T) {
	d, fe, repo, h, _ := guiSetup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	s := guiSignIn(t, d, h)
	for _, text := range []string{"見て", ""} {
		w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/main/send", bearer: s,
			body: map[string]any{"text": text, "images": imgs(testPNG(t, 5, 5))}})
		var r struct {
			OK     bool   `json:"ok"`
			Images int    `json:"images"`
			Text   string `json:"text"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &r) != nil {
			t.Fatalf("send %q: %d %s", text, w.Code, w.Body)
		}
		typed := lastText(fe)
		if !r.OK || r.Images != 1 || r.Text != typed || len(typedImages(r.Text)) != 1 {
			t.Fatalf("send %q answered %+v, typed %q", text, r, typed)
		}
	}
}
