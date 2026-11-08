package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Where releases come from. Builds of a fork can point them elsewhere:
//
//	-ldflags "-X pekoe/internal/selfupdate.Repo=owner/repo"
var (
	Repo = "sakamosu/pekoe"
	API  = "https://api.github.com"
)

// Release is the part of GitHub's release object pekoe update reads.
type Release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	HTMLURL    string  `json:"html_url"`
	Assets     []Asset `json:"assets"`
}

// Asset is a file attached to a release. URL is the API's URL for it: the
// file itself comes with Accept: application/octet-stream, which works for
// a private repository too (browser_download_url does not, with a token).
type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// Asset returns the release's file called name, or nil.
func (r *Release) Asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// GitHub reads releases of Repo through the REST API at API. Token may be
// empty (a public repository needs none).
type GitHub struct {
	API, Repo, Token string
	HTTP             *http.Client
}

// NewGitHub is the client for this build's Repo and API.
func NewGitHub(token string) *GitHub {
	return &GitHub{API: API, Repo: Repo, Token: token, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// Token finds a GitHub token: $GITHUB_TOKEN, $GH_TOKEN, then `gh auth
// token`. It returns "" when there is none; from names where it came from.
func Token() (token, from string) {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t, "$" + k
		}
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", ""
	}
	if t := strings.TrimSpace(string(out)); t != "" {
		return t, "gh auth token"
	}
	return "", ""
}

// StatusError is a GitHub API answer other than 200.
type StatusError struct {
	Status int
	URL    string
	Msg    string // what to do about it
}

func (e *StatusError) Error() string { return e.Msg }

func (g *GitHub) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return http.DefaultClient
}

func (g *GitHub) get(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "pekoe-update")
	if g.Token != "" {
		// net/http drops it when the asset redirects to another host
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	return g.client().Do(req)
}

// statusError explains a failed request: without a token a private
// repository looks like a missing one (404), so say so.
func (g *GitHub) statusError(resp *http.Response, what string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	detail := ""
	if m.Message != "" {
		detail = ": " + m.Message
	}
	code := resp.StatusCode
	e := &StatusError{Status: code, URL: resp.Request.URL.String()}
	switch {
	case g.Token == "" && (code == 404 || code == 401 || code == 403):
		e.Msg = fmt.Sprintf("cannot read %s of %s (HTTP %d%s) without a GitHub token: the repository may be private. Run `gh auth login` (or set GITHUB_TOKEN) and try again", what, g.Repo, code, detail)
	case code == 401:
		e.Msg = fmt.Sprintf("GitHub refused the token (HTTP 401%s): run `gh auth login` again, or check GITHUB_TOKEN / GH_TOKEN", detail)
	case code == 404:
		e.Msg = fmt.Sprintf("%s of %s not found (HTTP 404%s), or the token cannot read the repository (check `gh auth status`)", what, g.Repo, detail)
	default:
		e.Msg = fmt.Sprintf("reading %s of %s: HTTP %d%s", what, g.Repo, code, detail)
	}
	return e
}

func (g *GitHub) release(ctx context.Context, path, what string) (*Release, error) {
	resp, err := g.get(ctx, strings.TrimSuffix(g.API, "/")+"/repos/"+g.Repo+path, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, g.statusError(resp, what)
	}
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("reading %s of %s: %w", what, g.Repo, err)
	}
	return &r, nil
}

// Latest is the newest published release (GitHub's "latest": never a draft
// nor a prerelease; checked here too).
func (g *GitHub) Latest(ctx context.Context) (*Release, error) {
	r, err := g.release(ctx, "/releases/latest", "the latest release")
	if err != nil {
		return nil, err
	}
	if r.Draft || r.Prerelease {
		return nil, fmt.Errorf("the latest release of %s (%s) is a draft or a prerelease; name a version with --version", g.Repo, r.TagName)
	}
	return r, nil
}

// ByTag is the release of tag ("v0.2.0"; "0.2.0" gets the v).
func (g *GitHub) ByTag(ctx context.Context, tag string) (*Release, error) {
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	r, err := g.release(ctx, "/releases/tags/"+url.PathEscape(tag), "release "+tag)
	if err != nil {
		return nil, err
	}
	if r.Draft {
		return nil, fmt.Errorf("release %s of %s is a draft", tag, g.Repo)
	}
	return r, nil
}

// ErrTooLarge: an asset is bigger than pekoe update accepts.
var ErrTooLarge = errors.New("too large")

// Download writes the asset to w, reading at most limit bytes.
func (g *GitHub) Download(ctx context.Context, a *Asset, w io.Writer, limit int64) error {
	resp, err := g.get(ctx, a.URL, "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return g.statusError(resp, a.Name)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("downloading %s: %w", a.Name, err)
	}
	if n > limit {
		return fmt.Errorf("downloading %s: %w (over %d bytes)", a.Name, ErrTooLarge, limit)
	}
	return nil
}

// HasAttestation reports whether GitHub keeps a build provenance for the
// file with this SHA-256 (hex) in Repo. Releases of a private repository
// have none (release.yml attests only while it is public).
func (g *GitHub) HasAttestation(ctx context.Context, sha256hex string) (bool, error) {
	resp, err := g.get(ctx, strings.TrimSuffix(g.API, "/")+"/repos/"+g.Repo+"/attestations/sha256:"+sha256hex, "application/vnd.github+json")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, g.statusError(resp, "the attestations")
	}
	var r struct {
		Attestations []json.RawMessage `json:"attestations"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&r); err != nil {
		return false, err
	}
	return len(r.Attestations) > 0, nil
}
