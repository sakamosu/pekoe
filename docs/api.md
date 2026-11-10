# pekoed API

すべてのクライアント(TUI・CLI・Neovim・Chrome 拡張・MCP)が叩く共通 API。賢さはデーモン側にあり、クライアントは表示と入力だけを持つ。

## 1. unix socket(TUI・CLI・Neovim)

次の順で最初に当たったもの(`internal/paths`):

1. `$PEKOE_SOCK`
2. `$PEKOE_HOME/share/pekoed.sock`(`$PEKOE_HOME` があれば)
3. `$XDG_DATA_HOME/pekoe/pekoed.sock`(`$XDG_DATA_HOME` があれば)
4. `~/.local/share/pekoe/pekoed.sock`

改行区切りの JSON(JSON Lines)。1 接続でリクエストとイベントが混ざって流れる。

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
  | `session.new` | ハブのエージェントの `pekoe new`(既知のリポジトリだけ。下) |
  | `session.send` | ハブのエージェントの `pekoe send`(制限つき。下) |
  | `repo.resolve` | `pekoe ls` / `pekoe new --repo` がリポジトリを決める |
  | `editor.open` | MCP の `open_in_editor`(nvim へのイベントを出すだけ) |
  | `land` | ハブのエージェントの `pekoe land`(origin のあるリポジトリだけ。下) |
  | `worktree.rm` | ハブのエージェントの `pekoe rm`(landed のセッションだけ。下) |
  | `report.post` | 作業セッションの `pekoe report`(呼び出し元のセッションの分だけ。`unknown` は断る。長さと件数の上限。下の「報告」) |
  | `report.list` | ハブのエージェントの `pekoe reports`(エージェントが書いた要約で、`session.list` と同じ公開度) |
  | `wait` | ハブのエージェントの `pekoe wait`(呼び出し元ごとに 8 本、1 時間まで。下の「待ち合わせ」) |
  | `send.log` | ハブのエージェントが自分の頼んだことを読み直す(token なしでは呼び出し元が送った・受けた記録だけ。下の「送信の記録」) |
  | `inbox.post` | エージェントの `pekoe ask-human`(人への問い。`unknown` は断る。呼び出し元ごとに未回答 10 件、長さの上限。下の「判断の受信箱」) |
  | `inbox.list` / `inbox.get` | ハブのエージェントの `pekoe inbox`、`ask-human --wait`(エージェントが書いた問い・pekoed が付けた事実・人の答え) |
  | `inbox.withdraw` | エージェントが自分の問いを取り下げる(自分が出したものだけ) |
  | `merge.check` | ハブのエージェント・ダッシュボードが「マージしてよい根拠」の何が足りないかを読む(`session.list` と `report.list` で見えるものからの計算だけ。下の「マージしてよいかの根拠」) |
  | `ledger.list` / `ledger.close` / `ledger.link` | tapi chat の `pekoe ledger`(依頼の台帳を読む・閉じる・セッションと結ぶ。tapi の窓からだけ。下の「依頼の台帳」) |
  | `conflicts.list` | ハブのエージェントの `pekoe conflicts`(並列にするか・順番にするかを、触るファイルの重なりで決める。同じ情報は `session.list` の各セッションの `overlaps` で既に見える。token なしでは計算し直しは 10 秒に 1 回まで。下の表) |

- それ以外はすべて token が要る。ないか違えば `<method>: only a person can do this: …` のエラーで、何もしない。人間の判断(`session.answer`・`session.keys`・`review.*` の承認やコメント・`error.send`・`pick.send` など)、sandbox の外で何かを動かすもの(`init.write`・`run.start`・`dev.toggle`・`review.surface`・`session.open`・`issues.*`・`pr.view`・`pr.failed_log` の gh 呼び出しなど)、秘密や他のセッションの中身を返すもの(`ext.info` の HTTP token、`web.status` のログイン URL、`session.screen`・`tapi.screen`・`error.get` / `error.list` のソースの抜粋、`changes`・`review.files` など)、TUI の内部の記録(`session.seen`・`session.resize`・`daemon.status` など)。分類と理由の一覧は `internal/daemon/human.go` の `agentMethods` / `humanMethods`(登録した全メソッドがどちらかに載っていることをテストで確かめている)
- `land` は token なしでも使える。push と PR 作成は、エージェントの sandbox が github.com / api.github.com を許していればエージェントが自分の Bash でもできるので、断っても守りにならない。テストは pekoed の sandbox で動き(下の「hooks の sandbox」)、マージは人間が決める。後片付け(teardown と worktree の削除)はマージを検出してから(`watchMerges`)。ただし origin のないリポジトリでは land がその場で base を fast-forward して worktree を消すので、token なしでは `land in a repository without origin …: only a person can do this` で断る(origin の有無は land の中で読む値で決める)
- `worktree.rm` は token なしでは landed のセッションだけを消す(`hAgentRm`)。landed のセッションは、マージを検知した pekoed が teardown と worktree の削除を済ませていて、残っているのは一覧の記録(DB の行・プロンプトやレビュー・付随ファイル)だけ、コードは base にある。ほかの状態(creating・running・waiting・idle・done・reviewing・approved・failed)は worktree と未コミットの変更を消すので `rm of a <state> session …: only a person can do this` で断る。`force` も断る。landed でも worktree のディレクトリが残っている(teardown や削除が失敗した)ときは、消さずに `… is still there …: only a person can do this` で断る(pekoe が作っていない worktree は元から消さないので、記録だけ消す)。状態は、セッションを一覧から外すのと同じロックの中でもう一度確かめる(その間に同じ slug の `session.new` が landed のセッションを置き換えても、新しい方は消さない)。token なしでは teardown も git も動かさない
- `session.send` は token なしでも使える(ハブのエージェントが `pekoe send` で指示を渡すため)。ただし token なしでは、エージェントの窓(`window` 省略か `agent`)だけに送れて、相手に許可ダイアログが出ている間は `submit` の有無にかかわらず断る(下の「send と許可ダイアログ」)
- `session.new` は token なしでは、pekoe が既に知っているリポジトリ(`repo.list` に出るもの)で、知っているディレクトリと同じ場所のときだけ受け付け、セッションはその知っているディレクトリから作る。repo-id は origin の URL から決まり、エージェントが作ったクローンでも同じ id になるので、ディレクトリも見る。新しいリポジトリの登録は人間だけの操作(pekoed はそこで git を動かし、エージェントが自作したリポジトリの設定の filter / merge ドライバは、sandbox が使えない環境では外で動く)
- `subscribe` は token なしでも使えるが、届くのは `session.created` / `session.updated` / `session.removed` / `session.progress` / `session.waiting` / `session.done` / `session.output` / `report.posted` だけ。ほかのイベント(人の答えを含む `inbox.updated`、他のセッションのクリップボードを運ぶ `term.passthrough`、`pick.received`、`error.updated`、`review.*` など)は token を付けて購読した接続にだけ流れる
- Web UI と Chrome 拡張はソケットを通らない(デーモンの中でハンドラを呼ぶ)ので、それぞれの認証(tailnet の WhoIs、http-token)だけで足りる
- 同じユーザーのプロセスはファイルを読めてしまうので、これだけでは塞ぎきれない。Claude Code のエージェントには pekoe が sandbox の設定(`--settings <データディレクトリ>/agent-settings/<slug>.json`。組み立ては `internal/daemon/agentsandbox.go`)を渡してトークン・`state.db`(と `-wal` / `-shm`)・tsnet の state・設定ディレクトリを読めなくする。中身・自分の設定との合わせ方・緩め方は README の「エージェントに pekoe を使わせるときの権限」

