//go:build web

package daemon

// The tsnet side of the web UI: pekoed joins the tailnet as its own machine
// ([web] hostname, default "pekoe") and serves HTTPS on :443 there, and only
// there. Nothing in this file runs unless [web] enable = true.
//
// Only built with -tags web (make install WEB=1). The default build has no
// Tailscale code at all (webts_off.go): pekoe is also used where a VPN
// client in the binary is not welcome.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

const webBuilt = true

// startWeb brings the tsnet node up in the background. The first start
// without an auth key waits for a login: the URL goes to pekoed's log and
// to `pekoe web`.
func (d *Daemon) startWeb() {
	cfg := d.cfg().Web
	if !cfg.Enable {
		d.web.set(func(s *webStatus) { *s = webStatus{State: "off"} })
		return
	}
	fail := func(err error) {
		log.Printf("web: %v (web UI disabled)", err)
		d.web.set(func(s *webStatus) { s.State, s.Error = "error", err.Error() })
	}
	d.web.set(func(s *webStatus) { *s = webStatus{Enabled: true, State: "starting"} })
	if len(lowerSet(cfg.AllowUsers)) == 0 {
		fail(errors.New("[web] allow_users is empty: nobody could sign in"))
		return
	}
	if !filepath.IsAbs(cfg.StateDir) {
		// pekoed's working directory is wherever pekoe was first run: a
		// relative state_dir would put the node key inside some repository
		fail(fmt.Errorf("[web] state_dir must be an absolute path: %q", cfg.StateDir))
		return
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		fail(err)
		return
	}
	key := cfg.AuthKey
	if key == "" {
		key = d.authKey // TS_AUTHKEY, already out of the environment
	}
	srv := &tsnet.Server{
		Dir:      cfg.StateDir,
		Hostname: cfg.Hostname,
		AuthKey:  key, // empty: a login URL (web.status; `pekoe web` shows it)
		UserLogf: func(format string, a ...any) {
			// tsnet repeats the login URL every 5 s while it waits
			if strings.HasPrefix(format, "To start this tsnet server") || strings.HasPrefix(format, "AuthLoop:") {
				return
			}
			log.Print(redactAuthURL("web: tsnet: " + fmt.Sprintf(format, a...)))
		},
		// The backend's verbose log is discarded, except the errors it
		// gets from the control server: without them a node that never
		// gets to Running says nothing about why.
		Logf: d.webBackendLogf(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1) // srv.Start's result; Close must not run before it
	var (
		mu      sync.Mutex
		httpSrv *http.Server
	)
	d.web.mu.Lock()
	d.web.stop = func() {
		cancel()
		mu.Lock()
		hs := httpSrv
		mu.Unlock()
		if hs != nil {
			sctx, c := context.WithTimeout(context.Background(), time.Second)
			hs.Shutdown(sctx)
			c()
		}
		// tsnet: Close must not be called before or concurrently with Start
		select {
		case err := <-started:
			if err == nil {
				srv.Close()
			}
		case <-time.After(10 * time.Second):
			log.Printf("web: tsnet did not finish starting; not closed")
		}
	}
	d.web.mu.Unlock()

	go func() {
		err := srv.Start()
		started <- err
		if err != nil {
			fail(err)
			return
		}
		lc, err := srv.LocalClient()
		if err != nil {
			fail(err)
			return
		}
		go d.watchWeb(ctx, srv, lc)
		self, _ := os.Hostname()
		h := d.webHandler(webOpts{
			cfg:   cfg,
			who:   tsWhoIs(lc, self),
			hosts: srv.CertDomains, // read per request: a renamed node keeps working
		})
		for ctx.Err() == nil {
			if err := d.serveWeb(ctx, srv, lc, h, &mu, &httpSrv); err != nil && ctx.Err() == nil {
				// e.g. HTTPS not enabled in the tailnet yet: try again later
				fail(err)
				select {
				case <-ctx.Done():
				case <-time.After(30 * time.Second):
				}
				continue
			}
			return
		}
	}()
}

// serveWeb listens on :443 (blocking until the node is up, i.e. after the
// login on a first start) and serves until the server is shut down.
func (d *Daemon) serveWeb(ctx context.Context, srv *tsnet.Server, lc *local.Client, h http.Handler, mu *sync.Mutex, out **http.Server) error {
	ln, err := srv.ListenTLS("tcp", ":443")
	if err != nil {
		return err
	}
	d.web.set(func(s *webStatus) {
		if s.State == "error" {
			s.State, s.Error = "running", ""
		}
	})
	// The first certificate comes from Let's Encrypt and can take longer
	// than a handshake may: fetch it before the first phone asks.
	for _, dom := range srv.CertDomains() {
		cctx, c := context.WithTimeout(ctx, 2*time.Minute)
		if _, _, err := lc.CertPair(cctx, dom); err != nil {
			log.Printf("web: certificate for %s: %v", dom, err)
		}
		c()
	}
	log.Printf("web: serving %s (allow_users %s)", strings.Join(webURLs(srv.CertDomains()), " "), strings.Join(d.cfg().Web.AllowUsers, ", "))
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	mu.Lock()
	*out = hs
	mu.Unlock()
	if ctx.Err() != nil {
		ln.Close()
		return nil
	}
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func webURLs(domains []string) []string {
	urls := make([]string, 0, len(domains))
	for _, h := range domains {
		urls = append(urls, "https://"+h+"/")
	}
	return urls
}

func (d *Daemon) stopWeb() {
	d.web.mu.Lock()
	stop := d.web.stop
	d.web.stop = nil
	d.web.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// webBackendLogf is tsnet's backend Logf: it keeps only "Received error:"
// (an error from the control server, e.g. a netmap this build cannot
// decode) for `pekoe web`, and logs it when it changes. The backend retries
// with a backoff of a few seconds, so the same error comes again and again.
func (d *Daemon) webBackendLogf() func(string, ...any) {
	var (
		mu       sync.Mutex
		last     string
		lastTime time.Time
	)
	return func(format string, a ...any) {
		msg, ok := backendError(fmt.Sprintf(format, a...))
		if !ok {
			return
		}
		// it goes to pekoed.log, which the agents' sandbox can read:
		// no login URL, no auth key (#89)
		msg = redactBackendError(msg)
		d.web.set(func(s *webStatus) { s.BackendError = msg })
		mu.Lock()
		again := msg == last && time.Since(lastTime) < 10*time.Minute
		if !again {
			last, lastTime = msg, time.Now()
		}
		mu.Unlock()
		if !again {
			log.Printf("web: tsnet backend: %s", msg)
		}
	}
}

// backendError picks the error out of a backend log line. "[v1] Received
// error: EOF" (a verbose line) is the control connection closing: normal.
func backendError(line string) (string, bool) {
	const mark = "Received error: "
	i := strings.Index(line, mark)
	if i < 0 || strings.HasPrefix(line, "[v") {
		return "", false
	}
	return strings.TrimSpace(line[i+len(mark):]), true
}

// authKeyRe matches a Tailscale auth key (tskey-auth-…, tskey-client-…).
var authKeyRe = regexp.MustCompile(`tskey-[0-9A-Za-z_-]+`)

// redactBackendError keeps a backend error safe for pekoed.log and
// web.status: login URLs and auth keys are dropped.
func redactBackendError(s string) string {
	return authKeyRe.ReplaceAllString(redactAuthURL(s), "<auth key>")
}

// webStuckAfter is how long the node may stay "starting" before pekoed.log
// says so (with the backend state): logging in and the first netmap take
// seconds.
const webStuckAfter = time.Minute

// watchWeb keeps the status `pekoe web` shows: the backend state, the URLs
// and, while it needs a login, the URL to open (web.status only: the log
// says to run `pekoe web`, once per URL).
func (d *Daemon) watchWeb(ctx context.Context, srv *tsnet.Server, lc *local.Client) {
	lastURL := ""
	since := time.Now() // when the node last left Running or a login wait
	warned := false
	for {
		st, err := lc.StatusWithoutPeers(ctx)
		if err == nil {
			state := "starting"
			switch st.BackendState {
			case ipn.Running.String():
				state = "running"
			case ipn.NeedsLogin.String(), ipn.NeedsMachineAuth.String():
				state = "needs-login"
			}
			urls := webURLs(srv.CertDomains())
			var backendErr string
			d.web.set(func(s *webStatus) {
				if s.State != "error" {
					s.State = state
				}
				s.AuthURL = st.AuthURL
				s.URLs = urls
				s.Backend = st.BackendState
				if state == "running" {
					s.BackendError = ""
				}
				backendErr = s.BackendError
			})
			if st.AuthURL != "" && st.AuthURL != lastURL {
				// not the URL itself: pekoed.log is readable from the
				// agents' sandbox, and whoever opens the URL first adds
				// this node to their tailnet. web.status needs the token.
				log.Printf("web: waiting for a tailnet login: run `pekoe web` to see the login URL (or start pekoed with TS_AUTHKEY)")
			}
			lastURL = st.AuthURL
			if state != "starting" {
				since, warned = time.Now(), false
			} else if !warned && time.Since(since) > webStuckAfter {
				warned = true
				why := ""
				if backendErr != "" {
					why = "; last backend error: " + backendErr
				}
				log.Printf("web: tsnet still not running after %s (backend state %s%s)", webStuckAfter, st.BackendState, why)
			}
		}
		wait := 2 * time.Second
		if err == nil && st.BackendState == ipn.Running.String() {
			wait = 15 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// authURLRe matches a Tailscale login URL (https://login.tailscale.com/a/…,
// or the same path on another control server).
var authURLRe = regexp.MustCompile(`https?://[^\s"'<>]+/a/[0-9A-Za-z]+`)

// redactAuthURL drops login URLs from a line bound for pekoed.log.
func redactAuthURL(s string) string {
	return authURLRe.ReplaceAllString(s, "<login URL: see `pekoe web`>")
}

// tsWhoIs asks the node who owns a connection's source address. self is
// this machine's hostname: a peer reporting it is the Mac's own Tailscale
// (e.g. in userspace-networking mode, where its 100.x address is on no
// interface and isLocalAddr cannot see it).
func tsWhoIs(lc *local.Client, self string) whoIsFunc {
	self = shortHost(self)
	return func(ctx context.Context, remoteAddr string) (*webPeer, error) {
		res, err := lc.WhoIs(ctx, remoteAddr)
		if err != nil {
			return nil, err
		}
		if res.Node == nil || res.UserProfile == nil {
			return nil, errors.New("unknown peer")
		}
		dev := res.Node.ComputedName
		if dev == "" {
			dev, _, _ = strings.Cut(res.Node.Name, ".")
		}
		host := ""
		if res.Node.Hostinfo.Valid() {
			host = res.Node.Hostinfo.Hostname()
		}
		return &webPeer{
			Login:  res.UserProfile.LoginName,
			Device: dev,
			Tagged: res.Node.IsTagged(),
			Self:   self != "" && shortHost(host) == self,
		}, nil
	}
}

func shortHost(h string) string {
	h, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(h)), ".")
	return h
}
