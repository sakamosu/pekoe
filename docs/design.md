# pekoe 設計書

Nov 7, 2026 · @Yuki Sakamoto

## 概要

pekoe は、Orca の開発体験(複数エージェントの並列運用・worktree 分離・ブラウザ連携・差分レビュー)を、Ghostty の中で完結するターミナルツールとして作り直す個人開発プロジェクト。普段の Claude Code の使い方は一切変えず、その周辺作業(切り替え・待ち・worktree・ブラウザ・レビュー)だけを引き受ける。

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

1. **デーモンに賢さ、クライアントは薄く** — worktree・セッション・状態・レビュー記録の持ち主は pekoed のみ。TUI・Chrome 拡張・Neovim プラグインは同じ API を叩く表示層で、機能追加はデーモン側の一箇所で済む
2. **ただのシェル** — パッケージマネージャやフレームワークの知識を実行時に持たない。設定はそのリポジトリで普段打つコマンドの列で、pekoe は変数を埋めて順に実行するだけ。自動検出は初期値の下書きにしか使わない
3. **リポジトリを汚さない** — 設定・状態・一時ファイル・スナップショットはすべてリポジトリ外か、git のローカル専用領域(`refs/pekoe/`、`.git/info/exclude`)に置く。残る痕跡は worktree という git 標準のものだけ
4. **Claude Code の使い方を変えない** — エージェントは pty の中で素のまま動かす。pekoe からの入力はすべてキーボード入力と区別がつかない形で pty に書き込む。状態は画面を読むのではなく hooks から受け取る
5. **段階的に差し替えられる** — ターミナル層は tmux エンジンから始めて libghostty-vt の自前 mux に差し替える。デーモンとクライアントの境界を守る限り、どの層も差し替えで機能が失われない
6. **レビューを中心に置く** — エージェントが増えるほどボトルネックは人間のレビューになる。「前回レビューからの差分だけ読む」を成立させるために、レビュー地点と hunk 単位の承認をデーモンが記録する

## 全体構成

&#91;embedded content: 全体構成 · pekoed と 4 つのクライアント\]

4 つのクライアントはすべて同じコマンド群を叩く表示層で、エージェントは worktree ごとの pty の中で素のまま動く。pekoed が pty への書き込みと出力の受け取りを一手に担い、Claude Code の hooks は `pekoe event` を通じて pekoed に状態を知らせる。

**通信**

- TUI・CLI・Neovim → pekoed: `~/.local/share/pekoe/pekoed.sock` 上の JSON Lines。コマンド(`session.send` など)とイベント(`session.waiting` など)の双方向
- Chrome 拡張 → pekoed: `127.0.0.1:<port>` への HTTP POST。トークン必須、一方向
- iPhone(Web UI)→ pekoed: pekoed に埋め込んだ tsnet ノードの HTTPS(tailnet の中だけ)。認証は接続ごとの Tailscale WhoIs。既定は無効([docs/web.md](web.md))
- pekoed → エージェント: pty への書き込み。エージェント → pekoed: pty の出力と hooks
- pekoed → nvim(逆方向): pekoed が MCP サーバを兼ね、エージェントの `open_in_editor` 呼び出しを nvim へのイベントに変える

## セッションと状態モデル

状態は画面を読んで推測せず、Claude Code の hooks から受け取る。「待ち」は許可待ちと質問を区別し、「完了」の後にレビューの状態を置く。

&#91;embedded content: セッションの状態遷移 · 8 状態\]

**hooks と状態の対応**(`~/.claude/settings.json` に書き、すべて `pekoe event` を呼ぶ。hooks は stdin の JSON で session\_id と cwd を渡すので、cwd → worktree → セッションの解決ができる)

| 信号 | 状態・処理 |
| --- | --- |
| SessionStart | running。session\_id を保存して `--resume` に使う |
| UserPromptSubmit | running。`refs/pekoe/<slug>/prompt-<n>` にスナップショット |
| Notification | waiting。許可待ちか質問かを入力 JSON から判別(要確認) |
| Stop | done。`test` を自動実行して結果を左ペインに |
| PreToolUse(任意) | 直近のツール名を左ペインに表示(「今何をしているか」) |
| OSC 9 / 99 / 777、タイトル変更 | hooks のないエージェント用のフォールバック。VT エンジンがイベントとして出す |
| 仮想画面の `[y/n]` 検知 | 最終フォールバック |

**左ペインの 1 行** — セッション名、状態、待ち時間、diffstat、テスト結果、ポート。例: `● feat/csv  wait 2m  +120 -8  ✓test  :3003`

**衝突予報** — 並行するセッション同士が同じファイルを触っていると、左ペインの行に `⚠2`(相手の数)が出る。重なりだけなら warn、コミット済みの内容が `git merge-tree` で衝突するなら err。触っているファイルは base からの変更(コミット済み + 未コミット)と許可待ちの Edit/Write の対象で、pekoed が tickLoop で 10 秒ごとに計算する。許可モーダルには「also changed in <slug>」、Changes パネルには各ファイルの相手、land の確認には衝突する相手とファイルが出る。

**Attention queue** — waiting のセッションを待ち時間順に並べた行列。`]a` で次の待ちへ飛び、答えて `[a` で元の場所に戻る。複数待っていれば順に巡る。

