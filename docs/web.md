# Web UI(iPhone から pekoe を操作する)

Mac で pekoed を動かしたまま、外出先の iPhone(Tailscale アプリが ON)の Safari から開き、ホーム画面に追加して使う軽い Web UI。できるのは次の 8 つだけ。

- 画面は下端のタブバーで切り替える 3 つ: **Now**(人が見るべきもの・tapi の直近の発言・依頼の進み・マージの結果)、**Sessions**(状態で分けた一覧)、**tapi**(tapi chat)。下の「画面の構成」
- セッション一覧(状態で分けて。許可待ちは Now と一覧の両方にカードで)
- 許可待ちへの allow / deny(ツールの要約と、エージェントが画面に出している許可ダイアログを見せ、押す前に確認を挟む。`always` は出さない)
- `send`(短い指示を送る。写真・スクショを 4 枚まで添えられる。allow / deny で答える許可待ちの間は送れない)
- 質問(AskUserQuestion などの択一・複数選択メニュー)へのキーパッド(← ↑ ↓ → / Tab ⇧Tab Space / 1〜9 / Esc Enter。質問待ちのときだけ出る)
- エージェント画面の最新テキストを読むだけの表示(色・太字などは残す。カーソル位置や画面の再現まではしない)
- 判断の受信箱への回答(Now の `Open q<id>` から開く Inbox。pekoed の事実とリンクを先に、エージェントの文を後に出し、送る前に確認シートを挟む)
- 概要と工程表を読むだけの表示(Now の依頼の 5 工程と、そこから開く Flow。件数・tapi の状態・セッションごとの `impl → review → PR → CI → merge` と、マージしてよいかの根拠の足りない条件)
- tapi chat(統括役)の画面・質問へのキーパッド・`send`(画像も添えられる。送る前に宛先と全文の確認シート。既定は必ず挟み、`confirm_tapi_send = false` で切れる。`allow_devices` を設定しているときだけ送れる。tapi の許可待ちへの allow / deny は出さない)

