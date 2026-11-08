# boba → pekoe リネーム指示書

このドキュメントは、ツール名を **boba** から **pekoe** に変える作業の指示書。対象は 2 つ。

- **A. LP**(`site/` 一式。現在はこのリポジトリの外に移してある。リポジトリ内の最新版は `refs/boba/main/prompt-125` の `site/`)
- **B. ツール本体**(このリポジトリ全体)

作業前に `CLAUDE.md` を必ず読むこと。特に「本物のデーモンには触らない」の節。

---

## 0. 経緯と決定事項

### なぜ変えるか
- `boba` は一般名詞すぎて、検索でもパッケージ名でも埋もれる。実際に `boba` という名前の CLI やパッケージが複数ある(@tradeboba/cli のバイナリ `boba`、@boba-cli/*、go-booba など)
- 候補として tapi / tapiq / sago / chewy / tama なども調べたが、衝突や読みにくさがあって見送った

### 決定事項
| 項目 | 決定 |
|---|---|
| ツール名 | **pekoe** |
| 読み | **ペコー**(英語で「ピーコー」と読まれても訂正はしない) |
| 由来 | orange pekoe。紅茶のニュアンス。語源の中国語「白毫」(アモイ方言 pe̍h-hô)もペコーに近い |
| マスコット | **tapi**(変更なし)。各セッションの状態を見守る小さな相棒 |
| 関係 | pekoe = エージェントたちの「家・居場所」、tapi = そこにいる companion |
| キャッチコピー | 下記 |

```
pekoe
A cozy home for your coding agents.
with tapi, your little companion
```

- 衝突チェック: `pekoe` という名前の CLI やパッケージは見つかっていない。近い名前に `peky`(マルチエージェントのダッシュボード)と `pekora-cli`(バイナリ名 `peko`)があるが、綴りが十分違うので問題なしと判断した
- 公式の読みは README と LP の 1 か所にだけ書く: `pekoe — 読みは「ペコー」(PEK-oh)。orange pekoe から。`

### 命名の対応表(本体・LP 共通)
| 旧 | 新 |
|---|---|
| `boba`(CLI) | `pekoe` |
| `bobad`(デーモン) | `pekoed` |
| Go module `boba` | `pekoe` |
| `cmd/boba` | `cmd/pekoe` |
| `BOBA_*` 環境変数 | `PEKOE_*` |
| `~/.config/boba` | `~/.config/pekoe` |
| `~/.local/share/boba`(`state.db`, `bobad.sock`, `bobad.log`, `bobad.pid`, `bobad.lock`) | `~/.local/share/pekoe`(`pekoed.sock` など) |
| tmux ソケット名 `boba`(`tmux -L boba`) | `pekoe` |
| `refs/boba/<slug>/…` | `refs/pekoe/<slug>/…` |
| launchd `dev.boba.bobad` | `dev.pekoe.pekoed` |
| MCP サーバー名 `boba`(`claude mcp add boba -- boba mcp`) | `pekoe` |
| hooks のコマンド `boba event` | `pekoe event` |
| nvim の `lua/boba`、`plugin/boba.lua`、`:Boba*` コマンド、`Boba*` ハイライトグループ | `pekoe` / `:Pekoe*` / `Pekoe*` |
| スナップショットのコミット author `boba <boba@localhost>` | `pekoe <pekoe@localhost>` |
| TUI のモード表示 `BOBA` | `PEKOE` |
| `~/worktrees/` | **変更なし** |
| tapi まわり(`internal/mascot`、`BOBA_TAPI_DEMO` → `PEKOE_TAPI_DEMO` 以外) | **変更なし** |

---

## A. LP(site/)

1. **名前の差し替え**
   - `index.html`: `<title>`、ヘッダーのワードマーク(`<span class="w">boba</span>` → `pekoe`。タピオカの粒 `.pearl` と下線グラデーションはそのまま)、`.path` 表記、パネル名 `[1] ◆ boba`、ナビ、`data-mode="BOBA"` とステータスバーの `#smode`、`?` モーダルの行、kicker、TUI モックのタイトルバー、コマンド例(`boba new` / `ls` / `land` / `hooks install` / `init` / `mcp`)、install 節(`~/.local/bin/pekoe (+ pekoed)`)、図の `bobad` と `hooks → boba event`
   - `copy/ja.js` / `copy/en.js`: 本文中の `boba` / `bobad` / `refs/boba/` / `tmux -L boba` をすべて置き換える
   - `app.js`: `window.BOBA_COPY` → `window.PEKOE_COPY`、localStorage キー `boba-lang` → `pekoe-lang`
   - `style.css` / `README.md` / `build.py` のコメントと出力名(`dist/boba-lp.html` → `dist/pekoe-lp.html`)
   - アセット名 `assets/boba-logo.webp` → `assets/pekoe-logo.webp`(参照も更新)
2. **キャッチコピーをヒーローに入れる**
   - [1] の冒頭(h1 の上)に、ワードマーク `pekoe`・`A cozy home for your coding agents.`・`with tapi, your little companion` の 3 行をブランド行として置く。英語のまま ja / en で共通にする
   - 既存の h1「エージェントは何体でも。あなたは、ターミナルにいるだけ。」とリードは残し、ブランド行の下に置く
   - `meta.title` はキャッチコピーに合わせる(ja: `pekoe — A cozy home for your coding agents.`、en: 同じ)
   - ブランド行の近くかフッターに読みを 1 か所だけ添える: `pekoe /ペコー/ — from orange pekoe`
3. **トーン**: [3] Meet tapi の導入に「pekoe が家、tapi はそこにいる相棒」という関係を一文で加える(ja はですます調、見出しは体言止めのまま)
4. **変えないもの**: 配色トークン、背景のラジアルグラデーション、TUI 風のレイアウト、キー操作、tapi のスプライト・ステッカー画像、日本語本文のですます調
5. **確認**: `grep -rni boba` の結果が 0 件になること(dist 除く)。`python3 build.py` で dist を再生成し、ブラウザで ja / en 両方を開いて表示が崩れていないこと、`l` キーで言語が切り替わること、localStorage の新しいキーで言語が保持されることを確かめる

---

## B. ツール本体

### 進め方
- ブランチ `chore/rename-pekoe`。PR は次の 3 つに分ける
  1. コード・ビルド・テストのリネームと移行処理
  2. nvim プラグイン・Chrome 拡張・Web UI
  3. ドキュメント(README / docs / CLAUDE.md)
- 動作確認はすべて `scripts/sandbox.sh`(リネーム後の環境変数名に更新したもの)で行う。**ユーザーの本物のデーモン(`tmux -L boba`、`~/.local/share/boba/`)には絶対に触らない。** 切り替えはユーザーが手で行う(下の「切り替え手順」)

### 1. コード
- `go.mod` の module を `pekoe` に。全 import の `boba/internal/...` → `pekoe/internal/...`
- `cmd/boba` → `cmd/pekoe`。`Makefile` の `BIN`、`bobad` シンボリックリンク、`share/boba` → `share/pekoe`、install 後のメッセージ
- `internal/paths`: ディレクトリ・ファイル名・既定の tmux ソケット名・環境変数を対応表どおりに変える
- 環境変数はすべて `PEKOE_*` に(`HOME`, `SOCK`, `SESSION`, `COLOR`, `TMUX_SOCKET`, `WORKTREES`, `REVIEW`, `BIN`, `CHROME`, `ICONS`, `PORT`, `SQUARE`, `KITTY`, `GRAPHICS`, `NVIM_RTP`, `TAPI_DEMO`, `TEST`, `TEST_TMP`)。エージェントの pty に渡す `BOBA_SESSION` も `PEKOE_SESSION` に
- `refs/boba/` → `refs/pekoe/`(`internal/gitx` ほか)
- MCP の `serverInfo.name`、ツール説明文、`bobad is not running` などのメッセージ
- TUI の文言・モード表示、tmux のウィンドウ名やオプション名に入っている `boba-*`(`boba-sb` など)
- launchd: `contrib/launchd/dev.pekoe.pekoed.plist` を作り、Makefile の `install-launchd` / `uninstall-launchd` を新しいラベルにする。`uninstall-launchd` は旧ラベル `dev.boba.bobad` も見つかれば外す
- 内部識別子の `Boba*` / `boba*` も残さない(`internal/theme` など)

### 2. 移行処理(既存データを捨てないため)
- `pekoe migrate`(新サブコマンド)を作る。やること:
  - デーモンが動いていたら中止してメッセージを出す(旧 `bobad.sock` / 新 `pekoed.sock` の両方を確認)
  - `~/.config/boba` → `~/.config/pekoe`、`~/.local/share/boba` → `~/.local/share/pekoe` を移動(移動先がすでにあれば中止)。中のファイル名 `bobad.*` も改名
  - `state.db` に旧パスや旧 ref 名が入っていれば書き換える
  - 登録済みの各リポジトリで `refs/boba/*` を `refs/pekoe/*` にコピー(`git update-ref`)。旧 ref の削除は `--prune-old` を付けたときだけ
  - Claude Code の `~/.claude/settings.json` から `boba event` の hooks を取り除き、`pekoe event` を入れる(`pekoe hooks install` でも旧 hooks を検出して置き換えるようにする)
  - 最後に、ユーザーが手でやる残作業(MCP の付け替え、launchd、tmux)を表示する
- `--dry-run` で何をするかだけ表示できること
- 古い名前の環境変数や旧パスを読み続ける互換処理は**入れない**(個人用ツールなので、migrate で一度に切り替える)

### 3. nvim / Chrome 拡張 / Web UI
- nvim: `lua/boba` → `lua/pekoe`、`plugin/boba.lua` → `plugin/pekoe.lua`、`require("boba…")`、`:Boba*` コマンド、`Boba*` ハイライトグループ、ソケットの既定パス、`vim.env.BOBA_SESSION`、テスト(`nvim/test/*`、`fake_daemon.py`)
- Chrome 拡張: `manifest.json` の name / description / default_title、各 JS の文言とメッセージ識別子、`internal/daemon/chrome.go` 側の対応
- Web UI(`internal/daemon/webui/*`): タイトル、`manifest.webmanifest` の name、文言

### 4. ドキュメント
- `README.md`: 名前・コマンド例・パスをすべて更新。冒頭に読みと由来、キャッチコピーを入れる
  ```
  # pekoe
  A cozy home for your coding agents. — with tapi, your little companion

  pekoe(ペコー)— orange pekoe から。
  ```
- `docs/*.md`(design, api, web, verification, mascot, ui-guidelines)、`nvim/README.md`、`extension/README.md`、`CLAUDE.md`(隔離環境の環境変数名・`tmux -L pekoe`・パス)
- `docs/mascot.md` に pekoe と tapi の関係(家と、そこにいる相棒)を一段落加える
- `docs/mock.html`(13MB)は文字列置換だけで済むなら置換し、済まなければ触らない

### 5. 完了条件
- `make test`(既定 + `-tags web` + nvim ヘッドレス)が通る
- `git grep -ni boba` の残りが、移行処理と CHANGELOG/移行手順の記述など**意図したものだけ**になっている(残したものは PR 本文に列挙する)
- sandbox で `pekoe init` → `pekoe new` → TUI 表示 → `pekoe land` まで一通り動く
- sandbox で旧レイアウト(`~/.local/share/boba` 相当と `refs/boba/*`)を作り、`pekoe migrate --dry-run` と `pekoe migrate` が期待どおりに動く

### 6. 切り替え手順(ユーザーが手で行う。PR 本文に載せる)
tmux のソケット名が変わるため、旧デーモン配下で動いているエージェントは新デーモンから見えなくなる。**実行中のエージェントがないとき**に行う。
1. 旧デーモン配下のエージェントを終わらせ、`boba daemon stop`
2. `make install`(`pekoe` / `pekoed` が入る)。旧 `~/.local/bin/boba` と `bobad` は手で消す
3. `pekoe migrate`
4. `claude mcp remove boba && claude mcp add pekoe -- pekoe mcp`
5. launchd を使っていれば `make uninstall-launchd && make install-launchd`
6. Neovim の設定で `require("boba")` を使っていれば `require("pekoe")` に、Chrome 拡張を再読み込み
7. 不要になったら `tmux -L boba kill-server`

### 7. スコープ外(ユーザーがやる)
- GitHub リポジトリ名 `sakamosu/boba` → `pekoe` の変更と、ローカルの `~/repos/boba` の改名
- LP のロゴ画像(ステッカー)に `boba` の文字が入っている場合の描き直し。エージェントはファイル名の変更だけ行い、画像に文字が入っているかどうかを PR 本文で報告する
