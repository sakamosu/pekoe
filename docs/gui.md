# ローカル GUI(設計案、未実装)

pekoed が動いている Mac 自身のブラウザから、localhost で待ち受ける Web の画面で pekoe を操作する。この文書は**設計案**で、まだコードは無い。レビューで決まったことを反映してから実装する。

根拠はファイル名で添える。コードを読んで確かめたことと、推測(「推測」「要確認」と書く)を分ける。

## 目的

1. **TUI が苦手な人に、GUI で TUI と同等の操作を提供する。** キーを覚えなくても、セッションを作る・画面を見る・指示を送る・許可に答える・受信箱に答える・land するまでをマウスでできるようにする
2. **HTML の表現力で、どこまで便利にできるかを検証する。** 端末の文字のマス目では描きにくいもの(工程表のダッシュボード、集計のグラフ、触るファイルの重なり、差分、証拠の画像)を、どこまで使いやすくできるかを試す場にする

### 今の Web UI・TUI との棲み分け

| | TUI | Web UI(`docs/web.md`) | ローカル GUI(この文書) |
| --- | --- | --- | --- |
| どこから | Mac の端末 | 外出先の iPhone(tailnet 経由) | Mac 自身のブラウザ |
| 解く課題 | 全部の操作(主役) | 離れている間の、許可・短い指示・受信箱への回答 | TUI と同じことを GUI で。リッチな表示の検証 |
| 届き方 | unix ソケット + `human-token` | tsnet の `:443`(localhost には開かない) | `127.0.0.1:<port>` |
| 認証 | `human-token` のファイル | 接続ごとの Tailscale WhoIs | 一度きりのトークン → セッションのトークン(下の「認証」) |
| 守りの考え方 | エージェントは token を読めない | **Mac 自身からの接続は 403**(`internal/daemon/web.go` の `webGuard`・`isLocalAddr`) | Mac 自身からつなぐことが前提。だから「Mac の上の別のプロセス」と人を見分ける仕組みが要る |

Web UI は「Mac 自身からは使わせない」ことで、同じ Mac で動くエージェントが自分の許可待ちに allow を返す道を塞いでいる(`docs/web.md` の「認証の流れ」2 と脅威モデルの 1 行目)。ローカル GUI はその逆で、Mac 自身から使うのが目的なので、この守りをそのまま流用できない。これがこの設計の中心の問題になる。

## なぜ「localhost で立てるだけ」では足りないか

今の Web UI のハンドラは CLI と同じ API を `Daemon.Call` か `Daemon.CallAs(humanCaller(webVia(p)), …)` で呼ぶ(`internal/daemon/web.go`・`webinbox.go`・`webtapi.go`)。どちらも**人間の呼び出しとして**動く(`internal/daemon/server.go` の `Call` は `humanCaller("")` を渡す)。つまり HTTP の入口で人を確かめることが、許可待ちへの回答・生キー・tapi への入力といった「人間だけの操作」(`docs/api.md`)を守る唯一の線になる。認証なしで localhost に開くと、次がすべて通る。

- **エージェントが自分の許可待ちに allow を返す。** pekoed が worktree のコードを動かす hooks の sandbox(`[hooks] test`・`dev`・`land` のテストなど)は、unix ソケットの接続(tmux・nvim)は断るが、TCP の接続は断らない(`internal/sandbox/policy.go` の `DefaultPolicy` は `DenyConnect` に tmux と nvim のディレクトリだけを入れる)。エージェントが書いたテストや dev server が `curl http://127.0.0.1:<port>/api/sessions/<自分>/answer` を叩けば、自分の許可待ちに allow が返る。Claude Code のエージェント自身の Bash が localhost の TCP につなげるかは Claude Code の sandbox の設定次第(推測: `allowedDomains` のプロキシを通るので既定では断られるが、`[agents.claude] sandbox = false` や `excludedCommands` のコマンド、codex など pekoe が sandbox を付けないエージェントからは届く。README の「Letting agents use pekoe: permissions」)
- **ブラウザで開いた他のページからの CSRF・DNS rebinding。** エージェントが書いた dev server(`localhost:3005` など)を人が同じブラウザで開くのは pekoe の日常の使い方(TUI の `b`、Chrome 拡張)。そのページの JavaScript は `fetch('http://127.0.0.1:<port>/api/…', {method: 'POST', mode: 'no-cors'})` で単純なリクエストを送れる。DNS rebinding なら、自分のドメインを 127.0.0.1 に向け直して同一オリジンとして読み書きできる
- **Cookie はポートを区別しない。** `localhost:<GUI のポート>` で付けた Cookie は、ブラウザが `localhost:3005` へ送るリクエストにも付く(Cookie の対象はホストで、ポートを見ない。RFC 6265 の 8.5)。エージェントが書いた dev server のサーバー側は、人がそのページを開いただけで GUI の Cookie を受け取り、`curl -H 'Cookie: …'` でそのまま再生できる。`HttpOnly` は JavaScript から読めなくするだけで、サーバーへの送信は止めない。`SameSite` もサイト(スキームと登録可能ドメイン)の単位でポートを見ないので、`localhost:3005` から `localhost:<GUI のポート>` へは同じサイト扱いになり守れない

