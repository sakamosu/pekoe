package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// Events emitted one after another reach a client in that order: a
// goroutine per event let two session.updated swap, and the client kept
// the older copy until the next periodic refresh.
func TestEventsKeepOrderPerConnection(t *testing.T) {
	d, _, _ := setup(t)
	srv, cli := net.Pipe()
	go d.serve(srv)
	t.Cleanup(func() { cli.Close() })
	d.humanToken = "tok"
	enc := json.NewEncoder(cli)
	if err := enc.Encode(Request{ID: json.RawMessage("1"), Method: "subscribe", Params: json.RawMessage(`{"events":["*"]}`), Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(cli)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	cli.SetReadDeadline(time.Now().Add(10 * time.Second))
	if !sc.Scan() { // the subscribe response
		t.Fatal(sc.Err())
	}
	const n = 500
	go func() {
		for i := 0; i < n; i++ {
			d.emit("session.progress", map[string]any{"n": i})
		}
	}()
	for i := 0; i < n; i++ {
		if !sc.Scan() {
			t.Fatalf("after %d events: %v", i, sc.Err())
		}
		var ev struct {
			Data struct {
				N int `json:"n"`
			} `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Data.N != i {
			t.Fatalf("event %d arrived as #%d", ev.Data.N, i)
		}
	}
}