### 呼び出し元の識別

報告・送信の記録・`wait` の `mine`・受信箱は「誰が」呼んだかを使う。`$PEKOE_SESSION` や cwd はエージェントが偽れるので、pekoed はソケットの相手のプロセスから決める(`internal/daemon/caller.go`)。

1. 接続を受けたら相手の pid を取る(macOS は `getsockopt(SOL_LOCAL, LOCAL_PEERPID)`、Linux は `SO_PEERCRED`。`peer_*.go`)
2. その接続で呼び出し元が要る最初のリクエストのときに、親プロセスを辿る(64 段まで)。pekoe の窓の根のプロセス(tmux の `#{pane_pid}`。`Engine.Panes`)に当たれば、その窓の slug(tapi chat は `+tapi`)が呼び出し元
3. token つきのリクエストは `human`。token なしで窓に当たらなければ `unknown`

Claude Code のエージェントは pekoe が渡す sandbox の中で動き、tmux のソケットにはつなげない(`network.allowUnixSockets` は pekoed のソケットだけ)ので、別のセッションの窓の中にプロセスを作れない。二重 fork などで親を launchd に付け替えても `unknown` になるだけで、別のセッションには化けられない。

- クライアントは `$PEKOE_SESSION` をリクエストの最上位の `session` に入れて送る(`internal/client`)。pekoed はこれを名乗り(`claimed`)として、識別したものと違うときだけ記録に残す。振り分けには使わない
- `session.event`(hooks)は、窓に当たればその slug に振り分ける(`$PEKOE_SESSION` と cwd より優先)。Claude Code は hooks を sandbox の外で自分の子として起動するので、親を辿ると Bash のコマンドと同じ窓に当たる。当たらないもの(`pekoe adopt` で引き取った、tmux の外で動いているエージェントなど)は今までどおり `$PEKOE_SESSION` と cwd で決める。hooks は token を読めるので、ここでは token の有無ではなく窓を見る
- `report.post` と `inbox.*` も token の有無より窓を優先する(token を読めてしまう sandbox なしのエージェントでも、報告と問いは自分の窓のセッションに付く)

### hooks の sandbox

pekoed は sandbox の外で動くので、エージェントが書き換えられるコード(worktree の Makefile・package.json・テスト、エージェントのブランチを `--base` にした `pekoe new`)を pekoed が実行すると、そのコードは human-token を読めてしまう。`session.event` は token なしで受け付けるので、Stop を偽れば何度でも走らせられる。そこで、worktree から来るコマンドはすべて sandbox(`internal/sandbox`)で包む。

- 包むもの: Stop のたびの `[hooks] test`、`[review] gatekeeper`(`claude -p` も worktree の `CLAUDE.md` や `.claude/settings.json` を読む)、`session.new` の `setup`、`dev.toggle` の `dev`(tmux の窓のコマンドを `exec sandbox-exec … $SHELL -c '…'` にする)、`worktree.rm` / land 後の `teardown`、`land` のテスト。シェル・環境変数・cwd・標準入出力はそのまま。子プロセス(make → go test、npm → node)にも効く。pekoed が動かす git もすべてこの sandbox で包む(下の「pekoed が動かす git」。git だけは sandbox が使えなくても動かす)
- 包まないもの: `run.start`(run 窓。人間が打ったコマンドで、自分の端末で打つのと同じ)、エージェント自身の起動(Claude Code の sandbox の役目)、`review.surface` の nvim
- macOS: `/usr/bin/sandbox-exec`。プロファイルは `(allow default)` に deny を足す。パスは `-D` の引数で渡し、プロファイルに埋めない。Seatbelt は解決後のパスで照合するので、各パスを書いたままのものと symlink を解いたもの(`/tmp` → `/private/tmp`)の両方で塞ぐ
  - 読めない: `human-token`、`http-token`、tsnet の state(`[web] state_dir` と既定の場所)、`config.toml`(`[web] auth_key`)
  - 書けない: データディレクトリと設定ディレクトリ(sandbox を外す・hooks を書き換える・token を差し替えることを防ぐ)、あとで sandbox の外で実行されるもの(シェルの起動ファイル、`~/.gitconfig`・`~/.config/git`、`~/.ssh`、`~/.claude/settings*.json`、tmux・vim / nvim の設定、`~/Library/LaunchAgents`、`$PATH` のうち書き込めるディレクトリ。pekoed は git・gh・tmux を `$PATH` から起動する)。上のどれかの親ディレクトリも名前の変更・削除はできない(`~/.local/share` をずらせば規則の外に出てしまう)
  - つなげない: tmux のソケットのディレクトリ(`$TMUX_TMPDIR/tmux-<uid>`。つなげれば sandbox の外で動く窓を開ける)。pekoed のソケットにはつなげる(token なしの扱い)
  - `lsopen`・`appleevent-send` も拒否(`open` や Terminal の `do script` で外に出られる)。`launchctl submit` は sandbox の中からは launchd が受け付けない
- Linux: bubblewrap(`/usr/bin/bwrap` などの決まった場所だけを探す。`$PATH` は使わない: `~/.local/bin` に偽の bwrap を置かれると、次から素のまま動いてしまう)。`--dev-bind / /` で今の / をそのまま見せ、書けないものを `--ro-bind` で重ね、読めないファイルに `/dev/null`、ディレクトリと tmux のソケットのディレクトリに空の tmpfs を重ねる。`--unshare-pid` と新しい `/proc` で、`/proc/<pid>` から pekoed のメモリや環境を読めないようにし、`--die-with-parent` で pekoed と一緒に止まる。存在しないパスは重ねられない(bwrap がホスト側に作ってしまう)ので外す。pekoed は起動時にデータ・設定ディレクトリと token を作るので、要のものは必ずある
- 使えるかどうかは、初めて使うときに `sh -c :` を包んで動かして確かめる。通れば覚え、だめならその都度確かめ直す(bwrap を入れたらそのまま使える)
- 使えないとき(sandbox-exec が無い・動かない、bwrap が無い・ユーザー名前空間が使えない、sandbox の中で pekoed を動かしている、ほかの OS)は**動かさない**。Stop の test は `test.status` を `blocked`(`summary` に理由)にし、門番はリスクに理由を 1 件入れる(`path` は空)。setup・dev・teardown・land は理由を添えたエラーにする。人間が始めるものも止めるのは、`pekoe new` はエージェント(ハブ)も呼べて、`--base` にエージェントのブランチを渡せるから。dev は動き続ける。どうしても要るなら `config.toml` の `[sandbox] enable = false`(pekoed の再起動で効く。設定ディレクトリは sandbox から書けない)
- 残る穴: 塞ぐのは秘密と、知られた「あとで外で実行される場所」だけで、ホームのほかの場所(キャッシュ、`~/go/pkg/mod`、Homebrew の Cellar など)には書ける。そこに置いたものを人間や pekoed があとで sandbox の外で実行すれば抜けられる。Linux では、まだ無いファイルと親ディレクトリの名前の変更は塞げない

