# ローカル GUI

pekoed が動いている Mac 自身のブラウザから、localhost で待ち受ける Web の画面で pekoe を操作する。

**状態**: 段階 0(入口だけ: 待ち受け・認証)と段階 1(MVP: デスクトップ向けの画面、GUI だけの `always`)を実装した(`internal/daemon/gui.go`・`cmd/pekoe/gui.go`・`internal/daemon/webui/gui/`、`pekoe gui`)。段階 2 は一部(New session・tapi の Reset・Interrupt、セッションの吹き出しと送信欄の `/ Commands`・`@ Files`。下の「段階 2 の一部」の 2 つの節)を実装し、残りと段階 4 の大半は設計で、まだコードは無い。

**見た目は [docs/gui-design.md](gui-design.md) のとおり。** 段階 1 は配置とデータのつなぎ込みまでで見た目は仮にしてあり、その後の PR でデザイン原則に沿って作り直した(タイトルバー・アイコンレール付きのサイドバー・タブ・ステータスライン、Dashboard・Session・許可と質問のカード、ダークとライト)。GUI のスタイルは `webui/gui/gui.css` にだけ書く(HTML の `style` 属性・`gui/` のスクリプトからの `style` の書き込み・外から読むフォントや CSS は無い。`TestGUIStylesOnlyInCSS`。例外は画面の色というデータで、エージェントの画面の `#rrggbb` の色は前から `lib/term.js` が CSSOM で入れている)。GUI のページは今の Web UI の `app.css` を読まない(共有するのは `/theme.css` のトークンと `lib/` のモジュール)。レビューで決まったことは下の「決まったこと」にまとめた。

根拠はファイル名で添える。コードを読んで確かめたことと、推測(「推測」「要確認」と書く)を分ける。

## 目的

1. **TUI が苦手な人に、GUI で TUI と同等の操作を提供する。** キーを覚えなくても、セッションを作る・画面を見る・指示を送る・許可に答える・受信箱に答える・land するまでをマウスでできるようにする
2. **HTML の表現力で、どこまで便利にできるかを検証する。** 端末の文字のマス目では描きにくいもの(工程表のダッシュボード、集計のグラフ、触るファイルの重なり、差分、証拠の画像)を、どこまで使いやすくできるかを試す場にする

### 今の Web UI・TUI との棲み分け

| | TUI | Web UI(`docs/web.md`) | ローカル GUI(この文書) |
| --- | --- | --- | --- |
| どこから | Mac の端末 | 外出先の iPhone(tailnet 経由) | Mac 自身のブラウザ |
| 解く課題 | 全部の操作(主役) | 離れている間の、許可・短い指示・受信箱への回答 | TUI と同じことを GUI で。リッチな表示の検証 |
| 届き方 | unix ソケット + `human-token` | tsnet の `:443`(localhost には開かない) | `127.0.0.1:<port>`(既定は毎回空いているポート) |
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
| **0. 入口だけ**(実装済み) | localhost の待ち受けと認証(下の「認証」)を作り、今の Web UI の静的ファイル(`internal/daemon/webui/`)と API をそのまま Mac のブラウザに配る。iPhone と同じ 8 つの機能が Mac で使える | 一番危ない部分(認証・脅威モデル)を先に、小さく固める。画面はまだスマホ向けのまま |
| **1. MVP**(実装済み。見た目は docs/gui-design.md) | デスクトップ向けの画面(下の「MVP」) | 「TUI が苦手な人が毎日の操作をできる」最小 |
| **2. TUI と同等(操作)**(一部実装: セッションの作成・tapi の Reset・Interrupt・セッションの吹き出し・送信欄の `/`・`@`) | セッションの作成(`session.new`、リポジトリの選択、issue から)、land(衝突予報つきの確認)、dev server の起動・停止、セッションの終了・worktree の削除(確認つき)、undo・再起動、片付け(`cleanup`)、tapi chat、PR の表示(チェック・レビュー・スレッドを読み、エージェントに送る)、issue の一覧、受信箱の dismiss | TUI のキー一覧(README の「TUI」、`docs/design.md`)の操作を一通り GUI で |
| ~~3. TUI と同等(重いもの)~~ **作らない** | レビュー面(差分を読み、行にコメントし、承認する。TUI では nvim の `r`)、run 窓(`!`、1 回のコマンドを人が打って対話する)、エージェントの画面への生の入力(TUI で `↵` で入る入力) | どれも端末の再現か、任意のキー・コマンドを送る経路で、XSS が通ったときの被害が大きい。GUI では作らず「それは TUI で」とする(下の「決まったこと」7) |
| **4. HTML の検証**(一部実装: Dashboard の集計と台帳) | 集計(`stats`)のグラフ(**実装済み**: Overview の日別のマージ数の棒と今日のトークン、`GET /api/stats`)、セッションの時系列(いつ動き・待ち・終わったか)、触るファイルの重なりの図(`conflicts.list`)、証拠(スクショ・ログ)の表示、デスクトップ通知(Notification API) | 目的 2。どれが TUI に戻す価値があるかも見る |

段階 2 と 4 は順番を入れ替えてよい(検証を早く見たいなら 4 の一部を 2 より先に)。

### MVP(段階 1)

- **概要・工程表のダッシュボード**: 件数、tapi の状態、依頼ごとのセッションと `impl → review → PR → CI → merge`、マージしてよいかの足りない条件。データは今の `GET /api/overview` と同じ(`internal/daemon/webinbox.go` の `webOverview`)。デスクトップの幅を使い、表で一度に見せる
- **セッション一覧と画面**: 左に一覧(リポジトリごと、待ちを上に)、右に選んだセッションの画面(`session.screen`、色つき。今の `/screen` と同じ最後 80 行)
- **send**: 選んだセッションへの短い指示(画像の添付を含む。今の `POST …/send` と同じ確認)
- **許可待ちへの回答**: allow / deny / always。確認シートで pekoe の要約と画面を並べ、判断は画面で行う(`docs/web.md` の「allow / deny の扱い」と同じ)。`always` は TUI の `A` に合わせて GUI では出す(「決まったこと」5)。API の側で GUI の入口だけ `always` を受け付けるのも段階 1 で入れる
- **質問へのキーパッド**: AskUserQuestion のメニューに答える(今の `POST …/keys`)。これが無いと質問に答えられず「毎日の操作」にならないので、MVP に入れる
- **受信箱への回答**: 事実が先・推奨を既定にしない・確認シート・`rev` の突き合わせ(`docs/web.md` の「受信箱(Inbox タブ)の扱い」と同じ)

段階 4 の一部として、Dashboard に集計と台帳を足した(**実装済み**。GUI の mux にだけ登録し、tailnet の Web UI には無い。どちらも既存のソケットのメソッドを GUI の Caller(人)で呼ぶ薄いハンドラで、新しいデーモンのメソッドは無い。`internal/daemon/guidash.go`、docs/web.md の API の表):