なお、Chrome 拡張用の `127.0.0.1:47777` は、すべてのリクエストに `Authorization: Bearer <http-token>` を求め、`chrome-extension://` 以外の `Origin` を 403 にしている(`docs/api.md` の HTTP の節、`internal/daemon/http.go` の `guard`)。ローカル GUI も「localhost に開くなら秘密を持つ者だけ」という同じ方針に立つ。

## 範囲

### 段階

| 段階 | 中身 | ねらい |
| --- | --- | --- |
| **0. 入口だけ** | localhost の待ち受けと認証(下の「認証」)を作り、今の Web UI の静的ファイル(`internal/daemon/webui/`)と API をそのまま Mac のブラウザに配る。iPhone と同じ 8 つの機能が Mac で使える | 一番危ない部分(認証・脅威モデル)を先に、小さく固める。画面はまだスマホ向けのまま |
| **1. MVP** | デスクトップ向けの画面(下の「MVP」) | 「TUI が苦手な人が毎日の操作をできる」最小 |
| **2. TUI と同等(操作)** | セッションの作成(`session.new`、リポジトリの選択、issue から)、land(衝突予報つきの確認)、dev server の起動・停止、セッションの終了・worktree の削除(確認つき)、undo・再起動、片付け(`cleanup`)、tapi chat、PR の表示(チェック・レビュー・スレッドを読み、エージェントに送る)、issue の一覧、受信箱の dismiss | TUI のキー一覧(README の「TUI」、`docs/design.md`)の操作を一通り GUI で |
| **3. TUI と同等(重いもの)** | レビュー面(差分を読み、行にコメントし、承認する。TUI では nvim の `r`)、run 窓(`!`、1 回のコマンドを人が打って対話する)、エージェントの画面への生の入力(TUI で `↵` で入る入力) | どれも端末の再現か、任意のキー・コマンドを送る経路。危険も実装の量も大きいので、作るかどうかを段階 2 の後に決める(下の「未決事項」) |
| **4. HTML の検証** | 集計(`stats`)のグラフ、セッションの時系列(いつ動き・待ち・終わったか)、触るファイルの重なりの図(`conflicts.list`)、証拠(スクショ・ログ)の表示、デスクトップ通知(Notification API) | 目的 2。どれが TUI に戻す価値があるかも見る |

段階 2〜4 は順番を入れ替えてよい(検証を早く見たいなら 4 の一部を 2 より先に)。

### MVP(段階 1)

- **概要・工程表のダッシュボード**: 件数、tapi の状態、依頼ごとのセッションと `impl → review → PR → CI → merge`、マージしてよいかの足りない条件。データは今の `GET /api/overview` と同じ(`internal/daemon/webinbox.go` の `webOverview`)。デスクトップの幅を使い、表で一度に見せる
- **セッション一覧と画面**: 左に一覧(リポジトリごと、待ちを上に)、右に選んだセッションの画面(`session.screen`、色つき。今の `/screen` と同じ最後 80 行)
- **send**: 選んだセッションへの短い指示(画像の添付を含む。今の `POST …/send` と同じ確認)
- **許可待ちへの回答**: allow / deny。確認シートで pekoe の要約と画面を並べ、判断は画面で行う(`docs/web.md` の「allow / deny の扱い」と同じ)。`always` を出すかは未決事項
- **質問へのキーパッド**: AskUserQuestion のメニューに答える(今の `POST …/keys`)。これが無いと質問に答えられず「毎日の操作」にならないので、MVP に入れる
- **受信箱への回答**: 事実が先・推奨を既定にしない・確認シート・`rev` の突き合わせ(`docs/web.md` の「受信箱(Inbox タブ)の扱い」と同じ)