**未読ライン** — セッションから離れた時点の出力オフセットを記録し、戻ったときに「ここから先が新しい」線を引く。

**永続性** — エージェントは pekoed 専用の tmux サーバー(`tmux -L pekoe`)の中で動くので、ターミナルを閉じても、`pekoe daemon stop` やデーモンの再起動でも止まらない。次に起動した pekoed は state.db からセッションを読み戻し、生きている tmux のウィンドウにそのままつなぎ直す(会話も実行中の処理も途切れない)。起動時には各セッションの repo-id をリポジトリの今の origin から引き直し、作成後に origin が付いたリポジトリのセッションも今の ID に揃える(ポート番号は変わらない)。tmux サーバーごと止めるのは `pekoe daemon stop --kill-agents` だけで、これは使い捨ての隔離環境の片付け用。`claude --resume` が使われるのは tmux サーバーごと消えていたとき(OS の再起動、`tmux -L pekoe kill-server`、`--kill-agents` など)だけで、`x` で止めたもの以外を state.db の session\_id で起動し直す(会話は戻るが、プロセスとその時点で動いていたツールは戻らない)。その id の会話がまだ保存されていなければ `--continue`、それもなければ新しく起動する。worktree が見つからないセッションは failed として残り、`pekoe rm` で消せる。

**デーモンは 1 つだけ** — pekoed は起動時、state.db を開く前に `~/.local/share/pekoe/pekoed.lock` に `flock` をかけ、動いている間ずっと持つ。取れなければ、持ち主がソケットに応答していればすぐに「already running」で終わり、応答していなければ(起動中か終了処理中)最大 5 秒待ってから取り直す。複数のクライアントが同時に自動起動しても、残るのは 1 つだけ。終了時はソケットと pid ファイルを、まだ自分が作ったもの(ソケットは `Listen` 直後の inode、pid ファイルは中身の pid)のときだけ消し、ロックはその後で最後に放す。`pekoe daemon stop` はプロセスが終わるまで(最大 10 秒)待ってから戻り、時間内に終わらなければエラーにする。

## worktree フロー

`pekoe new feat/x` 一発で「worktree 作成 → 無視ファイルの持ち込み → setup コマンド → ポート割当 → セッション起動」が走る。pekoe が知っているのはこの 5 歩だけで、中身はリポジトリごとの設定が決める。

**コマンド**

| コマンド | すること |
| --- | --- |
| `pekoe new <branch> [--repo R] [--from gh#123]` | `~/worktrees/<repo>/<slug>` に worktree を作り、setup を走らせてセッション起動。issue 指定時は本文を初期プロンプトに。対象は今いるリポジトリ(worktree の中ならそのメイン)か `--repo` |
| `pekoe open <worktree>` | 既存 worktree(pekoe 製でなくてもよい)でセッションを起動。過去の Claude Code セッションがあれば `--resume` か新規かを選ぶ |
| `pekoe adopt` | 別タブで手動起動しているエージェントを pekoe 管理下に入れる(tmux エンジン時は既存 tmux セッションも拾う) |
| `pekoe land` | rebase → test → `gh pr create`。マージ検出後に teardown を走らせて worktree を削除 |
| `pekoe rm <slug>` | teardown → worktree 削除。`~/worktrees` 配下以外は消さない |

**複数リポジトリ** — デーモンは 1 つで全リポジトリのセッションを扱う。CLI の対象リポジトリは今いるディレクトリから決まり、`--repo R` で明示できる。`R` は repo-id・短い名前(`~/worktrees/<repo>` の `<repo>`)・パスのどれでもよく、解決はデーモンの 1 か所(`repo.resolve`)でする。短い名前で引けるのは既知のリポジトリ(セッションがあるもの + 一度 pekoe で解決したもの + `~/.config/pekoe/repos/*.toml` があるもの。`repo.list`)で、同じ名前が複数あれば候補の repo-id を出してエラーにする。

**slug** — slug は全リポジトリで 1 つの名前空間。ブランチ名から作った slug が別リポジトリのセッションと衝突したら `<repo>-<slug>`(`web-feat-x`)にし、それも埋まっていれば `-2` `-3` を付ける。同じリポジトリの同じブランチは同じ slug のまま(「already exists」)。既存セッションの slug は変えない。slug を取るコマンドは `<repo>/<slug>` でも指定でき、`<repo>` は repo-id か短い名前、`<slug>` は本物の slug でもリポジトリ名を前に付ける前の形でもよい(`web/feat-x` → `web-feat-x`)。

**Issue インボックス** — 左パネル [2] のサブタブ Issues に `gh issue list` の結果(既定は自分担当の open)を出し、選んだ issue の本文とコメントをメインパネルで読む。`n` で `fix/`(bug ラベル)か `feat/` + タイトルの kebab-case を提案して `pekoe new --from gh#N` と同じ流れでセッションを作る。セッションは issue 番号を覚えるので(ブランチ名に番号があればそれでも)、行にそのセッションの状態が出て Enter で飛べる。issue → 実装 → PR → CI → マージを pekoe の中で一周するための入口。gh が無い・未認証・GitHub リモートが無いときは 1 行の案内だけを出す。

**設定ファイル** `~/.config/pekoe/repos/<repo-id>.toml`(repo-id は origin URL の正規化、なければパス)

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