- `GET /api/stats`: `stats {days: 7}`。Overview の `Merged`(今日と 7 日のマージ数、日別の棒。過去の日は dim、今日は secondary、0 の日は border の 2px。数は各棒の `<title>` と図の `aria-label` で読める)と `Tokens today`(out / in / cache write / cache read。値は `pekoe stats` と同じ短縮で、正確な数は `title`)。pekoed は呼ばれるたびに transcript の新しい行を読むので、ページは TUI のダッシュボードと同じく 30 秒に 1 回までしか読まない
- `GET /api/ledger?closed=N`: `ledger.list {status: "all"}` を開いているものと、閉じた時刻の新しい順の `N` 件に分ける。Requests は開いている依頼を先頭に、閉じたもの(`done`・`dropped`・`chat`、pekoed が自動で閉じたものは `closed by pekoe`)を直近 5 件。`Open ledger` はその場で展開して閉じたものを 500 件まで出す(残りは `pekoe ledger --all`)。依頼の id は TUI と同じ `r123`。依頼の文は人の入力なので、ほかと同じく `textContent` で描く

### 段階 2 の一部: New session・tapi の Reset・Interrupt

TUI の `n`・`T`・(打っている間の)`esc` と同じ意味の操作を、どれも確認シートを通して足した(**実装済み**。`internal/daemon/guiact.go`・`interrupt.go`・`webui/gui/actions.js`、docs/web.md の API の表)。ハンドラは GUI の mux にだけ登録し、tailnet の Web UI には無い(人が Mac の画面から行う操作で、iPhone からの用途は今は無い。出すなら別に決める)。どれも GUI の Caller(人)で呼ぶ:

