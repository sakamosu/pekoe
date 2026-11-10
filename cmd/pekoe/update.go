package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/term"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/paths"
	"pekoe/internal/selfupdate"
)

// updater is `pekoe update`, with what it reaches outside as fields so the
// tests can give it a fake GitHub, a temporary prefix and a fake pekoed.
type updater struct {
	gh       *selfupdate.GitHub
	current  string // daemon.Version
	web      bool   // this binary has the web UI (-tags web)
	goos     string
	goarch   string
	exe      func() (string, error) // the running binary, links resolved
	session  string                 // $PEKOE_SESSION
	human    func() error           // nil when the human token can be read
	in       *bufio.Reader
	tty      bool // stdin is a terminal (questions can be answered)
	out, err io.Writer

	// attest runs `gh attestation verify`; nil when gh is not installed
	attest func(file, repo string) error
	// daemonRunning: a pekoed answers on the socket
	daemonRunning func() bool
	// restart runs `<new pekoe> daemon restart`
	restart func(exe string) error
}

type updateOpts struct {
	check, yes, force, noWeb bool
	version                  string // --version vX.Y.Z; "" for the latest
}

func newUpdater() *updater {
	tok, _ := selfupdate.Token()
	u := &updater{
		gh:      selfupdate.NewGitHub(tok),
		current: daemon.Version,
		web:     selfupdate.BuiltWithWeb(),
		goos:    runtime.GOOS,
		goarch:  runtime.GOARCH,
		exe: func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(exe)
		},
		session: os.Getenv("PEKOE_SESSION"),
		human:   humanTokenReadable,
		in:      bufio.NewReader(os.Stdin),
		tty:     term.IsTerminal(int(os.Stdin.Fd())),
		out:     os.Stdout,
		err:     os.Stderr,
		daemonRunning: func() bool {
			cl, err := client.Dial()
			if err != nil {
				return false
			}
			cl.Close()
			return true
		},
		restart: func(exe string) error {
			cmd := exec.Command(exe, "daemon", "restart")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		},
	}
	if _, err := exec.LookPath("gh"); err == nil {
		u.attest = func(file, repo string) error {
			cmd := exec.Command("gh", "attestation", "verify", file, "-R", repo)
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			return cmd.Run()
		}
	}
	return u
}

// humanTokenReadable is the human check of `pekoe update`: only a person
// replaces the binary, as only a person stops pekoed (daemon.stop). It
// cannot ask pekoed (which may be down, or old), so it asks for what a
// person has and an agent must not: the human token file.
func humanTokenReadable() error {
	if client.ReadHumanToken() != "" {
		return nil
	}
	if _, err := os.Stat(paths.HumanTokenFile()); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w (%s does not exist yet: start pekoe once, then run pekoe update)", daemon.ErrHumanOnly, paths.HumanTokenFile())
	}
	return daemon.ErrHumanOnly
}

func cmdUpdate(args []string) error {
	f, pos, err := parseCmd("update", args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return &usageError{use: specs["update"].use, msg: "unexpected argument " + pos[0]}
	}
	return newUpdater().run(context.Background(), updateOpts{
		check:   f["check"] != "",
		yes:     f["yes"] != "",
		force:   f["force"] != "",
		noWeb:   f["no-web"] != "",
		version: f["version"],
	})
}

