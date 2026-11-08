package daemon

import (
	"strings"
	"testing"

	"pekoe/internal/tips"
)

// tapi's CLAUDE.md tells it to greet with a tip from `pekoe tip` (shared with
// the TUI's record) and how to read more; the example names a real tip.
func TestTapiInstructionsTip(t *testing.T) {
	doc := tapiInstructions(nil)
	for _, want := range []string{"## pekoe の tip", "`pekoe tip`", "`pekoe tip --list`", "`pekoe tip --doc <パス>`", "`tips are off`"} {
		if !strings.Contains(doc, want) {
			t.Errorf("CLAUDE.md lacks %q", want)
		}
	}
	zoom, ok := tips.ByID("zoom")
	if !ok || !strings.Contains(zoom.Short, "^q z") || !strings.Contains(doc, "^q z って知ってた?") {
		t.Error("the greeting example should match the zoom tip")
	}
}
