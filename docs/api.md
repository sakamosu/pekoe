# pekoed API

すべてのクライアント(TUI・CLI・Neovim・Chrome 拡張・MCP)が叩く共通 API。賢さはデーモン側にあり、クライアントは表示と入力だけを持つ。

## 1. unix socket(TUI・CLI・Neovim)

`~/.local/share/pekoe/pekoed.sock`(`$PEKOE_HOME` があれば `$PEKOE_HOME/share/pekoed.sock`)。改行区切りの JSON(JSON Lines)。1 接続でリクエストとイベントが混ざって流れる。

```jsonc
// client → daemon
{"id": 1, "method": "session.list", "params": {}}
// daemon → client(応答)
{"id": 1, "result": [ ... ]}
{"id": 2, "error": "no such session: feat-x"}
// daemon → client(イベント。subscribe した接続にだけ流れる)
{"event": "session.waiting", "data": { ...Session }}
```

`{"id":N,"method":"subscribe","params":{"events":["*"]}}` でイベント購読を始める。`events` は前方一致(`"session."` で session 系すべて)。

### 人間だけの操作(human token)

ソケットは 0600 だが、同じユーザーで動くエージェントもつなげる。pekoed はエージェントの sandbox の外で動くので、エージェントが pekoed に「代わりに」コマンドを実行させたりファイルを読ませたりできると、sandbox が意味をなくす。そこで、下の「token なしで呼べるメソッド」以外は、リクエストの最上位に `token` が要る(許可リスト。後から足したメソッドは、エージェントに要ると決めるまで token 必須)。

```jsonc
{"id": 3, "method": "session.answer", "params": {"slug": "feat-x", "answer": "allow"}, "token": "<human-token の中身>"}
```

- token は pekoed が起動時に `~/.local/share/pekoe/human-token`(`$PEKOE_HOME` があれば `$PEKOE_HOME/share/human-token`、0600)に作る。再起動しても変わらない
- TUI・CLI・Neovim はこのファイルを読んで毎回付ける(`subscribe` にも)。MCP サーバー(`pekoe mcp`)は付けない。エージェントのペインの環境には token もそのパスも渡さない
- token なしで呼べるメソッド(エージェントのフック・ハブのエージェント・MCP サーバーが使うもの):

  | メソッド | 使うところ |
  | --- | --- |
  | `session.event` | エージェントのフック(`pekoe event`) |
  | `session.list` / `session.get` | `pekoe ls`、MCP の `session_list` |
  | `session.new` | ハブのエージェントの `pekoe new` |
  | `session.send` | ハブのエージェントの `pekoe send`(制限つき。下) |
  | `repo.resolve` | `pekoe ls` / `pekoe new --repo` がリポジトリを決める |
  | `editor.open` | MCP の `open_in_editor`(nvim へのイベントを出すだけ) |

- それ以外はすべて token が要る。ないか違えば `<method>: only a person can do this: …` のエラーで、何もしない。人間の判断(`session.answer`・`session.keys`・`review.*` の承認やコメント・`error.send`・`pick.send` など)、sandbox の外で何かを動かすもの(`init.write`・`run.start`・`dev.toggle`・`review.surface`・`session.open`・`land`・`issues.*`・`pr.view`・`pr.failed_log` の gh 呼び出しなど)、秘密や他のセッションの中身を返すもの(`ext.info` の HTTP token、`web.status` のログイン URL、`session.screen`・`tapi.screen`・`error.get` / `error.list` のソースの抜粋、`changes`・`review.files` など)、TUI の内部の記録(`session.seen`・`session.resize`・`daemon.status` など)。分類と理由の一覧は `internal/daemon/human.go` の `agentMethods` / `humanMethods`(登録した全メソッドがどちらかに載っていることをテストで確かめている)
- `session.send` は token なしでも使える(ハブのエージェントが `pekoe send` で指示を渡すため)。ただし token なしでは、エージェントの窓(`window` 省略か `agent`)だけに送れて、相手に許可ダイアログが出ている間は `submit` の有無にかかわらず断る(下の「send と許可ダイアログ」)
- `subscribe` は token なしでも使えるが、届くのは `session.created` / `session.updated` / `session.removed` / `session.progress` / `session.waiting` / `session.done` / `session.output` だけ。ほかのイベント(他のセッションのクリップボードを運ぶ `term.passthrough`、`pick.received`、`error.updated`、`review.*` など)は token を付けて購読した接続にだけ流れる
- Web UI と Chrome 拡張はソケットを通らない(デーモンの中でハンドラを呼ぶ)ので、それぞれの認証(tailnet の WhoIs、http-token)だけで足りる
- 同じユーザーのプロセスはファイルを読めてしまうので、これだけでは塞ぎきれない。エージェントからファイルを読めなくする設定は README の「エージェントに pekoe を使わせるときの権限」