変数は `{{slug}}` `{{branch}}` `{{port}}` `{{path}}` `{{repo}}` の 5 つ。setup は順に実行し、失敗したら止めて左ペインに赤で出す。届かないものは `setup = ["./scripts/pekoe-setup.sh"]` と逃げられる。

**自動検出(`pekoe init`)** はこのファイルの下書きを作るだけで、実行時には参照しない。

- lockfile → パッケージマネージャ(pnpm-lock.yaml / yarn.lock / package-lock.json / bun.lock / uv.lock / poetry.lock / Cargo.toml / go.mod / Gemfile)
- `.tool-versions` `mise.toml` `.nvmrc` → `mise install`、`.envrc` → `direnv allow`
- `git ls-files --others --ignored --exclude-standard` で実在する無視ファイルを列挙し、node\_modules や dist などの定番を除いた候補を見せて選んでもらう → `copy` に入る

**ポート台帳** — worktree ごとに決定的なポートを割り、デーモンの状態 DB に記録する。dev server の衝突を消すだけでなく、ブラウザのタブ(URL のポート)→ worktree → セッションを機械的に結ぶ鍵になる。

**重い依存の扱い**

- node\_modules: pnpm はストア共有で元々速い。npm / yarn classic は元 worktree の node\_modules を APFS クローンで持ってきてから `install --prefer-offline` で差分だけ直す
- DB: worktree ごとに作って teardown で消す。マイグレーションも setup に書く
- docker compose: `COMPOSE_PROJECT_NAME={{slug}}` でコンテナ名を分ける。公開ポートは compose 側が環境変数参照になっている必要があり、ここだけはリポジトリ側の協力が要る

**プロンプト単位スナップショット** — UserPromptSubmit hook で作業ツリーを `refs/pekoe/<slug>/prompt-<n>` に記録する。プロンプト単位の undo と、レビュー時の「この hunk はどの指示から生まれたか」の対応付けに使う。

## ターミナル層

pekoe は Ghostty の中で動く TUI であり、Ghostty を置き換えない。フォント・配色・キーバインドは Ghostty のものがそのまま効き、pekoe は「エージェント用のタブ 1 枚」として開く。

**仕組み** — エージェントは pty の中で起動し、自分が普通のターミナルに繋がっていると思っている。

```
キー入力:  Ghostty → pekoe → pty → claude
出力:    claude → pty → pekoe(VT エンジンで仮想画面を組み立て)
            → 右ペインの矩形に描き直し → Ghostty
```

右ペインのサイズ変更は pty に伝える(TIOCSWINSZ)ので、claude は自分でレイアウトし直す。pekoe からの文字注入(要素情報の貼り付け、y/n の回答)は pty への書き込みで、キーボード入力と区別がつかない。複数行は bracketed paste で囲む。

**2 段階で作る**

| 段階 | エンジン | 利点 | 欠点 |
| --- | --- | --- | --- |
| 1 | tmux control mode(`tmux -CC`) | pty 管理・描画・永続化を tmux が全部やる。左ペインと機能に集中できる | 見た目とペイン操作が tmux の制約を受ける |
| 2 | libghostty-vt を使った自前 mux | 描画とレイアウトを完全に握れる。ベル・タイトル変更などのイベントを直接取れる | VT 周りの実装とパススルーを自分で持つ |

デーモンとクライアントの境界を守れば、段階 1 で作った機能は段階 2 でそのまま残る。

**libghostty の位置づけ** — 使うのは libghostty-vt(シーケンス解析と端末状態に特化したゼロ依存の部品)。描画と窓を含むフルの libghostty は Mac 中心で API も未安定なので使わない。libghostty-vt は 2026 年春時点で Zig モジュールはあり C API が整備中のため、段階 2 に入る前に C API の安定度を再確認する。

**入れ子の税** — Ghostty → pekoe → claude の二段になるため、pekoe が通さない機能は死ぬ。少なくとも次は「中から来たら外にそのまま流す」パススルーを実装する。

- OSC 52(クリップボード書き込み)
- OSC 8(クリック可能リンク。pekoe がマウスを握るため Ghostty はクリックを見られない。エージェント欄のリンクと素の URL は pekoe がクリック位置から解決して `open` で開く)
- Kitty graphics(画像表示)
- マウスイベント(右ペインの座標に変換して転送)
- Kitty keyboard protocol(修飾キーの正確な伝達)

## ブラウザ拡張(Chrome MV3)

普段の Chrome をフラグなしで使う。起点は Chrome 側に置き、拡張 → デーモンの一方向 HTTP だけで成立させる(要素を選びたい瞬間は必ずブラウザを見ているので、pekoe 側から選択モードに入れる逆方向の通信は要らない)。

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

**ターミナルへの渡し方** — デーモンはペイロードを `~/.local/share/pekoe/<repo-id>/picks/0012.md` と `0012.png` に保存し、pty には短い一行だけ貼る: `@/Users/…/picks/0012.md の要素の余白を広げて`。Claude Code は `@` 参照でファイルを読み、画像も Read で見られる。長い HTML を pty に流さないので入力欄が壊れない。

**タブ ↔ セッションの対応** — URL が `localhost:<port>` ならポート台帳で即決。localhost 以外(ステージング等)ならデーモンが TUI の左ペインに「どのセッションに送る?」を出す。

**落とし穴**

