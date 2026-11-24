package voice

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"pekoe/internal/voice/voicetest"
)

// readRig is a Reader on the fake clock, with the session modes under the
// test's control (read unless set).
type readRig struct {
	t     *testing.T
	clk   *voicetest.Clock
	r     *Reader
	modes map[string]Mode
	due   []Item // what ReadConfig.Due's Reader.Due returned
}

func newReadRig(t *testing.T, edit ...func(*ReadConfig)) *readRig {
	rr := &readRig{t: t, clk: voicetest.NewClock(), modes: map[string]Mode{}}
	cfg := ReadConfig{
		Clock: rr.clk,
		Skip:  DefaultSkip,
		Mode: func(slug string) Mode {
			if m, ok := rr.modes[slug]; ok {
				return m
			}
			return ModeRead
		}, Due: func(slug string) { rr.due = append(rr.due, rr.r.Due(slug)...) },
	}
	for _, e := range edit {
		e(&cfg)
	}
	rr.r = NewReader(cfg)
	return rr
}

// stop sends a Stop with no turn (as Claude Code's) and returns what is
// read of it, now and when its text is due (GapWait later).
func (rr *readRig) stop(slug, text string, again bool) []Item {
	out := rr.r.Stop(slug, "", text, again)
	rr.clk.Advance(GapWait)
	out = append(out, rr.due...)
	rr.due = nil
	return out
}

// unlimited lifts the limits for the tests that compare the whole text.
func unlimited(c *ReadConfig) { c.MaxSentences, c.MaxTurnSentences = 1000, 1000 }

// batches sends a message's batches in order (the last is final) and
// returns the texts read.
func (rr *readRig) batches(slug, turn, msg string, parts []string) []string {
	var got []string
	for i, p := range parts {
		got = append(got, texts(rr.r.Display(slug, Display{Turn: turn, Message: msg, Index: i, Final: i == len(parts)-1, Delta: p}))...)
	}
	return got
}

func (rr *readRig) send(slug, turn, msg string, index int, final bool, delta string) []string {
	return texts(rr.r.Display(slug, Display{Turn: turn, Message: msg, Index: index, Final: final, Delta: delta}))
}

func texts(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}

// lineBatches splits text into batches of n lines each (each with its
// newline).
func lineBatches(text string, n int) []string {
	lines := strings.SplitAfter(text, "\n")
	var out []string
	for i := 0; i < len(lines); i += n {
		out = append(out, strings.Join(lines[i:min(i+n, len(lines))], ""))
	}
	return out
}

// Rule 6.2: the same reply read through any split of its batches gives the
// same sentences (one line each, fence open/body/close apart, body and
// fence open together, the whole at once, and cuts in the middle of lines).
func TestRule6_2ReadBatchSplits(t *testing.T) {
	splits := map[string][]string{
		"whole":        {replyText},
		"line by line": lineBatches(replyText, 1),
		"two lines":    lineBatches(replyText, 2),
		"three lines":  lineBatches(replyText, 3),
		"fence apart": {
			"## 変更点\n`Shape` を足しました。テストも書きました!\n\n",
			"```go\n",
			"func Shape(text string) []string {\n\treturn nil // これは読まない。\n",
			"}\n",
			"```\n",
			"\n| 項目 | 結果 |\n|---|---|\n| test | pass |\n\nThe buffer keeps\nthe partial line. See internal/voice/read.go for details.\n- 1 つ目の項目\n- 2 つ目の項目\n最後の段落です。",
		},
		"body with fence open": {
			"## 変更点\n`Shape` を足しました。テストも書きました!\n\n```go\n",
			"func Shape(text string) []string {\n\treturn nil // これは読まない。\n}\n```\n\n",
			"| 項目 | 結果 |\n|---|---|\n| test | pass |\n\nThe buffer keeps\nthe partial line. See internal/voice/read.go for details.\n- 1 つ目の項目\n- 2 つ目の項目\n最後の段落です。",
		},
	}
	// Cuts inside lines: every 7 bytes on rune boundaries.
	var cuts []string
	rs := []rune(replyText)
	for i := 0; i < len(rs); i += 7 {
		cuts = append(cuts, string(rs[i:min(i+7, len(rs))]))
	}
	splits["mid-line cuts"] = cuts
	// A final batch with no text.
	splits["empty final"] = append(lineBatches(replyText, 1), "")
	for name, parts := range splits {
		if got := strings.Join(parts, ""); got != replyText {
			t.Fatalf("%s: the parts do not make the reply", name)
		}
		rr := newReadRig(t, unlimited)
		if got := rr.batches("s", "t1", "m1", parts); !reflect.DeepEqual(got, replyWant) {
			t.Errorf("%s: read %q, want %q", name, got, replyWant)
		}
	}
}