### pekoed が動かす git

git は設定に書かれたコマンドを動かす(`git status` のたびの `core.fsmonitor`、フック、credential helper、`.gitattributes` が選ぶ filter・merge・diff のドライバ、`gpg.program`、`core.sshCommand` など)。pekoed は diffstat・スナップショット・changes・review・undo・衝突予報・land・rm・PR のレビューで worktree の git を頻繁に動かし、sandbox の外で動く。一方、worktree の `.git` は `gitdir: <パス>` と書かれたファイルでエージェントが書き換えられ、偽の git ディレクトリに向ければその設定はすべてエージェントのもの。commit できるエージェントは本体の `.git`(objects・refs・`worktrees/<id>`)にも書けることが多く、`.git/config` や `config.worktree` も書けると見なす。token なしの `pekoe new --repo <パス>` には、エージェントが自分で作ったリポジトリも渡せる。そこで、pekoed の git(`internal/gitx` の `RunRaw` を必ず通る。`exec` で git を直接呼ぶのはここだけ)には三つの層をかける(`internal/gitx/guard.go`)。

1. **git ディレクトリの固定**: セッションの worktree(`session.new` / `session.open` / `review.pr` で登録、再起動時にも)では `.git` ファイルを信じない。本体の `.git/worktrees/*` のうち `gitdir` がその worktree を指し返すものを使い、`GIT_DIR`・`GIT_WORK_TREE`・`GIT_COMMON_DIR` を明示する。`.git` ファイルがそこを指していなければ(別の場所・symlink・ディレクトリ)、その worktree では git を動かさずにエラーを返し、セッションに `git_blocked`(理由)を立てる。TUI と Web UI は `.git changed: git stopped` と出す。diffstat のたび(編集・Stop)と再起動時に確かめ、元に戻れば消える。`gh` にも同じ環境を渡す(`gh pr create` は worktree で `git status` を動かす)
2. **設定の上書き**: コマンドを名指しする設定のうち、名前が決まっているものは `GIT_CONFIG_COUNT`(どの設定ファイルより強い)と環境変数で上書きする
   - 無効にする: `core.fsmonitor=false`、`core.hooksPath=/dev/null`(pekoed の git ではフックが一切走らない。land の push でも pre-push は走らない)、`core.alternateRefsCommand`、`protocol.ext.allow=never`(`ext::` の URL)
   - 環境変数で決める(設定より強い): `GIT_EDITOR=:`・`GIT_SEQUENCE_EDITOR=:`・`GIT_PAGER=cat`、`GIT_ASKPASS` と `GIT_PROXY_COMMAND` は pekoed の環境に無ければ空(`core.askPass`・`core.gitProxy` を読ませない)、`GIT_SSH_COMMAND` は pekoed の環境、利用者の `core.sshCommand`、`GIT_SSH`、`ssh` の順
   - 利用者自身の設定(system と global のスコープ。そこから include したものも含む。`git config --show-scope` で見分ける)か git の既定値: `gpg.program`・`gpg.<format>.program`・`gpg.ssh.defaultKeyCommand`・`gpg.format`・`commit.gpgSign`・`tag.gpgSign`・`push.gpgSign`・`merge.verifySignatures`・`log.showSignature`。credential helper は空の `credential.helper` でリポジトリのぶんまで空にしてから、利用者の helper(URL ごとのものも、順番どおり)を足し直す。利用者の設定はディレクトリごとに 10 秒覚える
   - 引数で決める: `diff`・`show`・`log` に `--no-ext-diff --no-textconv`、`fetch`・`pull`・`ls-remote` に `--upload-pack=git-upload-pack`、`push` に `--receive-pack=git-receive-pack`(`remote.<name>.uploadpack` は最初の値が勝つので設定では上書きできない。origin がローカルのパスならこの機械で動く)
3. **sandbox**: 名前で上書きできないもの(`.gitattributes` が選ぶ任意の名前の filter・merge ドライバ、git-lfs が読む `lfs.*`、origin がローカルのパスのときの相手側のフック(git は相手側に呼び出し側の設定を渡さない)、今後の git が足すコマンド)のために、git 自体を「hooks の sandbox」と同じ sandbox で動かす。ドライバは動くが、human-token などは読めず pekoe のファイルも書けない。git の起動が 1 回 10ms ほど遅くなる。git 設定の読み出し(`git config --show-scope`)は何も実行しないので包まない

- sandbox が使えないとき(sandbox-exec や bwrap が無い・動かない、`[sandbox] enable = false`)は、hooks と違って git は**そのまま動かす**(git が止まると pekoe が使えない)。1 と 2 は効くが、3 で塞いでいるもの(filter・merge ドライバ、`lfs.*`、ローカルの origin の相手側のフック)は残る。エージェントがリポジトリの `.git/config` に書けるか、自分で作ったリポジトリを `pekoe new --repo` に渡せば、それらが sandbox の外で走る。pekoed のログに `git: running without a sandbox (…)` と出る
- 対象外: 人間が自分で動かす git(シェル、run 窓、レビュー面の nvim の git プラグイン、本体のチェックアウトでの git)。エージェントが書いた `.git` ファイルや `.git/config` はそのまま読まれる。`git_blocked` が出たら、その worktree で git を動かす前に `.git` を確かめる。本体の `.git/config`・`.git/hooks` をエージェントに書かせない設定(Claude Code なら `sandbox.filesystem.denyWrite`)もあわせて使う
- `gh auth token`(`pekoe update`)と `gh attestation verify -R` は git を動かさないので素のまま

### send と許可ダイアログ

許可メニューは最初の 1 文字を選択肢として受け取り、Enter は選ばれている「Yes」を確定する。そこで `session.send` / `tapi.send` は、許可ダイアログが出ている間は文字を打たない。「出ている」は、hook が知らせた許可待ち(`wait_kind` が `question` 以外の `waiting`)か、agent 窓の画面の下に Claude Code の「Do you want to …」と「1. Yes」の選択肢(または最後の行の `[y/n]`)があること。

