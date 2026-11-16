package daemon

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"pekoe/internal/gitx"
)

// ghRepoInfo is which GitHub repositories gh is pointed at for a
// repository (its main checkout): Base, where its PRs go and what every gh
// pekoed runs for it is given (--repo), and Origin, where pekoe pushes.
// Both are host/owner/name passed by ghRepoFrom, or "". They differ for a
// fork (gh repo set-default, or an upstream remote): the PR is then opened
// in Base from Origin's owner (headRef).
//
// pekoed decides Base as gh does when it starts in the checkout (gh's
// context.ResolvedRemotes.BaseRepo, not prompting), from what pekoed's own
// git (gitx, guarded) reads: gh itself runs no git (ghCommand). See
// docs/api.md (pekoed が動かす gh).
type ghRepoInfo struct {
	Base, Origin string
}

// Fork tells whether PRs go into another repository than origin.
func (r ghRepoInfo) Fork() bool { return r.Base != "" && r.Base != r.Origin }

// headRef is how gh names branch as the head of a PR in Base: the branch
// itself, or <origin owner>:<branch> for a fork.
func (r ghRepoInfo) headRef(branch string) string {
	if !r.Fork() {
		return branch
	}
	_, rest, _ := strings.Cut(r.Origin, "/")
	owner, _, _ := strings.Cut(rest, "/")
	return owner + ":" + branch
}

// ghRepoInfoOf is ghRepoInfo of repoDir, remembered per directory (as
// pekoed read it first: a later change of the remotes or of gh-resolved
// is seen after pekoed restarts, so what a person approved stays what gh
// is pointed at).
func (d *Daemon) ghRepoInfoOf(repoDir string) ghRepoInfo {
	if repoDir == "" {
		return ghRepoInfo{}
	}
	if v, ok := d.ghRepos.Load(repoDir); ok {
		return v.(ghRepoInfo)
	}
	r := readGHRepo(repoDir)
	d.ghRepos.Store(repoDir, r)
	return r
}

// ghRemote is a remote as gh sees it: its name, the GitHub repository its
// fetch URL names (host/owner/name, the host an ssh alias resolved), and
// its remote.<name>.gh-resolved.
type ghRemote struct {
	Name, Repo, Resolved string
}

// readGHRepo reads repoDir's remotes and gh-resolved with gitx and decides
// ghRepoInfo (pickGHBase).
func readGHRepo(repoDir string) ghRepoInfo {
	out, err := gitx.Run(repoDir, "remote", "-v")
	if err != nil {
		return ghRepoInfo{}
	}
	// no gh-resolved is exit status 1 and no output
	cfg, _ := gitx.Run(repoDir, "config", "--get-regexp", `^remote\..*\.gh-resolved$`)
	return pickGHBase(parseGHRemotes(out, cfg, sshHostOf))
}

// gitRemoteRe is a line of git remote -v (gh's remoteRE).
var gitRemoteRe = regexp.MustCompile(`(.+)\s+(\S+)\s+\((push|fetch)\)`)

// parseGHRemotes reads git remote -v (in git's order) and git config
// --get-regexp of gh-resolved as gh does (git.Client.Remotes): the fetch
// URL of each remote, and the remote name as the key's second part. A
// remote whose URL names no repository ghRepoFrom passes has Repo "".
func parseGHRemotes(remoteV, resolved string, sshHost func(string) string) []ghRemote {
	var rs []ghRemote
	for _, l := range strings.Split(remoteV, "\n") {
		m := gitRemoteRe.FindStringSubmatch(l)
		if m == nil || m[3] != "fetch" {
			continue
		}
		name := strings.TrimSpace(m[1])
		if slices.ContainsFunc(rs, func(r ghRemote) bool { return r.Name == name }) {
			continue
		}
		rs = append(rs, ghRemote{Name: name, Repo: remoteRepo(m[2], sshHost)})
	}
	for _, l := range strings.Split(resolved, "\n") {
		key, val, ok := strings.Cut(l, " ")
		if !ok {
			continue
		}
		parts := strings.SplitN(key, ".", 3)
		if len(parts) < 2 {
			continue
		}
		for i := range rs {
			if rs[i].Name == parts[1] {
				rs[i].Resolved = val
				break
			}
		}
	}
	return rs
}

