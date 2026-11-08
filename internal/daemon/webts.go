//go:build web

package daemon

// The tsnet side of the web UI: bobad joins the tailnet as its own machine
// ([web] hostname, default "boba") and serves HTTPS on :443 there, and only
// there. Nothing in this file runs unless [web] enable = true.
//
// Only built with -tags web (make install WEB=1). The default build has no
// Tailscale code at all (webts_off.go): boba is also used where a VPN
// client in the binary is not welcome.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

const webBuilt = true

// startWeb brings the tsnet node up in the background. The first start
// without an auth key waits for a login: the URL goes to bobad's log and
// to `boba web`.
func (d *Daemon) startWeb() {
	cfg := d.Global.Web
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
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		fail(err)
		return
	}
	srv := &tsnet.Server{
		Dir:      cfg.StateDir,
		Hostname: cfg.Hostname,
		AuthKey:  cfg.AuthKey, // empty: TS_AUTHKEY, else a login URL
		UserLogf: func(format string, a ...any) { log.Printf("web: tsnet: "+format, a...) },
		// Logf (the backend's verbose log) stays nil: discarded.
	}
	ctx, cancel := context.WithCancel(context.Background())
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
		srv.Close()
	}
	d.web.mu.Unlock()

	go func() {
		if err := srv.Start(); err != nil {
			fail(err)
			return
		}
		lc, err := srv.LocalClient()
		if err != nil {
			fail(err)
			return
		}
		go d.watchWeb(ctx, lc)
		// blocks until the node is up (after the login, on a first start)
		ln, err := srv.ListenTLS("tcp", ":443")
		if err != nil {
			if ctx.Err() == nil {
				fail(err)
			}
			return
		}
		hosts := srv.CertDomains()
		urls := make([]string, 0, len(hosts))
		for _, h := range hosts {
			urls = append(urls, "https://"+h+"/")
		}
		d.web.set(func(s *webStatus) { s.URLs = urls })
		log.Printf("web: serving %s (allow_users %s)", strings.Join(urls, " "), strings.Join(cfg.AllowUsers, ", "))
		h := d.webHandler(webOpts{cfg: cfg, who: tsWhoIs(lc), hosts: hosts})
		hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		mu.Lock()
		httpSrv = hs
		mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
			fail(err)
		}
	}()
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

// watchWeb keeps the status `boba web` shows: the backend state and, while
// it needs a login, the URL to open.
func (d *Daemon) watchWeb(ctx context.Context, lc *local.Client) {
	lastURL := ""
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
			d.web.set(func(s *webStatus) {
				if s.State != "error" {
					s.State = state
				}
				s.AuthURL = st.AuthURL
			})
			if st.AuthURL != "" && st.AuthURL != lastURL {
				log.Printf("web: to join the tailnet, open %s (or run bobad with TS_AUTHKEY)", st.AuthURL)
			}
			lastURL = st.AuthURL
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

// tsWhoIs asks the node who owns a connection's source address.
func tsWhoIs(lc *local.Client) whoIsFunc {
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
		return &webPeer{Login: res.UserProfile.LoginName, Device: dev, Tagged: res.Node.IsTagged()}, nil
	}
}