| いつ | だれ | 結果 |
| --- | --- | --- |
| 打つ前 | token なし(agent 窓への送信すべて) | `<slug> is waiting for a permission answer: answer it in the pekoe TUI first, then send (nothing was sent)`。受け付けた時と、セッションごとの送信ロックを取って打つ直前の 2 回確かめる |
| 受け付けた時 | token なしで、先頭(空白・改行を除く)が数字の送信 | `session.send without the human token: text may not start with a digit …`。直前の確認と打鍵の間の数ミリ秒にダイアログが出ると、先頭の数字がそのまま選択肢になる(「2」は always)ため |
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
  "test": {"status": "pass", "summary": "12 passed", "at": "..."},      // pass | fail | running | none | blocked(sandbox が使えず動かしていない。summary に理由)
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
    "head_sha": "3f2a…",           // GitHub 上の PR の HEAD(headRefOid)
    "last_comment": "…", "last_thread": "…",
    "attention": ["ci_failed"],    // 前回 PR ビューを開いてから起きたこと: ci_failed | changes_requested | new_comment | mergeable
    "attention_at": "…", "at": "…"
  },
  "error": "setup failed: pnpm install",
  "git_blocked": "…/.git points at …, not …",  // worktree の .git が書き換えられていて、pekoed がそこで git を動かさない理由(「pekoed が動かす git」)。戻れば消える
  "overlaps": [                    // 衝突予報(同じリポジトリの open なセッションのうち、同じファイルを触っている相手)
    {"slug": "feat-x", "files": ["src/api.ts", "README.md"], "conflict": true, "conflict_files": ["src/api.ts"]}
  ],
  "parent": "+tapi",               // session.new の呼び出し元(上の「呼び出し元の識別」): +tapi | <slug> | human | unknown。session.new 以外で作ったもの(open・adopt・review.pr)は空
  "report": {"id": 17, "status": "done", "summary": "CSV の空行を飛ばすように直した", "at": "…", "head": "3f2a…"}, // 最新の報告(summary は 1 行目、200 文字まで)
  "request": 4,                    // 結んだ台帳の依頼(pekoe new --request、ledger.link)。無ければ省略
  "after": ["feat-api"]            // このセッションが後に来るセッションの slug(session.new の after)。記録と表示だけで、起動は止めない
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
| `session.new` | `{repo, branch, from?, agent?, prompt?, base?, request?: id, after?: [slug]}` | `Session`(state = creating。進捗は `session.progress` イベント)。呼び出し元を `parent` に入れ、`prompt` を送信の記録に残す。`request` は台帳の依頼(open のもの)とセッションを結ぶ(token なしでは tapi の窓からだけ。断るときはセッションを作らない)。`after` は `Session.after` に入る(`<repo>/<slug>` も可、重複は 1 つに、10 個まで)。今ないセッション(`no such session`)・自分自身(着地済みの同じ slug を作り直すとき)・循環(`feat-a → feat-b → feat-a`)は断り、何も作らない。後から `pekoe rm` された相手は残る(TUI は `gone`) |
| `session.cancel` | `{slug}` | 作成中を中止し worktree を消す |
| `session.open` | `{path, resume?: "ask"\|"resume"\|"new", agent?}` | `Session` |
| `session.adopt` | `{path?}` | `[Session]` |
| `session.send` | `{slug, text, submit?: bool}` | pty に文字を書く。複数行は bracketed paste。`submit` で最後に Enter。テキストとして送るので、改行(CR・CRLF は LF に)とタブ以外の制御文字は落とす。キーは `session.keys` で送る。許可ダイアログが出ている間は送らない(下の「send と許可ダイアログ」)。token なしでは agent の窓だけ(上の「人間だけの操作」)。agent の窓への指示(`submit` つき、または token なし)は送信の記録に残す(断ったものも) |
| `session.keys` | `{slug, keys: [tmuxKey]}` / `{slug, literal: "abc"}` / `{slug, hex: "1b5b..."}` | 生キー転送(TUI の右ペインフォーカス時) |
| `session.answer` | `{slug, answer: "allow"\|"deny"\|"always", expect?: {tool}}` | 許可待ちに答える。`expect` を渡すと、待っているのがそのツール(`pending_tool` の name と input)のときだけ答え、違えば `the prompt changed; look again` のエラー(Web UI が使う) |
| `session.screen` | `{slug, window?: "agent"\|"review"\|"dev"\|"run"}` | `{lines: [ansi string], cursor: {x,y,visible}, width, height, dead, exit_status, signal?}`(`exit_status` は終わった窓の終了コード。動いている間やシグナルで終わったときは -1。`signal` は `"SIGINT"` など) |
| `session.resize` | `{slug, width, height}` | pty サイズ変更 |
| `session.seen` | `{slug}` | 未読ラインの基点を今に |
| `session.kill` | `{slug}` | セッション終了(worktree は残す) |
| `session.undo` | `{slug, n?}` | プロンプト単位 undo(省略時は最後のプロンプト前に戻す) |
| `session.event` | `{hook, session?, payload}` | hooks から(`pekoe event` が呼ぶ)。振り分けは窓が先、なければ `session`(`$PEKOE_SESSION`)と `payload.cwd` |
| `report.post` | `{summary, status: "done"\|"blocked"\|"progress"\|"review", evidence?: [string], verdict?: "pass"\|"fail", reviewed_sha?}` | `Report`(下記)。呼び出し元のセッションの報告(下の「報告」)。token つきなら `slug` で相手を指定できる |
| `report.list` | `{slug?, since?: id, limit?: 20}` | `[Report]`(新しい順。`limit` は 200 まで) |
| `wait` | `{slugs?: [slug], mine?: bool, on?: [kind], since?: cursor, timeout_s?: 1800}` | `{cursor, epoch, changes: [Change], timed_out?, reset?}`(下の「待ち合わせ」) |
| `send.log` | `{to?, from?, since?: id, limit?: 100}` | `[SendRecord]`(新しい順。下の「送信の記録」) |
| `inbox.post` | `{question, kind: "decision"\|"approve"\|"login"\|"fyi", options?: [string], recommend?: int, context?, links?: [string], about?: slug, decided?: int}` | `InboxItem`(下の「判断の受信箱」) |
| `inbox.list` | `{status?: "open"\|"answered"\|"withdrawn"\|"dismissed"\|"all", mine?: bool, limit?: 100}` | `[InboxItem]`(新しい順。既定は `open`。`limit` は 500 まで) |
| `inbox.get` | `{id}` | `InboxItem` |
| `inbox.withdraw` | `{id, note?}` | `InboxItem`。token なしでは自分が出したものだけ |
| `inbox.answer` | `{id, choice?: int, note?, rev}` | `InboxItem`。人だけ。`rev` が今のものと違えば `… the item changed; look again` |
| `inbox.dismiss` | `{id, note?}` | `InboxItem`。人だけ(答えずに閉じる) |
| `ledger.list` | `{status?: "open"\|"all", since?: id}` | `[LedgerEntry]`(古い順。既定は `open`。下の「依頼の台帳」)。token なしでは tapi の窓からだけ |
| `ledger.close` | `{id, as: "done"\|"dropped"\|"chat", evidence?: [string], note?}` | `LedgerEntry`。`dropped` は `note` が要る。`open` のものだけ。token なしでは tapi の窓からだけ |
| `ledger.link` | `{id, slug}` | `LedgerEntry`。セッションを依頼に結ぶ(`Session.request` も入る)。`open` のものだけ。token なしでは tapi の窓からだけ |
| `ledger.reopen` | `{id}` | `LedgerEntry`。人だけ(閉じたものを開き直す) |
| `ledger.delete` | `{id}` | `null`。人だけ |
| `attention.next` | `{from?: slug}` | 待ち時間順で次の waiting セッション。いなければ `pr_status.attention` のあるセッション(古い順) |
| `review.next` | — | 次のレビュー待ち(done) |
| `worktree.list` | `{repo_id?}` | `[{path, branch, slug, port, managed, current}]` |
| `worktree.rm` | `{slug, force}` | teardown → worktree 削除(`~/worktrees` 配下のみ)。token なしでは landed のセッションの記録を消すだけ(上) |
| `land` | `{slug}` | rebase → test → push → `gh pr create`。マージ検出後に自動 teardown |
| `dev.toggle` | `{slug}` | dev server の起動/停止 |
| `run.start` | `{slug, command?, width?, height?}` | `RunState` = `{running, finished, started, command, exit_status, signal?}`。worktree で `command` を 1 回だけ run 窓に起動する(dev と同じ環境変数、`$SHELL -c`)。run 窓が動いている・終わって閉じていない間は起動せずにその状態(実行中のコマンドと、終わっていれば終了コード。シグナルで終わったら `exit_status` は -1 で `signal: "SIGINT"`)を返す。`command` なしは状態を見るだけ。作成中・landed・worktree が無いセッションではエラー。呼び出しは 1 本ずつ処理する |
| `run.close` | `{slug}` | run 窓を閉じ、閉じる前の `RunState` を返す(TUI の「tell the agent?」用)。セッションの `run` を空にする |
| `changes` | `{slug}` | `[{status: "A"\|"M"\|"D", path}]`(base からの変更) |
| `conflicts.list` | `{repo_id?}` | `[{repo_id, a, b, status: "overlap"\|"conflict", files, conflict_files?}]`。その場で計算し直し、各セッションの `overlaps` も更新する(`pekoe conflicts`)。token なしでは、前の計算(pekoed が裏で回す約 10 秒ごとの計算を含む)から 10 秒以内ならそのときの結果を返し、git を動かさない(どのエージェントも 10 秒に 1 回より多く worktree の git を走らせられない)。git は pekoed の git(下の「pekoed が動かす git」)で、diff と `merge-tree` だけ |
| `review.start` | `{slug}` | state を reviewing に |
| `review.end` | `{slug}` | レビュー画面を閉じたとき。未送信コメントが無ければ reviewing → done（全 hunk 承認済みなら approved） |
| `review.files` | `{slug, show_approved?: bool, since?: n}` | `Review`(下記) |
| `review.approve` | `{slug, hunk_id}` | |
| `review.unapprove` | `{slug, hunk_id}` | |
| `review.reject` | `{slug, hunk_id, text?}` | 差し戻し(コメントとして溜まる) |
| `review.comment` | `{slug, path, line, hunk_id?, text}` | `{id}` |
| `review.comment.delete` | `{slug, id}` | |
| `review.ask` | `{slug, hunk_id, question?}` | エージェントに意図を聞く。返事は `review.answer` イベント。エージェントがターンの途中(running・waiting)なら断る |
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
| `merge.check` | `{slug?}` | `[MergeCheck]`(slug 順)。landed でない自分の PR のセッション(他人の PR のレビューを除く)ごとに、マージしてよいかの条件の何が揃っていて何が足りないか。`slug` を渡すとそのセッションだけ(下の「マージしてよいかの根拠」) |
| `pr.failed_log` | `{slug, check, run_id?, job_id?, lines?: 200}` | 失敗した Actions の check のログ末尾 `{check, run_id, job_id, log, truncated}`(`gh run view --job <id> --log-failed`、取れなければ run 単位) |
| `window.kill` | `{slug, window: "review"\|"dev"\|"run"}` | レビュー面 / dev server / run のウィンドウを閉じる(run は `run.close` と同じ) |
| `browser.send_errors` | `{port, slug?}` | 溜まった console error をまとめてセッションに送る(ファイルに書いて `@` 参照の 1 行。そのポートのエラーカードは sent になる) |
| `error.list` | `{slug?, all?}` | `[ErrorReport]`(古い順。既定は未対応 = `new` だけ、`all` で送信済み・無視も) |
| `error.get` | `{id}` | `ErrorReport` + `excerpt`(先頭フレームが worktree 内なら前後 5 行の抜粋) |
| `error.send` | `{id, slug?, text?}` | エラーをエージェントに送る。1 行で済むものはそのまま、それ以外は `~/.local/share/pekoe/<repo-id>/errors/<slug>-<n>.md` に書いて `@<path> <text>` の 1 行を送る。`ErrorReport`(sent) |
| `error.ignore` | `{id}` | 無視する(同じ指紋が再発するまで出さない)。`ErrorReport`(ignored) |
| `ext.info` | — | `{port, token}`(`pekoe ext`) |
| `web.status` | — | `{built, enabled, state, auth_url?, urls?, error?, backend?, backend_error?}`(`pekoe web`)。`built` はこのバイナリに Web UI が入っているか(`-tags web`)。state は `off` `unavailable`(Web UI なしのビルドで有効にされている)`starting` `needs-login` `running` `error`。`auth_url` は初回参加のログイン URL。`backend` は Tailscale の backend の状態(`NoState` `Starting` `Running` など)、`backend_error` は backend が control から受け取った最後のエラー(Running になると消える)。`starting` のまま進まない理由を見るため |
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

