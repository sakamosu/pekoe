# Web UI(iPhone から pekoe を操作する)

Mac で pekoed を動かしたまま、外出先の iPhone(Tailscale アプリが ON)の Safari から開き、ホーム画面に追加して使う軽い Web UI。できるのは次の 8 つだけ。

- セッション一覧(状態つき。許可待ちは一番上に、カードで)
- 許可待ちへの allow / deny(ツールの要約と、エージェントが画面に出している許可ダイアログを見せ、押す前に確認を挟む。`always` は出さない)
- `send`(短い指示を送る。allow / deny で答える許可待ちの間は送れない)
- 質問(AskUserQuestion などの択一・複数選択メニュー)へのキーパッド(← ↑ ↓ → / Tab ⇧Tab Space / 1〜9 / Esc Enter。質問待ちのときだけ出る)
- エージェント画面の最新テキストを読むだけの表示(色・太字などは残す。カーソル位置や画面の再現まではしない)
- 判断の受信箱への回答(Inbox タブ。pekoed の事実とリンクを先に、エージェントの文を後に出し、送る前に確認シートを挟む)
- 概要と工程表を読むだけの表示(Overview タブ。件数・tapi の状態・セッションごとの `impl → review → PR → CI → merge` と、マージしてよいかの根拠の足りない条件)
- tapi chat(統括役)の画面・質問へのキーパッド・`send`(送る前に宛先と全文の確認シート。`allow_devices` を設定しているときだけ送れる。tapi の許可待ちへの allow / deny は出さない)

賢さは pekoed にあり、Web は表示層。ハンドラは CLI と同じ API メソッド(`session.list` / `session.screen` / `session.answer` / `session.send` / `session.keys` / `inbox.list` / `inbox.answer` / `tapi.get` / `tapi.screen` / `tapi.send` / `tapi.keys` / `merge.check`)を同じ `Daemon.Call` 経由で呼ぶ。

## 構成

```text
iPhone (Safari / ホーム画面)
   │ HTTPS  https://pekoe.<tailnet>.ts.net/
   ▼  WireGuard(tailnet の中だけ)
┌─ pekoed ───────────────────────────────────────┐
│ tsnet ノード "pekoe"  :443 (ListenTLS)           │
│   └ webGuard: Host → WhoIs → 許可ユーザー → CSRF │
│       └ /api/* → Daemon.Call(session.*)         │
│       └ /      → go:embed の静的ファイル         │
│ unix socket (TUI・CLI・nvim)   … 既存のまま      │
│ 127.0.0.1:47777 (Chrome 拡張)  … 既存のまま      │
└─────────────────────────────────────────────────┘
```