MVP では画面のための新しい API を足さない。今の Web UI の `/api/*` で足りる(段階 0 でそのまま使えることを確かめる)。足すのは認証のためのもの(ソケットの `gui.*` と `POST /api/gui/session`、下の「認証」)だけ。

### 後回しにするもの

- 段階 2〜4 のすべて
- 別の端末(LAN の他の PC)からの接続。待ち受けは `127.0.0.1` だけで、`0.0.0.0` には開かない(外から使うのは Web UI の役目)
- Service Worker・オフライン・PWA としてのインストール
- 複数の人(OS ユーザー)で 1 つの pekoed を使うこと

## 認証

### 案の比較

| 案 | 流れ | 良いところ | 困るところ |
| --- | --- | --- | --- |
| **A. Jupyter 方式(Cookie)** | `pekoe gui` が一度きりのトークン付き URL でブラウザを開き、pekoed が `HttpOnly` の Cookie に替える | なじみがある。再読み込み・新しいタブでもそのまま使える | **Cookie はポートを区別しない**ので、人が開いたエージェントの dev server(`localhost:3005`)のサーバー側に Cookie が届き、再生される(上の節)。`SameSite` でも守れない。ホスト名をずらす工夫(案 C)が要る |
| **B. フラグメントのトークン → Bearer(Cookie なし)** | `pekoe gui` が `http://127.0.0.1:<port>/#t=<一度きりのトークン>` を開く。ページの JS がフラグメントを読み、`POST /api/gui/session` でセッションのトークンに替え、`history.replaceState` でフラグメントを消す。以後の API はすべて `Authorization: Bearer <セッションのトークン>` | ブラウザが勝手に付ける資格情報が無いので、CSRF が構造的に起きない。セッションのトークンの置き場(`sessionStorage`・メモリ)は**オリジン(スキーム・ホスト・ポート)ごと**に分かれるので、`localhost:3005` のページからは読めない。フラグメントはサーバーにもリファラにも送られない | 新しいタブ・ブラウザの再起動で入り直しになる(`sessionStorage` の範囲。`localStorage` にすれば残るが下の残るリスク)。XSS があればトークンを持ち出せる(案 A の `HttpOnly` なら持ち出せないが、XSS があればその場で操作はできるので差は「持ち出せるか」だけ) |
| **C. 専用ホスト名 + Cookie** | 案 A を `pekoe.localhost` のような専用の名前で開き、Cookie をそのホストだけに付ける(`Domain` を付けない) | Cookie が `localhost:3005` には付かない | `*.localhost` をループバックに解決するかはブラウザ次第(推測: Chrome・Firefox はする、Safari は要確認)。エージェントの dev server を `pekoe.localhost:3005` で開けば同じホストになり、また Cookie が届く(dev server の URL はエージェントが報告に書ける)。守りが「人がどの URL を開くか」に寄る |
| **D. ネイティブの窓(WKWebView)+ unix ソケット** | 小さな macOS アプリが WebView で画面を描き、TCP は使わずに pekoed のソケットへ `human-token` 付きでつなぐ | localhost に何も開かない。CSRF・DNS rebinding・Cookie の問題が全部消える | Swift のアプリと署名・配布が要る。「ブラウザで検証する」目的 2 から外れる。Linux で使えない |
| **E. 毎回パスワード・トークンを貼る** | ページの最初に `human-token` やパスワードを入れる | 仕組みが単純 | `human-token` を人がコピーして回すことになる(クリップボードはエージェントからも読める)。TUI が苦手な人に向かない |

### 推奨: 案 B

案 B を推奨する。守りの要が「ブラウザがオリジンごとに分けるもの」(`sessionStorage`・`Authorization` ヘッダはページの JS しか付けられない)になり、ポートを区別しない Cookie の問題と CSRF を、仕組みで避けられるため。案 A・C は Cookie の届く範囲を人の行動(どの URL を開くか)で守ることになる。

流れ:

