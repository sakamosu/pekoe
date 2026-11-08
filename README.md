<p align="center"><img src="docs/images/pekoe-logo.webp" alt="pekoe" width="320"></p>

# pekoe

A cozy home for your coding agents. — with tapi, your little companion

pekoe — 読みは「ペコー」(PEK-oh)。orange pekoe から。pekoe はエージェントたちの家で、最下行にいるマスコット tapi はそこにいる相棒。

Orca の開発体験(複数エージェントの並列運用・worktree 分離・ブラウザ連携・差分レビュー)を、Ghostty の中で完結させるターミナルツール。Claude Code の使い方は変えず、その周辺作業(切り替え・待ち・worktree・ブラウザ・レビュー・PR)だけを引き受ける。

- 1 タスク = 1 worktree = 1 エージェント。`pekoe new` で worktree を切り、設定ファイルのコピー・setup・ポート割り当てまで済ませてから claude(や codex)を起動する
- TUI でセッションを一覧し、待ち・許可待ち・レビュー待ちに順に飛ぶ。最下行のマスコット tapi が全体の状態を表す
- 変更は hunk 単位でレビューして、コメントを 1 通にまとめてエージェントに返す(Neovim か組み込み)
- Chrome 拡張で画面の要素を選んで指示と一緒に送る。dev server のスクショは TUI の中に出せる
- iPhone から Tailscale 経由で許可待ちに答えたり一言送ったりできる(任意のビルド)

## 構成

```
 TUI (pekoe) ─────┐
 CLI ─────────────┤ unix socket                        tmux -L pekoe
 Neovim ──────────┤ (JSON Lines)                 ┌── claude / codex / …
 MCP (pekoe mcp) ─┼──────────────── pekoed ──────┤   (worktree ごとの pty)
 Chrome 拡張 ─────┤ HTTP 127.0.0.1 + token       └──────────┬──────────
 Web UI (iPhone) ─┘ HTTPS on tailnet (tsnet)                │ hooks → `pekoe event`
                                   │                        │
                       state.db / refs/pekoe/*  ◀───────────┘
```

賢さはすべて pekoed にあり、TUI・CLI・Neovim・Chrome 拡張・MCP・Web UI は同じ API を叩く表示層。pekoed を止めてもエージェントは tmux の中で動き続け、次に起動したときに拾い直す。

## ドキュメント

| | |
| --- | --- |
| [docs/design.md](docs/design.md) | 設計書(状態モデル・worktree フロー・ターミナル層・レビュー・CLI とキー) |
| [docs/api.md](docs/api.md) | pekoed の API(unix socket・HTTP・HTTPS・MCP) |
| [docs/web.md](docs/web.md) | iPhone 用 Web UI の構成・設定・脅威モデル |
| [docs/mascot.md](docs/mascot.md) | マスコット tapi の形・状態・表情 |
| [docs/tapi-chat.md](docs/tapi-chat.md) | tapi chat(TUI の `t` で話しかける窓口エージェント)の設計 |
| [docs/ui-guidelines.md](docs/ui-guidelines.md) | TUI のデザイン原則(配色トークン・レイアウト・アイコン) |
| [docs/verification.md](docs/verification.md) | 設計書・デザイン原則・モックと実装の対応表 |
| [nvim/README.md](nvim/README.md) / [extension/README.md](extension/README.md) | Neovim プラグイン / Chrome 拡張 |

## インストール

必要なもの: Go 1.26.10+(`make` は `GOTOOLCHAIN=local` で手元の Go だけを使うので、古ければ Go を上げる)、tmux 3.3+、git。任意: gh(issue・land・PR ビュー・PR レビュー)、Neovim 0.10+(レビュー面)、Chrome(拡張・TUI 内プレビュー)、Tailscale(Web UI)。

```sh
make install            # ~/.local/bin/pekoe (+ pekoed), nvim/ と extension/ を ~/.local/share/pekoe に link
make install WEB=1      # iPhone 用 Web UI も入れる(-tags web、Tailscale の tsnet をリンク)
pekoe hooks install     # ~/.claude/settings.json に hooks を追加(バックアップを残す)
make install-launchd    # 任意: ログイン時に pekoed を起動
```

### リリースから(ビルド済みバイナリ)

GitHub Release に darwin / linux × arm64 / amd64 の既定ビルド(Web UI なし)を置いている。repo は private なので `gh` で落とす:

