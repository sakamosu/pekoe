package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// usageLine is one transcript line of an assistant message with usage.
func usageLine(id string, at time.Time, in, out, cw, cr int) string {
	b, _ := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": at.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"id": id, "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "x"}},
			"usage": map[string]any{"input_tokens": in, "output_tokens": out, "cache_creation_input_tokens": cw, "cache_read_input_tokens": cr}},
	})
	return string(b) + "\n"
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

// Claude Code writes one line per content block of a message, each with the
// message's usage: it counts once. Lines are read once (the cursor moves
// on), a line still being written waits, and other line types are skipped.
func TestReadUsageSumsMessagesOnce(t *testing.T) {
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	now := time.Now()
	appendFile(t, tr, `{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"+
		usageLine("m1", now, 3, 10, 100, 1000)+usageLine("m1", now, 3, 10, 100, 1000)+
		usageLine("m2", now, 1, 20, 0, 2000)+`{"type":"assistant","message":{"id":"m3","usage":{"input_tokens":5`)
	day, c, changed := readUsage(state.UsageCursor{Path: tr})
	want := state.Usage{Input: 4, Output: 30, CacheCreation: 100, CacheRead: 3000}
	if !changed || len(day) != 1 || day[localDay(now.Format(time.RFC3339))] != want || c.LastID != "m2" {
		t.Fatalf("first read: %+v %+v %v", day, c, changed)
	}
	if _, c2, changed := readUsage(c); changed || c2.Offset != c.Offset {
		t.Fatalf("nothing new but the half line: %+v %v", c2, changed)
	}
	// the half line ends; m2 again (one more block of it) does not count again
	appendFile(t, tr, `,"output_tokens":7}}}`+"\n"+usageLine("m3", now, 0, 0, 0, 0))
	day, c, _ = readUsage(c)
	if got := day[localDay(now.Format(time.RFC3339))]; got != (state.Usage{Input: 5, Output: 7}) {
		t.Fatalf("second read: %+v", day)
	}
	appendFile(t, tr, usageLine("m3", now, 9, 9, 9, 9))
	if day, _, _ = readUsage(c); day[localDay(now.Format(time.RFC3339))].Total() != 0 {
		t.Fatalf("a later block of m3 counted: %+v", day)
	}
	// not a regular file: nothing
	if _, _, changed := readUsage(state.UsageCursor{Path: "/dev/zero"}); changed {
		t.Fatal("read /dev/zero")
	}
}

// Lines go to the local date of their timestamp: a session working across
// midnight shows on both days, and --days 1 keeps only today.
func TestStatsDayBoundary(t *testing.T) {
	d, _, _ := setup(t)
	y, m, dd := time.Now().Date()
	midnight := time.Date(y, m, dd, 0, 0, 0, 0, time.Local)
	tr := filepath.Join(t.TempDir(), "tapi.jsonl")
	appendFile(t, tr, usageLine("a", midnight.Add(-time.Minute), 1, 2, 3, 4)+usageLine("b", midnight.Add(time.Minute), 10, 20, 30, 40))
	d.countUsage(usageLife{Slug: TapiSlug}, tr, true)
	now := midnight.Add(time.Hour)
	st, err := d.stats(now, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	today, yesterday := midnight.Format("2006-01-02"), midnight.Add(-time.Minute).Format("2006-01-02")
	if len(st.Rows) != 2 || st.Rows[0].Day != today || st.Rows[1].Day != yesterday ||
		st.Rows[0].Tokens.Total() != 100 || st.Rows[1].Tokens.Total() != 10 || st.Rows[0].Repo != "" || st.Tokens.Total() != 110 {
		t.Fatalf("two days: %+v", st)
	}
	if st, _ := d.stats(now, 1, ""); len(st.Rows) != 1 || st.From != today || st.Tokens.Total() != 100 {
		t.Fatalf("today only: %+v", st)
	}
	// tapi chat belongs to no repository
	if st, _ := d.stats(now, 2, "app"); len(st.Rows) != 0 {
		t.Fatalf("--repo app shows tapi: %+v", st)
	}
}

// A session's usage counts while it runs (its subagents too), the merge
// is recorded when it lands, and both stay after rm; a hook arriving after
// rm adds nothing.
func TestStatsSurviveRm(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	for i := 0; i < 100; i++ {
		if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"}); s.State != state.Creating {
			break
		}
		sleep()
	}
	dir := t.TempDir()
	tr := filepath.Join(dir, "abc.jsonl")
	now := time.Now()
	appendFile(t, tr, usageLine("m1", now, 1, 2, 3, 4))
	appendFile(t, filepath.Join(dir, "abc", "subagents", "agent-1.jsonl"), usageLine("s1", now, 10, 20, 30, 40))
	event(t, d, "PostToolUse", "feat-x", map[string]any{"transcript_path": tr, "tool_name": "Bash"})

	statsNow := func() Stats {
		t.Helper()
		return call[Stats](t, d, "stats", map[string]any{"days": 1})
	}
	var st Stats
	for i := 0; i < 100; i++ { // the hook counts in the background
		if st = statsNow(); st.Tokens.Total() == 110 {
			break
		}
		sleep()
	}
	if st.Tokens != (state.Usage{Input: 11, Output: 22, CacheCreation: 33, CacheRead: 44}) || st.Merged != 0 || len(st.Rows) != 1 || st.Rows[0].Repo != "app" {
		t.Fatalf("live: %+v", st)
	}
	// more work, not yet seen by a hook: stats counts it itself
	appendFile(t, tr, usageLine("m2", now, 100, 0, 0, 0))
	if st = statsNow(); st.Tokens.Total() != 210 {
		t.Fatalf("live, counted by stats: %+v", st)
	}

	d.update("feat-x", func(x *state.Session) { x.Landing, x.PR = true, 7 })
	if err := d.finishLanding("feat-x"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, tr, usageLine("m3", now, 1000, 0, 0, 0)) // the agent's last words, after landing
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-x", "force": true})

	st = statsNow()
	if st.Merged != 1 || len(st.Merges) != 1 || st.Merges[0].PR != 7 || st.Merges[0].Slug != "feat-x" ||
		st.Merges[0].Tokens.Total() != 1210 || st.Tokens.Total() != 1210 || st.Rows[0].Merged != 1 {
		t.Fatalf("after rm: %+v", st)
	}
	recs, _ := d.DB.SessionRecords(now.Add(-time.Hour))
	if len(recs) != 1 || recs[0].Repo != "app" || recs[0].MergedAt == nil || recs[0].EndedAt == nil ||
		recs[0].CreatedAt.IsZero() || recs[0].EndedAt.Before(*recs[0].MergedAt) {
		t.Fatalf("record: %+v", recs)
	}
	if cs, _ := d.DB.UsageCursors("feat-x", "*"); len(cs) != 0 {
		t.Fatalf("cursors left after rm: %+v", cs)
	}
	// a late hook of the removed session: not counted again from the start
	d.noteTranscript("feat-x", tr)
	if st = statsNow(); st.Tokens.Total() != 1210 {
		t.Fatalf("late hook counted: %+v", st)
	}
}

// A slug used again is a new life: its usage and record are its own.
func TestStatsSlugReused(t *testing.T) {
	d, _, repo := setup(t)
	newX := func() state.Session {
		s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
		for i := 0; i < 100; i++ {
			if s = call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"}); s.State != state.Creating {
				break
			}
			sleep()
		}
		return s
	}
	now := time.Now()
	for i := 1; i <= 2; i++ {
		s := newX()
		tr := filepath.Join(t.TempDir(), fmt.Sprintf("%d.jsonl", i))
		appendFile(t, tr, usageLine("m", now, i, 0, 0, 0))
		d.countUsage(usageLife{Slug: s.Slug, Born: state.Born(&s), Repo: s.Repo}, tr, true)
		call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-x", "force": true})
		time.Sleep(10 * time.Millisecond) // created_at differs
	}
	recs, _ := d.DB.SessionRecords(now.Add(-time.Hour))
	if len(recs) != 2 || recs[0].Tokens.Input+recs[1].Tokens.Input != 3 || recs[0].MergedAt != nil {
		t.Fatalf("two lives: %+v", recs)
	}
	if !strings.HasPrefix(recs[0].Slug, "feat-x") {
		t.Fatal(recs[0].Slug)
	}
}