### 報告(report)

作業セッションが完了・詰まり・途中経過・独立レビューの結果を残す(`pekoe report`)。状態(`state`)は変えない(done は Stop hook で決まる)。最新の 1 件を `Session.report` に載せて `session.updated` を出し、全体を `report.posted` で流す。`pekoe rm` でそのセッションの分も消える。

```jsonc
// Report
{
  "id": 17, "slug": "feat-csv", "by": "feat-csv",  // by は識別した呼び出し元
  "claimed": "other",                              // $PEKOE_SESSION の名乗り。by と違うときだけ
  "status": "done", "summary": "…",
  "evidence": ["https://github.com/me/app/pull/42", "/tmp/shot.png"],
  "head": "3f2a…", "dirty": false,                 // 報告を受けた時点の worktree の HEAD と未コミットの変更の有無(pekoed が読む)
  "verdict": "pass", "reviewed_sha": "3f2a…",      // status = review のときだけ(verdict は必須)
  "at": "…"
}
```

- 呼び出し元(上の「呼び出し元の識別」)の窓のセッションに付く。token なしで `slug` に別のセッションを渡すと `a session reports only on itself` で断る。窓に当たらなければ `cannot tell which session is calling` で断る。tapi chat はセッションではないので断る
- 証拠のファイルは読まない(読めば、エージェントが pekoed に任意のファイルを読ませられる)。パスと URL を文字列として残すだけ
- 上限: summary 4000 文字、evidence 20 件・各 500 文字(超えたら断る)。summary は改行とタブ以外の制御文字を、evidence はすべての制御文字を落とす
- state.db の `reports` の表に残る