```sh
ver=0.2.0                                 # 入れるバージョン(gh release list で確認)
gh release download "v$ver" -R sakamosu/pekoe -p "pekoe_${ver}_darwin_arm64.tar.gz" -p checksums.txt
shasum -a 256 --ignore-missing -c checksums.txt
gh attestation verify "pekoe_${ver}_darwin_arm64.tar.gz" -R sakamosu/pekoe   # このリポジトリのワークフローがビルドしたか
mkdir -p ~/.local/opt ~/.local/bin
tar -xzf "pekoe_${ver}_darwin_arm64.tar.gz" -C ~/.local/opt
ln -sfn ~/.local/opt/"pekoe_${ver}_darwin_arm64" ~/.local/opt/pekoe
ln -sf ~/.local/opt/pekoe/bin/pekoe ~/.local/bin/pekoe
ln -sf ~/.local/opt/pekoe/bin/pekoed ~/.local/bin/pekoed
pekoe version
```

`checksums.txt` は同じ Release に置いてあるので、アーカイブが壊れていないかしか分からない。`gh attestation verify` は、アーカイブがこのリポジトリの `release.yml` でビルドされたこと(GitHub Actions が Sigstore で署名したビルド来歴)を確かめる。来歴は release.yml に attestation を足した後の、リポジトリが公開されているときのリリースにだけ付く(private リポジトリでは GitHub Enterprise Cloud が要るので付けない)。

アーカイブの中は `bin/pekoe`、`bin/pekoed`(→ pekoe)、`share/pekoe/nvim/`、`share/pekoe/extension/`、`contrib/launchd/`。`bin/pekoe` は実体の位置から `../share/pekoe/nvim` を探すので、上のようにシンボリックリンクで PATH に置けば nvim プラグインもそのまま見つかる。launchd で起動するなら `sed -e "s|PEKOE_BIN|$HOME/.local/bin/pekoe|" -e "s|HOME|$HOME|g" ~/.local/opt/pekoe/contrib/launchd/dev.pekoe.pekoed.plist > ~/Library/LaunchAgents/dev.pekoe.pekoed.plist` のあと `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.pekoe.pekoed.plist`。

既定のビルドには Tailscale のコードを一切含めない。入れ直したあとは `pekoe daemon restart` で pekoed を新しいバイナリに替える(エージェントは tmux の中で動き続ける。launchd で動かしているときは launchd から起動し直す)。

hooks は `SessionStart` `UserPromptSubmit` `Notification` `Stop` `PreToolUse` `PostToolUse` `SessionEnd` で `pekoe event` を呼ぶだけ。プロジェクト側の `.claude/` は触らない。`pekoe hooks status` で確認、`pekoe hooks uninstall` で pekoe の分だけ外す。

## クイックスタート

```sh
cd ~/src/app
pekoe init                                            # ~/.config/pekoe/repos/<repo-id>.toml を下書き
pekoe new feat/csv --prompt "CSV エクスポートを追加"   # worktree → copy → setup → port → claude 起動
pekoe new feat/invoice --from gh#128                  # issue 本文を初期プロンプトに
pekoe                                                 # TUI
```

`pekoe init` は lockfile・`.envrc`・git の無視ファイルから setup や copy を推測し、コピーする無視ファイルを番号で選ばせてから書く。手で始めたエージェントは `pekoe adopt` で、既存の worktree は `pekoe open` で取り込める。

## コマンド