1. 人が `pekoe gui` を打つ。CLI は `human-token` を読んで(今の CLI と同じ)、新しいメソッド `gui.open`(token 必須)を呼ぶ
2. pekoed は待ち受けていなければ `127.0.0.1:<port>` で待ち受けを始め、**一度きりのトークン**(32 バイトの乱数、60 秒で失効、1 回使えば失効)を作る
3. pekoed は `<データディレクトリ>/gui/open-<乱数>.html`(0600、ディレクトリは 0700)に、`http://127.0.0.1:<port>/#t=<トークン>` へ移るだけの HTML を書き、そのパスを返す。CLI はそのファイルを `open`(macOS)/ `xdg-open` で開く
   - **トークンを argv に載せない**ため。`open 'http://…#t=…'` にすると、`open` とブラウザのプロセスの引数に URL が載り、同じユーザーのプロセスは `ps` で読めて、人より先にトークンを使える。ファイルのパスは見えても中身は denyRead で読めない(下の 4)
   - 推測: Jupyter も同じ理由で、トークン入りの URL ではなくリダイレクト用の HTML ファイルを開く作りにしている(記憶による。要確認)
   - HTML はリダイレクトしたら pekoed が消す(トークンを使った時点、または 60 秒後)
4. `<データディレクトリ>/gui/` を、エージェントの sandbox の `denyRead` と `Read` / `Edit` の deny(`internal/daemon/agentsandbox.go` の `agentSecrets`)と、hooks の sandbox の `DenyRead`(`internal/sandbox/policy.go` の `DefaultPolicy`)に足す。今の `human-token`・`http-token` と同じ扱い
5. ページはフラグメントのトークンで `POST /api/gui/session` を呼ぶ。pekoed は一度きりのトークンを失効させ、**セッションのトークン**(32 バイト)を返す。ページはフラグメントを消し、セッションのトークンを `sessionStorage` に置く
6. 以後の `/api/*` はすべて `Authorization: Bearer <セッションのトークン>` を要る。無い・違う・失効したものは 401 で、ページは「`pekoe gui` をもう一度」と出す
7. セッションのトークンは pekoed のメモリにだけ持つ(ディスクに書かない)。pekoed の再起動・`pekoe gui --revoke`・設定の有効期限(下の `session_ttl`)で失効する

一度きりのトークンを人より先に使われたとき(何かが HTML を読めた場合)、人のブラウザの交換は失敗する。そのとき pekoed は**全部のセッションのトークンを失効**させ、ページと `pekoed.log` に「別の誰かが先に使った」と出す。気づかずに乗っ取られたままにしない。

ただしこれは事後の手当てで、攻撃者は交換してから人のブラウザが交換に失敗するまでの間(人がブラウザに切り替わるまでの数百ミリ秒〜数秒)に allow や send を送れ、その操作は取り消せない。この穴は「HTML を読める」ことが前提で、HTML を読めるプロセスは同じ denyRead の `human-token` も読めて、ソケットから直接 `session.answer` を呼べる。だから今の穴を広げはしない(残る穴として脅威モデルの表に書く)。

フラグメント付きの URL はブラウザの履歴に残りうる。60 秒で失効し 1 回しか使えないので、後から履歴を読まれても使えない。

### 待ち受けの確認(すべてのリクエスト)

今の Web UI の `webGuard`(`internal/daemon/web.go`)にならい、WhoIs の代わりにトークンを見る `guiGuard` を置く。順と中身は同じではない(`webGuard` は Host を先に見て、`Origin` と `Sec-Fetch-Site` は書き込みのときだけ確かめる。`guiGuard` は下のとおり GET の API でも見る)。

