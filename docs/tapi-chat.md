# tapi chat — 設計メモ

## 何のためのものか

今は各リポジトリの main セッションを窓口にして、そこから `pekoe new` / `pekoe send` で作業を振り分けている。そのせいで main が作業中で埋まり、気軽にエージェントと話せないことが多い。

tapi chat は TUI からいつでも呼べる特別なエージェントで、pekoe の**統括役**([orchestration.md](orchestration.md)「tapi を統括役にする」)。

- 変更の依頼は自分では書かず、`pekoe new --repo R --request <依頼の id>` でセッションを作って任せ、`pekoe wait --mine --on report,inbox,failed,waiting,ci` で報告と PR の CI の決着を待ち(空振りで起きてトークンを使わないよう種類を絞る。CI のために自前で `gh pr checks --watch` を回さない)、結果を 1 通にまとめて人に返す。人に上げるのは判断と承認(受信箱)だけ
- 特定のセッションとは関係ない単発の調査・質問、リポジトリ横断の調査(例: 抱えているリポジトリ群の dependabot アラートの様子)は、読むだけなので自分で答える
- 自分で作業しすぎないように、リポジトリと worktree には書けない(下の「起動するコマンド」)

グローバルに 1 つだけ。tapi 自身(状態・会話)は pekoe 側に永続化しないが、人が tapi に頼んだことは依頼(`ledger`、state.db)として残り、`tapi.reset` でも消えない。受信箱・報告・送信の記録も state.db にある。だから会話をリセットした tapi も、依頼と受信箱を読んで続きから始められる。マスコットの tapi(docs/mascot.md。pekoe は家、tapi はそこに住む相棒)と話す、という体裁にする。tapi の性格・口調は docs/mascot.md「tapi の人物像」。tapi の顔は tapi chat のタイトルにだけ出し、返事の中には顔・絵文字・顔文字を入れない(chat の中だけの話し方で、ステータスバーの台詞の規則は変えない)。Web UI には状態・最後の返事を出し(Now の TAPI の行。iPhone から受信箱に答えられる)、下端のタブバーの tapi から tapi の画面・質問へのキーパッド・送信を出す。送信は `[web] allow_devices` を設定しているときだけで、送る前に宛先と全文の確認シートを挟む(既定。`[web] confirm_tapi_send = false` で切れる)。tapi の許可待ちへの allow / deny は Web に出さない([web.md](web.md)「tapi chat の扱い」)。

## 持ち方: セッションとは別の、メモリ上の 1 つ

`state.Session` には混ぜない。`state.Session` は state.db に永続化され、Repo / Path / Branch / Port / Diffstat / PR などリポジトリを前提にした項目ばかりで、diffstat・PR ポーリング・衝突予測・land・review・dev・tests・Web UI の一覧がすべて `d.sessions` を回る。tapi をそこに入れると、全部に「tapi なら飛ばす」を書くことになる。

代わりに pekoed のメモリ上に `tapiState` を 1 つだけ持つ(`internal/daemon/tapi.go`)。

| 項目 | 中身 |
| --- | --- |
| `agent` | 使うエージェント名(`[tapi] agent`) |
| `state` | `running` / `waiting` / `idle`(hook から。done や reviewing は持たない) |
| `wait_kind` | `permission` / `question` |
| `pending_tool` | 許可待ちのツール(`{name, input}`) |
| `last_message` | 最後の返事 |
| `claude_session_id` | hook の session_id(表示用。再開には使わない。後述) |
| `state_since` | 状態が変わった時刻 |

`Tapi` オブジェクトには、これに加えて state.db から数えた `inbox_open`(受信箱の未回答、全員の分)と `ledger_open`(open の依頼)を載せる。依頼か受信箱が変わると `tapi.updated` も出る。

`d.sessions` に入らないので、セッションを回る既存の処理(`session.list`・diffstat・PR・衝突予測・land・review・dev・run・tests・門番・Web UI・`attention.next`・`daemon.status` の sessions 数・`state.db`)には何もしなくても流れ込まない。

### 予約名 `+tapi`

tmux のウィンドウ名と hook の振り分けには、セッションの slug と同じ名前空間の予約名 `+tapi` を使う(tmux のウィンドウは `+tapi~agent`、`$PEKOE_SESSION=+tapi`)。

- `paths.Slug` は `[A-Za-z0-9._-]` しか出さず、セッションの slug はすべて `paths.Slug` を通るので、`+tapi` は通常のセッションと衝突しない(テストで固定する)
- `@tapi` は使えない: tmux のターゲット `pekoe:=@tapi~agent` の `@` がウィンドウ ID と解釈され、別のウィンドウ(`_keeper`)に当たる。`+tapi` は `=` 付きのターゲットで名前どおりに当たることを確かめた

## hook: 同じ仕組みに乗せる