### 待ち合わせ(wait)

統括役が「何か起きるまで」止まって待つ(Claude Code のバックグラウンド実行で待てば、待っている間にトークンを使わない)。pekoed は変化を連番つきでメモリの輪(直近 2000 件)に積む。`since` より後に条件に合うものがあればすぐ返し、なければ起きるまで(か `timeout_s` まで)待つ。

```jsonc
// wait の結果
{
  "cursor": "9f3c01ab-1532",     // 次の wait の since に渡す(<epoch>-<連番>)
  "epoch": "9f3c01ab",           // pekoed の起動ごとに変わる
  "changes": [{"seq": 1531, "kind": "report", "slug": "feat-csv", "at": "…", "summary": "feat-csv: report done: CSV の空行を…"},
              {"seq": 1532, "kind": "done", "slug": "feat-csv", "at": "…", "summary": "feat-csv: done"}],
  "timed_out": false,
  "reset": false
}
```

- `kind`: `report`(報告が来た)・`done` `waiting` `idle` `failed` `landed`(その状態に入った)・`pr`(`pr_status.attention` に項目が増えた。1 項目ごと)・`removed`・`inbox`(自分が出した問いに人が答えた・答えずに閉じた。`summary` は `inbox #12 answered: 1 変える`。届くのは問いを出した呼び出し元だけで、`mine` にかかわらず含まれる。`slugs` は問いの `about` で絞る)。`on` を省くとすべて
- `summary` は 1 行: `feat-csv: done`、`feat-csv: waiting (permission)`、`feat-csv: failed: <エラー>`、`feat-csv: PR #42 ci_failed`、`feat-csv: report done: <要約の 1 行目>`。報告なしで done になった(最後のプロンプトの後に報告が無い)ものは `feat-csv: done (no report)`
- `since` を省くと「今から」。`since` の epoch が違う(pekoed が再起動した)・輪から落ちている・知らない番号なら、すぐに `reset: true` と今の `cursor` を返す。呼び出し側は `pekoe --json ls` を読み直す。永続化はしない
- 何も起きずに `timeout_s`(既定 1800、3600 まで)が過ぎたら `timed_out: true` と今の `cursor`。条件に合わなかった変化は飛ばして `cursor` は進む
- `mine` は `Session.parent` が呼び出し元のもの。`unknown` の呼び出し元は使えない。`slugs` は `<repo>/<slug>` も受け付ける
- token なしでは呼び出し元ごとに同時に 8 本まで。接続が切れると待ちも終わる

### 送信の記録

pekoed が仲立ちしたテキストの送信を、誰が誰に送ったか付きで state.db の `sends` の表に残す。

| 記録するもの | from | kind |
| --- | --- | --- |
| `session.new` の `prompt`(`from` があれば先頭に `--from gh#N` の行) | 呼び出し元 | `new` |
| `session.send` の agent の窓への指示(`submit` つき、または token なし) | 呼び出し元(TUI・CLI は `human`) | `send` |
| 受信箱の答えの配送(下の「判断の受信箱」) | `pekoed` | `inbox` |

```jsonc
// SendRecord
{"id": 3, "at": "…", "from": "+tapi", "to": "feat-csv", "kind": "new", "text": "…", "via": "socket", "ok": true, "error": ""}
```

- `via` はソケット経由なら `socket`、Web UI からなら `web:<端末名>`
- 記録しないもの: 打鍵(`session.keys`)、TUI が入力をそのまま流す `submit` なしの `session.send`、dev・run の窓への送信、`tapi.send`、ピック・エラー・レビューの送信
- 送れなかったもの(許可ダイアログが出ていた、先頭が数字だったなど。token なしの受け付け時の確認で断ったものも)も `ok: false` とエラーで残す
- 本文は 64 KiB で切る
- token なしの `send.log` は、呼び出し元が `from` か `to` の記録だけ。`unknown` は断る

### 判断の受信箱(inbox)

エージェントが人に判断・承認・手作業を頼む(`pekoe ask-human`)。人は TUI(トーストと `]a`)か CLI(`pekoe inbox answer`)で答え、答えは問いを出したエージェントに届く。state.db の `inbox` の表に残り、答えたもの・取り下げたものも消さない(前例として読む。`pekoe rm` でも消えない)。

```jsonc
// InboxItem
{
  "id": 12, "rev": 2, "kind": "decision",
  "from": "feat-pagination", "claimed": "",           // 識別した呼び出し元と、食い違うときの名乗り
  "about": "feat-pagination",                         // 事実を取るセッション(省くと呼び出し元のセッション)
  "question": "…", "options": ["変える", "今のオフセット式のまま"],
  "recommend": 0,                                     // エージェントの推奨(0 始まり)。意見として出し、既定の選択にしない
  "context": "…", "links": ["https://github.com/…/pull/42"],
  "facts": {                                          // pekoed が自分で読んだもの(エージェントは書けない)
    "slug": "feat-pagination", "state": "idle", "branch": "feat/pagination", "base": "main", "head": "3f2a…",
    "diffstat": {"added": 120, "deleted": 8, "files": 4},
    "pr": 42, "pr_url": "https://github.com/me/app/pull/42",
    "pr_status": {"state": "OPEN", "pass": 5, "fail": 0, "pending": 0, "review_decision": "APPROVED", "unresolved": 0, "merge_state": "CLEAN", "head_sha": "3f2a…"},
    "test": {"status": "pass"}, "overlaps": [], "report": {"id": 17, "status": "done", "at": "…", "head": "3f2a…"},  // 最新の報告(要約はエージェントの文なので入れない)
    "git_blocked": "", "gone": false,
    "merge": { …MergeCheck }                          // 自分の PR があり landed でないセッションだけ(下の「マージしてよいかの根拠」)
  },
  "status": "answered",                               // open | answered | withdrawn | dismissed
  "answer": {"choice": 0, "note": "…", "by": "human", "via": "socket", "at": "…"},
  "delivery": "delivered",                            // pending | delivered | waited | none
  "created_at": "…", "updated_at": "…"
}
```

