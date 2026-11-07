# boba 設計書

Nov 7, 2026 · @Yuki Sakamoto

## 概要

boba は、Orca の開発体験(複数エージェントの並列運用・worktree 分離・ブラウザ連携・差分レビュー)を、Ghostty の中で完結するターミナルツールとして作り直す個人開発プロジェクト。普段の Claude Code の使い方は一切変えず、その周辺作業(切り替え・待ち・worktree・ブラウザ・レビュー)だけを引き受ける。

**作る理由**

- Orca は良かったが、ターミナルから離れたくない
- cmux は合わなかった
- 既存の TUI(claude-tmux、cctl、ccmanager 等)はセッション管理で止まっており、ブラウザ連携とレビューまで一体化したものがない

**スコープ**

- 第一対象は Claude Code。Codex や OpenCode など他の CLI エージェントも同じ仕組み(pty + hooks)で扱う
- 利用者は当面自分一人。対象リポジトリに痕跡を残さない
- worktree 作成 → 実装 → レビュー → 取り込み、までの一連のワークフローを焼き込んだ「意見のあるツール」にする

**非目標**

- GUI ターミナルの新規開発(ロードマップ最終フェーズに任意項目として残すのみ)
- エージェント自体の賢さ(タスク分割、自動オーケストレーション)には踏み込まない
- チーム配布・ストア公開は考えない(設計上は後から足せる形にはしておく)

## 設計原則

賢さはすべてデーモンに置き、クライアントは表示と入力だけを持つ。この一点から他の原則が導かれる。

1. **デーモンに賢さ、クライアントは薄く** — worktree・セッション・状態・レビュー記録の持ち主は bobad のみ。TUI・Chrome 拡張・Neovim プラグインは同じ API を叩く表示層で、機能追加はデーモン側の一箇所で済む
2. **ただのシェル** — パッケージマネージャやフレームワークの知識を実行時に持たない。設定はそのリポジトリで普段打つコマンドの列で、boba は変数を埋めて順に実行するだけ。自動検出は初期値の下書きにしか使わない
3. **リポジトリを汚さない** — 設定・状態・一時ファイル・スナップショットはすべてリポジトリ外か、git のローカル専用領域(`refs/boba/`、`.git/info/exclude`)に置く。残る痕跡は worktree という git 標準のものだけ
4. **Claude Code の使い方を変えない** — エージェントは pty の中で素のまま動かす。boba からの入力はすべてキーボード入力と区別がつかない形で pty に書き込む。状態は画面を読むのではなく hooks から受け取る
5. **段階的に差し替えられる** — ターミナル層は tmux エンジンから始めて libghostty-vt の自前 mux に差し替える。デーモンとクライアントの境界を守る限り、どの層も差し替えで機能が失われない
6. **レビューを中心に置く** — エージェントが増えるほどボトルネックは人間のレビューになる。「前回レビューからの差分だけ読む」を成立させるために、レビュー地点と hunk 単位の承認をデーモンが記録する

## 全体構成

&#91;embedded content: 全体構成 · bobad と 4 つのクライアント\]

4 つのクライアントはすべて同じコマンド群を叩く表示層で、エージェントは worktree ごとの pty の中で素のまま動く。bobad が pty への書き込みと出力の受け取りを一手に担い、Claude Code の hooks は `boba event` を通じて bobad に状態を知らせる。

**通信**

- TUI・CLI・Neovim → bobad: `~/.local/share/boba/bobad.sock` 上の JSON Lines。コマンド(`session.send` など)とイベント(`session.waiting` など)の双方向
- Chrome 拡張 → bobad: `127.0.0.1:<port>` への HTTP POST。トークン必須、一方向
- bobad → エージェント: pty への書き込み。エージェント → bobad: pty の出力と hooks
- bobad → nvim(逆方向): bobad が MCP サーバを兼ね、エージェントの `open_in_editor` 呼び出しを nvim へのイベントに変える

## セッションと状態モデル

状態は画面を読んで推測せず、Claude Code の hooks から受け取る。「待ち」は許可待ちと質問を区別し、「完了」の後にレビューの状態を置く。

&#91;embedded content: セッションの状態遷移 · 8 状態\]

**hooks と状態の対応**(`~/.claude/settings.json` に書き、すべて `boba event` を呼ぶ。hooks は stdin の JSON で session\_id と cwd を渡すので、cwd → worktree → セッションの解決ができる)

