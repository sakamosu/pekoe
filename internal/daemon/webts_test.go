//go:build web

package daemon

import (
	"strings"
	"testing"
)

// The login URL never reaches pekoed.log: the log is readable from the
// agents' sandbox, and whoever opens the URL first adds the node to their
// tailnet. web.status (token only) is where `pekoe web` reads it.
func TestRedactAuthURL(t *testing.T) {
	for _, in := range []string{
		"web: tsnet: To authenticate, visit:\n\n\thttps://login.tailscale.com/a/1a2b3c4d5e6f\n",
		"web: tsnet: go to https://hs.example.com/a/abcDEF123 now",
		`web: tsnet: "https://login.tailscale.com/a/xyz9"`,
	} {
		got := redactAuthURL(in)
		if strings.Contains(got, "/a/") {
			t.Errorf("URL left in %q", got)
		}
		if !strings.Contains(got, "pekoe web") {
			t.Errorf("no pointer to `pekoe web` in %q", got)
		}
	}
	if in := "web: tsnet: listening on https://pekoe.example.ts.net/"; redactAuthURL(in) != in {
		t.Errorf("an ordinary URL was changed: %q", redactAuthURL(in))
	}
}
