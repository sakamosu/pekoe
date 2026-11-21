package daemon

import (
	"strings"
	"testing"
)

// appParts takes top-level declarations out of the phone's page
// (webui/app.js) as they are written, by name: a function (to the "}" that
// closes it at the start of a line) or a const / let (one line, or to the
// "}" closing its object). The page itself needs a browser; its parts run
// in node (runWebModule) with what they call stubbed around them.
func appParts(t *testing.T, names ...string) string {
	t.Helper()
	b, err := webFiles.ReadFile("webui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	for _, name := range names {
		start := -1
		for i, l := range lines {
			for _, p := range []string{"function " + name + "(", "async function " + name + "(", "const " + name + " ", "let " + name + " "} {
				if strings.HasPrefix(l, p) {
					start = i
				}
			}
			if start >= 0 {
				break
			}
		}
		if start < 0 {
			t.Fatalf("app.js: no top-level %s", name)
		}
		end := start
		if strings.HasSuffix(strings.TrimRight(lines[start], " "), "{") {
			for end < len(lines) && !strings.HasPrefix(lines[end], "}") {
				end++
			}
		}
		out = append(out, lines[start:end+1]...)
	}
	return strings.Join(out, "\n")
}

// The tapi chat keeps the newest CHAT_KEEP items (older ones are cut, a
// divider says so). Once it is full, a read that brings new items leaves
// the count as it was: the new bubbles are drawn all the same (the redraw
// does not hang on the count alone), and so is a new conversation's
// divider that comes with no item.
func TestWebChatDrawsPastKeep(t *testing.T) {
	prog := `const TAPI = "+tapi";
let current = TAPI;
const node = () => ({ scrollTop: 0, scrollHeight: 0, replaceChildren() {}, append() {}, querySelectorAll: () => [] });
const nodes = {};
const $ = (id) => (nodes[id] ||= node());
const el = () => node(), txt = () => node();
const selectingIn = () => false, atBottom = () => true, updateJump = () => {}, markRewound = () => {};
const tapiMode = () => "chat", applyTapiMode = () => {}, refreshScreen = () => {};
let screenSig = "";
const drawn = []; // the newest bubble of each redraw
const chatView = (items) => { drawn.push(items.at(-1).text || items.at(-1).kind); return node(); };
let n = 0, next = [];
let conv = false; // the next read starts a new conversation (/clear)
const api = async () => ({ items: next.splice(0), cursor: "c" + n, reset: conv, new_conversation: conv });
const mk = (k) => Array.from({ length: k }, () => ({ kind: "agent", text: "msg " + n++ }));
` + appParts(t, "CHAT_KEEP", "chat", "toBottom", "renderChat", "sameWords", "refreshChat") + `
const step = async (k) => { next = mk(k); await refreshChat(); return drawn.at(-1); };
const got = [await step(CHAT_KEEP - 1), await step(1), await step(1), await step(5), await step(1), await step(0)];
conv = true; got.push(await step(0)); // the divider alone, the count still CHAT_KEEP
console.log(JSON.stringify({ got, items: chat.items.length, draws: drawn.length }));`
	out := strings.TrimSpace(string(runWebModule(t, prog)))
	want := `{"got":["msg 1498","msg 1499","msg 1500","msg 1505","msg 1506","msg 1506","conv"],"items":1500,"draws":6}`
	if out != want {
		t.Fatalf("chat past CHAT_KEEP:\n got %s\nwant %s", out, want)
	}
}

// The images being read belong to the session they were chosen for, like
// the attachments: reading session a's images never leaves b's + off nor
// "reading…" under b's box, nor holds b's send; a's own box shows them
// once they are read.
func TestWebImagesReadingPerTarget(t *testing.T) {
	prog := `const IMG_MAX = 4;
const toasts = [];
const toast = (m) => toasts.push(m);
const mkNode = () => { const n = { kids: [], hidden: true, cls: new Set(), disabled: false,
  classList: { toggle: (c, on) => (on ? n.cls.add(c) : n.cls.delete(c)) },
  append: (...k) => n.kids.push(...k), replaceChildren: () => (n.kids = []) }; return n; };
const nodes = {};
const $ = (id) => (nodes[id] ||= mkNode());
const el = (tag, cls, text) => ({ tag, cls, text });
const thumbs = (box, images) => { box.replaceChildren(); images.forEach((im) => box.append(el("span", "thumb", im.name))); };
const renderComposer = () => {};
let sending = false;
const sessions = [{ slug: "a" }, { slug: "b" }];
const sent = [], opened = [];
const sendTo = (slug) => sent.push(slug), openSendSheet = (slug) => opened.push(slug), openTapiSheet = () => {};
const TAPI = "+tapi";
let done;
const prepareImage = (f) => new Promise((res) => { done = () => res({ name: f.name }); });
let current = "a";
const detailTarget = () => current;
const renderTarget = () => {};
` + appParts(t, "attachments", "preparing", "attached", "dropAttached", "addImages", "renderThumbs", "doSend") + `
const look = () => ({ off: $("d-attach").cls.has("off"), notes: $("d-thumbs").kids.filter((k) => k.cls === "thumb-note").map((k) => k.text) });
const r = {};
const reading = addImages([{ name: "a1" }]);
r.aReading = look();
current = "b"; renderThumbs(); // the page moves to b (route draws b's thumbs)
r.bWhileA = look();
$("d-text").value = "hi";
await doSend({ preventDefault() {} });
r.bSends = sent.join(",");
done(); await reading;
r.bAfterA = look();
r.aHas = attached("a").map((im) => im.name).join(",");
current = "a"; renderThumbs();
r.aBack = look();
console.log(JSON.stringify(r));`
	out := strings.TrimSpace(string(runWebModule(t, prog)))
	want := `{"aReading":{"off":true,"notes":["reading…"]},"bWhileA":{"off":false,"notes":[]},"bSends":"b",` +
		`"bAfterA":{"off":false,"notes":[]},"aHas":"a1","aBack":{"off":false,"notes":["1/4"]}}`
	if out != want {
		t.Fatalf("reading images per target:\n got %s\nwant %s", out, want)
	}
}