- **New session**(タイトルバーの `n New session`、入力欄の外で `n`。許可のカードでは `n` は Deny が先): `GET /api/repos`(`repo.list` のうちディレクトリの分かるもの)からリポジトリを選び、ブランチ名とプロンプトを入れ、`Continue` で確認シート(リポジトリとディレクトリ・ブランチ・エージェント・プロンプトの全文)を見せてから `POST /api/sessions` = `session.new`(TUI の `n`・`pekoe new` と同じ。人の呼び出しなので `hNew`)。ページが渡すのはリポジトリの id だけで、pekoed が知っているディレクトリに置き換える(パスは受け取らない)。エージェントは選ばない: TUI の `n` にも選択は無く、リポジトリの設定の `agent`(無ければ `claude`)になる。`issue から`(`from`)は TUI の `n` にあるが今回は入れていない。プロンプトの先頭の `!` は断らない(#254 で断るのは token なしの呼び出しだけ。GUI は人)。ただし `prompt = "paste"` のエージェント(opencode)では入力欄に打たれてシェルのコマンドとして動くので、確認シートに `!` の警告を出す(判定は送信欄と同じ `gui/shell.js`)。作ったらそのセッションのタブへ移る
- **tapi の Reset**(Dashboard の tapi パネルのヘッダの `T Reset`、入力欄の外で `T`): `POST /api/tapi/reset` = `tapi.reset`(TUI の `T`・`pekoe tapi reset`)。確認シートに、消えるもの(tapi の窓と会話の記録。元に戻せない)と残るもの(依頼(ledger)・受信箱・報告・送信の記録・セッション。tapi は次の起動で開いている依頼を読み直す)、tapi が動いている最中ならそのターンが止まることを書く。Reset の後の tapi は起動していない(TUI の `T` と同じ)。GUI に起動の操作はまだ無いので、TUI の `t` で起動する
- **Interrupt**(セッションと tapi のヘッダの `esc Interrupt`、入力欄の外で `esc`): `POST /api/sessions/{slug}/interrupt` = `session.interrupt`、`POST /api/tapi/interrupt` = `tapi.interrupt`(どちらも新しいメソッドで人だけ。docs/api.md)。pekoed がエージェントの窓に Escape を 1 回だけ送る。生のキーを GUI に出さず(docs/gui-design.md 決まったこと 5)名前付きの操作にした。tapi にも効かせる: TUI では tapi chat に打っている間も `esc` がそのまま tapi に届く(`internal/tui/pty.go` の `ptyTypingKey` はセッションと tapi chat の両方の pty に使う)ので、同じ操作を出す。送ってよいのはエージェントが動いているときだけ(hooks の状態が `running` で、画面の下端にターンのスピナー行 `esc to interrupt` があり、メニュー(`Enter to select`)が無い)で、それ以外は 409 で何も送らない。画面も見るのは、Claude Code が esc で止めたターンには Stop を出さず、1 回 Interrupt した後も hooks の状態が次の入力まで `running` のままになるため(画面を見ないと、2 秒後の 2 回目が idle の入力欄に届く): 許可待ち(hooks の状態、またはまだ報告されていない画面の許可メニュー。`send` と同じ `promptUp`)では Escape がエージェントの「No」になるので Deny を使ってもらう、質問待ちではメニューを抜けてしまう(キーパッドに `esc` がある)、idle では入力欄を消し、2 回続くと Claude Code の巻き戻しのメニューが開く。同じ理由で、同じエージェントへの Interrupt は 2 秒に 1 回まで(二度押しで 2 回目の Escape にしない)。`send` と同じ送信の排他の中で送るので、貼り付けと Enter の間には入らない。確認シートは OK にフォーカスを置く(`esc` → `↵` で送れる。シートそのものが確認)。確かめてから Escape を送るまでの数ミリ秒に許可ダイアログが出る隙間は、`send` と同じく塞ぎきれない(エージェントの描画は止められない)が、許可ダイアログでの Escape は「No」なので、許可する方向には倒れない

### 段階 2 の一部: セッションの吹き出しと送信欄の / @

docs/gui-design.md の決まったこと 6・7 で段階 2 以降に回した 2 つを足した(**実装済み**。`internal/daemon/sessionchat.go`・`transcript.go`・`webui/gui/chatpane.js`・`picker.js`・`compose.js`、docs/web.md と docs/api.md の表)。ハンドラは GUI の mux にだけ登録し、tailnet の Web UI には無い(Web UI のセッションの画面は Screen だけで、ファイル名の一覧を tailnet に出す用途も今は無い。出すなら別に決める)。どれも人として(GUI の Caller)、人間だけのデーモンのメソッドを呼ぶ:

- **セッションの吹き出し(Chat)**: Session の画面の枠の見出しの `Chat | Screen` で、エージェントの画面(Screen)と、Claude Code の会話を tapi chat と同じ吹き出しにしたもの(Chat)を切り替える。`GET /api/sessions/{slug}/transcript?after=` = `session.transcript`(2 秒ごとの差分)。返すもの・返さないものは tapi の `tapi.transcript` と同じ実装(`transcript.go` の `readChat`。人・エージェントのテキスト、ツールの名前と主な引数 1 つを 200 バイトまで、ターンの所要時間。tool_result・thinking・システムの注入・サブエージェントの行は返さない)を共有し、セッション用に重複させていない。tapi の Dashboard のパネルと Session の Chat は同じ部品(`chatpane.js`)。`#/tapi` の画面にも同じ切り替えがある(tapi の transcript を読む)
  - **前のページ**: 最初の読み込みは transcript の最後の 6 MB・400 件まで(tapi と同じ)。その前は答えの `before` のカーソルで `?before=` を読む(`readChatBefore`: そのカーソルで終わる 6 MB・400 件の新しい方)。ページは持っている分を `Show earlier` で 120 件ずつ出し、出し切ったら `Load earlier` で pekoed に前のページを読ませ、先頭まで読めば `start of the conversation` を出す。tapi の Dashboard のパネルにも効く(前は「ここには無い、TUI で」と出していた。docs/gui-design.md の決まったこと 7 の「tapi の吹き出しは `earlier` で前を読めるようにする」)。ページが持つのは 3000 件までで、それを超えて古いものを手放したら、そこから先は前を読めない(開き直す)と出す
  - **既定は Screen。** 理由: docs/gui-design.md の Session の 3 層で、画面(読むだけ)が主で「内容は素のまま」。許可と質問は画面で判断する(docs/web.md の「allow / deny の扱い」。Chat には tool_result が無く、許可のコマンドの前後も見えない)。Chat は経緯を読むための選択で、選んだものはそのタブの間だけ覚える(`sessionStorage`。テーマと同じく、ポートが毎回変わるので `localStorage` は効かない)。Chat を選んでいても、許可待ち・質問待ちの間は Screen を出し(見出しに理由)、終われば Chat に戻る。読めない(Claude Code でない・まだ会話が無い)ときも Screen を出して理由を書き、読めるようになれば Chat に戻る(docs/web.md の tapi chat の「落とし方」と同じ)
  - **読むパスの制限**: `session.event` は token なしで誰でも送れ、どのセッションも名乗れるので、hook が渡す `transcript_path` は使わない。pekoed が使うのは hook が報告した会話の id(`claude_session_id`。`[A-Za-z0-9_-]` だけの id として確かめたもの)だけで、ファイルはセッションの worktree のパス(とシンボリックリンクを解いたパス)の Claude Code のプロジェクトディレクトリ(`~/.claude/projects/<エンコードしたパス>/`、`$CLAUDE_CONFIG_DIR` があればその下)の中だけで探す。その id のファイルが無ければ、同じディレクトリのいちばん新しいもの(pekoe が引き取ったエージェント、hook がまだ来ていない)。別のセッションの会話の id を報告されても、そのファイルは別のディレクトリにあるので読まない(テスト `TestSessionTranscriptOnlyItsOwn`)。読むのは普通のファイルだけで、transcript のシンボリックリンクと、シンボリックリンクのディレクトリはたどらない(`openChat`。`O_NOFOLLOW`。tapi の transcript にも効く)
  - **残る穴**: 自分のセッションの id を書き換えられたプロセスは、同じ worktree のプロジェクトディレクトリの別の会話(前に同じパスで動いたセッションの会話)を見せられる。読める範囲は同じ worktree の会話だけで、人がそこで `/resume` で見られるものと同じ
  - **hooks のないエージェント(codex・opencode など)**: Claude Code の transcript を書かないので Chat は無い(404 `the agent is not Claude Code`)。Chat を選んでいても Screen と理由を出す。Claude Code かどうかは `[agents.claude]`(command が何でも。ラッパーを含む)か、command が `claude` のエージェント
- **送信欄の `/ Commands`・`@ Files`**: 入力を補うだけの道具。`/` を送信欄の先頭に打つ(または `/ Commands` を押す)と、そのセッションで使えるスラッシュコマンドの一覧、`@` を文頭か空白の後に打つ(または `@ Files` を押す)と、そのセッションの worktree のファイルの一覧が送信欄の上に出る。打った語で絞り、`↑` `↓` で選び、`↵` か `Tab` で送信欄の文字に入れる(`/name ` や `@path `。空白を含むパスは `@"a b.md"`。要確認: Claude Code が引用符つきの @ を読むか)。`esc` で閉じる。**一覧からは送らない。** 送るのはいつもの `⌘↵` / `Send` で、`!` で始まる文の確認シートなど送る前の確認は今のまま。tapi(`#/tapi`・Dashboard のパネル)には出さない(tapi の稼働ディレクトリはリポジトリではない)
  - **コマンドの一覧**: `GET /api/sessions/{slug}/commands` = `session.commands`。Claude Code の組み込み(pekoe が持つ一覧。版で増減するので近似で、本当の一覧はエージェントの窓の `/help`)、worktree の `.claude/commands/*.md`(と 1 段下のサブディレクトリ。名前はファイル名で、サブディレクトリは出どころに書く)と `.claude/skills/<name>/SKILL.md`、ユーザーの `~/.claude`(`$CLAUDE_CONFIG_DIR`)の `commands`・`skills`。説明は frontmatter の `description:`、無ければ最初の行。**推測で足りない分**: プラグインのコマンドとスキル、MCP サーバのプロンプト(`/mcp__…`)、`--add-dir` で足したディレクトリのものは出さない。Claude Code でないエージェントは一覧を出さず「Claude Code のエージェントだけ」と書く(codex のカスタムプロンプトなどは読まない)
  - **ファイルの一覧**: `GET /api/sessions/{slug}/files?q=` = `session.files`。worktree の `git ls-files --cached --others --exclude-standard`(.gitignore を尊重。git は pekoed のいつもの守り(gitx)で動かす)。git はシンボリックリンクの先に降りないので worktree の外は出ず、pekoed はファイルの中身を読まない。50000 件まで読み、`q` で絞って 50 件まで返す(多ければ「N of M」)。同じ worktree の `git ls-files` は 5 秒に 1 回まで
  - **信頼しないもの**: 会話・コマンドの名前と説明・ファイル名はエージェントやリポジトリが書いたもの。どれも `textContent` で描き、長い名前は 1 行で `…` に切る(全体は `title`)。制御文字を含むファイル名は pekoed が一覧から外し(数だけ返す)、ページも制御文字(と U+2028 / U+2029)を含む名前は送信欄に入れない(送っても pekoed の send が断る)。コマンドの説明の制御文字は pekoed が空白にする。worktree の `.claude` の下はシンボリックリンクをたどらない(エージェントが worktree の外のファイルを説明として読ませないため)

### 段階 2 の一部: サイドバーのビュー(レール)

左端のアイコンレール(docs/gui-design.md の「GUI で変えるもの」7・「アイコン」)で、サイドバーに出すビューを Sessions / Worktrees / Changes / Settings(下端)から 1 つ選ぶ。**今は枠だけ**: Sessions は前のサイドバーをそのまま移したもので、ほかの 3 つは「Not built yet」とそのビューが出す予定のものだけを出す空のビュー(何も読まず、何もできない)。中身は後で 1 ビューずつ別に作る。

- **選んだビューはそのタブの間だけ覚える**(`sessionStorage` の `pekoe-gui-side`。テーマ・Chat と Screen と同じ理由で `localStorage` は使わない)。既定の Sessions を選べば消す。URL のフラグメントは `#/`・`#/s/<slug>` のメインの行き先に使っているので、サイドバーのビューは入れない(リンクで開くのはメインの行き先だけ)。数字キーでの切り替えは無い(docs/gui-design.md の決まったこと 4)。クリック(と Tab で移ってのボタン)だけ
- **タイトルバーの欄は、出しているビューのもの。** ビューが `search`(欄の placeholder)を持てば、欄の文字はそのビューの `query` に渡る(Sessions は名前・ブランチ・リポジトリの絞り込みで、今と同じ)。持たないビューを出している間、欄は無効で `Nothing to filter in <ビュー>`。ビューを切り替えると欄は空に戻る。`:` は今のとおり欄にフォーカスする(無効なら何もしない)
- **コマンド欄(モックの `Search sessions, PRs, requests, or run a command`)は作らない。入口は上の「欄はビューのもの」まで。** 理由: (a) コマンドを実行する欄は、何を実行できて、どれが確認シートを通るか(New session・Interrupt・Reset のように、人だけの操作は今どれも確認シートを通す)を決めないと作れない。枠だけの空のコマンド欄は、押しても何も起きない入力欄になる。(b) PR や依頼の検索は、それを出すビュー(Changes・Requests)が無いと結果の行き先が無い。(c) 欄をビューに渡す形にしておけば、Changes などが自分の `query` で PR を絞れ、コマンドを足すときも「`:` で開いて、`↵` で何かする」の行き先を rail.js の `query` の隣に足せばよい。コマンドの実行は段階 2 の残りで、コマンドの一覧と確認の規則を決めてから
- 狭い幅(760px 以下)ではサイドバーを上に積み、レールはその上端の横 1 列にする(前は狭いとレールを隠していた。ビューが 1 つだけだったため)

#### ビューの差し込みの形(後でビューを作るとき)

1 ビュー 1 ファイル: `internal/daemon/webui/gui/views/<id>.js`。**ビューを作るセッションが触るのは自分のファイル(と、そのビューの見た目の gui.css の節・テスト・API)だけ**で、`rail.js`・`index.html`・`gui.js`・ほかのビューのファイルは触らない(4 つのボタンと `VIEWS` の並びはもうある)。今の空のビューは `notyet.js` の `notYet(…)` を 1 行で export しているので、ファイルごと本物に置き換える(`notyet.js` は消さない。最後の 1 つが置き換わったら消す)。

ファイルの default export は次の形のオブジェクト(`gui/rail.js` の頭のコメントと同じ。`TestGUIRailViews` が確かめる):

```js
export default {
  id: "worktrees",          // index.html のボタンの data-view と同じ
  title: "Worktrees",       // ボタンの aria-label・title と同じ。サイドバーの aria-label にも使う
  search: "Filter worktrees by …", // 省略可。あればタイトルバーの欄の placeholder で、query が呼ばれる
  mount(box, ctx) {},       // 見せるとき。box は rail.js が持つ空の要素で、ここに組む
  update(state) {},         // mount の直後(ページがもう読んでいれば)と、一覧を読み直すたび(3 秒ごと・画面の移動)
  query(text) {},           // search があるときだけ。欄の文字(trim 済み)。切り替えた直後は ""
  unmount() {},             // 隠すとき。始めたタイマーや fetch を止める。要素は rail.js が消す
};
```

- `state` はページが持っているもの: `{ sessions, tapi, overview, current }`(`/api/sessions` の一覧、`/api/tapi`、最後に読んだ `/api/overview`(まだ無ければ `null`)、メインで開いているもの(slug・`+tapi`・`null`))。ビューが自分で別の API を読むなら(`lib/api.js` の `api`)、mount で始めて unmount で止め、出していない間は読まない
- `ctx`: `{ go }`。`go(slug)` でメインにセッション(`TAPI` なら tapi)を開く。足すときは `gui.js` の `sideRail({ ctx })` に足す(ビューのファイルの外なので、足すセッションは 1 つに決める)
- 見た目は既存の部品を使う: 区画の頭は `side-head` と `label`(大文字の小さなラベル)、行は `srow`(開いている行は `sel` と `aria-current`)、空は `empty`。新しい class は gui.css のそのビューの節に、`TestGUIStylesOnlyInCSS` のとおり。描画は `lib/dom.js` の `el`・`txt`・`colored` で textContent だけ(`TestWebInboxScriptIsText` が `gui/views/` も見る)
- 状態の記号・色・語は `gui/states.js`(`stateBits`・`tapiState`・`readySet`)を使い、Sessions・Session のヘッダとそろえる(docs/gui-design.md の決まったこと 3)
- 人だけの操作(書き込み・削除・設定の書き込み)をビューに置くなら、今の操作と同じく確認シートを通し、GUI の mux にだけ登録する(Settings の書き込みは今の範囲の外)

MVP では画面のための新しい API を足さない。今の Web UI の `/api/*` で足りた(段階 0 でそのまま使えることを確かめ、段階 1 の画面もこれだけで作った)。段階 0 で足したのは認証のためのもの(ソケットの `gui.*` と `POST /api/gui/session`、下の「認証」)だけ。段階 1 で変えたのは、GUI の入口だけ `always` を受け付けること(`webSurface.always`)と、GUI の `/` でデスクトップ向けのページを返すこと(`webSurface.index`)。

段階 1 で作ったもの(`internal/daemon/webui/gui/`。見た目は docs/gui-design.md の「画面の構成」):

- 左にセッション一覧(tapi のカード、セッションが 2 つ以上のリポジトリは展開、1 つだけのものは `OTHER REPOSITORIES` に 1 行ずつ。`/api/sessions` が待ちを上に並べるので、待ちのあるリポジトリが上に来る)。フィルタのチップ(All / Needs you / Running)と、タイトルバーの絞り込み欄(`:`)
- タブ: Dashboard・tapi・開いたセッション(そのタブの間だけ覚え、× で閉じる)
- Dashboard(`#/`。`#/inbox` は受信箱の区画へ): 受信箱(Web UI と同じ規則: 事実が先・推奨を既定にしない・`rev` の突き合わせ・確認シート。先頭のカードが注意のフォーカス)、Overview(件数・マージ数と日別の棒・今日のトークン)、Pipeline(`impl → review → PR → CI → merge` と Status・マージの前に足りないもの)、Requests(依頼。開いているものと閉じた直近 5 件、台帳の展開。上の段階 4 の一部)。右に tapi の吹き出し(transcript)と送信欄(確認シートなし。`!` で始まる文だけシート)
- セッション(`#/s/<slug>`): ヘッダ(状態・鮮度)、色つきの画面(最後 80 行、選択中は止まる、一時停止)、右に pekoed が持つ事実(SESSION・CHANGES・LINKED・NOTICES)、下端に送達の表示と送信欄(`⌘↵` で送る。画像は選ぶ・貼り付け・ドロップで付け、画像つきは確認シート。送信欄の外に落としたファイルはページが受けず、ブラウザがそのファイルを開いてページを離れることもない(`gui/drop.js`)。`! Shell` と、`!` で始まる文の確認シート)。許可待ちの間は送信欄の代わりに許可のカード(Allow once / Always allow / Deny、`y` / `A` / `n`。どれも確認シートで pekoe の要約と画面を並べる)、質問待ちの間は質問のカード(キーパッド、`1`〜`9`)
- tapi(`#/tapi`): セッションと同じ画面で tapi の画面(と、段階 2 の一部で足した Chat)と質問のキーパッド。tapi の許可待ちには答えない(今の Web UI と同じく端末で)
- ダークとライト(OS の外観に従い、タイトルバーのボタンで切り替え。選択はそのタブの間だけ)
- 狭い幅では右の列を下に、さらに狭ければ一覧を上に積む(使えるが、スマホ向けの作りではない。スマホは Web UI)

### 後回しにするもの

- 段階 2 のうち上の節以外(レールの Worktrees / Changes / Settings の中身(枠は上の「サイドバーのビュー」)、検索欄のコマンド実行、右パネルの USAGE など)と、段階 4 の Dashboard の集計と台帳(上)以外(段階 3 は作らない)
- Touch ID(WebAuthn)を allow の前に挟むこと(脅威モデルの「エージェントがブラウザを操作する」と「決まったこと」2)
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

### 決定: 案 B

案 B にした(「決まったこと」1)。守りの要が「ブラウザがオリジンごとに分けるもの」(`sessionStorage`・`Authorization` ヘッダはページの JS しか付けられない)になり、ポートを区別しない Cookie の問題と CSRF を、仕組みで避けられるため。案 A・C は Cookie の届く範囲を人の行動(どの URL を開くか)で守ることになる。

流れ:

1. 人が `pekoe gui` を打つ。CLI は `human-token` を読んで(今の CLI と同じ)、新しいメソッド `gui.open`(token 必須)を呼ぶ
2. pekoed は待ち受けていなければ `127.0.0.1:<port>` で待ち受けを始め(`[gui] port`。既定の `0` は空いているもの)、**一度きりのトークン**(32 バイトの乱数、60 秒で失効、1 回使えば失効)を作る。bind に失敗したら、ファイルを書かずにエラーを返す(下の「待ち受けていない間に固定ポートを横取りされる」。CLI はブラウザを開かない)
3. pekoed は `<データディレクトリ>/gui/open-<乱数>.html`(0600 で新しく作る。ディレクトリは 0700)に、`http://127.0.0.1:<port>/#t=<トークン>` へ移るだけの HTML(`location.replace` と `meta refresh`)を書き、パス・ポート・`[gui] browser` を返す(トークンは返さない)。CLI はそのファイルを `open`(macOS、`browser` があれば `open -a <browser>`)/ `xdg-open` で開く
   - **トークンを argv に載せない**ため。`open 'http://…#t=…'` にすると、`open` とブラウザのプロセスの引数に URL が載り、同じユーザーのプロセスは `ps` で読めて、人より先にトークンを使える。ファイルのパスは見えても中身は denyRead で読めない(下の 4)
   - 推測: Jupyter も同じ理由で、トークン入りの URL ではなくリダイレクト用の HTML ファイルを開く作りにしている(記憶による。要確認)
   - HTML は pekoed が消す(トークンを使った時点、または 60 秒後。`pekoe gui --revoke` / `--stop` でも)。pekoed の起動時に、前の pekoed が残したものも消す
4. `<データディレクトリ>/gui/` を、エージェントの sandbox の `denyRead` と `Read` / `Edit` の deny(`internal/daemon/agentsandbox.go` の `agentSecrets`)と、hooks の sandbox の `DenyRead`(`internal/sandbox/policy.go` の `DefaultPolicy`)に足す。今の `human-token`・`http-token` と同じ扱い。ディレクトリは pekoed の起動時(`daemon.New`、sandbox を組む前)に 0700 で作る。Linux の bwrap は無いパスを塞げない(`internal/sandbox` は無いパスを飛ばす)ので、初めての `pekoe gui` で作るのでは遅い
5. ページ(`webui/app.js` の `guiSignIn`)は、まず `history.replaceState` でフラグメントを消してから、そのトークンで `POST /api/gui/session` を呼ぶ。pekoed は一度きりのトークンを失効させ、**セッションのトークン**(32 バイト)を返す。ページはそれを `sessionStorage` に置く。iPhone の Web UI ではフラグメントも `sessionStorage` のトークンも無いので、この処理は何もしない
6. 以後の `/api/*` はすべて `Authorization: Bearer <セッションのトークン>` を要る。無い・違う・失効したものは 401 で、ページは「`pekoe gui` をもう一度」と出し、それ以上 API を呼ばない(401 を返すのはローカル GUI だけで、tailnet の入口は 403)
7. セッションのトークンは pekoed のメモリにだけ持つ(ディスクに書かない)。pekoed の再起動・`pekoe gui --revoke` / `--stop`・設定の有効期限(下の `session_ttl`、最後に使ってから)で失効する。同時に持つのは 32 個まで(それを超えると古いものから消す)

一度きりのトークンを人より先に使われたとき(何かが HTML を読めた場合)、人のブラウザの交換は失敗する。そのとき pekoed は**全部のセッションのトークンを失効**させ、ページと `pekoed.log` に「別の誰かが先に使った」と出す。気づかずに乗っ取られたままにしない。使用済みのトークンは見分けるために 10 分覚えておく(それより後に来たものは「無効」として 401 にするだけ)。

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
7. **応答のヘッダ**: 今と同じ CSP(`default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`。`web.go` の `webCSP` を両方の入口で使う)・`X-Frame-Options: DENY`・`nosniff`・`Referrer-Policy: no-referrer`、API は `Cache-Control: no-store`。Chrome では `require-trusted-types-for 'script'` を足すと、`innerHTML` などへの文字列の代入をブラウザが断るので、XSS の守りをもう 1 枚にできる(Safari・Firefox は無視する)。段階 0・1 は今の Web UI と同じ CSP のままにした(下の「残っている要確認」)
8. **記録**: 拒否は理由つきで `pekoed.log` に `gui: <code> <method> <path>: <理由>`。送信の記録(`sends` の `via`)と受信箱の答え(`answer.via`)は `gui`(`webPeer.Via`。tailnet の `webVia` は `web:<端末名>`)。allow / deny / keys / send のログの行は Web UI と同じ形で、`local … (from gui)` と出る
9. **メソッド**: GET・HEAD・POST だけ。`/api/` の外への POST は 403

トークンの総当たりは 256 ビットなので数えない。

## 脅威モデル

`docs/web.md` の表と同じ形式。前提も同じで、エージェントは人と同じ OS ユーザーで動き、`human-token` と秘密のファイルはエージェントと hooks の sandbox から読めない(`docs/web.md` の脅威モデルの 2〜3 行目、`docs/api.md` の「人間だけの操作」)。

| 脅威 | 対策 | 残る穴 |
| --- | --- | --- |
| **Mac 上のエージェント・hooks・dev server が `curl` で API を叩く**(自分の許可待ちに allow を返す) | `/api/*` はセッションのトークンが要る。一度きりのトークンは argv に載せず、0600 のファイル(`<データディレクトリ>/gui/`)を経由し、そのディレクトリをエージェントと hooks の sandbox の denyRead に足す。セッションのトークンは pekoed のメモリとページの `sessionStorage` にだけある。`gui.open` は token 必須なので、エージェントは一度きりのトークンを作らせることもできない | 一度きりのトークンの HTML を読めるプロセスは、人より先に交換でき、人のブラウザが失敗して全部を失効させるまでの間に allow / send を送れる(取り消せない。上の「認証」)。ただしそのプロセスは `human-token` も読めるので、ソケットからでも同じことができる。Claude Code 以外のエージェント・`sandbox = false`・`excludedCommands` のコマンドはファイルを読める(今の `human-token` と同じ穴で、広げない)。ブラウザの `sessionStorage` はセッションの復元のためにブラウザのプロファイル(推測: Chrome は `~/Library/Application Support/Google/Chrome/…/Session Storage`)に書かれうる。同じユーザーのプロセスはそこを読める。ただしブラウザのプロファイルが読めるなら全サイトの Cookie も読めるので、pekoe だけの穴ではない。エージェントの `denyRead` にブラウザのプロファイルは足さない(「決まったこと」4: pekoe の役目を超え、エージェントのブラウザの自動操作を壊しうる) |
| **ブラウザで開いた他のページ(エージェントの dev server を含む)からの CSRF** | 資格情報は `Authorization` ヘッダだけで、ブラウザが自動で付ける Cookie を使わない。クロスオリジンのページはこのヘッダを付けられない(付けるとプリフライトになり、CORS を返さないので止まる)。`Origin` / `Sec-Fetch-Site` もポートまで確かめる | — |
| **DNS rebinding** | `Host` を `127.0.0.1:<port>` と完全一致で確かめる(421)。rebinding したページはトークンも持っていない | — |
| **ポートを区別しない Cookie・SameSite** | Cookie を使わない(案 B)。`sessionStorage` はオリジン(ポートを含む)ごとに分かれるので、`localhost:3005` のページから読めない | 案 A・C を選んだ場合はここが穴になる(上の「案の比較」) |
| **XSS(エージェントの文・画面・受信箱・報告に HTML やスクリプトを書く)** | 今の Web UI の規則を崩さない: API は JSON だけ、ページは `textContent` と自前の最小のマークダウンで組み、HTML を作る入口を使わない(`internal/daemon/webinbox_test.go` が `app.js` に `innerHTML` などが無いことを確かめている。GUI の JS にも同じテストを当てる)。リンクは `http` / `https` だけ。画面の色は役割名か確かめた `#rrggbb` だけ(`docs/web.md` の Run の節)。CSP はインライン script なし。グラフは外部のライブラリを使わず、SVG を `createElementNS` と数値の属性だけで組む。エージェントが置いた SVG を画像として出すときも `<img>` でだけ(インラインにしない) | XSS が通れば、そのタブのトークンで何でもできる(案 A でも同じ)。`sessionStorage` のトークンは持ち出せるので、`session_ttl` で失効させる。ダッシュボードを豊かにするほど組み立てのコードが増えるので、テストで縛る範囲(HTML を作る入口の禁止)を GUI の全 JS に広げる |
| **クリックジャッキング** | `frame-ancestors 'none'`・`X-Frame-Options: DENY` | — |
| **他の OS ユーザー** | `127.0.0.1` はほかのユーザーからも届くが、トークンが要る。トークンのファイルとディレクトリは 0600 / 0700 | 同じ Mac の管理者(root)は何でも読める(今と同じ) |
| **エージェントがブラウザを操作する**(Claude in Chrome・Playwright・Chrome DevTools Protocol などで、人の Chrome で開いている GUI のタブを操作し、allow を押す) | pekoe では塞げない。GUI は「人のブラウザのタブ = 人」とみなすので、そのタブを操作できるプロセスは人として振る舞える。人が気をつけることとして文書(README)と `pekoe gui` の出力(開くたびに 1 行)に書く: GUI を開くブラウザ(プロファイル)と、エージェントに操作させるブラウザを分ける(`[gui] browser` で別のブラウザを指定する、Chrome なら別のプロファイル)。CDP は Chrome を `--remote-debugging-port` 付きで起動しないと使えない(推測: 普段の Chrome は開いていない)。Claude in Chrome のような拡張は人のプロファイルの中で動くので、プロファイルを分けるのが要(「決まったこと」2 の (a)) | **今の Web UI には無い新しい露出。** iPhone のブラウザは Mac のエージェントから操作できないが、Mac のブラウザは操作できる。同じプロファイルでエージェントにブラウザ操作を許していれば、GUI の認証は意味をなさない。Touch ID(WebAuthn)を allow の前に挟むと、ブラウザ操作だけでは押せなくなるかもしれないが、段階 1 には入れず後回しにした。入れるときの要確認: (1) WebAuthn の RP ID は登録可能なドメインか `localhost` でなければならず、IP アドレスは使えない(推測)。今のオリジン `http://127.0.0.1:<port>` のままでは使えないので、オリジンを `localhost` に決め直す(Host の確認・`sessionStorage` のオリジンも変わる)などの見直しが要る。(2) Chrome の CDP には仮想の認証器(`WebAuthn.addVirtualAuthenticator`)があり、CDP で操作できるブラウザなら OS のダイアログを経ずに通せるかもしれない(推測。プロファイルを分ける案内は WebAuthn を入れても要る) |
| **待ち受けていない間にポートを横取りされる**(`[gui] port` を決まった番号にしたとき。最初の `pekoe gui` の前・`--stop` の後・pekoed の再起動の間に、同じ Mac の任意のプロセスがその番号を先に取る。同じオリジンになる) | 既定の `port = 0` では毎回空いているポートを選ぶので、前もって取っておく的が無い(「決まったこと」9)。決まった番号でも、`pekoe gui` は pekoed が自分で bind できたときだけブラウザを開く。bind に失敗したら(誰かが使っている)HTML を書かず、使っているプロセスを調べる手がかり(`lsof -nP -iTCP:<port> -sTCP:LISTEN`)とともにエラーを出す。これが安全の条件なので、テストで縛る(`internal/daemon/gui_test.go` の `TestGUIBindFailure*`、`cmd/pekoe/gui_test.go` の `TestGUIBindFailureOpensNoBrowser`)。セッションのトークンは `sessionStorage` に置くので、同じオリジンの偽のサーバーが後から来ても、閉じたタブのトークンは読めない(タブが開いたままなら、そのタブの JS は偽のサーバーへ API を送り、トークンを渡してしまう) | 開いたままのタブ・ブックマーク・履歴から開いた先に偽の GUI を見せられる(偽の確認シートで人を誘導できる。ただし pekoed への操作はできない)。開いたままのタブはトークンを偽のサーバーに送る(そのトークンは pekoed が止まっている間は使えず、再起動で失効する。メモリにだけ持つので)。`localStorage` に置く案を選ぶと、偽のサーバーが配る JS にトークンを読まれる(だから使わない)。既定の `port = 0` では、開いたままのタブが指す古いポートを pekoed の再起動の後に誰かが取ることはありうる(そのタブのトークンは再起動で失効している)。ブックマークは効かない(トークンが要るので、どちらでも入り直しは要る) |
| **悪意あるブラウザ拡張** | — | ページの DOM と `sessionStorage` を読める拡張は、トークンも画面も読める。pekoe では塞げない(今の Web UI の iPhone でも同じ種類の穴) |
| **ロックを解いたまま離れた Mac** | — | 開いているタブから allow / send ができる。TUI を開いたまま離れたのと同じ。`session_ttl` を短くすれば窓は狭まる |

## 今の Web UI とのコードの共有

### 分かったこと(コードを正とする)

依頼の前提では「今の Web UI は tsnet を使うので build tag `web` のビルドにだけ入っている」だったが、コードを読むと**ハンドラと静的ファイルは既定のビルドにも入っている**。build tag が付いているのは tsnet の待ち受け(`internal/daemon/webts.go`、`//go:build web`)とその代わり(`webts_off.go`、`//go:build !web`)だけで、`web.go`・`webinbox.go`・`webtapi.go`・`webimage.go`・`webicon.go` と `//go:embed webui` には tag が無い。既定のビルドでは誰も呼ばないだけ。

### 方針

- **ハンドラは共有する。**(段階 0 で実装)`webHandler` の中身を「API と静的ファイルを mux に登録する部分」(`webMux`)と「入口の確認(guard)」に分け、guard だけを 2 つ持つ: tailnet 用の今の `webGuard` と、ローカル用の `guiGuard`(上の「待ち受けの確認」)。ハンドラは呼び出し元の情報(`webPeer`)を context から読むので、ローカルでは `{login: "local", device: "gui", via: "gui"}` を入れる(`gui.go` の `guiPeer`)
- **面ごとの方針はハンドラに引数で渡す。** 今のハンドラには iPhone 向けの方針が入っている: `always` は 400、tapi への send は `allow_devices` が要る、`confirm_tapi_send`。面ごとの値は `webSurface`(`web.go`)に持たせ、`webMux` に渡す。段階 0 の GUI は tapi への send を `allow_devices` なし・確認シートなしで通す(TUI と同じ。「決まったこと」6)。`always` は段階 1 で `webSurface` に足す(「決まったこと」5)
- **静的ファイルは段階で変える。**
  - 段階 0: 今の `webui/` をそのまま配る
  - 段階 1(実装済み): デスクトップ向けの入口を別に作った(`webui/gui/index.html`・`gui.js`・`gui.css`。GUI の `/` だけがこれを返し、tailnet の `/` は今の `index.html`。tailnet の mux は `/gui/*` を 404 にして配らない: `webSurface.gui`)。画面の描画(Run の色、`lib/term.js`)・自前のマークダウン(`lib/md.js`)・API の呼び出しとサインイン(`lib/api.js`)・確認シート(`lib/sheets.js`)・受信箱のカード(`lib/inbox.js`)・工程表の部品(`lib/overview.js`)・tapi chat の吹き出し(`lib/chat.js`)・画像の添付(`lib/images.js`)・行の組み立て(`lib/fmt.js`・`lib/dom.js`)を、今の `app.js` から ES モジュールに切り出して両方から読む。ビルドの道具(bundler)は入れない(CSP の `script-src 'self'` のまま `<script type="module">` で読む)。`app.js` は電話の画面の組み立て(ルーティング・iOS のビューポート・一覧・詳細)だけを持つ
  - 切り出しは段階 1 で行った(「決まったこと」8)。今の Web UI の挙動と見た目は変えていない(`app.css` は触っていない。切り出しの前後で、同じ偽のデータの電話の幅の画面を撮り比べ、一覧は画素まで同じだった)。JS のテスト(`TestWebMarkdownBold`・`TestWebMarkRewound`)はモジュールを node で import して動かす
  - 段階 0 で今の `app.js` に足したのは、フラグメントのトークンの交換・`Authorization` ヘッダ・401 のときのサインアウトの表示だけ(上の「認証」5・6)。tailnet ではどれも働かない
- **新しい API は段階 2 から。** 段階 0・1 は今の `/api/*` で足りる。段階 2 で足すもの(session.new・land・dev など)は、今の Web UI に出すかどうかと別に決め、GUI の mux にだけ登録できるようにする

### 既定のビルドに入れる

既定のビルドに入れる(build tag にしない)。理由:

- build tag `web` があるのは Tailscale のコードを既定のビルドに持ち込まないため(`docs/web.md` の「ビルド:既定は Tailscale なし」、CLAUDE.md)。ローカル GUI は `net/http` と `crypto/rand` だけで足り、新しい依存は無い
- ハンドラと静的ファイルはもう既定のビルドに入っている(上)。足すのは guard と待ち受けだけで、バイナリはほとんど増えない(推測)
- 目的 1 の「TUI が苦手な人」は、Homebrew や Release の既定のビルドを入れる人だと考えられる(推測)。tag にすると、その人たちに届かない
- 守りは build tag ではなく、既定で待ち受けないこと(下の「設定」: `pekoe gui` を打つまでポートを開かない)と認証で作る

`webts.go` 以外から `tailscale.com` を import しない決まりはそのまま守る。

## 設定と CLI

### config.toml

```toml
[gui]
enable      = true        # false で `pekoe gui` を断り、待ち受けない
port        = 0           # 127.0.0.1 のポート。0(既定)で待ち受けを始めるたびに空いているものを選ぶ
session_ttl = "12h"       # セッションのトークンの有効期限(最後に使ってから)
# browser   = "Safari"    # 開くブラウザ(macOS の `open -a`)。空なら既定のブラウザ
```

- **既定では待ち受けない。** `enable = true`(既定。「決まったこと」10)は「`pekoe gui` で開いてよい」という意味で、pekoed は `gui.open` が初めて呼ばれたときに待ち受けを始める。人が一度も `pekoe gui` を打たなければ、ポートは開かない
- `port` の既定は `0`(「決まったこと」9)。`pekoe gui` は `gui.open` の答えでポートを知る。決まった番号にもできるが、そのときは待ち受けていない間に横取りされうる(脅威モデル)。bind できなければ `pekoe gui` はブラウザを開かず、エラーと理由を出す
- `port` は待ち受けを始めるときに読む(最初の `pekoe gui`、または `pekoe gui --stop` の後の `pekoe gui`)。`[http] port` と違い pekoed の再起動は要らない(待ち受けを `gui.open` が始めるので、`config.RestartNeeded` には足さない)。`session_ttl`・`browser` は次の要求から効く。`enable = false` は次の `gui.open` から断る(待ち受け中のタブはそのまま使えるので、止めるなら `pekoe gui --stop`)
- `[gui]` は `config.Sections` に載るので、`internal/tips/tips.toml` に tip(`local-gui`)を足した(CLAUDE.md の TIPS の節。網羅テスト `TestTipsCoverSections` が見る)
- `pekoe config check` は `port` の範囲と `session_ttl` の書き方を確かめる。`pekoe config show` は `enable` と `session_ttl` を使っている値で出す

### CLI

| コマンド | 動き |
| --- | --- |
| `pekoe gui` | 待ち受けていなければ始め、一度きりのトークンでブラウザを開く(上の「認証」)。`human-token` が要るので、エージェントからは呼べない |
| `pekoe gui --no-open` | ブラウザを開かず、リダイレクト用の HTML ファイルのパスを出す(別のブラウザで開くとき)。URL そのものは出さない(端末の出力に残さない) |
| `pekoe gui --status` | 待ち受けているか、ポート、有効なセッションのトークンの数と最後に使われた時刻 |
| `pekoe gui --revoke` | すべてのセッションのトークンを失効させる(開いているタブは入り直しになる) |
| `pekoe gui --stop` | 待ち受けを止める(トークンも失効) |

- 5 つとも段階 0 で実装した。`--json` で答えをそのまま出す
- `gui.open` / `gui.status` / `gui.revoke` / `gui.stop` は token 必須のメソッドとして `internal/daemon/human.go` の `humanMethods` に載せた
- `pekoe gui` は開くたびに「エージェントに操作させないブラウザ(プロファイル)で開く」と 1 行出す(「決まったこと」2)
- TUI から開く入口(例: `:gui` コマンド)は段階 0 には入れていない。足すなら tip と網羅テストも(CLAUDE.md)
- `pekoe gui` は CLI のサブコマンドなので `cmd/pekoe/tipcover_test.go` の対象で、tip `local-gui` に載せた

## 決まったこと

レビューで決めた(すべて推奨どおり)。

1. **認証の方式**: 案 B(フラグメント → Bearer、Cookie なし)。新しいタブ・ブラウザの再起動で入り直しになるのは許す。`localStorage` は使わない(待ち受けていない間にポートを横取りしたプロセスに読まれる)
2. **ブラウザ操作への備え**: (a) 文書(README)と `pekoe gui` の出力で「GUI はエージェントに操作させないブラウザ・プロファイルで開く」と案内するだけ。Touch ID(WebAuthn)は段階 1 には入れず後回し。入れるときの要確認(127.0.0.1 のオリジンでは使えずオリジンの見直しが要ること、CDP の仮想認証器)は脅威モデルの表に書いた
3. **セッションのトークンの置き場**: `sessionStorage`(再読み込みは通る、新しいタブ・再起動で入り直し)
4. **ブラウザのプロファイルは denyRead に足さない**(pekoe の役目を超え、エージェントの作業を壊しうる)
5. **`always`**: GUI では許可待ちに `always` を出す(TUI の `A` に合わせる)。段階 0 は今の Web UI の画面を使い、そこに `always` のボタンは無いので、API の側で GUI の入口だけ `always` を受け付けるのは段階 1 に回した(使う画面の無い入口を先に開けない)。**段階 1 で実装した**: `webSurface.always` を GUI の mux にだけ立て、`POST /api/sessions/{slug}/answer` は GUI からだけ `always` を受け付ける(「always」という語だけ。`a`・`A` などのキーの別名は GUI でも 400)。tailnet の Web UI は今までどおり 400(`TestGUIAnswer`・`TestGUIAlwaysOnlyOnGUI`・`TestWebNoAlways`)。画面では deny / allow / always の 3 つのボタンを並べ、always も確認シートを通し、「この先の同じ種類の許可も聞かれなくなる」ことと画面で範囲を確かめることを書く
6. **tapi への send**: GUI では確認シートなしで送れる(TUI と同じ)。Web UI の `allow_devices` の条件は GUI には効かない(`GET /api/tapi` は `sendable: true`・`confirm_send: false`。`TestGUITapiSend`)。Web UI 側の挙動は変えていない
7. **段階 3(レビュー面・run 窓・生の入力)は GUI では作らない。** 「それは TUI で」とする
8. **静的ファイルの切り出し**: 段階 1 で今の `app.js` を ES モジュールに切り出す
9. **既定のポート**: `0`(毎回空いているもの)。`pekoe gui` は `gui.open` の答えでポートを知る
10. **`[gui] enable` の既定**: `true`(`pekoe gui` を打つまでは待ち受けない)

### 残っている要確認

- Safari・Firefox で、`file://` の HTML から `http://127.0.0.1` への `location.replace` / `meta refresh` が通るか(Chrome では確かめた)
- Jupyter がリダイレクト用の HTML を開く作りにしている理由(上の「認証」3。記憶による推測)
- `require-trusted-types-for 'script'` を足すか: 段階 1 では足さず、CSP は今のまま(依頼で「CSP は今のまま」とした)。HTML を作る入口の禁止は、`webui/` の下のすべての JS をテストで縛る形に広げた(`TestWebInboxScriptIsText`)。足すかは見た目の作業の PR か段階 2 で決める
- WebAuthn を入れるときの、オリジンと CDP の仮想認証器の件(「決まったこと」2)