### send と許可ダイアログ

許可メニューは最初の 1 文字を選択肢として受け取り、Enter は選ばれている「Yes」を確定する。そこで `session.send` / `tapi.send` は、許可ダイアログが出ている間は文字を打たない。「出ている」は、hook が知らせた許可待ち(`wait_kind` が `question` 以外の `waiting`)か、agent 窓の画面の下に Claude Code の「Do you want to …」と「1. Yes」の選択肢(または最後の行の `[y/n]`)があること。

| いつ | だれ | 結果 |
| --- | --- | --- |
| 打つ前 | token なし(agent 窓への送信すべて) | `<slug> is waiting for a permission answer: answer it in the pekoe TUI first, then send (nothing was sent)` |
| 打つ前 | token あり・Web UI・Chrome 拡張(`submit` つき) | `<slug> is waiting for a permission answer: answer it first, then send (nothing was sent)`。Web UI は 409 |
| Enter の直前 | `submit` つきのすべて | `<slug> is waiting for a permission answer: the text was typed but not submitted (Enter would have answered it)`。文字は入力欄に残る。Web UI は 409 |

TUI がユーザーの打鍵として送るペースト(token あり・`submit` なし)は止めない。

### Session オブジェクト

```jsonc
{
  "slug": "feat-csv",              // worktree・セッションの識別子(branch の / や . を - に。全リポジトリで一意で、別リポジトリと衝突したら <repo>-<slug>)
  "branch": "feat/csv",
  "repo": "app",                   // リポジトリ名
  "repo_id": "github.com_me_app",
  "path": "/Users/me/worktrees/app/feat-csv",
  "agent": "claude",
  "state": "running",              // creating | running | waiting | idle | done | reviewing | approved | landed | failed
  "wait_kind": "permission",       // waiting のときだけ: permission | question(AskUserQuestion と elicitation。AskUserQuestion の直後に Claude Code が送る permission_prompt は、待ちの対象が AskUserQuestion のままなら question のまま。知らない notification_type は permission)
  "state_since": "2026-11-07T10:00:00Z",
  "port": 3001,
  "last_tool": "Edit src/api.ts",  // PreToolUse の直近
  "pending_tool": {"name": "Edit", "input": "src/api.ts", "touches": "src/api.ts", "also_in": ["feat-x"]}, // 許可待ちの対象。input は主な引数 1 つの 1 行の要約(改行のかたまりは " ⏎ "、200 バイトまで)。touches は Edit/Write が変えるファイル、also_in はそれを既に変更済みの他セッション。引数が複数行なら lines(行数)、要約が切れていれば cut: true と size(全体のバイト数)
  "test": {"status": "pass", "summary": "12 passed", "at": "..."},      // pass | fail | running | none
  "diffstat": {"added": 120, "deleted": 8, "files": 4},
  "prompts": 7,                    // スナップショット数
  "review_n": 2,                   // 最後のレビュー地点番号(0 = 未レビュー)
  "claude_session_id": "…",
  "unread": 1234,                  // 離れてからの未読バイト数
  "activity": [0,1,3,5,8,6,4,5,7,9], // 直近の活動量(スパークライン用、古い順)
  "tokens": 42000,
  "dev": true,                     // dev server 起動中
  "run": "gh auth login",          // run 窓(TUI の !)のコマンド。閉じると消える
  "pr": 123,                       // セッションの PR 番号(gh#N のレビュー、land、自動検出)
  "issue": 12,                     // --from gh#N で作ったときの issue 番号
  "pr_status": {                   // PR の要約(60 秒ごとにポーリング。PR ビューを開いている間は 15 秒)
    "state": "OPEN",               // OPEN | MERGED | CLOSED
    "draft": false,
    "pass": 5, "fail": 1, "pending": 0,   // checks の数
    "review_decision": "CHANGES_REQUESTED", // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED
    "unresolved": 2,               // 未解決のレビュースレッド
    "merge_state": "BLOCKED",      // mergeStateStatus: CLEAN | BLOCKED | DIRTY | BEHIND | UNSTABLE …
    "last_comment": "…", "last_thread": "…",
    "attention": ["ci_failed"],    // 前回 PR ビューを開いてから起きたこと: ci_failed | changes_requested | new_comment | mergeable
    "attention_at": "…", "at": "…"
  },
  "error": "setup failed: pnpm install",
  "overlaps": [                    // 衝突予報(同じリポジトリの open なセッションのうち、同じファイルを触っている相手)
    {"slug": "feat-x", "files": ["src/api.ts", "README.md"], "conflict": true, "conflict_files": ["src/api.ts"]}
  ]
}
```

