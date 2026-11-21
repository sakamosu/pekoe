package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// The lines tapi's transcript had on 2026-11-21 (the ids cut short): the
// person sent 入力テスト３, interrupted it (ctrl+c) before the first word of
// the reply, and sent 入力テスト４, which Claude Code wrote as another child
// of the same parent. The reply answered 4 only, and Claude Code's screen
// no longer showed 3. A prompt the agent took in the middle of its turn
// (typed while it worked: Claude Code's queue, as a send from the web or
// the TUI is while tapi is busy) has no user line, only a queued_command
// attachment.
var rewoundFixture = []string{
	`{"parentUuid":"p1","type":"system","subtype":"turn_duration","durationMs":1216,"timestamp":"2026-11-20T15:59:57.738Z","uuid":"b774"}`,
	`{"parentUuid":"b774","type":"user","message":{"role":"user","content":"入力テスト３"},"uuid":"fe57","timestamp":"2026-11-20T16:00:02.935Z","origin":{"kind":"human"},"promptSource":"typed"}`,
	`{"parentUuid":"b774","type":"user","message":{"role":"user","content":"入力テスト４"},"uuid":"e5f7","timestamp":"2026-11-20T16:00:05.540Z","origin":{"kind":"human"},"promptSource":"typed"}`,
	`{"parentUuid":"e5f7","type":"assistant","message":{"model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"入力テスト 4 も届いたよ。"}]},"uuid":"a1","timestamp":"2026-11-20T16:00:07.073Z"}`,
	`{"parentUuid":"a1","type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"TOOL_RESULT"}]},"uuid":"r1","timestamp":"2026-11-20T16:00:46Z"}`,
	`{"parentUuid":"r1","isSidechain":false,"attachment":{"type":"queued_command","prompt":"わかったよ","commandMode":"prompt","origin":{"kind":"human"}},"type":"attachment","uuid":"q1","timestamp":"2026-11-20T16:00:47.633Z"}`,
	`{"parentUuid":"q1","attachment":{"type":"queued_command","prompt":"<task-notification>\nTASK_SECRET</task-notification>","commandMode":"task-notification","origin":{"kind":"task-notification"}},"type":"attachment","uuid":"q2","timestamp":"2026-11-20T16:00:48Z"}`,
	`{"parentUuid":"q2","attachment":{"type":"queued_command","prompt":"PEER_SECRET","commandMode":"prompt","origin":{"kind":"peer"}},"type":"attachment","uuid":"q3","timestamp":"2026-11-20T16:00:49Z"}`,
	`{"parentUuid":"q3","attachment":{"type":"queued_command","prompt":[{"type":"text","text":"<pasted_content id=\"ab12\">\n[image] /x.png\n画像も\n</pasted_content id=\"ab12\">"},{"type":"image","source":{"type":"base64","data":"IMG_SECRET"}}],"commandMode":"prompt","origin":{"kind":"human"}},"type":"attachment","uuid":"q4","timestamp":"2026-11-20T16:00:50Z"}`,
	`{"parentUuid":"q4","attachment":{"type":"edited_text_file","filename":"x"},"type":"attachment","uuid":"q5","timestamp":"2026-11-20T16:00:51Z"}`,
}

// Each line typed at the prompt carries its id and parent, so the page can
// tell 入力テスト３ was taken back (markRewound); what the agent took in
// the middle of a turn is the person's words too, not left out.
func TestReadChatRewoundAndQueued(t *testing.T) {
	path := filepath.Join(t.TempDir(), "44444444-dddd.jsonl")
	writeChat(t, path, rewoundFixture)
	page, err := readChat(path, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(page)
	for _, secret := range []string{"TOOL_RESULT", "TASK_SECRET", "PEER_SECRET", "IMG_SECRET"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("%s in the page: %s", secret, b)
		}
	}
	type got struct{ kind, id, parent, text string }
	var gs []got
	for _, it := range page.Items {
		gs = append(gs, got{it.Kind, it.ID, it.Parent, it.Text})
	}
	want := []got{
		{"turn", "", "", ""},
		{"human", "fe57", "b774", "入力テスト３"},
		{"human", "e5f7", "b774", "入力テスト４"},
		{"agent", "", "", "入力テスト 4 も届いたよ。"},
		{"human", "q1", "", "わかったよ"},
		{"human", "q4", "", "[image] /x.png\n画像も"},
	}
	if len(gs) != len(want) {
		t.Fatalf("items:\n%+v\nwant\n%+v", gs, want)
	}
	for i := range want {
		if gs[i] != want[i] {
			t.Errorf("item %d: %+v, want %+v", i, gs[i], want[i])
		}
	}
}

// markRewound and chatBlocks (lib/chat.js, run in node; skipped without node):
// of the person's words with the same parent only the last reached the
// agent; the others are taken back and marked, also when the later one
// comes in a later read (the page runs it over all it has); words with no
// parent (taken mid-turn) and different parents are never marked. The
// bubbles leave out what was taken back and keep what was taken mid-turn.
func TestWebMarkRewound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "44444444-dddd.jsonl")
	writeChat(t, path, rewoundFixture)
	page, err := readChat(path, "")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := json.Marshal(page.Items)
	// the same, read in two: 入力テスト４ in the second read
	first, _ := json.Marshal(page.Items[:2])
	second, _ := json.Marshal(page.Items[2:])
	extra := `[{"kind":"human","id":"x1","parent":"r9","text":"a"},{"kind":"human","id":"x2","parent":"r8","text":"b"},` +
		`{"kind":"human","id":"y1","parent":"s1","text":"打ちなおす前"},{"kind":"pekoe","id":"y2","parent":"s1","text":"[pekoe inbox q1] 答え: yes"},` +
		`{"kind":"human","id":"z1","parent":"u1","text":"1"},{"kind":"human","id":"z2","parent":"u1","text":"2"},{"kind":"human","id":"z3","parent":"u1","text":"3"}]`
	prog := `import { markRewound, chatBlocks } from "lib/chat.js";
const marked = (items) => items.filter((it) => it.rewound).map((it) => it.text);
const all = ` + string(all) + `;
markRewound(all);
const two = ` + string(first) + `;
markRewound(two);
const before = marked(two);
two.push(...` + string(second) + `);
markRewound(two);
const extra = ` + extra + `;
markRewound(extra);
const shown = chatBlocks(all, []).filter((b) => b.type === "me").flatMap((b) => b.msgs.map((m) => m.text));
console.log(JSON.stringify([marked(all), before, marked(two), marked(extra), shown]));
`
	out := runWebModule(t, prog)
	var got [][]string
	if err := json.Unmarshal(out, &got); err != nil || len(got) != 5 {
		t.Fatalf("node output: %v\n%s", err, out)
	}
	want := [][]string{{"入力テスト３"}, {}, {"入力テスト３"}, {"打ちなおす前", "1", "2"},
		{"入力テスト４", "わかったよ", "[image] /x.png\n画像も"}}
	for i := range want {
		if strings.Join(got[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("case %d: marked %q, want %q", i, got[i], want[i])
		}
	}
}