- service worker の寿命: MV3 の SW は 30 秒放置で止まる。常時 WebSocket は不安定になるので「その場で HTTP POST、送ったら終わり」に徹する
- セキュリティ: localhost の HTTP には任意の Web ページからも POST が届きうる。デーモンはインストール時に生成したトークンを要求し、Origin が `chrome-extension://` であることも確認する
- console / network の取得: content script からは見えない。まずはページ側に小さなスクリプトを注入して `window.onerror` と `console.error` を拾う。`chrome.debugger` 権限で CDP を使う道もあるが「デバッグ中」の黄色バーが出るので後回し

**run 窓(`!`)** — エージェントが「このコマンドを実行してください」と渡してくるものには、確認ダイアログやパスワード入力のようにエージェントのシェルモード(Claude Code の `!`)では答えられないものがある。そのためだけに別のペインを開かずに済むよう、Sessions の `!` でそのセッションの worktree に 1 回だけのコマンド窓を開く。常駐シェルではない。

- 入力欄の初期値はエージェントの今の画面から拾う。最後の `! <cmd>`(`!` の後に空白、または `` `!cmd` ``。`!=` や `!ok`、Claude Code のショートカット一覧の `! for bash mode` は拾わない)→ 画面のいちばん下にある次のどれか: インラインコード(バッククォート、または Claude Code のように色だけで描かれた部分。引数のあるものだけで、1 語のファイル名などは拾わない)/ コードブロックの 1 行(開きの ``` が画面外に出ていても、画面上の ``` の数が奇数なら先頭からブロックの中とみなす)/ `$ ` で始まる行。URL だけのものは拾わないが、URL を引数に取るコマンド(`curl … https://…`)は拾う。色は `internal/termstyle`(Web の画面と同じ SGR パーサー)で読む。拾えなければ空。拾い方はゆるく、外れても編集して Enter で実行、Esc で取りやめ。複数行を貼り付けたときは改行をそのまま残し(入力欄では `⏎` と見せる)、シェルに貼った通りに渡す
- pekoed は tmux の `run` 窓を作り、worktree をカレントに、dev と同じ環境変数(リポジトリ設定の env・`PEKOE_PORT`・`PEKOE_SESSION`)で `$SHELL -c '<cmd>'` を起動する。本物の pty なので、確認やパスワードにそのまま答えられる。窓の先頭にはコマンドを dim で出す。メインパネルは run 窓に切り替わり、フォーカスと入力は run に移る。作成中・landed のセッションや worktree が無いときは起動しない(tmux は `-c` のディレクトリが無いと `$HOME` で起動するため)
- 実行中のコマンドは pekoed が持つ(セッションの `run`、state.db に残る)。見出しの `$ <cmd>` も報告もそこから取るので、TUI を再起動しても、TUI を 2 つ開いていても同じコマンド名になる。`run.start` はセッションをまたいで 1 本ずつ処理し、Enter の 2 度押しで 2 本目が 1 本目を殺して同じコマンドを再実行することはない
- 終わると `exit N · ↵ back to the agent` を出す(終了コードは tmux の `pane_dead_status`。Ctrl-C などシグナルで終わったときは `pane_dead_signal` から `interrupted (SIGINT)`)。Enter で窓を閉じて agent に戻り、`tell the agent?` を 1 行で聞く。答えは `y` / `n`(Esc)だけで、Enter では送らない(コマンドのプロンプトに Enter で答えているうちに終わっても、勝手に送られない)。`y` なら「`<cmd>` を実行しました(exit N)」(シグナルなら「…を実行しましたが、中断されました(SIGINT)」)を `session.send` と同じ経路(貼り付けて Enter)でエージェントに送る。`x` / Esc は聞かずに閉じる。入力中でなくても(Sessions や Agent パネルにフォーカスがあっても)、終わった run 窓が出ている間の `x` はセッションの終了ではなく run 窓を閉じる
- run は 1 セッションに 1 つ。動いている(または終わって閉じていない)間の `!` は、新しく起動せずにその窓を出す。`v` で agent に戻っても run は裏で動き続ける

**エラーの拾い上げ** — dev server の窓(`d` で起動する tmux の dev 窓)の出力と、拡張が送る console error から、pekoed がエラーを見つけて「このエラーを送る?」とカードで提案する。気づいてコピペする手間をなくすのが目的で、送るのは人が `s` を押したときだけ。

- 検出は `internal/errcatch` の表(`Rules`)で、1 形式 1 行。ANSI を除いてから、エラーの始まりの行・続きの行(スタックやコードフレーム)・終わりの行を正規表現で判定する。複数行は次の無関係な行か、出力が 0.5 秒止まった時点で 1 件に確定する
- 同じエラーはメッセージ(数字・アドレス・`?t=` を除く)と先頭フレーム(行番号を除く)の指紋でまとめて回数を数える。先頭フレームは worktree 内の最初のフレーム(node_modules やランタイムは飛ばす)
- 送った・無視したものは同じ指紋が再発するまで出さない
- 送るときは出力と、先頭フレームの前後 5 行の抜粋を `~/.local/share/pekoe/<repo-id>/errors/` の md に書き、pty には `@` 参照の 1 行だけ貼る(ピックと同じ)

**逆方向(エージェントがブラウザを操作)** は自作しない。Chrome DevTools MCP や Playwright MCP を各セッションに繋ぐ。

