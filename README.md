<p align="center"><img src="docs/images/boba-logo.webp" alt="boba" width="320"></p>

<p align="center"><b>agents in worktrees, reviewed hunk by hunk</b></p>

# boba

Orca の開発体験(複数エージェントの並列運用・worktree 分離・ブラウザ連携・差分レビュー)を、Ghostty の中で完結させるターミナルツール。Claude Code の使い方は変えず、その周辺作業(切り替え・待ち・worktree・ブラウザ・レビュー・PR)だけを引き受ける。

- 1 タスク = 1 worktree = 1 エージェント。`boba new` で worktree を切り、設定ファイルのコピー・setup・ポート割り当てまで済ませてから claude(や codex)を起動する
- TUI でセッションを一覧し、待ち・許可待ち・レビュー待ちに順に飛ぶ。最下行のマスコット tapi が全体の状態を表す
- 変更は hunk 単位でレビューして、コメントを 1 通にまとめてエージェントに返す(Neovim か組み込み)
- Chrome 拡張で画面の要素を選んで指示と一緒に送る。dev server のスクショは TUI の中に出せる
- iPhone から Tailscale 経由で許可待ちに答えたり一言送ったりできる(任意のビルド)

## 構成

```
 TUI (boba) ─────┐
 CLI ────────────┤ unix socket                       tmux -L boba
 Neovim ─────────┤ (JSON Lines)                ┌── claude / codex / …
 MCP (boba mcp) ─┼──────────────── bobad ──────┤   (worktree ごとの pty)
 Chrome 拡張 ────┤ HTTP 127.0.0.1 + token      └──────────┬──────────
 Web UI (iPhone) ┘ HTTPS on tailnet (tsnet)                │ hooks → `boba event`
                                  │                        │
                       state.db / refs/boba/*  ◀───────────┘
```

賢さはすべて bobad にあり、TUI・CLI・Neovim・Chrome 拡張・MCP・Web UI は同じ API を叩く表示層。bobad を止めてもエージェントは tmux の中で動き続け、次に起動したときに拾い直す。

## ドキュメント

| | |
| --- | --- |
| [docs/design.md](docs/design.md) | 設計書(状態モデル・worktree フロー・ターミナル層・レビュー・CLI とキー) |
| [docs/api.md](docs/api.md) | bobad の API(unix socket・HTTP・HTTPS・MCP) |
| [docs/web.md](docs/web.md) | iPhone 用 Web UI の構成・設定・脅威モデル |
| [docs/mascot.md](docs/mascot.md) | マスコット tapi の形・状態・表情 |
| [docs/ui-guidelines.md](docs/ui-guidelines.md) | TUI のデザイン原則(配色トークン・レイアウト・アイコン) |
| [docs/verification.md](docs/verification.md) | 設計書・デザイン原則・モックと実装の対応表 |
| [nvim/README.md](nvim/README.md) / [extension/README.md](extension/README.md) | Neovim プラグイン / Chrome 拡張 |

## インストール

必要なもの: Go 1.25+、tmux 3.3+、git。任意: gh(issue・land・PR ビュー・PR レビュー)、Neovim 0.10+(レビュー面)、Chrome(拡張・TUI 内プレビュー)、Tailscale(Web UI)。

```sh
make install            # ~/.local/bin/boba (+ bobad), nvim/ と extension/ を ~/.local/share/boba に link
make install WEB=1      # iPhone 用 Web UI も入れる(-tags web、Tailscale の tsnet をリンク)
boba hooks install      # ~/.claude/settings.json に hooks を追加(バックアップを残す)
make install-launchd    # 任意: ログイン時に bobad を起動
```

既定のビルドには Tailscale のコードを一切含めない。入れ直したあとは `boba daemon stop; boba daemon start` で bobad を新しいバイナリに替える。

hooks は `SessionStart` `UserPromptSubmit` `Notification` `Stop` `PreToolUse` `PostToolUse` `SessionEnd` で `boba event` を呼ぶだけ。プロジェクト側の `.claude/` は触らない。`boba hooks status` で確認、`boba hooks uninstall` で boba の分だけ外す。

## クイックスタート

```sh
cd ~/src/app
boba init                                             # ~/.config/boba/repos/<repo-id>.toml を下書き
boba new feat/csv --prompt "CSV エクスポートを追加"    # worktree → copy → setup → port → claude 起動
boba new feat/invoice --from gh#128                   # issue 本文を初期プロンプトに
boba                                                  # TUI
```