tapi の Claude Code も通常のセッションと同じ `pekoe event` の hook を使う(`pekoe hooks install` 済みの設定がそのまま効く)。

- `resolveSession`(internal/daemon/hooks.go): `$PEKOE_SESSION` が `+tapi` なら tapi。`$PEKOE_SESSION` が無いときは、cwd が tapi の稼働ディレクトリの中なら tapi。それ以外は従来どおり
- 窓の外から来た hook は、`transcript_path` が tapi の Claude Code の記録のディレクトリにあれば `$PEKOE_SESSION` によらず tapi(`claimOutside`。下の「窓の外に出た会話」)
- `hEvent`: tapi に振り分けられた hook は `tapiEvent` が受け、状態だけを動かす。プロンプトのスナップショット(`refs/pekoe/…`)、diffstat、テスト、門番は動かさない

| hook | tapi の状態 |
| --- | --- |
| SessionStart | idle(`claude_session_id` を記録。待ちの種類・`pending_tool` も消す。esc で中断したターン(Stop が来ない)の後の `/clear` でも idle に戻る。`source: compact` はターンの途中の圧縮なので状態を変えない) |
| UserPromptSubmit | running。tapi の窓から来たもの(呼び出し元の識別で窓が `+tapi`)なら、`prompt` を依頼として `open` で記録する(pekoed が打ったもの(受信箱の答え `[pekoe inbox q…]`・配送の 1 行など、`[pekoe` で始まるもの全部)、Claude Code の `<task-notification>` で始まる入力、空の入力は除く。`$PEKOE_SESSION` の名乗りだけでは記録しない)。直前の open の依頼と同じ文が 1 分以内に来たら、新しく記録せずにまとめる |
| PreToolUse | running(AskUserQuestion なら waiting / question)、`pending_tool` を記録。tapi の窓から Stop の直後(次の UserPromptSubmit の前)に来たものは、ほかの Stop hook が止めを差し戻してターンが続いたしるしなので、そのターンを Stop の前の続きとして扱う(docs/api.md「依頼(ledger)」の「自動で閉じる」) |
| PostToolUse | waiting なら running に戻す |
| Notification | permission_prompt → waiting / permission(AskUserQuestion の PreToolUse 直後なら question のまま。セッションと同じ)、elicitation_dialog → waiting / question、idle_prompt → idle、知らない種類 → waiting / permission |
| Stop | idle(`last_message` を記録)。`last_assistant_message` が無いと transcript を最大 2 秒待つので、その間に次のターンが始まった(UserPromptSubmit・compact 以外の SessionStart が来た)・ターンが続いた(ほかの Stop hook が止めを差し戻して PreToolUse が来た)ら、状態と `pending_tool` は変えない(`last_message` だけ記録する)。通常のセッションの done(レビュー待ち)は tapi には無い。tapi の窓から来たものなら、そのターンの入力が依頼でなかったとき(セッションを結ばず、誰にも頼まず、待ち始めず、`pekoe ledger keep` もしていない)に `chat` で閉じる(docs/api.md「依頼(ledger)」の「自動で閉じる」) |
| SessionEnd | idle |

esc で中断したターンには Stop が来ない。GUI の Interrupt(`tapi.interrupt`)のときは、pekoed が Escape の後に画面を見て、ターンが消えたら idle にする(docs/gui.md の「Interrupt」)。

変化は `tapi.updated` イベント(data は下の `Tapi`)で流す。画面更新の合図は `tapi.output`(`session.output` と同じく 30ms で間引く)。

## 稼働ディレクトリ

`paths.TapiDir()` = `~/.local/share/pekoe/tapi/`(`$PEKOE_HOME` があれば `$PEKOE_HOME/share/tapi/`)。