| 信号 | 状態・処理 |
| --- | --- |
| SessionStart | running。session\_id を保存して `--resume` に使う |
| UserPromptSubmit | running。`refs/boba/<slug>/prompt-<n>` にスナップショット |
| Notification | waiting。許可待ちか質問かを入力 JSON から判別(要確認) |
| Stop | done。`test` を自動実行して結果を左ペインに |
| PreToolUse(任意) | 直近のツール名を左ペインに表示(「今何をしているか」) |
| OSC 9 / 99 / 777、タイトル変更 | hooks のないエージェント用のフォールバック。VT エンジンがイベントとして出す |
| 仮想画面の `[y/n]` 検知 | 最終フォールバック |

**左ペインの 1 行** — セッション名、状態、待ち時間、diffstat、テスト結果、ポート。例: `● feat/csv  wait 2m  +120 -8  ✓test  :3003`

**衝突予報** — 並行するセッション同士が同じファイルを触っていると、左ペインの行に `⚠2`(相手の数)が出る。重なりだけなら warn、コミット済みの内容が `git merge-tree` で衝突するなら err。触っているファイルは base からの変更(コミット済み + 未コミット)と許可待ちの Edit/Write の対象で、bobad が tickLoop で 10 秒ごとに計算する。許可モーダルには「also changed in <slug>」、Changes パネルには各ファイルの相手、land の確認には衝突する相手とファイルが出る。

**Attention queue** — waiting のセッションを待ち時間順に並べた行列。`]a` で次の待ちへ飛び、答えて `[a` で元の場所に戻る。複数待っていれば順に巡る。

**未読ライン** — セッションから離れた時点の出力オフセットを記録し、戻ったときに「ここから先が新しい」線を引く。

**永続性** — セッションはデーモン側で生きているので、ターミナルを閉じても死なない。デーモン自体を再起動した場合は state.db の session\_id から `claude --resume` で復元する(プロセスそのものは復元しない)。

## worktree フロー

`boba new feat/x` 一発で「worktree 作成 → 無視ファイルの持ち込み → setup コマンド → ポート割当 → セッション起動」が走る。boba が知っているのはこの 5 歩だけで、中身はリポジトリごとの設定が決める。

**コマンド**

| コマンド | すること |
| --- | --- |
| `boba new <branch> [--from gh#123]` | `~/worktrees/<repo>/<slug>` に worktree を作り、setup を走らせてセッション起動。issue 指定時は本文を初期プロンプトに |
| `boba open <worktree>` | 既存 worktree(boba 製でなくてもよい)でセッションを起動。過去の Claude Code セッションがあれば `--resume` か新規かを選ぶ |
| `boba adopt` | 別タブで手動起動しているエージェントを boba 管理下に入れる(tmux エンジン時は既存 tmux セッションも拾う) |
| `boba land` | rebase → test → `gh pr create`。マージ検出後に teardown を走らせて worktree を削除 |
| `boba rm <slug>` | teardown → worktree 削除。`~/worktrees` 配下以外は消さない |

**設定ファイル** `~/.config/boba/repos/<repo-id>.toml`(repo-id は origin URL の正規化、なければパス)

```toml
[worktree]
copy  = [".env*", ".envrc", "config/*.local.yml"]
clone = ["node_modules"]          # APFS クローン(cp -c)

[hooks]
setup    = ["mise install", "pnpm install --offline", "createdb app_{{slug}}"]
dev      = "pnpm dev --port {{port}}"
test     = "pnpm test"
teardown = ["dropdb app_{{slug}}"]

[env]
PORT = "{{port}}"
DATABASE_URL = "postgres:///app_{{slug}}"
COMPOSE_PROJECT_NAME = "{{slug}}"
```

変数は `{{slug}}` `{{branch}}` `{{port}}` `{{path}}` `{{repo}}` の 5 つ。setup は順に実行し、失敗したら止めて左ペインに赤で出す。届かないものは `setup = ["./scripts/boba-setup.sh"]` と逃げられる。

**自動検出(`boba init`)** はこのファイルの下書きを作るだけで、実行時には参照しない。