**衝突予報** — 「触っているファイル」は base(merge-base)からの変更(コミット済み・未コミット・未追跡)と `pending_tool.touches` の和。同じ `repo_id` の、landed でも停止済み(`x`)でもないセッション同士で重なりを取り、pekoed が約 10 秒ごと(と Edit/Write の直後)に計算し直す。変化があれば `session.updated` が出る。重なったペアのうちコミット済みの内容も重なっているものは `git merge-tree --write-tree` で worktree を汚さずに試しマージし、衝突すれば `conflict: true`。未コミット分は重なり検知だけ。

### メソッド

`{slug}` を取るメソッドはすべて `<repo>/<slug>` も受け付ける(`<repo>` は repo-id か短い名前。`<slug>` は本物の slug か、別リポジトリとの衝突でリポジトリ名が前に付く前の形)。デーモンが受け取った時点で slug に直す。当たらなければ `no such session: <repo>/<slug>`、複数のリポジトリに当たれば候補を並べたエラー。

| method | params | result |
| --- | --- | --- |
| `daemon.status` | — | `{pid, version, sessions, http_port}` |
| `daemon.stop` | `{kill_agents?}` | `null`。既定ではエージェント(tmux サーバー)を残す。`kill_agents: true` なら、止まる直前に tmux サーバーも止める(使い捨ての隔離環境向け。CLI は `pekoe daemon stop --kill-agents`)。応答はすぐ返り、プロセスはその後に終わる。終了を待つには `daemon.status` の `pid` を見る(`pekoe daemon stop` と `pekoe daemon restart` はそうしている) |
| `subscribe` | `{events: [prefix]}` | `null` |
| `repo.list` | `{cwd?}` | `[KnownRepo]`(下記)。既知のリポジトリ = セッションがあるもの + 一度解決したもの(`session.new` / `session.open` / `init.*` / `repo.list` の `cwd`。state.db の kv に `repo:<repo-id>` で覚える) + `~/.config/pekoe/repos/*.toml` があるもの。設定ファイルしかないものは、`path_` の repo-id ならそのパス、それ以外は `~/worktrees/<name>/` の worktree から場所を探す。`cwd` を渡すと、そのリポジトリ(worktree の中ならメイン)を覚えたうえで `cwd: true` を付ける。名前順 |
| `repo.resolve` | `{repo, cwd?}` | `KnownRepo`。`repo` は repo-id・短い名前・パス(`/` を含むか `.` `~` で始まるもの。相対パスは `cwd` 基準。worktree の中ならメインに解決し、既知でなくてもよい)。短い名前が複数に当たるときは候補の repo-id を並べたエラー、知らない名前は既知の名前を並べたエラー。`cwd` 直下に同名のディレクトリがあればそのパスとして扱う |
| `session.list` | `{repo_id?}` | `[Session]` |
| `session.get` | `{slug}` | `Session` |
| `session.new` | `{repo, branch, from?, agent?, prompt?}` | `Session`(state = creating。進捗は `session.progress` イベント) |
| `session.cancel` | `{slug}` | 作成中を中止し worktree を消す |
| `session.open` | `{path, resume?: "ask"\|"resume"\|"new", agent?}` | `Session` |
| `session.adopt` | `{path?}` | `[Session]` |
| `session.send` | `{slug, text, submit?: bool}` | pty に文字を書く。複数行は bracketed paste。`submit` で最後に Enter。テキストとして送るので、改行(CR・CRLF は LF に)とタブ以外の制御文字は落とす。キーは `session.keys` で送る。許可ダイアログが出ている間は送らない(下の「send と許可ダイアログ」)。token なしでは agent の窓だけ(上の「人間だけの操作」) |
| `session.keys` | `{slug, keys: [tmuxKey]}` / `{slug, literal: "abc"}` / `{slug, hex: "1b5b..."}` | 生キー転送(TUI の右ペインフォーカス時) |
| `session.answer` | `{slug, answer: "allow"\|"deny"\|"always", expect?: {tool}}` | 許可待ちに答える。`expect` を渡すと、待っているのがそのツール(`pending_tool` の name と input)のときだけ答え、違えば `the prompt changed; look again` のエラー(Web UI が使う) |
| `session.screen` | `{slug, window?: "agent"\|"review"\|"dev"\|"run"}` | `{lines: [ansi string], cursor: {x,y,visible}, width, height, dead, exit_status, signal?}`(`exit_status` は終わった窓の終了コード。動いている間やシグナルで終わったときは -1。`signal` は `"SIGINT"` など) |
| `session.resize` | `{slug, width, height}` | pty サイズ変更 |
| `session.seen` | `{slug}` | 未読ラインの基点を今に |
| `session.kill` | `{slug}` | セッション終了(worktree は残す) |
| `session.undo` | `{slug, n?}` | プロンプト単位 undo(省略時は最後のプロンプト前に戻す) |
| `session.event` | `{hook, payload}` | hooks から(`pekoe event` が呼ぶ) |
| `attention.next` | `{from?: slug}` | 待ち時間順で次の waiting セッション。いなければ `pr_status.attention` のあるセッション(古い順) |
| `review.next` | — | 次のレビュー待ち(done) |
| `worktree.list` | `{repo_id?}` | `[{path, branch, slug, port, managed, current}]` |
| `worktree.rm` | `{slug}` | teardown → worktree 削除(`~/worktrees` 配下のみ) |
| `land` | `{slug}` | rebase → test → push → `gh pr create`。マージ検出後に自動 teardown |
| `dev.toggle` | `{slug}` | dev server の起動/停止 |
| `run.start` | `{slug, command?, width?, height?}` | `RunState` = `{running, finished, started, command, exit_status, signal?}`。worktree で `command` を 1 回だけ run 窓に起動する(dev と同じ環境変数、`$SHELL -c`)。run 窓が動いている・終わって閉じていない間は起動せずにその状態(実行中のコマンドと、終わっていれば終了コード。シグナルで終わったら `exit_status` は -1 で `signal: "SIGINT"`)を返す。`command` なしは状態を見るだけ。作成中・landed・worktree が無いセッションではエラー。呼び出しは 1 本ずつ処理する |
| `run.close` | `{slug}` | run 窓を閉じ、閉じる前の `RunState` を返す(TUI の「tell the agent?」用)。セッションの `run` を空にする |
| `changes` | `{slug}` | `[{status: "A"\|"M"\|"D", path}]`(base からの変更) |
| `conflicts.list` | `{repo_id?}` | `[{repo_id, a, b, status: "overlap"\|"conflict", files, conflict_files?}]`。その場で計算し直し、各セッションの `overlaps` も更新する(`pekoe conflicts`) |
| `review.start` | `{slug}` | state を reviewing に |
| `review.end` | `{slug}` | レビュー画面を閉じたとき。未送信コメントが無ければ reviewing → done（全 hunk 承認済みなら approved） |
| `review.files` | `{slug, show_approved?: bool, since?: n}` | `Review`(下記) |
| `review.approve` | `{slug, hunk_id}` | |
| `review.unapprove` | `{slug, hunk_id}` | |
| `review.reject` | `{slug, hunk_id, text?}` | 差し戻し(コメントとして溜まる) |
| `review.comment` | `{slug, path, line, hunk_id?, text}` | `{id}` |
| `review.comment.delete` | `{slug, id}` | |
| `review.ask` | `{slug, hunk_id, question?}` | エージェントに意図を聞く。返事は `review.answer` イベント |
| `review.flush` | `{slug}` | 未送信コメントを 1 通にまとめて pty(PR なら GitHub review)へ。レビュー地点 `review-<n>` を記録 |
| `review.risks` | `{slug}` | 門番エージェントの結果 `[{path, line, text}]` |
| `pick.list` | `{slug?}` | `[Pick]` |
| `pick.send` | `{id, slug?, text?, new_branch?}` | pty に `@<path>.md <text>` を貼る |
| `pick.discard` | `{id}` | |
| `browser.tabs` | — | `[{url, port, slug, title, errors}]` |
| `editor.open` | `{path, line, note?, slug?}` | nvim へ `editor.open` イベントを流す(MCP から) |
| `init.detect` | `{path}` | `{repo_id, config_path, toml, ignored: [path]}` |
| `init.write` | `{path, toml, overwrite?}` | `{path}` |
| `session.restart` | `{slug, width?, height?}` | エージェントを再起動(Claude Code セッションを resume) |
| `session.progress` | `{slug}` | 作成中の手順 `{slug, steps}` |
| `review.surface` | `{slug, width, height}` | 右ペインを nvim(pekoe.nvim のレビュー面)に入れ替える。claude は裏で動き続ける。読み込むプラグインは pekoed 自身のもの(`PEKOE_NVIM_RTP` か実行ファイルの隣の `nvim/`)だけで、呼び出し元からは指定できない |
| `review.pr` | `{repo, number}` | `pekoe review gh#N`: PR ブランチの worktree を作り、同じレビュー面で開く。コメントは GitHub review に送られる |
| `pr.view` | `{slug, seen?: bool}` | `PRView`(下記)。PR が無ければ `null`(番号未登録ならブランチから探す)。`gh api graphql` 1 回で取り、ついでに `pr_status` を更新する。`seen` で attention を既読に |
| `pr.seen` | `{slug}` | `pr_status.attention` を既読にする(取得はしない) |
| `pr.failed_log` | `{slug, check, run_id?, job_id?, lines?: 200}` | 失敗した Actions の check のログ末尾 `{check, run_id, job_id, log, truncated}`(`gh run view --job <id> --log-failed`、取れなければ run 単位) |
| `window.kill` | `{slug, window: "review"\|"dev"\|"run"}` | レビュー面 / dev server / run のウィンドウを閉じる(run は `run.close` と同じ) |
| `browser.send_errors` | `{port, slug?}` | 溜まった console error をまとめてセッションに送る(ファイルに書いて `@` 参照の 1 行。そのポートのエラーカードは sent になる) |
| `error.list` | `{slug?, all?}` | `[ErrorReport]`(古い順。既定は未対応 = `new` だけ、`all` で送信済み・無視も) |
| `error.get` | `{id}` | `ErrorReport` + `excerpt`(先頭フレームが worktree 内なら前後 5 行の抜粋) |
| `error.send` | `{id, slug?, text?}` | エラーをエージェントに送る。1 行で済むものはそのまま、それ以外は `~/.local/share/pekoe/<repo-id>/errors/<slug>-<n>.md` に書いて `@<path> <text>` の 1 行を送る。`ErrorReport`(sent) |
| `error.ignore` | `{id}` | 無視する(同じ指紋が再発するまで出さない)。`ErrorReport`(ignored) |
| `ext.info` | — | `{port, token}`(`pekoe ext`) |
| `web.status` | — | `{built, enabled, state, auth_url?, urls?, error?}`(`pekoe web`)。`built` はこのバイナリに Web UI が入っているか(`-tags web`)。state は `off` `unavailable`(Web UI なしのビルドで有効にされている)`starting` `needs-login` `running` `error`。`auth_url` は初回参加のログイン URL |
| `issues.list` | `{repo? \| repo_id?, filter?: "mine"\|"open"\|"label", label?, refresh?}` | `IssueList`(下記)。`gh issue list --state open --json number,title,labels,assignees,updatedAt,comments,url`、`mine` は `--assignee @me`。3 分キャッシュし、`refresh` で取り直す |
| `tapi.get` | — | `Tapi`(下記) |
| `tapi.open` | `{width?, height?}` | `Tapi`。tapi chat のエージェントを、窓が無ければ起動する(pekoed の起動時には起動しない)。窓が残っていれば(pekoed を再起動しても tmux サーバーは残る)そのまま |
| `tapi.restart` | `{width?, height?}` | `Tapi`。窓を作り直す(エージェントが終了した後の ↵。Claude Code は `--continue` で同じ会話を続ける) |
| `tapi.reset` | — | `Tapi`。窓を閉じ、Claude Code の会話の記録(`~/.claude/projects/<tapi の cwd>/`)を消す。次の `tapi.open` は新しい会話 |
| `tapi.screen` | — | `session.screen` と同じ形(起動前はエラー) |
| `tapi.send` | `{text, submit?}` | `session.send` と同じ |
| `tapi.keys` | `{keys?, literal?, hex?}` | `session.keys` と同じ |
| `tapi.resize` | `{width, height}` | pty サイズ変更(`session.resize "*"` でも合わせる) |
| `issues.view` | `{repo? \| repo_id?, number, refresh?}` | `IssueDetail`(下記)。`gh issue view N --json …`。同じく 3 分キャッシュ |
| `preview.capture` | `{slug, path?, width?, height?, mobile?: bool}` | `Preview`。headless Chrome で `http://localhost:<port><path>` を撮り、終わってから返る。`path` と `mobile` は次回以降も使う(既定 `/`、デスクトップ 1280×800、モバイル 390×844)。撮れなかった理由は `error` に入る |
| `preview.get` | `{slug}` | `Preview` |
| `preview.watch` | `{slug, on: bool}` | `Preview`。表示中の印。on のセッションはエージェントが手を止めたとき(running → waiting / idle / done)に自動で撮り直す |