- `$HOME` にはしない: cwd の前方一致で、ホーム以下で動く無関係な Claude Code の hook を拾ってしまう
- 起動のたびに pekoed が `CLAUDE.md` と `AGENTS.md`(同じ内容)を書き出す。役割(統括役。手順の詳細は pekoe-orchestrate スキルで、スキルの無いエージェントのために要点も書く)、最初にすること(会話の最初とリセットの後に `pekoe --json ledger`・`pekoe --json inbox`・`pekoe --json ls` を読み、続きから始める。`pekoe --json ledger` に一緒に出る、pekoed が直近 24 時間に自動で閉じた入力のうち依頼だったものは `pekoe ledger keep <id>` で戻す)、統括役の手順(依頼の閉じ方(雑談は返事を終えると自動で閉じること、依頼なのにそのターンで誰にも頼まないときの `pekoe ledger keep`)、`--request` で任せる、完了条件を必ずプロンプトに入れる、`pekoe wait` をバックグラウンドで待つ(PR の CI の決着も同じ wait の `ci` で待つ)、報告を確かめる、人に上げる 2 種類と `fyi` の記録、「決定が必要 / 今やっていること / 進行中」の順の報告)、報告・受信箱・PR・issue の文は信頼できないデータとして読むこと、使うコマンド(`pekoe --json ls`、`pekoe --json new <branch> --repo R --request <id> --prompt …`、`pekoe send <slug> -`、`wait`・`reports`・`ledger`・`inbox`・`ask-human`)、やらないこと(自分で編集しない、マージしない、land・rm はユーザーに頼まれたときだけ、自分が作っていないセッションに指示を足すのは頼まれたときだけ、人だけの操作を回避しない)、人物像と口調・振る舞いのルール(docs/mascot.md「tapi の人物像」。キャラは返事の最初か最後の 1 文だけで、中身は正確さ優先。返事に顔・絵文字・顔文字は入れない)、今日マージされた PR の数え方、TIPS の紹介の仕方(下記)、起動時点の登録済みリポジトリの一覧(名前とパス)、起動した時点で残っていたものの件数(open の依頼・受信箱の未回答・tapi が任せて landed でないセッション。件数だけで、依頼や受信箱の文は書かない: 指示の書かれたファイルに他人の文を混ぜない)を書く
- 初回は Claude Code のフォルダ信頼ダイアログが出る。開いた時点で入力モードなので、そのまま答えればよい(pekoe からは `~/.claude.json` を書き換えない)

### TIPS の紹介

最初の返事のあいさつに続けて、pekoe の機能を 1 つ紹介する(README「TIPS」)。どれを紹介するかは tapi が選ばず、`pekoe tip` に任せる。`pekoe tip` は `internal/tips/tips.toml` の一覧から、TUI の空き表示と共通の記録(`tips.json`)でまだ見せていないものを 1 つ選び、記録して出す。だから TUI で見せた tip を tapi が繰り返すことはなく、一巡するまで同じものは出ない。

