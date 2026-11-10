package gitx

// What keeps the git that pekoed runs from running commands an agent wrote.
//
// pekoed runs outside the agents' sandbox, and git runs commands named in
// its configuration: core.fsmonitor on every status, hooks, credential
// helpers, filter and merge drivers selected by .gitattributes, gpg.program,
// core.sshCommand, … An agent can write its worktree (the ".git" file in it
// included, which says where the git directory is), and it may be able to
// write the repository's .git as well (a commit needs objects and refs), so
// all of that configuration is the agent's. Three layers, all applied in
// RunRaw (every git pekoed runs goes through it) and, for gh, in Env:
//
//  1. Pinned git directory. A session's worktree is registered with Pin;
//     its git directory is looked up in the repository's own
//     .git/worktrees/<id> (the entry whose "gitdir" points back at the
//     worktree) and passed as GIT_DIR / GIT_WORK_TREE / GIT_COMMON_DIR,
//     whatever the worktree's ".git" file says. When that file no longer
//     points there, git is not run at all (*TamperedError).
//  2. Overrides. The settings that name a command and hold one value are
//     set on the command line (GIT_CONFIG_COUNT, which beats every config
//     file): no fsmonitor, no hooks, gpg programs and signing from the
//     user's own config (system / global scope) or git's defaults, only the
//     user's credential helpers, no ext:: transport. Editor, pager,
//     askpass, ssh and the git:// proxy come from the environment, which
//     beats config. diff / show / log get --no-ext-diff --no-textconv,
//     fetch / push the default upload-pack / receive-pack.
//  3. Sandbox. What cannot be overridden by name — filter, merge and diff
//     drivers (any name, picked by .gitattributes), git-lfs's own config,
//     the hooks of a remote that is a local path (git clears our settings
//     for the other side), commands of future git versions — runs in
//     pekoed's sandbox (SetSandbox): it cannot read the human token or
//     write pekoe's files. Where no sandbox works, git runs without one
//     (pekoe cannot work without git) with layers 1 and 2 only.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var guard struct {
	mu      sync.Mutex
	wrap    func(argv []string) ([]string, error)
	pins    map[string]*pin // cleaned worktree path → its pin
	trusted map[string]trustedVals
	bare    string // the reason last logged for running git without a sandbox
}

// SetSandbox makes every git command run through wrap (pekoed's sandbox).
// When wrap fails (no sandbox on this machine) git runs without one. nil:
// no sandbox (tests, the CLI).
func SetSandbox(wrap func(argv []string) ([]string, error)) {
	guard.mu.Lock()
	guard.wrap = wrap
	guard.mu.Unlock()
}

func sandboxed(argv []string) []string {
	guard.mu.Lock()
	wrap := guard.wrap
	guard.mu.Unlock()
	if wrap == nil {
		return argv
	}
	w, err := wrap(argv)
	if err != nil {
		guard.mu.Lock()
		if guard.bare != err.Error() {
			guard.bare = err.Error()
			log.Printf("git: running without a sandbox (%v)", err)
		}
		guard.mu.Unlock()
		return argv
	}
	return w
}

// ---- 1. pinned git directories ----

// ErrTampered is wrapped by *TamperedError.
var ErrTampered = errors.New("the worktree's .git was changed")

// TamperedError: the ".git" file of a pinned worktree does not point at the
// git directory the repository has for it.
type TamperedError struct {
	Path string // the worktree
	Want string // the repository's git directory for it ("" if it has none)
	Got  string // what <Path>/.git says (or what it is instead of a file)
}

func (e *TamperedError) Error() string {
	want := e.Want
	if want == "" {
		want = "an entry under its repository's .git/worktrees (there is none)"
	}
	return fmt.Sprintf("%s/.git points at %s, not %s: pekoe runs no git in this worktree until it points back "+
		"(it may have been changed to make pekoe run another repository's settings; if you moved the repository, run git worktree repair)",
		e.Path, e.Got, want)
}

func (e *TamperedError) Unwrap() error { return ErrTampered }

type pin struct {
	repoDir   string
	commonDir string // of repoDir, found once
	gitDir    string // .git/worktrees/<id>, kept while its gitdir points back
}