**実装** — TypeScript。WXT か素の MV3。デベロッパーモードで「パッケージ化されていない拡張機能を読み込む」でストア不要。拡張は数百行に抑え、賢さはデーモン側に置いて拡張の更新頻度を下げる。

## ブラウザプレビュー(TUI 内)

UI の変更を確かめるためだけに Chrome へ移らなくて済むように、dev server の画面をメインパネルの preview 窓に出す。拡張のピックが「変更を頼む側」なら、プレビューは「結果を確かめる側」。

- **撮影はデーモン** — pekoed が headless Chrome(`/Applications/Google Chrome.app`、`$PEKOE_CHROME` で差し替え)で `http://localhost:<port><path>` を撮る。`--remote-debugging-pipe` 上の DevTools プロトコルを直接話す(fd 3/4 の NUL 区切り JSON なので websocket も追加の依存も要らない)。`--screenshot` の一発撮りにしないのは、新しい headless モードの窓幅が最小 500px でモバイル幅が再現できないため。CDP の `Emulation.setDeviceMetricsOverride` なら 390×844・DPR 2・viewport meta 有効の本物のモバイル幅になり、load イベントも待てる。1 枚ごとに使い捨てのプロファイルで起動する(キャッシュが残らず、普段の Chrome とぶつからない)
- **自動の撮り直し** — preview 窓を開いているセッションで、エージェントが手を止めたとき(running → waiting / idle / done)に撮り直す。ポートで何も待ち受けていなければ撮らずに「dev server is not running · d to start」と出す
- **before / after** — 撮り直すたびに 1 つ前の画像を before として残す(それより古いものは消す)。最後に撮ったパスとモバイル切替はセッションごとに覚える
- **表示は TUI** — Kitty graphics の Unicode プレースホルダで、パネルに収まる最大の大きさに縦横比を保って描く。画面サイズの変更・セッション切替・窓を閉じたときは、使わなくなった画像を削除コマンド(`a=d,d=I`)で消してから送り直す。非対応端末では画像の大きさの破線枠に `after 1280×800 · png` と書く


Lua プラグイン 1 つがデーモンの unix socket に繋がり、レビュー面と日常の小技の両方を担う。pekoe が起動した nvim も普段の nvim も同じプラグインが同じデーモンに繋がるので、区別はない。

| 場面 | 操作 | 起きること | 優先 |
| --- | --- | --- | --- |
| A. レビュー | TUI で `r` → `:PekoeReview` | 左に変更ファイル一覧、右に差分。`]h` `[h` で hunk 移動、`a` 承認、`c` コメント、`x` 差し戻し、`?` 意図を聞く。`:PekoeSend` でコメントを 1 通にまとめて pty に送る | 本体 |
| B. 範囲送信 | visual 選択 → `<leader>bs` | セッションをピッカーで選び、フロートに指示を打つ。`path:L40-72` + コード + 指示が送られる。`<leader>bn` はその選択を初期指示に新セッション | 日常 |
| C. 承認 | 自動 | 待ちが発生すると右下にフロート。`y` / `n` が pty にそのまま流れる。`g` でそのセッションに飛ぶ、Esc で無視 | 日常 |
| D. Follow | `:PekoeFollow <slug>` | エージェントが保存したファイルが split に順に開き、変更箇所にスクロールする。`!` で停止 | 後回し |
| E. 診断送信 | `:PekoeFixDiagnostics[!]` | バッファ(`!` でワークスペース)の LSP 診断を一覧にして送る | 後回し |
| F. 逆方向 | エージェントが呼ぶ | デーモンが MCP サーバを兼ね、`open_in_editor(path, line, note)` で nvim がその行に飛びフロートにメモが出る | 後回し |

**レビュー面の起動** — TUI で `r` を押すと、右ペインの claude 画面が nvim に入れ替わる(claude は裏で生きている)。nvim は worktree を cwd にして `PEKOE_SESSION=<slug>` を環境変数で受け取り、プラグインが自動で `:PekoeReview` を開く。`:PekoeSend` で nvim が閉じて claude 画面に戻る。

**プロトコル** — JSON Lines。デーモン → nvim はイベント(`session.waiting`、`session.done`、`editor.open`)。nvim → デーモンはコマンド(`session.send`、`review.approve`、`review.comment`、`review.flush`)。全部デーモンの共通 API なので、TUI や CLI と同じコマンドを叩いている。

**前提** — Neovim。Vim 9 でも popup と channel で作れるが、Lua が使えない分書きづらい。

## レビュー機能

レビューはこのツールで一番価値が出る場所。エージェントを増やすほどボトルネックは人間のレビューになるので、「読む量を減らす」と「探す側ではなく確認する側に回る」の二点で設計する。

**レビュー待ち行列** — `done` になったセッションが自動で列に入る。`]r` で次へ。待ち時間と diffstat、テスト結果が横に出るので、どれを先に見るかを一瞬で決められる。

**差分の差分** — レビューを終えた地点を `refs/pekoe/<slug>/review-<n>` に記録する。指摘 → 修正後の再レビューでは main との全差分ではなく、前回レビューからの変更だけを出す。再レビューの手間が一番減るのはこれ。

