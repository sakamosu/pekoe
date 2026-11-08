# Web UI(iPhone から boba を操作する)

Mac で bobad を動かしたまま、外出先の iPhone(Tailscale アプリが ON)の Safari から開き、ホーム画面に追加して使う軽い Web UI。できるのは次の 4 つだけ。

- セッション一覧(状態つき。許可待ちは一番上に、黄色の枠で)
- 許可待ちへの allow / deny(ツールの要約と、エージェントが画面に出している許可ダイアログを見せ、押す前に確認を挟む。`always` は出さない)
- `send`(一言の指示を送る。allow / deny で答える許可待ちの間は送れない)
- エージェント画面の最新テキストを読むだけの表示(色・カーソルなし。ターミナルの再現はしない)

賢さは bobad にあり、Web は表示層。ハンドラは CLI と同じ API メソッド(`session.list` / `session.screen` / `session.answer` / `session.send`)を同じ `Daemon.Call` 経由で呼ぶ。

## 構成

```text
iPhone (Safari / ホーム画面)
   │ HTTPS  https://boba.<tailnet>.ts.net/
   ▼  WireGuard(tailnet の中だけ)
┌─ bobad ─────────────────────────────────────────┐
│ tsnet ノード "boba"  :443 (ListenTLS)            │
│   └ webGuard: Host → WhoIs → 許可ユーザー → CSRF │
│       └ /api/* → Daemon.Call(session.*)         │
│       └ /      → go:embed の静的ファイル         │
│ unix socket (TUI・CLI・nvim)   … 既存のまま      │
│ 127.0.0.1:47777 (Chrome 拡張)  … 既存のまま      │
└─────────────────────────────────────────────────┘
```