- lockfile → パッケージマネージャ(pnpm-lock.yaml / yarn.lock / package-lock.json / bun.lock / uv.lock / poetry.lock / Cargo.toml / go.mod / Gemfile)
- `.tool-versions` `mise.toml` `.nvmrc` → `mise install`、`.envrc` → `direnv allow`
- `git ls-files --others --ignored --exclude-standard` で実在する無視ファイルを列挙し、node\_modules や dist などの定番を除いた候補を見せて選んでもらう → `copy` に入る

**ポート台帳** — worktree ごとに決定的なポートを割り、デーモンの状態 DB に記録する。dev server の衝突を消すだけでなく、ブラウザのタブ(URL のポート)→ worktree → セッションを機械的に結ぶ鍵になる。

**重い依存の扱い**

- node\_modules: pnpm はストア共有で元々速い。npm / yarn classic は元 worktree の node\_modules を APFS クローンで持ってきてから `install --prefer-offline` で差分だけ直す
- DB: worktree ごとに作って teardown で消す。マイグレーションも setup に書く
- docker compose: `COMPOSE_PROJECT_NAME={{slug}}` でコンテナ名を分ける。公開ポートは compose 側が環境変数参照になっている必要があり、ここだけはリポジトリ側の協力が要る

**プロンプト単位スナップショット** — UserPromptSubmit hook で作業ツリーを `refs/boba/<slug>/prompt-<n>` に記録する。プロンプト単位の undo と、レビュー時の「この hunk はどの指示から生まれたか」の対応付けに使う。

## ターミナル層

boba は Ghostty の中で動く TUI であり、Ghostty を置き換えない。フォント・配色・キーバインドは Ghostty のものがそのまま効き、boba は「エージェント用のタブ 1 枚」として開く。

**仕組み** — エージェントは pty の中で起動し、自分が普通のターミナルに繋がっていると思っている。

```
キー入力:  Ghostty → boba → pty → claude
出力:    claude → pty → boba(VT エンジンで仮想画面を組み立て)
            → 右ペインの矩形に描き直し → Ghostty
```

右ペインのサイズ変更は pty に伝える(TIOCSWINSZ)ので、claude は自分でレイアウトし直す。boba からの文字注入(要素情報の貼り付け、y/n の回答)は pty への書き込みで、キーボード入力と区別がつかない。複数行は bracketed paste で囲む。

**2 段階で作る**

| 段階 | エンジン | 利点 | 欠点 |
| --- | --- | --- | --- |
| 1 | tmux control mode(`tmux -CC`) | pty 管理・描画・永続化を tmux が全部やる。左ペインと機能に集中できる | 見た目とペイン操作が tmux の制約を受ける |
| 2 | libghostty-vt を使った自前 mux | 描画とレイアウトを完全に握れる。ベル・タイトル変更などのイベントを直接取れる | VT 周りの実装とパススルーを自分で持つ |

デーモンとクライアントの境界を守れば、段階 1 で作った機能は段階 2 でそのまま残る。

**libghostty の位置づけ** — 使うのは libghostty-vt(シーケンス解析と端末状態に特化したゼロ依存の部品)。描画と窓を含むフルの libghostty は Mac 中心で API も未安定なので使わない。libghostty-vt は 2026 年春時点で Zig モジュールはあり C API が整備中のため、段階 2 に入る前に C API の安定度を再確認する。

**入れ子の税** — Ghostty → boba → claude の二段になるため、boba が通さない機能は死ぬ。少なくとも次は「中から来たら外にそのまま流す」パススルーを実装する。

- OSC 52(クリップボード書き込み)
- OSC 8(クリック可能リンク。boba がマウスを握るため Ghostty はクリックを見られない。エージェント欄のリンクと素の URL は boba がクリック位置から解決して `open` で開く)
- Kitty graphics(画像表示)
- マウスイベント(右ペインの座標に変換して転送)
- Kitty keyboard protocol(修飾キーの正確な伝達)

## ブラウザ拡張(Chrome MV3)

普段の Chrome をフラグなしで使う。起点は Chrome 側に置き、拡張 → デーモンの一方向 HTTP だけで成立させる(要素を選びたい瞬間は必ずブラウザを見ているので、boba 側から選択モードに入れる逆方向の通信は要らない)。

**操作フロー**

1. ショートカット(例 ⌘⇧E)で選択モード。ホバーで要素がハイライトされ、クリックで確定、Esc で中止
2. 確定するとページ上に小さな入力欄が出る。指示を打って Enter(空のまま Enter なら要素情報だけ送る)
3. service worker がスクショを撮ってデーモンに POST
4. デーモンが URL のポートからセッションを解決し、pty に貼る。数秒後に claude が動き始める

