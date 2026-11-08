# tapi chat — 設計メモ

Nov 8, 2026 · 試作

## 何のためのものか

今は各リポジトリの main セッションを窓口にして、そこから `pekoe new` / `pekoe send` で作業を振り分けている。そのせいで main が作業中で埋まり、気軽にエージェントと話せないことが多い。

tapi chat は TUI からいつでも呼べる特別なエージェントで、次の用途に使う。

- 特定のセッションとは関係ない単発の調査・質問
- リポジトリ横断の調査(例: 抱えているリポジトリ群の dependabot アラートの様子)
- 振り分け役(調べた結果から `pekoe new --repo R` でセッションを作る、`pekoe send` で指示を足す)

グローバルに 1 つだけで、pekoe 側には何も永続化しない一時的なもの。マスコットの tapi(docs/mascot.md。pekoe は家、tapi はそこに住む相棒)と話す、という体裁にする。tapi の性格・口調・顔の使い方は docs/mascot.md「tapi の人物像」(chat の中だけの話し方で、ステータスバーの台詞の規則は変えない)。Web UI には出さない。

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

`d.sessions` に入らないので、セッションを回る既存の処理(`session.list`・diffstat・PR・衝突予測・land・review・dev・run・tests・門番・ntfy・Web UI・`attention.next`・`daemon.status` の sessions 数・`state.db`)には何もしなくても流れ込まない。

### 予約名 `+tapi`

tmux のウィンドウ名と hook の振り分けには、セッションの slug と同じ名前空間の予約名 `+tapi` を使う(tmux のウィンドウは `+tapi~agent`、`$PEKOE_SESSION=+tapi`)。

- `paths.Slug` は `[A-Za-z0-9._-]` しか出さず、セッションの slug はすべて `paths.Slug` を通るので、`+tapi` は通常のセッションと衝突しない(テストで固定する)
- `@tapi` は使えない: tmux のターゲット `pekoe:=@tapi~agent` の `@` がウィンドウ ID と解釈され、別のウィンドウ(`_keeper`)に当たる。`+tapi` は `=` 付きのターゲットで名前どおりに当たることを確かめた

## hook: 同じ仕組みに乗せる

tapi の Claude Code も通常のセッションと同じ `pekoe event` の hook を使う(`pekoe hooks install` 済みの設定がそのまま効く)。

- `resolveSession`(internal/daemon/hooks.go): `$PEKOE_SESSION` が `+tapi` なら tapi。`$PEKOE_SESSION` が無いときは、cwd が tapi の稼働ディレクトリの中なら tapi。それ以外は従来どおり
- `hEvent`: tapi に振り分けられた hook は `tapiEvent` が受け、状態だけを動かす。プロンプトのスナップショット(`refs/pekoe/…`)、diffstat、テスト、門番、ntfy は動かさない

| hook | tapi の状態 |
| --- | --- |
| SessionStart | idle(`claude_session_id` を記録) |
| UserPromptSubmit | running |
| PreToolUse | running(AskUserQuestion なら waiting / question)、`pending_tool` を記録 |
| PostToolUse | waiting なら running に戻す |
| Notification | permission_prompt → waiting / permission(AskUserQuestion の PreToolUse 直後なら question のまま。セッションと同じ)、elicitation_dialog → waiting / question、idle_prompt → idle、知らない種類 → waiting / permission |
| Stop | idle(`last_message` を記録)。通常のセッションの done(レビュー待ち)は tapi には無い |
| SessionEnd | idle |

変化は `tapi.updated` イベント(data は下の `Tapi`)で流す。画面更新の合図は `tapi.output`(`session.output` と同じく 30ms で間引く)。

## 稼働ディレクトリ

`paths.TapiDir()` = `~/.local/share/pekoe/tapi/`(`$PEKOE_HOME` があれば `$PEKOE_HOME/share/tapi/`)。