`boba init` は lockfile・`.envrc`・git の無視ファイルから setup や copy を推測し、コピーする無視ファイルを番号で選ばせてから書く。手で始めたエージェントは `boba adopt` で、既存の worktree は `boba open` で取り込める。

## コマンド

| コマンド | すること |
| --- | --- |
| `boba` | TUI(デーモンがなければ起動) |
| `boba init [<dir>] [--yes] [--overwrite] [--print]` | リポジトリの設定を検出して下書き。`--print` は書かずに出すだけ |
| `boba new <branch> [--repo R] [--from gh#N] [--agent codex] [--base REF] [--prompt TEXT]` | worktree + セッション作成。setup の進み具合を出し、エージェントが起動したら戻る |
| `boba open [<worktree>] [--resume\|--new] [--agent NAME]` | 既存 worktree で起動。過去の Claude Code セッションを resume するか選ぶ(`--resume` で resume、`--new` で新規) |
| `boba adopt [<path>] [--yes]` | boba の外で動いているエージェントを止め、同じセッションを boba の中で resume する |
| `boba ls [--all\|--repo R]` | セッション一覧と状態。リポジトリの中ではそのリポジトリだけ、外では全部 |
| `boba issues [--repo R] [--all\|--label L] [--refresh]` | GitHub の open issue(既定は自分担当)。セッションがあれば状態とブランチを添える |
| `boba issues <N> [--repo R]` | issue の本文とコメント |
| `boba conflicts [--repo R]` | 衝突予報。同じファイルを触っているセッションのペアと、衝突するか(`git merge-tree`) |
| `boba send <slug> <text> [--no-enter]` | pty に文字を送る(既定で Enter を足す。`-` で stdin) |
| `boba review [<slug> \| gh#N [--repo R]]` | レビュー面。引数なしなら次のレビュー待ち。`gh#N` で人の PR |
| `boba land <slug>` | rebase → test → push → `gh pr create`。マージ検出後に teardown と worktree 削除 |
| `boba rm <slug> [--force] [--yes]` | teardown → worktree 削除(`~/worktrees` 配下のみ。外のものはセッションを忘れるだけ) |
| `boba dev <slug>` | `dev` をそのセッションのポートで起動 / 停止 |
| `boba undo <slug> [n]` | 最後(か n 番目)のプロンプトの変更を取り消す |
| `boba answer <slug> allow\|deny\|always` | 許可待ちに答える |
| `boba attach <slug> [agent\|review\|dev]` | tmux の窓に直接 attach(デバッグ用) |
| `boba hooks install\|uninstall\|status [--yes]` | Claude Code の hooks を足す / 外す / 確認 |
| `boba ext` | Chrome 拡張に入れる port と token |
| `boba web` | Web UI の状態・URL・初回のログイン URL |
| `boba mcp` | エージェント用 MCP サーバ(stdio) |
| `boba daemon start\|stop\|status` | デーモン操作。`stop` はプロセスが終わるまで待つ。エージェントは tmux の中で動き続ける |
| `boba daemon stop --kill-agents [--yes]` | tmux サーバー(`$BOBA_TMUX_SOCKET`)も止める。**既定のソケット `boba` に使うと全エージェントが止まる**。隔離環境の片付け用 |
| `boba version` | バージョン |

- `R` は repo-id、短い名前(`~/worktrees/<name>` の name)、パスのどれでもよい。slug を取るコマンドは `<repo>/<slug>` でも指定できる
- `--json` で機械可読に出す(`init` `new` `open` `ls` `issues` `conflicts` `land` `dev` `ext` `web` `daemon status`)。サブコマンドの前後どちらにも書ける
- `--help` / `-h` は使い方を出すだけで何もしない(終了コード 0)。知らないフラグや値のない `--repo` はエラー(終了コード 2)。`-` で始まる文字列を位置引数にするときは `--` の後に書く(例: `boba send <slug> -- --verbose を外して`)
- `boba event <Hook>` は hooks から呼ばれる内部用。失敗しても常に終了コード 0

## TUI

