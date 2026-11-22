package daemon

import (
	"testing"

	"pekoe/internal/state"
)

// A session's PR goes to the GUI with its page on GitHub (pr_url), built
// from the repository's remotes as the inbox's facts are: in the Sessions
// list and the Pipeline's rows. No PR or no GitHub remote: none.
func TestWebPRURL(t *testing.T) {
	d, _, _ := setup(t)
	d.ghRepos.Store("/r/app", ghRepoInfo{Base: "github.com/up/app", Origin: "github.com/me/app", BaseRemote: "upstream"})
	d.ghRepos.Store("/r/local", ghRepoInfo{})
	for _, c := range []struct {
		s    state.Session
		want string
	}{
		{state.Session{Slug: "a", RepoDir: "/r/app", PR: 4}, "https://github.com/up/app/pull/4"},
		{state.Session{Slug: "b", RepoDir: "/r/app"}, ""},
		{state.Session{Slug: "c", RepoDir: "/r/local", PR: 5}, ""},
	} {
		if got := d.toWebSession(&c.s).PRURL; got != c.want {
			t.Errorf("%s: session pr_url %q, want %q", c.s.Slug, got, c.want)
		}
		if got := d.webFlowOf(&c.s, nil).PRURL; got != c.want {
			t.Errorf("%s: flow pr_url %q, want %q", c.s.Slug, got, c.want)
		}
	}
}
