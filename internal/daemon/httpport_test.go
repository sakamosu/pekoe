package daemon

import (
	"net"
	"strings"
	"testing"

	"pekoe/internal/testenv"
)

// Before (or without) the extension's HTTP port, daemon.status and
// ext.info do not claim the configured port: another pekoed may hold it.
func TestHTTPPortNotListening(t *testing.T) {
	d, _, _ := setup(t)
	st := call[map[string]any](t, d, "daemon.status", nil)
	if p, _ := st["http_port"].(float64); p != 0 {
		t.Fatalf("http_port %v while not listening", st["http_port"])
	}
	if e, _ := st["http_error"].(string); e == "" {
		t.Fatalf("no http_error: %v", st)
	}
	if err := callErr(d, "ext.info", nil); err == nil || !strings.Contains(err.Error(), "not listening") {
		t.Fatalf("ext.info: %v", err)
	}
}

// The port taken (another pekoed on it): status says why, ext.info
// refuses; a free one is reported as the port pekoed got.
func TestHTTPPortBindFails(t *testing.T) {
	testenv.NeedTCP(t)
	d, _, _ := setup(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	d.Global.HTTP.Port = busy.Addr().(*net.TCPAddr).Port
	d.listenHTTP()
	st := call[map[string]any](t, d, "daemon.status", nil)
	if p, _ := st["http_port"].(float64); p != 0 || !strings.Contains(st["http_error"].(string), "address already in use") {
		t.Fatalf("status with the port taken: %v", st)
	}
	if err := callErr(d, "ext.info", nil); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("ext.info: %v", err)
	}

	d.Global.HTTP.Port = 0 // a free port
	d.listenHTTP()
	t.Cleanup(func() { d.httpSrv.Close() })
	got := d.httpState().port
	st = call[map[string]any](t, d, "daemon.status", nil)
	if p, _ := st["http_port"].(float64); got == 0 || int(p) != got || st["http_error"] != nil {
		t.Fatalf("status listening on %d: %v", got, st)
	}
	info := call[map[string]any](t, d, "ext.info", nil)
	if p, _ := info["port"].(float64); int(p) != got || info["token"] == "" {
		t.Fatalf("ext.info: %v", info)
	}
}