1. **待ち受け**: `127.0.0.1` だけ(`::1`・`0.0.0.0` には開かない)。接続元がループバックでなければ 403(念のため)
2. **Host**: `127.0.0.1:<port>` と完全一致(ポートまで)でなければ 421。`localhost:<port>` も断り、オリジンを 1 つに決める(`sessionStorage` と `Origin` の確認を 1 つの値で済ませるため)。DNS rebinding で来た名前はここで落ちる
3. **Origin**: `Origin` があれば `http://127.0.0.1:<port>` と完全一致でなければ 403(GET でも)。`Sec-Fetch-Site` があれば、`/api/*` は `same-origin` でなければ 403
4. **CORS なし**: `Access-Control-Allow-*` を一切返さず、`OPTIONS` は 403。クロスオリジンのページは `Authorization` ヘッダ付きの要求をプリフライトで止められる
5. **トークン**: `/api/*` は `Authorization: Bearer` を定数時間で比べる(`crypto/subtle`、`http.go` の `guard` と同じ)。静的ファイル(`/`・`app.js`・`app.css`・`/theme.css`)はトークンなしで返す(中身はバイナリに埋め込んだ公開のもの)。`/theme.css` はユーザーのパレットの色だけで秘密を含まない
6. **書き込み**: 今の Web UI と同じく `Content-Type: application/json` を要る(`X-Pekoe-Web` の代わりに `Authorization` がカスタムヘッダの役をする)
7. **応答のヘッダ**: 今と同じ CSP(`default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`)・`X-Frame-Options: DENY`・`nosniff`・`Referrer-Policy: no-referrer`、API は `Cache-Control: no-store`。Chrome では `require-trusted-types-for 'script'` を足すと、`innerHTML` などへの文字列の代入をブラウザが断るので、XSS の守りをもう 1 枚にできる(Safari・Firefox は無視する。推測: 今の `app.js` は HTML を組まないので足しても動く、要確認)
8. **記録**: 拒否は理由つきで `pekoed.log` に `gui: 401 POST /api/… : …`。allow / deny / send / keys / 受信箱の答えは `via: gui` で残す(今の `webVia` は `web:<端末名>`。`internal/daemon/webinbox.go`)

トークンの総当たりは 256 ビットなので数えない。

## 脅威モデル

`docs/web.md` の表と同じ形式。前提も同じで、エージェントは人と同じ OS ユーザーで動き、`human-token` と秘密のファイルはエージェントと hooks の sandbox から読めない(`docs/web.md` の脅威モデルの 2〜3 行目、`docs/api.md` の「人間だけの操作」)。