- `$HOME` にはしない: cwd の前方一致で、ホーム以下で動く無関係な Claude Code の hook を拾ってしまう
- 起動のたびに pekoed が `CLAUDE.md` と `AGENTS.md`(同じ内容)を書き出す。役割(横断調査・振り分け役)、使うコマンド(`pekoe --json ls`、`pekoe --json new <branch> --repo R --prompt …`、`pekoe send <slug> -`)、やらないこと(land・rm・answer はユーザーに頼まれたときだけ、ここでファイルを変更しない)、人物像と口調・振る舞いのルール・顔の目の表(docs/mascot.md「tapi の人物像」。キャラは返事の頭の顔と最初か最後の 1 文だけで、中身は正確さ優先)、今日マージされた PR の数え方、TIPS の紹介の仕方(下記)、起動時点の登録済みリポジトリの一覧(名前とパス)を書く
- 初回は Claude Code のフォルダ信頼ダイアログが出る。開いた時点で入力モードなので、そのまま答えればよい(pekoe からは `~/.claude.json` を書き換えない)

### TIPS の紹介

最初の返事のあいさつに続けて、pekoe の機能を 1 つ紹介する(README「TIPS」)。どれを紹介するかは tapi が選ばず、`pekoe tip` に任せる。`pekoe tip` は `internal/tips/tips.toml` の一覧から、TUI の空き表示と共通の記録(`tips.json`)でまだ見せていないものを 1 つ選び、記録して出す。だから TUI で見せた tip を tapi が繰り返すことはなく、一巡するまで同じものは出ない。