### Tapi オブジェクト

tapi chat(TUI の `t`、docs/tapi-chat.md)。セッションではないので `session.*` には出てこない(`session.list` に載らず、state.db にも保存しない。Web UI からも見えない)。tmux の窓は `+tapi~agent`、hook は `$PEKOE_SESSION=+tapi`(`+` はセッションの slug に現れない)か、cwd が tapi の稼働ディレクトリの中なら tapi に振り分ける。

```jsonc
{
  "agent": "claude",                       // [tapi] agent
  "dir": "~/.local/share/pekoe/tapi",      // 稼働ディレクトリ(pekoed が CLAUDE.md / AGENTS.md を書く)
  "started": true,                         // 窓がある(エージェントが終了していても true)
  "alive": true,                           // エージェントが動いている
  "state": "waiting",                      // running | waiting | idle(hook から)
  "wait_kind": "permission",               // permission | question
  "pending_tool": {"name": "Bash", "input": "gh api …"},
  "last_message": "…",                     // 最後の返事
  "claude_session_id": "…",
  "state_since": "2026-11-08T21:37:30+09:00"
}
```

### KnownRepo オブジェクト

```jsonc
{
  "repo_id": "github.com_me_app",
  "name": "app",                 // 短い名前(~/worktrees/<name>)。設定ファイルしかないときは repo-id から推測
  "dir": "/Users/me/src/app",    // メインの worktree。場所が分からない(設定ファイルだけで、探しても見つからない)ときは省略
  "sessions": 2,                 // landed でないセッションの数
  "config": true,                // ~/.config/pekoe/repos/<repo-id>.toml がある
  "cwd": true                    // repo.list に渡した cwd がこのリポジトリの中(それ以外は省略)
}
```

