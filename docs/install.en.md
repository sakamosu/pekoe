# Install and update

The long form of [Install](../README.md#install) in the README: the Homebrew cask, checking a release (checksums, build provenance, signing and notarization), what `pekoe update` does, and installing the hooks and skills.

## Install

See [Status and requirements](../README.md#status-and-requirements) for what you need. From source:

```sh
make install            # ~/.local/bin/pekoe (+ pekoed); links nvim/ and extension/ into ~/.local/share/pekoe
make install WEB=1      # also build in the iPhone Web UI (-tags web, links Tailscale's tsnet)
pekoe hooks install     # add hooks to ~/.claude/settings.json (asks first, keeps a backup)
pekoe skills install    # optional: link pekoe's skills from ~/.claude/skills (asks first)
make install-launchd    # optional: start pekoed at login
```

The hooks only call `pekoe event` on `SessionStart` `UserPromptSubmit` `Notification` `Stop` `PreToolUse` `PostToolUse` `SessionEnd`. The project's own `.claude/` is never touched. Check with `pekoe hooks status`; remove only pekoe's hooks with `pekoe hooks uninstall`.

### With Homebrew

From v0.4.0 on, macOS and Linux (arm64 / amd64) can install the default build (no Web UI) from the tap [sakamosu/homebrew-tap](https://github.com/sakamosu/homebrew-tap). tmux and git come along as dependencies:

```sh
brew install sakamosu/tap/pekoe
pekoe hooks install     # add hooks to ~/.claude/settings.json (asks first, keeps a backup)
brew upgrade pekoe      # update (pekoe update refuses a Homebrew install), then pekoe daemon restart
```

- It is a cask made from the release archive (`pekoe_<ver>_<os>_<arch>.tar.gz`). It is unpacked into `$(brew --prefix)/Caskroom/pekoe/<ver>/` with the archive's layout (`bin/`, `share/pekoe/`, `contrib/launchd/`), and `pekoe` / `pekoed` are linked into `$(brew --prefix)/bin`. pekoe follows that link, so the nvim plugin and the Chrome extension in `share/pekoe` are found as is
- For the Web UI, install the `_web` archive from a release (below) or build with `make install WEB=1`
- macOS marks what brew downloads as quarantined, and Gatekeeper checks a binary with that mark before it runs. The cask removes the mark after installing. From v0.5.0 the macOS binaries are signed and notarized (see below), so Gatekeeper would let them run anyway, but a bare binary can't carry its notarization ticket and Gatekeeper would check it online the first time; removing the mark skips that check. Releases up to v0.4.0 are only ad-hoc signed, and Gatekeeper refuses them while they carry the mark
- To start pekoed at login, `brew info pekoe` shows the launchd commands. `brew services` doesn't work with a cask

### From a release (prebuilt binaries)

GitHub Releases have builds for darwin / linux × arm64 / amd64. `pekoe_<ver>_<os>_<arch>.tar.gz` is the default build (no Web UI) and `pekoe_<ver>_<os>_<arch>_web.tar.gz` includes the Web UI (`-tags web`, [docs/web.md](web.md)); both have the same layout. Download with `gh` (any logged-in account; for the Web UI build, add `_web` before `.tar.gz` in the archive names and at the end of the directory the `ln -sfn` line points to (`pekoe_${ver}_darwin_arm64_web`)), or with curl without a token:

```sh
ver=$(gh release view -R sakamosu/pekoe --json tagName -q .tagName | sed 's/^v//')   # the latest version (or ver=<version>)
gh release download "v$ver" -R sakamosu/pekoe -p "pekoe_${ver}_darwin_arm64.tar.gz" -p checksums.txt   # use your <os>_<arch>
# without gh, with curl:
#   ver=$(curl -fsSL https://api.github.com/repos/sakamosu/pekoe/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
#   base="https://github.com/sakamosu/pekoe/releases/download/v$ver"
#   curl -fL -O "$base/pekoe_${ver}_darwin_arm64.tar.gz" -O "$base/checksums.txt"
shasum -a 256 --ignore-missing -c checksums.txt
gh attestation verify "pekoe_${ver}_darwin_arm64.tar.gz" -R sakamosu/pekoe \
  --signer-workflow sakamosu/pekoe/.github/workflows/release.yml --source-ref "refs/tags/v$ver"   # built by release.yml from this tag? (v0.1.0 to v0.3.0 have no provenance)
mkdir -p ~/.local/opt ~/.local/bin
tar -xzf "pekoe_${ver}_darwin_arm64.tar.gz" -C ~/.local/opt
ln -sfn ~/.local/opt/"pekoe_${ver}_darwin_arm64" ~/.local/opt/pekoe
ln -sf ~/.local/opt/pekoe/bin/pekoe ~/.local/bin/pekoe
ln -sf ~/.local/opt/pekoe/bin/pekoed ~/.local/bin/pekoed
pekoe version
```

`checksums.txt` sits in the same release, so it only tells you the archive isn't corrupted. `gh attestation verify` checks that the archive was built by this repository's `release.yml` (build provenance signed by GitHub Actions with Sigstore). Provenance is attached only to releases that `release.yml` makes while the repository is public (on a private repository, attestations need GitHub Enterprise Cloud, so `release.yml` skips them). Provenance starts with the releases made after the repository went public (v0.4.0 on); v0.1.0 to v0.3.0 were made while it was private, so they have no provenance and `gh attestation verify` fails for them. `--signer-workflow` and `--source-ref` make sure it was `release.yml` building that very tag (not another workflow or branch of the repository). `release.yml` creates the release as a draft, attests the archives, and only then publishes it, so a published release (v0.4.0 on) always has its provenance.

From v0.5.0 (among pre-releases, from v0.5.0-rc.1), `release.yml` signs the darwin binaries (`bin/pekoe` in every darwin archive, default and `_web`, arm64 and amd64) with a Developer ID Application certificate and has Apple notarize them. `codesign -dv ~/.local/opt/pekoe/bin/pekoe` shows `Authority=Developer ID Application: Yuki Sakamoto` and `TeamIdentifier=K9F9554C8G`, and `spctl -a -vvv -t install ~/.local/opt/pekoe/bin/pekoe` shows `source=Notarized Developer ID`. A bare binary can't have its notarization ticket stapled, so Gatekeeper checks it online the first time it runs a quarantined copy. v0.1.0 to v0.4.0 carry only the Go linker's ad-hoc signature: `codesign -dv` shows `Signature=adhoc`.

An archive contains `bin/pekoe`, `bin/pekoed` (→ pekoe), `share/pekoe/nvim/`, `share/pekoe/extension/`, `contrib/launchd/`, plus `README.md`, `README.ja.md`, `LICENSE` and `THIRD_PARTY_LICENSES` (licenses of the dependencies, see [License](../README.md#license)). `bin/pekoe` looks for `../share/pekoe/nvim` relative to its real location, so with the symlinks above the nvim plugin is found as is. To start it with launchd, run `sed -e "s|PEKOE_BIN|$HOME/.local/bin/pekoe|" -e "s|HOME|$HOME|g" ~/.local/opt/pekoe/contrib/launchd/dev.pekoe.pekoed.plist > ~/Library/LaunchAgents/dev.pekoe.pekoed.plist` and then `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.pekoe.pekoed.plist`.

The default build contains no Tailscale code at all. After reinstalling, run `pekoe daemon restart` to switch pekoed to the new binary (agents keep running inside tmux; if launchd runs pekoed, it is restarted through launchd).

### Updating

```sh
pekoe update --check    # only check whether there is a newer release (anyone can run it)
pekoe update            # replace pekoe with the latest release
pekoe update --version vX.Y.Z --force   # a specific version (--force to go back)
pekoe update --allow-unverified         # go on even if the build provenance can't be verified (see below)
```

`pekoe update` takes the latest GitHub release (`releases/latest`, excluding drafts and prereleases) and picks the archive for the current OS, architecture and build (the `_web` archive if the current pekoe has the Web UI).

- Nothing happens if the current version is the latest or newer. A `make install` version (`git describe`, e.g. `0.1.0-59-g3761d74`) counts as "59 commits after 0.1.0". Versions that can't be compared, such as `dev`, ask first
- The archive is checked against the SHA-256 in `checksums.txt`, and nothing is replaced if it doesn't match. `checksums.txt` sits in the same release, so the build provenance (attestation) is what shows the archive is genuine: it is checked with `gh attestation verify --signer-workflow sakamosu/pekoe/.github/workflows/release.yml --source-ref refs/tags/<tag>`
  - From v0.4.0 on (`release.yml` attests every release before publishing it), nothing is replaced if the release has no provenance, if it can't be looked up, or if `gh attestation verify` fails. The message says why. If you have checked the release yourself, `--allow-unverified` goes on with only the SHA-256 checked (after a warning)
  - Without `gh` installed or logged in (`gh auth login`, or `GH_TOKEN` / `GITHUB_TOKEN`), the signature can't be verified: if GitHub keeps a provenance for the archive, it only warns and goes on
  - v0.1.0 to v0.3.0 were made while the repository was private and have no provenance: it only warns, as before (a provenance that is there but fails verification stops it for any version)
- Only the real file of the running pekoe (after following symlinks), the `pekoed` next to it, and `<prefix>/share/pekoe/nvim` and `extension` are replaced. Nothing else in `~/.local/share/pekoe` (such as `state.db`) is touched. Binaries are written next to the old ones and renamed; if something fails halfway, what was replaced is restored
- With a `make install`, `nvim` / `extension` are links into the repository, so they are replaced by directories with the release's contents (the repository isn't written to; it asks first; `make install` again brings the links back)
- If the current pekoe has the Web UI and the release has no `_web` archive, it refuses (install the default build with `--no-web`, or build from source with `make install WEB=1`)
- It refuses locations it can't write to and installs made by a package manager such as Homebrew
- At the end it prints the old and new versions and, if pekoed is running, asks whether to `pekoe daemon restart`. If the Chrome extension was replaced, reload it in `chrome://extensions`
- It sends a GitHub token if it finds one: `$GITHUB_TOKEN` / `$GH_TOKEN`, or else `gh auth token`. No token is needed; one only raises the GitHub API rate limit (without a token, 60 requests per hour; if you hit it, wait a while or `gh auth login`). For a private copy of the repository, a token that can read it is required; without one GitHub answers HTTP 404 and `pekoe update` tells you to `gh auth login`
- Replacing is for humans only. Inside an agent's session (`$PEKOE_SESSION` is set) or where `human-token` can't be read, it refuses with `only a person can do this`. `--check` works for anyone

## Installing the Claude Code skills

Install them with `pekoe skills install`. Their text is built into the binary: it is written to the data directory (`~/.local/share/pekoe/skills/`) and linked from `~/.claude/skills/<name>` (`$CLAUDE_CONFIG_DIR/skills/<name>` if set). It shows what it will do and asks first (`--yes` skips the question). Work sessions run in worktrees of any repository, so the skills belong in your own `~/.claude/skills`, where every session reads them.

- The text is that of the installed pekoe, not of a repository checkout. A newer pekoe (`pekoe update`, `make install`) writes it out again when pekoed starts; `pekoe skills install` does it right away
- A link you made by hand (`ln -s "$PWD/skills/pekoe" ~/.claude/skills/pekoe`) is replaced only after asking (only the link changes; what it pointed at is left alone). A link to the same place is left as it is. A directory or file that is not a link is skipped untouched, with a note to move it away and run again (not inside `~/.claude/skills`: Claude Code reads everything there as a skill)
- `pekoe skills status` shows each one's state (installed, older text, a link to somewhere else, a directory in the way, not installed); `pekoe skills uninstall` removes pekoe's links and the written text (links made by hand stay)
- To try changes to the skills themselves, link them by hand (`ln -s "$PWD/skills/<name>" ~/.claude/skills/<name>`); `pekoe skills install` asks before replacing such a link
- Claude Code agents cannot rewrite the written text with the Edit tool (pekoe's sandbox settings deny it; see [agent-permissions.en.md](agent-permissions.en.md))
