//go:build web

package daemon

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
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

// TestTailscaleJSONDecodes guards the pairing of Go and tailscale.com.
// Go 1.27 runs encoding/json on the v2 implementation, which rejects struct
// tags older Tailscale releases carry (tailscale v1.92's SSHAction had
// `format:nano` on a time.Duration). Such a build decodes nothing the
// control server sends in a tailnet with SSH rules and the node stays
// "starting" forever. Every struct type reachable from what the node reads
// (netmaps, LocalAPI replies, prefs) must decode with this build's
// encoding/json.
func TestTailscaleJSONDecodes(t *testing.T) {
	roots := []any{
		tailcfg.MapResponse{},
		tailcfg.RegisterResponse{},
		ipn.Notify{},
		ipn.Prefs{},
		ipnstate.Status{},
		apitype.WhoIsResponse{},
	}
	seen := map[reflect.Type]bool{}
	for _, r := range roots {
		checkDecodes(t, reflect.TypeOf(r), seen)
	}
	if !seen[reflect.TypeOf(tailcfg.SSHAction{})] {
		t.Fatal("tailcfg.SSHAction was not reached: the walk misses types")
	}
}

// the walk itself must catch the kind of tag that broke tsnet
func TestCheckDecodesCatchesBadTag(t *testing.T) {
	type bad struct {
		D time.Duration `json:"d,omitempty,format:bogus"`
	}
	ft := &fakeT{}
	checkDecodes(ft, reflect.TypeOf(bad{}), map[reflect.Type]bool{})
	if !ft.failed {
		t.Fatal("a struct with an unsupported format tag was not reported")
	}
}

type tb interface {
	Errorf(string, ...any)
	Helper()
}

type fakeT struct{ failed bool }

func (f *fakeT) Errorf(string, ...any) { f.failed = true }
func (f *fakeT) Helper()               {}

var (
	jsonUnmarshaler = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	textUnmarshaler = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// checkDecodes unmarshals {} into every struct type reachable from t,
// through fields (unexported ones too: views keep their struct there),
// pointers, slices, arrays and maps. Types with their own UnmarshalJSON are
// walked but not decoded: {} need not be valid for them.
func checkDecodes(t tb, typ reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Map {
		checkDecodes(t, typ.Key(), seen)
		checkDecodes(t, typ.Elem(), seen)
		return
	}
	if typ.Kind() != reflect.Struct || seen[typ] {
		return
	}
	seen[typ] = true
	pt := reflect.PointerTo(typ)
	if !pt.Implements(jsonUnmarshaler) && !pt.Implements(textUnmarshaler) {
		if err := json.Unmarshal([]byte("{}"), reflect.New(typ).Interface()); err != nil && !strings.Contains(err.Error(), "unexported") {
			t.Errorf("%s: %v", typ, err)
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		checkDecodes(t, typ.Field(i).Type, seen)
	}
}

func TestBackendError(t *testing.T) {
	cases := []struct {
		line, want string
		ok         bool
	}{
		{"Received error: PollNetMap: response: json: cannot unmarshal", "PollNetMap: response: json: cannot unmarshal", true},
		{"[v1] Received error: EOF", "", false},
		{"control: [v1] PollNetMap: stream=true ep=[]", "", false},
	}
	for _, c := range cases {
		got, ok := backendError(c.line)
		if got != c.want || ok != c.ok {
			t.Errorf("backendError(%q) = %q, %v; want %q, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

// backend errors reach pekoed.log, which the agents' sandbox can read
func TestRedactBackendError(t *testing.T) {
	in := "register request: http 410: auth path not found: https://login.tailscale.com/a/1a2b3c4d; key tskey-auth-kAbC123-XyZ456"
	got := redactBackendError(in)
	if strings.Contains(got, "/a/") || strings.Contains(got, "tskey-") {
		t.Errorf("secret left in %q", got)
	}
	if !strings.Contains(got, "http 410: auth path not found") {
		t.Errorf("the error itself was dropped: %q", got)
	}
}