### Preview オブジェクト

```jsonc
{
  "slug": "feat-x", "port": 3001, "path": "/settings", "mobile": false,
  "width": 1280, "height": 800,          // 次に撮るビューポート(CSS px)
  "after":  {"file": "~/.local/share/pekoe/<repo-id>/preview/feat-x/shot-7.png", "width": 1280, "height": 800, "path": "/settings", "mobile": false, "at": "..."},
  "before": {"file": ".../shot-6.png", ...},   // 1 つ前。これより古いものは消す
  "n": 7, "busy": false,
  "error": "dev server is not running · d to start",
  "watch": true, "rev": 42
}
```

### Review オブジェクト

```jsonc
{
  "slug": "feat-x", "base": "<sha>", "base_label": "review #2" | "main",
  "since": 2, "total": 9, "approved": 5,
  "files": [
    {"path": "src/api.ts", "status": "M", "hunks": [
      {"id": "<sha1>", "header": "@@ -82,6 +82,9 @@", "old_start": 82, "new_start": 82,
       "lines": [{"kind": " "|"+"|"-", "old": 82, "new": 0, "text": "..."}],
       "approved": false, "rejected": false, "prompt": {"n": 6, "text": "stream the rows"}}
    ]}
  ],
  "comments": [{"id": 1, "path": "src/api.ts", "line": 88, "hunk_id": "...", "text": "...", "sent": false}],
  "test": {...}, "diffstat": {...}
}
```

