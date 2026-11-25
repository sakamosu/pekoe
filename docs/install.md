# インストールと更新

README の「[インストール](../README.ja.md#インストール)」の詳しい版です。Homebrew の cask・リリースの検証(チェックサム・ビルド来歴・署名と公証)・`pekoe update` の挙動・hooks とスキルのインストールをまとめます。

## インストール

必要なものは[状況と動作環境](../README.ja.md#状況と動作環境)をご覧ください。ソースからインストールする場合:

```sh
make install            # ~/.local/bin/pekoe (+ pekoed), nvim/ と extension/ を ~/.local/share/pekoe に link
make install WEB=1      # iPhone 用 Web UI も入れる(-tags web、Tailscale の tsnet をリンク)
pekoe hooks install     # ~/.claude/settings.json に hooks を追加(書く前に確認し、バックアップを残す)
pekoe skills install    # 任意: pekoe のスキルを ~/.claude/skills にリンク(書く前に確認する)
make install-launchd    # 任意: ログイン時に pekoed を起動
```

hooks は `SessionStart` `UserPromptSubmit` `Notification` `Stop` `PreToolUse` `PostToolUse` `SessionEnd` で `pekoe event` を呼ぶだけです。プロジェクト側の `.claude/` には触れません。`pekoe hooks status` で確認でき、`pekoe hooks uninstall` で pekoe の分だけ外せます。

### Homebrew で

v0.4.0 以降、macOS と Linux(arm64 / amd64)では、tap [sakamosu/homebrew-tap](https://github.com/sakamosu/homebrew-tap) から既定のビルド(Web UI なし)をインストールできます。tmux と git も依存としてインストールされます:

```sh
brew install sakamosu/tap/pekoe
pekoe hooks install     # ~/.claude/settings.json に hooks を追加(書く前に確認し、バックアップを残す)
brew upgrade pekoe      # 更新(pekoe update は Homebrew で入れたものを断る)。そのあと pekoe daemon restart
```

- リリースのアーカイブ(`pekoe_<ver>_<os>_<arch>.tar.gz`)から作った cask です。`$(brew --prefix)/Caskroom/pekoe/<ver>/` にアーカイブと同じ配置(`bin/`・`share/pekoe/`・`contrib/launchd/`)で展開し、`pekoe`・`pekoed` を `$(brew --prefix)/bin` にリンクします。pekoe はそのリンクをたどるので、`share/pekoe` の nvim プラグインと Chrome 拡張もそのまま見つかります
- Web UI が必要な場合は、リリースの `_web` のアーカイブをインストールするか(下記)、`make install WEB=1` でビルドしてください
- macOS は brew がダウンロードしたものに quarantine 属性を付け、Gatekeeper はこの属性の付いたバイナリを実行前に検査します。cask はインストール後にこの属性を外します。v0.5.0 以降の macOS 向けバイナリは署名・公証済み(下記)なので属性が付いていても動きますが、単体のバイナリには公証のチケットを添付(staple)できないため、Gatekeeper は初回にオンラインで確認します。属性を外しておけば、この確認を省けます。v0.4.0 までのリリースはアドホック署名だけなので、属性が付いたままだと Gatekeeper が実行を拒否します
- ログイン時に pekoed を起動したい場合は、`brew info pekoe` に launchd 用のコマンドが表示されます。`brew services` は cask では使えません

### リリースから(ビルド済みバイナリ)

GitHub Release に darwin / linux × arm64 / amd64 のビルドを置いています。`pekoe_<ver>_<os>_<arch>.tar.gz` が既定のビルド(Web UI なし)、`pekoe_<ver>_<os>_<arch>_web.tar.gz` が Web UI 入りのビルド(`-tags web`。[docs/web.md](web.md))で、中の配置は同じです。`gh`(どのアカウントでログインしていても構いません。Web UI 入りの場合は、アーカイブ名の `.tar.gz` の前と、`ln -sfn` が指す展開先のディレクトリ名(`pekoe_${ver}_darwin_arm64_web`)の末尾に `_web` を付けます)か、トークンなしの curl でダウンロードします:

```sh
ver=$(gh release view -R sakamosu/pekoe --json tagName -q .tagName | sed 's/^v//')   # 最新の版(特定の版なら ver=<version>)
gh release download "v$ver" -R sakamosu/pekoe -p "pekoe_${ver}_darwin_arm64.tar.gz" -p checksums.txt   # darwin_arm64 は手元の <os>_<arch> に
# gh がなければ curl で:
#   ver=$(curl -fsSL https://api.github.com/repos/sakamosu/pekoe/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
#   base="https://github.com/sakamosu/pekoe/releases/download/v$ver"
#   curl -fL -O "$base/pekoe_${ver}_darwin_arm64.tar.gz" -O "$base/checksums.txt"
shasum -a 256 --ignore-missing -c checksums.txt
gh attestation verify "pekoe_${ver}_darwin_arm64.tar.gz" -R sakamosu/pekoe \
  --signer-workflow sakamosu/pekoe/.github/workflows/release.yml --source-ref "refs/tags/v$ver"   # release.yml がこのタグからビルドしたか(v0.1.0〜v0.3.0 には来歴がない)
mkdir -p ~/.local/opt ~/.local/bin
tar -xzf "pekoe_${ver}_darwin_arm64.tar.gz" -C ~/.local/opt
ln -sfn ~/.local/opt/"pekoe_${ver}_darwin_arm64" ~/.local/opt/pekoe
ln -sf ~/.local/opt/pekoe/bin/pekoe ~/.local/bin/pekoe
ln -sf ~/.local/opt/pekoe/bin/pekoed ~/.local/bin/pekoed
pekoe version
```

`checksums.txt` は同じ Release に置いてあるので、これで分かるのはアーカイブが壊れていないことだけです。`gh attestation verify` は、アーカイブがこのリポジトリの `release.yml` でビルドされたこと(GitHub Actions が Sigstore で署名したビルド来歴)を検証します。来歴が付くのは、リポジトリが公開されている状態で `release.yml` が作ったリリースだけです(private のリポジトリで来歴を付けるには GitHub Enterprise Cloud が必要なので、`release.yml` は付けません)。そのため、来歴があるのはリポジトリの公開後に作ったリリース(v0.4.0 以降)です。v0.1.0〜v0.3.0 は private だったときに作ったので来歴がなく、`gh attestation verify` は通りません。`--signer-workflow` と `--source-ref` を付けると、このリポジトリのほかのワークフローやブランチではなく、`release.yml` がそのタグからビルドしたことまで検証できます。`release.yml` はリリースを下書きで作り、アーカイブに来歴を付けてから公開するので、公開されたリリース(v0.4.0 以降)には必ず来歴があります。

v0.5.0 以降(プレリリースでは v0.5.0-rc.1 以降)、`release.yml` は darwin のバイナリ(darwin のすべてのアーカイブ(既定と `_web`、arm64 と amd64)の `bin/pekoe`)を Developer ID Application 証明書で署名し、Apple の公証を受けています。`codesign -dv ~/.local/opt/pekoe/bin/pekoe` は `Authority=Developer ID Application: Yuki Sakamoto` と `TeamIdentifier=K9F9554C8G` を、`spctl -a -vvv -t install ~/.local/opt/pekoe/bin/pekoe` は `source=Notarized Developer ID` を表示します。単体のバイナリには公証のチケットを添付(staple)できないので、quarantine 属性の付いたバイナリを初めて実行するときに、Gatekeeper がオンラインで確認します。v0.1.0〜v0.4.0 は Go リンカのアドホック署名だけで、`codesign -dv` は `Signature=adhoc` と表示します。

アーカイブには、`bin/pekoe`、`bin/pekoed`(→ pekoe)、`share/pekoe/nvim/`、`share/pekoe/extension/`、`contrib/launchd/` と、`README.md`・`README.ja.md`・`LICENSE`・`THIRD_PARTY_LICENSES`(依存ライブラリのライセンス。[ライセンス](../README.ja.md#ライセンス))が入っています。`bin/pekoe` は自分の実体の位置から `../share/pekoe/nvim` を探すので、上のようにシンボリックリンクで PATH に置けば、nvim プラグインもそのまま見つかります。launchd で起動する場合は、`sed -e "s|PEKOE_BIN|$HOME/.local/bin/pekoe|" -e "s|HOME|$HOME|g" ~/.local/opt/pekoe/contrib/launchd/dev.pekoe.pekoed.plist > ~/Library/LaunchAgents/dev.pekoe.pekoed.plist` を実行してから、`launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.pekoe.pekoed.plist` を実行します。

既定のビルドには Tailscale のコードを一切含めていません。インストールし直したあとは、`pekoe daemon restart` で pekoed を新しいバイナリに切り替えてください(エージェントは tmux の中で動き続けます。launchd で動かしている場合は、launchd 経由で起動し直します)。

### 更新

```sh
pekoe update --check    # 新しいリリースがあるか見るだけ(誰でも使える)
pekoe update            # 最新のリリースに入れ替える
pekoe update --version vX.Y.Z --force   # 特定の版に(古くなるときは --force)
pekoe update --allow-unverified         # 来歴を確かめられなくても続ける(下を参照)
```

`pekoe update` は GitHub の最新リリース(`releases/latest`。draft と prerelease は除きます)を取得し、今の pekoe と同じ OS・アーキテクチャ・ビルド(Web UI 入りなら `_web` のアーカイブ)を選びます。

- 今のバージョンが最新か、それより新しければ何もしません。`make install` で入れたバージョン(`git describe` の `0.1.0-59-g3761d74`)は「0.1.0 より 59 コミット先」として比べます。`dev` のように比較できないバージョンのときは確認を求めます
- アーカイブを `checksums.txt` の SHA-256 と照合し、合わなければ何も入れ替えません。`checksums.txt` は同じ Release にあるので、アーカイブが本物かどうかはビルド来歴(attestation)で検証します: `gh attestation verify --signer-workflow sakamosu/pekoe/.github/workflows/release.yml --source-ref refs/tags/<tag>`
  - v0.4.0 以降(`release.yml` が来歴を付けてから公開します)は、来歴がない・照会できない・`gh attestation verify` が通らない場合、何も入れ替えずに止まります。止まった理由はメッセージに表示されます。リリースを自分で確認したうえで続けるなら、`--allow-unverified` を付けます(警告を出し、SHA-256 の照合だけで続けます)
  - `gh` が入っていない、またはログインしていない(`gh auth login`、または `GH_TOKEN` / `GITHUB_TOKEN`)ときは、署名を検証できません。GitHub がそのアーカイブの来歴を持っていれば、警告だけ出して続けます
  - v0.1.0〜v0.3.0 はリポジトリが private だったときに作ったので来歴がなく、これまでどおり警告だけを出します(来歴があるのに検証に通らない場合は、どのバージョンでも止まります)
- 入れ替えるのは、今動いている pekoe の実体(シンボリックリンクをたどった先)、その隣の `pekoed`、`<prefix>/share/pekoe/nvim` と `extension` だけです。`~/.local/share/pekoe` のほかのもの(`state.db` など)には触れません。バイナリは隣に書いてから rename し、途中で失敗したら入れ替えた分を元に戻します
- `make install` で入れた場合、`nvim` / `extension` はリポジトリへのリンクなので、そのリンクを、リリースの中身を入れたディレクトリに置き換えます(リポジトリには書き込みません。置き換える前に確認します。`make install` をやり直せばリンクに戻ります)
- Web UI 入りの pekoe で、リリースに `_web` のアーカイブがないときは拒否します(`--no-web` で既定のビルドを入れるか、`make install WEB=1` でソースからインストールしてください)
- 書き込めない場所にあるものや、Homebrew などのパッケージマネージャーでインストールしたものは拒否します
- 終わると新旧のバージョンを表示し、pekoed が動いていれば `pekoe daemon restart` するかどうかを尋ねます。Chrome 拡張を入れ替えた場合は、`chrome://extensions` で再読み込みしてください
- GitHub のトークンが見つかれば、付けて呼び出します: `$GITHUB_TOKEN` / `$GH_TOKEN`、なければ `gh auth token`。トークンは必須ではなく、付けると GitHub API のレート制限が緩くなるだけです(トークンなしでは 1 時間に 60 回までです。超えたら、しばらく待つか `gh auth login` してください)。private の複製から取得するときは、リポジトリを読めるトークンが必要です。ないと GitHub が HTTP 404 を返し、`pekoe update` は `gh auth login` するよう案内します
- 入れ替えは人間だけの操作です。エージェントのセッションの中(`$PEKOE_SESSION` がある)や、`human-token` を読めない場所からは、`only a person can do this` で拒否します。`--check` は誰でも使えます

## Claude Code スキルのインストール

`pekoe skills install` でインストールします。スキルの文はバイナリに同梱されていて、データディレクトリ(`~/.local/share/pekoe/skills/`)に書き出したうえで、`~/.claude/skills/<name>`(`$CLAUDE_CONFIG_DIR` があれば `$CLAUDE_CONFIG_DIR/skills/<name>`)からシンボリックリンクを張ります。書き込む前に、何をするかを表示して確認します(`--yes` で省けます)。作業セッションはどのリポジトリの worktree でも動くので、ユーザーの `~/.claude/skills` に置いておけば、どのセッションからでも読めます。

- スキルの文は、インストールした pekoe のバージョンのものです(リポジトリの checkout のものではありません)。pekoe を新しくすると(`pekoe update`、`make install`)、pekoed の起動時に書き出し直します。すぐに反映したいときは、`pekoe skills install` を実行します
- 以前に手で張ったリンク(`ln -s "$PWD/skills/pekoe" ~/.claude/skills/pekoe`)があれば、置き換えてよいかを尋ねます(リンクを張り替えるだけで、リンク先には触れません)。同じ場所を指すリンクなら、そのままにします。リンクではないディレクトリやファイルがある場合は、触れずに飛ばし、退避してからもう一度実行するよう案内します(Claude Code は `~/.claude/skills` の下をすべてスキルとして読むので、退避先はその外にしてください)
- `pekoe skills status` で、状態(インストール済み・文が古い・別の場所へのリンク・ディレクトリがある・未インストール)を確認できます。`pekoe skills uninstall` は、pekoe が張ったリンクと書き出した文を削除します(手で張ったリンクは残します)
- pekoe のスキル自体を直しながら試すときは、手で `ln -s "$PWD/skills/<name>" ~/.claude/skills/<name>` を張って構いません。`pekoe skills install` は、それを置き換える前に確認します
- Claude Code のエージェントは、書き出した文を Edit ツールで書き換えられません(pekoe が渡す sandbox の設定で禁止しています。[agent-permissions.md](agent-permissions.md))