// Rule 6.2: only complete lines are shaped; the partial line waits for the
// next batch.
func TestRule6_2ReadWaitsForLine(t *testing.T) {
	rr := newReadRig(t, unlimited)
	if got := rr.send("s", "t", "m", 0, false, "前の文。途中"); !reflect.DeepEqual(got, []string(nil)) {
		t.Fatalf("partial line read: %q", got)
	}
	if got := rr.send("s", "t", "m", 1, false, "の文。\n```\n"); !reflect.DeepEqual(got, []string{"前の文。", "途中の文。"}) {
		t.Fatalf("got %q", got)
	}
	if got := rr.send("s", "t", "m", 2, false, "code.\n"); got != nil {
		t.Fatalf("code read: %q", got)
	}
	if got := rr.send("s", "t", "m", 3, true, "```\n後"); !reflect.DeepEqual(got, []string{"後"}) {
		t.Fatalf("final: %q", got)
	}
}

// Rule 6.2: batches that come out of index order are put back in order.
func TestRule6_2ReadIndexSwap(t *testing.T) {
	rr := newReadRig(t, unlimited)
	if got := rr.send("s", "t", "m", 1, false, "```\nsecret()\n"); got != nil {
		t.Fatalf("index 1 before 0 read: %q", got)
	}
	rr.clk.Advance(GapWait - time.Millisecond)
	got := rr.send("s", "t", "m", 0, false, "一文目。\n")
	got = append(got, rr.send("s", "t", "m", 2, true, "```\n二文目。")...)
	if want := []string{"一文目。", "二文目。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
	// A swap with the final batch: the final waits for the one before it.
	rr = newReadRig(t, unlimited)
	got = rr.send("s", "t", "m", 0, false, "a.\n")
	got = append(got, rr.send("s", "t", "m", 2, true, "c.")...)
	got = append(got, rr.send("s", "t", "m", 1, false, "b.\n")...)
	if want := []string{"a.", "b.", "c."}; !reflect.DeepEqual(got, want) {
		t.Errorf("final swapped: read %q, want %q", got, want)
	}
	// A batch sent twice is read once.
	rr = newReadRig(t, unlimited)
	got = rr.send("s", "t", "m", 0, false, "a.\n")
	got = append(got, rr.send("s", "t", "m", 0, false, "a.\n")...)
	if want := []string{"a."}; !reflect.DeepEqual(got, want) {
		t.Errorf("duplicate: read %q", got)
	}
}

// Rule 6.2: a missing index not filled within 2 seconds ends the message:
// nothing after it is read, even when it comes later. Other messages are
// read.
func TestRule6_2ReadGapTimeout(t *testing.T) {
	rr := newReadRig(t, unlimited)
	got := rr.send("s", "t", "m", 0, false, "読む。\n```\n")
	got = append(got, rr.send("s", "t", "m", 2, false, "```\n後。\n")...)
	rr.clk.Advance(GapWait)
	got = append(got, rr.send("s", "t", "m", 1, false, "code()\n")...)
	got = append(got, rr.send("s", "t", "m", 3, true, "最後。")...)
	if want := []string{"読む。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
	if got := rr.batches("s", "t", "m2", []string{"次のメッセージ。"}); !reflect.DeepEqual(got, []string{"次のメッセージ。"}) {
		t.Errorf("next message: %q", got)
	}
	// The wait starts at the first batch past the gap, not at the message's
	// start.
	rr = newReadRig(t, unlimited)
	rr.send("s", "t", "m", 0, false, "a.\n")
	rr.clk.Advance(5 * time.Second)
	rr.send("s", "t", "m", 2, true, "c.")
	rr.clk.Advance(GapWait - time.Millisecond)
	got = rr.send("s", "t", "m", 1, false, "b.\n")
	if want := []string{"b.", "c."}; !reflect.DeepEqual(got, want) {
		t.Errorf("filled in time: read %q, want %q", got, want)
	}
}

// Rule 6.2: no final (Esc): the complete lines are read, the partial line
// never is, and nothing waits forever.
func TestRule6_2ReadNoFinal(t *testing.T) {
	rr := newReadRig(t, unlimited)
	got := rr.send("s", "t", "m", 0, false, "一文目。\n二文目の途")
	if want := []string{"一文目。"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("read %q", got)
	}
	rr.r.Interrupt("s")
	if got := rr.send("s", "t", "m", 1, true, "中。\n"); got != nil {
		t.Errorf("after interrupt: %q", got)
	}
	// The next turn is read.
	if got := rr.batches("s", "t2", "m", []string{"次。"}); !reflect.DeepEqual(got, []string{"次。"}) {
		t.Errorf("next turn: %q", got)
	}
}

// Rule 6.2: at most max_sentences of one message.
func TestRule6_2ReadMaxSentences(t *testing.T) {
	rr := newReadRig(t)
	body := "1。\n2。\n3。\n4。\n5。\n6。\n7。"
	got := rr.batches("s", "t", "m", lineBatches(body, 1))
	if want := []string{"1。", "2。", "3。", "4。", "5。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
	rr = newReadRig(t, func(c *ReadConfig) { c.MaxSentences = 2 })
	if got := rr.batches("s", "t", "m", []string{body}); len(got) != 2 {
		t.Errorf("max_sentences 2: %q", got)
	}
}

// Rule 6.2: at most max_turn_sentences (default 10) in a turn. Preambles
// of one sentence each still leave room for the head of the summary.
func TestRule6_2ReadTurnSentences(t *testing.T) {
	rr := newReadRig(t)
	var got []string
	for i := range 3 {
		got = append(got, rr.batches("s", "t", fmt.Sprintf("pre%d", i), []string{fmt.Sprintf("README を読みます%d。", i)})...)
	}
	summary := "要約1。要約2。要約3。要約4。要約5。要約6。要約7。"
	got = append(got, rr.batches("s", "t", "sum", []string{summary})...)
	want := []string{"README を読みます0。", "README を読みます1。", "README を読みます2。", "要約1。", "要約2。", "要約3。", "要約4。", "要約5。"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("3 preambles: read %q, want %q", got, want)
	}

	// Eight preambles: two sentences of the summary fit in the ten.
	rr = newReadRig(t)
	got = nil
	for i := range 8 {
		got = append(got, rr.batches("s", "t", fmt.Sprintf("pre%d", i), []string{fmt.Sprintf("前置き%d。", i)})...)
	}
	got = append(got, rr.batches("s", "t", "sum", []string{summary})...)
	if len(got) != DefaultMaxTurnSentences || got[8] != "要約1。" || got[9] != "要約2。" {
		t.Errorf("8 preambles: read %q", got)
	}
	// Full: later messages of the turn are not read; the next turn is.
	if got := rr.batches("s", "t", "more", []string{"もう一つ。"}); got != nil {
		t.Errorf("past the limit: %q", got)
	}
	if got := rr.batches("s", "t2", "m", []string{"次のターン。"}); !reflect.DeepEqual(got, []string{"次のターン。"}) {
		t.Errorf("next turn: %q", got)
	}

	// max_turn_sentences below max_sentences: the turn's limit comes first.
	rr = newReadRig(t, func(c *ReadConfig) { c.MaxTurnSentences = 3 })
	if got := rr.batches("s", "t", "m", []string{summary}); len(got) != 3 {
		t.Errorf("max_turn_sentences 3: %q", got)
	}
}

// Rule 6.2: at most 800 characters in a turn; the sentence that would pass
// it is not read, nor anything after it in the turn.
func TestRule6_2ReadTurnRunes(t *testing.T) {
	rr := newReadRig(t)
	long := strings.Repeat("あ", 199) + "。" // 200 runes
	var got []string
	for i := range 3 {
		got = append(got, rr.batches("s", "t", fmt.Sprintf("m%d", i), []string{long + "\n" + long})...)
	}
	if len(got) != 4 {
		t.Fatalf("read %d sentences of 200, want 4 (800 runes)", len(got))
	}
	rr = newReadRig(t)
	got = rr.batches("s", "t", "m", []string{long + "\n" + long + "\n" + long + "\n" + strings.Repeat("い", 198) + "。\n短い。"})
	if len(got) != 4 {
		t.Errorf("read %q", got)
	}
	got = rr.batches("s", "t", "m2", []string{"短い。"})
	if got != nil {
		t.Errorf("after the rune limit: %q", got)
	}
}

// Rule 6.3: a new turn_id drops what is left of the old turn; late
// batches of the old turn are not read. A new turn without
// UserPromptSubmit (the typed-ahead turn) is read.
func TestRule6_3ReadNewTurn(t *testing.T) {
	rr := newReadRig(t)
	got := rr.send("s", "t1", "m", 0, false, "一。\n途中")
	got = append(got, rr.batches("s", "t2", "m", []string{"新しい。"})...)
	got = append(got, rr.send("s", "t1", "m", 1, true, "の文。")...)
	if want := []string{"一。", "新しい。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
	items := rr.r.Display("s", Display{Turn: "t3", Message: "x", Index: 0, Final: true, Delta: "三。"})
	if len(items) != 1 || items[0] != (Item{Kind: KindRead, Slug: "s", Turn: "t3", Message: "x", Text: "三。"}) {
		t.Errorf("item %+v", items)
	}
}

// Rule 6.3: a UserPromptSubmit while running stops the read until the
// next Stop: the batches before that Stop, the Stop's text and the
// stopped turns' late batches are dropped; the next turn (no
// UserPromptSubmit of its own) is read.
func TestRule6_3ReadTypeAhead(t *testing.T) {
	rr := newReadRig(t)
	got := rr.send("s", "t1", "m", 0, false, "読む。\n")
	rr.r.UserPrompt("s", true)
	got = append(got, rr.send("s", "t1", "m", 1, false, "読まない。\n")...)
	got = append(got, rr.batches("s", "t1", "m2", []string{"これも読まない。"})...)
	got = append(got, texts(rr.stop("s", "Stop の本文。", false))...)
	// The last batch of the stopped turn comes after Stop.
	got = append(got, rr.send("s", "t1", "m", 2, true, "遅れた。")...)
	// The typed-ahead prompt's turn has no UserPromptSubmit.
	got = append(got, rr.batches("s", "t2", "m", []string{"先打ちの返事。"})...)
	if want := []string{"読む。", "先打ちの返事。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
}

// Rule 6.3: typed ahead while the turn shows no text yet (thinking): the
// last turn known is the old one, yet the new turn is not read.
func TestRule6_3ReadTypeAheadBeforeText(t *testing.T) {
	rr := newReadRig(t)
	got := rr.batches("s", "t1", "m", []string{"前のターン。"})
	rr.stop("s", "前のターン。", false)
	rr.r.UserPrompt("s", false) // the person starts t2
	rr.r.UserPrompt("s", true)  // and types ahead while it thinks
	got = append(got, rr.batches("s", "t2", "m", []string{"今のターンの要約。"})...)
	got = append(got, texts(rr.stop("s", "今のターンの要約。", false))...)
	got = append(got, rr.batches("s", "t3", "m", []string{"先打ちの返事。"})...)
	if want := []string{"前のターン。", "先打ちの返事。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
}

// Rule 6.3: a UserPromptSubmit while not running does not stop the turn
// it starts.
func TestRule6_3ReadPromptNotRunning(t *testing.T) {
	rr := newReadRig(t)
	rr.batches("s", "t1", "m", []string{"一。"})
	rr.stop("s", "一。", false)
	rr.r.UserPrompt("s", false)
	if got := rr.batches("s", "t2", "m", []string{"二。"}); !reflect.DeepEqual(got, []string{"二。"}) {
		t.Errorf("read %q", got)
	}
	// The Stop way too.
	rr = newReadRig(t)
	rr.r.UserPrompt("s", false)
	if got := texts(rr.stop("s", "返事。", false)); !reflect.DeepEqual(got, []string{"返事。"}) {
		t.Errorf("Stop way: read %q", got)
	}
}

// Rule 6.1: the Stop way reads last_assistant_message within max_sentences,
// not when a MessageDisplay of the turn came, and not after a
// UserPromptSubmit while running.
func TestRule6_1ReadStopWay(t *testing.T) {
	rr := newReadRig(t)
	got := texts(rr.stop("s", "1。2。3。4。5。6。\n```\ncode\n```", false))
	if want := []string{"1。", "2。", "3。", "4。", "5。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
	items := rr.r.Stop("s", "turn-x", "a.", false)
	if len(items) != 1 || items[0].Turn != "turn-x" || items[0].Message != "turn-x" {
		t.Errorf("turn given: %+v", items)
	}
	items = rr.stop("s", "a.", false)
	if len(items) != 1 || items[0].Turn == "" || items[0].Turn == "turn-x" {
		t.Errorf("no turn: %+v", items)
	}

	// A MessageDisplay came in the turn: the Stop text is not read again.
	rr = newReadRig(t)
	rr.batches("s", "t1", "m", []string{"表示した。"})
	if got := rr.stop("s", "表示した。", false); got != nil {
		t.Errorf("Stop after display: %q", texts(got))
	}
	// The display flag is per turn: the next turn's Stop with no display
	// is read, even when a late batch of t1 came after its Stop.
	rr.send("s", "t1", "m2", 0, true, "遅れた。")
	if got := texts(rr.stop("s", "次。", false)); !reflect.DeepEqual(got, []string{"次。"}) {
		t.Errorf("next Stop: %q", got)
	}

	// Typed ahead: the Stop that ends stopping is not read; the next is.
	rr = newReadRig(t)
	rr.r.UserPrompt("s", true)
	if got := rr.stop("s", "読まない。", false); got != nil {
		t.Errorf("stopped Stop: %q", texts(got))
	}
	if got := texts(rr.stop("s", "読む。", false)); !reflect.DeepEqual(got, []string{"読む。"}) {
		t.Errorf("next Stop: %q", got)
	}
}

// Rule 6.3: an interrupt through pekoe stops the last turn: its later
// batches are not read, stopping ends, and the next turn is read.
func TestRule6_3ReadInterrupt(t *testing.T) {
	rr := newReadRig(t)
	rr.send("s", "t1", "m", 0, false, "一。\n")
	rr.r.Interrupt("s")
	if got := rr.batches("s", "t1", "m2", []string{"遅れた。"}); got != nil {
		t.Errorf("late batch: %q", got)
	}
	if got := rr.batches("s", "t2", "m", []string{"次。"}); !reflect.DeepEqual(got, []string{"次。"}) {
		t.Errorf("next turn: %q", got)
	}
	// An interrupt while stopping ends it (no Stop comes after one).
	rr = newReadRig(t)
	rr.r.UserPrompt("s", true)
	rr.r.Interrupt("s")
	if got := rr.batches("s", "t5", "m", []string{"次。"}); !reflect.DeepEqual(got, []string{"次。"}) {
		t.Errorf("after interrupt while stopping: %q", got)
	}
}

// Rule 6.3: the focus moving away (or the mode changing) drops the
// buffers: the messages begun are not read on, new messages are. Text that
// comes while the mode is not read is not read later (6.1).
func TestRule6_3ReadDiscard(t *testing.T) {
	rr := newReadRig(t, unlimited)
	got := rr.send("s", "t", "m", 0, false, "一。\n途中")
	rr.r.Discard("s")
	got = append(got, rr.send("s", "t", "m", 1, true, "の文。")...)
	got = append(got, rr.batches("s", "t", "m2", []string{"新しいメッセージ。"})...)
	if want := []string{"一。", "新しいメッセージ。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}

	rr = newReadRig(t, unlimited)
	rr.modes["s"] = ModeNarrate
	if got := rr.send("s", "t", "m", 0, false, "見ていない。\n"); got != nil {
		t.Fatalf("narrate read: %q", got)
	}
	rr.modes["s"] = ModeRead
	got = rr.send("s", "t", "m", 1, true, "フォーカスが来た。")
	got = append(got, rr.batches("s", "t", "m2", []string{"次のメッセージ。"})...)
	if want := []string{"次のメッセージ。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
	// The Stop way: not read when the mode is not read.
	rr.modes["s"] = ModeMute
	if got := rr.stop("s", "ミュート。", false); got != nil {
		t.Errorf("muted Stop: %q", texts(got))
	}
}

// Sessions are apart: one session's prompt does not stop another's read.
func TestReadSessionsApart(t *testing.T) {
	rr := newReadRig(t)
	rr.send("a", "t", "m", 0, false, "a1。\n")
	rr.send("b", "t", "m", 0, false, "b1。\n")
	rr.r.UserPrompt("a", true)
	if got := rr.send("b", "t", "m", 1, true, "b2。"); !reflect.DeepEqual(got, []string{"b2。"}) {
		t.Errorf("b: %q", got)
	}
	if got := rr.send("a", "t", "m", 1, true, "a2。"); got != nil {
		t.Errorf("a: %q", got)
	}
	rr.r.Removed("a")
	if got := rr.batches("a", "t", "m", []string{"消えた後。"}); !reflect.DeepEqual(got, []string{"消えた後。"}) {
		t.Errorf("after Removed: %q", got)
	}
}

// DiscardAll drops every session's buffers (the focus moved, a mute).
func TestRule6_3ReadDiscardAll(t *testing.T) {
	rr := newReadRig(t, unlimited)
	rr.send("a", "t", "m", 0, false, "a1。\n途中")
	rr.send("b", "t", "m", 0, false, "b1。\n途中")
	rr.r.DiscardAll()
	var got []string
	got = append(got, rr.send("a", "t", "m", 1, true, "の文。")...)
	got = append(got, rr.send("b", "t", "m", 1, true, "の文。")...)
	got = append(got, rr.batches("a", "t", "m2", []string{"新しい。"})...)
	if want := []string{"新しい。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
}

// unreadRig is a readRig that keeps what Unread was told.
func unreadRig(t *testing.T, edit ...func(*ReadConfig)) (*readRig, *[]string) {
	var told []string
	edit = append(edit, func(c *ReadConfig) {
		c.Unread = func(u Unread) { told = append(told, u.Slug+" "+u.Turn+" "+u.Message+" "+u.Why) }
	})
	return newReadRig(t, edit...), &told
}

func wantTold(t *testing.T, what string, got *[]string, want ...string) {
	t.Helper()
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("%s: told %q, want %q", what, *got, want)
	}
	*got = nil
}

// Text that is not read leaves its reason (Unread), once per turn (per
// message for a missing batch and a message given up), and text that is
// read, or cut by max_sentences, leaves none: a reply that goes unheard
// is otherwise in no log (nothing of it reaches the queue).
func TestReadUnreadTold(t *testing.T) {
	rr, told := unreadRig(t)
	rr.batches("s", "t1", "m1", []string{"一。\n", "二。"})
	rr.batches("s", "t1", "m2", []string{"一。二。三。四。五。六。"}) // max_sentences: not told
	wantTold(t, "read", told)

	// the mode: once per turn, with the mode's name; the Stop way too
	rr.modes["s"] = ModeNarrate
	rr.batches("s", "t2", "m1", []string{"見ていない。\n", "まだ。"})
	rr.batches("s", "t2", "m2", []string{"次も。"})
	wantTold(t, "narrate", told, "s t2  mode narrate")
	rr.stop("s", "", false) // ends t2's displayed
	rr.stop("s", "Stop の本文。", false)
	wantTold(t, "the Stop way", told, "s   mode narrate")
	rr.modes["s"] = ModeRead

	// a missing batch: the rest of the message, once
	rr.send("s", "t3", "m1", 0, false, "読む。\n")
	rr.send("s", "t3", "m1", 2, false, "後。\n")
	rr.clk.Advance(GapWait)
	rr.send("s", "t3", "m1", 1, false, "欠け。\n")
	rr.send("s", "t3", "m1", 3, true, "最後。")
	wantTold(t, "gap", told, "s t3 m1 missing batch")

	// dropped (the focus moved): the message begun, once
	rr.send("s", "t3", "m2", 0, false, "一。\n")
	rr.r.Discard("s")
	rr.send("s", "t3", "m2", 1, false, "二。\n")
	rr.send("s", "t3", "m2", 2, true, "三。")
	wantTold(t, "dropped", told, "s t3 m2 dropped")

	// typed ahead, then the turn stopped, then a turn left behind
	rr.r.UserPrompt("s", true)
	rr.batches("s", "t3", "m3", []string{"先打ちの後。"})
	wantTold(t, "typed ahead", told, "s t3  typed ahead")
	rr.stop("s", "", false)
	rr.batches("s", "t3", "m4", []string{"Stop の後の遅れ。"})
	wantTold(t, "stopped", told, "s t3  turn stopped")
	rr.batches("s", "t4", "m1", []string{"新しいターン。"})
	rr.batches("s", "t5", "m1", []string{"もう一つ。"})
	*told = nil
	rr.batches("s", "t4", "m2", []string{"古いターンの遅れ。"})
	wantTold(t, "past", told, "s t4  turn left")

	// the turn limit, once per turn
	rr, told = unreadRig(t, func(c *ReadConfig) { c.MaxTurnSentences = 2 })
	rr.batches("s", "t", "m1", []string{"一。二。三。"})
	rr.batches("s", "t", "m2", []string{"四。"})
	wantTold(t, "turn limit", told, "s t  turn limit")
}

// Rule 6.1: a Stop handled before the MessageDisplay of its turn (the
// hooks run side by side, 3.2) does not read the reply twice: the Stop's
// text waits GapWait and is not read when a batch of the turn comes
// meanwhile. With no batch it is read (the Stop way).
func TestRule6_1ReadStopBeforeDisplay(t *testing.T) {
	rr := newReadRig(t)
	rr.r.UserPrompt("s", false)
	got := texts(rr.r.Stop("s", "", "了解。", false))
	got = append(got, rr.batches("s", "t1", "m", []string{"了解。"})...)
	rr.clk.Advance(GapWait)
	got = append(got, texts(rr.due)...)
	if want := []string{"了解。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
	// the batch's turn is the Stop's: the next turn's Stop with no batch
	// is read (displayed is not left set by the late batch)
	rr.due = nil
	rr.r.UserPrompt("s", false)
	if got := texts(rr.stop("s", "次。", false)); !reflect.DeepEqual(got, []string{"次。"}) {
		t.Errorf("next Stop: %q", got)
	}

	// the window is GapWait: a batch after it is another turn's
	rr = newReadRig(t)
	got = texts(rr.r.Stop("s", "", "一。", false))
	if got != nil {
		t.Errorf("Stop read before its window: %q", got)
	}
	rr.clk.Advance(GapWait)
	got = append(got, texts(rr.due)...)
	got = append(got, rr.batches("s", "t2", "m", []string{"二。"})...)
	if want := []string{"一。", "二。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after the window: read %q, want %q", got, want)
	}

	// with no Due set, the waiting text is read at the next hook
	rr = newReadRig(t, func(c *ReadConfig) { c.Due = nil })
	rr.r.Stop("s", "", "一。", false)
	rr.clk.Advance(GapWait)
	if got := texts(rr.r.Stop("s", "", "", false)); !reflect.DeepEqual(got, []string{"一。"}) {
		t.Errorf("no Due: %q", got)
	}

	// a prompt or an interrupt in the window: the waiting text is not read
	// (Q8, 6.3); nor when the read is discarded (the focus moved)
	for name, f := range map[string]func(*Reader){
		"prompt":    func(r *Reader) { r.UserPrompt("s", false) },
		"interrupt": func(r *Reader) { r.Interrupt("s") },
		"discard":   func(r *Reader) { r.Discard("s") },
	} {
		rr = newReadRig(t)
		rr.r.Stop("s", "", "読まない。", false)
		f(rr.r)
		rr.clk.Advance(GapWait)
		if rr.due != nil {
			t.Errorf("%s: read %q", name, texts(rr.due))
		}
	}
}

// Rule 6.3: typed ahead while the turn shows no text yet, and its Stop
// handled before its batch: the batch is still the stopped turn's and is
// not read; the typed-ahead turn after it is.
func TestRule6_3ReadTypeAheadStopBeforeDisplay(t *testing.T) {
	rr := newReadRig(t)
	got := rr.batches("s", "t0", "m", []string{"前。"})
	rr.r.Stop("s", "", "前。", false)
	rr.r.UserPrompt("s", false)
	rr.r.UserPrompt("s", true)
	got = append(got, texts(rr.r.Stop("s", "", "了解。", false))...)
	got = append(got, rr.batches("s", "t1", "m", []string{"了解。"})...)
	rr.clk.Advance(GapWait)
	got = append(got, texts(rr.due)...)
	got = append(got, rr.batches("s", "t2", "m", []string{"先打ちの返事。"})...)
	if want := []string{"前。", "先打ちの返事。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}
}

// Rule 6.1: a Stop hook that blocks the Stop (stop_hook_active on the
// next) goes on in the same turn: the next Stop's text came by
// MessageDisplay and is not read again, whichever comes first.
func TestRule6_1ReadStopHookActive(t *testing.T) {
	rr := newReadRig(t)
	got := rr.batches("s", "t1", "m1", []string{"直す。"})
	got = append(got, texts(rr.stop("s", "直す。", false))...)
	got = append(got, rr.batches("s", "t1", "m2", []string{"直した。"})...)
	got = append(got, texts(rr.stop("s", "直した。", true))...)
	if want := []string{"直す。", "直した。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read %q, want %q", got, want)
	}

	// the blocked turn's Stop before its batch
	rr = newReadRig(t)
	got = rr.batches("s", "t1", "m1", []string{"直す。"})
	got = append(got, texts(rr.stop("s", "直す。", false))...)
	got = append(got, texts(rr.r.Stop("s", "", "直した。", true))...)
	got = append(got, rr.batches("s", "t1", "m2", []string{"直した。"})...)
	rr.clk.Advance(GapWait)
	got = append(got, texts(rr.due)...)
	if want := []string{"直す。", "直した。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Stop first: read %q, want %q", got, want)
	}

	// the Stop way (no MessageDisplay): each Stop's text is read
	rr = newReadRig(t)
	got = texts(rr.stop("s", "直す。", false))
	got = append(got, texts(rr.stop("s", "直した。", true))...)
	if want := []string{"直す。", "直した。"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the Stop way: read %q, want %q", got, want)
	}
}