### IssueList / IssueDetail

```jsonc
// issues.list
{
  "repo_id": "github.com_me_app", "filter": "mine", "label": "",
  "fetched_at": "...", "cached": true,
  "unavailable": "no GitHub remote",   // gh が使えないときの 1 行(gh not installed / gh not authenticated · gh auth login / no GitHub remote)。issues は空
  "issues": [
    {"number": 12, "title": "Login fails on Safari", "labels": ["bug"], "assignees": ["me"],
     "updated_at": "...", "comments": 2, "url": "https://github.com/me/app/issues/12",
     "session": {"slug": "fix-login-fails-on-safari", "branch": "fix/login-fails-on-safari", "state": "running"}}
  ]
}
// issues.view: Issue の項目 + 本文とコメント
{"number": 12, "title": "...", "body": "...", "state": "OPEN", "author": "alice", "created_at": "...",
 "thread": [{"author": "bob", "body": "...", "created_at": "..."}], "session": null, ...}
```

`session` はその issue を扱っている生きたセッション。`--from gh#N` で作ったとき記録した `Session.issue` を優先し、なければブランチ名に番号があるもの(`fix/123-login`、`feat/issue-123`、`gh-123`)。キャッシュから返すときも突き合わせは毎回やり直す。

### PRView オブジェクト