// Pin registers path as a linked worktree of the repository whose main
// worktree is repoDir: from now on git in path (or below it) uses the git
// directory that repository has for it, never what path/.git says. A path
// that is repoDir itself (a session in the main checkout) needs no pin.
func Pin(path, repoDir string) {
	if path == "" || repoDir == "" || samePath(path, repoDir) {
		return
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.pins == nil {
		guard.pins = map[string]*pin{}
	}
	k := filepath.Clean(path)
	if p := guard.pins[k]; p != nil && p.repoDir == repoDir {
		return
	}
	guard.pins[k] = &pin{repoDir: repoDir}
}

// Unpin forgets path (the session is gone).
func Unpin(path string) {
	guard.mu.Lock()
	delete(guard.pins, filepath.Clean(path))
	guard.mu.Unlock()
}

// pinFor returns the pinned worktree containing dir, if any.
func pinFor(dir string) (string, *pin) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if len(guard.pins) == 0 || dir == "" {
		return "", nil
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if p := guard.pins[d]; p != nil {
			c := *p
			return d, &c
		}
		if up := filepath.Dir(d); up == d {
			return "", nil
		}
	}
}

func savePin(path string, p *pin) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if cur := guard.pins[path]; cur != nil && cur.repoDir == p.repoDir {
		cur.commonDir, cur.gitDir = p.commonDir, p.gitDir
	}
}

// CheckPin tells whether git can run in the pinned worktree path: nil, a
// *TamperedError, or another error (not a worktree any more). Not pinned:
// nil.
func CheckPin(path string) error {
	_, _, err := pinnedEnv(path)
	return err
}

// pinnedEnv is GIT_DIR / GIT_WORK_TREE / GIT_COMMON_DIR for dir when it is
// in a pinned worktree (nil otherwise), after checking that the worktree's
// .git file still points at the repository's entry for it.
func pinnedEnv(dir string) (env []string, pinned bool, err error) {
	path, p := pinFor(dir)
	if p == nil {
		return nil, false, nil
	}
	if p.commonDir == "" {
		c, err := run(context.Background(), p.repoDir, nil, nil, false, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return nil, true, fmt.Errorf("repository of %s: %w", path, err)
		}
		p.commonDir = strings.TrimRight(string(c), "\n")
	}
	if p.gitDir == "" || !pointsBack(p.gitDir, path) {
		p.gitDir = findAdmin(p.commonDir, path)
	}
	savePin(path, p)
	got, err := readGitFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, true, fmt.Errorf("%s is not a git worktree (no .git)", path)
		}
		var te *TamperedError
		if errors.As(err, &te) {
			te.Want = p.gitDir
		}
		return nil, true, err
	}
	if p.gitDir == "" || !samePath(got, p.gitDir) {
		return nil, true, &TamperedError{Path: path, Want: p.gitDir, Got: got}
	}
	return []string{"GIT_DIR=" + p.gitDir, "GIT_WORK_TREE=" + path, "GIT_COMMON_DIR=" + p.commonDir}, true, nil
}

// readGitFile reads "gitdir: X" from path/.git (X made absolute). A
// directory or a symlink there is not what git worktree add makes.
func readGitFile(path string) (string, error) {
	f := filepath.Join(path, ".git")
	fi, err := os.Lstat(f)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		what := "a directory"
		if fi.Mode()&os.ModeSymlink != 0 {
			t, _ := os.Readlink(f)
			what = "a symlink to " + t
		} else if !fi.IsDir() {
			what = "not a regular file"
		}
		return "", &TamperedError{Path: path, Got: what}
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	g, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", &TamperedError{Path: path, Got: strconv.Quote(strings.TrimSpace(line))}
	}
	g = strings.TrimSpace(g)
	if !filepath.IsAbs(g) {
		g = filepath.Join(path, g)
	}
	return filepath.Clean(g), nil
}

// pointsBack: admin's gitdir file names path/.git.
func pointsBack(admin, path string) bool {
	b, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return false
	}
	g := strings.TrimSpace(string(b))
	if !filepath.IsAbs(g) { // worktree.useRelativePaths
		g = filepath.Join(admin, g)
	}
	g = filepath.Clean(g)
	return filepath.Base(g) == ".git" && samePath(filepath.Dir(g), path)
}