- 紹介は顔の後ろのあいさつに続く 1 文に収める(キャラの部分)。キー・コマンドは `pekoe tip` の出力をそのまま写し、出力に無いものを作らない。`^q` はユーザーの `[keys] prefix` に置き換えて出てくる
- 「なんか tip ちょうだい」と頼まれたら `pekoe tip` で次の 1 つ。`--list` で一覧、`pekoe tip <id>` で特定の 1 つ(記録しない)
- 「もっと詳しく」と聞かれたら、tip の docs 行のパスを `pekoe tip --doc <path>` で読んで普通の文で説明する。docs(README と docs/*.md、nvim/ と extension/ の README)はバイナリに同梱しているので、pekoe のリポジトリが手元に無くても読める
- `[tips] enable = false` のとき `pekoe tip` は `tips are off` と出すだけで、tapi は頼まれない限り tip に触れない。CLAUDE.md の文面は設定によらず同じ(書き出す関数は設定を見ない)
- `pekoe tip` は pekoed につながらず、`tips.json` を書くだけ。sandbox などで書けないときは tip を出して記録だけ諦める(stderr に一言)。Claude Code の許可リストを使っているなら `Bash(pekoe tip)` と `Bash(pekoe tip *)` を足すと、あいさつのたびに聞かれない

## 起動するコマンド

`config.toml` の `[tapi]` 節で、既存の `[agents.*]` の名前を指す。

```toml
[tapi]
agent = "claude"   # 既定。[agents.claude-ask] などを定義していればその名前も使える
```

- 起動はエージェントの `command` を `$SHELL` で実行する(既存の `spawnAgent` と同じ形。終了後は `[pekoe] agent exited …` を残す)
- `command` の先頭語が `claude` のとき(`claude-ask` のように引数を足した定義も含む)だけ次を足す
  - `--add-dir <各リポジトリのメイン worktree> <worktree のルート>`: 登録済みリポジトリ(`repo.list` と同じ `knownRepos` で、ディレクトリが見つかるもの)と `~/worktrees`。各リポジトリを読むたびに許可を求められないように。リポジトリの増減は次に tapi を起動したときに反映する
  - `--append-system-prompt <人格>`: 「pekoe に住むタピオカの粒の相棒 tapi。一人称ぼく・ユーザーはきみ・くだけた話し言葉。返事の頭に 1 行の顔。キャラは最初か最後の 1 文だけで、中身は正確に、確かめたことと推測を分ける。詳細は CLAUDE.md」の数文(人物像は docs/mascot.md「tapi の人物像」)
  - 会話の記録(`~/.claude/projects/<tapi の cwd を符号化したもの>/`)が残っていれば `--continue`(tmux サーバーごと落ちた後に開き直しても、リセットするまで同じ会話が続く)
- codex など claude 以外は `--add-dir` 相当を付けない(cwd だけ)。人格は `AGENTS.md` で伝わる。再開もしない
- 環境変数は通常のセッションと同じ(`PEKOE_SESSION=+tapi`、`PEKOE_SOCK`、`PEKOE=1`、`PEKOE_HOME`)
- pty の大きさは TUI の右ペインの大きさ(`session.resize "*"` で覚えている値)。`session.resize "*"` は tapi の窓も合わせる

## 寿命

- pekoed の起動時には起動しない。TUI で初めて開いたとき(`tapi.open`)に起動する
- tmux サーバーは pekoed より長生きなので、pekoed を再起動しても `+tapi~agent` の窓は残っている。`tapi.open` / `tapi.get` は窓の有無を tmux に聞くだけなので、そのままつながる(状態は idle から始まり、次の hook で正しくなる)
- エージェントが終了した(窓が dead)ときは、TUI の ↵ で `tapi.restart`(claude なら `--continue` で同じ会話を続ける)
- リセット(`tapi.reset`、TUI の `T`、`pekoe tapi reset`)でエージェントの窓を閉じ、Claude Code の記録 `~/.claude/projects/<符号化した tapi の cwd>/` を消し(シンボリックリンクを解決した cwd の分も)、メモリ上の状態を初期化する。次に開くと新しい会話になる
- pekoe 側(state.db など)には何も書かない

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
| `tapi.resize` | `{width, height}` | |

`Tapi` = `{agent, dir, started, alive, state, wait_kind?, pending_tool?, last_message?, claude_session_id?, state_since}`。`started` は窓があるか(dead でも true)、`alive` はプロセスが動いているか。

CLI は `pekoe tapi`(状態)と `pekoe tapi reset [--yes]`。

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
- ペイン幅のキー(`^q z` / `^q < >` / `^q =`)はエージェントに入力中と同じく効き、続けて tapi chat の入力に戻る。ズーム中に `t` で開けば全幅で、pty の大きさは幅が落ち着いたとき(`session.resize "*"` が tapi の窓も合わせる)と画面の取得のたびに [4] の大きさへ合わせる
- 入力中でないのに tapi chat が出ているとき(左パネルをクリックしたなど)は `↵` / `i` で入力に戻る、`esc` で閉じる
- エージェントが終了していれば `↵` で再起動、`x` で閉じる
- `T`: tapi chat をリセット(確認モーダル。破壊的操作の扱い)。どのパネルからでも
- `?` の一覧のグローバルキーに `t` と `T` を載せる。ボトムバーにはグローバルキーを載せない規則なので、`t` は出さない(開いている間のヒントは `^q leave` など)

### 気づかせ方

tapi chat を閉じている間に tapi が許可待ち・質問待ちになったら、ステータスバーの tapi の声を `chat waits · t` にする(セッションの wait より弱く、セッションの wait が無いときだけ)。tapi は人が呼んだときにしか動かないので、トーストやデスクトップ通知までは出さない。

## スキル(skills/pekoe/SKILL.md)

今は「`$PEKOE_SESSION` があれば自分はセッション内なので直接作業し、`pekoe new` しない」とある。`$PEKOE_SESSION` が `+tapi` のときは窓口側なので例外で、変更作業は `pekoe new <branch> --repo <R>` でセッションを作って任せる(cwd はリポジトリではないので `--repo` が要る)、と書き足す。

## 触るファイル

| ファイル | 変更 |
| --- | --- |
| internal/paths/paths.go | `TapiDir()` |
| internal/config/config.go | `[tapi]`(`Tapi{Agent}`、既定 `claude`) |
| internal/daemon/tapi.go(新規) | `tapiState`、`tapi.*` ハンドラ、起動・リセット、CLAUDE.md の書き出し、hook の状態遷移 |
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
| 許可待ちの通知 | バーの声だけ。ntfy・トースト・OSC 通知は出さない | 長い横断調査を投げて離席する使い方が多ければ ntfy を足す |
| リポジトリの増減の反映 | 次回起動時(リセットか再起動) | 不便なら、増えたときに「再起動で反映」と声で知らせる |
| codex などの横断読み取り | cwd だけ(`--add-dir` 相当なし) | 各エージェントに相当のオプションができたら `[agents.*]` に `add_dir` のような書式を足す |
| 会話の記録の場所 | Claude Code の既定(`~/.claude/projects/…`)。`CLAUDE_CONFIG_DIR` があればそちら | — |
| Web UI | 出さない | 要望が出たら別途 |
| フォルダ信頼ダイアログ | ユーザーがその場で答える | 毎回のリセットで煩わしければ、信頼済みにする方法を検討する |