**送るペイロード**

| 項目 | 取り方 |
| --- | --- |
| outerHTML | 深さ・長さを制限して取る |
| セレクタと親の構造 | `main > section > Card > button` の形で |
| 計算済みスタイル | margin / padding / font / color / size に絞る |
| 切り抜きスクショ | `captureVisibleTab` を content script の座標でトリミング |
| コンポーネント名 | React: DOM ノードの `__reactFiber$…` から `_debugOwner` を辿る。Vue: `__vueParentComponent`。Svelte: dev 時の `__svelte_meta` |
| ソース位置 | React 18 までは `_debugSource`。React 19 では消えたので、デーモン側でコンポーネント名を `rg` して当てる(リポジトリ無改修) |
| ページ URL | ポート番号がセッション解決に使われる |

**ターミナルへの渡し方** — デーモンはペイロードを `~/.local/share/boba/<repo-id>/picks/0012.md` と `0012.png` に保存し、pty には短い一行だけ貼る: `@/Users/…/picks/0012.md の要素の余白を広げて`。Claude Code は `@` 参照でファイルを読み、画像も Read で見られる。長い HTML を pty に流さないので入力欄が壊れない。

**タブ ↔ セッションの対応** — URL が `localhost:<port>` ならポート台帳で即決。localhost 以外(ステージング等)ならデーモンが TUI の左ペインに「どのセッションに送る?」を出す。

**落とし穴**

- service worker の寿命: MV3 の SW は 30 秒放置で止まる。常時 WebSocket は不安定になるので「その場で HTTP POST、送ったら終わり」に徹する
- セキュリティ: localhost の HTTP には任意の Web ページからも POST が届きうる。デーモンはインストール時に生成したトークンを要求し、Origin が `chrome-extension://` であることも確認する
- console / network の取得: content script からは見えない。まずはページ側に小さなスクリプトを注入して `window.onerror` と `console.error` を拾う。`chrome.debugger` 権限で CDP を使う道もあるが「デバッグ中」の黄色バーが出るので後回し

**エラーの拾い上げ** — dev server の窓(`d` で起動する tmux の dev 窓)の出力と、拡張が送る console error から、bobad がエラーを見つけて「このエラーを送る?」とカードで提案する。気づいてコピペする手間をなくすのが目的で、送るのは人が `s` を押したときだけ。

- 検出は `internal/errcatch` の表(`Rules`)で、1 形式 1 行。ANSI を除いてから、エラーの始まりの行・続きの行(スタックやコードフレーム)・終わりの行を正規表現で判定する。複数行は次の無関係な行か、出力が 0.5 秒止まった時点で 1 件に確定する
- 同じエラーはメッセージ(数字・アドレス・`?t=` を除く)と先頭フレーム(行番号を除く)の指紋でまとめて回数を数える。先頭フレームは worktree 内の最初のフレーム(node_modules やランタイムは飛ばす)
- 送った・無視したものは同じ指紋が再発するまで出さない
- 送るときは出力と、先頭フレームの前後 5 行の抜粋を `~/.local/share/boba/<repo-id>/errors/` の md に書き、pty には `@` 参照の 1 行だけ貼る(ピックと同じ)

**逆方向(エージェントがブラウザを操作)** は自作しない。Chrome DevTools MCP や Playwright MCP を各セッションに繋ぐ。

**実装** — TypeScript。WXT か素の MV3。デベロッパーモードで「パッケージ化されていない拡張機能を読み込む」でストア不要。拡張は数百行に抑え、賢さはデーモン側に置いて拡張の更新頻度を下げる。

## Neovim プラグイン

Lua プラグイン 1 つがデーモンの unix socket に繋がり、レビュー面と日常の小技の両方を担う。boba が起動した nvim も普段の nvim も同じプラグインが同じデーモンに繋がるので、区別はない。