```jsonc
{
  "number": 42, "title": "Add CSV export", "url": "https://github.com/me/app/pull/42",
  "state": "OPEN", "is_draft": false, "mergeable": "MERGEABLE", "merge_state_status": "BLOCKED",
  "review_decision": "CHANGES_REQUESTED", "base": "main", "head": "feat/csv",
  "checks": [{"name": "test", "workflow": "CI", "status": "COMPLETED", "conclusion": "FAILURE",
              "bucket": "fail",            // pass | fail | pending | skip
              "url": "…", "started_at": "…", "finished_at": "…", "seconds": 192,
              "run_id": 123, "job_id": 456}], // run_id / job_id は GitHub Actions のときだけ
  "reviews": [{"author": "bob", "state": "CHANGES_REQUESTED", "body": "…", "at": "…", "url": "…"}],
  "threads": [{"path": "src/api.ts", "line": 88, "original_line": 88, "resolved": false, "outdated": false,
               "comments": [{"author": "alice", "body": "…", "at": "…", "url": "…"}]}],
  "comments": [{"author": "carol", "body": "…", "at": "…", "url": "…"}],
  "fetched_at": "…"
}
```

`session.updated` は `pr_status` が変わったときだけ出る(取得時刻だけの変化では出ない)。gh が無い・未認証のときはポーリングを静かに飛ばす。

### ErrorReport オブジェクト

dev 窓の出力(Node / TypeScript / Next.js / Vite / Go の panic / Python の Traceback)とブラウザの console error から pekoed が拾ったエラー。メッセージと先頭フレームを正規化した指紋でまとめ、同じものは `count` が増える。

```jsonc
{
  "id": 3, "slug": "feat-x",
  "source": "dev",                 // dev | browser
  "port": 3001, "url": "http://localhost:3001/list",  // browser のときだけ
  "kind": "next",                  // 検出したルール: node | ts | next | vite | go | python | browser
  "message": "TypeError: Cannot read properties of undefined (reading 'id')",
  "file": "src/api.ts", "line": 12, "col": 7,  // 先頭フレーム(worktree 内なら相対パス)
  "in_tree": true,                 // file が worktree 内のプロジェクトのファイル
  "text": " ⨯ TypeError: …\n    at handler (…)",   // 出力そのまま(ANSI 除去済み、最新の 1 回分)
  "count": 3, "first": "…", "last": "…",
  "status": "new",                 // new | sent | ignored
  "fingerprint": "26c249cb96b4"
}
```

- 送った・無視したものは同じ指紋が再発するまで `error.list` に出ない。送信・無視の直後 3 秒以内の繰り返し(HMR の同じ出力)は再発に数えない
- ブラウザの console error のうち、スタックもスクリプト位置もないもの(`console.error("Warning: …")` など)はカードにしない。Browser パネルの件数と `browser.send_errors` には含まれる

### イベント

| event | data |
| --- | --- |
| `session.created` / `session.updated` / `session.removed` | `Session` |
| `session.progress` | `{slug, steps: [{name, label, status: "pending"\|"running"\|"ok"\|"fail", detail, progress?: 0-100, elapsed_ms}]}` |
| `session.waiting` | `Session`(running → waiting に変わった瞬間) |
| `session.done` | `Session` |
| `session.output` | `{slug, window}`(画面更新の合図。30ms で間引く) |
| `review.updated` | `{slug}` |
| `review.answer` | `{slug, hunk_id, text}` |
| `pick.received` | `Pick` |
| `preview.updated` | `Preview`(撮影開始・完了・失敗のたび。`rev` の小さいものは古い) |
| `browser.updated` | `[tabs]` |
| `error.updated` | `{report: ErrorReport, fresh}`(`fresh` は新規か、送信・無視後の再発。回数の更新や送信・無視では false) |
| `editor.open` | `{path, line, note, slug}` |
| `tapi.updated` | `Tapi` |
| `tapi.output` | `{}`(tapi chat の画面更新の合図。30ms で間引く) |
| `term.passthrough` | `{slug, seq}`(OSC 52 / OSC 9 / 777 など、外側の端末にそのまま流すシーケンス) |

