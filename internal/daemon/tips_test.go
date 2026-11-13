package daemon

import (
	"encoding/json"
	"os"
	"testing"

	"pekoe/internal/paths"
	"pekoe/internal/tips"
)

// tips.next works without the human token (tapi chat's `pekoe tip` runs in
// a sandbox that cannot write pekoe's data directory, so pekoed records the
// tip), records it in the file the TUI reads, and honors [tips] enable.
func TestTipsNextWithoutToken(t *testing.T) {
	d, _, _ := setup(t)
	c := dialPipe(t, d)
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		res, e := c.rawCall(map[string]any{"method": "tips.next"})
		if e != "" {
			t.Fatalf("tips.next without the token: %s", e)
		}
		var r tipsNextR
		if err := json.Unmarshal(res, &r); err != nil || !r.Enabled || r.Tip == nil {
			t.Fatalf("result %s", res)
		}
		if _, ok := tips.ByID(r.Tip.ID); !ok || seen[r.Tip.ID] {
			t.Fatalf("tip %q (seen %v)", r.Tip.ID, seen)
		}
		seen[r.Tip.ID] = true
	}
	b, err := os.ReadFile(paths.TipsFile())
	if err != nil {
		t.Fatal(err)
	}
	var rec struct{ Seen []string }
	if json.Unmarshal(b, &rec) != nil || len(rec.Seen) != 2 || !seen[rec.Seen[0]] || !seen[rec.Seen[1]] {
		t.Fatalf("record %s, shown %v", b, seen)
	}
	// the TUI's record is the same one: its next tip is neither of these
	if next, _ := tips.Open(paths.TipsFile()).Next(); seen[next.ID] {
		t.Fatalf("the TUI would show %s again", next.ID)
	}

	before, _ := os.ReadFile(paths.TipsFile())
	off := false
	d.Global.Tips.Enable = &off
	res, e := c.rawCall(map[string]any{"method": "tips.next"})
	var r tipsNextR
	if e != "" || json.Unmarshal(res, &r) != nil || r.Enabled || r.Tip != nil {
		t.Fatalf("tips off: %s %s", res, e)
	}
	if after, _ := os.ReadFile(paths.TipsFile()); string(after) != string(before) {
		t.Fatalf("recorded with tips off: %s", after)
	}
}
