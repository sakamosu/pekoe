# エージェントに pekoe を使わせるときの権限

README の「[エージェントに pekoe を使わせるときの権限](../README.ja.md#エージェントに-pekoe-を使わせるときの権限)」の詳しい版です。

エージェントはあなたと同じユーザーで動くので、pekoed のソケットに接続できます。一方、pekoed はエージェントの sandbox の外で動きます。そこで、`human-token` を付けない呼び出しには、エージェントに必要なものだけを許可しています: hooks の `pekoe event`、ハブ(スキル)の `pekoe ls`・`pekoe new`・`pekoe send`(相手の入力欄にだけ、許可ダイアログが出ていないときだけ)・`pekoe land`(origin のあるリポジトリだけ)・`pekoe rm`(landed のセッションだけ。下記)・`pekoe wait`・`pekoe reports`・`pekoe inbox`(一覧)・`pekoe stats`(数だけ)・`pekoe conflicts`・`pekoe cleanup`(一覧。worktree の git status を読むのは、worktree ごとに 10 秒に 1 回まで)、tapi chat の `pekoe ledger`(一覧と、閉じる・結び付ける・残す操作。tapi の窓からだけ)、作業セッションの `pekoe report` と `pekoe dev`(自分のセッションの分だけ)、エージェントの `pekoe ask-human`(人への問い。答えられるのは人だけです。自分の問いは `withdraw` で取り下げられます)、MCP サーバーの `session_list`・`open_in_editor`。それ以外(許可待ちへの回答、生のキー入力、tapi chat への入力、設定の下書きと書き込み、ほかのセッションの `dev` の起動、run 窓・レビュー面でのコマンド実行、ほかのセッションの画面・差分・エラーの抜粋の閲覧、デーモンの停止など)には、すべて token が必要です。token なしの `rm` で消せるのは、landed のセッションだけです。PR のマージを検知した時点で pekoed が teardown と worktree の削除を済ませているので、残っているのは pekoe の記録だけで、コードは base のブランチにあります。ほかの状態のセッションの `rm` は、worktree と未コミットの変更を消してしまうので、人間だけの操作のままにしています。`rm --force` と、worktree が残っている(削除に失敗した)landed のセッションの `rm` も同じです。token は `~/.local/share/pekoe/human-token`(`$PEKOE_HOME` があれば `$PEKOE_HOME/share/human-token`。パーミッションは 0600)にあり、TUI・CLI・Neovim が読んで呼び出しに付けます。エージェントのペインの環境変数には、token もそのパスも渡しません。メソッドごとの分類は、[docs/api.md](api.md) の「人間だけの操作」にあります。

ただし、同じユーザーで動くエージェントは、ファイルを読めてしまいます。そこで、次の 2 つで token を守ります。

1. **Claude Code のエージェントは、pekoe が設定を渡す Claude Code の sandbox の中で動きます(既定)。** pekoe は Claude Code を起動するたびに(新規・`open`・再起動・デーモン再起動後の再開。tapi chat も含みます。`[agents.*]` の `command` が `claude` で始まるものすべてが対象です)、`--settings <データディレクトリ>/agent-settings/<slug>.json` を付けます。中身は次のとおりで、パスには `$PEKOE_HOME`・`$XDG_DATA_HOME`・`$XDG_CONFIG_HOME`・`[web] state_dir` を反映した実際の値が入ります:
   - `sandbox.enabled: true`・`allowUnsandboxedCommands: false`(`dangerouslyDisableSandbox` での再実行を許しません)・`failIfUnavailable: true`(sandbox が使えなければ、Claude Code は起動しません)・`filesystem.disabled: false`
   - `filesystem.denyRead`: `human-token`・`http-token`・`state.db`(と `-wal`・`-shm`。プロンプト・レビューのコメント・報告・送信の記録が入っています。エージェントは、pekoed の API を通してだけ読めます)・tsnet の state・ローカル GUI のサインイン用のページ(`~/.local/share/pekoe/gui/`)・pekoe の設定ディレクトリ(`~/.config/pekoe`)。Bash のコマンドとその子プロセスに、OS のレベルで効きます
   - `permissions.deny`: 同じパスへの `Read(...)` と `Edit(...)`(sandbox の外で動く Read / Edit ツール向け)、`agent-settings` 自身と書き出したスキルの文(`skills/`)への `Edit(...)`
   - `permissions.allow`: Web UI から送られた自分宛ての画像のディレクトリ(`<データディレクトリ>/images/<slug>/`、tapi chat は `images/+tapi/`)への `Read(...)`。送られた文の `[image] <パス>` を、確認なしで開けます([docs/web.md](web.md) の「画像の扱い」)
   - `filesystem.denyWrite` と `Edit(...)`: リポジトリの `.git/config`・`.git/config.worktree`・`.git/hooks` と、サブモジュールの git ディレクトリ(`.git/modules/…`。エージェントの起動時に存在するもの)の中の同じファイル(commit はできます。あなたが自分で実行する git が従う設定は、書き換えられません)
   - tapi chat だけ: `filesystem.denyWrite` と `Edit(...)` に、`--add-dir` で読ませている各リポジトリと `~/worktrees`(シンボリックリンクを解決したパスも)を加えます。統括役は自分では作業せず、セッションに任せるので、Bash でも Edit / Write でも書き込めないようにしています。稼働ディレクトリ(`tapi/`)には書き込めます。`[agents.<tapi の agent>] sandbox = false` にすると、この制限も外れます
   - `network.allowUnixSockets`: pekoed のソケット(`pekoe ls`・`new`・`send` のため)。hooks は sandbox の外で動くので、`pekoe event` はもともと影響を受けません
   - `hooks.MessageDisplay`: 起動の時点で `[voice] enable = true` で、`focus_mode` か `background_mode` が `read` のときだけ。`<pekoe の絶対パス> event MessageDisplay`(timeout 2 秒)で、返事の本文を表示と同時に pekoed に渡します([docs/voice.md](voice.md) の 3.1)。`~/.claude/settings.json` の hook と合わさって両方動きます。`sandbox = false` のエージェントにも、この hook だけを書いたファイルを渡します

   **対象は claude だけです。** codex・opencode など、ほかのエージェントには何も渡しません。使う場合は、そのエージェント自身の sandbox で、同じパスを読めないようにしておいてください。`config.toml` の `[agents.claude] sandbox = false` で外せます(`[agents.claude-ask]` のような別名の定義ごとに指定します)。

   **自分の設定との組み合わせ方**(Claude Code の [sandbox](https://code.claude.com/docs/en/sandboxing) と [settings](https://code.claude.com/docs/en/settings) の仕様によります。バージョンによって変わることがあります):
   - `--settings` は、`~/.claude/settings.json` やリポジトリの `.claude/settings.json` より優先されます(その上にあるのは managed settings だけです)。`~/.claude/settings.json` に `"enabled": false` があっても、pekoe のセッションでは sandbox は無効になりません
   - `denyRead`・`allowWrite`・`allowedDomains`・`permissions.deny` のような配列は、足し合わされます。あなたの設定の分が、pekoe の分に加わります
   - `allowUnsandboxedCommands: false` を `--settings` で渡すので、sandbox は「admin-required」になり、制限を緩める設定は、`--settings`・`~/.claude/settings.json`・managed settings に書いたものだけが効きます。リポジトリの `.claude/settings.json` / `.claude/settings.local.json` にある `allowWrite`・`allowedDomains`・`excludedCommands` などは無視されます(エージェントが書き換えられるファイルだからです)
   - 逆に、`~/.claude/settings.json` の `excludedCommands` は効きます。そこに挙げたコマンドは sandbox の外で動き、トークンを読めてしまいます。ファイルを読めるコマンド(`gh`・`cat`・`python` など)は入れないでください。また、`filesystem.allowRead` で、上のパスを読めるように戻さないでください

   **既定で通らなくなるもの**: cwd(worktree)・一時ディレクトリ・`--add-dir` のディレクトリの外への書き込み(Go のビルドキャッシュ、npm / pnpm のキャッシュなど)と、許可したドメイン以外へのネットワーク接続(最初は何も許可されていません。auto モードでは拒否され、手動の許可モードでは確認されます)。`git commit` はできます(worktree が共有する本体の `.git` には書き込めます)。緩める場合は、`~/.claude/settings.json` に `allowWrite` / `allowedDomains` などだけを書いてください。`"enabled"` を書かなければ、pekoe の外の Claude Code には影響しません(そちらでは sandbox が無効のままだからです)。次は、pekoe 自身の開発に必要なものの例です(Go のキャッシュとモジュール、`gh`。npm は使うときだけ):

   ```json
   {
     "sandbox": {
       "enableWeakerNetworkIsolation": true,
       "filesystem": {
         "allowWrite": ["~/Library/Caches/go-build", "~/go/pkg/mod", "~/.npm", "~/Library/pnpm/store"]
       },
       "network": {
         "allowedDomains": ["proxy.golang.org", "sum.golang.org", "github.com", "api.github.com", "registry.npmjs.org"]
       }
     }
   }
   ```

   パスは `go env GOCACHE GOMODCACHE`・`npm config get cache`・`pnpm store path` で確認できます(Linux なら `~/.cache/go-build` など)。`gh` は、macOS の sandbox の中では TLS の検証に失敗します(`x509: OSStatus -26276`)が、`enableWeakerNetworkIsolation: true` を設定して `api.github.com` を許可すれば動きます(確認済み)。`gh` を `excludedCommands` に入れて sandbox の外で動かすのは避けてください(`gh api --input <ファイル>` で、トークンを外部に送れてしまいます)。緩めずに使う場合、issue は人間が TUI で扱い(`gh` を使う tapi chat の集計も同じです)、エージェントにとっては `pekoe land` が push の手段になります(pekoed が自分の sandbox でテストを実行してから、push と PR の作成を行います)。`github.com` / `api.github.com` を許可すれば、エージェントは自分で `git push` と `gh pr create` を実行できます。land をエージェントに開放しているのはそのためで、歯止めになるのは、マージを人間が行うことです。

   `--permission-mode bypassPermissions` で使うと、許可していないドメインへの接続も、確認なしに通ります(Claude Code の仕様)。そのため、ネットワークの制限は実質的に効きません。ファイルの制限(トークンを読めないこと)は残ります。Linux では、Claude Code の sandbox に bubblewrap と socat が必要で、なければ `failIfUnavailable` によって Claude Code が起動しません(エージェントの窓に、終了の理由が表示されます)。

   インストールし直したら、エージェントに `cat ~/.local/share/pekoe/human-token` と `pekoe answer no-such-session deny` を実行させて、前者が `Operation not permitted` で読めないこと、後者が `only a person can do this` で拒否されること(`no such session` ではないこと)を確認してください。

2. **`Bash(pekoe *)` のように、pekoe を丸ごと許可しないでください。** ハブ(pekoe・pekoe-orchestrate スキル)が使うのは `ls`・`new`・`send`・`wait`・`reports`・`inbox`・`conflicts`・`cleanup`(一覧。tapi は `ledger` も)、作業セッション(pekoe-work・pekoe-review スキル)が使うのは `report`・`ask-human`・`dev`(UI を確認するとき)です。許可リストに入れるなら、これらだけに絞ってください。`pekoe inbox answer` と `pekoe ledger reopen` / `rm` は人だけの操作で、許可リストに入れても拒否されます。`pekoe event`(hooks 用)、`answer`、`init`、`rm`、`daemon` は入れないでください。`dev` は、token なしでは呼び出し元のセッション(自分の窓)の dev server にだけ効き、別のセッションの slug は拒否されます(実行されるのは、リポジトリの設定の `[hooks] dev` で、`[hooks] test` と同じく pekoe 自身の sandbox で動きます)。`land` は token なしでも通りますが、許可リストに入れなければ、push のたびに Claude Code が確認します。`rm` も同じで、landed のセッションはエージェントが消せますが、その前に Claude Code が確認します

   ```json
   {
     "permissions": {
       "allow": [
         "Bash(pekoe ls)", "Bash(pekoe --json ls)", "Bash(pekoe --json new *)", "Bash(pekoe send *)",
         "Bash(pekoe report *)", "Bash(pekoe --json reports *)", "Bash(pekoe --json wait *)",
         "Bash(pekoe --json ask-human *)", "Bash(pekoe --json inbox)", "Bash(pekoe --json inbox *)",
         "Bash(pekoe --json ledger)", "Bash(pekoe ledger *)", "Bash(pekoe --json stats *)",
         "Bash(pekoe --json conflicts)", "Bash(pekoe --json conflicts *)", "Bash(pekoe --json cleanup)", "Bash(pekoe dev *)"
       ]
     }
   }
   ```

**worktree から来るコマンドは、pekoe 自身の sandbox で動かします。** pekoed は、エージェントが書き換えられるコードを実行することがあります: Stop のたびの `[hooks] test`、`[review] gatekeeper`、`pekoe new` の `setup`、`dev` と `dev_stop`、rm / land の `teardown`、`land` のテスト。Stop は誰でも報告できるので、そのままでは、エージェントが Makefile に `cat ~/.local/share/pekoe/human-token` を紛れ込ませ、エージェントの sandbox の外で pekoed に実行させることができてしまいます。そこで pekoed は、これらを sandbox の中で実行します。macOS では `sandbox-exec`(Seatbelt)、Linux では bubblewrap(`bwrap`。ディストリビューションのパッケージでインストールします)を使います。基本的にはすべてを許可し(ビルド・テスト・開発サーバーは、これまでどおり動きます)、禁止するのは次のことだけです。`human-token`・`http-token`・tsnet の state・`config.toml`・`state.db` を読むこと。pekoe のデータと設定のディレクトリ、および、あとで sandbox の外で実行されるファイル(シェルの起動ファイル、`~/.gitconfig`、launch agent、`$PATH` のうち書き込めるディレクトリ、次に起動する Claude Code が読んで実行するもの(`~/.claude.json`、`~/.claude` か `$CLAUDE_CONFIG_DIR` の `settings*.json`・`skills`・`hooks`・`plugins`・`agents`・`commands`・`rules`・`output-styles`・`CLAUDE.md`)など)に書き込むこと。tmux サーバーと、レビュー面の nvim(pekoe がデータディレクトリの中に `--listen` で立てます)に接続すること。macOS では、アプリを開くことと、Apple イベントを送ること。sandbox が使えない環境では、これらのコマンドを実行しません。セッションには `test not run: no sandbox` と表示し、門番は理由をリスクの欄に表示し、setup・dev・teardown・land は理由を添えて失敗します。`config.toml` の `[sandbox] enable = false` で、sandbox なしの実行に戻せます。Linux の bwrap は、存在するパスしか保護できません。そこで pekoed は、コマンドの実行前に、空のまま置いても何も変わらないもの(`~/.claude` か `$CLAUDE_CONFIG_DIR` の中の、上に挙げたディレクトリを空のディレクトリとして、`CLAUDE.md` を空のファイルとして、`settings*.json` を `{}` として。ほかに、空の `~/.config/git`・`~/.config/nvim` など)を作り、それ以外(存在しないシェルの起動ファイル、`~/.claude.json` など)については、いちばん近い既存の親ディレクトリを読み取り専用にします(その時点で中にあるものには書き込めます)。つまり、ホームの直下に新しいファイルやディレクトリは作れません(既存のものの中には書き込めます)。sandbox で包まないものは、run 窓(`!`。あなたが自分で打つコマンド)と、エージェント自身(それぞれの sandbox の役目)です。禁止する範囲は秘密の情報に絞っているので、ホームのほかの場所には書き込めてしまいます。したがって、エージェントの変更を確認する代わりにはなりません。ツールがこれまでどおり動くように、意図的に残している穴が 2 つあります。1 つは、リポジトリ本体の `.git/hooks`・`.git/config` に書き込めることです(husky・pre-commit・lefthook が、`npm install` や setup から書き込みます。本体のチェックアウトで実行する git は、書き込まれたものを sandbox の外で実行します。pekoed は、これらのコマンドの前後で(`core.hooksPath` が指すディレクトリも含めて)変化を見張り、フックや、コマンドを指定する設定(`core.hooksPath`・`core.fsmonitor`・`alias.*`・`include.path` など)が変わったら、何が・どのセッションの・どの hook で変わったかを受信箱に知らせます。修正も削除もしません)。もう 1 つは、Docker や ssh-agent などの unix ソケットに接続できることです。詳しくは、[docs/api.md](api.md) の「hooks の sandbox」「本体の .git の見張り」をご覧ください。

**pekoed 自身の git は、エージェントが git の設定に書いたものを実行しません。** git は、設定に書かれたコマンドを実行します(`git status` のたびに動く `core.fsmonitor`、フック、credential helper、`.gitattributes` が選ぶ filter・merge ドライバ、`gpg.program`、`core.sshCommand` など)。そして pekoed は、worktree で頻繁に git を実行します(diffstat・スナップショット・レビュー・undo・衝突予報・land・rm)。エージェントは、worktree の `.git` ファイルを自前の git ディレクトリに向け直せますし、commit できるエージェントは、たいていリポジトリの `.git` にも書き込めます。そこで pekoed(と pekoed が実行する `gh`)は、セッションの worktree では、`.git` ファイルの内容にかかわらず、リポジトリが持っているその worktree 用の git ディレクトリを使います(`.git` ファイルが書き換えられていたら、そこでは git を実行せず、セッションに `.git changed: git stopped` と表示します)。コマンドを指定する設定は、あなた自身の設定(system / global)か、git の既定値で上書きします: fsmonitor なし、フックは一切なし(`land` の push でも、pre-push は実行されません)、credential helper と署名用のプログラムはあなた自身のものだけ、`ext::` の remote なし、`--no-ext-diff --no-textconv`、upload-pack / receive-pack は既定のもの。名前が決まっていないもの(filter・merge ドライバ、git-lfs の設定、ローカルのパスにある remote のフック)に備えて、git 自体も hooks と同じ sandbox の中で実行します。sandbox が使えない環境でも git は実行するので(git なしでは pekoe が使えません)、この最後のものは封じ込められません。あなたが自分で実行する git(シェル、run 窓、レビュー面の nvim の git プラグイン)は、通常の git と同じように worktree を読みます。そのため、警告が出た worktree で git を使う前に、`.git` を確認してください。また、エージェントに `<repo>/.git/config` や `.git/hooks` を書かせないでください(Claude Code のエージェントには、pekoe が `sandbox.filesystem.denyWrite` と `Edit(...)` の deny を渡しています。ほかのエージェントでは、自分で禁止してください)。pekoed が worktree から実行するコマンドは、これらに書き込めます(上記)。変更があったと受信箱に届いたら、内容を確認してから本体で git を使ってください。詳しくは、[docs/api.md](api.md) の「pekoed が動かす git」をご覧ください。

Web UI と Chrome 拡張は、ソケットを通らない人間用の入口なので、それぞれの認証だけで使えます。脅威モデルは [docs/web.md](web.md#脅威モデル) にあります。