| コマンド | すること |
| --- | --- |
| `pekoe` | TUI(デーモンがなければ起動) |
| `pekoe init [<dir>] [--yes] [--overwrite] [--print]` | リポジトリの設定を検出して下書き。`--print` は書かずに出すだけ |
| `pekoe new <branch> [--repo R] [--from gh#N] [--agent codex] [--base REF] [--prompt TEXT]` | worktree + セッション作成。setup の進み具合を出し、エージェントが起動したら戻る |
| `pekoe open [<worktree>] [--resume\|--new] [--agent NAME]` | 既存 worktree で起動。過去の Claude Code セッションを resume するか選ぶ(`--resume` で resume、`--new` で新規) |
| `pekoe adopt [<path>] [--yes]` | pekoe の外で動いているエージェントを止め、同じセッションを pekoe の中で resume する |
| `pekoe ls [--all\|--repo R]` | セッション一覧と状態。リポジトリの中ではそのリポジトリだけ、外では全部 |
| `pekoe issues [--repo R] [--all\|--label L] [--refresh]` | GitHub の open issue(既定は自分担当)。セッションがあれば状態とブランチを添える |
| `pekoe issues <N> [--repo R]` | issue の本文とコメント |
| `pekoe conflicts [--repo R]` | 衝突予報。同じファイルを触っているセッションのペアと、衝突するか(`git merge-tree`) |
| `pekoe send <slug> <text> [--no-enter]` | pty に文字を送る(既定で Enter を足す。`-` で stdin) |
| `pekoe review [<slug> \| gh#N [--repo R]]` | レビュー面。引数なしなら次のレビュー待ち。`gh#N` で人の PR |
| `pekoe land <slug>` | rebase → test → push → `gh pr create`。マージ検出後に teardown と worktree 削除 |
| `pekoe rm <slug> [--force] [--yes]` | teardown → worktree 削除(`~/worktrees` 配下のみ。外のものはセッションを忘れるだけ) |
| `pekoe dev <slug>` | `dev` をそのセッションのポートで起動 / 停止 |
| `pekoe undo <slug> [n]` | 最後(か n 番目)のプロンプトの変更を取り消す |
| `pekoe answer <slug> allow\|deny\|always` | 許可待ちに答える |
| `pekoe attach <slug> [agent\|review\|dev]` | tmux の窓に直接 attach(デバッグ用) |
| `pekoe hooks install\|uninstall\|status [--yes]` | Claude Code の hooks を足す / 外す / 確認 |
| `pekoe ext` | Chrome 拡張に入れる port と token |
| `pekoe web` | Web UI の状態・URL・初回のログイン URL |
| `pekoe mcp` | エージェント用 MCP サーバ(stdio) |
| `pekoe daemon start\|stop\|restart\|status` | デーモン操作。`stop` はプロセスが終わるまで待つ。`restart` は止まるのを待ってから起動し直し、前後のバージョンを出す(`make install` の後に。動いていなければ起動するだけ)。エージェントは tmux の中で動き続ける |
| `pekoe daemon stop --kill-agents [--yes]` | tmux サーバー(`$PEKOE_TMUX_SOCKET`)も止める。**既定のソケット `pekoe` に使うと全エージェントが止まる**。隔離環境の片付け用 |
| `pekoe tapi [reset [--yes]]` | tapi chat の状態 / 終了して会話を忘れる(TUI の `T` と同じ) |
| `pekoe tip [<id>] [--list] [--doc PATH]` | pekoe の機能を 1 つ紹介する(まだ見せていない tip。[TIPS](#tips))。`<id>` はその tip、`--list` は一覧、`--doc` は tip が指す docs を出す(バイナリに同梱) |
| `pekoe migrate [--dry-run] [--prune-old]` | boba からの移行(設定・状態・refs・hooks を一度に書き換える。[boba からの移行](#boba-からの移行)) |
| `pekoe version` | バージョン |

- `R` は repo-id、短い名前(`~/worktrees/<name>` の name)、パスのどれでもよい。slug を取るコマンドは `<repo>/<slug>` でも指定できる
- `--json` で機械可読に出す(`init` `new` `open` `ls` `issues` `conflicts` `land` `dev` `ext` `web` `tapi` `tip` `daemon status`)。サブコマンドの前後どちらにも書ける
- `--help` / `-h` は使い方を出すだけで何もしない(終了コード 0)。知らないフラグや値のない `--repo` はエラー(終了コード 2)。`-` で始まる文字列を位置引数にするときは `--` の後に書く(例: `pekoe send <slug> -- --verbose を外して`)
- `pekoe event <Hook>` は hooks から呼ばれる内部用。失敗しても常に終了コード 0
- `answer`・`dev`・`init` の書き込み・`daemon stop` / `daemon restart` は人間だけの操作で、`human-token` を読める端末からしか通らない(エージェントからは断られる。[エージェントに pekoe を使わせるときの権限](#エージェントに-pekoe-を使わせるときの権限))。`send` は誰でも使えるが、相手に許可ダイアログが出ている間は断る(人間の `send` も、Enter つきなら打つ前と Enter の直前に確かめる)

## TUI

左に番号付きパネル([1] Sessions、[2] Worktrees ⇄ Browser ⇄ Issues、[3] Changes ⇄ Picks)、右に [4] Agent(エージェントの画面そのもの。preview・dev・run・PR の窓に切り替わる)、最下行に powerline のステータスとキーヒント。100 列未満では左列がタブ 1 枚になる。複数リポジトリのセッションはリポジトリごとの見出しでまとめる。

セッションの状態はアイコンと色で出す(配色は [docs/ui-guidelines.md](docs/ui-guidelines.md) のトークン)。最下行左端のマスコット tapi は全セッションの最優先の状態を顔で表し、隣の 1 行で誰が何をしているかを言う:

| 顔 | 状態 | 意味 |
| --- | --- | --- |
| `⨯ ⨯` | error | エージェントの異常終了・コマンド失敗 |
| `◕ ◕` | wait | 入力・許可を待っている |
| `● ●` | work | 出力中・コマンド実行中 |
| `^ ^` | done | ターンが終わり、まだ見ていない |
| `• •` | idle | プロンプトで待機 |
| `- -` | sleep | 全セッションが 3 分以上 idle |

見ていないセッションが wait / error になるとトーストが出る。PR のマージ・CI の失敗・レビューにも一度だけ反応する(詳細は [docs/mascot.md](docs/mascot.md))。

| キー | 動作 |
| --- | --- |
| `j` `k` / `↵` | セッション選択 / 右ペインに入って入力(`^q` 以外のキーはすべてエージェントへ) |
| `^q` | 入力から抜けて Sessions に戻る(`[keys] prefix` で変更可)。左のパネルをクリックしても抜けられる |
| `i` / `h` `Esc` | Agent パネルで入力を始める / 左に戻る |
| `^q` `z` | 右ペイン(4)を全幅にして左ペインを隠す / もう一度で戻す。入力中に押せば入力に戻る |
| `^q` `<` `>` / `^q` `=` | 左ペインを 4 列ずつ狭める・広げる / 既定(40%)に戻す。幅とズームは `~/.local/share/pekoe/layout.json` に残り、再起動後の TUI と CLI から起動するエージェントの幅も同じになる。100 列未満(縦並び)では効かない |
| `n` | 新しい worktree + セッション |
| `t` / `T` | tapi chat を開いて話しかける([tapi chat](#tapi-chat))/ tapi chat をリセット(確認あり) |
| `w` | リポジトリを選ぶ |
| `]a` `[a` | 次の待ちセッション(PR に動きがあったものも)/ 元の場所に戻る |
| `]r` | 次のレビュー待ち |
| `r` | レビュー面(右ペインが nvim に入れ替わる。エージェントは裏で動き続ける) |
| `p` | PR ビュー(checks・レビュー・スレッド。`space` で選んで `s` でエージェントに送る) |
| `v` | preview 窓(dev server のスクショを TUI 内に表示。`m` モバイル幅・`c` before/after)→ dev 窓 → agent |
| `b` `l` `d` | ポートを Chrome で開く / land(衝突予報つきの確認)/ dev server |
| `!` | その worktree でコマンドを 1 回実行(run 窓)。エージェントの画面の `! <cmd>` やコードを初期値に入れる。確認やパスワードにもそのまま答えられ、終わったら `↵` で agent に戻り、`y` で結果(終了コード、Ctrl-C なら中断)をエージェントに伝える。`x` は聞かずに閉じる |
| `u` `R` | 最後のプロンプトを undo / エージェント再起動(resume) |
| `s` `i` `↵` | エラーカード(dev server やブラウザのエラーを pekoed が見つけて出す)を送る / 無視 / 詳細。カードがある間、入力は `a` |
| `x` `D` | セッション終了(worktree は残す)/ worktree 削除(確認あり) |
| `/` `?` `:` | 絞り込み / キー一覧(選んで Enter で実行)/ コマンド(`new` `send` `land` `rm` `dev` `undo` `review` `ext` `q`) |
| `.` | 次の tip(4 の空き表示に tip が出ている間だけ。[TIPS](#tips)) |
| `1`–`4` `Tab` `[` `]` | パネル移動 / サブタブ切替 |
| `q` | 終了 |

- 許可待ちになったセッションを選ぶと Permission モーダルが出る(`y` 許可・`n` 拒否・`A` 常に許可・`g` そのセッションへ・`esc` 後で)
- [2] Issues では `f` で自分担当 → 全 open → ラベル指定を切り替え、`n` で issue からセッションを作る
- [2] Browser は Chrome で開いている localhost のタブをポートでセッションに結びつける。`E` でコンソールのエラーを送る
- Chrome から要素が届くと Agent ペインにカードが出る(`↵` 送信・`^s` 別セッション・`^n` 新セッション・`esc` 破棄)。指示つきのピックは、ページのポートを持つセッションにそのまま送る(`[picks] auto_send`)
- 同じファイルを触っているセッションには一覧に `⚠N` が付く
- エージェントへのパススルーは OSC 52(クリップボード)・OSC 8(リンク)・OSC 9/99/777(通知)・マウス(SGR)・Kitty keyboard protocol(Shift+Enter などが届く。`PEKOE_KITTY=0` で無効)

すべてのキーは [docs/design.md](docs/design.md#cli-コマンドとキーバインド) にある。

### tapi chat

どのパネルからでも `t` を押すと、[4] がマスコット tapi との会話(tapi chat)に切り替わり、そのまま入力できる。特定のセッションに属さない窓口のエージェントで、単発の調査や質問、リポジトリ横断の調査(「抱えているリポジトリの dependabot アラートは?」)、作業の振り分け(`pekoe new --repo R` でセッションを作って任せる、`pekoe send` で指示を足す)に使う。main セッションが作業中でも気軽に話せる。

- `^q`(prefix)で閉じる。会話は残り、もう一度 `t` で続きから。`Esc` はエージェントに届く(Claude Code の中断)。`^q` `z` / `^q` `<` `>` `=` はエージェントのときと同じく効き、そのまま tapi chat の入力に戻る(ズーム中に開けば全幅で、pty の大きさもペインに合わせる)
- 初めて開いたときにエージェントが起動する(pekoed の起動時には起動しない)。稼働ディレクトリは `~/.local/share/pekoe/tapi/` で、pekoed が役割と使うコマンドを書いた `CLAUDE.md` を置く。初回は Claude Code のフォルダ信頼の確認が出る
- Claude Code なら pekoe が知っているリポジトリと `~/worktrees` を `--add-dir` で渡すので、横断で読むたびに許可を求められない(リポジトリの増減は次の起動から)
- tapi の顔はタイトルに出て、考え中・許可待ちで表情が変わる。閉じている間に許可待ち・質問待ちになると、最下行の声が `chat waits · t` になる
- `T`(または `pekoe tapi reset`)で終了し、Claude Code の会話の記録も消す。pekoe 側には何も保存しない。Web UI には出ない
- エージェントは `[tapi] agent` で変えられる([設定](#設定))

### TIPS

pekoe の機能を「こう使うと便利」と 1 つずつ紹介する。出るのは 2 か所:

- [4] の空き表示(セッションを選んでいないとき、worktree の作成中)。tapi の顔とキャプションの下に `tip` の見出しと 1 行の説明、詳しい説明を出す。`.` で次の tip。高さや幅が足りなければ詳しい説明から省き、それでも入らなければ出さない
- tapi chat の最初の返事のあいさつ。「なんか tip ちょうだい」と頼めば次の 1 つ、「もっと詳しく」と聞けば tip が指す docs を読んで説明する(tapi は `pekoe tip` と `pekoe tip --doc` を使う)

見せた tip は `~/.local/share/pekoe/tips.json` に記録し、全部を一巡するまで同じものは出さない(TUI と tapi chat で共通)。`[tips] enable = false` で両方とも止まる。一覧は `pekoe tip --list`、中身は `internal/tips/tips.toml`。TIPS の文面だけは日本語(ほかの表示は英語)。

## レビュー

- `done` になったセッションはレビュー待ちに入り、Stop hook で `test` が自動で走る
- 承認は hunk 単位(パス + 変更内容のハッシュで識別するので行番号がずれても残る)
- `S`(nvim では `:PekoeSend`)で未送信コメントを 1 通にまとめて送り、レビュー地点 `refs/pekoe/<slug>/review-<n>` を記録。次のレビューはそこからの差分だけ
- 各 hunk には「どのプロンプトから生まれたか」(`← prompt #3: …`)が付く
- `?` で hunk の意図をエージェントに聞くと、返事が Stop hook で切り出されて表示される
- `pekoe review gh#123` で人の PR も同じ画面で。コメントは `gh api` で GitHub review に送られる
- `[review] gatekeeper` に読み取り専用エージェントのコマンドを書くと、危ない箇所の一覧が Comments パネルと nvim の `:PekoeRisks`(quickfix)に出る

## 連携

### Neovim

[nvim/README.md](nvim/README.md)。`{ dir = "~/repos/pekoe/nvim" }` を lazy.nvim に足す(pekoe が起動する nvim には自動で読み込まれる)。レビュー面(`:PekoeReview` `:PekoeSend`)のほか、選択範囲を送る `<leader>bs`、選択から新セッションの `<leader>bn`、許可待ちのフロート、`:PekoeSessions` `:PekoeNew` `:PekoeAnswer` `:PekoeFollow` `:PekoeFixDiagnostics` `:PekoeRisks`。

### Chrome 拡張

[extension/README.md](extension/README.md)。`chrome://extensions` → デベロッパーモード → パッケージ化されていない拡張機能を読み込む → `pekoe ext` の port と token をオプションに入れる。⌘⇧E で要素を選び、指示と切り抜きスクショを送る。localhost のページのエラーと開いているタブも pekoed に届く。

### MCP

`claude mcp add pekoe -- pekoe mcp` で登録する。ツールは `open_in_editor(path, line, note?)`(nvim がその行に飛びメモを出す)と `session_list()`。

### Claude Code スキル

[skills/pekoe/SKILL.md](skills/pekoe/SKILL.md)。main のチェックアウトで動く Claude Code をハブにして、変更依頼を `pekoe new` で別 worktree・別エージェントに任せ、追加の指示も main から `pekoe send` で送るためのスキル。リポジトリのルートで `ln -s "$PWD/skills/pekoe" ~/.claude/skills/pekoe` としてインストールする(`~/.claude/skills/pekoe` が既にある場合は先に退避か削除する)。

### エージェントに pekoe を使わせるときの権限

エージェントは同じユーザーで動くので、pekoed のソケットにつなげる。pekoed は「人間がその場で判断・実行する操作」(許可待ちへの回答、生キー、tapi chat への入力、設定の書き込み、`dev` や run 窓でのコマンド実行、デーモンの停止)を、`human-token` を付けた呼び出しにだけ許す。token は `~/.local/share/pekoe/human-token`(`$PEKOE_HOME` があれば `$PEKOE_HOME/share/human-token`、0600)にあり、TUI・CLI・Neovim が読んで付ける。エージェントのペインの環境には token もそのパスも渡さない。対象のメソッドは [docs/api.md](docs/api.md) の「人間だけの操作」。

ただし同じユーザーのエージェントはファイルを読めてしまう。次の 2 つを合わせて使う。

1. **token のファイルをエージェントから読めなくする。** Claude Code なら `~/.claude/settings.json` で sandbox を有効にし、`sandbox.filesystem.denyRead`(Bash のコマンドとその子プロセスに OS レベルで効く)と `permissions.deny` の `Read(...)`(Read ツールに効く)の両方に入れる。`$PEKOE_HOME` や `$XDG_DATA_HOME` を変えているならそのパスにする

   ```json
   {
     "permissions": {
       "deny": ["Read(~/.local/share/pekoe/human-token)"]
     },
     "sandbox": {
       "enabled": true,
       "filesystem": {
         "denyRead": ["~/.local/share/pekoe/human-token"]
       }
     }
   }
   ```

   設定したら、エージェントに `cat ~/.local/share/pekoe/human-token` と `pekoe answer no-such-session deny` を試させて、前者が読めず、後者が `only a person can do this` で断られる(`no such session` ではない)ことを確かめる。キーの名前や挙動は Claude Code の版で変わりうるので、[sandbox](https://code.claude.com/docs/en/sandboxing) と [permissions](https://code.claude.com/docs/en/permissions) のドキュメントも見る。sandbox の中から pekoe がソケットにつながらないときは、sandbox の unix ソケットの設定を見直す

2. **`Bash(pekoe *)` のように pekoe を丸ごと許可しない。** ハブ(スキル)が使うのは `ls`・`new`・`send` なので、許可リストに入れるならそこだけに絞る。`pekoe event`(hooks 用)、`answer`、`dev`、`init`、`rm`、`land`、`daemon` は入れない

   ```json
   {
     "permissions": {
       "allow": ["Bash(pekoe ls)", "Bash(pekoe --json ls)", "Bash(pekoe --json new *)", "Bash(pekoe send *)"]
     }
   }
   ```

Web UI と Chrome 拡張はソケットを通らない人間の入口なので、それぞれの認証だけで使える。脅威モデルは [docs/web.md](docs/web.md#脅威モデル)。

### iPhone から(Web UI)

`make install WEB=1` のビルドだけ。pekoed に Tailscale の tsnet を埋め込み、pekoed 自身が tailnet の 1 台(`https://pekoe.<tailnet>.ts.net/`)として HTTPS で待ち受ける。できるのはセッション一覧・許可待ちへの allow / deny(確認つき、`always` は出さない)・`send`・エージェント画面の表示(色つき)だけ。localhost にポートは開けず、接続ごとに Tailscale の WhoIs で `allow_users` を確かめる。

1. tailnet で MagicDNS と HTTPS を有効にし、`[web]` を書いて pekoed を再起動
2. `pekoe web` に出るログイン URL を開いて tailnet に参加(`TS_AUTHKEY` でもよい)
3. `pekoe web` の URL を iPhone の Safari で開き、ホーム画面に追加

証明書のホスト名は Certificate Transparency ログに載る。設計・脅威モデルは [docs/web.md](docs/web.md)。

## 設定

`~/.config/pekoe/repos/<repo-id>.toml`(repo-id は origin URL の正規化。clone し直しても同じ設定が当たる。`pekoe init` で下書きできる):

```toml
agent = "claude"                  # 既定のエージェント

[worktree]
copy  = [".env*", ".envrc"]
clone = ["node_modules"]          # APFS クローン(cp -c)
base  = "main"                    # 既定: origin の HEAD か main

[hooks]
setup    = ["mise install", "pnpm install --offline", "createdb app_{{slug}}"]
dev      = "pnpm dev --port {{port}}"
test     = "pnpm test"
teardown = ["dropdb app_{{slug}}"]

[env]
PORT = "{{port}}"
DATABASE_URL = "postgres:///app_{{slug}}"

[review]
gatekeeper = "claude -p '危ない箇所を path:line: text で挙げて'"   # diff を stdin で受ける
```

変数は `{{slug}}` `{{branch}}` `{{port}}` `{{path}}` `{{repo}}`。setup は順に実行し、失敗したら止めて左ペインに赤で出す。`[hooks]` のコマンドに埋めるとき、シェルが特別に扱う文字(空白・`$`・`` ` ``・`;` など)を含む値は単引用符で囲んで 1 語として渡す(`feat/x` のような普通の値はそのまま)。`[env]` の値には引用符を付けない。

`~/.config/pekoe/config.toml`(全体):

```toml
animate = true              # 状態アイコンのアニメーション
[http]
port = 47777                # Chrome 拡張用(127.0.0.1)
[ports]
base = 3001                 # ポート台帳の開始番号
[keys]
prefix = "ctrl+q"           # 入力から抜けるキー
[review]
surface = "nvim"            # nvim | tui
[notify]
ntfy = "https://ntfy.sh/my-topic"   # 待ちが発生したら通知
[picks]
auto_send = true            # 指示つきのピックを、ページのポートを持つセッションへすぐ送る
[web]                       # Web UI(make install WEB=1 のビルドのみ、docs/web.md)
enable        = true
allow_users   = ["you@github"]      # Tailscale のログイン名
allow_devices = ["iphone-15"]       # 任意。この端末だけに絞る
hostname      = "pekoe"             # tailnet のマシン名
[theme]
mode  = "auto"              # truecolor | ansi | none | auto
icons = "nerd"              # nerd | unicode | ascii
[theme.colors]              # 役割単位で上書き(docs/ui-guidelines.md のトークン)
primary = "#ff87da"
[mascot]                    # マスコット tapi(docs/mascot.md)
shape   = "auto"            # auto | round(六分ブロック) | block(四分ブロック)
glyphs  = "auto"            # auto(● の幅を測る) | unicode | narrow(● を 🞄 に) | ascii
palette = "dark"            # dark(D ミルキー＋影) | light(E キャラメル)
splash  = true              # 起動時のスプラッシュ
[mascot.colors]             # hi / bd / sh / eye を上書き
bd = "#e4c49a"
[agents.codex]              # Claude Code 以外のエージェント
command = "codex"
allow   = "y"
deny    = "n"
[tapi]                      # tapi chat(TUI の t)
agent = "claude"            # [agents.*] の名前。claude 以外は --add-dir なし(cwd だけ)
[tips]                      # TIPS(4 の空き表示と tapi chat のあいさつ)
enable = true               # false で両方とも出さない
```

`[agents.<name>]` には `command` `resume` `prompt`(`arg` | `paste`)`allow` `always` `deny` `hooks` を書ける。Claude Code の許可メニューへの回答は既定で `1`(許可)/ `2`(常に許可)/ `Esc`(拒否)を送る。挙動が変わったら `[agents.claude]` で直せる。

## データの置き場(リポジトリを汚さない)

| データ | 置き場 |
| --- | --- |
| 設定 | `~/.config/pekoe/` |
| 状態 DB・socket・ログ・picks | `~/.local/share/pekoe/` |
| TUI のペイン幅・ズーム | `~/.local/share/pekoe/layout.json`(`^q` `<` `>` `=` `z` で書き換わる) |
| 見せた tip の記録 | `~/.local/share/pekoe/tips.json`(一巡すると空からやり直す) |
| tapi chat の稼働ディレクトリ | `~/.local/share/pekoe/tapi/`(会話の記録は Claude Code の `~/.claude/projects/` 側。`pekoe tapi reset` で消える) |
| 人間用トークン(許可待ちへの回答などに要る。エージェントに読ませない) | `~/.local/share/pekoe/human-token` |
| Web UI の tsnet ノードの鍵 | `~/.local/share/pekoe/tsnet/`(`[web]` が有効なときだけ) |
| プロンプト単位スナップショット / レビュー地点 | `refs/pekoe/<slug>/prompt-<n>` / `review-<n>`(push されず `git log` にも出ない) |
| worktree | `~/worktrees/<repo>/<slug>`(pekoe が消すのはこの配下だけ) |

## boba からの移行

旧名 boba で使っていた環境は `pekoe migrate` で一度に移す。旧名を読み続ける互換処理は無いので、以下を順に行う。

1. 旧エージェントを終わらせ、`boba daemon stop` でデーモンを止める(`pekoe migrate` はデーモンが動いていると中止する)
2. `make install` で pekoe を入れ、旧 `~/.local/bin/boba` と `~/.local/bin/bobad` を消す
3. `pekoe migrate --dry-run` で何が変わるかを確かめてから `pekoe migrate` を実行する
   - `~/.config/boba` → `~/.config/pekoe`、`~/.local/share/boba` → `~/.local/share/pekoe` に移し、`bobad.sock` `bobad.log` などを `pekoed.*` に改名
   - `state.db` の中の旧パスと旧 ref を書き換え、各リポジトリの `refs/boba/*` を `refs/pekoe/*` にコピー(`--prune-old` で旧 `refs/boba/*` も消す)
   - セッションに保存された環境変数の `BOBA_PORT=…` などを `PEKOE_PORT=…` に改名する(値やプロンプト本文は変えない)
   - `~/.claude/settings.json` の `boba event` の hooks を `pekoe event` に置き換える
   - 最後に手作業で残っているものを表示する
4. MCP を登録し直す: `claude mcp remove boba && claude mcp add pekoe -- pekoe mcp`
5. launchd を使っていたら `make uninstall-launchd && make install-launchd`
6. nvim の設定の `require("boba")` を `require("pekoe")` に(lazy.nvim の `dir` / `name` も)替え、Chrome 拡張を `chrome://extensions` で再読み込みする
7. 旧エージェントが残っていなければ `tmux -L boba kill-server` で旧 tmux サーバーを止める

すでに移行を済ませたあとで dev 窓などに `BOBA_PORT` が渡っている(セッションの環境変数が旧名のまま残っている)ときは、`pekoe daemon stop` でデーモンを止めて `pekoe migrate --dry-run` で `env BOBA_* → PEKOE_*` の行を確かめ、`pekoe migrate` をもう一度実行する。旧ディレクトリが無ければ移動は飛ばして `state.db` だけを直す。デーモンを立て直したら、開いている dev 窓・run 窓は閉じて開き直す(すでに起動している窓の環境は変わらない)。

## 開発

```sh
make build && make test     # go vet + go test(既定と -tags web の両方)+ nvim のヘッドレステスト
```

テストや試用で本物の環境を汚したくないときは `PEKOE_HOME`(設定・状態の置き場)、`PEKOE_SOCK`、`PEKOE_TMUX_SOCKET`、`PEKOE_WORKTREES` で隔離できる。まとめて切るには `scripts/sandbox.sh` を使う:

```sh
scripts/sandbox.sh                    # 隔離したデーモンを立ててサブシェルへ。抜けると片付く
scripts/sandbox.sh pekoe new feat/x   # コマンドを 1 つだけ
WEB=1 scripts/sandbox.sh              # Web UI 入りのビルドで
```

一時ディレクトリに 4 つを切り、今のソースをビルドした `pekoe` でデーモンを立てる(`PEKOE_BIN` で既製のバイナリも指定できる)。終わると、失敗や Ctrl-C で抜けたときも含めて `pekoe daemon stop --kill-agents --yes` で tmux サーバーごと止め、一時ディレクトリを消す。手で隔離環境を立てたときも、最後に同じ環境変数のまま `pekoe daemon stop --kill-agents --yes` で片付ける(`pekoe daemon stop` だけでは tmux サーバーと偽エージェントが残る)。tmux サーバーの pid は起動時に `$PEKOE_HOME/share/tmux-<socket>.pid` に記録するので、`TMUX_TMPDIR` が違うなどで `kill-server` が届かなくても止められる。

### リリースの切り方

`v*` のタグを push すると GitHub Actions(`.github/workflows/release.yml`)が GoReleaser(`.goreleaser.yaml`)で 4 プラットフォームをクロスビルドし、アーカイブ・`checksums.txt`・前のタグからの changelog を付けて Release を作り、アーカイブごとにビルド来歴(attestation)を付ける。ビルドは go.mod の `go` 行の Go だけで行い(`GOTOOLCHAIN=local`)、その前に同じ Go で govulncheck を回して、既知の脆弱性に届くなら止める。既定ビルドと `-tags web` の govulncheck は `.github/workflows/govulncheck.yml` が push・PR・毎週回す。手元では `make vulncheck`。ワークフローのアクションはコミット SHA で固定し、Dependabot(`.github/dependabot.yml`)が上げる。バージョンはタグから ldflags で `pekoe version` に入る(手元の `make build` は `git describe`、`go build` だけなら `dev`)。

```sh
git tag v0.2.0 && git push origin v0.2.0
goreleaser release --snapshot --clean   # 手元で試す(dist/ にできる。公開はしない)
```

goreleaser はワークフローでも手元でも Go のモジュールプロキシから入れる(GitHub Release のアセット配信が落ちることがあるため)。版はワークフローの `GORELEASER_VERSION` に合わせる。

```sh
GOTOOLCHAIN=auto go install github.com/goreleaser/goreleaser/v2@v2.20.0
```

### 現状の制約

- ターミナル層は設計書の段階 1(tmux エンジン)。段階 2(libghostty-vt の自前 mux)は未着手で、設計どおり任意
- Kitty graphics のパススルーは、右ペインを文字として描き直す tmux エンジンでは通せないため段階 2 の課題(TUI 内プレビューの画像は pekoe 自身が Kitty graphics で描く)