- 添字(`recommend`・`decided`・`choice`)は 0 始まり。CLI と画面は 1 始まりで出す
- `kind`: `decision`(選ぶ。選択肢なしなら答えはメモで)・`approve`(選択肢を省くと「承認する」「承認しない」)・`login`(人の手作業。選択肢は「済んだ」「できない」に固定)・`fyi`(エージェントが自分で決めたことの記録。`decided` に選んだもの。`answered` で作られ、`answer.by` は呼び出し元)。選択肢が 1 つだけのものは断る
- `from` は呼び出し元(上の「呼び出し元の識別」)の窓。窓がなく token つきなら `human`(人の問いは誰にも届けない)、token なしで窓に当たらなければ断る
- 上限(超えたら切らずに断る): question 500 文字、options 6 個・各 200 文字、context 4000 文字、links 10 件・各 500 文字、メモ 4000 文字。呼び出し元ごとに未回答 10 件まで(`fyi` は数えない)。保存の前に ANSI・改行とタブ以外の制御文字・見えない書式文字(bidi の上書き・ゼロ幅)を落とす。選択肢とリンクは 1 行
- **facts と rev**: `facts` は `about` のセッションを pekoed が読んだもの(HEAD は git、ほかは pekoed が持つセッションの状態)。未回答のものは `inbox.list` / `inbox.get` / `inbox.answer` のたびに読み直し、変わっていれば `rev` を上げて `inbox.updated` を出す。`inbox.answer` は読み直した後の `rev` と比べるので、人が見てから HEAD が動いた・CI が落ちたなどの答えは断られる
- `inbox.withdraw` は token なしでは自分が出したものだけ。未回答のものだけ閉じられる(答え・取り下げ・閉じるのどれも、`open` 以外には断る)
- **答えの届け方**(`answer` と `dismiss`): 問いを出したエージェントが今 `wait` で待っていて、その `wait` が `inbox` を受け取るなら、その返り値で渡す(`delivery: waited`)。待っていなければ `pending` にして、依頼元のエージェントの入力欄が空いたときに 1 行打つ: `[pekoe inbox #12] 答え: 1「変える」(メモ: …) 詳しくは pekoe --json inbox 12`(閉じたものは `答えずに閉じられました`)。1 行に均し、先頭は `[`(数字にしない)
  - 打つ条件: セッションなら `idle` か `done` で agent の窓が生きている、tapi chat なら起動していて `idle`。どちらも許可ダイアログが出ていない(hooks の状態と画面の両方。受け付けた時と、送信ロックを取って打つ直前の 2 回。`session.send` の token なしの経路と同じ)。人がその窓に打った(TUI・CLI・Web UI の `session.send`・`session.keys`・`tapi.send`・`tapi.keys`)のが 30 秒以内なら待つ
  - 試すのは、依頼元が `idle` / `done` に入ったとき、答えた直後、5 秒ごと。1 回に 1 件(古い順)で、次は次に空いたとき
  - 依頼元が API で答えを読んだ(`inbox.get` / `inbox.list`、`wait` の返り値)ら `waited` にして打たない。依頼元のセッションが消えたら `none`
  - 打ったものは送信の記録に `from: pekoed, kind: inbox` で残る。Enter の直前に許可ダイアログが出た(文字は入力欄に残った)ときは打ち直さず、記録に `ok: false` で残す
- `inbox.updated` は token つきの購読にだけ届く。エージェントは `pekoe wait --on inbox`(や `ask-human --wait`)で自分宛ての答えだけを受け取る
- pekoed 自身も項目を作る(`from: "pekoed"`)。v1 では下の「マージしてよいかの根拠」だけ

### マージしてよいかの根拠

pekoed は `pekoe land` 済みのセッション(`landing`。push して PR を作り、人のマージを待っている)を 10 秒ごとに見て、下の条件が全部揃ったら受信箱に `from: "pekoed"`・`kind: "approve"` の項目を 1 件作る: `#42 をマージしてよい根拠が揃いました(feat-csv・HEAD 3f2a1b0)`。**pekoed はマージしない**(`gh pr merge` などを呼ばない。マージは人が GitHub でする)。

| 条件(`name`) | 揃っている |
| --- | --- |
| `land` | `pekoe land` が PR を作った(`landing`)、PR が `OPEN` |
| `review` | 最新の `status: review` の報告が `verdict: pass` で、その `reviewed_sha` が PR の HEAD(`pr_status.head_sha`)と同じ(短い SHA は前方一致) |
| `ci` | PR の HEAD のチェックが 1 件以上あり、失敗も実行中もない |
| `merge_state` | `pr_status.merge_state` が `CLEAN` |
| `conflicts` | 衝突予報(`overlaps`)に `conflict` がない(自動で合わさる重なりはよい) |
| `after` | `after` のセッションがすべて `landed`(`pekoe rm` された相手は「無い」で揃わない) |

```jsonc
// MergeCheck(merge.check の要素、InboxItem.facts.merge)
{
  "slug": "feat-csv", "pr": 42, "pr_head": "3f2a…",
  "ready": false,
  "conds": [
    {"name": "land", "ok": true, "detail": "pekoe land 済み(PR #42)"},
    {"name": "review", "ok": false, "detail": "レビューした 1b2c3d4 と PR の HEAD 3f2a1b0 が違う"},
    {"name": "ci", "ok": true, "detail": "5 件すべて成功"},
    {"name": "merge_state", "ok": true, "detail": "CLEAN"},
    {"name": "conflicts", "ok": true, "detail": "衝突の予報なし"},
    {"name": "after", "ok": false, "detail": "feat-a が running"}
  ],
  "missing": ["review", "after"],     // 揃っていない条件の name(conds の順)
  "inbox": 0                          // 開いている根拠の項目の id(merge.check だけ。無ければ省く)
}
```

