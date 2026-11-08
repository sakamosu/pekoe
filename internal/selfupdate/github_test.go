package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI serves one release of owner/repo, its assets, and records the
// headers it was asked with.
type fakeAPI struct {
	srv     *httptest.Server
	release string // JSON; "" answers 404
	assets  map[string][]byte
	private bool // without a token: 404, as GitHub answers for a private repository
	auth    []string
	accept  []string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{assets: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.accept = append(f.accept, r.Header.Get("Accept"))
		if f.private && r.Header.Get("Authorization") == "" {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		switch {
		case r.URL.Path == "/repos/owner/repo/releases/latest" || r.URL.Path == "/repos/owner/repo/releases/tags/v0.2.0":
			if f.release == "" {
				http.Error(w, `{"message":"Not Found"}`, 404)
				return
			}
			w.Write([]byte(f.release))
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "json", 415)
				return
			}
			b, ok := f.assets[strings.TrimPrefix(r.URL.Path, "/assets/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/attestations/"):
			http.Error(w, `{"message":"Not Found"}`, 404)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) gh(token string) *GitHub {
	return &GitHub{API: f.srv.URL, Repo: "owner/repo", Token: token, HTTP: f.srv.Client()}
}

func TestLatestSendsToken(t *testing.T) {
	f := newFakeAPI(t)
	f.private = true
	f.release = `{"tag_name":"v0.2.0","assets":[{"id":1,"name":"checksums.txt","url":"` + f.srv.URL + `/assets/checksums.txt"}]}`
	r, err := f.gh("tok").Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.TagName != "v0.2.0" || r.Asset("checksums.txt") == nil {
		t.Fatalf("release %+v", r)
	}
	if f.auth[0] != "Bearer tok" {
		t.Errorf("Authorization %q", f.auth[0])
	}
	if f.accept[0] != "application/vnd.github+json" {
		t.Errorf("Accept %q", f.accept[0])
	}
}

func TestLatestPrivateWithoutToken(t *testing.T) {
	f := newFakeAPI(t)
	f.private = true
	f.release = `{"tag_name":"v0.2.0"}`
	_, err := f.gh("").Latest(context.Background())
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 {
		t.Fatalf("err %v", err)
	}
	for _, want := range []string{"may be private", "gh auth login", "GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q not in %q", want, err)
		}
	}
	if f.auth[0] != "" {
		t.Errorf("sent Authorization %q without a token", f.auth[0])
	}
}

func TestLatestPublicWithoutToken(t *testing.T) {
	f := newFakeAPI(t)
	f.release = `{"tag_name":"v0.2.0"}`
	if _, err := f.gh("").Latest(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLatestRefusesPrerelease(t *testing.T) {
	f := newFakeAPI(t)
	for _, rel := range []string{`{"tag_name":"v0.2.0","prerelease":true}`, `{"tag_name":"v0.2.0","draft":true}`} {
		f.release = rel
		if _, err := f.gh("").Latest(context.Background()); err == nil {
			t.Errorf("%s: no error", rel)
		}
	}
}

func TestByTagAddsV(t *testing.T) {
	f := newFakeAPI(t)
	f.release = `{"tag_name":"v0.2.0","prerelease":true}`
	r, err := f.gh("").ByTag(context.Background(), "0.2.0")
	if err != nil || r.TagName != "v0.2.0" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestNotFoundWithToken(t *testing.T) {
	f := newFakeAPI(t)
	_, err := f.gh("tok").Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gh auth status") {
		t.Fatalf("err %v", err)
	}
}

func TestDownload(t *testing.T) {
	f := newFakeAPI(t)
	f.assets["a.tar.gz"] = []byte("archive bytes")
	a := &Asset{Name: "a.tar.gz", URL: f.srv.URL + "/assets/a.tar.gz"}
	var buf bytes.Buffer
	if err := f.gh("tok").Download(context.Background(), a, &buf, 100); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "archive bytes" {
		t.Errorf("got %q", buf.String())
	}
	if f.auth[0] != "Bearer tok" {
		t.Errorf("Authorization %q", f.auth[0])
	}
	buf.Reset()
	if err := f.gh("tok").Download(context.Background(), a, &buf, 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("limit: %v", err)
	}
}

func TestHasAttestationNone(t *testing.T) {
	f := newFakeAPI(t)
	has, err := f.gh("tok").HasAttestation(context.Background(), strings.Repeat("a", 64))
	if err != nil || has {
		t.Fatalf("%v %v", has, err)
	}
}