- pekoed は Tailscale の [tsnet](https://tailscale.com/kb/1244/tsnet) を埋め込み、pekoed 自身が tailnet の 1 台(既定のホスト名 `pekoe`)になる。Mac に入っている Tailscale アプリとは別のマシンとして admin console に出る
- 待ち受けは tsnet の仮想ネットワーク上の `:443` だけ。**localhost にも Mac の LAN 側にも TCP ポートは開かない**。tsnet は WireGuard 用に UDP ポートを 1 つ使う(P2P の直結用。無くても DERP 経由で動く)
- 証明書は tsnet が Let's Encrypt から `pekoe.<tailnet>.ts.net` の分を取る(tailnet で MagicDNS と HTTPS を有効にしておく必要がある)
- Funnel(インターネットへの公開)は使わない。`ListenFunnel` は呼ばず、tsnet は起動時に serve 設定を空にする
- 静的ファイル(`internal/daemon/webui/`)は `go:embed`。外部 CDN・フォントは読まない。PWA の manifest とアイコン(PNG をその場で描く)つき。Service Worker と Web Push は入れない
- 証明書は待ち受けを始めるときに先に取っておく(初回の発行は時間がかかり、iPhone の最初の接続がタイムアウトしないように)。HTTPS が tailnet で無効などで待ち受けに失敗したら、30 秒ごとにやり直す(admin console で後から有効にしても再起動は要らない)。マシン名の変更にも追従する
- 更新は 3 秒ごとのポーリング(画面を開いている間は画面テキストも 2.5 秒ごと)。アプリが裏に回ると止まる
- 画面テキストは中身が変わったときだけ描き直す。画面の中を選択している間(長押しのメニューが出ている間も)と指が触れている間は描き直さず、選択が外れたらすぐ追いつく。`pause` ボタンで止めておくこともできる(コピー用)

### ビルド:既定は Tailscale なし

Web UI は build tag `web` を付けたビルドにだけ入る。職場の Mac のように VPN クライアントを含むソフトを入れたくない環境もあるので、既定のビルドには Tailscale のコードを一切含めない。

```sh
make install          # 既定: Web UI なし(Tailscale のコードを含まない、約 19MB)
make install WEB=1    # Web UI 入り(-tags web、tsnet をリンク、約 38MB)
```

GitHub Release にも両方を置いている。`pekoe_<ver>_<os>_<arch>.tar.gz` が既定、`pekoe_<ver>_<os>_<arch>_web.tar.gz` が Web UI 入り(配置は同じ)。`pekoe update` は今の pekoe が Web UI 入りなら `_web` の方を取る(README の「更新」)。

- 既定のビルドでは `go version -m pekoe` にも `tailscale.com` は出ない(`internal/daemon/webts.go` が `//go:build web`、代わりに `webts_off.go`)。ただし `go.mod` には依存として載っている
- 既定のビルドで設定に `[web] enable = true` があっても(dotfiles で個人の Mac と設定を共有しているなど)、何もせずに pekoed のログに警告を出すだけ。`pekoe web` は「Web UI なしのビルド」と再ビルドの方法を出す
- `make test` は両方のビルドを検査する。隔離環境で Web UI 入りを試すには `WEB=1 scripts/sandbox.sh`
- Go と `tailscale.com` の版は組にして上げる。Go 1.27 から `encoding/json` が v2 の実装の上で動くようになり、tailscale v1.92 の `tailcfg.SSHAction` の `format:nano` タグを受け付けなくなった。Go 1.27 でビルドした v1.92 は、SSH ルールのある tailnet だと control から届く netmap を毎回読めず、`starting` のまま止まっていた(エラーにもログイン待ちにもならない)。`internal/daemon/webts_test.go` の `TestTailscaleJSONDecodes` が、control や LocalAPI とやりとりする型をビルドに使う Go の `encoding/json` で読めるかを確かめる

### `[web]` が無効なとき

Web UI 入りのビルドでも既定は無効。無効なら `startWeb` は状態を `off` にして戻るだけで、`tsnet.Server` を作らず、状態ディレクトリも作らず、ソケットも開かない(`TestWebOffStartsNothing`)。リンクされた tailscale パッケージの `init()` には、ゴルーチン・ファイル・ネットワークを使うものはない。

## 設定

`~/.config/pekoe/config.toml`:

```toml
[web]
enable        = true
hostname      = "pekoe"              # tailnet 上のマシン名 → https://pekoe.<tailnet>.ts.net/
allow_users   = ["you@github"]       # Tailscale のログイン名(admin console の Users に出るもの)
allow_devices = ["iphone-15"]        # 任意。この端末だけに絞る(admin console の Machines の名前)
# state_dir   = "~/.local/share/pekoe/tsnet"   # 既定。$PEKOE_HOME があればその share/tsnet。~ か絶対パスで
# auth_key    = "tskey-auth-…"        # 初回参加用。環境変数 TS_AUTHKEY でもよい
```

- `allow_users` が空なら起動しない(誰も入れないサーバーを立てても意味がないので、エラーにして `pekoe web` に出す)
- `allow_devices` は強く勧める(下の脅威モデルを参照)
- 設定を変えたら pekoed を再起動する(`pekoe daemon restart`。エージェントは tmux の中で動き続ける)

### 初回の tailnet 参加

1. `make install WEB=1` で Web UI 入りのビルドを入れる
2. tailnet の admin console で MagicDNS と HTTPS Certificates を有効にする
3. `[web]` を書いて pekoed を再起動する
4. `pekoe web` を実行する。`state needs-login` と `login https://login.tailscale.com/a/…` が出るので、そのリンクを開いて承認する。URL は `pekoed.log` には出さない(ログはエージェントの sandbox から読め、先に開いた人の tailnet にこのノードが入ってしまうため。ログには「`pekoe web` で確認」とだけ出る)。`web.status` は人間用トークンが要るので、エージェントは `pekoe web` でも読めない。pekoed の起動直後の数秒は URL がまだ無く、`login  (getting the login URL…)` と出るので、少し待ってもう一度 `pekoe web` を実行する。auth key を使うなら `auth_key` か `TS_AUTHKEY` を設定して再起動すれば、URL は出ずに参加する
5. `pekoe web` が `state running` と `url https://pekoe.<tailnet>.ts.net/` を出したら、iPhone の Safari でその URL を開き、共有メニューから「ホーム画面に追加」

`TS_AUTHKEY` は pekoed が起動時に自分の環境変数から取り除いてから tmux を立ち上げるので、エージェントのペインには渡らない(渡ると、エージェントがその鍵で自分のノードを tailnet に参加させ、ユーザー本人として Web UI を叩けてしまう)。`auth_key` を設定に書いた場合は、同じユーザーで動くエージェントには読めるので、参加したらすぐ消す。ログイン URL で参加するのがいちばん安全。

`state starting` のまま進まないときは、`pekoe web` の `backend`(Tailscale の backend の状態)と `backend error`(control から受け取れなかった理由など)を見る。同じものが `pekoed.log` にも `web: tsnet backend: …` として出て(ログイン URL と auth key は伏せる)、1 分たっても starting のままなら `web: tsnet still not running after 1m0s (backend state …)` も出る。

参加後の鍵は `state_dir`(既定 `~/.local/share/pekoe/tsnet`、パーミッション 700)に残るので、2 回目以降は何もしなくてよい。admin console で `pekoe` マシンの key expiry を無効にしておくと、180 日ごとの再ログインが要らない。

## 認証の流れ

token もパスワードも cookie も持たない。リクエストごとに、tailnet がその接続を誰のものと言うかだけを見る(`webGuard`)。

1. **Host**: `Host` ヘッダがこのノードの名前(tsnet の `CertDomains`)でなければ 421。DNS rebinding で別の名前から来たものを落とす
2. **このマシン自身**: 接続元の IP が Mac のネットワークインターフェイスのどれかのアドレス(Mac の Tailscale アプリの `100.x` など)かループバックなら 403。Mac の Tailscale が userspace-networking(TUN なし)で 100.x がどのインターフェイスにも無い場合に備え、WhoIs で引いた相手ノードのホスト名が Mac のホスト名と同じでも 403
3. **WhoIs**: tsnet の `LocalClient().WhoIs(remoteAddr)` で接続元のノードとユーザーを引く。引けない・タグつきノード・`allow_users` にないユーザー・(設定があれば)`allow_devices` にない端末は 403。比較は大文字小文字を区別しない
4. **書き込み(POST)**: `X-Pekoe-Web: 1` ヘッダと `Content-Type: application/json` が必須。`Sec-Fetch-Site` があれば `same-origin`、`Origin` があれば自分の URL でなければ 403
5. 応答には CSP(`default-src 'self'`、インライン script なし)・`X-Frame-Options: DENY`・`Referrer-Policy: no-referrer` を付け、API は `Cache-Control: no-store`

拒否は理由つきで `pekoed.log` に出る(`web: 403 GET /api/sessions from 100.x:port: user … not in allow_users`)。allow / deny / send / keys は誰がどの端末から何をしたかをログに残す。

### allow / deny の扱い

- 受け付ける答えは `allow` と `deny` だけ。`always`(と CLI の別名 `a` `y` `yes` `n` など)は 400。恒久的な許可は端末の前で決める
- `wait_kind` が `question`(AskUserQuestion など)のときは 409。allow / deny で答えるものではないので、画面を見てキーパッドか `send` で答える
- 押したときに画面に出ていたツール(名前と引数)を一緒に送り、pekoed の今の `pending_tool` と違えば 409(「内容が変わりました」)。読んでいないプロンプトに allow が当たるのを防ぐ
- 押す前に確認シートを挟む。シートには pekoe の要約(`pending_tool`:ツール名と主な引数 1 つ、200 バイトまで)と、画面(`/screen` が返す最後 80 行。開いたときは一番下で、上にスクロールできる。エージェント自身の許可ダイアログに実行するコマンドの全体が出る)を並べる。**判断は画面の方で行う**
- 要約は改行のかたまりを ` ⏎ ` 1 つに、長い空白の並びを 1 つにまとめてから 200 バイトで切る。空行や空白で水増ししても、本体が要約の外に押し出されない。引数が複数行(`lines`)か、要約が切れている(`cut`、全体の `size`)ときは、一覧とシートに「⚠ 全体は画面で読む」旨の警告を出し、シートの画面を高くする
- 一致の確認は `session.answer` の中(`expect`)で、キーを送る直前に行う

`pending_tool` は「最後に届いた PreToolUse」で、許可ダイアログが出ているツールそのものとは限らない(エージェントが複数のツールを並行に呼ぶと、後から来た方が残る)。また 200 バイトで切った要約なので、末尾だけ違うコマンドは区別できない。だから要約の一致は「読んでから状況が変わっていないか」の検出に使い、何を許可するかは画面で確かめる作りにしている。

### send の扱い

allow / deny で答える許可待ち(`answerable`)の間は 409。許可メニューは最初の 1 文字を選択肢として受け取るので、「2つ目も…」のような文が「Yes, and don't ask again」(= always)になってしまう。質問(`wait_kind == question`)には send かキーパッドで答える。

send はテキストを送るもので、キーは送らない。改行とタブ以外の制御文字(ESC、`^C`、DEL など)を含む本文は 400。ESC を通すと Shift-Tab(`\e[Z`)などのキーやペーストの終端を打てて、キーパッドの許可リストを迂回できてしまうため。デーモン側でも、テキストとして pty に送る経路(`session.send` / `tapi.send`、エラーやピックの送信、opencode への初期プロンプト)はすべて同じ文字を落としてから送る。

### 受信箱(Inbox タブ)の扱い

docs/orchestration.md の「受信箱で人を誘導する」への対策を、TUI のモーダルと同じ並びで Web にも当てる。

- 1 件のカードは上から、pekoed が読んだ事実(`facts`: セッション・状態・ブランチ・HEAD・差分・PR と CI・レビューの判定・`merge_state`・テスト・衝突予報・最新の報告・マージしてよいかの条件)とリンク → エージェントの文(`from`、名乗りが違えば警告、問い・背景・選択肢)の順に出す
- 推奨は選択肢の横に `recommended (agent)` と書くだけで、選んだ状態にしない。何も選ばない(選択肢のない問いならメモが空の)うちは `answer…` を押せない
- `answer…` で確認シートを開き、宛先(問いを出したエージェント)・問い・選んだもの・メモを並べてから送る
- 送るのは、カードを描いたときの `rev`。pekoed は `inbox.answer` の中で事実を読み直して比べるので、見た後に HEAD が動いた・CI が落ちた・別の端末で答えた項目は 409 になり、ページは選択を外して「changed while you looked」と出す。3 秒ごとの更新で `rev` が上がったときも、選択を外して同じ表示にする
- 答えは `answer.by: human`・`answer.via: web:<端末名>` で残り、誰がどの端末で何番を選んだかを pekoed のログに出す
- 答えずに閉じる(`dismiss`)・取り下げは Web には出さない(TUI か CLI で)
- tapi の許可待ちへの allow / deny は出さない。Overview は tapi の状態・待ち時間・最後の返事・件数を読み、`talk to tapi ›` で tapi chat の画面を開く(次の「tapi chat の扱い」)。`pending_tool`・Claude のセッション ID・稼働ディレクトリは返さない

### tapi chat の扱い

tapi は全リポジトリを読み、`pekoe new` でセッションを作って任せる。tapi への send はセッション 1 つへの send より重い(iPhone が全リポジトリへの指示の入口になる)ので、セッションの send の確認に次を足す。

- **`allow_devices` が要る**: `[web] allow_devices` を設定していないと `POST /api/tapi/send` は 403(`allow_users` の誰かの別の端末から tapi に指示させない)。ページは送信欄に理由を出して send を押せなくする。画面とキーパッドは指示にならないので、`allow_devices` が無くても出す
- **確認シート**: send を押すと必ずシートを挟む。宛先が tapi(統括役、全リポジトリ)であることと、打たれる文の全体(文字数・行数つき、シートの中でスクロール)を見せ、`send to tapi` を押したときだけ送る。送信欄の宛先も「tapi orchestrator · all repositories」を warn の色で出す
- **確認はセッションと同じ**: 本文は前後の空白を落として 1〜4000 文字、改行・タブ以外の制御文字は 400。tapi が許可待ち(hook の状態)なら 409。`tapi.send` が打つ前と Enter の直前に hook の状態と画面の許可メニュー(`promptUp`)を見直し、出ていれば 409。tapi が起動していない・終了しているときも 409
- **記録**: 送ったものと、断ったもの(許可待ち・画面の許可メニューの 409。400・`allow_devices` の 403・tapi が起動していない・終了しているときの 409 は除く)を送信の記録(`send.log`)に `from: human`・`to: +tapi`・`kind: send`・`via: web:<端末名>` で残す。pekoed のログには誰が・どの端末から・送信の記録の何番か・本文の最初の 200 文字を出す(全文は送信の記録。ログは同じユーザーのエージェントから読めるので切る)。`allow_devices` で断ったものはログにだけ出る
- **キーパッド**: tapi が質問待ちのときだけ、セッションと同じキー(`Up Down Left Right Tab BTab Space Enter Escape 0`〜`9`、1 回 8 個まで)。許可待ちのときと、画面に許可メニューが出ているとき(hook は偽れるので画面で見る)は 409
- **allow / deny は無い**: tapi の許可待ちに答える API は無い(`/api/tapi/` に answer は無く、`/api/sessions/+tapi/…` は `session.*` が `+tapi` を拒むので届かない)。許可待ちの間、ページは「端末(pekoe TUI)で答える」とだけ出し、send とキーも止める

#### 吹き出し(chat)と画面(screen)

tapi chat のパネルは既定で会話を LINE のような吹き出しで出す。タイトルの右の `chat | screen` で、今までの端末の画面(pause つき)に切り替えられる(選んだ方は端末のブラウザに残る)。

- **データ**: 会話は画面ではなく Claude Code の transcript から取る。tapi の hook が渡す `transcript_path` を pekoed が覚え(`tapi.transcript`、人間だけ)、ページは `GET /api/tapi/transcript?after=<cursor>` で 2 秒ごとに差分を取る。覚えるのは tapi の Claude Code のプロジェクトディレクトリ(`~/.claude/projects/<tapi の cwd>/`)の中の `<id>.jsonl` だけ。`session.event` は token なしで誰でも `+tapi` を名乗れるので、ほかのパスは読まない。pekoed を再起動して hook がまだ来ていないときは、同じディレクトリのいちばん新しい transcript を読む
- **返すもの**: 人のテキスト・tapi のテキスト・ツールの名前と主な引数 1 つ(許可待ちの要約と同じく 200 バイトで切る。AskUserQuestion は問いと選択肢の見出し)・ターンの所要時間と時刻だけ。tool_result(`type: user` の行に入っているものも)・thinking・システムの注入(system-reminder・compact の要約・スラッシュコマンドとその出力・`!` のコマンド・task-notification など)・サブエージェントの行は返さない。pekoe が打った行(`[pekoe inbox #3] …`)は人の発言と分けて返し、ページは dim の 1 行で出す
- **差分**: カーソルは `<会話の id>:<読んだバイト数>`。書きかけの行は次に回す。最初の読み込みは transcript の最後の 6 MB・400 件まで(それより前は `earlier`)。`/clear` などで hook が別のファイルを渡すと会話が変わったとみなし、ページは `new conversation` の区切りを入れて続ける
- **描画**: エージェントの文は信用しない(受信箱と同じ)。`textContent` と自前の最小のマークダウン(見出し・リスト・チェックボックス・インラインコード・コードブロック)だけで組み、リンクは張らない。15 行を超える返事は畳んで `more › +N lines`。ツール呼び出しは吹き出しの間の dim の 1 行(`› 4 tools · Read docs/x.md · …`)で、タップで名前と引数の一覧を開く(結果の中身は出さない)
- **状態**: run 中は左に Braille のスピナーと `thinking · 12s`、許可待ちはペインの中のカード(端末で答える)、質問は tapi の吹き出しの下にキーパッドのカード。送信の確認シートはそのまま。送った文は transcript に現れるまで `●`(secondary)、現れたら `✓`
- **落とし方**: tapi が起動していない(409)・transcript が無い(404)・読めないときは画面(screen)に切り替え、画面の上に理由を出す。読めるようになれば吹き出しに戻る

### keys(キーパッド)の扱い

AskUserQuestion の択一・複数選択メニューは矢印・Tab・Space・数字・Enter で操作するもので、文字を打ち込む send では答えられない(複数の質問がタブで並び、最後に Submit)。そこで質問待ちのときだけ、決まったキーを送るキーパッドを出す。

- 送れるのは許可リストのキーだけ: `Up Down Left Right Tab BTab(Shift-Tab) Space Enter Escape` と数字 `0`〜`9`(tmux のキー名)。修飾キー(`C-c` `M-x` など)・任意の文字列・2 桁以上の数字は 400。1 回のリクエストで 8 個まで(ページは 1 タップ 1 キー)
- 送れるのは `state == waiting` かつ `wait_kind == question` で、止まっていない(`stopped` でない)ときだけ。allow / deny で答える許可待ち(`answerable`)のときも、それ以外の状態も 409
- ページは送信中(と応答後の少しの間)キーパッドを無効にし、二度押しで ↓ や Enter が 2 回にならないようにする。押したら画面を早めに取り直して結果を見せる
- 質問の中身(`tool_input`)は解析しない。何を選んでいるかは画面で確かめる

## 脅威モデル

| 脅威 | 対策 | 残る穴 |
| --- | --- | --- |
| **Mac 上のプロセス(エージェント自身を含む)がなりすます** | localhost に何も開かない。ヘッダ(`Tailscale-User-Login` など)を信じず、接続ごとに WhoIs。Mac 自身(アドレス、または WhoIs のホスト名)からの接続は拒否するので、エージェントが Mac の Tailscale 経由で `https://pekoe…/` を叩いて自分の許可待ちに allow を返すことはできない。`TS_AUTHKEY` はエージェントに渡さない | Mac 上で同じユーザーとして動くプロセスは、もともと `tmux -L pekoe send-keys` で自分の pty にキーを送れる(下の「pekoe の API」も)。Web UI はその穴を広げないが、塞ぎもしない。また、エージェントが同じユーザーの**別の端末**(ssh できる Linux 機など)に入って叩くことはできる → `allow_devices` で iPhone だけに絞る |
| **エージェントが pekoe の API(unix ソケット)で許可待ちに答える** | ソケットは 0600 だが、同じユーザーのエージェントはつなげる。そこで token なしで受け付けるのはエージェントに要るメソッド(`session.event` `session.list` `session.get` `session.new` `session.send` `repo.resolve` `editor.open`)だけにし、許可待ちへの回答・生キー・tapi chat への入力・設定の書き込み・コマンドの実行・他のセッションの画面や抜粋・デーモンの停止を含むほかのすべては、`human-token`(データディレクトリの 0600 のファイル)を付けた呼び出しだけ受け付ける(許可リスト。一覧は [api.md](api.md) の「人間だけの操作」)。TUI・CLI・Neovim はファイルを読んで付ける。エージェントのペインの環境には token もそのパスも渡さない。`session.send` はハブのために token なしでも使えるが、エージェントの入力欄だけに限る(許可ダイアログが出ている間の扱いは次の行) | 同じユーザーのエージェントは、Bash でファイルを読めば token を手に入れられる。Claude Code のエージェントには pekoe が sandbox の設定(`--settings`: sandbox を有効、`human-token`・`http-token`・tsnet の state・設定ディレクトリを `denyRead` と `Read` / `Edit` の deny、pekoed のソケットだけ許す)を渡すので読めない。ほかのエージェント、`[agents.claude] sandbox = false`、`~/.claude/settings.json` の `excludedCommands` に挙げたコマンド(sandbox の外で動く)は自分で塞ぐ(README の「エージェントに pekoe を使わせるときの権限」)。token なしの `session.new` は既知のリポジトリに限る(エージェントが自作したリポジトリで pekoed に git を動かさせない)。また `Bash(pekoe *)` のように pekoe を丸ごと許可しない。許可するなら `pekoe ls` / `pekoe --json ls` / `pekoe --json new …` / `pekoe send …` に絞る。状態はエージェントの hook(`pekoe event`)が報告するので、`pekoe event` を叩けるエージェントは状態を偽って、send の確認のうち hook の状態を見る方をすり抜けうる(画面の判定は残る。`pekoe event` も許可しない) |
| **エージェントが書いたコードを pekoed に実行させて token を読む** | pekoed が worktree のコードを実行するもの(Stop のたびの `[hooks] test`、門番、`setup`、`dev`、`teardown`、`land` のテスト)は sandbox で包む(macOS は `sandbox-exec`、Linux は `bwrap`)。`human-token`・`http-token`・tsnet の state・`config.toml` は読めず、pekoe のデータ・設定ディレクトリやシェルの起動ファイルなど、あとで外で実行される場所には書けず、tmux のソケットにもつなげない。sandbox が使えない環境では動かさない(`[sandbox] enable = false` で明示的に外したときだけ素のまま動かす)。詳しくは [api.md](api.md) の「hooks の sandbox」 | 塞ぐのは秘密と知られた場所だけで、ホームのほかの場所(キャッシュなど)には書ける。人間が run 窓(`!`)で打ったコマンドは包まない |
| **tailnet 上の他人・他の端末** | `allow_users` / `allow_devices`。タグつきノード(サーバー・CI)は常に拒否 | tailnet の ACL で他人から `pekoe:443` への到達自体を止めておくとなお良い |
| **iPhone のブラウザで開いた悪意あるページ(CSRF・DNS rebinding)** | 書き込みにはカスタムヘッダと JSON が必須(クロスオリジンではプリフライトになり通らない)。`Origin` / `Sec-Fetch-Site` を確認。`Host` が自分の名前でなければ拒否 | — |
| **キーパッドで許可メニューを操作する** | keys は `wait_kind == question` のときだけ受け付け、許可待ち(`answerable`)では 409。状態は hook が知らせるもので、エージェントは token なしの `session.event` で AskUserQuestion の PreToolUse を偽り、本物の許可ダイアログを question に見せかけられるので、agent 窓の画面の下に許可メニュー(send と同じ判定: 「Do you want to …」と「1. Yes」)が出ていれば question でも 409。許可メニューに数字や ↓ + Enter を送れると「Yes, and don't ask again」(= always)を選べてしまうので、許可は allow / deny だけのまま。送れるキーも矢印・Tab・Space・数字・Enter・Esc に絞り、`C-c` などの修飾キーや任意の文字列は送れない(エージェントを止める・シェルに文字を流し込むといった、メニュー操作以上のことをさせない) | Claude Code は AskUserQuestion の PreToolUse の直後に permission_prompt の Notification も送る。これを question のまま扱うのは、待ちの対象(`pending_tool`)がまだ AskUserQuestion のときだけで、別のツールの PreToolUse が来ていれば permission になる。pekoe が質問と判断した直後(AskUserQuestion の PreToolUse)から、エージェントが答えを受け取るまでの間に別の許可ダイアログに切り替わると、その間に送ったキーはそちらに当たりうる。hook が状態を変えるまでの短い窓で、1 回に送るのも 1 キー。質問の文が「Do you want to …」で選択肢の 1 が「Yes…」の AskUserQuestion は許可メニューと見分けられないので、キーパッドでは答えられない(TUI で答える) |
| **許可ダイアログに send の文字や Enter が入る** | 許可メニューは最初の 1 文字を選択肢として受け取り(「2…」は「Yes, and don't ask again」= always)、Enter は選ばれている「Yes」を確定する。そこで send は、許可ダイアログが出ている間は打たない。「出ている」は、hook が知らせた許可待ち(`wait_kind` が question 以外)か、agent 窓の画面の下に Claude Code の「Do you want to …」と「1. Yes」の選択肢(または最後の行の `[y/n]`)があること。判定は 1 つ(`promptUp`)で、どこから来たかで確かめる時点が変わる: token なしのソケット(ハブのエージェント)は `submit` の有無にかかわらず、受け付けた時と打つ直前(セッションごとの送信ロックの中)の 2 回確かめて断り、先頭が数字の文字列は断る(エージェントは自分の許可ダイアログが出る時刻を決められるので、確認と打鍵の間を狙って「2」を入れられないように)。token あり(TUI・CLI・Neovim)・Web UI・Chrome 拡張(ピックやエラーの送信)は `submit` つきの送信を、打つ前と Enter の直前の 2 回確かめ、打つ前なら何も送らず、打った後なら Enter だけ送らない(文字は入力欄に残る。Web は 409)。エラー文はどれも `<slug> is waiting for a permission answer: …` で、そのあとに何が起きたか(何も送っていない / 打ったが Enter は送っていない)を続ける。Web は send の前に `answerable` でも 409 を返す | 2 回目の確認から Enter が届くまでの数ミリ秒の間にダイアログが出ると、Enter はそれを確定しうる(許可は 1 回分で、always にはならない)。token なしの送信で、打つ直前の確認から文字が届くまでの数ミリ秒にダイアログが出ると、途中の文字がメニューに入りうる(先頭の数字は断るので、先頭の 1 文字で always が選ばれることはない)。token ありの `submit` なしの送信(TUI がユーザーの打鍵として送るペースト)は止めない。画面の判定は Claude Code と `[y/n]` 形式の文言に合わせたもので、ほかのエージェントの見た目の違うダイアログは hook の状態でしか止められない |
| **受信箱の文で Web UI を乗っ取る・誘導する**(エージェントが問い・背景・選択肢・リンクに HTML やスクリプト、`javascript:` の URL を書く) | API は JSON だけを返し(`encoding/json` は `<` `>` `&` を `\u003c` などにする、`nosniff`)、ページは文字列を `textContent` で入れて HTML を組み立てない(`app.js` に `innerHTML` などの HTML を作る入口がないことをテストで確かめる)。リンクは `http` / `https` で始まり空白や `<>"\` を含まないものだけを `<a rel="noopener noreferrer">` にし、ほかは文字のまま出す。CSP(インライン script なし)はそのまま。誘導には上の「受信箱(Inbox タブ)の扱い」(事実が先、推奨を既定にしない、確認シート、`rev`) | 人が事実とリンク先を見ずに答えれば誘導は通る(TUI と同じ) |
| **Web UI から tapi に送る**(iPhone が全リポジトリへの指示の入口になる。tapi は全リポジトリを読み、`gh` などを動かし、セッションを作って任せる) | セッション 1 つへの send と重みが違うので、`allow_devices` を設定していないときは tapi への送信を断る(403)。送る前に必ず確認シート(宛先が tapi であること、文の全体)を挟む。許可待ちの間・制御文字はセッションの send と同じに断る。tapi の許可待ちへの allow / deny は Web に出さない(許可は端末の前で)。送った内容と端末を pekoed のログ(本文は 200 文字まで)と送信の記録(全文)に残す | ロックの解けた iPhone を拾った人は tapi に指示できる。tapi の権限(ファイルを書けない sandbox・許可は端末で)を超えることはできないが、tapi がセッションを作れば、そのセッションは worktree に書ける(セッションの許可待ちも Web から allow できる)。気づいたら admin console でその端末を外す |
| **端末の紛失・盗難** | 認証は端末の Tailscale の鍵なので、admin console でその端末を削除(または key を失効)すれば即座に入れなくなる。iPhone の画面ロックが実質のパスワード | ロックが解かれた iPhone を拾った人は allow / deny / send / 質問へのキー操作・受信箱への回答ができる(`allow_devices` で絞っていれば tapi への send も。上の行)。`always` は出さず、許可待ちの間は send も止めるので、恒久的な許可までは与えられない |
| **Certificate Transparency ログ** | — | HTTPS 証明書を取ると、`pekoe.<tailnet>.ts.net` という名前が CT ログ(誰でも検索できる公開ログ)に載る。tailnet 名(`tail1234` のようなランダム名か、自分で付けた名前)とホスト名が世に出る。tailnet の外からは到達できないので中身は見えないが、名前に秘密を含めない。気になるなら `hostname` を目立たないものにし、tailnet 名はランダムのものを使う |
| **Funnel での公開** | `ListenFunnel` を呼ばない。tsnet は起動時に serve 設定を空にする | — |
| **Tailscale へのログ送信** | — | tsnet は通常の tailscaled と同じく、診断ログを Tailscale のログサーバーに送る(`state_dir/tailscaled.log.conf`)。中身は接続のメタデータで、pekoe の画面やプロンプトは含まない |

## API(Web UI 用、tailnet 上の HTTPS)

| method / path | body | 返り値 |
| --- | --- | --- |
| `GET /api/me` | — | `{login, device}` |
| `GET /api/sessions` | — | `{sessions: [WebSession], at}`。待ち(古い順)→ 実行中 → その他(新しい順) |
| `GET /api/sessions/{slug}/screen` | — | `{slug, rows: [[Run]], lines: [string], dead}`。agent 窓の最後 80 行。`rows` は色つき、`lines` は同じ行のテキストだけ |
| `POST /api/sessions/{slug}/answer` | `{answer: "allow"\|"deny", tool: {name, input}\|null}` | `WebSession`。400: answer が違う / 409: 待っていない・質問・プロンプトが変わった |
| `POST /api/sessions/{slug}/send` | `{text}` | `{ok: true}`。text は前後の空白を落として 1〜4000 文字、Enter つきで送る。改行・タブ以外の制御文字を含むと 400。送る直前・Enter の直前に許可ダイアログが出ていれば 409。409: allow / deny で答える許可待ちの間 |
| `GET /api/inbox` | — | `{items: [InboxItem], at}`。未回答の項目を古い順に(`inbox.list`。事実は読み直したもの)。InboxItem は docs/api.md のまま |
| `POST /api/inbox/{id}/answer` | `{choice?: int, note?, rev}` | `InboxItem`(`inbox.answer`、`answer.via` は `web:<端末名>`)。`choice` は 0 始まり。400: `rev` がない・`choice` が範囲外・選択肢があるのに選んでいない・選択肢のない問いにメモがない・`send back` にメモがない / 404: 無い id / 409: `rev` が今のものと違う(事実が変わった)・答え済み・閉じられた |
| `GET /api/tapi` | — | `{started, alive, state, wait_kind, state_since, last_message, inbox_open, ledger_open, look, answerable, sendable}`。`answerable` は tapi が許可待ち(Web からは答えない)、`sendable` は `allow_devices` があって送れること。`pending_tool`・Claude のセッション ID・稼働ディレクトリは返さない |
| `GET /api/tapi/screen` | — | `{slug: "+tapi", rows, lines, dead}`。tapi chat の最後 80 行(セッションの `/screen` と同じ形)。409: tapi が起動していない |
| `GET /api/tapi/transcript?after=<cursor>` | — | `ChatPage`: `{conversation, cursor, items: [{kind: human\|pekoe\|agent\|tool\|turn, text?, name?, input?, question?, ms?, at}], reset?, new_conversation?, earlier?}`。上の「吹き出し(chat)と画面(screen)」。404: transcript が無い / 409: tapi が起動していない |
| `POST /api/tapi/send` | `{text}` | `{ok: true, send: <送信の記録の id>}`。`tapi.send`(`submit: true`)。text はセッションの send と同じ(1〜4000 文字、制御文字は 400)。403: `allow_devices` が無い / 409: tapi が許可待ち・画面に許可メニュー・起動していない・終了している |
| `POST /api/tapi/keys` | `{keys: ["Down", …]}` | `{ok: true}`。セッションの keys と同じキー・個数。409: tapi が質問待ちでない・許可待ち・画面に許可メニュー・起動していない |
| `GET /api/overview` | — | `{counts, groups, at}`。下の「Overview」 |
| `POST /api/sessions/{slug}/keys` | `{keys: ["Down", …]}` | `{ok: true}`。1〜8 個のキーを順に送る。キーは `Up Down Left Right Tab BTab Space Enter Escape 0`〜`9` だけ。400: 許可外のキー・個数 / 409: 質問待ち(`wait_kind == question`、`stopped` でない)以外 |

Overview: `counts` は `{running, waiting, review (done の数), open_prs, ready (根拠が揃った PR), inbox_open, ledger_open}`。`groups` は依頼元(`parent`)と依頼(`request`)ごとのまとまりで、tapi の依頼(新しい依頼が上)→ ほかのセッションが作ったもの → 人・unknown → 直接開いたもの、の順。各まとまりは `{parent, request?: {id, text (200 文字で切る), status}, rows: [Flow]}`。他人の PR のレビュー(`pr_review`)は工程表に入れない。

Flow は `{slug, repo, branch, state, state_since, look, pr, pr_look, after, report (最新の報告の status), steps, merge?}`。`steps` は `impl` `pr` `ci` `review` `merge` の 5 つで、それぞれ `{name, status: done|now|fail|todo, detail}`:

| step | done | now | fail |
| --- | --- | --- | --- |
| impl(実装) | PR がある・`pekoe land` 済み・landed | それ以外 | `failed`・エラー・最新の報告が `blocked` |
| pr | PR がある | — | PR が閉じられた |
| ci | PR のチェックがすべて成功 | 実行中・まだ読んでいない | 失敗がある |
| review | 最新の `status: review` の報告が pass(PR があれば `merge.check` の `review` が揃っている = PR の HEAD をレビューした) | pass だが PR の HEAD と違う | 最新のレビューが pass でない |
| merge | landed(PR がマージされた) | `merge.check` が ready(人のマージ待ち) | — |

`merge` は自分の PR があり landed でないセッションの `merge.check`(docs/api.md の「マージしてよいかの根拠」)で、ページは揃っていない条件を `detail` つきで出す。

WebSession は Session(docs/api.md)から表示に要るものだけを抜いたもの: `slug repo branch agent state wait_kind state_since answerable pending_tool last_tool last_message diffstat pr stopped error test look pr_look`。worktree とリポジトリのパス・環境変数・Claude のセッション ID は出さない。ツールの引数・エラー・最後のメッセージに含まれるパスはそのまま出る(許可の判断材料なので)。`answerable` は「allow / deny で答えられる待ち」(`state == waiting` かつ `wait_kind != question`)。`look` / `pr_look` は TUI の Sessions パネルと同じ記号・色の役割・言葉(`theme.Look` / `theme.PRLook`、アイコンは unicode。`{icon, role, word}`)。

Run は 1 行の中の同じ見た目の区間: `{t, fg?, bg?, b?, d?, i?, u?, s?}`(テキスト、文字色、背景色、太字・薄字・斜体・下線・取り消し線)。pekoed が `capture-pane -e` の SGR を解析して作る(`internal/termstyle`)。色は 16 色なら TUI の役割名(`err ok warn primary secondary text dim border primary-end`、反転の既定色は `bg` / `text`)で、ユーザーのテーマがそのまま効く。256 色と truecolor は `#rrggbb`。OSC 8 のリンクやカーソル移動など、色以外のエスケープと制御文字は捨てる。行ごとに既定の見た目から始め(TUI と同じ)、行末の空白は背景色があっても落とす。

画面の文字はエージェントの出力で、信用しない。Run には HTML を作らず、ページは `t` を `textContent` で入れ、色は決まった役割名のクラスか、`#rrggbb` を確かめてから CSSOM(`style.color`)で当てる。どちらでもない値は無視する。CSP(`style-src 'self'`)はそのまま。

見た目は TUI と同じ規則(docs/ui-guidelines.md)に合わせる。色は `GET /theme.css` が TUI のパレット(`[theme.colors]` の上書きを含む。`#rrggbb` 以外は捨てる)を CSS 変数で返し、`app.css` は役割名だけを使う。
