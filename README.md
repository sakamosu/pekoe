# boba

```
         ╻
╭────────┸───────╮
│                │    boba
│ 🬞🬹🬹🬏 🬞🬹🬹🬏 🬞🬹🬹🬏 │    agents in worktrees,
│ █●●█ █◕◕█ █^^█ │    reviewed hunk by hunk
│ 🬁🬎🬎🬀 🬁🬎🬎🬀 🬁🬎🬎🬀 │
╰────────────────╯
```

Orca の開発体験(複数エージェントの並列運用・worktree 分離・ブラウザ連携・差分レビュー)を、Ghostty の中で完結させるターミナルツール。Claude Code の使い方は変えず、その周辺作業(切り替え・待ち・worktree・ブラウザ・レビュー)だけを引き受ける。

- 設計書: [docs/design.md](docs/design.md)
- デザイン原則: [docs/ui-guidelines.md](docs/ui-guidelines.md)
- モック: [docs/mock.html](docs/mock.html)
- デーモン API: [docs/api.md](docs/api.md)

```
             ┌── TUI (boba) ─────┐
 Chrome 拡張 ─┤                   ├─ bobad ── tmux -L boba ── claude / codex / …(worktree ごとの pty)
  (HTTP+token)│  CLI / Neovim / MCP├─ (unix socket, JSON Lines)        ▲
             └───────────────────┘        ▲                          │ hooks → `boba event`
                                 state.db / refs/boba/*                 │
```

賢さはすべて bobad にあり、TUI・CLI・Neovim・Chrome 拡張・MCP は同じ API を叩く表示層。

## インストール

必要なもの: Go 1.25+、tmux 3.3+、git。任意: gh(land・PR レビュー)、Neovim 0.10+(レビュー面)、Chrome。

```sh
make install            # ~/.local/bin/boba (+ bobad), nvim/ と extension/ を ~/.local/share/boba に link
boba hooks install      # ~/.claude/settings.json に hooks を追加(バックアップを残す)
make install-launchd    # 任意: ログイン時に bobad を起動
```

hooks は `SessionStart` `UserPromptSubmit` `Notification` `Stop` `PreToolUse` `PostToolUse` `SessionEnd` で `boba event` を呼ぶだけ。プロジェクト側の `.claude/` は触らない。`boba hooks uninstall` で boba の分だけ外す。

## 使い方

```sh
cd ~/src/app
boba init                    # ~/.config/boba/repos/<repo-id>.toml を下書き(lockfile・.envrc・無視ファイルから)
boba new feat/csv --prompt "CSV エクスポートを追加"   # worktree → copy → setup → port → claude 起動
boba new feat/invoice --from gh#128                     # issue 本文を初期プロンプトに
boba                         # TUI
```

| コマンド | すること |
| --- | --- |
| `boba` | TUI(デーモンがなければ起動) |
| `boba init` | 今のリポジトリの設定を検出して下書き |
| `boba new <branch> [--from gh#N] [--agent codex] [--prompt …]` | worktree + セッション作成 |
| `boba open <worktree>` / `boba adopt` | 既存 worktree で起動(過去の Claude Code セッションを resume するか選ぶ)/ 手動起動したエージェントを取り込む |
| `boba ls [--json]` | セッション一覧と状態 |
| `boba send <slug> <text>` | pty に文字を送る(`-` で stdin) |
| `boba review [<slug> \| gh#N]` | レビュー面(nvim か組み込み) |
| `boba land <slug>` | rebase → test → push → `gh pr create`。マージ検出後に teardown と worktree 削除 |
| `boba rm <slug>` | teardown → worktree 削除(`~/worktrees` 配下のみ) |
| `boba dev <slug>` | `dev` をそのセッションのポートで起動 / 停止 |
| `boba undo <slug> [n]` | プロンプト単位 undo |
| `boba answer <slug> allow\|deny\|always` | 許可待ちに答える |
| `boba ext` | Chrome 拡張に入れる port と token |
| `boba mcp` | エージェント用 MCP サーバ(`claude mcp add boba -- boba mcp`) |
| `boba daemon start\|stop\|status` | デーモン操作 |

### TUI

左に番号付きパネル([1] Sessions、[2] Worktrees ⇄ Browser、[3] Changes ⇄ Picks)、右に [4] Agent(エージェントの画面そのもの)、最下行に powerline のステータスとキーヒント。100 列未満では左列がタブ 1 枚になる。

| キー | 動作 |
| --- | --- |
| `j` `k` / `↵` | セッション選択 / 右ペインに入って入力(`^q` 以外のキーはすべて claude へ) |
| `^q` | 入力モードから抜けて Sessions に戻る(`[keys] prefix` で変更可)。左のパネルをクリックしても抜けられる |
| `h` / `Esc` | Agent パネル(入力モードでないとき)から左に戻る |
| `n` | 新しい worktree + セッション |
| `]a` `[a` | 次の待ちセッション / 元の場所に戻る |
| `]r` | 次のレビュー待ち |
| `r` | レビュー面(右ペインが nvim に入れ替わる。claude は裏で動き続ける) |
| `b` `l` `d` | ポートを Chrome で開く / land / dev server |
| `u` `R` | 最後のプロンプトを undo / エージェント再起動(resume) |
| `x` `D` | セッション終了(worktree は残す)/ worktree 削除(確認あり) |
| `/` `?` `:` | 絞り込み / キー一覧(選んで Enter で実行) / コマンド |
| `1`–`4` `Tab` `[` `]` | パネル移動 / サブタブ切替 |

