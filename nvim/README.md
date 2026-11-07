# boba.nvim

boba のデーモン(bobad)に unix socket で繋がる Neovim プラグイン。エージェントの変更を hunk 単位でレビューする面と、選択範囲の送信・許可待ちへの返答・診断送信などの日常の小技を担う。boba が起動した nvim も普段の nvim も同じプラグインが同じデーモンに繋がる。

- Neovim 0.10 以上
- 外部依存なし(`vim.uv` と `vim.json` だけ)

## インストール(lazy.nvim)

```lua
{
  dir = "~/repos/boba/nvim",
  name = "boba.nvim",
  opts = {},
}
```

設定を変える場合:

```lua
{
  dir = "~/repos/boba/nvim",
  name = "boba.nvim",
  opts = {
    keymaps = {
      send_range = "<leader>bs",  -- false で割り当てない
      new_session = "<leader>bn",
      review = { approve = "a", reject = "x", comment = "c", ask = "?" },
      approval = { allow = "y", deny = "n", always = "A" },
    },
    approval = true,     -- 許可待ちフロートを出す
    editor_open = true,  -- true: 自分宛てだけ / "all": すべて / false: 無視
    auto_review = true,  -- BOBA_REVIEW=1 で起動時に :BobaReview
    list_width = 40,
  },
}
```

`setup()` を呼ばなくても、起動時(VimEnter)かコマンドの初回実行時に既定値で初期化される。自動初期化を止めたいときは `vim.g.boba_no_auto_setup = true`。

## 接続先

1. `$BOBA_SOCK`
2. `$BOBA_HOME/share/bobad.sock`(`$BOBA_HOME` があれば)
3. `~/.local/share/boba/bobad.sock`

繋がると `session.` / `review.` / `editor.` のイベントを購読する。切れたら 0.5 秒から最大 15 秒の間隔で再接続する。

## コマンド

| コマンド | すること |
| --- | --- |
| `:BobaReview [slug]` | レビュー面を開く。slug 省略時は `:BobaSessions` の送信先 → `$BOBA_SESSION` → ピッカーの順 |
| `:BobaSend [slug]` | 未送信コメントを 1 通にまとめて送る(`review.flush`)。`$BOBA_SESSION` があれば(boba が起動したレビュー面なら)そのまま `:qa` してエージェント画面に戻る |
| `:BobaSessions` | セッション一覧(状態記号・slug・状態語)。`<CR>` で以降のコマンドの送信先に、`r` でレビュー |
| `:'<,'>BobaSendRange` | 範囲をセッションに送る(`<leader>bs` と同じ) |
| `:'<,'>BobaNew` | 範囲を初期指示に新しいセッションを作る(`<leader>bn` と同じ) |
| `:BobaAnswer y\|n\|always [slug]` | 許可待ちに答える |
| `:BobaFollow [slug]` / `:BobaFollow!` | エージェントが編集したファイルを split に開いて変更箇所へ飛ぶ / 停止 |
| `:BobaFixDiagnostics[!] [slug]` | バッファ(`!` でワークスペース)の LSP 診断を `path:line: severity: message` の一覧にして送る |
| `:BobaRisks [slug]` | 門番エージェント(`[review] gatekeeper`)が挙げた危ない箇所を quickfix に流し込む。読む順序を決める材料 |

## キー

### レビュー面(`:BobaReview`)

左がファイル一覧(`●` 未承認あり / `✓` 全 hunk 承認済み、`N hunks M✓`)、右が選択中ファイルの差分。承認済みの hunk は既定で隠れる。hunk ヘッダの横に「指示 #n: …」(その hunk を生んだプロンプト)、未送信コメントは仮想行で出る。

| キー | 動作 |
| --- | --- |
| `]h` / `[h` | 次 / 前の hunk(ファイル端では隣のファイルへ) |
| `a` | hunk を承認(承認済みなら取り消し) |
| `x` | 差し戻し(理由を入力、空でも可) |
| `c` | カーソル行にコメント |
| `?` | エージェントに意図を聞く。返事はフロートに出る |
| `A` | 承認済み hunk の表示切替 |
| `S` | コメントを送る(`:BobaSend`) |
| `o` | 実ファイルをその行で開く |
| `R` | 再取得 |
| `h` | ファイル一覧へ |
| `q` | 閉じる |

ファイル一覧では `j` `k` で差分が切り替わり、`<CR>` / `l` で差分に移る。

### 範囲送信(visual モード)

| キー | 動作 |
| --- | --- |
| `<leader>bs` | セッションを選び、フロートに指示を打つ。`path:L40-72` + コード + 指示が送られる(path はそのセッションの worktree からの相対、外なら絶対) |
| `<leader>bn` | ブランチ名と指示を聞き、選択を初期指示に新しいセッションを作る |

入力フロートは `<CR>` で送信、`<C-j>` で改行、ノーマルモードの `<Esc>` / `q` で取消。

### 許可待ちフロート

エージェントが許可待ち(`session.waiting` / `wait_kind = "permission"`)になると右下にフロートが出てフォーカスが移る。

| キー | 動作 |
| --- | --- |
| `y` | 許可(allow) |
| `n` | 拒否(deny) |
| `A` | 常に許可(always) |
| `g` | そのセッションを送信先にして、boba で開くよう案内 |
| `<Esc>` / `q` | 閉じて元のウィンドウに戻る |

### エージェントからのジャンプ

エージェントが MCP の `open_in_editor(path, line, note)` を呼ぶと、その行に飛んでメモをフロートに出す。`$BOBA_SESSION` がある nvim はその slug 宛てだけ、それ以外は cwd 配下のパスだけを扱う(`editor_open = "all"` ですべて)。

## 起動時の自動レビュー

boba の TUI で `r` を押すと、worktree を cwd に `BOBA_SESSION=<slug> BOBA_REVIEW=1` で nvim が起動し、自動で `:BobaReview` が開く。`S` で送ると nvim が閉じてエージェント画面に戻る。

## ハイライト

`BobaPrimary` `BobaSecondary` `BobaWarn` `BobaOk` `BobaErr` `BobaDim` `BobaBorder` `BobaSelect` を docs/ui-guidelines.md のカラートークンで定義する(`default = true` なので上書きできる)。派生グループ(`BobaDiffAdd` `BobaDiffDelete` `BobaHunkHeader` `BobaPromptNote` `BobaComment` `BobaFloatBorder` など)はこれらにリンクしている。

## テスト

```sh
nvim/test/run.sh
```

偽デーモン(`test/fake_daemon.py`)を立て、ヘッドレス nvim でスモークテスト(`test/smoke.lua`)と、デーモン後起動からの再接続 + 自動レビュー(`test/auto_review.lua`)を走らせる。ソケットの置き場は `$BOBA_TEST_TMP`(なければ `$TMPDIR`)。