| 場面 | 操作 | 起きること | 優先 |
| --- | --- | --- | --- |
| A. レビュー | TUI で `r` → `:BobaReview` | 左に変更ファイル一覧、右に差分。`]h` `[h` で hunk 移動、`a` 承認、`c` コメント、`x` 差し戻し、`?` 意図を聞く。`:BobaSend` でコメントを 1 通にまとめて pty に送る | 本体 |
| B. 範囲送信 | visual 選択 → `<leader>bs` | セッションをピッカーで選び、フロートに指示を打つ。`path:L40-72` + コード + 指示が送られる。`<leader>bn` はその選択を初期指示に新セッション | 日常 |
| C. 承認 | 自動 | 待ちが発生すると右下にフロート。`y` / `n` が pty にそのまま流れる。`g` でそのセッションに飛ぶ、Esc で無視 | 日常 |
| D. Follow | `:BobaFollow <slug>` | エージェントが保存したファイルが split に順に開き、変更箇所にスクロールする。`!` で停止 | 後回し |
| E. 診断送信 | `:BobaFixDiagnostics[!]` | バッファ(`!` でワークスペース)の LSP 診断を一覧にして送る | 後回し |
| F. 逆方向 | エージェントが呼ぶ | デーモンが MCP サーバを兼ね、`open_in_editor(path, line, note)` で nvim がその行に飛びフロートにメモが出る | 後回し |

**レビュー面の起動** — TUI で `r` を押すと、右ペインの claude 画面が nvim に入れ替わる(claude は裏で生きている)。nvim は worktree を cwd にして `BOBA_SESSION=<slug>` を環境変数で受け取り、プラグインが自動で `:BobaReview` を開く。`:BobaSend` で nvim が閉じて claude 画面に戻る。

**プロトコル** — JSON Lines。デーモン → nvim はイベント(`session.waiting`、`session.done`、`editor.open`)。nvim → デーモンはコマンド(`session.send`、`review.approve`、`review.comment`、`review.flush`)。全部デーモンの共通 API なので、TUI や CLI と同じコマンドを叩いている。

**前提** — Neovim。Vim 9 でも popup と channel で作れるが、Lua が使えない分書きづらい。

## レビュー機能

レビューはこのツールで一番価値が出る場所。エージェントを増やすほどボトルネックは人間のレビューになるので、「読む量を減らす」と「探す側ではなく確認する側に回る」の二点で設計する。

**レビュー待ち行列** — `done` になったセッションが自動で列に入る。`]r` で次へ。待ち時間と diffstat、テスト結果が横に出るので、どれを先に見るかを一瞬で決められる。

**差分の差分** — レビューを終えた地点を `refs/boba/<slug>/review-<n>` に記録する。指摘 → 修正後の再レビューでは main との全差分ではなく、前回レビューからの変更だけを出す。再レビューの手間が一番減るのはこれ。

**hunk 単位の承認** — `a` で承認、`x` で差し戻し、`c` でコメント。承認済み hunk はデーモンが記録し、次回は隐れる。hunk の同一性はファイルパス + 変更後内容のハッシュで取る(行番号がズレても残る)。

**hunk ↔ プロンプトの対応** — プロンプト単位スナップショットがあるので、各 hunk がどのスナップショット間で生まれたかを逆算できる。hunk の横に「指示 #3: CSV の空行をスキップして」を表示し、「なぜこう変えたか」を聞く前に自分で分かる。

**人間が見る前の門番** — Stop hook で `test` を自動実行し、結果を左ペインに出す。さらに別の読み取り専用エージェントに「危ない箇所だけ列挙」させ、quickfix に流し込む。これはレビューの代替ではなく、読む順序を決めるための材料。

**その場で聞く** — hunk 上で `?` → そのセッションのエージェントに「ここの意図は?」を送り、返事をフロートに出す。返事の検出は Stop hook で行い、その間の出力をデーモンが切り出す。

**人の PR にも同じ UI** — `boba review gh#123` で PR ブランチの worktree を作り、同じレビュー画面を開く。コメントの送り先をエージェントの pty ではなく `gh api` 経由の GitHub レビューに切り替えるだけ。チームのレビューと自分のエージェントのレビューが同じ操作体系になる。

**状態遷移** — done → reviewing → (指摘あり) run → done → reviewing → … → (全 hunk 承認) approved → `boba land` → landed。

## データの置き場(リポジトリ非汚染)

リポジトリに残るのは worktree という git 標準の痕跡だけで、`git worktree prune` で跡形なく消える。

