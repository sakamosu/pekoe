package theme

import (
	"testing"

	"pekoe/internal/testenv"
)

// TestMain keeps every test away from the real ~/.local/share/pekoe,
// ~/.config/pekoe, ~/worktrees and tmux -L pekoe (internal/testenv).
func TestMain(m *testing.M) { testenv.Main(m) }