**hunk 単位の承認** — `a` で承認、`x` で差し戻し、`c` でコメント。承認済み hunk はデーモンが記録し、次回は隐れる。hunk の同一性はファイルパス + 変更後内容のハッシュで取る(行番号がズレても残る)。

**hunk ↔ プロンプトの対応** — プロンプト単位スナップショットがあるので、各 hunk がどのスナップショット間で生まれたかを逆算できる。hunk の横に「指示 #3: CSV の空行をスキップして」を表示し、「なぜこう変えたか」を聞く前に自分で分かる。

**人間が見る前の門番** — Stop hook で `test` を自動実行し、結果を左ペインに出す。さらに別の読み取り専用エージェントに「危ない箇所だけ列挙」させ、quickfix に流し込む。これはレビューの代替ではなく、読む順序を決めるための材料。

**その場で聞く** — hunk 上で `?` → そのセッションのエージェントに「ここの意図は?」を送り、返事をフロートに出す。返事の検出は Stop hook で行い、その間の出力をデーモンが切り出す。

**人の PR にも同じ UI** — `pekoe review gh#123` で PR ブランチの worktree を作り、同じレビュー画面を開く。コメントの送り先をエージェントの pty ではなく `gh api` 経由の GitHub レビューに切り替えるだけ。チームのレビューと自分のエージェントのレビューが同じ操作体系になる。

**状態遷移** — done → reviewing → (指摘あり) run → done → reviewing → … → (全 hunk 承認) approved → `pekoe land` → landed。

## データの置き場(リポジトリ非汚染)

リポジトリに残るのは worktree という git 標準の痕跡だけで、`git worktree prune` で跡形なく消える。

| データ | 置き場 | 備考 |
| --- | --- | --- |
| リポジトリごとの設定 | `~/.config/pekoe/repos/<repo-id>.toml` | repo-id は origin URL の正規化。clone し直しても同じ設定が当たる |
| 言語別の既定値 | `~/.config/pekoe/defaults/*.toml` | `pekoe init` の検出ルール。自分で書き換えられる |
| プレビューのスクショ | `~/.local/share/pekoe/<repo-id>/preview/<slug>/` | after と before の 2 枚と、最後のパス・モバイル切替(`preview.json`) |
| 送ったエラー | `~/.local/share/pekoe/<repo-id>/errors/<slug>-<n>.md` | `pekoe rm` で消える |
| 要素情報(picks)・スクショ | `~/.local/share/pekoe/<repo-id>/picks/` | pty には絶対パスで `@` 参照を貼る。worktree 内に置きたければ `.git/info/exclude` に `.pekoe/` を追記(コミット対象外、worktree 間で共有) |
| プロンプト単位スナップショット | `refs/pekoe/<slug>/prompt-<n>` | 通常の push では送られず `git log` にも出ない |
| レビュー地点 | `refs/pekoe/<slug>/review-<n>` | 同上 |
| hunk 承認・レビューコメント | 状態 DB | hunk はパス + 内容ハッシュで識別 |
| Claude Code の hooks | `~/.claude/settings.json` | ユーザー側に書き、全リポジトリに一括で効く。プロジェクト側 `.claude/` は触らない |
| worktree 本体 | `~/worktrees/<repo>/<slug>` | pekoe が削除していい領域はこの配下だけ |
| ポート台帳・DB 名・セッション ID・状態 | `~/.local/share/pekoe/state.db`(SQLite) | デーモンの外部記憶。再起動後の復元に使う |
| デーモンの socket | `~/.local/share/pekoe/pekoed.sock` | TUI・CLI・nvim が繋ぐ |
| デーモンのロック | `~/.local/share/pekoe/pekoed.lock` | 動いている pekoed が `flock` で持つ。1 つの state.db に pekoed は 1 つ |
| 拡張用 HTTP | `127.0.0.1:<port>` + トークン | トークンはインストール時に生成し拡張の storage に入れる |

将来チームに広げる場合は、リポジトリ内 `.pekoe.toml` を「あれば読む・個人設定が上書き」の順で足す。

## CLI コマンドとキーバインド

CLI は人間とエージェントの両方が叩く。出力は `--json` で機械可読にできる。各サブコマンドは受け付けるフラグ(bool と値つき)を宣言し(`cmd/pekoe/flags.go`)、知らないフラグはエラーにする。`--help` / `-h` は何も実行せずに使い方だけを出す。打ち間違いや `--help` が状態を変えるコマンドにそのまま流れないようにするため。