左に番号付きパネル([1] Sessions、[2] Worktrees ⇄ Browser ⇄ Issues、[3] Changes ⇄ Picks)、右に [4] Agent(エージェントの画面そのもの。preview・dev・PR の窓に切り替わる)、最下行に powerline のステータスとキーヒント。100 列未満では左列がタブ 1 枚になる。複数リポジトリのセッションはリポジトリごとの見出しでまとめる。

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
| `n` | 新しい worktree + セッション |
| `w` | リポジトリを選ぶ |
| `]a` `[a` | 次の待ちセッション(PR に動きがあったものも)/ 元の場所に戻る |
| `]r` | 次のレビュー待ち |
| `r` | レビュー面(右ペインが nvim に入れ替わる。エージェントは裏で動き続ける) |
| `p` | PR ビュー(checks・レビュー・スレッド。`space` で選んで `s` でエージェントに送る) |
| `v` | preview 窓(dev server のスクショを TUI 内に表示。`m` モバイル幅・`c` before/after)→ dev 窓 → agent |
| `b` `l` `d` | ポートを Chrome で開く / land(衝突予報つきの確認)/ dev server |
| `u` `R` | 最後のプロンプトを undo / エージェント再起動(resume) |
| `s` `i` `↵` | エラーカード(dev server やブラウザのエラーを bobad が見つけて出す)を送る / 無視 / 詳細。カードがある間、入力は `a` |
| `x` `D` | セッション終了(worktree は残す)/ worktree 削除(確認あり) |
| `/` `?` `:` | 絞り込み / キー一覧(選んで Enter で実行)/ コマンド(`new` `send` `land` `rm` `dev` `undo` `review` `ext` `q`) |
| `1`–`4` `Tab` `[` `]` | パネル移動 / サブタブ切替 |
| `q` | 終了 |

- 許可待ちになったセッションを選ぶと Permission モーダルが出る(`y` 許可・`n` 拒否・`A` 常に許可・`g` そのセッションへ・`esc` 後で)
- [2] Issues では `f` で自分担当 → 全 open → ラベル指定を切り替え、`n` で issue からセッションを作る
- [2] Browser は Chrome で開いている localhost のタブをポートでセッションに結びつける。`E` でコンソールのエラーを送る
- Chrome から要素が届くと Agent ペインにカードが出る(`↵` 送信・`^s` 別セッション・`^n` 新セッション・`esc` 破棄)。指示つきのピックは、ページのポートを持つセッションにそのまま送る(`[picks] auto_send`)
- 同じファイルを触っているセッションには一覧に `⚠N` が付く
- エージェントへのパススルーは OSC 52(クリップボード)・OSC 8(リンク)・OSC 9/99/777(通知)・マウス(SGR)・Kitty keyboard protocol(Shift+Enter などが届く。`BOBA_KITTY=0` で無効)

