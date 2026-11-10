package testenv

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
)

// PipeListener is a net.Listener in memory, for a stand-in pekoed (or a
// real one's serve loop) that tests reach without binding a unix socket.
// Dial connects to it as if Addr were a socket path; any other path, or a
// closed listener, is refused like a socket nobody listens on.
type PipeListener struct {
	path  string
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// Listen makes a PipeListener answering at path ($PEKOE_SOCK).
func Listen(path string) *PipeListener {
	return &PipeListener{path: path, conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *PipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *PipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *PipeListener) Addr() net.Addr { return &net.UnixAddr{Name: l.path, Net: "unix"} }

// Dial connects to the listener when path is its own, and refuses it
// otherwise (ECONNREFUSED, as a socket without a listener).
func (l *PipeListener) Dial(path string) (net.Conn, error) {
	refused := &net.OpError{Op: "dial", Net: "unix", Addr: &net.UnixAddr{Name: path, Net: "unix"}, Err: syscall.ECONNREFUSED}
	if path != l.path {
		return nil, refused
	}
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		client.Close()
		server.Close()
		return nil, refused
	}
}

// Server is an HTTP server in memory, standing in for httptest.Server where
// a TCP port cannot be bound: URL names it, and Client sends every request
// straight to the handler, never over the network.
type Server struct {
	URL string
	h   http.Handler
}

// NewServer serves h at URL ("http://pekoe-test.invalid", which resolves
// nowhere: only Client reaches it).
func NewServer(h http.Handler) *Server {
	return &Server{URL: "http://pekoe-test.invalid", h: h}
}

// Close is a no-op, for symmetry with httptest.Server.
func (s *Server) Close() {}

// Client returns an http.Client that sends its requests to the handler.
func (s *Server) Client() *http.Client {
	return &http.Client{Transport: handlerTransport{s.h}}
}

type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// the handler sees what a server would: a server-side request
	r := req.Clone(req.Context())
	r.RequestURI = req.URL.RequestURI()
	if r.Body == nil {
		r.Body = http.NoBody
	}
	if r.Host == "" {
		r.Host = req.URL.Host
	}
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	resp := w.Result()
	resp.Request = req
	return resp, nil
}