| データ | 置き場 | 備考 |
| --- | --- | --- |
| リポジトリごとの設定 | `~/.config/boba/repos/<repo-id>.toml` | repo-id は origin URL の正規化。clone し直しても同じ設定が当たる |
| 言語別の既定値 | `~/.config/boba/defaults/*.toml` | `boba init` の検出ルール。自分で書き換えられる |
| 送ったエラー | `~/.local/share/boba/<repo-id>/errors/<slug>-<n>.md` | `boba rm` で消える |
| 要素情報(picks)・スクショ | `~/.local/share/boba/<repo-id>/picks/` | pty には絶対パスで `@` 参照を貼る。worktree 内に置きたければ `.git/info/exclude` に `.boba/` を追記(コミット対象外、worktree 間で共有) |
| プロンプト単位スナップショット | `refs/boba/<slug>/prompt-<n>` | 通常の push では送られず `git log` にも出ない |
| レビュー地点 | `refs/boba/<slug>/review-<n>` | 同上 |
| hunk 承認・レビューコメント | 状態 DB | hunk はパス + 内容ハッシュで識別 |
| Claude Code の hooks | `~/.claude/settings.json` | ユーザー側に書き、全リポジトリに一括で効く。プロジェクト側 `.claude/` は触らない |
| worktree 本体 | `~/worktrees/<repo>/<slug>` | boba が削除していい領域はこの配下だけ |
| ポート台帳・DB 名・セッション ID・状態 | `~/.local/share/boba/state.db`(SQLite) | デーモンの外部記憶。再起動後の復元に使う |
| デーモンの socket | `~/.local/share/boba/bobad.sock` | TUI・CLI・nvim が繋ぐ |
| 拡張用 HTTP | `127.0.0.1:<port>` + トークン | トークンはインストール時に生成し拡張の storage に入れる |

将来チームに広げる場合は、リポジトリ内 `.boba.toml` を「あれば読む・個人設定が上書き」の順で足す。

## CLI コマンドとキーバインド

CLI は人間とエージェントの両方が叩く。出力は `--json` で機械可読にできる。

| コマンド | すること |
| --- | --- |
| `boba` | TUI を開く(デーモンがなければ起動) |
| `boba init` | 今のリポジトリの設定を検出して下書き |
| `boba new <branch> [--from gh#N] [--agent codex]` | worktree + セッション作成 |
| `boba open <worktree>` / `boba adopt` | 既存を開く / 管理下に入れる |
| `boba ls` | セッション一覧と状態 |
| `boba conflicts` | 衝突予報。同じファイルを触っている open なセッションのペアと、コミット済みの内容が衝突するか(`git merge-tree`)。`--repo <repo-id>` で絞り込み |
| `boba send <slug> <text>` | pty に文字を送る |
| `boba event` | hooks から呼ばれる。stdin の JSON(session\_id, cwd, hook 名)をデーモンに渡す |
| `boba review [<slug> \| gh#N]` | レビュー面を開く |
| `boba land <slug>` / `boba rm <slug>` | 取り込む / 消す |
| `boba dev <slug>` | 設定の `dev` をその worktree のポートで起動 |
| `boba daemon start\|stop\|status` | デーモン操作 |

**TUI キーバインド**(vim 風。すべて設定で変更可)

| キー | 動作 |
| --- | --- |
| `j` `k` / Enter | セッション選択 / 右ペインにフォーカス |
| `n` | 新しい worktree + セッション |
| `]a` `[a` | 次の待ちセッション(PR に動きがあったセッションも含む) / 元の場所に戻る |
| `]r` | 次のレビュー待ち |
| `r` | レビュー面(nvim)を開く |
| `p` | PR ビューを開く(右ペインが PR の状態に切り替わり、左の選択に追従する。Esc で agent に戻る) |
| `b` | その worktree のポートを Chrome で開く |
| `l` | land(確認に、他の open なセッションとの衝突予報を出す) |
| `d` | dev server 起動 / 停止 |
| `x` | セッション終了(worktree は残す) |
| `D` | worktree 削除(確認あり) |
| `/` | セッションの絞り込み |
| `?` | ヘルプ |
| prefix + `h` `l` | 左ペインと右ペインのフォーカス移動(右ペインにフォーカス中はキーがすべて claude に渡る) |

**PR ビュー**(`p`。右ペインの 4 つ目の窓。GitHub への書き込みはしない)