すべてのキーは [docs/design.md](docs/design.md#cli-コマンドとキーバインド) にある。

## レビュー

- `done` になったセッションはレビュー待ちに入り、Stop hook で `test` が自動で走る
- 承認は hunk 単位(パス + 変更内容のハッシュで識別するので行番号がずれても残る)
- `S`(nvim では `:BobaSend`)で未送信コメントを 1 通にまとめて送り、レビュー地点 `refs/boba/<slug>/review-<n>` を記録。次のレビューはそこからの差分だけ
- 各 hunk には「どのプロンプトから生まれたか」(`← prompt #3: …`)が付く
- `?` で hunk の意図をエージェントに聞くと、返事が Stop hook で切り出されて表示される
- `boba review gh#123` で人の PR も同じ画面で。コメントは `gh api` で GitHub review に送られる
- `[review] gatekeeper` に読み取り専用エージェントのコマンドを書くと、危ない箇所の一覧が Comments パネルと nvim の `:BobaRisks`(quickfix)に出る

## 連携

### Neovim

[nvim/README.md](nvim/README.md)。`{ dir = "~/repos/boba/nvim" }` を lazy.nvim に足す(boba が起動する nvim には自動で読み込まれる)。レビュー面(`:BobaReview` `:BobaSend`)のほか、選択範囲を送る `<leader>bs`、選択から新セッションの `<leader>bn`、許可待ちのフロート、`:BobaSessions` `:BobaNew` `:BobaAnswer` `:BobaFollow` `:BobaFixDiagnostics` `:BobaRisks`。

### Chrome 拡張

[extension/README.md](extension/README.md)。`chrome://extensions` → デベロッパーモード → パッケージ化されていない拡張機能を読み込む → `boba ext` の port と token をオプションに入れる。⌘⇧E で要素を選び、指示と切り抜きスクショを送る。localhost のページのエラーと開いているタブも bobad に届く。

### MCP

`claude mcp add boba -- boba mcp` で登録する。ツールは `open_in_editor(path, line, note?)`(nvim がその行に飛びメモを出す)と `session_list()`。

### iPhone から(Web UI)

`make install WEB=1` のビルドだけ。bobad に Tailscale の tsnet を埋め込み、bobad 自身が tailnet の 1 台(`https://boba.<tailnet>.ts.net/`)として HTTPS で待ち受ける。できるのはセッション一覧・許可待ちへの allow / deny(確認つき、`always` は出さない)・`send`・エージェント画面の表示(色つき)だけ。localhost にポートは開けず、接続ごとに Tailscale の WhoIs で `allow_users` を確かめる。

1. tailnet で MagicDNS と HTTPS を有効にし、`[web]` を書いて bobad を再起動
2. `boba web` に出るログイン URL を開いて tailnet に参加(`TS_AUTHKEY` でもよい)
3. `boba web` の URL を iPhone の Safari で開き、ホーム画面に追加

証明書のホスト名は Certificate Transparency ログに載る。設計・脅威モデルは [docs/web.md](docs/web.md)。

## 設定

`~/.config/boba/repos/<repo-id>.toml`(repo-id は origin URL の正規化。clone し直しても同じ設定が当たる。`boba init` で下書きできる):

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

変数は `{{slug}}` `{{branch}}` `{{port}}` `{{path}}` `{{repo}}`。setup は順に実行し、失敗したら止めて左ペインに赤で出す。

`~/.config/boba/config.toml`(全体):

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
hostname      = "boba"              # tailnet のマシン名
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
```

`[agents.<name>]` には `command` `resume` `prompt`(`arg` | `paste`)`allow` `always` `deny` `hooks` を書ける。Claude Code の許可メニューへの回答は既定で `1`(許可)/ `2`(常に許可)/ `Esc`(拒否)を送る。挙動が変わったら `[agents.claude]` で直せる。

## データの置き場(リポジトリを汚さない)

| データ | 置き場 |
| --- | --- |
| 設定 | `~/.config/boba/` |
| 状態 DB・socket・ログ・picks | `~/.local/share/boba/` |
| Web UI の tsnet ノードの鍵 | `~/.local/share/boba/tsnet/`(`[web]` が有効なときだけ) |
| プロンプト単位スナップショット / レビュー地点 | `refs/boba/<slug>/prompt-<n>` / `review-<n>`(push されず `git log` にも出ない) |
| worktree | `~/worktrees/<repo>/<slug>`(boba が消すのはこの配下だけ) |

## 開発

```sh
make build && make test     # go vet + go test(既定と -tags web の両方)+ nvim のヘッドレステスト
```

テストや試用で本物の環境を汚したくないときは `BOBA_HOME`(設定・状態の置き場)、`BOBA_SOCK`、`BOBA_TMUX_SOCKET`、`BOBA_WORKTREES` で隔離できる。まとめて切るには `scripts/sandbox.sh` を使う:

```sh
scripts/sandbox.sh                    # 隔離したデーモンを立ててサブシェルへ。抜けると片付く
scripts/sandbox.sh boba new feat/x    # コマンドを 1 つだけ
WEB=1 scripts/sandbox.sh              # Web UI 入りのビルドで
```

一時ディレクトリに 4 つを切り、今のソースをビルドした `boba` でデーモンを立てる(`BOBA_BIN` で既製のバイナリも指定できる)。終わると、失敗や Ctrl-C で抜けたときも含めて `boba daemon stop --kill-agents --yes` で tmux サーバーごと止め、一時ディレクトリを消す。手で隔離環境を立てたときも、最後に同じ環境変数のまま `boba daemon stop --kill-agents --yes` で片付ける(`boba daemon stop` だけでは tmux サーバーと偽エージェントが残る)。tmux サーバーの pid は起動時に `$BOBA_HOME/share/tmux-<socket>.pid` に記録するので、`TMUX_TMPDIR` が違うなどで `kill-server` が届かなくても止められる。

### 現状の制約

- ターミナル層は設計書の段階 1(tmux エンジン)。段階 2(libghostty-vt の自前 mux)は未着手で、設計どおり任意
- Kitty graphics のパススルーは、右ペインを文字として描き直す tmux エンジンでは通せないため段階 2 の課題(TUI 内プレビューの画像は boba 自身が Kitty graphics で描く)