- `conds` は上の表の順で常に 6 つ。`detail` は pekoed が書く 1 行(日本語)。`review` の `detail` には誰のレビューか(作者のセッションの中のレビューならそう書く。docs/orchestration.md の U3)を入れる
- 項目: `options` は `["マージした", "待つ", "差し戻す"]`、`recommend` はなし。`facts` は他の項目と同じ(`facts.merge` に上の判定)、`links` は PR の URL、`context` は条件ごとの `detail`。`facts` が変われば(CI が落ちた・HEAD が動いた)他の項目と同じく `rev` が上がり、古い `rev` への答えは断られる
- **取り消し**: 開いている項目は、条件が 1 つでも崩れる・PR の HEAD が動く・セッションが landed になる(答える前に GitHub でマージされた)・消えると、pekoed が `withdrawn` にする(`answer.by: "pekoed"`、`answer.note` に理由)
- **重ねて作らない**: 同じ PR・同じ HEAD には開いている項目を 1 件だけ。人が答えた(閉じた)ものは、HEAD が動くまで作り直さない。pekoed が自分で取り消したもの(条件が崩れた)は、同じ HEAD で条件が戻ればもう一度作る。最後に作った PR・HEAD・id は state.db の `kv` に残るので、pekoed を再起動しても重ならない
- **答え**: `マージした`(0)と `待つ`(1)は記録だけ(マージの検知は今までどおり `watchMerges`)。`差し戻す`(2)は `note` が必須で、メモをそのセッションに送る: 受信箱の答えと同じ経路(エージェントが `idle` か `done`・許可ダイアログなし・人が 30 秒打っていない、受け付け時と打つ直前の 2 回の確認)で `[pekoe inbox #12] 人が PR #42 のマージを差し戻しました(メモ: …) 詳しくは pekoe --json inbox 12` を 1 行打つ。送信の記録には `from: pekoed, kind: inbox` で残る

### 依頼の台帳(ledger)

人が tapi chat に頼んだことを、完了まで追う(`pekoe ledger`)。state.db の `ledger` の表に残り、`tapi.reset` でも `pekoe rm` でも消えない。

```jsonc
// LedgerEntry
{
  "id": 4, "at": "…",
  "text": "CSV の空行を飛ばすように直して",   // 人が tapi に打った文(16000 文字まで。超えた分は切って … )
  "status": "done",                          // open | done | dropped(やらないことにした)| chat(依頼ではなかった)
  "sessions": ["fix-csv"],                   // 結んだセッション(session.new の request、ledger.link)
  "evidence": ["https://github.com/me/app/pull/42"],
  "note": "PR #42 で直した",
  "closed_at": "…"
}
```

- **記録**: tapi の UserPromptSubmit hook(`pekoe event`)が tapi の窓から来たとき(上の「呼び出し元の識別」で窓が `+tapi` と分かったとき)に、`prompt` を `open` で記録する。`$PEKOE_SESSION=+tapi` を名乗るだけの呼び出し(窓に当たらない・別のセッションの窓)は tapi の状態は動かしても台帳には入れない(台帳の文は tapi が「人に頼まれたこと」として読むため)。pekoed 自身が tapi の入力欄に打ったもの(`[pekoe` で始まる受信箱の答え)と空の入力は入れない。保存の前に ANSI・改行とタブ以外の制御文字・見えない書式文字を落とす。hooks の無いエージェント(codex など)の tapi では記録されない
- **閉じる**: tapi が `ledger.close`(`pekoe ledger done <id> --evidence …`・`drop <id> --note …`・`close <id> --as chat`)。`evidence` は `report.post` と同じ上限(20 件・各 500 文字、1 行)、`note` は 4000 文字まで。`open` 以外は断る(`… is done already (a person can reopen it …)`)
- **結ぶ**: `session.new` の `request`(`pekoe new … --request <id>`)か `ledger.link`。どちらも `Session.request` を入れ、依頼の `sessions` に slug を足す(同じものは 1 回)。`request` の依頼が無い・閉じているときは、セッションを作らずに断る
- **人だけ**: `ledger.reopen`(閉じたものを `open` に戻す。`closed_at` を消し、`note` と `evidence` は残す)・`ledger.delete`。tapi が自分の取りこぼしを消したり戻したりできないように
- token なしでは `ledger.*` はすべて tapi の窓からだけ(main セッションのハブは台帳を使わない。docs/orchestration.md の U13)
- 台帳か受信箱が変わると `tapi.updated` も出る(`Tapi.ledger_open` / `inbox_open`)

### Tapi オブジェクト

tapi chat(TUI の `t`、docs/tapi-chat.md)。統括役のエージェントで、セッションではないので `session.*` には出てこない(`session.list` に載らず、tapi 自身の状態は state.db にも保存しない(台帳は上の「依頼の台帳」)。Web UI からも見えない)。tmux の窓は `+tapi~agent`、hook は `$PEKOE_SESSION=+tapi`(`+` はセッションの slug に現れない)か、cwd が tapi の稼働ディレクトリの中なら tapi に振り分ける。

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
  "state_since": "2026-11-08T21:37:30+09:00",
  "inbox_open": 2,                         // 受信箱の未回答(全員の分)
  "ledger_open": 1                         // 台帳の open な依頼
}
```

- tapi は統括役(docs/orchestration.md「tapi を統括役にする」)。pekoed が稼働ディレクトリに書く `CLAUDE.md` / `AGENTS.md` と `--append-system-prompt` の人格に、自分では編集せずセッションに任せること・会話の最初に `pekoe --json ledger`・`inbox`・`ls` を読むことを書き、`CLAUDE.md` の末尾に起動した時点の台帳・受信箱・任せたセッションの件数(中身は書かない)を入れる
- Claude Code の tapi には、セッションと同じ `--settings` に加えて、`--add-dir` で読ませている各リポジトリと `~/worktrees`(まだ無くても。シンボリックリンクを解いたものも)を `sandbox.filesystem.denyWrite` と `permissions.deny` の `Edit(...)` に入れる。Bash でも Edit / Write でも書けない。稼働ディレクトリは書ける。`[agents.<tapi の agent>] sandbox = false` ならこれも渡さない(ほかのエージェントにも渡さない)
- `tapi.reset` は会話だけを消し、台帳・受信箱・報告・送信の記録(state.db)には触らない

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
| `report.posted` | `Report`(token なしの購読にも届く) |
| `inbox.updated` | `InboxItem`(作られた・事実が変わった・答えた・取り下げた・届けた。人の答えを含むので token つきの購読にだけ届く) |
| `ledger.updated` | `LedgerEntry`(記録した・閉じた・結んだ・開き直した。token つきの購読にだけ届く) |
| `ledger.removed` | `{id}`(人が消した。token つきの購読にだけ届く) |
| `review.updated` | `{slug}` |
| `review.answer` | `{slug, hunk_id, text}` |
| `pick.received` | `Pick` |
| `preview.updated` | `Preview`(撮影開始・完了・失敗のたび。`rev` の小さいものは古い) |
| `browser.updated` | `[tabs]` |
| `error.updated` | `{report: ErrorReport, fresh}`(`fresh` は新規か、送信・無視後の再発。回数の更新や送信・無視では false) |
| `editor.open` | `{path, line, note, slug}` |
| `tapi.updated` | `Tapi`(受信箱・台帳の件数が変わったときも) |
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