// findAdmin is the entry of commonDir/worktrees that points back at path.
func findAdmin(commonDir, path string) string {
	root := filepath.Join(commonDir, "worktrees")
	es, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	// git worktree add names the entry after the directory (plus a number
	// when taken): try that first
	base := filepath.Base(path)
	for _, e := range es {
		if e.Name() == base && e.IsDir() && pointsBack(filepath.Join(root, base), path) {
			return filepath.Join(root, base)
		}
	}
	for _, e := range es {
		if e.IsDir() && pointsBack(filepath.Join(root, e.Name()), path) {
			return filepath.Join(root, e.Name())
		}
	}
	return ""
}

// ---- 2. overrides ----

// fixedConfig: settings that run a command, set to values that run none.
var fixedConfig = [][2]string{
	{"core.fsmonitor", "false"},
	{"core.hooksPath", os.DevNull},
	{"core.alternateRefsCommand", "true"},
	{"protocol.ext.allow", "never"},
}

// signingConfig: settings about signing and checking signatures, taken from
// the user's own config, else git's default. The programs run; the rest
// decides whether they run.
var signingConfig = [][2]string{
	{"gpg.program", "gpg"},
	{"gpg.openpgp.program", "gpg"},
	{"gpg.x509.program", "gpgsm"},
	{"gpg.ssh.program", "ssh-keygen"},
	{"gpg.ssh.defaultkeycommand", "false"},
	{"gpg.format", "openpgp"},
	{"commit.gpgsign", "false"},
	{"tag.gpgsign", "false"},
	{"push.gpgsign", "false"},
	{"merge.verifysignatures", "false"},
	{"log.showsignature", "false"},
}

// trustedKeys matches what is read from the user's config (system and
// global scope) and passed on as overrides.
const trustedKeys = `^(credential\..*helper|core\.sshcommand|gpg\..*|(commit|tag|push)\.gpgsign|merge\.verifysignatures|log\.showsignature)$`

type trustedVals struct {
	kv [][2]string
	at time.Time
}

// trustedTTL: how long the user's own settings are reused per directory.
const trustedTTL = 10 * time.Second

// userConfig returns the trusted settings for git run in dir with pinEnv:
// system and global scope only (includes of those files count as theirs),
// in the order git reads them. Reading config runs nothing.
func userConfig(dir string, pinEnv []string) [][2]string {
	key := dir + "\x00" + strings.Join(pinEnv, "\x00")
	guard.mu.Lock()
	if t, ok := guard.trusted[key]; ok && time.Since(t.at) < trustedTTL {
		guard.mu.Unlock()
		return t.kv
	}
	guard.mu.Unlock()
	cmd := exec.Command("git", "config", "--show-scope", "-z", "--get-regexp", trustedKeys)
	cmd.Dir = dir
	cmd.Env = append(baseEnv(), pinEnv...)
	cmd.Env = append(cmd.Env, configEnv(fixedConfig)...)
	out, _ := cmd.Output() // exit 1: none set
	var kv [][2]string
	f := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		if f[i] != "system" && f[i] != "global" {
			continue
		}
		k, v, _ := strings.Cut(f[i+1], "\n")
		kv = append(kv, [2]string{k, v})
	}
	guard.mu.Lock()
	if guard.trusted == nil || len(guard.trusted) > 256 {
		guard.trusted = map[string]trustedVals{}
	}
	guard.trusted[key] = trustedVals{kv: kv, at: time.Now()}
	guard.mu.Unlock()
	return kv
}

// overrides is the command-line config for git in dir.
func overrides(dir string, pinEnv []string) [][2]string {
	user := userConfig(dir, pinEnv)
	out := append([][2]string(nil), fixedConfig...)
	for _, d := range signingConfig {
		v := d[1]
		for _, u := range user {
			if u[0] == d[0] {
				v = u[1] // the last one wins, as in git
			}
		}
		out = append(out, [2]string{d[0], v})
	}
	// an empty helper empties the list built so far (the repository's
	// helpers included): then the user's own, in their order
	out = append(out, [2]string{"credential.helper", ""})
	for _, u := range user {
		if strings.HasPrefix(u[0], "credential.") && strings.HasSuffix(u[0], ".helper") {
			out = append(out, u)
		}
	}
	return out
}

func configEnv(kv [][2]string) []string {
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(kv))}
	for i, c := range kv {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, c[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, c[1]))
	}
	return env
}