## 2. HTTP(Chrome 拡張)

`http://127.0.0.1:<port>`(既定 47777、`~/.config/pekoe/config.toml` の `[http] port`)。

- すべてのリクエストに `Authorization: Bearer <token>` が必要。token は初回起動時に `~/.local/share/pekoe/http-token` に生成される(`pekoe ext` で port と token を表示)
- `Origin` が `chrome-extension://` で始まらないリクエストは 403
- 一方向。拡張は「その場で POST、送ったら終わり」

| path | body | 処理 |
| --- | --- | --- |
| `POST /pick` | `Pick` 入力(下記) | 保存して URL のポートからセッションを解決。指示つきで解決できれば即 pty へ。できなければ TUI に「どのセッションに送る?」 |
| `POST /console` | `{url, errors: [{message, source?, line?, stack?, at}]}` | Browser パネルにエラー数、`E` でセッションに送れる。ポートの持ち主のセッションがあれば、スタックかスクリプト位置のあるものはエラーカード(`error.*`)にもなる。localhost のページなら何でも送れるので上限がある: 本文 1 MiB、1 回 50 件(超えた分は捨てる)、message 2000 文字・stack 8000 文字・source / at / url 500 文字で切る、1 ポートにつき 1 分 120 件(超えた分は捨てる)。拡張も中継の前に同じ件数と長さに切る |
| `POST /tabs` | `{tabs: [{url, title}]}` | 開いている localhost タブの一覧 |
| `GET /health` | — | `{ok: true}`(token 必須) |

Pick 入力:

```jsonc
{
  "url": "http://localhost:3001/transactions", "title": "…",
  "instruction": "余白を広げて",            // 空なら要素情報だけ
  "outerHTML": "<button …>",               // 深さ・長さ制限済み
  "selector": "main > section > Card > button",
  "styles": {"margin": "…", "padding": "…", "font": "…", "color": "…", "background-color": "…", "width": "…", "height": "…", "border-radius": "…"},
  "component": {"name": "ExportButton", "framework": "react", "source": {"file": "src/components/ExportButton.tsx", "line": 41}, "via": "fiber _debugOwner"},
  "rect": {"x": 0, "y": 0, "width": 0, "height": 0, "dpr": 2},
  "screenshot": "data:image/png;base64,…"  // 切り抜き済み
}
```

## 3. HTTPS(Web UI、tailnet 上)

Web UI 入りのビルド(`make install WEB=1`)で `[web] enable = true` のときだけ、pekoed に埋め込んだ tsnet ノードが `https://<hostname>.<tailnet>.ts.net/` で待ち受ける。localhost には何も開かない。token は無く、接続ごとに Tailscale の WhoIs で `allow_users`(と `allow_devices`)に入っているかを確かめる。パスと認証・脅威モデルは [web.md](web.md)。

| method / path | body | 処理 |
| --- | --- | --- |
| `GET /api/me` | — | `{login, device}` |
| `GET /api/sessions` | — | `session.list` を待ち優先に並べ、表示用に絞ったもの |
| `GET /api/sessions/{slug}/screen` | — | `session.screen` の最後 80 行を色なしで |
| `POST /api/sessions/{slug}/answer` | `{answer: "allow"\|"deny", tool}` | `session.answer`。`always` は 400、待っていない・質問・プロンプトが変わったら 409 |
| `POST /api/sessions/{slug}/send` | `{text}` | `session.send`(`submit: true`)。allow / deny で答える許可待ちの間は 409。改行・タブ以外の制御文字を含むと 400 |

POST には `X-Pekoe-Web: 1` と `Content-Type: application/json` が要る(CSRF 対策)。

## 4. MCP(エージェント → nvim)

`pekoe mcp` が stdio の MCP サーバとして動き、デーモンに中継する。`claude mcp add pekoe -- pekoe mcp` で登録する。ツールは `open_in_editor(path, line, note?)` と `session_list()`。