// updateCheck is `pekoe update --check --json`.
type updateCheck struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"update_available"`
	Known     bool   `json:"current_known"` // false: the current version cannot be compared (dev)
	Web       bool   `json:"web"`
	Archive   string `json:"archive,omitempty"`
	Problem   string `json:"problem,omitempty"` // why update would not install it
}

func (u *updater) printf(format string, a ...any) { fmt.Fprintf(u.out, format, a...) }

// ask asks a yes/no question; yes answers it, and without a terminal
// there is no one to answer.
func (u *updater) ask(q string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if !u.tty {
		return false, fmt.Errorf("%s: no terminal to answer on; run pekoe update --yes", strings.TrimSuffix(q, "?"))
	}
	fmt.Fprintf(u.out, "%s [y/N] ", q)
	line, _ := u.in.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes", nil
}

func (u *updater) run(ctx context.Context, o updateOpts) error {
	// replacing the binary is for people: refuse before anything else
	if !o.check {
		if u.session != "" {
			return fmt.Errorf("pekoe update in session %s: %w", u.session, daemon.ErrHumanOnly)
		}
		if err := u.human(); err != nil {
			return err
		}
	}

	var rel *selfupdate.Release
	var err error
	if o.version != "" {
		rel, err = u.gh.ByTag(ctx, o.version)
	} else {
		rel, err = u.gh.Latest(ctx)
	}
	if err != nil {
		return err
	}
	target, ok := selfupdate.ParseVersion(rel.TagName)
	if !ok {
		return fmt.Errorf("release %s: not a version (vX.Y.Z)", rel.TagName)
	}
	newVer := strings.TrimPrefix(rel.TagName, "v")
	cur, known := selfupdate.ParseVersion(u.current)
	cmp := 0
	if known {
		cmp = selfupdate.Compare(cur, target)
	}

	web := u.web && !o.noWeb
	archive, sums, pickErr := selfupdate.Pick(rel, u.goos, u.goarch, web)
	if errors.Is(pickErr, selfupdate.ErrNoWebBuild) {
		pickErr = fmt.Errorf("this pekoe has the web UI (-tags web), but %w.\n"+
			"  pekoe update --no-web   installs the default build of %s (no web UI)\n"+
			"  make install WEB=1      builds pekoe with the web UI from the source", pickErr, rel.TagName)
	}

	if o.check {
		c := updateCheck{Current: u.current, Latest: newVer, Known: known, Available: !known || cmp < 0, Web: web}
		if archive != nil {
			c.Archive = archive.Name
		}
		if pickErr != nil {
			c.Problem = pickErr.Error()
		}
		if jsonOut {
			printJSON(c)
			return nil
		}
		switch {
		case !known:
			u.printf("pekoe %s (version unknown) · latest release: %s\n", u.current, newVer)
		case cmp < 0:
			u.printf("pekoe %s → %s is available: pekoe update\n", u.current, newVer)
		default:
			u.printf("pekoe %s is up to date (latest release: %s)\n", u.current, newVer)
		}
		if pickErr != nil && (!known || cmp < 0) {
			u.printf("note: %v\n", pickErr)
		}
		return nil
	}

	switch {
	case known && cmp == 0 && !o.force:
		u.printf("pekoe %s is up to date\n", u.current)
		return nil
	case known && cmp > 0 && !o.force:
		if o.version != "" {
			return fmt.Errorf("pekoe %s is newer than %s; pekoe update --version %s --force goes back to it", u.current, newVer, rel.TagName)
		}
		u.printf("pekoe %s is up to date (newer than the latest release %s)\n", u.current, newVer)
		return nil
	}
	if pickErr != nil {
		return pickErr
	}

	exe, err := u.exe()
	if err != nil {
		return fmt.Errorf("finding the running pekoe: %w", err)
	}
	plan, err := selfupdate.NewPlan(exe)
	if err != nil {
		return err
	}
	if err := plan.Check(); err != nil {
		return err
	}

	// what will happen, and the questions it needs
	kind := ""
	if web {
		kind = " (web UI)"
	}
	u.printf("pekoe %s → %s%s · %s\n", u.current, newVer, kind, archive.Name)
	u.printf("  replace %s\n", plan.Exe)
	for _, d := range plan.Dirs {
		switch {
		case d.LinkTo != "":
			u.printf("  replace %s (now a link to %s) with the release's files\n", d.Path, d.LinkTo)
		case d.Exists:
			u.printf("  replace %s\n", d.Path)
		default:
			u.printf("  create  %s\n", d.Path)
		}
	}
	if plan.ShareDir == "" {
		u.printf("  (no %s: only the binary is replaced)\n", filepath.Join(filepath.Dir(filepath.Dir(plan.Exe)), "share", "pekoe"))
	}
	var why []string
	if !known {
		why = append(why, fmt.Sprintf("the current version (%s) cannot be compared with %s", u.current, newVer))
	}
	if known && cmp > 0 {
		why = append(why, fmt.Sprintf("this goes back from %s to %s (--force)", u.current, newVer))
	}
	if links := plan.Links(); len(links) > 0 {
		why = append(why, "the links above point at a source checkout (make install); they become plain directories with the release's files, and the checkout is not touched (make install links them again)")
	}
	if len(why) > 0 {
		for _, w := range why {
			u.printf("note: %s\n", w)
		}
		ok, err := u.ask("Continue?", o.yes)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("not updated")
		}
	}

	// download and check, in a directory of our own
	tmp, err := os.MkdirTemp("", "pekoe-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var sumsBuf strings.Builder
	if err := u.gh.Download(ctx, sums, &sumsBuf, 1<<20); err != nil {
		return err
	}
	want, err := selfupdate.Checksum([]byte(sumsBuf.String()), archive.Name)
	if err != nil {
		return err
	}
	file := filepath.Join(tmp, archive.Name)
	got, err := u.download(ctx, archive, file)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s: SHA-256 %s does not match %s (%s); nothing was replaced", archive.Name, got, selfupdate.ChecksumsName, want)
	}
	u.printf("checked SHA-256 against %s\n", selfupdate.ChecksumsName)
	if err := u.checkProvenance(ctx, file, got); err != nil {
		return err
	}
	staged := filepath.Join(tmp, "staged")
	if err := extractFile(file, staged); err != nil {
		return fmt.Errorf("%s: %w; nothing was replaced", archive.Name, err)
	}

	res, err := selfupdate.Install(plan, staged)
	if err != nil {
		return fmt.Errorf("replacing pekoe: %w (what was replaced has been put back)", err)
	}
	u.printf("updated pekoe %s → %s · %s\n", u.current, newVer, plan.Exe)
	if res.Extension {
		u.printf("Chrome: reload the pekoe extension on chrome://extensions (it was replaced)\n")
	}
	if u.daemonRunning() {
		ok, err := u.ask("pekoed is still running the old version. Restart it now (pekoe daemon restart; agents keep running)?", o.yes)
		if err != nil || !ok {
			u.printf("run pekoe daemon restart when you are ready\n")
			return nil
		}
		return u.restart(plan.Exe)
	}
	return nil
}

// download saves the asset to file and returns its SHA-256.
func (u *updater) download(ctx context.Context, a *selfupdate.Asset, file string) (string, error) {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if err := u.gh.Download(ctx, a, io.MultiWriter(f, h), 512<<20); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkProvenance verifies the build provenance when the release has one.
// Releases made while the repository is private have none (release.yml
// attests only a public one; it is not public yet), so a missing one is a
// warning.
func (u *updater) checkProvenance(ctx context.Context, file, sum string) error {
	has, err := u.gh.HasAttestation(ctx, sum)
	switch {
	case err != nil:
		fmt.Fprintf(u.err, "warning: could not look up the build provenance (%v); checked the SHA-256 only\n", err)
		return nil
	case !has:
		fmt.Fprintf(u.err, "warning: no build provenance (attestation) for this release (releases made while the repository is private have none); checked the SHA-256 only\n")
		return nil
	case u.attest == nil:
		fmt.Fprintf(u.err, "warning: the release has a build provenance, but gh is not installed to verify it; checked the SHA-256 only\n")
		return nil
	}
	if err := u.attest(file, u.gh.Repo); err != nil {
		return fmt.Errorf("gh attestation verify %s: %w; nothing was replaced", filepath.Base(file), err)
	}
	u.printf("verified the build provenance (gh attestation verify)\n")
	return nil
}

func extractFile(file, dst string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	return selfupdate.Extract(f, dst)
}