// scrubbed: variables that would point git elsewhere or carry config of
// their own; the parent's are dropped and ours set.
func scrubbed(name string) bool {
	switch name {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_ATTR_SOURCE",
		"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
		"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "GIT_PAGER", "GIT_EXTERNAL_DIFF":
		return true
	}
	return strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

// baseEnv is pekoed's environment for git: scrubbed, no prompts, C locale,
// and the commands git takes from the environment before its config (so
// the repository's config cannot name them) fixed.
func baseEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if k, _, _ := strings.Cut(e, "="); !scrubbed(k) {
			env = append(env, e)
		}
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:", "GIT_PAGER=cat")
	// set but empty: no askpass program, and git skips core.askPass
	if _, ok := os.LookupEnv("GIT_ASKPASS"); !ok {
		env = append(env, "GIT_ASKPASS=")
	}
	// set but empty: no proxy for git://, and git skips core.gitProxy
	if _, ok := os.LookupEnv("GIT_PROXY_COMMAND"); !ok {
		env = append(env, "GIT_PROXY_COMMAND=")
	}
	return env
}

// sshEnv: GIT_SSH_COMMAND beats core.sshCommand, so set it from pekoed's
// environment or the user's own core.sshCommand, else plain ssh.
func sshEnv(user [][2]string) []string {
	if _, ok := os.LookupEnv("GIT_SSH_COMMAND"); ok {
		return nil
	}
	cmd := ""
	for _, u := range user {
		if u[0] == "core.sshcommand" {
			cmd = u[1]
		}
	}
	if cmd == "" {
		if s := os.Getenv("GIT_SSH"); s != "" {
			cmd = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		} else {
			cmd = "ssh"
		}
	}
	return []string{"GIT_SSH_COMMAND=" + cmd}
}

// Env is the environment for a program that runs git itself in dir (gh):
// pekoed's, with the pinned git directory and the overrides. A tampered
// pinned worktree is an error.
func Env(dir string) ([]string, error) {
	pinEnv, _, err := pinnedEnv(dir)
	if err != nil {
		return nil, err
	}
	return gitEnv(dir, pinEnv), nil
}

func gitEnv(dir string, pinEnv []string) []string {
	env := append(baseEnv(), pinEnv...)
	env = append(env, sshEnv(userConfig(dir, pinEnv))...)
	return append(env, configEnv(overrides(dir, pinEnv))...)
}

// subcommandFlags go right after the subcommand.
//   - Commands that print diffs: --no-ext-diff --no-textconv
//     (diff.external and diff.<driver>.command / textconv have any name,
//     so they cannot be overridden one by one).
//   - fetch / push: the program run for the other side. For a remote that
//     is a local path it runs on this machine, and
//     remote.<name>.uploadpack / receivepack keep their first value, so
//     only the command line beats the repository's config.
var subcommandFlags = map[string][]string{
	"diff":      {"--no-ext-diff", "--no-textconv"},
	"show":      {"--no-ext-diff", "--no-textconv"},
	"log":       {"--no-ext-diff", "--no-textconv"},
	"fetch":     {"--upload-pack=git-upload-pack"},
	"pull":      {"--upload-pack=git-upload-pack"},
	"ls-remote": {"--upload-pack=git-upload-pack"},
	"push":      {"--receive-pack=git-receive-pack"},
}

func run(ctx context.Context, dir string, env []string, stdin []byte, pinned bool, args ...string) ([]byte, error) {
	var pinEnv []string
	if pinned {
		var err error
		if pinEnv, _, err = pinnedEnv(dir); err != nil {
			return nil, &Error{Args: args, Err: err}
		}
	}
	// paths in git's output as they are (core.quotePath defaults to true:
	// "\346\227\245..." for non-ASCII names), whatever the user's config
	argv := []string{"git", "-c", "core.quotePath=false"}
	if len(args) > 0 && subcommandFlags[args[0]] != nil {
		argv = append(argv, args[0])
		argv = append(argv, subcommandFlags[args[0]]...)
		argv = append(argv, args[1:]...)
	} else {
		argv = append(argv, args...)
	}
	argv = sandboxed(argv)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(gitEnv(dir, pinEnv), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return out, nil
}