許可待ちになったセッションを選ぶと Permission モーダルが出る(`y` 許可・`n` 拒否・`A` 常に許可・`g` そのセッションへ・`esc` 後で)。Chrome から要素が届くと Agent ペインにカードが出る(`↵` 送信・`^s` 別セッション・`^n` 新セッション・`esc` 破棄)。

### レビュー

- `done` になったセッションはレビュー待ちに入り、Stop hook で `test` が自動で走る
- 承認は hunk 単位(パス + 変更内容のハッシュで識別するので行番号がずれても残る)
- `S`(nvim では `:BobaSend`)で未送信コメントを 1 通にまとめて送り、レビュー地点 `refs/boba/<slug>/review-<n>` を記録。次のレビューはそこからの差分だけ
- 各 hunk には「どのプロンプトから生まれたか」(`← 指示 #3: …`)が付く
- `?` で hunk の意図をエージェントに聞くと、返事が Stop hook で切り出されて表示される
- `boba review gh#123` で人の PR も同じ画面で。コメントは `gh api` で GitHub review に送られる
- `[review] gatekeeper` に読み取り専用エージェントのコマンドを書くと、危ない箇所の一覧が Comments パネルと nvim の `:BobaRisks`(quickfix)に出る

### 設定

`~/.config/boba/repos/<repo-id>.toml`(repo-id は origin URL の正規化。clone し直しても同じ設定が当たる):

```toml
[worktree]
copy  = [".env*", ".envrc"]
clone = ["node_modules"]          # APFS クローン(cp -c)

[hooks]
setup    = ["mise install", "pnpm install --offline", "createdb app_{{slug}}"]
dev      = "pnpm dev --port {{port}}"
test     = "pnpm test"
teardown = ["dropdb app_{{slug}}"]

[env]
PORT = "{{port}}"
DATABASE_URL = "postgres:///app_{{slug}}"
```

変数は `{{slug}}` `{{branch}}` `{{port}}` `{{path}}` `{{repo}}`。setup は順に実行し、失敗したら止めて左ペインに赤で出す。

`~/.config/boba/config.toml`(全体):

```toml
[http]
port = 47777                # Chrome 拡張用
[ports]
base = 3001                 # ポート台帳の開始番号
[keys]
prefix = "ctrl+q"
[review]
surface = "nvim"            # nvim | tui
[notify]
ntfy = "https://ntfy.sh/my-topic"   # 待ちが発生したら通知
[theme]
mode  = "auto"              # truecolor | ansi | none | auto
icons = "nerd"              # nerd | unicode | ascii
[theme.colors]              # 役割単位で上書き(docs/ui-guidelines.md のトークン)
primary = "#ff87da"
[mascot]                    # マスコット tapi(docs/mascot.md)
shape   = "auto"            # auto | round(六分ブロック) | block(四分ブロック)
glyphs  = "auto"            # auto(● の幅を測る) | unicode | narrow(● を ⬤ に) | ascii
palette = "dark"            # dark(D ミルキー＋影) | light(E キャラメル)
splash  = true              # 起動時のカップ
[mascot.colors]             # hi / bd / sh / eye / tea を上書き
bd = "#e4c49a"
[agents.codex]              # Claude Code 以外のエージェント
command = "codex"
allow = "y"
deny  = "n"
```

### Chrome 拡張と Neovim

- 拡張: [extension/README.md](extension/README.md)。`chrome://extensions` → デベロッパーモード → パッケージ化されていない拡張機能を読み込む → `boba ext` の port と token をオプションに入れる。⌘⇧E で要素を選ぶ
- Neovim: [nvim/README.md](nvim/README.md)。`{ dir = "~/repos/boba/nvim" }` を lazy.nvim に足す(boba が起動する nvim には自動で読み込まれる)

## データの置き場(リポジトリを汚さない)

| データ | 置き場 |
| --- | --- |
| 設定 | `~/.config/boba/` |
| 状態 DB・socket・ログ・picks | `~/.local/share/boba/` |
| プロンプト単位スナップショット / レビュー地点 | `refs/boba/<slug>/prompt-<n>` / `review-<n>`(push されず `git log` にも出ない) |
| worktree | `~/worktrees/<repo>/<slug>`(boba が消すのはこの配下だけ) |

## 開発

```sh
make build && make test     # go vet + go test + nvim のヘッドレステスト
```

テストや試用で本物の環境を汚したくないときは `BOBA_HOME`(設定・状態の置き場)、`BOBA_SOCK`、`BOBA_TMUX_SOCKET`、`BOBA_WORKTREES` で隔離できる。

### 現状の制約

- ターミナル層は設計書の段階 1(tmux エンジン)。段階 2(libghostty-vt の自前 mux)は未着手で、設計どおり任意
- パススルーは OSC 52(クリップボード)・OSC 8(リンク)・OSC 9/99/777(通知)・マウス(SGR)・Kitty keyboard protocol(Shift+Enter などがエージェントに届く。`BOBA_KITTY=0` で無効)に対応。Kitty graphics は、右ペインを文字として描き直す tmux エンジンでは通せないため段階 2 の課題
- Claude Code の許可メニューへの回答は `1`(許可)/ `2`(常に許可)/ `Esc`(拒否)を送る。挙動が変わったら `[agents.claude] allow/always/deny` で直せる
