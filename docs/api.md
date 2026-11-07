# bobad API

すべてのクライアント(TUI・CLI・Neovim・Chrome 拡張・MCP)が叩く共通 API。賢さはデーモン側にあり、クライアントは表示と入力だけを持つ。

## 1. unix socket(TUI・CLI・Neovim)

`~/.local/share/boba/bobad.sock`(`$BOBA_HOME` があれば `$BOBA_HOME/share/bobad.sock`)。改行区切りの JSON(JSON Lines)。1 接続でリクエストとイベントが混ざって流れる。

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

### Session オブジェクト

```jsonc
{
  "slug": "feat-csv",              // worktree・セッションの識別子(branch の / を - に)
  "branch": "feat/csv",
  "repo": "app",                   // リポジトリ名
  "repo_id": "github.com_me_app",
  "path": "/Users/me/worktrees/app/feat-csv",
  "agent": "claude",
  "state": "running",              // creating | running | waiting | idle | done | reviewing | approved | landed | failed
  "wait_kind": "permission",       // waiting のときだけ: permission | question
  "state_since": "2026-11-07T10:00:00Z",
  "port": 3001,
  "last_tool": "Edit src/api.ts",  // PreToolUse の直近
  "pending_tool": {"name": "Bash", "input": "pnpm prisma migrate dev"}, // 許可待ちの対象
  "test": {"status": "pass", "summary": "12 passed", "at": "..."},      // pass | fail | running | none
  "diffstat": {"added": 120, "deleted": 8, "files": 4},
  "prompts": 7,                    // スナップショット数
  "review_n": 2,                   // 最後のレビュー地点番号(0 = 未レビュー)
  "claude_session_id": "…",
  "unread": 1234,                  // 離れてからの未読バイト数
  "activity": [0,1,3,5,8,6,4,5,7,9], // 直近の活動量(スパークライン用、古い順)
  "tokens": 42000,
  "dev": true,                     // dev server 起動中
  "pr": 123,                       // セッションの PR 番号(gh#N のレビュー、land、自動検出)
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
  "error": "setup failed: pnpm install"
}
```

### メソッド

| method | params | result |
| --- | --- | --- |
| `daemon.status` | — | `{pid, version, sessions, http_port}` |
| `daemon.stop` | — | `null` |
| `subscribe` | `{events: [prefix]}` | `null` |
| `session.list` | `{repo_id?}` | `[Session]` |
| `session.get` | `{slug}` | `Session` |
| `session.new` | `{repo, branch, from?, agent?, prompt?}` | `Session`(state = creating。進捗は `session.progress` イベント) |
| `session.cancel` | `{slug}` | 作成中を中止し worktree を消す |
| `session.open` | `{path, resume?: "ask"\|"resume"\|"new", agent?}` | `Session` |
| `session.adopt` | `{path?}` | `[Session]` |
| `session.send` | `{slug, text, submit?: bool}` | pty に文字を書く。複数行は bracketed paste。`submit` で最後に Enter |
| `session.keys` | `{slug, keys: [tmuxKey]}` / `{slug, literal: "abc"}` / `{slug, hex: "1b5b..."}` | 生キー転送(TUI の右ペインフォーカス時) |
| `session.answer` | `{slug, answer: "allow"\|"deny"\|"always"}` | 許可待ちに答える |
| `session.screen` | `{slug, window?: "agent"\|"review"\|"dev"}` | `{lines: [ansi string], cursor: {x,y,visible}, width, height}` |
| `session.resize` | `{slug, width, height}` | pty サイズ変更 |
| `session.seen` | `{slug}` | 未読ラインの基点を今に |
| `session.kill` | `{slug}` | セッション終了(worktree は残す) |
| `session.undo` | `{slug, n?}` | プロンプト単位 undo(省略時は最後のプロンプト前に戻す) |
| `session.event` | `{hook, payload}` | hooks から(`boba event` が呼ぶ) |
| `attention.next` | `{from?: slug}` | 待ち時間順で次の waiting セッション。いなければ `pr_status.attention` のあるセッション(古い順) |
| `review.next` | — | 次のレビュー待ち(done) |
| `worktree.list` | `{repo_id?}` | `[{path, branch, slug, port, managed, current}]` |
| `worktree.rm` | `{slug}` | teardown → worktree 削除(`~/worktrees` 配下のみ) |
| `land` | `{slug}` | rebase → test → push → `gh pr create`。マージ検出後に自動 teardown |
| `dev.toggle` | `{slug}` | dev server の起動/停止 |
| `changes` | `{slug}` | `[{status: "A"\|"M"\|"D", path}]`(base からの変更) |
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
| `review.surface` | `{slug, width, height}` | 右ペインを nvim(boba.nvim のレビュー面)に入れ替える。claude は裏で動き続ける |
| `review.pr` | `{repo, number}` | `boba review gh#N`: PR ブランチの worktree を作り、同じレビュー面で開く。コメントは GitHub review に送られる |
| `pr.view` | `{slug, seen?: bool}` | `PRView`(下記)。PR が無ければ `null`(番号未登録ならブランチから探す)。`gh api graphql` 1 回で取り、ついでに `pr_status` を更新する。`seen` で attention を既読に |
| `pr.seen` | `{slug}` | `pr_status.attention` を既読にする(取得はしない) |
| `pr.failed_log` | `{slug, check, run_id?, job_id?, lines?: 200}` | 失敗した Actions の check のログ末尾 `{check, run_id, job_id, log, truncated}`(`gh run view --job <id> --log-failed`、取れなければ run 単位) |
| `window.kill` | `{slug, window: "review"\|"dev"}` | レビュー面 / dev server のウィンドウを閉じる |
| `browser.send_errors` | `{port, slug?}` | 溜まった console error をセッションに送る |
| `ext.info` | — | `{port, token}`(`boba ext`) |

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
| `browser.updated` | `[tabs]` |
| `editor.open` | `{path, line, note, slug}` |
| `term.passthrough` | `{slug, seq}`(OSC 52 / OSC 9 / 777 など、外側の端末にそのまま流すシーケンス) |

## 2. HTTP(Chrome 拡張)

`http://127.0.0.1:<port>`(既定 47777、`~/.config/boba/config.toml` の `[http] port`)。

- すべてのリクエストに `Authorization: Bearer <token>` が必要。token は初回起動時に `~/.local/share/boba/http-token` に生成される(`boba ext` で port と token を表示)
- `Origin` が `chrome-extension://` で始まらないリクエストは 403
- 一方向。拡張は「その場で POST、送ったら終わり」

| path | body | 処理 |
| --- | --- | --- |
| `POST /pick` | `Pick` 入力(下記) | 保存して URL のポートからセッションを解決。指示つきで解決できれば即 pty へ。できなければ TUI に「どのセッションに送る?」 |
| `POST /console` | `{url, errors: [{message, source?, line?, stack?, at}]}` | Browser パネルにエラー数、`E` でセッションに送れる |
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

## 3. MCP(エージェント → nvim)

`boba mcp` が stdio の MCP サーバとして動き、デーモンに中継する。`claude mcp add boba -- boba mcp` で登録する。ツールは `open_in_editor(path, line, note?)` と `session_list()`。