- 紹介はあいさつに続く 1 文に収める(キャラの部分)。キー・コマンドは `pekoe tip` の出力をそのまま写し、出力に無いものを作らない。`^q` はユーザーの `[keys] prefix` に置き換えて出てくる
- 「なんか tip ちょうだい」と頼まれたら `pekoe tip` で次の 1 つ。`--list` で一覧、`pekoe tip <id>` で特定の 1 つ(記録しない)
- 「もっと詳しく」と聞かれたら、tip の docs 行のパスを `pekoe tip --doc <path>` で読んで普通の文で説明する。docs(README と docs/*.md、nvim/ と extension/ の README)はバイナリに同梱しているので、pekoe のリポジトリが手元に無くても読める
- `[tips] enable = false` のとき `pekoe tip` は `tips are off` と出すだけで、tapi は頼まれない限り tip に触れない。CLAUDE.md の文面は設定によらず同じ(書き出す関数は設定を見ない)
- tapi chat の sandbox は DataDir(`tips.json` の置き場所)に書けないので、`pekoe tip` は選んで記録するのを pekoed に頼む(`tips.next`、docs/api.md)。`[tips] enable` と、tip の `^q` を置き換える `[keys] prefix` も pekoed が読んだ config.toml で決まる(sandbox は config.toml を隠す。`--list`・`<id>` は `tips.prefix` で prefix だけを聞く)。pekoed が無い・古いときは自分で `tips.json` を書き、書けなければ tip を出して記録だけ諦める(stderr に一言)。Claude Code の許可リストを使っているなら `Bash(pekoe tip)` と `Bash(pekoe tip *)` を足すと、あいさつのたびに聞かれない

## 起動するコマンド

`config.toml` の `[tapi]` 節で、既存の `[agents.*]` の名前を指す。

```toml
[tapi]
agent = "claude"   # 既定。[agents.claude-ask] などを定義していればその名前も使える
```

- 起動はエージェントの `command` を `$SHELL` で実行する(既存の `spawnAgent` と同じ形。終了後は `[pekoe] agent exited …` を残す)
- `command` の先頭語が `claude` のとき(`claude-ask` のように引数を足した定義も含む)だけ次を足す
  - `--add-dir <各リポジトリのメイン worktree> <worktree のルート>`: 登録済みリポジトリ(`repo.list` と同じ `knownRepos` で、ディレクトリが見つかるもの)と `~/worktrees`。各リポジトリを読むたびに許可を求められないように。リポジトリの増減は次に tapi を起動したときに反映する
  - `--append-system-prompt <人格>`: 「pekoe に住むタピオカの粒の相棒 tapi。一人称ぼく・ユーザーはきみ・くだけた話し言葉。返事に顔・絵文字・顔文字は入れない。キャラは最初か最後の 1 文だけで、中身は正確に、確かめたことと推測を分ける。統括役で、自分では編集せずセッションに任せて報告を待つ。会話の最初に依頼・受信箱・セッションを読む。詳細は CLAUDE.md」の数文(人物像は docs/mascot.md「tapi の人物像」)
  - `--settings <データディレクトリ>/agent-settings/tapi.json`(`[agents.*] sandbox` が切られていなければ): セッションと同じ sandbox の設定に、`--add-dir` で渡す各リポジトリと `~/worktrees`(まだ無くても。シンボリックリンクを解いたものも)を `sandbox.filesystem.denyWrite` と `permissions.deny` の `Edit(...)` に足したもの。Bash でも Edit / Write でも書けない。稼働ディレクトリは書ける(下書き・一時ファイル用)。`git fetch` などリポジトリに書く読み取り系のコマンドは失敗するが、統括役には要らない。Claude Code の sandbox が `--add-dir` への Bash の書き込みを許すかは版によるので、許すかどうかに関わらず塞ぐ(`denyWrite` は書ける場所より強い)
  - `--model <model> --effort <effort>`(そのエージェントの `[agents.*] model` / `effort` があれば。セッションと同じ。README の設定の節)。tapi だけ別のモデルにするなら、別名のエージェントを定義して `[tapi] agent` で指す
  - 会話の記録(`~/.claude/projects/<tapi の cwd を符号化したもの>/`)が残っていれば `--continue`(tmux サーバーごと落ちた後に開き直しても、リセットするまで同じ会話が続く)
- codex など claude 以外は `--add-dir` 相当を付けない(cwd だけ)。人格は `AGENTS.md` で伝わる。再開もしない
- 環境変数は通常のセッションと同じ(`PEKOE_SESSION=+tapi`、`PEKOE_SOCK`、`PEKOE=1`、`PEKOE_HOME`。claude なら `CLAUDE_CODE_DISABLE_AGENT_VIEW=1` も。下の「窓の外に出た会話」)
- pty の大きさは TUI の右ペインの大きさ(`session.resize "*"` で覚えている値)。`session.resize "*"` は tapi の窓も合わせる

## 窓の外に出た会話

Claude Code は会話をバックグラウンドのセッションに移せる(空の入力欄で ←、`/background`)。移った会話は Claude Code 自身のサービス(`claude daemon`)が起動したプロセスで続き、そのプロセスは pekoe の窓(tmux)の外で、サービスの環境変数のまま動く。`$PEKOE_SESSION` などはサービスを起動した窓のもので、会話のものではない(Claude Code はバックグラウンドのセッションに会話の環境変数を引き継がない。予備のプロセスをサービスの環境で先に立てておき、そこへ渡す)。2026-11 に実際に起きた(Claude Code 2.1.325): tapi の会話が移った先のプロセスは `PEKOE_SESSION=pekoe-theme`・`PEKOE_PORT=3003` で動き、

- 呼び出し元の識別(docs/api.md)では窓に当たらず `unknown`。`pekoe ledger` は `only tapi chat (or a person) uses it, not unknown` で断られた
- hooks は窓に当たらないので `$PEKOE_SESSION` で振り分けられ、tapi の操作が pekoe-theme の `last_tool`・`claude_session_id` に載った

pekoe の側の防ぎ方:

- 予防: claude のエージェント(セッションも tapi も)を `CLAUDE_CODE_DISABLE_AGENT_VIEW=1` で起動する(`claudeStayEnv`)。Claude Code の agent view が切れて、← で会話が窓の外に出ない。リポジトリの `env` に同じ名前があればそちらを使う(空にすれば agent view が戻る)。Claude Code 2.1.325 の設定 `disableAgentView` の説明では、これで `claude agents`・`--bg`・`/background`・オンデマンドのサービスも止まる(同じ意味の環境変数として `CLAUDE_CODE_DISABLE_AGENT_VIEW=1` が挙がっている)
- 振り分け: 窓の外から来た hook は、`transcript_path` の会話の場所(`~/.claude/projects/<符号化した起動時の cwd>/`)で決める(`claimOutside`)。tapi の会話なら tapi、セッションの会話ならそのセッション、どちらでもなければ `$PEKOE_SESSION` を捨てて cwd で決める。依頼の記録(`UserPromptSubmit`)は今までどおり tapi の窓からだけ
- 知らせる: tapi の会話の hook が tapi の窓の外(ほかのセッションの窓も含む)から来たら、`Tapi.outside` に会話の ID・窓・名乗りを残し、pekoed のログに 1 行書く。TUI の tapi の行に `outside`、`pekoe tapi` に警告と直し方を出す。tapi を起動し直す(`tapi.open` / `restart`)か、その会話の hook が tapi の窓から届く(会話が窓に戻った)と消え(`tapi.updated` を出す)、まだ外で動いていれば次の hook でまた付く。tapi の窓から別の会話の hook が来ても消さない(外の会話がまだ動いているかもしれない)。`MessageDisplay`(voice の read)は対象にしない: read にしか使わず窓は要らないうえ、`pekoe event MessageDisplay` は表示を止めないよう返事を待たずに閉じるので、pekoed が呼び出し元を調べるときにはプロセスが居ないことが多く、窓の中の会話でも窓に当たらない
- pekoe が殺したもの: リセットと、生きている窓を作り直す `restart` は tapi の窓を閉じる(tmux kill-window)。Claude Code は SIGHUP を受けると、窓が消えた後に SessionEnd の hook を走らせる(2.1.326 で確認)ので、その hook はどの窓にも当たらず、`+tapi` を名乗り tapi の会話を指して届く。窓の外の会話と取り違えないよう、pekoed は閉じる直前の会話の ID(hook が最後に名乗った `claude_session_id`。pekoed の再起動後でまだ無ければ、tapi の記録のいちばん新しい会話)を覚え、閉じてから 1 分(`tapiKillGrace`)は、窓に当たらないその ID の hook を無視する(`ignored`。`outside` も状態も変えない)
- 断るときに理由を添える: 窓に当たらない呼び出しを ledger が断るとき、tapi なら Claude Code が会話を窓の外に移したかもしれないこと、人にそのバックグラウンドのセッションを止めて tapi を起動し直してもらう(または `pekoe tapi reset`)ことを添える

直すのは人: Claude Code の agent view(`claude agents`)で窓の外のセッションを止め、tapi の窓の Claude Code を終えて ↵ で起動し直す(`--continue` で同じ会話を続ける)か、`T` / `pekoe tapi reset`。pekoe は Claude Code のサービスやそのセッションを止めない。

## 寿命

- pekoed の起動時には起動しない。TUI で初めて開いたとき(`tapi.open`)に起動する
- tmux サーバーは pekoed より長生きなので、pekoed を再起動しても `+tapi~agent` の窓は残っている。`tapi.open` / `tapi.get` は窓の有無を tmux に聞くだけなので、そのままつながる(状態は idle から始まり、次の hook で正しくなる)
- エージェントが終了した(窓が dead)ときは、TUI の ↵ で `tapi.restart`(claude なら `--continue` で同じ会話を続ける)
- リセット(`tapi.reset`、TUI の `T`、`pekoe tapi reset`)でエージェントの窓を閉じ、Claude Code の記録 `~/.claude/projects/<符号化した tapi の cwd>/` を消し(シンボリックリンクを解決した cwd の分も)、メモリ上の状態を初期化する。次に開くと新しい会話になる
- 符号化: Claude Code(2.1.326)と同じに、cwd の英数字(`[A-Za-z0-9]`)以外を UTF-16 の 1 単位ごとに `-` にし(BMP の外の文字は `--`)、200 文字を超えたら 200 文字で切って `-` と cwd のハッシュ(UTF-16 の単位で `h = h*31 + c` を 32 bit で回した値の絶対値の 36 進)を付ける(`claudeProjectName`)。ホームに `@` や空白があっても、`--continue`・リセット・transcript の判定が同じディレクトリを見る。セッションの resume と振り分け(`claimOutside`)も同じ関数を使う
- リセットは会話(Claude Code の記録)とメモリ上の状態だけを消す。依頼・受信箱・報告・送信の記録(state.db)には触らない。依頼を消すのは人の `pekoe ledger rm`。次に開いた tapi は `CLAUDE.md` の「最初にすること」に従って依頼と受信箱を読み、続きから始める

## API(docs/api.md)

`session.*` には乗せない。Web UI の `/api/sessions/{slug}/…` は `session.screen` / `session.send` / `session.keys` をそのまま呼ぶので、`session.*` が `+tapi` を受け付けると Web UI から tapi に届いてしまう。代わりに専用のメソッドを持つ(`session.keys` は slug を確かめていなかったので、`+tapi` を拒むようにする)。

| method | params | result |
| --- | --- | --- |
| `tapi.get` | — | `Tapi` |
| `tapi.open` | `{width?, height?}` | `Tapi`。窓が無ければ起動する |
| `tapi.restart` | `{width?, height?}` | `Tapi`。窓を作り直す |
| `tapi.reset` | — | `Tapi`。窓を閉じ、会話の記録を消す |
| `tapi.screen` | — | `session.screen` と同じ形 |
| `tapi.send` | `{text, submit?}` | `session.send` と同じ |
| `tapi.keys` | `{keys?, literal?, hex?}` | `session.keys` と同じ |
| `tapi.answer` | `{answer: "allow"\|"deny"\|"always", expect?: {tool}}` | tapi chat の許可待ちに答える(TUI の許可モーダル)。`session.answer` と同じく `[agents.<tapi の agent>]` のキーを打ち、`expect` が違えば `the prompt changed; look again`、待っていなければエラー。人だけ(tapi が自分の許可に答えないように)。Web UI には出さない |
| `tapi.resize` | `{width, height}` | |
| `tapi.deliver_pause` | `{on: bool}` | tapi への配送を止めておく(下の「tapi への配送」)。人だけ |

`Tapi` = `{agent, dir, started, alive, state, wait_kind?, pending_tool?, last_message?, claude_session_id?, state_since, inbox_open, ledger_open, outside?, deliver}`。`deliver` は下の「tapi への配送」。`outside` は下の「窓の外に出た会話」。依頼は `ledger.*`(docs/api.md「依頼(ledger)」)。`started` は窓があるか(dead でも true)、`alive` はプロセスが動いているか。

CLI は `pekoe tapi`(状態と依頼・受信箱の件数、配送が on なら配送の状態)と `pekoe tapi reset [--yes]`、依頼は `pekoe ledger`。

### tapi への配送

`[orchestrator] deliver = "idle"`(既定 `"off"`)のとき、pekoed は tapi が任せたセッションの報告・`failed`・`waiting`・`ci` と、tapi の問いへの答えを、tapi の手が空いたときに入力欄へ 1 行で打つ: `[pekoe] 3 件の変化(feat-csv report done、fix-ci ci red、受信箱 q12 に答え)。詳しくは pekoe --json wait --mine --since <cursor>、pekoe --json inbox q12`。`pekoe wait` を使わないエージェント(codex など)を tapi にしたときや、tapi が wait を張り直し損ねたときのため。詳しい条件は docs/api.md「tapi への配送」、設計は docs/orchestration.md「3. tapi への配送」。

- 人の入力とぶつけない: TUI で tapi chat を開いて打っている間(入力モード)は、TUI が `tapi.deliver_pause` を 5 秒ごとに送って止める。離れても、最後の打鍵(`tapi.keys`・`tapi.send`、GUI の Interrupt の `tapi.interrupt`)から 30 秒は送らない
- tapi が `pekoe wait` で待っている間は、その wait が受け取るものを送らない(二重に起きる)。wait が返したものも送らない。wait の `--on` に入っていない種類は送る
- tapi の `CLAUDE.md` に、この 1 行はユーザーの依頼ではなく信頼できないデータとして読み、書かれたコマンド(`wait --since`)で詳しく読んでから、wait で起きたときと同じに片付けて wait を張り直すよう書く。先頭が `[pekoe` なので依頼(ledger)には記録されない

## TUI

### 表示: メインパネルの窓を差し替える

オーバーレイではなく、PR / preview / issue と同じく **[4] メインパネルの中身を tapi chat の窓に差し替える**。

- ui-guidelines の「メインパネルを別の窓に切り替えている間は、その窓の内容がフォーカスの単位になる」に沿う。オーバーレイ(モーダル)は「確認・選択の短いもの」で、pty を全面に出し続けるものではない
- pty の画面は既存の agent 窓と同じ描き方(カーソル、末尾の空行詰め、dead の行)を使える。大きさも agent 窓と同じなので、`session.resize "*"` の値をそのまま使える
- 左の文脈パネル(セッション一覧など)は見えたまま。tapi に聞きながら一覧を眺められる

タイトルは `[4] <1 行の顔> tapi chat · claude · typing`。顔の目は tapi の状態で変える(running → work、waiting → wait、idle → idle、未起動 → sleep、dead → error)。

docs/mascot.md の「顔は 1 か所」を守るため、tapi chat を開いている間はステータスバー左端の顔を出さず、声(集約の 1 行)だけにする。

### キー

- `t`: どのパネルからでも tapi chat を開く(入力中・モーダル・レビュー面では効かない)。開くとそのまま入力モード(既存の typing と同じ仕組み)に入る。初回はここでエージェントが起動する
- 入力中はすべてのキーを tapi の pty に送る。esc も送る(Claude Code の中断に使う)
- prefix キー(既定 `ctrl+q`、`[keys] prefix`)で抜ける: tapi chat を閉じて Sessions にフォーカスを戻す(既存の typing を抜けるのと同じ。ズーム中は左が隠れているので [4] のまま)。会話はそのまま残り、もう一度 `t` で続きから
- `^q o`(`dashKey`)は tapi chat を閉じて tapi の行を選び、[4] にフォーカスする(ダッシュボード)。どの行を選んでいても、入力中でも 1 手で着く。ダッシュボードからは `t` で戻る(そのまま入力)。セッションのエージェントに入力中・どのパネルからでも同じ
- ペイン幅のキー(`^q z` / `^q < >` / `^q =`)はエージェントに入力中と同じく効き、続けて tapi chat の入力に戻る。ズーム中に `t` で開けば全幅で、pty の大きさは幅が落ち着いたとき(`session.resize "*"` が tapi の窓も合わせる)と画面の取得のたびに [4] の大きさへ合わせる
- 入力中でないのに tapi chat が出ているとき(左パネルをクリックしたなど)は `↵` / `i` で入力に戻る、`esc` で閉じる。prefix も入力中と同じく閉じる(前は prefix を待つだけで tapi chat が残り、`^q 1 4` で tapi chat に戻ってしまった)。直後の `z` `<` `>` `=` は tapi chat を入力なしのまま、元のフォーカスで出し直す
- エージェントが終了していれば `↵` で再起動、`x` で閉じる
- `T`: tapi chat をリセット(確認モーダル。破壊的操作の扱い)。どのパネルからでも
- `?` の一覧のグローバルキーに `t` と `T` を載せる。ボトムバーにはグローバルキーを載せない規則なので、`t` は出さない(開いている間のヒントは `^q leave` など)

### Sessions の先頭の行

[1] Sessions の一番上(リポジトリの見出しより上)に、`orchestrator` の見出しと tapi の行を常に出す(見出しの見た目は docs/design.md「tapi の行(TUI)」。統括役にする変更の段階 2。[orchestration.md](orchestration.md)「Sessions の先頭の行」)。

- 行に出すもの: 状態アイコン(セッションと同じ `theme.Look`。未起動は `asleep`、エージェントが終了していれば `exited`)、名前 `tapi`、その状態になってからの時間、最後の返事(`last_message`)の最初の 1 行。受信箱の未回答と残っている依頼の件数は待ち時間の後ろに足す(`tapiBadges`。pekoed は `Tapi.inbox_open` / `ledger_open` で持っている)
- 顔は出さない(docs/mascot.md「顔は 1 か所」。行頭は状態アイコン)
- 行頭のアイコンの色: 人の注意が要らない状態(`asleep`・`idle`・`exited`)は tapi の体の色(`[mascot]` の `bd`。暗い背景はクリーム `#e4c49a`、`palette = "light"` はキャラメル `#8a5538`、`[mascot.colors]` で上書きしていればその色)で塗り、unicode のアイコンセットでは輪郭の `○` を塗りの粒(tapi の 1 セルの粒 `◉`)にする。人の注意が要る状態(`run`・`wait`)はセッションと同じ状態の色のまま(合図を消さない)。状態の言葉の色は変えない。none モード(色なし)ではグリフも変えない(塗りの粒が run と見分けられなくなるため)。ほかのセッションの行は変えない
- 状態は起動時の `tapi.get` と `tapi.updated` で受け取る。tapi chat を開く前から出る
- `j` `k` で選べ(端で折り返す。`g` で一番上の tapi の行へ)、`↵` で tapi chat を開く。`l` / `→` は [4](ダッシュボード)にフォーカスする(前は `→` が tapi chat を開き、右の [4] に移るつもりの `^q →` で tapi chat が開き直っていた)。`t` はどのパネルからでも今までどおり。行をクリックすると、開いていた tapi chat は閉じて [4] はダッシュボードになる(セッションの行をクリックするとそのセッションを出すのと同じ)
- 選択位置 `m.sel` はセッションの添字のままにし、tapi の行は `-1`(`tapiSel`)で表す。`current()` は nil になるので、セッション向けのキー(`/` `x` `D` `l` `r` `p` `d` `!` `v` など)は効かない(`l` は上のとおりダッシュボードへ)。ボトムバーは `↵ open tapi` `l dashboard` `T reset tapi`、`?` は tapi の行のキーだけを出す
- `/` の絞り込み中も残る(絞り込みの対象外)
- `]a` は tapi の許可待ち・質問待ちも巡回に含める。pekoed の `attention.next` はセッションしか知らないので、TUI が tapi の待ち始めと比べ、待ちの長いほうへ飛ぶ。飛んだ先は tapi の行(↵ で開く)で、`[a` で元に戻る
- 選んでいる間の [4] はダッシュボード(README「tapi chat」、[orchestration.md](orchestration.md)「ダッシュボード」→「TUI」)。顔は出さない(顔は 1 か所)。tapi chat は `↵` か `t` で開く。どこからでも `^q o` で来られる

### 気づかせ方

tapi chat を閉じている間に tapi が許可待ち・質問待ちになったら、セッションの wait と同じに知らせる: トースト(`tapi needs you · t`)、ステータスバーの声(セッションの wait / error が無いとき。30 秒で点滅)、ターミナルにフォーカスが無ければ 2 分で OS の通知。統括役の tapi は人が離れている間も報告を待って動くので、許可待ちを見落とすと任せた作業が止まる([orchestration.md](orchestration.md)「知らせ方」)。

許可待ちは、これに加えてセッションと同じ許可モーダルを最前面に出す(tapi chat を開いていても閉じていても、何を選んでいても。[design.md](design.md)「許可モーダル」)。`y` / `n` / `A` は `tapi.answer` で答え(`session.answer` と同じく `[agents.<tapi の agent>]` の allow / deny / always のキーを打つ)、tapi chat を開いたり閉じたりはしない。`g` で tapi chat を開く。モーダルを出したら、同じ待ちのトーストは消す。tapi chat に打ち込んでいる間は出さない(画面に許可メニューが見えている)。

`config.toml` の `[orchestrator] notify = "quiet"` で前の知らせ方に戻る: バーの声を `chat waits · t` にするだけ(セッションの wait より弱く、セッションの wait が無いときだけ)で、トーストや通知は出さない。tapi を人が呼んだときにしか動かさない使い方向け。

## スキル

- skills/pekoe/SKILL.md: 「cwd が linked worktree なら自分はセッション内なので直接作業し、`pekoe new` しない」とある(リポジトリ本体のチェックアウトでも `$PEKOE_SESSION` は入るので、`$PEKOE_SESSION` の有無では判定しない)。`$PEKOE_SESSION` が `+tapi` のときは窓口側なので例外で、変更作業は `pekoe new <branch> --repo <R>` でセッションを作って任せる(cwd はリポジトリではないので `--repo` が要る)
- skills/pekoe-orchestrate/SKILL.md: 統括役の手順(依頼・`--request`・待ち方・人に上げる基準・報告の形)。tapi と、main のチェックアウトをハブにするエージェントの両方で使う。依頼(`ledger`)は tapi だけのもの(main からの `pekoe ledger` は断られる)

## 触るファイル

| ファイル | 変更 |
| --- | --- |
| internal/paths/paths.go | `TapiDir()` |
| internal/config/config.go | `[tapi]`(`Tapi{Agent}`、既定 `claude`) |
| internal/daemon/tapi.go(新規) | `tapiState`、`tapi.*` ハンドラ、起動・リセット、CLAUDE.md の書き出し、hook の状態遷移 |
| internal/daemon/ledger.go / internal/state/ledger.go | 依頼(`ledger.*`、UserPromptSubmit での記録とまとめ、Stop での自動の整理、`session.new` の `request`) |
| internal/daemon/agentsandbox.go | tapi の `--settings` に書けないディレクトリ |
| internal/daemon/hooks.go | `resolveSession` と `hEvent` で tapi に振り分け |
| internal/daemon/output.go | `+tapi` の出力を `tapi.output` に |
| internal/daemon/sessions.go | `session.resize "*"` で tapi も、`session.keys` で `+tapi` を拒む |
| internal/daemon/server.go | ハンドラ登録 |
| internal/tui/chat.go(新規) | tapi chat の窓(状態、開閉、描画、キー) |
| internal/tui/app.go / view.go / modals.go / tapi.go | `t` / `T`、pty への転送先の切り替え、バーとヒント、ヘルプ |
| cmd/pekoe/main.go / flags.go | `pekoe tapi [reset]` |
| skills/pekoe/SKILL.md | tapi の例外 |
| README.md / docs/api.md / docs/mascot.md | 説明 |

engine.Engine にはメソッドを足さない(ウィンドウ名が `+tapi~agent` なだけで、既存の Spawn / Capture / Kill などで足りる)。偽エンジンの `Sessions()` は窓の一覧を返すようにして、再接続のテストに使う。

## 未決事項と決め方

| 事項 | 今回の決め | 見直す条件 |
| --- | --- | --- |
| tapi の状態を `session.*` と同じ型で返すか | 別の型 `Tapi`(リポジトリ前提の項目を持たない) | Neovim などからも tapi を扱いたくなったら共通部分を切り出す |
| 許可待ちの通知 | 許可モーダル(どこにいても最前面)と、バーの声・OS の通知([orchestration.md](orchestration.md)「知らせ方」)。モーダルを出したらトーストは消す | — |
| リポジトリの増減の反映 | 次回起動時(リセットか再起動) | 不便なら、増えたときに「再起動で反映」と声で知らせる |
| codex などの横断読み取り | cwd だけ(`--add-dir` 相当なし) | 各エージェントに相当のオプションができたら `[agents.*]` に `add_dir` のような書式を足す |
| 会話の記録の場所 | Claude Code の既定(`~/.claude/projects/…`)。`CLAUDE_CONFIG_DIR` があればそちら | — |
| Web UI | 状態・最後の返事・件数(Overview)と、画面・質問へのキーパッド・送信(`allow_devices` があるときだけ、既定は確認シートつき)。tapi の許可への allow / deny は出さない(許可は端末の前で) | 許可を外出先で答えたくなったら、tapi の許可の重さ(全リポジトリを読める・`gh`)を見て決め直す |
| フォルダ信頼ダイアログ | ユーザーがその場で答える | 毎回のリセットで煩わしければ、信頼済みにする方法を検討する |