| 脅威 | 対策 | 残る穴 |
| --- | --- | --- |
| **Mac 上のエージェント・hooks・dev server が `curl` で API を叩く**(自分の許可待ちに allow を返す) | `/api/*` はセッションのトークンが要る。一度きりのトークンは argv に載せず、0600 のファイル(`<データディレクトリ>/gui/`)を経由し、そのディレクトリをエージェントと hooks の sandbox の denyRead に足す。セッションのトークンは pekoed のメモリとページの `sessionStorage` にだけある | 一度きりのトークンの HTML を読めるプロセスは、人より先に交換でき、人のブラウザが失敗して全部を失効させるまでの間に allow / send を送れる(取り消せない。上の「認証」)。ただしそのプロセスは `human-token` も読めるので、ソケットからでも同じことができる。Claude Code 以外のエージェント・`sandbox = false`・`excludedCommands` のコマンドはファイルを読める(今の `human-token` と同じ穴で、広げない)。ブラウザの `sessionStorage` はセッションの復元のためにブラウザのプロファイル(推測: Chrome は `~/Library/Application Support/Google/Chrome/…/Session Storage`)に書かれうる。同じユーザーのプロセスはそこを読める。ただしブラウザのプロファイルが読めるなら全サイトの Cookie も読めるので、pekoe だけの穴ではない。気になるなら、エージェントの `denyRead` にブラウザのプロファイルを足す(未決事項)か、トークンをメモリだけに置く(再読み込みで入り直し) |
| **ブラウザで開いた他のページ(エージェントの dev server を含む)からの CSRF** | 資格情報は `Authorization` ヘッダだけで、ブラウザが自動で付ける Cookie を使わない。クロスオリジンのページはこのヘッダを付けられない(付けるとプリフライトになり、CORS を返さないので止まる)。`Origin` / `Sec-Fetch-Site` もポートまで確かめる | — |
| **DNS rebinding** | `Host` を `127.0.0.1:<port>` と完全一致で確かめる(421)。rebinding したページはトークンも持っていない | — |
| **ポートを区別しない Cookie・SameSite** | Cookie を使わない(案 B)。`sessionStorage` はオリジン(ポートを含む)ごとに分かれるので、`localhost:3005` のページから読めない | 案 A・C を選んだ場合はここが穴になる(上の「案の比較」) |
| **XSS(エージェントの文・画面・受信箱・報告に HTML やスクリプトを書く)** | 今の Web UI の規則を崩さない: API は JSON だけ、ページは `textContent` と自前の最小のマークダウンで組み、HTML を作る入口を使わない(`internal/daemon/webinbox_test.go` が `app.js` に `innerHTML` などが無いことを確かめている。GUI の JS にも同じテストを当てる)。リンクは `http` / `https` だけ。画面の色は役割名か確かめた `#rrggbb` だけ(`docs/web.md` の Run の節)。CSP はインライン script なし。グラフは外部のライブラリを使わず、SVG を `createElementNS` と数値の属性だけで組む。エージェントが置いた SVG を画像として出すときも `<img>` でだけ(インラインにしない) | XSS が通れば、そのタブのトークンで何でもできる(案 A でも同じ)。`sessionStorage` のトークンは持ち出せるので、`session_ttl` で失効させる。ダッシュボードを豊かにするほど組み立てのコードが増えるので、テストで縛る範囲(HTML を作る入口の禁止)を GUI の全 JS に広げる |
| **クリックジャッキング** | `frame-ancestors 'none'`・`X-Frame-Options: DENY` | — |
| **他の OS ユーザー** | `127.0.0.1` はほかのユーザーからも届くが、トークンが要る。トークンのファイルとディレクトリは 0600 / 0700 | 同じ Mac の管理者(root)は何でも読める(今と同じ) |
| **エージェントがブラウザを操作する**(Claude in Chrome・Playwright・Chrome DevTools Protocol などで、人の Chrome で開いている GUI のタブを操作し、allow を押す) | pekoe では塞げない。GUI は「人のブラウザのタブ = 人」とみなすので、そのタブを操作できるプロセスは人として振る舞える。人が気をつけることとして文書と `pekoe gui` の出力に書く: GUI を開くブラウザ(プロファイル)と、エージェントに操作させるブラウザを分ける(`[gui] browser` で別のブラウザを指定する、Chrome なら別のプロファイル)。CDP は Chrome を `--remote-debugging-port` 付きで起動しないと使えない(推測: 普段の Chrome は開いていない)。Claude in Chrome のような拡張は人のプロファイルの中で動くので、プロファイルを分けるのが要(未決事項) | **今の Web UI には無い新しい露出。** iPhone のブラウザは Mac のエージェントから操作できないが、Mac のブラウザは操作できる。同じプロファイルでエージェントにブラウザ操作を許していれば、GUI の認証は意味をなさない。確認シートや Touch ID(WebAuthn)を allow の前に挟むと、ブラウザ操作だけでは押せなくなる(推測: WebAuthn のユーザー確認はブラウザの外の OS のダイアログで、CDP や拡張からは押せない。要確認)。段階 1 で入れるかは未決事項 |
| **待ち受けていない間に固定ポートを横取りされる**(最初の `pekoe gui` の前・`--stop` の後・pekoed の再起動の間に、同じ Mac の任意のプロセスが `127.0.0.1:47778` を先に取る。同じオリジンになる) | `pekoe gui` は pekoed が自分で bind できたときだけブラウザを開く。bind に失敗したら(誰かが使っている)開かずに、使っているプロセスを調べる手がかり(`lsof -iTCP:47778`)とともにエラーを出す。これが安全の条件になるので、実装でテストする。セッションのトークンは `sessionStorage` に置くので、同じオリジンの偽のサーバーが後から来ても、閉じたタブのトークンは読めない(タブが開いたままなら、そのタブの JS は偽のサーバーへ API を送り、トークンを渡してしまう) | 開いたままのタブ・ブックマーク・履歴から開いた先に偽の GUI を見せられる(偽の確認シートで人を誘導できる。ただし pekoed への操作はできない)。開いたままのタブはトークンを偽のサーバーに送る(そのトークンは pekoed が止まっている間は使えず、再起動で失効する。メモリにだけ持つので)。`localStorage` に置く案を選ぶと、偽のサーバーが配る JS にトークンを読まれる。`port = 0`(毎回空いているもの)にすると横取りの的が無くなる代わりに、ブックマークが効かない(未決事項) |
| **悪意あるブラウザ拡張** | — | ページの DOM と `sessionStorage` を読める拡張は、トークンも画面も読める。pekoe では塞げない(今の Web UI の iPhone でも同じ種類の穴) |
| **ロックを解いたまま離れた Mac** | — | 開いているタブから allow / send ができる。TUI を開いたまま離れたのと同じ。`session_ttl` を短くすれば窓は狭まる |

## 今の Web UI とのコードの共有

### 分かったこと(コードを正とする)