// remoteRepo is host/owner/name of a remote URL (ghRepoFrom), "" when it
// names none. For an ssh URL the host is what ssh -G says it is (a Host
// alias of ~/.ssh/config resolved, sshHost), and ssh.github.com is
// github.com (go-gh's ssh.Translator).
func remoteRepo(u string, sshHost func(string) string) string {
	repo := ghRepoFrom(webBaseOf(u))
	if repo == "" || !gitx.IsSSH(u) {
		return repo
	}
	host, path, _ := strings.Cut(repo, "/")
	if h := sshHost(host); h != "" {
		host = h
	}
	if strings.EqualFold(host, "ssh.github.com") {
		host = "github.com"
	}
	if r := ghRepoFrom("https://" + strings.ToLower(host) + "/" + path); r != "" {
		return r
	}
	return repo
}

// ghRemoteScore is gh's remoteNameSortScore: upstream, then github, then
// origin, then the rest.
func ghRemoteScore(name string) int {
	switch strings.ToLower(name) {
	case "upstream":
		return 3
	case "github":
		return 2
	case "origin":
		return 1
	}
	return 0
}

// pickGHBase decides ghRepoInfo of rs (git's order) as gh's BaseRepo does
// without a prompt. Only remotes on origin's host count (gh: on a host it
// is logged in to; a PR goes from a fork on the same host), in gh's order
// (ghRemoteScore, then git's). The first with gh-resolved decides: "base",
// that remote; any other value, the owner/name it names ([host/]owner/name
// or a URL) on that remote's host, and when it names none, no repository
// at all (gh fails then). With none, the first remote. No origin, or one
// that names no repository: none either (pekoe pushes there).
func pickGHBase(rs []ghRemote) ghRepoInfo {
	var origin string
	for _, r := range rs {
		if r.Name == "origin" {
			origin = r.Repo
		}
	}
	if origin == "" {
		return ghRepoInfo{}
	}
	host, _, _ := strings.Cut(origin, "/")
	var cands []ghRemote
	for _, r := range rs {
		if h, _, _ := strings.Cut(r.Repo, "/"); r.Repo != "" && h == host {
			cands = append(cands, r)
		}
	}
	slices.SortStableFunc(cands, func(a, b ghRemote) int { return ghRemoteScore(b.Name) - ghRemoteScore(a.Name) })
	for _, r := range cands {
		switch r.Resolved {
		case "":
			continue
		case "base":
			return ghRepoInfo{Base: r.Repo, Origin: origin}
		default:
			return ghRepoInfo{Base: resolvedRepo(r.Resolved, host), Origin: origin}
		}
	}
	return ghRepoInfo{Base: cands[0].Repo, Origin: origin}
}

// resolvedRepo is host/owner/name of a gh-resolved value other than "base"
// (gh's ghrepo.FromFullName, on the remote's host), "" when it names none.
func resolvedRepo(v, host string) string {
	var owner, name string
	if strings.HasPrefix(v, "git@") || strings.Contains(v, "://") {
		// a URL: its owner and name
		_, rest, _ := strings.Cut(ghRepoFrom(webBaseOf(v)), "/")
		owner, name, _ = strings.Cut(rest, "/")
	} else {
		parts := strings.Split(v, "/")
		if slices.Contains(parts, "") {
			return ""
		}
		switch len(parts) {
		case 2:
			owner, name = parts[0], parts[1]
		case 3:
			owner, name = parts[1], parts[2]
		}
	}
	if owner == "" || name == "" {
		return ""
	}
	return ghRepoFrom("https://" + host + "/" + strings.ToLower(owner) + "/" + strings.ToLower(name))
}

// sshHostOf is ssh -G's hostname for host ("" when ssh does not say or is
// not there). Swapped in tests.
var sshHostOf = sshHostname

const sshResolveTimeout = 5 * time.Second

// sshHostname asks ssh -G (which only reads ~/.ssh/config and prints what
// it would connect to) for host's real hostname. host passed ghRepoFrom
// (no leading "-"), and goes after "--" too; the answer must pass it as
// well. ssh runs outside every repository with an environment of its own.
func sshHostname(host string) string {
	if !ghHostRe.MatchString(host) {
		return ""
	}
	path, err := exec.LookPath("ssh")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), sshResolveTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, path, "-G", "--", host)
	c.Dir = string(os.PathSeparator)
	for _, k := range []string{"HOME", "USER", "LOGNAME", "PATH"} {
		if v, ok := os.LookupEnv(k); ok {
			c.Env = append(c.Env, k+"="+v)
		}
	}
	if c.Env == nil {
		c.Env = []string{}
	}
	c.WaitDelay = time.Second
	out, err := c.Output()
	if err != nil {
		return ""
	}
	var got string
	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		if k, v, ok := strings.Cut(s.Text(), " "); ok && k == "hostname" {
			got = v
		}
	}
	if !ghHostRe.MatchString(got) {
		return ""
	}
	return strings.ToLower(got)
}

// ghHostRe is a host as ghRepoFrom passes it.
var ghHostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