| キー | 動作 |
| --- | --- |
| `j` `k` | 項目を選ぶ(Checks → Reviews → Threads → Comments の順。失敗・未解決が先) |
| Enter | 項目を展開(スレッド全文、レビュー・コメント本文、失敗した check のログ末尾) |
| Space | 選択トグル(`s` でまとめて 1 通にする) |
| `s` | 選んだ項目をプロンプトにしてエージェントの pty に送る(確認あり)。失敗 check はログ末尾付きで「直して」、スレッドは path:line とコメント全文付きで「対応して」。人の PR(`review gh#N`)では無効 |
| `o` | 選んだ項目(なければ PR)をブラウザで開く |
| `R` | 再取得(開いている間は 15 秒ごとに自動更新) |
| Esc / `h` | agent 窓に戻る / Sessions にフォーカス |

PR の状態はデーモンが 60 秒ごとに `gh api graphql` で要約を取り(`Session.pr_status`)、一覧の `#42` に状態アイコンを付ける。CI が失敗した・changes requested になった・未解決コメントが付いた・マージ可能(CLEAN)になったときは注意キュー(`]a`)に入り「•」が付く。PR ビューを開くと既読になる。マージ・クローズはどのセッションでも検出して表示するが、teardown は land 中のセッションだけ。

## 実装ロードマップ

Phase 2(レビュー面)を越えた時点で日常利用に切り替え、その後は使いながら足す。自前 mux は最後で、やらない選択もある。

&#91;embedded content: 実装ロードマップ · 7 フェーズと 6 つの gate\]

各 gate は「次に進む条件」であり、満たさないまま次のフェーズに手を出さない。

**技術選定**(実装言語を Rust とした場合。未決事項参照)

| 部品 | 技術 |
| --- | --- |
| bobad | Rust(tokio, serde)。git は CLI 呼び出しで済ませ libgit2 は使わない。状態は rusqlite |
| TUI | ratatui + crossterm。Phase 0–4 は tmux control mode を子プロセスで持つ |
| VT(Phase 5) | libghostty-vt を C API 経由で FFI |
| Chrome 拡張 | TypeScript, WXT, MV3 |
| Neovim | Lua。`vim.uv` で socket に接続 |
| MCP サーバ(逆方向) | bobad 内に同居。`open_in_editor` と `session.list` を公開 |
| 通知 | ntfy(curl 一発) |
| パッケージング | 当面 `cargo install` と launchd の plist。拡張は unpacked |

**Phase 0 の最初の一歩** — `boba new` を shell script で書いて 1 日使う。worktree 作成と setup の設計が合っているかは、デーモンを書く前に分かる。

## 未決事項とリスク

最大の未決は実装言語で、フェーズ 5(自前 mux)を本気でやるかで決まる。

**未決事項**

| 項目 | 選択肢 | 当面の考え |
| --- | --- | --- |
| 実装言語 | Rust(ratatui + libghostty-vt へ FFI。cmux-linux の先例) / Go(Bubble Tea。tmux エンジン段階が最速だが cgo が重い) / Zig(libghostty に最も近いが TUI エコシステムが薄い) | フェーズ 5 をやるなら Rust。やらないと決めるなら Go |
| 待ちの種別 | Claude Code の Notification hook が「許可待ち」と「idle」を入力 JSON で区別できるか | 要確認。できなければ仮想画面の `[y/n]` 検知を併用 |
| Claude Code 以外の状態検知 | Codex / OpenCode に hooks 相当があるか | なければ OSC と画面検知のフォールバックのみ |
| libghostty-vt C API | フェーズ 5 着手時点の安定度 | 着手前に再確認。不安定なら tmux エンジンを継続 |
| hunk の同一性 | 内容ハッシュは周辺行の変更で別 hunk 扱いになる | 許容して始め、実用で気になったら類似度マッチを足す |
| 拡張の配布 | unpacked のまま / 自己署名 | unpacked のまま |
| キー衝突 | tmux エンジン段階で prefix キーが Ghostty / claude と衝突しないか | prefix を設定可能にしておく |

**リスク**

- 入れ子の税で日常の快適さが落ちる → フェーズ 1(tmux)で体感を確認してから先へ進む
- Claude Code 側の仕樣変更(hooks、`@` 参照、`--resume`)に影響される → hooks と pty 注入を薄いアダプタ層に隔離する
- 機能が積み上がって終わらない → フェーズごとの完了条件を守り、フェーズ 2 が終わった時点で日常利用に切り替える
- worktree の容量(node\_modules の複製) → APFS クローンとストア共有で緩和。`boba rm` とマージ後の自動削除で残骸を残さない