依頼の前提では「今の Web UI は tsnet を使うので build tag `web` のビルドにだけ入っている」だったが、コードを読むと**ハンドラと静的ファイルは既定のビルドにも入っている**。build tag が付いているのは tsnet の待ち受け(`internal/daemon/webts.go`、`//go:build web`)とその代わり(`webts_off.go`、`//go:build !web`)だけで、`web.go`・`webinbox.go`・`webtapi.go`・`webimage.go`・`webicon.go` と `//go:embed webui` には tag が無い。既定のビルドでは誰も呼ばないだけ。

### 方針

- **ハンドラは共有する。** `webHandler` の中身を「API と静的ファイルを mux に登録する部分」と「入口の確認(guard)」に分け、guard だけを 2 つ持つ: tailnet 用の今の `webGuard` と、ローカル用の `guiGuard`(上の「待ち受けの確認」)。ハンドラは呼び出し元の情報(今の `webPeer`)を context から読むので、ローカルでは `{login: "local", device: "gui"}` のような値を入れる
- **面ごとの方針はハンドラに引数で渡す。** 今のハンドラには iPhone 向けの方針が入っている: `always` は 400、tapi への send は `allow_devices` が要る(`sendable`)、`confirm_tapi_send`。GUI は Mac の前にいる人なので、どれを TUI に合わせるかは未決事項。決まった値を `webOpts` に持たせ、ハンドラはそれを見る(今の `sendable` と同じやり方)
- **静的ファイルは段階で変える。**
  - 段階 0: 今の `webui/` をそのまま配る
  - 段階 1 から: デスクトップ向けの入口を別に作る(例 `guiui/index.html`)。画面の描画(Run の色)・自前のマークダウン・API の呼び出し・確認シートは、今の `app.js`(約 2200 行)から ES モジュールに切り出して両方から読む。ビルドの道具(bundler)は入れない(CSP の `script-src 'self'` のまま `<script type="module">` で読める)
  - 切り出しは今の Web UI のコードを動かすことになるので、別の PR で、今の Web UI の挙動を変えない形で行う(この文書の範囲外)
- **新しい API は段階 2 から。** 段階 0・1 は今の `/api/*` で足りる。段階 2 で足すもの(session.new・land・dev など)は、今の Web UI に出すかどうかと別に決め、GUI の mux にだけ登録できるようにする

### 既定のビルドに入れる

既定のビルドに入れる(build tag にしない)。理由:

- build tag `web` があるのは Tailscale のコードを既定のビルドに持ち込まないため(`docs/web.md` の「ビルド:既定は Tailscale なし」、CLAUDE.md)。ローカル GUI は `net/http` と `crypto/rand` だけで足り、新しい依存は無い
- ハンドラと静的ファイルはもう既定のビルドに入っている(上)。足すのは guard と待ち受けだけで、バイナリはほとんど増えない(推測)
- 目的 1 の「TUI が苦手な人」は、Homebrew や Release の既定のビルドを入れる人だと考えられる(推測)。tag にすると、その人たちに届かない
- 守りは build tag ではなく、既定で待ち受けないこと(下の「設定」: `pekoe gui` を打つまでポートを開かない)と認証で作る

`webts.go` 以外から `tailscale.com` を import しない決まりはそのまま守る。

## 設定と CLI

### config.toml(案)

```toml
[gui]
enable      = true        # false で `pekoe gui` を断り、待ち受けない
port        = 47778       # 127.0.0.1 のポート。0 で毎回空いているものを選ぶ
session_ttl = "12h"       # セッションのトークンの有効期限(最後に使ってから)
# browser   = "Safari"    # 開くブラウザ(macOS の `open -a`)。空なら既定のブラウザ
# allow_always      = false  # 許可待ちに always を出すか(未決事項)
# confirm_tapi_send = true   # tapi への send の前に確認シート(未決事項)
```

- **既定では待ち受けない。** `enable = true` は「`pekoe gui` で開いてよい」という意味で、pekoed は `gui.open` が初めて呼ばれたときに待ち受けを始める。人が一度も `pekoe gui` を打たなければ、ポートは開かない
- `port` の既定を決まった番号にするのは、ブラウザの履歴やログで分かりやすくするため。ただしトークンが要るので、ブックマークしても入り直しは要る。番号は Chrome 拡張の `47777` の隣にした(`[ports] base` でセッションの dev server に割り当てる範囲と重ならないことを実装で確かめる)。bind できなければ `pekoe gui` はブラウザを開かず、エラーと理由を出す(誰かが先に取っていれば偽の GUI かもしれないので。脅威モデルの「固定ポートを横取りされる」)
- `[http] port` と同じく、`port` を変えたら pekoed の再起動が要る(`config.RestartNeeded` に足す)。`session_ttl` などは次の要求から効く
- `[gui]` を足すと `config.Sections` に載るので、`internal/tips/tips.toml` に tip を足す(CLAUDE.md の TIPS の節。網羅テスト `TestTipsCoverSections` が見る)