賢さは pekoed にあり、Web は表示層(Now の区画分け・Sessions のグループ分けも pekoed が `GET /api/now`・`group` で決め、ページは描くだけ)。ハンドラは CLI と同じ API メソッド(`session.list` / `session.screen` / `session.answer` / `session.send` / `session.keys` / `inbox.list` / `inbox.answer` / `tapi.get` / `tapi.screen` / `tapi.send` / `tapi.keys` / `merge.check`)を同じ `Daemon.Call` 経由で呼ぶ。

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
- 静的ファイル(`internal/daemon/webui/`)は `go:embed`。外部 CDN・フォントは読まない。ページは `app.js` を `<script type="module">` で読み、ローカル GUI(docs/gui.md)と共有する部品(API の呼び出し・画面の色・自前のマークダウン・確認シート・受信箱・工程表・tapi chat の吹き出し・画像の添付)は `webui/lib/` の ES モジュールにある(bundler なし。CSP の `script-src 'self'` のまま)。PWA の manifest とアイコンつき。アイコンは `webui/icon.png`(180×180)と `webui/icon-512.png`(512×512)の PNG を埋め込んで `/icon.png`・`/icon-512.png` で配る。ブラウザのタブの favicon は別の画像で、同じ絵を tapi の周りまで詰めて縮めた `webui/favicon-32.png`・`favicon-16.png`(`/favicon-32.png`・`/favicon-16.png`)。ページと manifest が参照する URL(favicon も)には画像の sha256 の先頭 8 桁を `?v=` で付けて配信時に差し込む(`webicon.go`。iOS は URL ごとにアイコンを覚えるので、絵を替えれば URL も変わる)。版付きは `immutable` で長くキャッシュし、版なしは古いページ向けに 1 日だけ。iOS はアイコンを自分で角丸に切り抜き、透過を黒で塗るので、アルファなし・正方形いっぱいに背景が広がった絵(full-bleed)にする(`TestWebIcon`)。Service Worker と Web Push は入れない
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
# confirm_tapi_send = true           # 既定。false で tapi への send の確認シートを出さない(下の「tapi chat の扱い」)
```

- `allow_users` が空なら起動しない(誰も入れないサーバーを立てても意味がないので、エラーにして `pekoe web` に出す)
- `allow_devices` は強く勧める(下の脅威モデルを参照)
- 設定を変えたら pekoed を再起動する(`pekoe daemon restart`。エージェントは tmux の中で動き続ける)。`confirm_tapi_send` だけは再起動なしで、保存すると次の `GET /api/tapi`(数秒以内)から効く

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

#### 送信欄

セッション・tapi chat・tapi の画面(screen)の詳細画面は、上のバーと下の送信欄のあいだをパネル(端末の画面・吹き出し)が全部使う形で、送信欄はいつも画面の最下部にある。形はメッセンジャー(LINE)に寄せ、配色はほかの画面と同じ:

- 左に枠のないアイコン `＋`(画像の添付。iOS のメニューから写真・写真を撮る・ファイルを選べるので、カメラのボタンは置かない)。真ん中に角の丸い入力欄(1 行から始まり、打つと数行まで伸びる)。右に送信のアイコン(送るものが無いあいだは dim、文字か画像があれば secondary の輪)。どのアイコンも当たり判定は 44px
- 入力中(入力欄にフォーカスがあり文字がある)は左の `＋` が `›` に畳まれる(LINE と同じ)。`›` を押すと戻る(キーボードは出たまま。次に打つとまた畳まれる)
- 宛先の行・送れない理由・添付のサムネイルは入力欄の上に出る(これまでどおり)。セッションと tapi の画面(screen)では、質問のカードやキーパッドが高くて収まらないときは送信欄の上の部分がスクロールする(tapi chat はログが縮み、キーパッドはログの中に出る)
- 高さ: 下端は iPhone のホームインジケーターの分(safe area)だけ空け、ソフトウェアキーボードが出たらキーボードのすぐ上に置く。iOS はキーボードで layout viewport も `100dvh` も縮めない(visual viewport だけが縮んで動く)ので、ページの高さと位置は `visualViewport` から app.js が決める(`fitViewport`)。ホーム画面に追加したアプリ(standalone)では、一度キーボードを出した後などに `innerHeight`・`visualViewport.height`・`100dvh` がそろってステータスバーの分(約 59px)短くなる WebKit の不具合があり、そのとき画面の下の約 59px はページが描かれない帯になる(システムが描き、送信欄を置いても見えない)。ページは報告された高さに収め(キーボードが出ているときは `visualViewport` の高さ)、下が切られているあいだはホームインジケーターの分を空けない。入力欄からフォーカスが外れたとき、高さが短いままならページを一度レイアウトから外して戻し、WebKit に高さを測り直させる(`healViewport`)。Safari のタブでは `innerHeight` と `visualViewport` をそのまま使う

### 画像の扱い

外出先で見つけた画面の不具合やデザインの参考を、写真やスクショのままエージェントに渡す。セッションと tapi chat の送信欄の左の `＋` で選ぶ(iOS の写真・カメラ・ファイル)。

- **ページ**: 選んだ画像を読み込み、canvas に描き直して送る。長い辺は 2048px まで縮め、PNG は PNG のまま(スクショの文字をつぶさない)、それ以外(HEIC・WebP など)は JPEG にする。HEIC はエージェントが読めないので、ここで JPEG になる。描き直すので、位置情報などの写真のメタデータは端末から出ない。1 回の send に 4 枚まで、1 枚 5 MB まで(それを超える PNG は JPEG に、それでも超えれば画質を下げる)。選んだ画像はサムネイルで送信欄の上に並び、`×` で外せる。下書きと同じく宛先ごとに持つので、別のセッションに紛れない
- **確認シート**: 画像つきの send は、セッション宛てでも送る前にシートを挟む(宛先・文の全体・サムネイル)。tapi 宛ては今までのシートにサムネイルが加わる(`confirm_tapi_send = false` ならシートは出ない)。画像のないセッションへの send は今までどおりシートなし
- **送るもの**: `POST …/send` の本文 `{text, images: [{data}]}`(`data` は base64)。文は画像があれば空でよい。この 2 つの send だけ本文の上限が 28 MB 程度(画像 4 枚分)で、ほかの書き込みは 64 KB のまま
- **pekoed の確認**: 中身の先頭のバイト(マジックナンバー)で PNG・JPEG・GIF だけを受け付け(Claude Code が読めるもの)、Go の `image.DecodeConfig` でヘッダが読めることと、縦横 8000px 以下・5 MB 以下・4 枚以下を確かめる。送られたファイル名や種類は受け取らない(本文に無い)。画像でないもの(HEIC・PDF・テキストなど)は 415、大きすぎる・多すぎるものは 400(本文全体が上限を超えれば 413)。どれも send の確認(制御文字・許可待ち・tapi の `allow_devices`)と同じく、保存も打鍵もしない
- **保存**: 許可待ちなどの確認を通った後で、`<データディレクトリ>/images/<slug>/`(tapi chat は `images/+tapi/`。セッションの slug に `+` は無い)に pekoed が名前を付けて書く(`20261120-101530-<乱数>-1.png`。拡張子は中身から。0600、ディレクトリは 0700)。worktree の中には置かない(コミットに紛れない)
- **打つ文**: 文の後に空行を 1 つ、画像ごとに `[image] <保存先の絶対パス>` の行を足して送る。エージェントはそのパスを Read で開いて画像として見る。Claude Code のエージェントには、自分の `images/<slug>/` を確認なしで Read できる許可(`permissions.allow` の `Read(//…/images/<slug>/**)`)を sandbox の設定(`--settings`)に入れる。Bash の sandbox でも読める(`denyRead` に入らない)。token・`state.db`・tsnet の state・設定ディレクトリは今までどおり読めない。設定は起動時に書くので、この版より前に起動したエージェントは、再起動するまで Read の許可を聞いてくることがある
- **記録**: 送信の記録(`send.log`)には打った文をそのまま(画像の行とパスつきで)残す。tapi への send を許可待ちなどで断ったとき(画像は保存しない)と、画像を保存できなかったとき(500。セッションへの send も)は、記録の文に `[image] ×N (not saved)` で枚数だけを残す。pekoed のログには `web: … sends … with 2 image(s) <パス> <パス>` のように件数とパスを出す
- **片付け**: セッションを `rm` すると `images/<slug>/` ごと消す。それ以外(tapi chat のものを含む)は 7 日たったら消す(pekoed が起動して 20 秒後と、その後 1 時間ごとに見る。空になったディレクトリも消す。保存と重なってディレクトリが消えたら、保存の側が作り直す)。打ったが Enter を送れなかった(許可ダイアログが間に出た)send の画像は入力欄にパスが残っているので消さず、それ以外の失敗では消す
- **やらないこと**: 画像以外のファイル(PDF・動画など)、TUI からの画像の送信、保存した画像を Web UI に返すこと(サムネイルはページが端末の中で作る)

### 画面の構成

ダークだけ(ライトモードは無い)。色はトークン(docs/ui-guidelines.md)の役割名だけで、バーの地は `chrome`、選んだチップと Chat / Screen の選択中は `raised`。フォーカスの表現は 2 つだけ: タブバーの選択(primary の文字と上辺 2px の線)と、入力欄の枠(primary → primary_end のグラデーション)。パネルの枠・`◆` の見出し・`pekoe` のピルは無い。

書体は system-ui(Mac・iPhone ではラテン文字が SF Pro、日本語がヒラギノ)の 1 系統で、数字は tabular figures。モックの Inter / Noto Sans JP は使わない: CSP(`webCSP`)は外のフォントもスタイルシートも断り、「外部 CDN・フォントは読まない」方針で、同梱すると Web UI 入りのビルドが日本語フォントの分だけ大きくなるため(docs/gui-design.md の決まったこと 2 と同じ判断)。等幅はエージェントの画面・実行するコマンド・吹き出しの中のコード・tapi の顔だけ。

- **上端のバー**(高さ 52px): 左に画面名(Now は `pekoe`)、右に鮮度(`◉ live · 18:47`。最後に読めたのが 10 秒より前なら warn の `stale`)。Inbox・Flow・セッションを開いた画面は左に `‹`。tapi では顔・`tapi`・状態と鮮度(`◦ idle · updated 2s ago`)・`Chat` / `Screen` の切り替え(枠の中のセグメント)
- **タブバー**(高さ 56px、下にホームインジケーターの分): Now / Sessions / tapi。Sessions のラベルには確認待ち(done)の件数を secondary で添える。セッションを開いた画面・tapi でも出し、キーボードが出ている間だけ隠す
- **Now**(`#/`): 上から 4 区画
  - NEEDS YOU: 対応が要るものをカードで(pekoed のマージが通らなかったもの・許可や質問で待っているセッション・受信箱の未回答)。`Open q<id>` は Inbox の該当カードへ。0 でない件数は 1 行のカード(`✓ 7 sessions to review ›` は Sessions の To review へ)、0 の件数は dim の 1 行(`0 questions · 0 ready to merge · 0 running · 0 waiting`)
  - TAPI: 会話一覧の 1 行(顔・状態・時刻・直近の発言を dim で 2 行まで)。タップで tapi タブ
  - REQUESTS: 右に台帳の open の件数。依頼ごとのカード(ブランチと状態・経過、依頼の ID と本文、5 工程の細いバーと語)。直近 2 件、残りは `N more requests`。下の `Every session's flow ›` で今までの工程表(Flow)
  - MERGED BY PEKOED: 通ったマージを 1 行に(`↣ #259 · #258`)。タップで Inbox のマージの履歴へ
- **Sessions**(`#/sessions`、`#/sessions/review|active|landed`): 上にフィルタのチップ(`All` / `To review` / `Active` / `Landed`、横スクロール)、一覧は ACTIVE(稼働中・許可待ちなど)→ TO REVIEW → IDLE → LANDED。LANDED は直近 2 件、残りは `Show N more landed`、行全体を dim。行は高さ 52px 以上で、左に状態の記号、中央に 2 行(ブランチ名 / リポジトリ名。リポジトリ本体のチェックアウトで動くセッションはリポジトリ名 / `main`)、右に状態語と経過。2 行目に diff の増減・テスト・PR を続ける
- **tapi**(`#/tapi`): 下の「tapi chat の扱い」。吹き出しは tapi が左(surface、左下だけ角が小さい)、自分が右(`bubble_me`、右下だけ角が小さい)、幅は 78% まで、時刻は吹き出しの外の下(自分の発言は `✓ 18:45`)。会話が短いときは下に寄せる。入力欄はタブバーの上に固定(左に添付の `+`、丸い入力欄 `Ask tapi`、select の地の送信ボタン)
- **Dismiss は無い**: NEEDS YOU の「PR #n was not merged」を閉じる API は今は無いので、ボタンは出さない(マージの履歴は 24 時間で Now から消える)
- まだ作り直していない画面(tapi の Screen、セッションを開いた画面、Inbox、Flow)は前のまま残し、新しいナビから辿れるようにしてある

#### 幅の広い画面(iPad の横向き)

同じページのまま、幅が `--wide-min`(既定 700px)以上なら配置だけを替える。端末の種類(User-Agent)では分けない(iPad の Safari は Mac を名乗る)。iPad を縦にしたり Split View で狭くしたりすれば、幅に従って上の iPhone の配置に戻る。部品は iPhone と同じもので、並べ方だけが違う。境目の幅・レールの幅(`--rail-w`、76px)・一覧の幅(`--list-w`、390px)は app.css の `:root` の 3 つの変数で変える(app.js は `--wide-min` を読んで media query にする)。

- **レール**(左端、幅 `--rail-w`、chrome の地): タブバーを縦にしたもの。`Now` と `Sessions`(確認待ちの件数を secondary で添える)の 2 つで、高さ 60px。選択中は primary の文字と左辺 2px の線。tapi の項目は置かない(チャットは Sessions に常に出ている)。下端のタブバーは出さない
- **Now**(`#/`): 3 列の格子(列の間と外側は 20px、画面全体で 1 つのスクロール)。左が NEEDS YOU、中央が REQUESTS、右が TAPI(直近の発言を 3 行まで)と MERGED BY PEKOED。iPhone と違うのは 2 つだけ: 0 の件数は 2×2 の小さいタイル(枠線だけ、地は塗らない、文字は dim)、通ったマージは 1 件 1 行(`↣`・PR 番号・ブランチ名(dim、あふれたら省略)・経過、タップで Inbox のマージの履歴へ)
- **Sessions + チャット**(`#/sessions…`・`#/tapi`・`#/s/<slug>`): レールの右を 2 列に分ける。左は Sessions の一覧(幅 `--list-w`。境目の近くでは、右に半分以上が残るようにレールの右の 45% まで縮む)で、先頭に tapi の行を固定する(顔・`tapi`・状態・時刻・直近の発言 1 行)。選んだ行は raised の地と左辺 2px の primary の線。右は選んだ相手の画面で、`#/sessions` では tapi のチャット。セッションの行を選ぶと、右がそのセッションの画面(上の「セッションを開いた画面」をそのまま)に替わる。フィルタのチップは一覧を絞るだけで、右の選択は変えない(選んでいたセッションが絞り込みで一覧から消えても、右はそのまま)。上端のバーも 2 つに分かれ、左半分が `Sessions` と鮮度、右半分が tapi の顔・状態・`Chat` / `Screen`(セッションのときはその slug)。Now の TAPI の行・`N sessions to review`・依頼のカードはここへ移る
- 入力欄の高さの扱い(`fitViewport` / `shortBelow` / `healViewport`、`kb` / `cut`)は iPhone と同じ。キーボードが出ている間もレールは隠さず、右の列の入力欄がキーボードのすぐ上に来る。ホームインジケーターの分は、キーボードが出ているときと下が切られているときを除いて、ページの下に空ける

### 受信箱(Inbox)の扱い

docs/orchestration.md の「受信箱で人を誘導する」への対策を、TUI のモーダルと同じ並びで Web にも当てる。

- 1 件のカードは上から、pekoed が読んだ事実(`facts`: セッション・状態・ブランチ・HEAD・差分・PR と CI・レビューの判定・`merge_state`・テスト・衝突予報・最新の報告・マージしてよいかの条件)とリンク → エージェントの文(`from`、名乗りが違えば警告、問い・背景・選択肢)の順に出す
- 推奨は選択肢の横に `recommended (agent)` と書くだけで、選んだ状態にしない。選択肢を選ぶか、メモを書くまで `answer…` を押せない。選択肢があっても、どれも選ばずメモだけで答えられる(選択肢のどれでもない。もう一度タップすると選択が外れる)。pekoed の根拠の項目(merge / wait / send back)だけは選ばないと押せない(pekoed もメモだけの答えを断る)
- `answer…` で確認シートを開き、宛先(問いを出したエージェント)・問い・選んだもの(メモだけなら `none of the options (the note alone)`)・メモを並べてから送る
- 送るのは、カードを描いたときの `rev`。pekoed は `inbox.answer` の中で事実を読み直して比べるので、見た後に HEAD が動いた・CI が落ちた・別の端末で答えた項目は 409 になり、ページは選択を外して「changed while you looked」と出す。3 秒ごとの更新で `rev` が上がったときも、選択を外して同じ表示にする
- 答えは `answer.by: human`・`answer.via: web:<端末名>` で残り、誰がどの端末で何番を選んだかを pekoed のログに出す
- 答えずに閉じる(`dismiss`)・取り下げは Web には出さない(TUI か CLI で)
- tapi の許可待ちへの allow / deny は出さない。Now の TAPI の行が tapi の状態・最後の返事を読み、タップ(またはタブバーの tapi)で tapi chat の画面を開く(次の「tapi chat の扱い」)。`pending_tool`・Claude のセッション ID・稼働ディレクトリは返さない

### tapi chat の扱い

tapi は全リポジトリを読み、`pekoe new` でセッションを作って任せる。tapi への send はセッション 1 つへの send より重い(iPhone が全リポジトリへの指示の入口になる)ので、セッションの send の確認に次を足す。

- **`allow_devices` が要る**: `[web] allow_devices` を設定していないと `POST /api/tapi/send` は 403(`allow_users` の誰かの別の端末から tapi に指示させない)。ページは送信欄に理由を出して send を押せなくする。画面とキーパッドは指示にならないので、`allow_devices` が無くても出す
- **確認シート**: 既定は send を押すと必ずシートを挟む(`[web] confirm_tapi_send = false` で切れる。下の段落)。宛先が tapi(統括役、全リポジトリ)であることと、打たれる文の全体(文字数・行数つき、シートの中でスクロール)を見せ、`send to tapi` を押したときだけ送る。宛先はこのシートで見せるので、tapi が動いている間は送信欄の上に宛先の行を出さない(その分をログに回す)。tapi が起動していない・終了しているときだけ、送れない理由として「tapi is not started / has exited」の行を出す
- **確認シートを切る**: `[web] confirm_tapi_send = false` にすると、send を押したらシートを挟まずにそのまま送る(送信中は send を押せない。送った文は同じく `●` → `✓`)。ほかの守り(`allow_devices` が無ければ 403、許可待ち・制御文字は断る、送信の記録と pekoed のログ、allow / deny を出さない)は変わらない。切ると、誤送信(押し間違い・書きかけの送信)と、ロックの解けた端末を拾われたときの守りが `allow_devices` と端末の画面ロックだけになる。設定の保存で再起動なしに切り替わる(ページは `GET /api/tapi` の `confirm_send` を見る)。セッションへの send・許可の allow / deny・受信箱の確認シートはこの設定で変わらない
- **確認はセッションと同じ**: 本文は前後の空白を落として 1〜4000 文字、改行・タブ以外の制御文字は 400。tapi が許可待ち(hook の状態)なら 409。`tapi.send` が打つ前と Enter の直前に hook の状態と画面の許可メニュー(`promptUp`)を見直し、出ていれば 409。tapi が起動していない・終了しているときも 409
- **記録**: 送ったものと、断ったもの(許可待ち・画面の許可メニューの 409。400・`allow_devices` の 403・tapi が起動していない・終了しているときの 409 は除く)を送信の記録(`send.log`)に `from: human`・`to: +tapi`・`kind: send`・`via: web:<端末名>` で残す。pekoed のログには誰が・どの端末から・送信の記録の何番か・本文の最初の 200 文字を出す(全文は送信の記録。ログは同じユーザーのエージェントから読めるので切る)。`allow_devices` で断ったものはログにだけ出る
- **キーパッド**: tapi が質問待ちのときだけ、セッションと同じキー(`Up Down Left Right Tab BTab Space Enter Escape 0`〜`9`、1 回 8 個まで)。許可待ちのときと、画面に許可メニューが出ているとき(hook は偽れるので画面で見る)は 409
- **allow / deny は無い**: tapi の許可待ちに答える Web の API は無い(`/api/tapi/` に answer は無く、`/api/sessions/+tapi/…` は `session.*` が `+tapi` を拒むので届かない。ソケットの `tapi.answer` は TUI の許可モーダル用で、Web からは呼ばない)。許可待ちの間、ページは「端末(pekoe TUI)で答える」とだけ出し、send とキーも止める

#### 吹き出し(chat)と画面(screen)

tapi chat のパネルは既定で会話を LINE のような吹き出しで出す。タイトルの右の `chat | screen` で、今までの端末の画面(pause つき)に切り替えられる(選んだ方は端末のブラウザに残る)。

- **データ**: 会話は画面ではなく Claude Code の transcript から取る。tapi の hook が渡す `transcript_path` を pekoed が覚え(`tapi.transcript`、人間だけ)、ページは `GET /api/tapi/transcript?after=<cursor>` で 2 秒ごとに差分を取る。覚えるのは tapi の Claude Code のプロジェクトディレクトリ(`~/.claude/projects/<tapi の cwd>/`)の中の `<id>.jsonl` だけ。`session.event` は token なしで誰でも `+tapi` を名乗れるので、ほかのパスは読まない。pekoed を再起動して hook がまだ来ていないときは、同じディレクトリのいちばん新しい transcript を読む
- **返すもの**: 人のテキスト・tapi のテキスト・ツールの名前と主な引数 1 つ(許可待ちの要約と同じく 200 バイトで切る。AskUserQuestion は問いと選択肢の見出し)・ターンの所要時間と時刻だけ。tool_result(`type: user` の行に入っているものも)・thinking・システムの注入(system-reminder・compact の要約・スラッシュコマンドとその出力・`!` のコマンド・task-notification など)・サブエージェントの行は返さない。pekoe が打った行(`[pekoe inbox q3] …`)は人の発言と分けて返し、ページは dim の 1 行で出す。複数行の send(画像つきの send は `[image]` の行があるので必ず複数行)は Claude Code に貼り付けとして届き、transcript では `<pasted_content id="…">…</pasted_content id="…">` に包まれる。人の発言の全体が 1 つのこの包みなら包みだけを外して返す(中身はそのまま。打った文の途中にある貼り付けや、包みの一部は触らない)
- **差分**: カーソルは `<会話の id>:<読んだバイト数>`。書きかけの行は次に回す。最初の読み込みは transcript の最後の 6 MB・400 件まで(それより前は `earlier`。そのとき `before` に前のページを読むカーソルが付き、`?before=<それ>` でその前の 6 MB・400 件を返す。先頭まで読めば `before` は無い。iPhone の Web UI は前のページを読まない。ローカル GUI は `Load earlier` で読む)。`/clear` などで hook が別のファイルを渡すと会話が変わったとみなし、ページは `new conversation` の区切りを入れて続ける
- **描画**: エージェントの文は信用しない(受信箱と同じ)。`textContent` と自前の最小のマークダウン(見出し・リスト・チェックボックス・インラインコード・太字・コードブロック)だけで組み、リンクは張らない。太字は 1 行の中の `**…**`(`**` の内側の端が空白でないもの)で、`strong` 要素に `textContent` で入れる。インラインコード・コードブロックの中の `**` と、閉じていない `**` は文字のまま。15 行を超える返事は畳んで `more › +N lines`。ツール呼び出しは吹き出しの間の dim の 1 行(`› 4 tools · Read docs/x.md · …`)で、タップで名前と引数の一覧を開く(結果の中身は出さない)
- **状態**: run 中は左に Braille のスピナーと `thinking · 12s`、許可待ちはペインの中のカード(端末で答える)、質問は tapi の吹き出しの下にキーパッドのカード。送信の確認シートはそのまま(`confirm_tapi_send = false` なら挟まない)。送った文は transcript に現れるまで `●`(secondary)、現れたら `✓`(送った文と transcript の文は空白の違いを無視して突き合わせ、同じ発言を 2 つ出さない)
- **レイアウト**: ログは上のバーと送信欄の間を全部使う(固定の高さを差し引かない)。送信欄の形と高さは上の「送信欄」(セッションの詳細画面と同じ)。いちばん下を見ているときは、送信欄が伸びた・許可待ちの理由が出た・キーボードが出たときも最新の吹き出しが見えたままになる
- **busy の間の発言**: tapi が返事を作っている(thinking・ツール実行中)間に打った文は、素の Claude Code と同じくキューに入り、ターンの途中(次のツール結果と一緒)か、ターンが終わった後に届く。Web UI の send(`tapi.send`)も TUI からの入力も、文を打って Enter を押すだけで、interrupt(Esc・Ctrl+C)は送らないので同じ動きになる。途中で取り込まれた文は transcript に人の行(`type: user`)としては残らず、`queued_command` の添付(`origin.kind == human`・`commandMode == prompt`)としてだけ残るので、それも人の発言として返す(task-notification や別のエージェントからのもの(`peer`)は返さない)
- **取り消された発言**: 人が返事の最初の 1 語より前に interrupt(Ctrl+C・Esc)したプロンプトは、会話から外れ、Claude Code の画面からも消える。transcript には行が残り、次に送ったプロンプトが同じ親(`parentUuid`)のもう 1 つの子として書かれる。pekoed は人の行と pekoe の行に `id`(`uuid`)と `parent`(`parentUuid`)を付けて返し、ページは同じ親を持つ発言のうち最後のもの以外を取り消されたとみなして吹き出しに出さない(`markRewound`。後の発言は次の差分で来ることがあるので、ページが持っている全部に毎回かける)。取り消した後にまだ何も送っていない間は見分けられないので、次に送るまでは吹き出しに残る。返事が始まってからの interrupt は `[Request interrupted by user]` の行が続き、プロンプトは会話に残る(Claude Code の画面にも残る)ので、取り消しとは扱わない
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
| **エージェントが pekoe の API(unix ソケット)で許可待ちに答える** | ソケットは 0600 だが、同じユーザーのエージェントはつなげる。そこで token なしで受け付けるのはエージェントに要るメソッドだけにし(許可リスト。どれを通すかと理由は [api.md](api.md) の「人間だけの操作」の表。`land`・自分のセッションの `dev.toggle` のように pekoed にテストや dev server を動かさせるものも入る)、許可待ちへの回答・生キー・tapi chat への入力・設定の書き込み・他のセッションの画面や抜粋・デーモンの停止を含むほかのすべては、`human-token`(データディレクトリの 0600 のファイル)を付けた呼び出しだけ受け付ける。TUI・CLI・Neovim はファイルを読んで付ける。エージェントのペインの環境には token もそのパスも渡さない。`session.send` はハブのために token なしでも使えるが、エージェントの入力欄だけに限り、先頭(空白を除く)が `!` の文は断る(空の入力欄の `!` は Claude Code のシェルモードで、残りが許可ダイアログなしにコマンドとして動く。token なしの `session.new` の `prompt` も同じ。人の send・Web UI・ローカル GUI は `!` を通す。許可ダイアログが出ている間の扱いは次の行) | 同じユーザーのエージェントは、Bash でファイルを読めば token を手に入れられる。Claude Code のエージェントには pekoe が sandbox の設定(`--settings`: sandbox を有効、`human-token`・`http-token`・tsnet の state・設定ディレクトリを `denyRead` と `Read` / `Edit` の deny、pekoed のソケットだけ許す)を渡すので読めない。ほかのエージェント、`[agents.claude] sandbox = false`、`~/.claude/settings.json` の `excludedCommands` に挙げたコマンド(sandbox の外で動く)は自分で塞ぐ(README の「エージェントに pekoe を使わせるときの権限」)。token なしの `session.new` は既知のリポジトリに限る(エージェントが自作したリポジトリで pekoed に git を動かさせない)。また `Bash(pekoe *)` のように pekoe を丸ごと許可しない。許可するなら `pekoe ls` / `pekoe --json ls` / `pekoe --json new …` / `pekoe send …` に絞る。状態はエージェントの hook(`pekoe event`)が報告するので、`pekoe event` を叩けるエージェントは状態を偽って、send の確認のうち hook の状態を見る方をすり抜けうる(画面の判定は残る。`pekoe event` も許可しない) |
| **エージェントが書いたコードを pekoed に実行させて token を読む** | pekoed が worktree のコードを実行するもの(Stop のたびの `[hooks] test`、門番、`setup`、`dev`、`teardown`、`land` のテスト)は sandbox で包む(macOS は `sandbox-exec`、Linux は `bwrap`)。`human-token`・`http-token`・tsnet の state・`config.toml`・`state.db` は読めず、pekoe のデータ・設定ディレクトリやシェルの起動ファイル・`~/.claude.json`・`~/.claude` の skills / hooks / plugins など、あとで外で実行される場所には書けず、tmux とレビュー面の nvim のソケットにもつなげない。sandbox が使えない環境では動かさない(`[sandbox] enable = false` で明示的に外したときだけ素のまま動かす)。詳しくは [api.md](api.md) の「hooks の sandbox」 | 塞ぐのは秘密と知られた場所だけで、ホームのほかの場所(キャッシュなど)には書ける。リポジトリ本体の `.git/hooks`・`.git/config` にも書ける(husky などのため。変わったら pekoed が受信箱に出す。api.md の「本体の .git の見張り」)。Docker・ssh-agent などの unix ソケットにはつながる(テストが使う)。人間が run 窓(`!`)で打ったコマンドは包まない |
| **tailnet 上の他人・他の端末** | `allow_users` / `allow_devices`。タグつきノード(サーバー・CI)は常に拒否 | tailnet の ACL で他人から `pekoe:443` への到達自体を止めておくとなお良い |
| **iPhone のブラウザで開いた悪意あるページ(CSRF・DNS rebinding)** | 書き込みにはカスタムヘッダと JSON が必須(クロスオリジンではプリフライトになり通らない)。`Origin` / `Sec-Fetch-Site` を確認。`Host` が自分の名前でなければ拒否 | — |
| **キーパッドで許可メニューを操作する** | keys は `wait_kind == question` のときだけ受け付け、許可待ち(`answerable`)では 409。状態は hook が知らせるもので、エージェントは token なしの `session.event` で AskUserQuestion の PreToolUse を偽り、本物の許可ダイアログを question に見せかけられるので、agent 窓の画面の下に許可メニュー(send と同じ判定: 「Do you want to …」と「1. Yes」)が出ていれば question でも 409。許可メニューに数字や ↓ + Enter を送れると「Yes, and don't ask again」(= always)を選べてしまうので、許可は allow / deny だけのまま。送れるキーも矢印・Tab・Space・数字・Enter・Esc に絞り、`C-c` などの修飾キーや任意の文字列は送れない(エージェントを止める・シェルに文字を流し込むといった、メニュー操作以上のことをさせない) | Claude Code は AskUserQuestion の PreToolUse の直後に permission_prompt の Notification も送る。これを question のまま扱うのは、待ちの対象(`pending_tool`)がまだ AskUserQuestion のときだけで、別のツールの PreToolUse が来ていれば permission になる。pekoe が質問と判断した直後(AskUserQuestion の PreToolUse)から、エージェントが答えを受け取るまでの間に別の許可ダイアログに切り替わると、その間に送ったキーはそちらに当たりうる。hook が状態を変えるまでの短い窓で、1 回に送るのも 1 キー。質問の文が「Do you want to …」で選択肢の 1 が「Yes…」の AskUserQuestion は許可メニューと見分けられないので、キーパッドでは答えられない(TUI で答える) |
| **許可ダイアログに send の文字や Enter が入る** | 許可メニューは最初の 1 文字を選択肢として受け取り(「2…」は「Yes, and don't ask again」= always)、Enter は選ばれている「Yes」を確定する。そこで send は、許可ダイアログが出ている間は打たない。「出ている」は、hook が知らせた許可待ち(`wait_kind` が question 以外)か、agent 窓の画面の下に Claude Code の「Do you want to …」と「1. Yes」の選択肢(または最後の行の `[y/n]`)があること。判定は 1 つ(`promptUp`)で、どこから来たかで確かめる時点が変わる: token なしのソケット(ハブのエージェント)は `submit` の有無にかかわらず、受け付けた時と打つ直前(セッションごとの送信ロックの中)の 2 回確かめて断り、先頭が数字の文字列は断る(エージェントは自分の許可ダイアログが出る時刻を決められるので、確認と打鍵の間を狙って「2」を入れられないように)。token あり(TUI・CLI・Neovim)・Web UI・Chrome 拡張(ピックやエラーの送信)は `submit` つきの送信を、打つ前と Enter の直前の 2 回確かめ、打つ前なら何も送らず、打った後なら Enter だけ送らない(文字は入力欄に残る。Web は 409)。エラー文はどれも `<slug> is waiting for a permission answer: …` で、そのあとに何が起きたか(何も送っていない / 打ったが Enter は送っていない)を続ける。Web は send の前に `answerable` でも 409 を返す | 2 回目の確認から Enter が届くまでの数ミリ秒の間にダイアログが出ると、Enter はそれを確定しうる(許可は 1 回分で、always にはならない)。token なしの送信で、打つ直前の確認から文字が届くまでの数ミリ秒にダイアログが出ると、途中の文字がメニューに入りうる(先頭の数字は断るので、先頭の 1 文字で always が選ばれることはない)。token ありの `submit` なしの送信(TUI がユーザーの打鍵として送るペースト)は止めない。画面の判定は Claude Code と `[y/n]` 形式の文言に合わせたもので、ほかのエージェントの見た目の違うダイアログは hook の状態でしか止められない |
| **受信箱の文で Web UI を乗っ取る・誘導する**(エージェントが問い・背景・選択肢・リンクに HTML やスクリプト、`javascript:` の URL を書く) | API は JSON だけを返し(`encoding/json` は `<` `>` `&` を `\u003c` などにする、`nosniff`)、ページは文字列を `textContent` で入れて HTML を組み立てない(`webui/` の下のすべての JS(`app.js`・`lib/`・ローカル GUI の `gui/`)に `innerHTML` などの HTML を作る入口がないことをテストで確かめる)。リンクは `http` / `https` で始まり空白や `<>"\` を含まないものだけを `<a rel="noopener noreferrer">` にし、ほかは文字のまま出す。CSP(インライン script なし)はそのまま。誘導には上の「受信箱(Inbox)の扱い」(事実が先、推奨を既定にしない、確認シート、`rev`) | 人が事実とリンク先を見ずに答えれば誘導は通る(TUI と同じ) |
| **Web UI から tapi に送る**(iPhone が全リポジトリへの指示の入口になる。tapi は全リポジトリを読み、`gh` などを動かし、セッションを作って任せる) | セッション 1 つへの send と重みが違うので、`allow_devices` を設定していないときは tapi への送信を断る(403)。送る前に確認シート(宛先が tapi であること、文の全体)を挟む(宛先を見せるのはこのシート。送信欄の上の宛先の行は tapi が動いていないときだけ出す)。既定は必ず挟む。`confirm_tapi_send = false` で切れる。切ると誤送信・端末を拾われたときの守りが `allow_devices` と端末のロックだけになる。許可待ちの間・制御文字はセッションの send と同じに断る。tapi の許可待ちへの allow / deny は Web に出さない(許可は端末の前で)。送った内容と端末を pekoed のログ(本文は 200 文字まで)と送信の記録(全文)に残す | ロックの解けた iPhone を拾った人は tapi に指示できる。tapi の権限(ファイルを書けない sandbox・許可は端末で)を超えることはできないが、tapi がセッションを作れば、そのセッションは worktree に書ける(セッションの許可待ちも Web から allow できる)。気づいたら admin console でその端末を外す |
| **Web UI から画像を送る**(画像の中身・形式・置き場所が新しい入口になる) | 受け付けるのは PNG・JPEG・GIF だけで、拡張子や MIME ではなく中身の先頭のバイトとヘッダの読み込みで確かめる。ファイル名と拡張子は pekoed が中身から付け、送られた名前は使わない(パスの細工・上書きができない。`O_EXCL` で作る)。5 MB・8000px・4 枚の上限と、send だけ大きい本文の上限(ほかの書き込みは 64 KB のまま)。保存先はデータディレクトリの `images/<slug>/`(0600)で、worktree の外(コミットに紛れない)。保存した画像は Web からは読めない(返す API が無い)。ページは画像を描き直して送るので、写真の位置情報は端末から出ない。CSRF・許可待ち・tapi の `allow_devices` と確認シートは文の send と同じ(シートには画像も出す)。`rm` と 7 日で消す | 画像に写った文字はエージェントにとって指示と区別できない(人が送るものなので、Web ページのスクショなど他人の文が写ったものを送るときは、その文にエージェントが従いうる)。保存した画像は同じユーザーのどのエージェントからも読める(Read の確認なしで開けるのは自分の分だけだが、Bash では読める。ピックやエラーの添付と同じ)ので、秘密の写ったスクショは送らない。ページを通さずに API を叩けば(許可された端末から)、JPEG のメタデータは pekoed がそのまま保存する。画像のデコーダ(Claude Code 側)の不具合を突くファイルは、ヘッダの確認だけでは防げない(送れるのは許可された端末の人だけ) |
| **端末の紛失・盗難** | 認証は端末の Tailscale の鍵なので、admin console でその端末を削除(または key を失効)すれば即座に入れなくなる。iPhone の画面ロックが実質のパスワード | ロックが解かれた iPhone を拾った人は allow / deny / send(画像つきを含む)/ 質問へのキー操作・受信箱への回答ができる(`allow_devices` で絞っていれば tapi への send も。上の行)。`always` は出さず、許可待ちの間は send も止めるので、恒久的な許可までは与えられない |
| **Certificate Transparency ログ** | — | HTTPS 証明書を取ると、`pekoe.<tailnet>.ts.net` という名前が CT ログ(誰でも検索できる公開ログ)に載る。tailnet 名(`tail1234` のようなランダム名か、自分で付けた名前)とホスト名が世に出る。tailnet の外からは到達できないので中身は見えないが、名前に秘密を含めない。気になるなら `hostname` を目立たないものにし、tailnet 名はランダムのものを使う |
| **Funnel での公開** | `ListenFunnel` を呼ばない。tsnet は起動時に serve 設定を空にする | — |
| **Tailscale へのログ送信** | — | tsnet は通常の tailscaled と同じく、診断ログを Tailscale のログサーバーに送る(`state_dir/tailscaled.log.conf`)。中身は接続のメタデータで、pekoe の画面やプロンプトは含まない |

## API(Web UI 用、tailnet 上の HTTPS)

| method / path | body | 返り値 |
| --- | --- | --- |
| `GET /api/me` | — | `{login, device}` |
| `GET /api/sessions` | — | `{sessions: [WebSession], at}`。待ち(古い順)→ 実行中(creating・queued を含む)→ done・failed → そのほか(idle など)→ landed。待ち以外はそれぞれの中で新しい順 |
| `GET /api/sessions/{slug}/screen` | — | `{slug, rows: [[Run]], lines: [string], dead}`。agent 窓の最後 80 行。`rows` は色つき、`lines` は同じ行のテキストだけ |
| `POST /api/sessions/{slug}/answer` | `{answer: "allow"\|"deny", tool: {name, input}\|null}` | `WebSession`。400: answer が違う / 409: 待っていない・質問・プロンプトが変わった |
| `POST /api/sessions/{slug}/send` | `{text, images?: [{data}]}` | `{ok: true, images: <枚数>}`。text は前後の空白を落として 1〜4000 文字(画像があれば空でよい)、Enter つきで送る。`images` は base64 の PNG・JPEG・GIF を 4 枚まで、1 枚 5 MB・8000px まで。pekoed が保存し、文の後に `[image] <パス>` の行を足して打つ(上の「画像の扱い」)。画像でないものは 415、上限を超えると 400(本文全体が上限を超えれば 413)。改行・タブ以外の制御文字を含むと 400。送る直前・Enter の直前に許可ダイアログが出ていれば 409。409: allow / deny で答える許可待ちの間 |
| `GET /api/inbox` | — | `{items: [InboxItem], merges: [InboxItem], at}`。`items` は未回答の項目を古い順に(`inbox.list`。事実は読み直したもの)。`merges` は人が `merge` と答えた pekoed の根拠の項目のうち、この 24 時間に変わったものを新しい順に。失敗はすべて、それ以外(マージ中・済み)は 5 件まで(`answer.merge` でマージ中・済み・失敗と理由が分かる。失敗は人を待つので、後のマージに押し出されない)。InboxItem は docs/api.md のまま |
| `POST /api/inbox/{id}/answer` | `{choice?: int, note?, rev}` | `InboxItem`(`inbox.answer`、`answer.via` は `web:<端末名>`)。`choice` は 0 始まりで、省くとメモだけの答え(選択肢のどれでもない。`answer` に `choice` は入らない)。400: `rev` がない・`choice` が範囲外・`choice` もメモもない・pekoed の根拠の項目に `choice` がない・`send back` にメモがない / 404: 無い id / 409: `rev` が今のものと違う(事実が変わった)・答え済み・閉じられた |
| `GET /api/tapi` | — | `{started, alive, state, wait_kind, state_since, last_message, inbox_open, ledger_open, look, answerable, sendable, confirm_send}`。`answerable` は tapi が許可待ち(Web からは答えない)、`sendable` は `allow_devices` があって送れること、`confirm_send` は `[web] confirm_tapi_send`(既定 true。false ならページは確認シートを挟まずに送る。リクエストごとに今の設定を読むので再起動は要らない)。`pending_tool`・Claude のセッション ID・稼働ディレクトリは返さない |
| `GET /api/tapi/screen` | — | `{slug: "+tapi", rows, lines, dead}`。tapi chat の最後 80 行(セッションの `/screen` と同じ形)。409: tapi が起動していない |
| `GET /api/tapi/transcript?after=<cursor>` | — | `ChatPage`: `{conversation, cursor, items: [{kind: human\|pekoe\|agent\|tool\|turn, text?, name?, input?, question?, ms?, at}], reset?, new_conversation?, earlier?, before?}`。上の「吹き出し(chat)と画面(screen)」。`?before=<cursor>`(答えの `before`)で前のページ(`cursor` は空)。404: transcript が無い / 409: tapi が起動していない・前のページの会話が変わった / 400: カーソルが読めない・200 バイトを超える |
| `POST /api/tapi/send` | `{text, images?: [{data}]}` | `{ok: true, send: <送信の記録の id>, images: <枚数>, text: <打った文>}`。`tapi.send`(`submit: true`)。text と images はセッションの send と同じ(1〜4000 文字・画像があれば空でよい、制御文字は 400、画像は 415 / 400)。`text` は画像の行を足した打った文(ページが transcript に現れたかを見るのに使う)。403: `allow_devices` が無い / 409: tapi が許可待ち・画面に許可メニュー・起動していない・終了している |
| `POST /api/tapi/keys` | `{keys: ["Down", …]}` | `{ok: true}`。セッションの keys と同じキー・個数。409: tapi が質問待ちでない・許可待ち・画面に許可メニュー・起動していない |
| `GET /api/overview` | — | `{counts, groups, at}`。下の「Overview」 |
| `GET /api/now` | — | `{needs, counts, requests, requests_open, merged, at}`。Now の画面。下の「Now」 |
| `GET /api/stats` | — | **ローカル GUI だけ**(tailnet の Web UI には無い。docs/gui.md)。`Stats`(`stats {days: 7}` = `pekoe --json stats` と同じ。docs/api.md の「集計(stats)」)。Dashboard の Overview の日別のマージ数と今日のトークン。ページは 30 秒に 1 回まで読む |
| `GET /api/ledger?closed=<N>` | — | **ローカル GUI だけ**。`{open: [LedgerEntry], closed: [LedgerEntry], closed_total}`(`ledger.list {status: "all"}` を人として呼ぶ)。`open` は開いている依頼の全部(古い順)、`closed` は閉じたもの(pekoed が自動で閉じた `chat` を含む)を閉じた時刻の新しい順に `N` 件(既定 5、0〜500。範囲外は 400)、`closed_total` は閉じたものの数。Dashboard の Requests と Open ledger |
| `GET /api/repos` | — | **ローカル GUI だけ**。`{repos: [{repo_id, name, dir, sessions}]}`(`repo.list` のうちディレクトリの分かるもの)。New session のリポジトリの選択 |
| `GET /api/repos/{repo_id}/issues?filter=mine\|open&refresh=1` | — | **ローカル GUI だけ**。`{filter, issues: [{number, title, labels, url, updated_at, branch, session?}], unavailable?, fetched_at, cached}`(`issues.list` を人として呼ぶ。`filter` は既定 `mine`。`branch` は TUI の `n` が埋めるブランチ名の案、`session` はその issue のセッション)。New session の issue の一覧。404: 知らない `repo_id` / 502: gh が答えない |
| `GET /api/repos/{repo_id}/issues/{n}` | — | **ローカル GUI だけ**。`{number, title, url, state, author, labels, session?, text, sig}`(`issues.view` をキャッシュなしで)。`text` はプロンプトの先頭に入る文の全文(`pekoe new --from gh#N` と同じ囲みと前置き。題と本文の改行・タブ以外の制御文字と書式文字(Unicode の Cf)は落とす。長さは切らない)、`sig` はその SHA-256。確認シートが `text` を見せる。400: 番号でない / 404: 知らない `repo_id` / 502: gh が答えない |
| `GET /api/worktrees` | — | **ローカル GUI だけ**(サイドバーの Worktrees。docs/gui.md の「Worktrees」)。`{repos: [{repo_id, name, dir, worktrees: [{path, branch, slug?, port?, managed, main, state?}]}]}`。`repo.list` のうちディレクトリの分かるリポジトリごとに `worktree.list {repo: dir}` を人として呼ぶ(TUI の [2] Worktrees と同じ)。読むだけ |
| `POST /api/sessions` | `{repo_id, branch, prompt?, issue?, issue_sig?}` | **ローカル GUI だけ**。`WebSession`(作ったセッション。`creating` か `queued`)。`session.new` を人として呼ぶ(TUI の `n` と同じ。エージェントはリポジトリの設定のもの)。`repo_id` は `GET /api/repos` のもので、pekoed が知っているディレクトリに置き換える。`issue` があれば pekoed がその issue を読み直し、`text` の SHA-256 が `issue_sig` と同じときだけ、`text`(と、あれば空行の後に `prompt`)をプロンプトにして作り、セッションの `issue` を記録する。400: 知らない `repo_id`・`branch` が空・制御文字・`prompt` が 16000 文字を超える・`issue` が番号でない・`issue_sig` が無い / 409: `session.new` が断った(ブランチ名・同じ slug がある など。本文に理由)・issue がシートで見せた後に変わった / 502: gh が答えない |
| `POST /api/sessions/{slug}/interrupt` | `{}` | **ローカル GUI だけ**。`WebSession`。`session.interrupt`(Escape を 1 回)。404: 無いセッション / 409: 動いていない(許可待ち・画面に許可メニュー・質問待ち・idle・画面にスピナー行 `esc to interrupt` が無い など)・止まっている・窓が無い・2 秒以内に Interrupt した |
| `POST /api/tapi/interrupt` | `{}` | **ローカル GUI だけ**。`/api/tapi` と同じ形。`tapi.interrupt`。409: 起動していない・終了している・動いていない・2 秒以内に Interrupt した |
| `GET /api/sessions/{slug}/transcript?after=<cursor>` | — | **ローカル GUI だけ**(セッションの Chat。docs/gui.md の「段階 2 の一部: セッションの吹き出しと送信欄の / @」)。`ChatPage`(tapi のものと同じ形・同じ絞り方、`?before=` で前のページ)。`session.transcript`: 読むのはセッションの worktree の Claude Code のプロジェクトディレクトリの中だけ(docs/api.md)。404: 無いセッション・Claude Code でないエージェント・まだ会話が無い / 409: 前のページの会話が変わった / 400: カーソルが読めない |
| `GET /api/sessions/{slug}/commands` | — | **ローカル GUI だけ**。`{agent, supported, commands: [{name, source, description?}]}`(`session.commands`)。送信欄の `/ Commands` |
| `GET /api/sessions/{slug}/files?q=<語>` | — | **ローカル GUI だけ**。`{files: [path], matches, total, truncated?, skipped?}`(`session.files`。worktree の `git ls-files`、50 件まで)。送信欄の `@ Files`。400: `q` が 256 バイトを超える / 404: 無いセッション |
| `GET /api/sessions/{slug}/panel` | — | **ローカル GUI だけ**。`{agent, supported, why?, model?, model_name?, effort?, effort_from?, mode?, tokens?, tokens_short?, running_version?, installed_version?, update_installed?}`(`session.panel`)。Session の右パネルの `AGENT` と `NOTICES` の `Update installed`。エージェントの自己申告で、表示だけに使う。404: 無いセッション |
| `GET /api/sessions/{slug}/changes` | — | **ローカル GUI だけ**。`{slug, files: [{status, path, added, deleted, binary?, peers?: [{slug, conflict?}]}], overlaps?}`(`changes` と、`session.get` の衝突予報 `overlaps` をファイルごとに並べたもの)。レールの Changes。404: 無いセッション |
| `GET /api/sessions/{slug}/diff?path=<パス>` | — | **ローカル GUI だけ**。`{slug, path, status, binary?, added, deleted, hunks: [{header, lines: [{kind, old?, new?, text, cut?}]}], lines, total, truncated?}`(`changes.diff`。3000 行・256 KiB まで、1 行 2000 バイトまで)。Changes の差分を読むだけの表示。400: `path` が無い・4096 バイトを超える / 404: 無いセッション、`path` がそのセッションの変更の一覧に無い |
| `GET /api/config` | — | **ローカル GUI だけ**。`{path, exists, sections: [{name, doc, keys: [{key, value, doc, restart?, set?}]}], restart: [string], problems: [{key?, message, fatal?}]}`(`config.show` を人として呼ぶ)。pekoed が今使っている設定(`pekoe config show` と同じく既定を埋め、`[web] auth_key` は `(hidden)`)を節ごとに。`restart` は config.toml が変えたが再起動まで効かないもの、`problems` は pekoed が最後に読んだファイルへの `config check`。レールの Settings(読むだけ) |
| `POST /api/tapi/reset` | `{}` | **ローカル GUI だけ**。`/api/tapi` と同じ形(`started: false`)。`tapi.reset`(TUI の `T`)。依頼・受信箱・報告・送信の記録は残る |
| `POST /api/tapi/start` | `{}` | **ローカル GUI だけ**。`/api/tapi` と同じ形。起動していなければ `tapi.open`(TUI の `t`)、窓はあってエージェントが終了していれば `tapi.restart`(その画面の `↵`)。確認は無い(TUI と同じ)。409: 動いている |
| `POST /api/sessions/{slug}/keys` | `{keys: ["Down", …]}` | `{ok: true}`。1〜8 個のキーを順に送る。キーは `Up Down Left Right Tab BTab Space Enter Escape 0`〜`9` だけ。400: 許可外のキー・個数 / 409: 質問待ち(`wait_kind == question`、`stopped` でない)以外 |

Overview: `counts` は `{running, queued, waiting, review (done の数), open_prs, ready (根拠が揃った PR), inbox_open, ledger_open}`。`groups` は依頼元(`parent`)と依頼(`request`)ごとのまとまりで、tapi の依頼(新しい依頼が上)→ ほかのセッションが作ったもの → 人・unknown → 直接開いたもの、の順。各まとまりは `{parent, request?: {id, text (200 文字で切る), status}, rows: [Flow]}`。他人の PR のレビュー(`pr_review`)は工程表に入れない。

Flow は `{slug, repo, branch, state, state_since, look, pr, pr_look, after, report (最新の報告の status), steps, merge?}`。`steps` は `impl` `review` `pr` `ci` `merge` の 5 つ(この順。独立レビューは PR の前にするので)で、それぞれ `{name, status: done|now|fail|todo, detail}`:

| step | done | now | fail |
| --- | --- | --- | --- |
| impl(実装) | PR がある・`pekoe land` 済み・landed・独立レビューの報告(`status: review`、pending を含む)がある | それ以外(queued は todo で、`detail` に待っている理由) | `failed`・エラー・最新の報告が `blocked` |
| pr | PR がある | — | PR が閉じられた |
| ci | PR のチェックがすべて成功 | 実行中・まだ読んでいない | 失敗がある |
| review | 最新の `status: review` の報告が pass(PR があれば `merge.check` の `review` が揃っている = PR の HEAD をレビューした) | 最新のレビューが pending(レビュー中)・pass だが PR の HEAD と違う | 最新のレビューが pass でも pending でもない |
| merge | landed(PR がマージされた) | `merge.check` が ready(人の答え待ち)、pekoed がマージ中、pekoed がマージして landed 待ち | 人の `merge` で pekoed がマージできなかった(根拠の項目がまだ作り直されていない間。`detail` に理由) |

`merge` は自分の PR があり landed でないセッションの `merge.check`(docs/api.md の「マージしてよいかの根拠」)で、ページは揃っていない条件を `detail` つきで出す。

Now: `needs` は人を待っているもののカードで、`{kind, since, item?, session?}`。`kind` は `merge_failed`(人の `merge` で pekoed がマージできなかった。`item` は答えた根拠の項目で `answer.merge.detail` が理由、`session` は PR のセッション)・`waiting`(許可か質問で待っているセッション。`session`)・`inbox`(受信箱の未回答。`item` は `GET /api/inbox` と同じ)。並びは pekoed のマージの失敗(新しい順)→ 待っているセッション(長く待っている順)→ 受信箱(古い順)。マージが失敗と記録されていても PR が実際にはマージされているもの(理由に pekoed が読み直した `PR #n is MERGED` がある、またはセッションが landed・同じ PR を MERGED と読んでいる。人が GitHub で先にマージしたときなど)は人を待っていないので、`needs` に入れず `merged` に入れる(`mergedAfterAll`)。`counts` は `{questions (受信箱のうち pekoed のマージの項目でないもの), ready (pekoed のマージの項目), running, queued, waiting, review (Sessions の To review の数)}`。`requests` は台帳の依頼で、それに取り組むセッションがあるもの(新しい順、20 件まで): `{id, text, status, slug, branch, repo, state, state_since, look, sessions (取り組むセッションの数), steps}`(先頭のセッションの Flow の `steps`)。`requests_open` は台帳の open の件数。`merged` は pekoed のマージで通ったもの(最近 1 日・5 件まで、新しい順): `{pr, item, about, at}`。`needs` の `merge_failed` は件数で切らず、この 24 時間の失敗は後にいくつマージが通っても残る。

WebSession は Session(docs/api.md)から表示に要るものだけを抜いたもの: `slug repo branch agent state wait_kind state_since answerable pending_tool last_tool last_message diffstat pr stopped error test git_blocked queue look pr_look group main`(`group` は Sessions の見出し: `active`(稼働中・許可待ち・作成中・queued・reviewing)・`review`(done・failed・approved)・`idle`・`landed`。`main` はリポジトリ本体のチェックアウトで動くセッション。`git_blocked` は本体の .git が変わって git を止めている、`queue` は queued のときに起動していない理由)。worktree とリポジトリのパス・環境変数・Claude のセッション ID は出さない。ツールの引数・エラー・最後のメッセージに含まれるパスはそのまま出る(許可の判断材料なので)。`answerable` は「allow / deny で答えられる待ち」(`state == waiting` かつ `wait_kind != question`)。`look` / `pr_look` は TUI の Sessions パネルと同じ記号・色の役割・言葉(`theme.Look` / `theme.PRLook`、アイコンは unicode。`{icon, role, word}`)。

Run は 1 行の中の同じ見た目の区間: `{t, fg?, bg?, b?, d?, i?, u?, s?}`(テキスト、文字色、背景色、太字・薄字・斜体・下線・取り消し線)。pekoed が `capture-pane -e` の SGR を解析して作る(`internal/termstyle`)。色は 16 色なら TUI の役割名(`err ok warn primary secondary text dim border primary-end`、反転の既定色は `bg` / `text`)で、ユーザーのテーマがそのまま効く。256 色と truecolor は `#rrggbb`。OSC 8 のリンクやカーソル移動など、色以外のエスケープと制御文字は捨てる。行ごとに既定の見た目から始め(TUI と同じ)、行末の空白は背景色があっても落とす。

画面の文字はエージェントの出力で、信用しない。Run には HTML を作らず、ページは `t` を `textContent` で入れ、色は決まった役割名のクラスか、`#rrggbb` を確かめてから CSSOM(`style.color`)で当てる。どちらでもない値は無視する。CSP(`style-src 'self'`)はそのまま。

見た目は TUI と同じ規則(docs/ui-guidelines.md)に合わせる。色は `GET /theme.css` が TUI のパレット(`[theme.colors]` の上書きを含む。`#rrggbb` 以外は捨てる)を CSS 変数で返し、`app.css` は役割名だけを使う。
