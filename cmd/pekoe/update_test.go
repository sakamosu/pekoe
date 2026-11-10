package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/daemon"
	"pekoe/internal/selfupdate"
	"pekoe/internal/testenv"
)

// fakeGitHub serves releases of owner/repo from memory (private: refuses
// requests without a token, as GitHub does) and counts the requests.
type fakeGitHub struct {
	srv      *testenv.Server
	tag      string
	assets   map[string][]byte
	requests int
	auth     map[string]bool
}

func newFakeGitHub(t *testing.T, tag string) *fakeGitHub {
	f := &fakeGitHub{tag: tag, assets: map[string][]byte{}, auth: map[string]bool{}}
	f.srv = testenv.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		f.auth[r.Header.Get("Authorization")] = true
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		switch {
		case r.URL.Path == "/repos/owner/repo/releases/latest" || r.URL.Path == "/repos/owner/repo/releases/tags/"+f.tag:
			rel := map[string]any{"tag_name": f.tag}
			var assets []map[string]any
			for name := range f.assets {
				assets = append(assets, map[string]any{"id": 1, "name": name, "url": f.srv.URL + "/assets/" + name})
			}
			rel["assets"] = assets
			json.NewEncoder(w).Encode(rel)
		case strings.HasPrefix(r.URL.Path, "/assets/") && r.Header.Get("Accept") == "application/octet-stream":
			b, ok := f.assets[strings.TrimPrefix(r.URL.Path, "/assets/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		default:
			http.Error(w, `{"message":"Not Found"}`, 404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// add puts an archive for darwin/arm64 (web or not) in the release, with
// the binary's content, and rewrites checksums.txt.
func (f *fakeGitHub) add(t *testing.T, web bool, bin string) {
	name := selfupdate.ArchiveName(f.tag, "darwin", "arm64", web)
	top := strings.TrimSuffix(name, ".tar.gz")
	f.assets[name] = tarGz(t, map[string]string{
		top + "/bin/pekoe":                           bin,
		top + "/README.md":                           "readme",
		top + "/share/pekoe/nvim/plugin/pekoe.lua":   "-- new plugin",
		top + "/share/pekoe/extension/manifest.json": `{"new":true}`,
	})
	var sums strings.Builder
	for n, b := range f.assets {
		if n == "checksums.txt" {
			continue
		}
		s := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(s[:]), n)
	}
	f.assets["checksums.txt"] = []byte(sums.String())
}

func tarGz(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// testPrefix is `make install` in a temporary prefix: the binary, pekoed,
// links to a checkout, and pekoe's data in share/pekoe.
type testPrefix struct {
	exe, share, repo string
}

func newTestPrefix(t *testing.T) testPrefix {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := testPrefix{exe: filepath.Join(root, "prefix", "bin", "pekoe"), share: filepath.Join(root, "prefix", "share", "pekoe"), repo: filepath.Join(root, "repo")}
	mustWrite(t, p.exe, "old binary")
	os.Symlink("pekoe", filepath.Join(root, "prefix", "bin", "pekoed"))
	mustWrite(t, filepath.Join(p.share, "state.db"), "db")
	mustWrite(t, filepath.Join(p.share, "human-token"), "secret")
	mustWrite(t, filepath.Join(p.repo, "nvim", "plugin", "pekoe.lua"), "-- repo")
	mustWrite(t, filepath.Join(p.repo, "extension", "manifest.json"), "repo")
	os.Symlink(filepath.Join(p.repo, "nvim"), filepath.Join(p.share, "nvim"))
	os.Symlink(filepath.Join(p.repo, "extension"), filepath.Join(p.share, "extension"))
	return p
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// unchanged: the prefix is as newTestPrefix left it.
func (p testPrefix) unchanged(t *testing.T) {
	t.Helper()
	if got := mustRead(t, p.exe); got != "old binary" {
		t.Errorf("binary replaced: %q", got)
	}
	for _, d := range []string{"nvim", "extension"} {
		if l, err := os.Readlink(filepath.Join(p.share, d)); err != nil || l != filepath.Join(p.repo, d) {
			t.Errorf("%s changed: %q %v", d, l, err)
		}
	}
}

type testUpdater struct {
	*updater
	out       *bytes.Buffer
	restarted string
}

func newTestUpdater(t *testing.T, gh *fakeGitHub, p testPrefix, current string, web bool) *testUpdater {
	out := &bytes.Buffer{}
	tu := &testUpdater{out: out}
	tu.updater = &updater{
		gh:            &selfupdate.GitHub{API: gh.srv.URL, Repo: "owner/repo", Token: "tok", HTTP: gh.srv.Client()},
		current:       current,
		web:           web,
		goos:          "darwin",
		goarch:        "arm64",
		exe:           func() (string, error) { return p.exe, nil },
		human:         func() error { return nil },
		in:            bufio.NewReader(strings.NewReader("")),
		out:           out,
		err:           out,
		daemonRunning: func() bool { return false },
		restart:       func(exe string) error { tu.restarted = exe; return nil },
	}
	return tu
}

func TestUpdateInstallsLatest(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0")
	gh.add(t, false, "new default")
	gh.add(t, true, "new web")
	for _, c := range []struct {
		web  bool
		want string
	}{{false, "new default"}, {true, "new web"}} {
		p := newTestPrefix(t)
		u := newTestUpdater(t, gh, p, "0.1.0-59-g3761d74", c.web)
		u.daemonRunning = func() bool { return true }
		if err := u.run(context.Background(), updateOpts{yes: true}); err != nil {
			t.Fatalf("web=%v: %v\n%s", c.web, err, u.out)
		}
		if got := mustRead(t, p.exe); got != c.want {
			t.Errorf("web=%v: binary %q", c.web, got)
		}
		if got := mustRead(t, filepath.Join(p.share, "nvim", "plugin", "pekoe.lua")); got != "-- new plugin" {
			t.Errorf("nvim %q", got)
		}
		// the data and the checkout are not touched
		if mustRead(t, filepath.Join(p.share, "state.db")) != "db" || mustRead(t, filepath.Join(p.share, "human-token")) != "secret" {
			t.Error("data changed")
		}
		if mustRead(t, filepath.Join(p.repo, "nvim", "plugin", "pekoe.lua")) != "-- repo" {
			t.Error("checkout changed")
		}
		out := u.out.String()
		for _, want := range []string{"0.1.0-59-g3761d74 → 0.2.0", "now a link to " + filepath.Join(p.repo, "nvim"), "updated pekoe", "chrome://extensions", "no build provenance"} {
			if !strings.Contains(out, want) {
				t.Errorf("web=%v: %q not in\n%s", c.web, want, out)
			}
		}
		if u.restarted != p.exe {
			t.Errorf("restart %q", u.restarted)
		}
	}
	if !gh.auth["Bearer tok"] || gh.auth[""] {
		t.Errorf("Authorization sent: %v", gh.auth)
	}
}

func TestUpdateWebNeedsWebBuild(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0")
	gh.add(t, false, "new default")
	p := newTestPrefix(t)
	u := newTestUpdater(t, gh, p, "0.1.0", true)
	err := u.run(context.Background(), updateOpts{yes: true})
	if err == nil || !strings.Contains(err.Error(), "--no-web") || !errors.Is(err, selfupdate.ErrNoWebBuild) {
		t.Fatalf("err %v", err)
	}
	p.unchanged(t)
	if err := u.run(context.Background(), updateOpts{yes: true, noWeb: true}); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, p.exe); got != "new default" {
		t.Errorf("binary %q", got)
	}
}

func TestUpdateChecksumMismatch(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0")
	gh.add(t, false, "new default")
	name := selfupdate.ArchiveName("v0.2.0", "darwin", "arm64", false)
	gh.assets[name] = append(gh.assets[name], 0) // not what checksums.txt says
	p := newTestPrefix(t)
	u := newTestUpdater(t, gh, p, "0.1.0", false)
	err := u.run(context.Background(), updateOpts{yes: true})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err %v", err)
	}
	p.unchanged(t)
}

func TestUpdateVersions(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0")
	gh.add(t, false, "new default")
	for _, c := range []struct {
		current string
		opts    updateOpts
		updates bool
		msg     string // in the output or the error
	}{
		{"0.2.0", updateOpts{yes: true}, false, "is up to date"},
		{"0.2.0-3-gabcdef0", updateOpts{yes: true}, false, "newer than the latest release"},
		{"0.2.0-3-gabcdef0-dirty", updateOpts{yes: true, version: "v0.2.0"}, false, "--force"},
		{"0.2.0", updateOpts{yes: true, force: true}, true, "updated"},
		{"0.3.0", updateOpts{yes: true, force: true}, true, "goes back"},
		{"0.1.0", updateOpts{yes: true}, true, "updated"},
		{"0.2.0-rc.1", updateOpts{yes: true}, true, "updated"},
		{"dev", updateOpts{yes: true}, true, "cannot be compared"},
		// unknown version, nobody to ask
		{"dev", updateOpts{}, false, "--yes"},
	} {
		p := newTestPrefix(t)
		u := newTestUpdater(t, gh, p, c.current, false)
		err := u.run(context.Background(), c.opts)
		text := u.out.String()
		if err != nil {
			text += err.Error()
		}
		if !strings.Contains(text, c.msg) {
			t.Errorf("%s %+v: %q not in %q", c.current, c.opts, c.msg, text)
		}
		if got := mustRead(t, p.exe) == "new default"; got != c.updates {
			t.Errorf("%s %+v: updated %v, want %v (%v)", c.current, c.opts, got, c.updates, err)
		}
	}
}

func TestUpdateAsksBeforeReplacingLinks(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0")
	gh.add(t, false, "new default")
	p := newTestPrefix(t)
	u := newTestUpdater(t, gh, p, "0.1.0", false)
	u.tty = true
	u.in = bufio.NewReader(strings.NewReader("n\n"))
	if err := u.run(context.Background(), updateOpts{}); err == nil {
		t.Fatal("declined, but no error")
	}
	p.unchanged(t)
	u.in = bufio.NewReader(strings.NewReader("y\nn\n"))
	u.daemonRunning = func() bool { return true }
	if err := u.run(context.Background(), updateOpts{}); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, p.exe) != "new default" {
		t.Error("not updated")
	}
	if u.restarted != "" || !strings.Contains(u.out.String(), "run pekoe daemon restart") {
		t.Errorf("restart declined, yet restarted %q\n%s", u.restarted, u.out)
	}
}

func TestUpdateRefusesAgents(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0")
	gh.add(t, false, "new default")
	p := newTestPrefix(t)
	for name, set := range map[string]func(*updater){
		"session":  func(u *updater) { u.session = "feat-x" },
		"no token": func(u *updater) { u.human = func() error { return daemon.ErrHumanOnly } },
	} {
		u := newTestUpdater(t, gh, p, "0.1.0", false)
		set(u.updater)
		before := gh.requests
		err := u.run(context.Background(), updateOpts{yes: true})
		if !errors.Is(err, daemon.ErrHumanOnly) || !strings.Contains(err.Error(), "only a person can do this") {
			t.Errorf("%s: err %v", name, err)
		}
		if gh.requests != before {
			t.Errorf("%s: asked GitHub before refusing", name)
		}
		p.unchanged(t)
		// looking is for anyone
		u.out.Reset()
		if err := u.run(context.Background(), updateOpts{check: true}); err != nil {
			t.Errorf("%s: --check: %v", name, err)
		}
		if !strings.Contains(u.out.String(), "0.1.0 → 0.2.0 is available") {
			t.Errorf("%s: --check said %q", name, u.out)
		}
		p.unchanged(t)
	}
}

func TestHumanTokenReadable(t *testing.T) {
	t.Setenv("PEKOE_HOME", t.TempDir())
	err := humanTokenReadable()
	if !errors.Is(err, daemon.ErrHumanOnly) || !strings.Contains(err.Error(), "does not exist yet") {
		t.Errorf("no file: %v", err)
	}
	mustWrite(t, filepath.Join(os.Getenv("PEKOE_HOME"), "share", "human-token"), "tok\n")
	if err := humanTokenReadable(); err != nil {
		t.Errorf("readable: %v", err)
	}
}

func TestUpdateRefusesPackageManager(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0")
	gh.add(t, false, "new default")
	u := newTestUpdater(t, gh, testPrefix{}, "0.1.0", false)
	u.exe = func() (string, error) { return "/opt/homebrew/Cellar/pekoe/0.1.0/bin/pekoe", nil }
	err := u.run(context.Background(), updateOpts{yes: true})
	if !errors.Is(err, selfupdate.ErrManaged) {
		t.Fatalf("err %v", err)
	}
}