### CLI(案)

| コマンド | 動き |
| --- | --- |
| `pekoe gui` | 待ち受けていなければ始め、一度きりのトークンでブラウザを開く(上の「認証」)。`human-token` が要るので、エージェントからは呼べない |
| `pekoe gui --no-open` | ブラウザを開かず、リダイレクト用の HTML ファイルのパスを出す(別のブラウザで開くとき)。URL そのものは出さない(端末の出力に残さない) |
| `pekoe gui --status` | 待ち受けているか、ポート、有効なセッションのトークンの数と最後に使われた時刻 |
| `pekoe gui --revoke` | すべてのセッションのトークンを失効させる(開いているタブは入り直しになる) |
| `pekoe gui --stop` | 待ち受けを止める(トークンも失効) |

- `gui.open` / `gui.status` / `gui.revoke` / `gui.stop` は token 必須のメソッドとして `internal/daemon/human.go` の `humanMethods` に載せる
- TUI からも開けるようにする(例: `:gui` コマンド)。キーを足すなら tip と網羅テストも(CLAUDE.md)
- `pekoe gui` は CLI のサブコマンドを足すので、`cmd/pekoe/tipcover_test.go` の対象になる(tip を足す)

## 未決事項(決めてほしいこと)

1. **認証の方式**: 推奨の案 B(フラグメント → Bearer、Cookie なし)でよいか。新しいタブで入り直しになる不便を許せるか(許せないなら `localStorage` に置くか、案 C を足すか)。`localStorage` は、待ち受けていない間に固定ポートを横取りしたプロセスに読まれる(脅威モデルの表)ので勧めない
2. **ブラウザ操作への備え**: エージェントにブラウザを操作させる(Claude in Chrome など)と GUI の認証は効かない。(a) 文書と `pekoe gui` の出力で「プロファイルを分ける」と案内するだけにするか、(b) allow / tapi への send の前に Touch ID(WebAuthn)を挟むか(段階 1 に入れるか)
3. **セッションのトークンの置き場**: `sessionStorage`(再読み込みは通る、新しいタブ・再起動で入り直し)か、メモリだけ(再読み込みでも入り直し、ディスクに残らない)か
4. **ブラウザのプロファイルを denyRead に足すか**: エージェントの sandbox の `denyRead` に Chrome・Safari のプロファイルを足すと、pekoe のトークンだけでなく全サイトの Cookie も守れる。ただし pekoe の役目を超え、エージェントの作業(ブラウザの自動操作など)を壊すかもしれない
5. **`always` を出すか**: Web UI は「恒久的な許可は端末の前で」として出さない。GUI は Mac の前にいる人なので TUI(`A`)に合わせて出すか、Web UI に合わせて出さないか
6. **tapi への send**: Web UI は `allow_devices` が要り、確認シートを挟む。GUI では TUI と同じく確認なしで送れるようにするか、Web UI と同じくシートを挟むか
7. **段階 3(レビュー面・run 窓・生の入力)を作るか**: 端末の再現や任意のキー・コマンドを送る経路で、XSS が通ったときの被害が大きくなる。GUI ではやらず「それは TUI で」とするか
8. **静的ファイルの切り出し**: 段階 1 で今の `app.js` を ES モジュールに切り出してよいか(今の Web UI のコードに手が入る)。代わりに GUI 側で書き直す(重複を許す)か
9. **既定のポート**: `47778` 固定か、`0`(毎回空いているもの)か。固定だと、待ち受けていない間に横取りされて、開いたままのタブ・ブックマークに偽の GUI を見せられる(脅威モデルの表)。`0` なら的は無くなるが、ブックマークは効かない(トークンが要るので、どちらでも入り直しは要る)
10. **`[gui] enable` の既定**: `true`(`pekoe gui` を打つまでは待ち受けない)でよいか、`false` にして明示的に有効にしてもらうか