| コマンド | すること |
| --- | --- |
| `pekoe` | TUI を開く(デーモンがなければ起動) |
| `pekoe init` | 今のリポジトリの設定を検出して下書き |
| `pekoe new <branch> [--repo R] [--from gh#N] [--agent codex]` | worktree + セッション作成 |
| `pekoe open <worktree>` / `pekoe adopt` | 既存を開く / 管理下に入れる |
| `pekoe ls [--all \| --repo R]` | セッション一覧と状態。リポジトリの中では既定でそのリポジトリだけ、`--all` で全部。リポジトリの外では全部をリポジトリごとの見出しでまとめる(1 つなら見出しなし)。slug がブランチ名どおりでないときは `slug <slug>` を添える。`--json` は絞り込みだけ反映した配列 |
| `pekoe issues [--repo R] [--all \| --label L] [--refresh]` / `pekoe issues <N>` | GitHub の open issue(既定は自分担当)と、すでにあるセッション / issue の本文とコメント |
| `pekoe conflicts [--repo R]` | 衝突予報。同じファイルを触っている open なセッションのペアと、コミット済みの内容が衝突するか(`git merge-tree`)。`--repo` で絞り込み |
| `pekoe send <slug> <text>` | pty に文字を送る。slug を取るコマンド(`send` `review` `land` `rm` `dev` `undo` `answer`)は `<repo>/<slug>` でも指定できる |
| `pekoe event` | hooks から呼ばれる。stdin の JSON(session\_id, cwd, hook 名)をデーモンに渡す |
| `pekoe review [<slug> \| gh#N [--repo R]]` | レビュー面を開く |
| `pekoe land <slug>` / `pekoe rm <slug>` | 取り込む / 消す |
| `pekoe dev <slug>` | 設定の `dev` をその worktree のポートで起動 |
| `pekoe daemon start\|stop\|status` | デーモン操作 |
| `pekoe migrate [--dry-run] [--prune-old]` | 旧名 boba の環境から一度だけ移す(下記) |

**旧名 boba からの移行(`pekoe migrate`)** — 旧名を読み続ける互換処理は持たず、移行はこのコマンドで一度に済ませる。デーモンが動いていれば何もせずに中止する。手順は、`~/.config/boba` を `~/.config/pekoe` に、`~/.local/share/boba` を `~/.local/share/pekoe` に移して `bobad.sock` `bobad.log` `bobad.pid` `bobad.lock` を `pekoed.*` に改名 → state.db に残る旧パスと `refs/boba/…` の ref 名を書き換え、セッションの env(`sessions.data` の JSON の `env` 配列だけを解いて書き換える)の `BOBA_<NAME>=…` / `BOBA=…` をキーだけ `PEKOE_<NAME>=…` / `PEKOE=…` に改名(同じ新キーがすでにあれば旧キーの方を捨てる。プロンプト本文など利用者が書いた列は触らない)→ 各リポジトリの `refs/boba/*` を `refs/pekoe/*` にコピー(`--prune-old` のときだけ旧 ref を消す)→ `~/.claude/settings.json` の `boba event` の hooks を `pekoe event` に置き換え → 最後に手作業で残るもの(MCP の登録し直し、launchd、nvim の設定、Chrome 拡張の再読み込み、旧 tmux サーバー)を表示する。`--dry-run` は何も書かずにすることだけを出す。移すものが無い(旧ディレクトリが残っていない)ときも state.db 以降の手順は新しい `~/.local/share/pekoe` に対して行うので、env の改名が入る前に移行を済ませた環境は、デーモンを止めてもう一度 `pekoe migrate` を実行すれば直る(専用のオプションは設けない)。

**TUI キーバインド**(vim 風。すべて設定で変更可)

| キー | 動作 |
| --- | --- |
| `j` `k` / Enter | セッション選択(リポジトリの見出しは飛ばす) / 右ペインにフォーカス |
| `n` | 今のリポジトリに新しい worktree + セッション(リポジトリの外で起動して今のリポジトリがなければ、先にリポジトリを選ぶ) |
| `w` | リポジトリを選ぶ(知っているリポジトリの一覧モーダル)。セッションがあればその先頭を選び、なければそのリポジトリだけを今のリポジトリにする |
| `]a` `[a` | 次の待ちセッション(PR に動きがあったセッションも含む) / 元の場所に戻る |
| `]r` | 次のレビュー待ち |
| `r` | レビュー面(nvim)を開く |
| `p` | PR ビューを開く(右ペインが PR の状態に切り替わり、左の選択に追従する。Esc で agent に戻る) |
| `b` | その worktree のポートを Chrome で開く |
| `v` | preview 窓(dev server のスクショを TUI 内に表示)。メインパネルでは agent → preview → dev → agent と巡る |
| preview 窓で `R` `m` `c` `g` `o` / Esc | 撮り直す / デスクトップ・モバイル切替 / before・after(幅があれば横並び) / パスを入力 / Chrome で開く / agent 窓に戻る |
| `l` | land(確認に、他の open なセッションとの衝突予報を出す) |
| `d` | dev server 起動 / 停止 |
| `!` | Sessions で: その worktree でコマンドを 1 回だけ実行する(run 窓。下記)。入力中の `!` は今までどおりエージェントに渡る |
| `x` | セッション終了(worktree は残す) |
| `D` | worktree 削除(確認あり) |
| `/` | セッションの絞り込み(ブランチ名・slug・リポジトリ名) |
| `[` `]` | パネル内のサブタブ切替([2] Worktrees → Browser → Issues、[3] Changes ⇄ Picks) |
| Issues: `j` `k` | issue 選択(本文とコメントをメインパネルに。Esc で agent 窓へ) |
| Issues: Enter / `n` | その issue のセッションへ飛ぶ / issue からセッション作成(ブランチ名を提案するモーダル) |
| Issues: `f` `/` `o` `R` | 自分担当 → 全 open → ラベル指定 / 絞り込み / ブラウザで開く / 再取得 |
| `?` | ヘルプ |
| prefix + `h` `l` | 左ペインと右ペインのフォーカス移動(右ペインにフォーカス中はキーがすべて claude に渡る) |