- bobad は Tailscale の [tsnet](https://tailscale.com/kb/1244/tsnet) を埋め込み、bobad 自身が tailnet の 1 台(既定のホスト名 `boba`)になる。Mac に入っている Tailscale アプリとは別のマシンとして admin console に出る
- 待ち受けは tsnet の仮想ネットワーク上の `:443` だけ。**localhost にも Mac の LAN 側にも TCP ポートは開かない**。tsnet は WireGuard 用に UDP ポートを 1 つ使う(P2P の直結用。無くても DERP 経由で動く)
- 証明書は tsnet が Let's Encrypt から `boba.<tailnet>.ts.net` の分を取る(tailnet で MagicDNS と HTTPS を有効にしておく必要がある)
- Funnel(インターネットへの公開)は使わない。`ListenFunnel` は呼ばず、tsnet は起動時に serve 設定を空にする
- 静的ファイル(`internal/daemon/webui/`)は `go:embed`。外部 CDN・フォントは読まない。PWA の manifest とアイコン(PNG をその場で描く)つき。Service Worker と Web Push は入れない(通知は `[notify] ntfy` で足りる)
- 証明書は待ち受けを始めるときに先に取っておく(初回の発行は時間がかかり、iPhone の最初の接続がタイムアウトしないように)。HTTPS が tailnet で無効などで待ち受けに失敗したら、30 秒ごとにやり直す(admin console で後から有効にしても再起動は要らない)。マシン名の変更にも追従する
- 更新は 3 秒ごとのポーリング(画面を開いている間は画面テキストも 2.5 秒ごと)。アプリが裏に回ると止まる

### ビルド:既定は Tailscale なし

Web UI は build tag `web` を付けたビルドにだけ入る。職場の Mac のように VPN クライアントを含むソフトを入れたくない環境もあるので、既定のビルドには Tailscale のコードを一切含めない。

```sh
make install          # 既定: Web UI なし(Tailscale のコードを含まない、約 19MB)
make install WEB=1    # Web UI 入り(-tags web、tsnet をリンク、約 38MB)
```

- 既定のビルドでは `go version -m boba` にも `tailscale.com` は出ない(`internal/daemon/webts.go` が `//go:build web`、代わりに `webts_off.go`)。ただし `go.mod` には依存として載っている
- 既定のビルドで設定に `[web] enable = true` があっても(dotfiles で個人の Mac と設定を共有しているなど)、何もせずに bobad のログに警告を出すだけ。`boba web` は「Web UI なしのビルド」と再ビルドの方法を出す
- `make test` は両方のビルドを検査する。隔離環境で Web UI 入りを試すには `WEB=1 scripts/sandbox.sh`

### `[web]` が無効なとき

Web UI 入りのビルドでも既定は無効。無効なら `startWeb` は状態を `off` にして戻るだけで、`tsnet.Server` を作らず、状態ディレクトリも作らず、ソケットも開かない(`TestWebOffStartsNothing`)。リンクされた tailscale パッケージの `init()` には、ゴルーチン・ファイル・ネットワークを使うものはない。

## 設定

`~/.config/boba/config.toml`:

```toml
[web]
enable        = true
hostname      = "boba"               # tailnet 上のマシン名 → https://boba.<tailnet>.ts.net/
allow_users   = ["you@github"]       # Tailscale のログイン名(admin console の Users に出るもの)
allow_devices = ["iphone-15"]        # 任意。この端末だけに絞る(admin console の Machines の名前)
# state_dir   = "~/.local/share/boba/tsnet"   # 既定。$BOBA_HOME があればその share/tsnet。~ か絶対パスで
# auth_key    = "tskey-auth-…"        # 初回参加用。環境変数 TS_AUTHKEY でもよい
```

- `allow_users` が空なら起動しない(誰も入れないサーバーを立てても意味がないので、エラーにして `boba web` に出す)
- `allow_devices` は強く勧める(下の脅威モデルを参照)
- 設定を変えたら bobad を再起動する(`boba daemon stop; boba daemon start`。エージェントは tmux の中で動き続ける)

### 初回の tailnet 参加

1. `make install WEB=1` で Web UI 入りのビルドを入れる
2. tailnet の admin console で MagicDNS と HTTPS Certificates を有効にする
3. `[web]` を書いて bobad を再起動する
4. `boba web` を実行する。`state needs-login` と `login https://login.tailscale.com/a/…` が出るので、そのリンクを開いて承認する(同じ URL は `bobad.log` にも出る)。bobad の起動直後の数秒は URL がまだ無く、`login  (getting the login URL…)` と出るので、少し待ってもう一度 `boba web` を実行する。auth key を使うなら `auth_key` か `TS_AUTHKEY` を設定して再起動すれば、URL は出ずに参加する
5. `boba web` が `state running` と `url https://boba.<tailnet>.ts.net/` を出したら、iPhone の Safari でその URL を開き、共有メニューから「ホーム画面に追加」

`TS_AUTHKEY` は bobad が起動時に自分の環境変数から取り除いてから tmux を立ち上げるので、エージェントのペインには渡らない(渡ると、エージェントがその鍵で自分のノードを tailnet に参加させ、ユーザー本人として Web UI を叩けてしまう)。`auth_key` を設定に書いた場合は、同じユーザーで動くエージェントには読めるので、参加したらすぐ消す。ログイン URL で参加するのがいちばん安全。

参加後の鍵は `state_dir`(既定 `~/.local/share/boba/tsnet`、パーミッション 700)に残るので、2 回目以降は何もしなくてよい。admin console で `boba` マシンの key expiry を無効にしておくと、180 日ごとの再ログインが要らない。

## 認証の流れ

token もパスワードも cookie も持たない。リクエストごとに、tailnet がその接続を誰のものと言うかだけを見る(`webGuard`)。

1. **Host**: `Host` ヘッダがこのノードの名前(tsnet の `CertDomains`)でなければ 421。DNS rebinding で別の名前から来たものを落とす
2. **このマシン自身**: 接続元の IP が Mac のネットワークインターフェイスのどれかのアドレス(Mac の Tailscale アプリの `100.x` など)かループバックなら 403。Mac の Tailscale が userspace-networking(TUN なし)で 100.x がどのインターフェイスにも無い場合に備え、WhoIs で引いた相手ノードのホスト名が Mac のホスト名と同じでも 403
3. **WhoIs**: tsnet の `LocalClient().WhoIs(remoteAddr)` で接続元のノードとユーザーを引く。引けない・タグつきノード・`allow_users` にないユーザー・(設定があれば)`allow_devices` にない端末は 403。比較は大文字小文字を区別しない
4. **書き込み(POST)**: `X-Boba-Web: 1` ヘッダと `Content-Type: application/json` が必須。`Sec-Fetch-Site` があれば `same-origin`、`Origin` があれば自分の URL でなければ 403
5. 応答には CSP(`default-src 'self'`、インライン script なし)・`X-Frame-Options: DENY`・`Referrer-Policy: no-referrer` を付け、API は `Cache-Control: no-store`

拒否は理由つきで `bobad.log` に出る(`web: 403 GET /api/sessions from 100.x:port: user … not in allow_users`)。allow / deny / send は誰がどの端末から何をしたかをログに残す。

### allow / deny の扱い

- 受け付ける答えは `allow` と `deny` だけ。`always`(と CLI の別名 `a` `y` `yes` `n` など)は 400。恒久的な許可は端末の前で決める
- `wait_kind` が `question`(AskUserQuestion など)のときは 409。数字キーで答えるものではないので、画面を見て `send` で答える
- 押したときに画面に出ていたツール(名前と引数)を一緒に送り、bobad の今の `pending_tool` と違えば 409(「内容が変わりました」)。読んでいないプロンプトに allow が当たるのを防ぐ
- 押す前に確認シートを挟む。シートには boba の要約(`pending_tool`:ツール名と主な引数 1 つ、200 バイトまで)と、画面の最後 18 行(エージェント自身の許可ダイアログ。実行するコマンドの全体が出る)を並べる。**判断は画面の方で行う**
- 一致の確認は `session.answer` の中(`expect`)で、キーを送る直前に行う

`pending_tool` は「最後に届いた PreToolUse」で、許可ダイアログが出ているツールそのものとは限らない(エージェントが複数のツールを並行に呼ぶと、後から来た方が残る)。また 200 バイトで切った要約なので、末尾だけ違うコマンドは区別できない。だから要約の一致は「読んでから状況が変わっていないか」の検出に使い、何を許可するかは画面で確かめる作りにしている。

### send の扱い

allow / deny で答える許可待ち(`answerable`)の間は 409。許可メニューは最初の 1 文字を選択肢として受け取るので、「2つ目も…」のような文が「Yes, and don't ask again」(= always)になってしまう。質問(`wait_kind == question`)には send で答える。

## 脅威モデル

| 脅威 | 対策 | 残る穴 |
| --- | --- | --- |
| **Mac 上のプロセス(エージェント自身を含む)がなりすます** | localhost に何も開かない。ヘッダ(`Tailscale-User-Login` など)を信じず、接続ごとに WhoIs。Mac 自身(アドレス、または WhoIs のホスト名)からの接続は拒否するので、エージェントが Mac の Tailscale 経由で `https://boba…/` を叩いて自分の許可待ちに allow を返すことはできない。`TS_AUTHKEY` はエージェントに渡さない | Mac 上で同じユーザーとして動くプロセスは、もともと `tmux -L boba send-keys` で自分の pty にキーを送れる。Web UI はその穴を広げないが、塞ぎもしない。また、エージェントが同じユーザーの**別の端末**(ssh できる Linux 機など)に入って叩くことはできる → `allow_devices` で iPhone だけに絞る |
| **tailnet 上の他人・他の端末** | `allow_users` / `allow_devices`。タグつきノード(サーバー・CI)は常に拒否 | tailnet の ACL で他人から `boba:443` への到達自体を止めておくとなお良い |
| **iPhone のブラウザで開いた悪意あるページ(CSRF・DNS rebinding)** | 書き込みにはカスタムヘッダと JSON が必須(クロスオリジンではプリフライトになり通らない)。`Origin` / `Sec-Fetch-Site` を確認。`Host` が自分の名前でなければ拒否 | — |
| **端末の紛失・盗難** | 認証は端末の Tailscale の鍵なので、admin console でその端末を削除(または key を失効)すれば即座に入れなくなる。iPhone の画面ロックが実質のパスワード | ロックが解かれた iPhone を拾った人は allow / deny / send ができる。`always` は出さず、許可待ちの間は send も止めるので、恒久的な許可までは与えられない |
| **Certificate Transparency ログ** | — | HTTPS 証明書を取ると、`boba.<tailnet>.ts.net` という名前が CT ログ(誰でも検索できる公開ログ)に載る。tailnet 名(`tail1234` のようなランダム名か、自分で付けた名前)とホスト名が世に出る。tailnet の外からは到達できないので中身は見えないが、名前に秘密を含めない。気になるなら `hostname` を目立たないものにし、tailnet 名はランダムのものを使う |
| **Funnel での公開** | `ListenFunnel` を呼ばない。tsnet は起動時に serve 設定を空にする | — |
| **Tailscale へのログ送信** | — | tsnet は通常の tailscaled と同じく、診断ログを Tailscale のログサーバーに送る(`state_dir/tailscaled.log.conf`)。中身は接続のメタデータで、boba の画面やプロンプトは含まない |

## API(Web UI 用、tailnet 上の HTTPS)

| method / path | body | 返り値 |
| --- | --- | --- |
| `GET /api/me` | — | `{login, device}` |
| `GET /api/sessions` | — | `{sessions: [WebSession], at}`。待ち(古い順)→ 実行中 → その他(新しい順) |
| `GET /api/sessions/{slug}/screen` | — | `{slug, lines: [string], dead}`。agent 窓の最後 80 行、色を落としたテキスト |
| `POST /api/sessions/{slug}/answer` | `{answer: "allow"\|"deny", tool: {name, input}\|null}` | `WebSession`。400: answer が違う / 409: 待っていない・質問・プロンプトが変わった |
| `POST /api/sessions/{slug}/send` | `{text}` | `{ok: true}`。text は前後の空白を落として 1〜4000 文字、Enter つきで送る。409: allow / deny で答える許可待ちの間 |

WebSession は Session(docs/api.md)から表示に要るものだけを抜いたもの: `slug repo branch agent state wait_kind state_since answerable pending_tool last_tool last_message diffstat pr stopped error`。worktree とリポジトリのパス・環境変数・Claude のセッション ID は出さない。ツールの引数・エラー・最後のメッセージに含まれるパスはそのまま出る(許可の判断材料なので)。`answerable` は「allow / deny で答えられる待ち」(`state == waiting` かつ `wait_kind != question`)。
