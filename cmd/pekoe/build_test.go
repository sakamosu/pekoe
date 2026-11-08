package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The default build carries no Tailscale code: pekoe is also used where a
// VPN client inside the binary is not allowed. Only -tags web links it
// (docs/web.md).
func TestDefaultBuildHasNoTailscale(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	for _, c := range []struct {
		tags []string
		want bool
	}{{nil, false}, {[]string{"-tags", "web"}, true}} {
		args := append([]string{"list", "-deps"}, c.tags...)
		out, err := exec.Command("go", append(args, ".")...).Output()
		if err != nil {
			t.Fatalf("go list %v: %v", c.tags, err)
		}
		has := strings.Contains(string(out), "tailscale.com/")
		if has != c.want {
			t.Errorf("tags %v: links tailscale = %v, want %v", c.tags, has, c.want)
		}
	}
}