**複数リポジトリ(TUI)** — TUI は全リポジトリのセッションを [1] Sessions に並べる。セッションが 2 つ以上のリポジトリにまたがるときは、リポジトリごとにまとめて見出し行(リポジトリ名と罫線。今のリポジトリにはカレント記号)を挟む。リポジトリの並びはそれぞれの最初のセッションの作成順で、まとまりの中は従来どおり作成順(landed は下)。1 つしかなければ見出しは出さない。見出しなどに出すリポジトリ名はふだん短い名前だけで、短い名前が同じリポジトリが複数あるときだけ、区別できるところまで repo-id の上位の要素を足す(`a/api` と `b/api`、所有者まで同じならホストまで。origin のない `path_` の ID は親ディレクトリ名を足して `src/api`)。この表示名は見出し・`w` のモーダル・ボトムバー・[2] のタイトル・ウィンドウタイトルで共通。

TUI は「今のリポジトリ」を 1 つ持ち、`n`・[2] Worktrees・[2] Issues・ピックからの新規セッションはそこに向く。今のリポジトリは、`w` で選んだもの(選択が別のセッションに動くまで) → 選んでいるセッションのリポジトリ → 起動したディレクトリのリポジトリ、の順に決まる。起動直後は起動したリポジトリのセッションを選び、セッションがなければ起動したリポジトリそのものを今のリポジトリにする。名前はボトムバーのブランチのセグメント(`web feat/x :3001`)、[2] Worktrees / Issues のタイトル末尾(`[2] Worktrees 3 web`、`[2] Issues 12 web mine`)、端末のウィンドウタイトル(`pekoe · web`)に出る。

リポジトリの外(`~` など)で起動したときは、既知のリポジトリ(`repo.list`)から `w` で選ぶ。どれもなければ [1] に 1 行の案内を出す。設定ファイルの repo-id からは場所が分からないことがあり(origin 由来の ID)、`~/worktrees/<repo>/` の worktree からも辿れないものは「path unknown」として一覧に出すだけで選べない。

**PR ビュー**(`p`。右ペインの 4 つ目の窓。GitHub への書き込みはしない)

| キー | 動作 |
| --- | --- |
| `j` `k` | 項目を選ぶ(Description → Checks → Reviews → Threads → Comments の順。失敗・未解決が先) |
| Enter | 項目を展開(PR 本文、スレッド全文、レビュー・コメント本文、失敗した check のログ末尾) |
| Space | 選択トグル(`s` でまとめて 1 通にする) |
| `s` | 選んだ項目をプロンプトにしてエージェントの pty に送る(確認あり)。失敗 check はログ末尾付きで「直して」、スレッドは path:line とコメント全文付きで「対応して」。人の PR(`review gh#N`)では無効 |
| `o` | 選んだ項目(なければ PR)をブラウザで開く |
| `R` | 再取得(開いている間は 15 秒ごとに自動更新) |
| Esc / `h` | agent 窓に戻る / Sessions にフォーカス |

Issue と PR の本文・レビュー・コメントは軽いマークダウンとして描く(`internal/tui/markdown.go`)。見出し・リスト・タスクリスト(☐ / ☑、ascii では `[ ]` / `[x]`)・引用・コードブロック・区切り線・簡単な表と、行内の太字・斜体・取り消し線・インラインコード・リンク。リンクと裸の URL は文字だけを secondary で出し、OSC 8 を付けてクリックで開ける(http / https / mailto だけ)。コードの中では記法を解釈しない。

PR の状態はデーモンが 60 秒ごとに `gh api graphql` で要約を取り(`Session.pr_status`)、一覧の `#42` に状態アイコンを付ける。CI が失敗した・changes requested になった・未解決コメントが付いた・マージ可能(CLEAN)になったときは注意キュー(`]a`)に入り「•」が付く。PR ビューを開くと既読になる。マージ・クローズはどのセッションでも検出して表示するが、teardown は land 中のセッションだけ。

## 実装ロードマップ

Phase 2(レビュー面)を越えた時点で日常利用に切り替え、その後は使いながら足す。自前 mux は最後で、やらない選択もある。

&#91;embedded content: 実装ロードマップ · 7 フェーズと 6 つの gate\]

各 gate は「次に進む条件」であり、満たさないまま次のフェーズに手を出さない。

**技術選定**(実装言語を Rust とした場合。未決事項参照)

| 部品 | 技術 |
| --- | --- |
| pekoed | Rust(tokio, serde)。git は CLI 呼び出しで済ませ libgit2 は使わない。状態は rusqlite |
| TUI | ratatui + crossterm。Phase 0–4 は tmux control mode を子プロセスで持つ |
| VT(Phase 5) | libghostty-vt を C API 経由で FFI |
| Chrome 拡張 | TypeScript, WXT, MV3 |
| Neovim | Lua。`vim.uv` で socket に接続 |
| MCP サーバ(逆方向) | pekoed 内に同居。`open_in_editor` と `session.list` を公開 |
| 通知 | ntfy(curl 一発) |
| パッケージング | 当面 `cargo install` と launchd の plist。拡張は unpacked |

**Phase 0 の最初の一歩** — `pekoe new` を shell script で書いて 1 日使う。worktree 作成と setup の設計が合っているかは、デーモンを書く前に分かる。

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
- worktree の容量(node\_modules の複製) → APFS クローンとストア共有で緩和。`pekoe rm` とマージ後の自動削除で残骸を残さない
