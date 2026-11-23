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
  | `session.new` | ハブのエージェントの `pekoe new`(既知のリポジトリと、sandbox の付くエージェントか呼び出し元と同じエージェントだけ。下) |
  | `session.send` | ハブのエージェントの `pekoe send`(制限つき。下) |
  | `repo.resolve` | `pekoe ls` / `pekoe new --repo` がリポジトリを決める |
  | `editor.open` | MCP の `open_in_editor`(nvim へのイベントを出すだけ) |
  | `land` | ハブのエージェントの `pekoe land`(origin のあるリポジトリだけ。下) |
  | `worktree.rm` | ハブのエージェントの `pekoe rm`(landed のセッションだけ。下) |
  | `dev.toggle` | 作業セッションの `pekoe dev <自分の slug>`(呼び出し元のセッションの dev server だけ。`unknown` は断る。下) |
  | `dev.log` | 作業セッションの `pekoe dev --log`(呼び出し元のセッションの dev hook のログだけ。`unknown` は断る。下) |
  | `report.post` | 作業セッションの `pekoe report`(呼び出し元のセッションの分だけ。`unknown` は断る。長さと件数の上限。下の「報告」) |
  | `report.list` | ハブのエージェントの `pekoe reports`(エージェントが書いた要約で、`session.list` と同じ公開度) |
  | `wait` | ハブのエージェントの `pekoe wait`(呼び出し元ごとに 8 本、1 時間まで。下の「待ち合わせ」) |
  | `send.log` | ハブのエージェントが自分の頼んだことを読み直す(token なしでは呼び出し元が送った・受けた記録だけ。下の「送信の記録」) |
  | `inbox.post` | エージェントの `pekoe ask-human`(人への問い。`unknown` と、`human`・`unknown`・`pekoed` という名前のセッション(名前の予約より前に作られたもの)の窓からは token つきでも断る。呼び出し元ごとに未回答 10 件、長さの上限。下の「判断の受信箱」) |
  | `inbox.list` / `inbox.get` | ハブのエージェントの `pekoe inbox`、`ask-human --wait`(エージェントが書いた問い・pekoed が付けた事実・人の答え) |
  | `inbox.withdraw` | エージェントが自分の問いを取り下げる(自分が出したものだけ。エージェントの窓からは token つきでも同じ。人(呼び出し元が `human`)は誰の問いでも取り下げられ、そのときは問いを出したエージェントに知らせる) |
  | `merge.check` | ハブのエージェント・ダッシュボードが「マージしてよい根拠」の何が足りないかを読む(`session.list` と `report.list` で見えるものからの計算だけ。下の「マージしてよいかの根拠」) |
  | `stats` | tapi chat・ハブのエージェントの `pekoe stats`(日ごと・リポジトリごとのマージ数とトークン。数・日付・リポジトリ名・slug・PR 番号だけで、エージェントが書いた文を含まない。下の「集計」) |
  | `tips.next` | tapi chat のあいさつの `pekoe tip`(組み込みの一覧から次の tip を選び、TUI と共通の `tips.json` に記録する。params なし。pekoed が書くのはこのファイルだけ。tapi chat の sandbox は DataDir に書けないので pekoed が代わりに記録する。pekoed が読んだ `[keys] prefix` も返す) |
  | `tips.prefix` | `pekoe tip --list` / `pekoe tip <id>` が tip の `^q` を `[keys] prefix` に置き換えるため(pekoed が読んだ prefix だけを返す。params なし。tapi chat の sandbox は config.toml を隠すので、CLI が自分で読むと既定の `ctrl+q` になる) |
  | `ledger.list` / `ledger.close` / `ledger.link` / `ledger.keep` | tapi chat の `pekoe ledger`(依頼を読む・閉じる・セッションと結ぶ・依頼として残す。tapi の窓からだけ。下の「依頼(ledger)」) |
  | `cleanup.list` | ハブのエージェントの `pekoe cleanup`(片付けの候補を読むだけ。`session.list` で見えるものを理由ごとに分け、worktree の未コミットのファイル数と push していないコミット数を足す。git は worktree ごとに 10 秒に 1 回まで。消すのは `worktree.rm` で、token なしでは landed だけ。下の表) |
  | `conflicts.list` | ハブのエージェントの `pekoe conflicts`(並列にするか・順番にするかを、触るファイルの重なりで決める。同じ情報は `session.list` の各セッションの `overlaps` で既に見える。token なしでは計算し直しは 10 秒に 1 回まで。下の表) |

- それ以外はすべて token が要る。ないか違えば `<method>: only a person can do this: …` のエラーで、何もしない。人間の判断(`session.answer`・`session.keys`・エージェントのターンを止める `session.interrupt` / `tapi.interrupt`・`review.*` の承認やコメント・`error.send`・`pick.send`・リポジトリを忘れさせる `repo.forget` など)、sandbox の外で何かを動かすもの(`init.write`・`run.start`・`review.surface`・`session.open`・`issues.*`・`pr.view`・`pr.failed_log` の gh 呼び出しなど)、秘密や他のセッションの中身を返すもの(`ext.info` の HTTP token、`web.status` のログイン URL、ブラウザを人としてサインインさせる一度きりのトークンを書く `gui.open` と、ローカル GUI の `gui.status`・`gui.revoke`・`gui.stop`、`session.screen`・`tapi.screen`・`tapi.transcript`・セッションの会話と送信欄の補助の `session.transcript` / `session.commands` / `session.files`・ローカル GUI の Settings が読む `config.show`(使っている設定。設定のディレクトリはエージェントの sandbox で隠しているので、pekoed が代わりに読ませない)・Session の右パネルの `session.panel`(エージェントのコマンドを `--version` で動かす)・`error.get` / `error.list` のソースの抜粋、`changes`・GUI の Changes が 1 ファイルの差分を読む `changes.diff`・`review.files` など)、TUI の内部の記録(`session.seen`・`session.resize`・`daemon.status` など)。分類と理由の一覧は `internal/daemon/human.go` の `agentMethods` / `humanMethods`(登録した全メソッドがどちらかに載っていることをテストで確かめている)
- `land` は token なしでも使える。push と PR 作成は、エージェントの sandbox が github.com / api.github.com を許していればエージェントが自分の Bash でもできるので、断っても守りにならない。テストは pekoed の sandbox で動き(下の「hooks の sandbox」)、マージは人間が決める(pekoed がマージするのは、根拠の項目に人が `merge` と答えたときだけ。下の「マージしてよいかの根拠」)。後片付け(teardown と worktree の削除)はマージを検出してから(下の「PR の状態のポーリング」が `MERGED` を見たその回、または `watchMerges` の早い方)。ただし origin のないリポジトリでは land がその場で base を fast-forward して worktree を消すので、token なしでは `land in a repository without origin …: only a person can do this` で断る(origin の有無は land の中で読む値で決める)
- `worktree.rm` は token なしでは landed のセッションだけを消す(`hAgentRm`)。landed のセッションは、マージを検知した pekoed が teardown と worktree の削除を済ませていて、残っているのは一覧の記録(DB の行・プロンプトやレビュー・付随ファイル)だけ、コードは base にある。ほかの状態(creating・running・waiting・idle・done・reviewing・approved・failed)は worktree と未コミットの変更を消すので `rm of a <state> session …: only a person can do this` で断る。`force` も断る。landed でも worktree のディレクトリが残っている(teardown や削除が失敗した)ときは、消さずに `… is still there …: only a person can do this` で断る(pekoe が作っていない worktree は元から消さないので、記録だけ消す)。状態は、セッションを一覧から外すのと同じロックの中でもう一度確かめる(その間に同じ slug の `session.new` が landed のセッションを置き換えても、新しい方は消さない)。token なしでは teardown も git も動かさない
- `dev.toggle` は token なしでは、呼び出し元の窓のセッション(下の「呼び出し元の識別」)の dev server だけを起動・停止する(`hAgentDevToggle`)。エージェントの sandbox はポートを listen できないので、UI を確かめるには pekoed に dev server を立ててもらうしかない。動かすのはリポジトリの設定の `[hooks] dev` で、Stop のたびに誰でも走らせられる `[hooks] test` と同じく hooks の sandbox で動く(下の「hooks の sandbox」)ので、新しい実行の経路は増えない。別の slug は `dev.toggle: a session starts or stops only its own dev server (<自分>, not <相手>): only a person can do this`、窓に当たらない呼び出し(`unknown`)は `pekoe cannot tell which session is calling` で断る。tapi chat の窓からも断る。1 セッションに dev server は 1 つ(動いていれば止める)は token つきと同じ
- `session.send` は token なしでも使える(ハブのエージェントが `pekoe send` で指示を渡すため)。ただし token なしでは、エージェントの窓(`window` 省略か `agent`)だけに送れて、相手に許可ダイアログが出ている間は `submit` の有無にかかわらず断る(下の「send と許可ダイアログ」)。先頭(空白・改行を除く)が `!` の文も `session.send without the human token: text may not start with '!' …` で断る(`submit` の有無にかかわらず)。Claude Code(と opencode)の空の入力欄で最初に打った `!` はシェルモードに切り替え、残りをその場でコマンドとして動かす(許可ダイアログなし)ので、エージェントが別のセッション・自分・tapi にコマンドを動かさせる道になる。確かめるのは打鍵の層(`send(p, agent)`)で、token なしの文が打たれる経路はすべてここを通る。断ったものは送信の記録に `ok: false` で残し、pekoed のログに `session.send from <呼び出し元> to <slug> refused: the text starts with '!'` を出す。人(token あり・Web UI・ローカル GUI)の send は今までどおり `!` を通す(人のシェルモード)。sandbox なしで token を読めるエージェントは token を付けて送るので止まらない(token で `session.keys` も打てるので、ここは守れない)
- `session.new` は token なしでは、pekoe が既に知っているリポジトリ(`repo.list` に出るもの)で、知っているディレクトリと同じ場所のときだけ受け付け、セッションはその知っているディレクトリから作る。repo-id は origin の URL から決まり、エージェントが作ったクローンでも同じ id になるので、ディレクトリも見る。新しいリポジトリの登録は人間だけの操作(pekoed はそこで git を動かし、エージェントが自作したリポジトリの設定の filter / merge ドライバは、sandbox が使えない環境では外で動く)
- `session.new` は token なしでは、起動するエージェント(`agent`、無ければリポジトリの `agent`、それも無ければ `claude`)を、pekoe の sandbox が付くもの(コマンドが Claude Code で `[agents.<name>] sandbox` が false でないもの。`--settings` を付ける判定と同じ)か、呼び出し元の窓のエージェントと同じ名前のもの(tapi なら `[tapi] agent`)に限る。プロンプトは呼び出し元が書くので、sandbox の付かないエージェント(codex・opencode など)で起動すると、呼び出し元の sandbox の外でその文を動かすことになる。それ以外は `session.new with agent "codex": pekoe does not sandbox that agent …; start that session from the TUI or your own terminal: only a person can do this …` で断り、何も作らない。人(token あり)は今までどおりどのエージェントでも作れる
- `session.new` は token なしでは、先頭(空白・改行を除く)が `!` の `prompt` を `session.new without the human token: the prompt may not start with '!' …` で断り、何も作らない(送信の記録に `ok: false`、pekoed のログに `session.new from <呼び出し元> refused …`)。`prompt = "paste"` のエージェント(opencode)は初期プロンプトを入力欄に打つので、`session.send` の `!` と同じことになる。コマンドラインで渡すエージェントでも、読み方に頼らないよう同じく断る
- `session.new` の `from` は `gh#N`(N は 1〜10 桁の数、先頭は 0 でない)だけ受け付ける(token の有無によらない)。pekoed は人の gh 認証で `gh issue view N` を動かすので、URL を渡して別のリポジトリの issue を読ませない。それ以外は `from "…": give an issue of this repository as gh#N` で断る
- `subscribe` は token なしでも使えるが、届くのは `session.created` / `session.updated` / `session.removed` / `session.progress` / `session.waiting` / `session.done` / `session.output` / `report.posted` だけ。ほかのイベント(人の答えを含む `inbox.updated`、他のセッションのクリップボードを運ぶ `term.passthrough`、`pick.received`、`error.updated`、`review.*` など)は token を付けて購読した接続にだけ流れる
- Web UI と Chrome 拡張はソケットを通らない(デーモンの中でハンドラを呼ぶ)ので、それぞれの認証(tailnet の WhoIs、http-token)だけで足りる
- 同じユーザーのプロセスはファイルを読めてしまうので、これだけでは塞ぎきれない。Claude Code のエージェントには pekoe が sandbox の設定(`--settings <データディレクトリ>/agent-settings/<slug>.json`。組み立ては `internal/daemon/agentsandbox.go`)を渡してトークン・`state.db`(と `-wal` / `-shm`)・tsnet の state・設定ディレクトリを読めなくする(Web UI から送られた自分宛ての画像 `<データディレクトリ>/images/<slug>/` だけは、`permissions.allow` で Read を確認なしにする。docs/web.md の「画像の扱い」)。中身・自分の設定との合わせ方・緩め方は README の「エージェントに pekoe を使わせるときの権限」

### 呼び出し元の識別

報告・送信の記録・`wait` の `mine`・受信箱は「誰が」呼んだかを使う。`$PEKOE_SESSION` や cwd はエージェントが偽れるので、pekoed はソケットの相手のプロセスから決める(`internal/daemon/caller.go`)。

1. 接続を受けたら相手の pid を取る(macOS は `getsockopt(SOL_LOCAL, LOCAL_PEERPID)`、Linux は `SO_PEERCRED`。`peer_*.go`)
2. その接続で呼び出し元が要る最初のリクエストのときに、親プロセスを辿る(64 段まで)。pekoe の窓の根のプロセス(tmux の `#{pane_pid}`。`Engine.Panes`)に当たれば、その窓の slug(tapi chat は `+tapi`)が呼び出し元
3. token つきのリクエストは、セッションのエージェントの窓(`<slug>~agent`。tapi chat の `+tapi~agent` を含む)に当たればその slug、それ以外(窓に当たらない、同じセッションでも run 窓・review 窓・dev 窓)は `human`。token なしで窓に当たらなければ `unknown`

sandbox なし(`[agents.<name>] sandbox = false`)のエージェントや tapi は `human-token` を読めるので、`pekoe` CLI は token を付けて送る。それでもエージェントの窓から来たものはそのエージェントのしたこととして記録する(`session.new` の `parent`、送信の記録の `from`、`wait` の `mine` と受信箱の答えの宛先、`send.log` の絞り込みの相手)。人のセッションと tapi が任せたセッションを取り違えないため。変わるのは記録と所有の判定だけで、token で human-only のメソッドを呼べること(認可)は変わらない(token を読める以上そこは守れない)。人が TUI の `!` で打つ run 窓と review の nvim は人の操作なので `human` のまま。TUI や素の端末は pekoe の窓の外なので `human`。dev 窓は hooks の sandbox で動き token を読めない

Claude Code のエージェントは pekoe が渡す sandbox の中で動き、tmux のソケットにはつなげない(`network.allowUnixSockets` は pekoed のソケットだけ)ので、別のセッションの窓の中にプロセスを作れない。二重 fork などで親を launchd に付け替えても `unknown` になるだけで、別のセッションには化けられない。

- クライアントは `$PEKOE_SESSION` をリクエストの最上位の `session` に入れて送る(`internal/client`)。pekoed はこれを名乗り(`claimed`)として、識別したものと違うときだけ記録に残す。振り分けには使わない
- `session.event`(hooks)は、窓に当たればその slug に振り分ける(`$PEKOE_SESSION` と cwd より優先)。Claude Code は hooks を sandbox の外で自分の子として起動するので、親を辿ると Bash のコマンドと同じ窓に当たる。当たらないもの(`pekoe adopt` で引き取った、tmux の外で動いているエージェントなど)は今までどおり `$PEKOE_SESSION` と cwd で決める。ただし `payload.transcript_path` が Claude Code の会話のファイル(`~/.claude/projects/<符号化した起動時の cwd>/<id>.jsonl`)なら、会話が始まった場所を先に見る: tapi の稼働ディレクトリなら tapi、あるセッションの worktree ならそのセッション、どちらでもなければ `$PEKOE_SESSION` を捨てる(cwd では決める)。Claude Code が会話をバックグラウンドのセッションに移すと、そのプロセスは別の窓の環境変数を持って窓の外で動くため(docs/tapi-chat.md「窓の外に出た会話」)。hooks は token を読めるので、ここでは token の有無ではなく窓を見る。tapi のリセット・起動し直しで pekoe が閉じた Claude Code の会話の hook(SIGHUP を受けて窓が消えた後に走る SessionEnd)は、閉じてから 1 分は窓に当たらなければ `{ignored: true}` で無視する(docs/tapi-chat.md「窓の外に出た会話」)
- `dev.toggle` も token なしなら窓で決める(token つきは今までどおり `slug` の相手)
- `dev.log` も同じ。token なしでは窓のセッションのログだけを返し(`slug` を省けば窓のセッション)、別の slug は `dev.log: a session reads only its own dev server's log (…)` で断る。ログは worktree のコマンドの出力で、起動時に `.env` の中身を出すような dev server だと秘密を含みうるので、ほかのセッションの分は読ませない
- `report.post` も token の有無より窓を優先する(token を読めてしまう sandbox なしのエージェントでも、報告は自分の窓のセッションに付く)
- `inbox.*` は上の 3. と同じ判定(`Caller.ID()`)にする。エージェントの窓からは token つきでもそのセッション(sandbox なしのエージェントの問いも自分のセッションに付く)。同じセッションの run 窓(TUI の `!`)・review 窓から token つきで打った `ask-human`・`inbox --mine`・`inbox withdraw` は人の操作で `human`(問いはエージェントのものにならず、答えもエージェントの入力欄に打たない)

### hooks の sandbox

pekoed は sandbox の外で動くので、エージェントが書き換えられるコード(worktree の Makefile・package.json・テスト、エージェントのブランチを `--base` にした `pekoe new`)を pekoed が実行すると、そのコードは human-token を読めてしまう。`session.event` は token なしで受け付けるので、Stop を偽れば何度でも走らせられる。そこで、worktree から来るコマンドはすべて sandbox(`internal/sandbox`)で包む。

- 包むもの: Stop のたびの `[hooks] test`、`[review] gatekeeper`(`claude -p` も worktree の `CLAUDE.md` や `.claude/settings.json` を読む)、`session.new` の `setup`、`dev.toggle` の `dev`(tmux の窓のコマンドを `exec sandbox-exec … $SHELL -c '…'` にする)と、dev を止めるときの `dev_stop`、`worktree.rm` / land 後の `teardown`、`land` のテスト。シェル・環境変数・cwd・標準入出力はそのまま。子プロセス(make → go test、npm → node)にも効く。pekoed が動かす git もすべてこの sandbox で包む(下の「pekoed が動かす git」。git だけは sandbox が使えなくても動かす)
- 包まないもの: `run.start`(run 窓。人間が打ったコマンドで、自分の端末で打つのと同じ)、エージェント自身の起動(Claude Code の sandbox の役目)、`review.surface` の nvim。nvim のサーバーソケットは `nvim --listen <データディレクトリ>/nvim/<slug のハッシュ>.sock` で pekoe の管理下に置く(下の「つなげない」。既定の `$TMPDIR/nvim.$USER/…` だと hooks の中から `nvim --server … --remote-send` で外のコマンドを打てる)。パスが unix ソケットの上限(約 104 バイト)を超えるときだけ、これまでどおり nvim に任せる
- macOS: `/usr/bin/sandbox-exec`。プロファイルは `(allow default)` に deny を足す。パスは `-D` の引数で渡し、プロファイルに埋めない。Seatbelt は解決後のパスで照合するので、各パスを書いたままのものと symlink を解いたもの(`/tmp` → `/private/tmp`)の両方で塞ぐ
  - 読めない: `human-token`、`http-token`、tsnet の state(`[web] state_dir` と既定の場所)、ローカル GUI の一度きりのトークンの HTML(`<データディレクトリ>/gui/`)、`config.toml`(`[web] auth_key`)、`state.db` と `-wal`・`-shm`(他のセッションのプロンプト・レビューのコメント・報告・受信箱。エージェントの sandbox と同じ扱い)、主なブラウザのプロファイル(`sandbox.BrowserProfiles`。ローカル GUI のセッションのトークンが `sessionStorage` から書かれうる。エージェントの sandbox と同じ扱い。docs/gui.md の脅威モデル。ほかの読めないものと同じく書くこともできず、`NativeMessagingHosts` のマニフェストも置けない)
  - 書けない: データディレクトリと設定ディレクトリ(sandbox を外す・hooks を書き換える・token を差し替えることを防ぐ)、あとで sandbox の外で実行されるもの(シェルの起動ファイル、`~/.gitconfig`・`~/.config/git`、`~/.ssh`、tmux・vim / nvim の設定、`~/Library/LaunchAgents`、`$PATH` のうち書き込めるディレクトリ。pekoed は git・gh・tmux を `$PATH` から起動する)、次に起動する Claude Code が読んで実行するもの(`~/.claude.json`(user スコープの MCP サーバー)、`~/.claude/` の `settings.json`・`settings.local.json`・`skills`・`hooks`・`plugins`・`agents`・`commands`・`rules`・`output-styles`・`CLAUDE.md`。`$CLAUDE_CONFIG_DIR` があればその下の同じもの)。`~/.claude/projects` やキャッシュは書ける(門番の `claude -p` が書く。`~/.claude.json` や `plugins` に書けなくても `claude -p` は動く)。`settings.json` の `statusLine` が `~/.claude` 直下の自作スクリプトを指すときは、そのスクリプトは塞がない(場所が決まっていない)。上のどれかの親ディレクトリも名前の変更・削除はできない(`~/.local/share` をずらせば規則の外に出てしまう)
  - 書ける(塞がない): リポジトリ本体の `.git/hooks`・`.git/config`(husky・pre-commit・lefthook が `npm install` や setup から書く)。代わりに pekoed が見張る(下の「本体の .git の見張り」)
  - つなげない: tmux のソケットのディレクトリ(`$TMUX_TMPDIR/tmux-<uid>`。つなげれば sandbox の外で動く窓を開ける)と、レビュー面の nvim のソケットのディレクトリ(`<データディレクトリ>/nvim`)。pekoed のソケットにはつなげる(token なしの扱い)。ほかの unix ソケット(Docker・ssh-agent・自分で開いた nvim など)は塞がない(テストやビルドが使う)
  - `lsopen`・`appleevent-send` も拒否(`open` や Terminal の `do script` で外に出られる)。`launchctl submit` は sandbox の中からは launchd が受け付けない
- Linux: bubblewrap(`/usr/bin/bwrap` などの決まった場所だけを探す。`$PATH` は使わない: `~/.local/bin` に偽の bwrap を置かれると、次から素のまま動いてしまう)。`--dev-bind / /` で今の / をそのまま見せ、書けないものを `--ro-bind` で重ね、読めないファイルに `/dev/null`、ディレクトリと tmux のソケットのディレクトリに空の tmpfs を重ねる。`--unshare-pid` と新しい `/proc` で、`/proc/<pid>` から pekoed のメモリや環境を読めないようにし、`--die-with-parent` で pekoed と一緒に止まる。まだ無いパスにはそのまま重ねられない(bwrap が重ねる先をホスト側に作ってしまう)ので、次のどちらかで塞ぐ(macOS は名前で拒むので要らない)
  - 空のままで何も変えないものは、親ディレクトリがあれば pekoed が先に作ってから重ねる: `~/.claude`(と `$CLAUDE_CONFIG_DIR`)の `skills`・`hooks`・`plugins`・`agents`・`commands`・`rules`・`output-styles` は空のディレクトリ(0700)、`CLAUDE.md` は空のファイル、`settings.json`・`settings.local.json` は `{}`、`~/.config` の `fish`・`zsh`・`git`・`tmux`・`nvim`・`systemd/user`・`autostart` は空のディレクトリ。ホストにこれらが増える。あるものは書き換えない
  - それ以外(シェルの起動ファイル・`~/.claude.json` など。空の `~/.bash_profile` は `~/.profile` を隠してしまうので作らない)と、親が無くて作れなかったものは、あるうちで最も近い親ディレクトリを読み取り専用にし、その中に今あるもの(ディレクトリと普通のファイル。symlink は除く)だけを書けるように重ね直す。その親の直下には新しいものを作れず、直下のものの名前の変更・削除もできない。たいていの Linux では起動ファイルのどれかが無いので、ホームの直下がこうなる(ホームの直下に初めてファイルやディレクトリを作るビルドや、ホーム直下の履歴ファイルを rename で書き換えるツールは失敗する。あるディレクトリの中は今までどおり書ける)。sandbox を始めた後にホスト側で増えたものも、中からは読み取り専用に見える。`/` とすでに読み取り専用の場所の中は除く
  - ブラウザのプロファイル(`Policy.DenyReadIfPresent`)は上のどちらもしない: コマンドを始める時点にあるものだけに空の tmpfs を重ね、無いもの(入れていないブラウザ)は飛ばす。中に読むものが無く、親を読み取り専用にするとホームや `~/.config` に何も作れなくなるため
  - pekoed は起動時にデータ・設定ディレクトリと token を作るので、要のものは必ずある
- 使えるかどうかは、初めて使うときに `sh -c :` を包んで動かして確かめる。通れば覚え、だめならその都度確かめ直す(bwrap を入れたらそのまま使える)
- 使えないとき(sandbox-exec が無い・動かない、bwrap が無い・ユーザー名前空間が使えない、sandbox の中で pekoed を動かしている、ほかの OS)は**動かさない**。Stop の test は `test.status` を `blocked`(`summary` に理由)にし、門番はリスクに理由を 1 件入れる(`path` は空)。setup・dev・teardown・land は理由を添えたエラーにする。人間が始めるものも止めるのは、`pekoe new` はエージェント(ハブ)も呼べて、`--base` にエージェントのブランチを渡せるから。dev は動き続ける。どうしても要るなら `config.toml` の `[sandbox] enable = false`(pekoed の再起動で効く。設定ディレクトリは sandbox から書けない)
- 残る穴: 塞ぐのは秘密と、知られた「あとで外で実行される場所」だけで、ホームのほかの場所(キャッシュ、`~/go/pkg/mod`、Homebrew の Cellar など)には書ける。そこに置いたものを人間や pekoed があとで sandbox の外で実行すれば抜けられる。Linux では、塞ぐパスの親ディレクトリの名前の変更は塞げない(上の読み取り専用にした親の直下を除く)。ほかにも:
  - hooks の中から、リポジトリ本体の `.git/hooks`・`.git/config`(サブモジュールの `.git/modules/*` も)に書ける。書いたフックや `core.fsmonitor` は、人が本体のチェックアウトで打つ git やエディタの git プラグインが sandbox の外で動かす。塞ぐと husky などが動かなくなるので、見張って受信箱に出すだけにしている(下)。直すか消すかは人が決める
  - hooks の中から、Docker(`~/.docker/run/docker.sock`)・ssh-agent・自分で開いた nvim など、sandbox の外のプロセスの unix ソケットにつながる。Docker なら `-v $HOME:/h` で token も読める。テストやビルドが使うので塞がない。気になるなら、そういうソケットを `[hooks]` の間は開いておかない
  - 他のセッションの worktree や、本体のチェックアウトの `.claude/settings*.json`・`.mcp.json` にも書ける(書き込みは deny リストで、allow-list ではない)

### 本体の .git の見張り

pekoed が hooks の sandbox で動かすもの(Stop の `[hooks] test`、land のテスト、`setup`、`teardown`、`[review] gatekeeper`、`dev`、`dev_stop`)の前後で、そのセッションのリポジトリ本体の git ディレクトリ(`git rev-parse --git-common-dir`)の、あとで git が実行するものの指紋を取って比べる(`internal/gitwatch`)。

- 見るもの: `hooks/` の中身(名前・内容の sha256・実行ビット・symlink の先。`*.sample` は git が動かさないので除く)、`core.hooksPath` が指すディレクトリの中身(husky の `.husky/_` など。相対パスは本体のチェックアウトから。本体のチェックアウトも hooks の中から書ける)、`config`・`config.worktree`・`worktrees/*/config.worktree` の危ない設定、サブモジュールの git ディレクトリ(`modules/…`。入れ子も)の `hooks/` と `config`
- 危ない設定: コマンドを名指しするか、git が読むもの・フックの場所を変えるもの。`core.hooksPath`・`core.fsmonitor`・`core.sshCommand`・`core.editor`・`core.pager`・`core.askPass`・`core.gitProxy`・`core.alternateRefsCommand`・`core.worktree`・`core.attributesFile`・`sequence.editor`・`diff.external`・`gpg.program`・`credential.helper`・`interactive.diffFilter`・`web.browser`・`ssh.variant`・`uploadpack.packObjectsHook`・`extensions.worktreeConfig`・`include.path`・`protocol.allow`・`receive.procReceiveRefs`・`sendemail.*cmd` など、と `alias.*`・`pager.*`・`includeIf.*.path`・`filter.*.{clean,smudge,process}`・`diff.*.{textconv,command}`・`merge.*.driver`・`gpg.*.program`・`credential.*.helper`・`{merge,diff}tool.*.{cmd,path}`・`browser.*.{cmd,path}`・`man.*.{cmd,path}`・`remote.*.{uploadpack,receivepack,url,pushurl,vcs}`・`submodule.*.update`・`protocol.*.allow`・`url.*.{insteadOf,pushInsteadOf}`・`trailer.*.{command,cmd}`。読むのは `git config --file <f> --no-includes -z --list`(pekoed の git と同じ守りで動かす)で、git が読めない config はファイルのハッシュで見る。`user.name` や `branch.*` のような変更では何も出さない
- 変わっていたら、受信箱に人あての項目を 1 件出す(`from: pekoed`、`kind: decision`、`about` はそのセッション)。問いにはリポジトリ・hook の名前・セッション、`context` に差分の要約(`+` 足された・`-` 消えた・`~` 変わった。40 行まで)と、同じリポジトリで同時に走っていたほかの hook。選択肢(「想定どおり(husky など): そのまま」「想定外: 見て戻す」)は記録のためで、pekoed は答えで何もしない。直しも削除もしない
- 同じリポジトリで重なって走った hook は同じ変化を見るので、先に終わった方だけが出す(出した状態と同じで、その hook が始まった後に出されていれば黙る)。人が戻したあとでまた同じ状態にされたら、また出す
- `dev` は動き続けるので、起動のときに指紋を取り、10 秒ごと(tickLoop)と止めたときに比べる。窓が消えていれば最後に 1 回比べて終える(起動の直後 15 秒は、窓がまだ無くても終えない)。pekoed を再起動したあとも動いている dev はその時点から見張る
- 何かを塞ぐものではない。気づくためのもの。比べるのは hook の前と後(dev は 10 秒ごと)だけで、その間に誰が変えたかは区別しない。hook が走っている間に人が本体で `npm install` を打ったり、エージェントが自分の worktree の `worktrees/<id>/config.worktree` を書いたり(エージェントの sandbox はここを塞がない。読まれるのは `extensions.worktreeConfig` が有効なときだけ)すれば、それもその hook の項目として出る。hook が走っていない間の変更は出さない(エージェントの sandbox は本体の `.git/config`・`.git/hooks` とサブモジュールのそれを書かせない。下の「pekoed が動かす git」)

### pekoed が動かす git

git は設定に書かれたコマンドを動かす(`git status` のたびの `core.fsmonitor`、フック、credential helper、`.gitattributes` が選ぶ filter・merge・diff のドライバ、`gpg.program`、`core.sshCommand` など)。pekoed は diffstat・スナップショット・changes・review・undo・衝突予報・land・rm・PR のレビューで worktree の git を頻繁に動かし、sandbox の外で動く。一方、worktree の `.git` は `gitdir: <パス>` と書かれたファイルでエージェントが書き換えられ、偽の git ディレクトリに向ければその設定はすべてエージェントのもの。commit できるエージェントは本体の `.git`(objects・refs・`worktrees/<id>`)にも書けることが多く、`.git/config` や `config.worktree` も書けると見なす。token なしの `pekoe new --repo <パス>` には、エージェントが自分で作ったリポジトリも渡せる。そこで、pekoed の git(`internal/gitx` の `RunRaw` を必ず通る。`exec` で git を直接呼ぶのはここだけ)には三つの層をかける(`internal/gitx/guard.go`)。

1. **git ディレクトリの固定**: セッションの worktree(`session.new` / `session.open` / `review.pr` で登録、再起動時にも)では `.git` ファイルを信じない。本体の `.git/worktrees/*` のうち `gitdir` がその worktree を指し返すものを使い、`GIT_DIR`・`GIT_WORK_TREE`・`GIT_COMMON_DIR` を明示する。`.git` ファイルがそこを指していなければ(別の場所・symlink・ディレクトリ)、その worktree では git を動かさずにエラーを返し、セッションに `git_blocked`(理由)を立てる。TUI と Web UI は `.git changed: git stopped` と出す。diffstat のたび(編集・Stop)と再起動時に確かめ、元に戻れば消える
2. **設定の上書き**: コマンドを名指しする設定のうち、名前が決まっているものは `GIT_CONFIG_COUNT`(どの設定ファイルより強い)と環境変数で上書きする
   - 無効にする: `core.fsmonitor=false`、`core.hooksPath=/dev/null`(pekoed の git ではフックが一切走らない。land の push でも pre-push は走らない)、`core.alternateRefsCommand`、`protocol.ext.allow=never`(`ext::` の URL)
   - 環境変数で決める(設定より強い): `GIT_EDITOR=:`・`GIT_SEQUENCE_EDITOR=:`・`GIT_PAGER=cat`、`GIT_ASKPASS` と `GIT_PROXY_COMMAND` は pekoed の環境に無ければ空(`core.askPass`・`core.gitProxy` を読ませない)、`GIT_SSH_COMMAND` は pekoed の環境、利用者の `core.sshCommand`、`GIT_SSH`、`ssh` の順
   - 利用者自身の設定(system と global のスコープ。そこから include したものも含む。`git config --show-scope` で見分ける)か git の既定値: `gpg.program`・`gpg.<format>.program`・`gpg.ssh.defaultKeyCommand`・`gpg.format`・`commit.gpgSign`・`tag.gpgSign`・`push.gpgSign`・`merge.verifySignatures`・`log.showSignature`。credential helper は空の `credential.helper` でリポジトリのぶんまで空にしてから、利用者の helper(URL ごとのものも、順番どおり)を足し直す。利用者の設定はディレクトリごとに 10 秒覚える
   - 引数で決める: `diff`・`show`・`log` に `--no-ext-diff --no-textconv`、`fetch`・`pull`・`ls-remote` に `--upload-pack=git-upload-pack`、`push` に `--receive-pack=git-receive-pack`(`remote.<name>.uploadpack` は最初の値が勝つので設定では上書きできない。origin がローカルのパスならこの機械で動く)
3. **sandbox**: 名前で上書きできないもの(`.gitattributes` が選ぶ任意の名前の filter・merge ドライバ、git-lfs が読む `lfs.*`、origin がローカルのパスのときの相手側のフック(git は相手側に呼び出し側の設定を渡さない)、今後の git が足すコマンド)のために、git 自体を「hooks の sandbox」と同じ sandbox で動かす。ドライバは動くが、human-token などは読めず pekoe のファイルも書けない。git の起動が 1 回 10ms ほど遅くなる。git 設定の読み出し(`git config --show-scope`)は何も実行しないので包まない

- sandbox が使えないとき(sandbox-exec や bwrap が無い・動かない、`[sandbox] enable = false`)は、hooks と違って git は**そのまま動かす**(git が止まると pekoe が使えない)。1 と 2 は効くが、3 で塞いでいるもの(filter・merge ドライバ、`lfs.*`、ローカルの origin の相手側のフック)は残る。エージェントがリポジトリの `.git/config` に書けるか、自分で作ったリポジトリを `pekoe new --repo` に渡せば、それらが sandbox の外で走る。pekoed のログに `git: running without a sandbox (…)` と出る
- 対象外: 人間が自分で動かす git(シェル、run 窓、レビュー面の nvim の git プラグイン、本体のチェックアウトでの git)。エージェントが書いた `.git` ファイルや `.git/config` はそのまま読まれる。`git_blocked` が出たら、その worktree で git を動かす前に `.git` を確かめる。本体の `.git/config`・`.git/hooks`(とサブモジュールの `.git/modules/*/{config,hooks}`)は、Claude Code のエージェントには pekoe が `sandbox.filesystem.denyWrite` で書かせない(ほかのエージェントは自分で塞ぐ)。ただし pekoed の hooks の sandbox からは書ける(husky などのため)。そこで変わったものは受信箱に出る(上の「本体の .git の見張り」)ので、見てから本体で git を使う
- gh: pekoed が動かす gh(land の `gh pr create`、PR・CI・issue の読み出し、`gh pr merge`、GitHub へのレビューの送信)には git を一切動かさせない。gh は起動した場所で git を動かす(`gh pr create` の `git status`、remote からのリポジトリの割り出し)ので、上の三つの層をかけるのではなく、git が動く場所をなくす(`internal/daemon/pr.go` の `ghCommand`。`exec` で gh を直接呼ぶのは、下の 2 つを除いてここだけ)
  - cwd はどのリポジトリでもない空のディレクトリ(データディレクトリの `gh/` の下に呼び出しごとに作り、終われば消す)。環境は gh に要るもの(`PATH`・`HOME`・`GH_*`・`GITHUB_TOKEN`・`XDG_*`・`DBUS_SESSION_BUS_ADDRESS`(Linux で token を Secret Service から読む)・ロケール・プロキシ・証明書)だけで、`GIT_DIR`・`GIT_WORK_TREE` などは渡さず、`GIT_CEILING_DIRECTORIES` で cwd より上のリポジトリも探させない
  - リポジトリは毎回 `--repo <host/owner/name>`(`gh api` は `--hostname` と、owner と name を入れたパスやフィールド)で渡す。host・owner・name は英数字と `.` `_` `-` だけで(host は `_` も不可)、`.`・`..`・`-` で始まるものでないものに限る。決まらなければ gh を起動せずにエラーにする
  - どのリポジトリかは、前に gh が自分で git を読んで決めていたのと同じ規則で、pekoed が main のチェックアウトを上の git(`git remote -v` と `git config --get-regexp '^remote\..*\.gh-resolved$'`)で読んで決める(`internal/daemon/ghrepo.go`。gh v2.103 の `context.ResolvedRemotes.BaseRepo` をプロンプトなしで動かしたときに合わせてある)。読んだ値はエージェントが書けるデータなので、どれも上の形を通らなければ使わない。結果はリポジトリごとに覚え、remote や gh-resolved を変えたら pekoed の再起動で読み直す(人が承認したときと違うリポジトリに gh が向かないように)
    - ホストの別名: remote の URL が SSH(`git@host:owner/name` と `ssh://`・`git+ssh://`)なら、`ssh -G -- <host>` の `hostname` を本当のホストにする(`~/.ssh/config` の `Host github-work` など)。`ssh.github.com`(443 番ポート経由)は `github.com` にする。ssh に渡すのは上の形を通ったホスト名だけで、`--` の後に置く。cwd は `/`、環境は `HOME`・`USER`・`LOGNAME`・`PATH` だけ、5 秒で打ち切る。ssh が無い・失敗した・答えが上の形を通らないときは URL に書かれたホストのまま。https などの URL では ssh を起動しない
    - base のリポジトリ(PR を出す先): origin が上の形のリポジトリを指していることが前提(指していなければ gh は起動しない)。候補は origin と同じホストを指す remote(gh は自分がログインしているホストに絞る)で、`upstream` > `github` > `origin` > その他(同じ順位は git の順)に並べる。最初に `remote.<name>.gh-resolved` を持つ remote が決める: `base`(`gh repo set-default` が書く値)ならその remote、ほかの値なら `owner/name`(`host/owner/name` や URL でもよく、host はその remote のもの)が指すリポジトリ、形が合わなければリポジトリなし(gh も失敗する)。gh-resolved がどこにも無ければ先頭の remote
    - fork(base が origin と違う): push 先は今までどおり origin。`gh pr create` は `--repo <base> --head <origin の owner>:<branch>`、PR 番号の割り出し(`gh pr view`)も `<owner>:<branch>` で、PR の状態・マージ・CI のログ・issue・レビューの送信はすべて base に向ける。他人の PR のレビュー(`pekoe review gh#N`)は `pull/N/head` を base を指す remote(gh の順で最初のもの。名前は英数字と `.` `_` `-` だけで `-` で始まらないもの)から fetch する。base を指す remote が無ければ(gh-resolved が remote の無いリポジトリを指すとき)、gh も git も動かさずにエラーにする。受信箱の facts の `pr_url` は base の PR を指し、`pr_repo`(base)と `push_repo`(origin)が付く(fork のときだけ)。マージの承認の本文にも `fork: the PR is in … ; the branch was pushed to … (origin)` が出る
    - remote が origin だけ、または gh-resolved が origin を指すときは、base = origin で、引数は今までと同じ
  - git から読む情報は pekoed が上の git で取り出して渡す。land の PR の title・本文(`--fill` が読んでいたもの)は base からの `git log` から作り、title は引数、本文は標準入力で渡す(commit が 1 つならその件名と本文、2 つ以上・0 なら title はブランチ名(`-` `_` を空白に)、本文は件名の箇条書き)
  - `gh auth token`(`pekoe update`)と `gh attestation verify -R … --signer-workflow … --source-ref …` は git を動かさないので、この仕組みを通さずそのまま起動する

### send と許可ダイアログ

許可メニューは最初の 1 文字を選択肢として受け取り、Enter は選ばれている「Yes」を確定する。そこで `session.send` / `tapi.send` は、許可ダイアログが出ている間は文字を打たない。「出ている」は、hook が知らせた許可待ち(`wait_kind` が `question` 以外の `waiting`)か、agent 窓の画面の下に Claude Code の「Do you want to …」と「1. Yes」の選択肢(または最後の行の `[y/n]`)があること。

| いつ | だれ | 結果 |
| --- | --- | --- |
| 打つ前 | token なし(agent 窓への送信すべて) | `<slug> is waiting for a permission answer: answer it in the pekoe TUI first, then send (nothing was sent)`。受け付けた時と、セッションごとの送信ロックを取って打つ直前の 2 回確かめる |
| 打つ前 | token なしで、先頭(空白を除く)が `!` の送信 | `session.send without the human token: text may not start with '!' …`。許可ダイアログとは別に、空の入力欄の `!` はシェルモードになるため(上の「人間だけの操作」) |
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
  "model": "claude-sonnet-5-5",    // pekoe new --model で上書きしたモデル(無ければ省略。[agents.*] model が使われる)
  "effort": "low",                 // pekoe new --effort で上書きした effort(無ければ省略)
  "last_model": "claude-opus-5-5", // このセッションが最後に答えたモデル(会話記録の最後の assistant のメッセージの model。Claude Code だけ。PostToolUse と Stop の hook で読む)
  "run_effort": "high",            // hook の共通入力の effort.level(Claude Code だけ。値を持つ hook が来るたびに上書き。エージェントの自己申告で表示だけ)
  "permission_mode": "auto",       // hook の共通入力の permission_mode(default | plan | acceptEdits | auto | dontAsk | bypassPermissions。同上)
  "agent_version": "2.1.325",      // 会話記録の行の version(動いている Claude Code の版。PostToolUse と Stop の hook で読む。同上)
  "state": "running",              // queued | creating | running | waiting | idle | done | reviewing | approved | landed | failed(queued は下の「待ち行列」)
  "wait_kind": "permission",       // waiting のときだけ: permission | question(AskUserQuestion と elicitation。AskUserQuestion の直後に Claude Code が送る permission_prompt は、待ちの対象が AskUserQuestion のままなら question のまま。知らない notification_type は permission)
  "state_since": "2026-11-07T10:00:00Z",
  "port": 3001,
  "last_tool": "Edit src/api.ts",  // PreToolUse の直近
  "pending_tool": {"name": "Edit", "input": "src/api.ts", "touches": "src/api.ts", "also_in": ["feat-x"]}, // 許可待ちの対象。input は主な引数 1 つの 1 行の要約(改行のかたまりは " ⏎ "、200 バイトまで)。touches は Edit/Write が変えるファイル、also_in はそれを既に変更済みの他セッション。引数が複数行なら lines(行数)、要約が切れていれば cut: true と size(全体のバイト数)
  "test": {"status": "pass", "summary": "12 passed", "at": "..."},      // pass | fail | running | none | blocked(sandbox が使えず動かしていない。summary に理由)| error(コマンドを起動できなかった。シェルの終了コード 127 = command not found。summary は何が無かったか(`command not found: yarn`。zsh・bash・dash の書き方から取り出す。取り出せなければシェルの最後の行)。テストが落ちたのとは別)。repos/<id>.toml に test が無ければ、次の Stop(か land)で none に戻る。running のまま pekoed が止まった(test の実行中の再起動)ものは、起動したときに none に戻す(`summary` に `pekoed restarted while the test ran`。次の Stop でまた走る)
  "diffstat": {"added": 120, "deleted": 8, "files": 4},
  "prompts": 7,                    // スナップショット数
  "review_n": 2,                   // 最後のレビュー地点番号(0 = 未レビュー)
  "claude_session_id": "…",
  "unread": 1234,                  // 離れてからの未読バイト数(セッションを保存するたびに state.db に残り、pekoed を再起動するとその値から続ける。最後の保存より後に増えた分は数えない)
  "activity": [0,1,3,5,8,6,4,5,7,9], // 直近の活動量(スパークライン用、古い順)
  "tokens": 42000,
  "dev": false,                    // dev hook([hooks] dev)のプロセスが動いている間だけ true。自分で終わったら pekoed が 2 秒ほどで見つけて false にする
  "dev_log": "~/.local/share/pekoe/devlogs/feat-x.log", // dev hook の出力のファイル(エスケープシーケンスを除く。pekoe dev --log・dev.log)。一度起動してから
  "dev_exit": {"status": 1, "at": "...", "dev_stop": {"command": "make down", "status": 0, "at": "..."}}, // 最後の dev hook がどう終わったか(動いている間・起動前は無い)。status は終了コード(不明・シグナル・pekoe が止めたときは -1)、signal、stopped(pekoe が止めた)、closed(終わった後の窓も閉じた)、dev_stop は [hooks] dev_stop の結果(error は失敗の理由)
  "warnings": ["[worktree] copy \".envrc.local\": not found in /src/app, nothing copied"], // 作るときに気づいたが止めなかったこと([worktree] copy / clone のコピー元が本体のチェックアウトに無い・0 バイト)
  "run": "gh auth login",          // run 窓(TUI の !)のコマンド。閉じると消える
  "pr": 123,                       // セッションの PR 番号(gh#N のレビュー、land、自動検出)
  "issue": 12,                     // --from gh#N で作ったときの issue 番号
  "pr_status": {                   // PR の要約(60 秒ごと(`[pr] poll_every`)にポーリング。PR ビューを開いている間は 15 秒)
    "state": "OPEN",               // OPEN | MERGED | CLOSED
    "draft": false,
    "pass": 5, "fail": 1, "pending": 0, "skip": 0,   // checks の数(skip は neutral・skipped・cancelled・stale。0 なら省略)
    "failed_checks": ["test (ubuntu)"],   // 落ちている check の名前(10 件まで、1 件 60 文字まで)
    "review_decision": "CHANGES_REQUESTED", // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED
    "unresolved": 2,               // 未解決のレビュースレッド
    "merge_state": "BLOCKED",      // mergeStateStatus: CLEAN | BLOCKED | DIRTY | BEHIND | UNSTABLE …
    "head_sha": "3f2a…",           // GitHub 上の PR の HEAD(headRefOid)
    "ci_head": "3f2a…",            // CI の決着を知らせた HEAD(wait の `ci`。同じ HEAD・同じ結果では 1 回だけ出すための記録。下の「待ち合わせ」)
    "ci_result": "red 1/4 3c9a…",  // そのとき知らせた結果(green|red、pass/fail の数、どのチェックが通り・落ちたかのハッシュ)
    "last_comment": "…", "last_thread": "…",
    "attention": ["ci_failed"],    // 前回 PR ビューを開いてから起きたこと: ci_failed | changes_requested | new_comment | mergeable
    "attention_at": "…", "at": "…"
  },
  "land_held": "PR #42 merged while the session is still at work (agent is running): window and worktree kept; …", // PR はマージされたが、エージェントがまだ作業中(running・waiting・コミットしていない変更・マージされた PR に入っていないコミット)なので後片付けを待っている理由(下の「PR の状態のポーリング」)。landed になれば消える
  "land_held_at": "…",  // land_held になった(pekoed が最初にマージを見た)時刻。landed になったときの stats のマージの日時はこれ。landed になれば消える
  "error": "setup failed: pnpm install",
  "git_blocked": "…/.git points at …, not …",  // worktree の .git が書き換えられていて、pekoed がそこで git を動かさない理由(「pekoed が動かす git」)。戻れば消える
  "overlaps": [                    // 衝突予報(同じリポジトリの open なセッションのうち、同じファイルを触っている相手)
    {"slug": "feat-x", "files": ["src/api.ts", "README.md"], "conflict": true, "conflict_files": ["src/api.ts"]}
  ],
  "parent": "+tapi",               // session.new の呼び出し元(上の「呼び出し元の識別」): +tapi | <slug> | human | unknown。session.new 以外で作ったもの(open・adopt・review.pr)は空
  "parent_born": "",               // parent がセッションのとき、作った時点のそのセッションの作成時刻(RFC3339Nano)。同じ slug を後で作り直したセッションを親と取り違えないため(`wait` の `mine`)。セッションでない親(+tapi・human・unknown)では省略
  "report": {"id": 17, "status": "done", "summary": "CSV の空行を飛ばすように直した", "at": "…", "head": "3f2a…"}, // 最新の報告(summary は 1 行目、200 文字まで)
  "review": {"id": 15, "verdict": "pending", "reviewed_sha": "3f2a…", "at": "…"}, // 最新の status: review の報告(pending = レビュー中・pass・fail)。後に別の報告が来ても残る。無ければ省略
  "request": 4,                    // 結んだ依頼の id(pekoe new --request、ledger.link)。無ければ省略
  "origin": 4,                     // セッションを作った依頼の id(pekoe new --request)。後の ledger.link では変わらない。無ければ省略(手で作った・これを記録する前に作ったセッション。後者は request を作った依頼とみなし、最初の ledger.link のときに origin に移す)
  "later": [7],                    // 作った後に ledger.link で結んだ依頼の id(結んだ順。origin は入れない)。無ければ省略
  "after": ["feat-api"],           // このセッションが後に来るセッションの slug(session.new の after)。hold が無ければ記録と表示だけで、起動は止めない
  "hold": true,                    // session.new の hold(pekoe new --hold): after がすべて landed になるまで起動しない。無ければ省略
  "holding": ["feat-api"],         // queued の間、after のうちまだ landed でないもの。起動したら消える
  "queue": "--hold: waiting for feat-api to land" // queued の間だけ: 起動していない理由(1 行)。下の「待ち行列」
}
```

**待ち行列**(`queued`、internal/daemon/queue.go) — `session.new` は、次のどちらかのときは作成(worktree・setup・エージェントの起動)を始めずに `state: queued` のセッションを返す。行・slug・ポートはあるが、worktree もエージェントもまだ無い。

- `[orchestrator] max_running` が 1 以上で、作業中のセッション(pekoe が作ったもの(`managed`)で `creating`・`running`・`waiting`。止めたもの(`x`)とリポジトリ本体のチェックアウトは数えない。`creating` は止めた印に関係なく数える)がその数に達している。`queue` は `max_running = 3: waiting for a session to stop working`
- `hold`(`pekoe new --after X --hold`)で、`after` のうち landed でないものがある。`queue` は `--hold: waiting for X to land`。待っている相手が `failed` なら `(X failed)` を足して待ち続ける(failed は resume で戻りうる)。相手が landed にならずに rm されたら、このセッションを `failed`(`--hold: X was removed before it landed …`)にする。landed になってから rm された相手は揃ったものとして扱う。pekoe が作っていない相手(`managed` でない。`pekoe open` したリポジトリ本体のチェックアウトなど)は land しない(`land` が断る)ので、`hold` があってもなくても `after` には取らず、`session.new` が `--after X: X is not a session pekoe made (pekoe open), so it never lands; …` で断る(`hold` なしで取ると、`merge.check` の `after` がいつまでも揃わない)

`max_running` を超えて上限を待つものは、作業中のものが止まる(idle・done・failed・landed・rm)たびに、古いもの(`created_at` の順)から起動する。hold で待つものは、揃ってからさらに上限を待つ(揃っていないものは後ろのものを止めない)。起動すると `creating` に移り、そこからは普通の作成と同じ(`session.progress`、`wait` の `started`)。hold で待ったものは、起動するときに origin から base を fetch して `origin/<base>` から(upstream は付けずに)ブランチを作る。待っていた相手のマージが入った base から始めるため。base が `origin/main` のようにすでに origin のブランチなら、そのブランチ(`main`)を fetch して同じ `origin/main` から。fetch が落ちたら少し置いて一度だけやり直す(同じリポジトリで別の git が worktree を作っている最中に fetch が読むと落ちるため)。fetch はやり直しも合わせて 60 秒で打ち切る。fetch の間に `session.cancel`・rm されたら fetch を止め、worktree もブランチも作らない。origin の無いリポジトリ、fetch がやり直しても落ちた・時間切れのとき、base が origin のブランチでない(タグ・コミット。動かないので fetch しても変わらない)ときは、ふだんどおり base そのものから。fetch がやり直しても落ちた・時間切れのときは、待っていた相手のマージが手元の base に入っていないかもしれないので、`warnings` に `could not fetch …: started from the local <base>, which may be behind origin` を出す(hold で待たない普通の作成と同じ文面)。作るブランチが既にあるときは、hold でもそのブランチをそのまま使い、fetch しない。上限も hold も無ければ、今までどおりすぐに作成を始める(`queued` は出ない)。

hold で待たない普通の作成でも、`base` を渡されていない(リポジトリの `[worktree] base`、無ければ origin の既定のブランチを使う)ときは、worktree を作る前に同じように origin から base を fetch する(やり直し・60 秒の打ち切り・cancel で止まるのも同じ)。ただし hold と違い、`origin/<base>` から作るのは手元の base がそれより古い(`origin/<base>` の祖先)とき、または手元にその名前のブランチが無いときだけ。手元の base に origin に無いコミットがある(push していない作業・手元にだけあるブランチ)ときは手元の base のまま作り、手元と origin の両方が進んでいる(分岐した)ときも手元の base から作って、`warnings` に「pull して」と出す。pull していないリポジトリ本体のチェックアウトから、古いコードで作業を始めないため。origin の無いリポジトリ、base が origin のブランチでないとき(タグ・コミット)は今までどおり手元の base から。fetch がやり直しても落ちた・時間切れのときも手元の base から作り、`warnings` に `could not fetch …` を出す。作るブランチが既にあるときは、そのブランチをそのまま使うので fetch しない。`base` を渡したとき(`--base main`・タグ・コミット・`origin/main`)は今までどおりそのものから作り、fetch しない。どこから作ったかは `session.progress` の `worktree` の手順の `detail` に `<path> · from origin/main (fetched, local main 3 behind)`・`from main (= origin/main, fetched)`・`from main (2 commits not on origin/main)`・`from main (diverged from origin/main)`・`from main (fetch failed)`・`from main (no origin)` のように出る(hold で fetch して作ったものは `from origin/main (fetched)`、hold で fetch が落ちたものは `from <base> (fetch failed)`、`base` を渡したもの・hold で origin が無い・base が origin のブランチでないものは `from <base>`、既にあるブランチは `existing branch <branch>`)。upstream は付けない(hold と同じ)。land はどちらでも `Session.base`(`main`)を fetch して `origin/main` へ rebase する。

queued のセッションでは、エージェントの起動(`session.restart` など)・`run.start`・`dev.toggle`・`session.kill` を断る。`creating` のセッションの `session.kill`(TUI の `x`)と `session.restart`(TUI の `R`)も断る(作成は止まらずにエージェントを起動するので、止めた印だけ付けると上限の数から外れてしまう。今エージェントを起動すると setup の途中で動き始め、上限の数からも外れ、作成がもう一度起動する。作成を止めるのは `session.cancel`・rm)。`session.cancel` と `worktree.rm`(token あり)で消せる。起動に要るもの(プロンプト・`from`・窓の大きさ)は state.db の kv(`queued:<slug>`)に置くので、pekoed を再起動しても queued のまま残り、起動後に順に始まる(kv が無ければ `failed`: `interrupted by daemon restart (queued)`)。

**衝突予報** — 「触っているファイル」は base(merge-base)からの変更(コミット済み・未コミット・未追跡)と `pending_tool.touches` の和。同じ `repo_id` の、landed でも停止済み(`x`)でもないセッション同士で重なりを取り、pekoed が約 10 秒ごと(と Edit/Write の直後)に計算し直す。変化があれば `session.updated` が出る。重なったペアのうちコミット済みの内容も重なっているものは `git merge-tree --write-tree` で worktree を汚さずに試しマージし、衝突すれば `conflict: true`。未コミット分は重なり検知だけ。

### メソッド

`{slug}` を取るメソッドはすべて `<repo>/<slug>` も受け付ける(`<repo>` は repo-id か短い名前。`<slug>` は本物の slug か、別リポジトリとの衝突でリポジトリ名が前に付く前の形)。デーモンが受け取った時点で slug に直す。当たらなければ `no such session: <repo>/<slug>`、複数のリポジトリに当たれば候補を並べたエラー。

| method | params | result |
| --- | --- | --- |
| `daemon.status` | — | `{pid, version, sessions, http_port, socket, http_error?, config_restart?}`。`socket` は pekoed が待ち受けている unix ソケットのパス。`http_port` は拡張用の HTTP を実際に待ち受けている番号(設定の `[http] port` ではない)。待ち受けていなければ `0` で、`http_error` に理由(`listen tcp 127.0.0.1:47777: bind: address already in use` など)。`config_restart` は config.toml で変わったが再起動まで効かない設定の名前(下の「config.toml の再読み込み」) |
| `daemon.stop` | `{kill_agents?}` | `null`。既定ではエージェント(tmux サーバー)を残す。`kill_agents: true` なら、止まる直前に tmux サーバーも止める(使い捨ての隔離環境向け。CLI は `pekoe daemon stop --kill-agents`)。応答はすぐ返り、プロセスはその後に終わる(処理中の hook(`session.event` と Stop の後処理)を最大 3 秒待ってから state.db を閉じる)。終了を待つには `daemon.status` の `pid` を見る(`pekoe daemon stop` と `pekoe daemon restart` はそうしている) |
| `config.show` | — | `{path, exists, sections: [{name, doc, keys: [{key, value, doc, restart?, set?}]}], restart: [name], waiting: [key], problems: [{key?, message, fatal?}]}`。pekoed が今使っている設定(`pekoe config show` と同じく既定を埋め、`[web] auth_key` は `(hidden)`)を、Global の並びの節ごと(`[agents.<name>]` は名前順、裸のキー `animate` はそれだけの節)に、各キーの説明(`config.KeyDocs`)・再起動まで効かないキーか(`restart`、`config.NeedsRestart`)・config.toml にあるか(`set`)を付けて返す。再起動待ちのキーの値は動いている値。`restart` は `daemon.status` の `config_restart` と同じ(`[web]` は節ごとに `web`)、`waiting` はそれをキーごとにしたもの(`config.RestartKeys`。`http.port`・`web.allow_devices`・`sandbox.enable` のように、変わったキーだけ)、`problems` は pekoed が最後に読んだファイルへの `config check`(`fatal` ならファイルが読めず、前の設定のまま)。書くメソッドは無い。人間だけ(ローカル GUI の Settings、`GET /api/config`) |
| `subscribe` | `{events: [prefix]}` | `null` |
| `repo.list` | `{cwd?}` | `[KnownRepo]`(下記)。既知のリポジトリ = セッションがあるもの + 一度解決したもの(`session.new` / `session.open` / `init.*` / `repo.list` の `cwd`。state.db の kv に `repo:<repo-id>` で覚える) + `~/.config/pekoe/repos/*.toml` があるもの。設定ファイルしかないものは、`path_` の repo-id ならそのパス、それ以外は `~/worktrees/<name>/` の worktree から場所を探す。`cwd` を渡すと、そのリポジトリ(worktree の中ならメイン)を覚えたうえで `cwd: true` を付ける。名前順 |
| `repo.resolve` | `{repo, cwd?}` | `KnownRepo`。`repo` は repo-id・短い名前・パス(`/` を含むか `.` `~` で始まるもの。相対パスは `cwd` 基準。worktree の中ならメインに解決し、既知でなくてもよい)。短い名前が複数に当たるときは候補の repo-id を並べたエラー、知らない名前は既知の名前を並べたエラー。`cwd` 直下に同名のディレクトリがあればそのパスとして扱う |
| `repo.forget` | `{repo, cwd?, config?, dry_run?}` | `{repo_id, name, dir?, landed?: [slug], remembered?, config_file?, config_deleted?, dry_run?}`。人間だけ(`pekoe repo forget`)。`repo` は `repo.resolve` と同じ(repo-id・短い名前・パス)。pekoe がそのリポジトリを知らなくなるようにする: landed のセッションの記録(`pekoe rm` と同じく、stats に残す分は先に書く)と state.db の `repo:<repo-id>`(同じディレクトリを指す古い id の分も)を消す。landed でないセッション、または worktree がまだ残っている landed のセッションがあれば、slug と状態を並べて断り何もしない(先に `pekoe rm`)。`~/.config/pekoe/repos/<repo-id>.toml` は人が書いたファイルなので、`config: true` のときだけ消し、無いときにファイルがあれば断る(残したままだと設定ファイルからまた知ってしまうため)。知らないリポジトリは `nothing to forget`。`dry_run: true` は何をするかを返すだけ(CLI は確認の前にこれを出す)。リポジトリのディレクトリ・worktree・ブランチ、stats の記録(`session_records`・日ごとのトークン)には触らない。この後は `repo.list`・TUI の `w`・tapi chat が読めるリポジトリの一覧(次に tapi chat を起動したとき)に出ず、トークンなしの `session.new` もそのリポジトリでは断られる。`session.new` / `session.open` / `init.*`、またはその中で TUI を起動して `repo.list {cwd}` を呼べば、また知っている状態に戻る(TUI が `cwd` を渡すのは起動して最初の一覧だけなので、そこで開いたままの TUI は、forget が出す `session.removed` で一覧を読み直しても覚え直さない)。`session.new` / `session.open` とは同じ排他で並ぶので、同時に来たときは、セッションができてから断るか、忘れた後にセッションができて(また知って)いるかのどちらかになる |
| `session.list` | `{repo_id?}` | `[Session]` |
| `session.get` | `{slug}` | `Session` |
| `session.new` | `{repo, branch, from?, agent?, model?, effort?, prompt?, base?, request?: id, after?: [slug], hold?: bool}` | `Session` に `cursor` を足したもの(state = creating。進捗は `session.progress` イベント。上限(`[orchestrator] max_running`)に達しているときと `hold` のときは state = queued で、起動したら creating に移る。上の「待ち行列」)。`hold` は `after` がすべて landed になるまで起動しない(`after` が無ければ `--hold waits for the --after sessions to land: give --after` で断る)。`cursor` は作る直前の `wait` の cursor(landed の同じ slug を置き換えたときは、古い方の `removed` の後)で、`wait --slug <slug> --since <cursor>` ならこのセッションの変化(`started`・`failed`・`report` …)を、返ってから待ち始めるまでの間に起きたものも取りこぼさない(下の「待ち合わせ」)。setup を待つなら `--on started,failed,removed`(`removed` は作成中に cancel・rm で消されたときも返るように)。`model` / `effort` はこのセッションだけ `[agents.*] model` / `effort` を上書きし、`Session.model` / `effort` に残して resume・再起動でも渡す(Claude Code のエージェントだけ。それ以外のエージェント・モデル名として読めないもの・`low`〜`max` 以外の effort は断り、何も作らない)。呼び出し元を `parent` に入れ、`prompt` を送信の記録に残す。`request` は依頼(open のもの)とセッションを結ぶ(token なしでは tapi の窓からだけ。断るときはセッションを作らない)。`after` は `Session.after` に入る(`<repo>/<slug>` も可、重複は 1 つに、10 個まで)。今ないセッション(`no such session`)・自分自身(着地済みの同じ slug を作り直すとき)・循環(`feat-a → feat-b → feat-a`)は断り、何も作らない。後から `pekoe rm` された相手は残る(TUI は `gone`)。同じ slug のセッションが landed 以外であれば `session <slug> already exists` で断る。同じ slug の `session.new`(`feat/x` と `feat-x` のように slug が同じになる別のブランチも)を同時に打っても、作られるのは 1 つで、ほかはこれで断る(確かめてから登録するまでを、`session.new`・`session.open` の間で 1 本ずつにする)。作る worktree のパスにほかのセッションがいる(そのディレクトリを `pekoe open` したもの)ときは、使い回さずに `worktree <path> is already managed as <slug>` で断る |
| `session.cancel` | `{slug}` | 作成中・待ち行列(queued)を中止し worktree を消す |
| `session.open` | `{path, resume?: "ask"\|"resume"\|"new", agent?, prompt?, width?, height?}` | `Session`。すでにセッションのある worktree は `already managed as <slug>` で断る(パスは symlink を解いて比べる。macOS の `/tmp` → `/private/tmp` や symlink 越しの `PEKOE_WORKTREES` で別の名前を渡しても同じ worktree) |
| `session.adopt` | `{path?}` | `[Session]` |
| `session.send` | `{slug, text, submit?: bool, window?}` | pty に文字を書く。複数行は bracketed paste。`submit` で最後に Enter。テキストとして送るので、改行(CR・CRLF は LF に)とタブ以外の制御文字は落とす。キーは `session.keys` で送る。許可ダイアログが出ている間は送らない(下の「send と許可ダイアログ」)。token なしでは agent の窓だけ(上の「人間だけの操作」)。agent の窓への指示(`submit` つき、または token なし)は送信の記録に残す(断ったものも) |
| `session.keys` | `{slug, window?, keys: [tmuxKey]}` / `{slug, window?, literal: "abc"}` / `{slug, window?, hex: ["1b", "5b", …]}` | 生キー転送(TUI の右ペインフォーカス時)。`window` は `session.screen` と同じ(省略で agent) |
| `session.answer` | `{slug, answer: "allow"\|"deny"\|"always", expect?: {tool}}` | 許可待ちに答える。`expect` を渡すと、待っているのがそのツール(`pending_tool` の name と input)のときだけ答え、違えば `the prompt changed; look again` のエラー(Web UI が使う) |
| `session.interrupt` | `{slug}` | `Session`。エージェントの窓に Escape を 1 回だけ送り、今のターンを止める(TUI で打っている間の `esc`、ローカル GUI の Interrupt)。送るのはエージェントが動いているとき(hooks の状態が `running` で、画面の下端 15 行にスピナー行の `esc to interrupt` があり `Enter to select` のメニューが無い)だけで、許可待ち(hooks の状態か画面の許可メニュー。Escape が「No」になる)・質問待ち・idle・画面にターンが見えない(esc で止めたターンには Stop が来ず、hooks の状態は `running` のまま残る)・`stopped`・窓が無いときは `… nothing was sent` で断る。同じエージェントには 2 秒に 1 回まで(2 回目の Escape で巻き戻しのメニューを開かない)。送った後は画面を見て(10 秒まで)、ターンが画面から消えて入力欄に戻ったら `running` を `idle` にする(Stop が来ないため。その間に hook が来たらそちらを使う)。`session.send` と同じ送信の排他の中で送る。人だけ |
| `session.transcript` | `{slug, after?: cursor, before?: cursor}` | `ChatPage`。セッションの Claude Code の会話を `tapi.transcript` と同じ形・同じ絞り方(返さないものも同じ)で返す。読むのはセッションの worktree のパス(とシンボリックリンクを解いたパス)の Claude Code のプロジェクトディレクトリ(`~/.claude/projects/<エンコードしたパス>/`、`$CLAUDE_CONFIG_DIR` があればその下)の中だけで、hook が報告した会話の id(`claude_session_id`)のファイルがそこにあればそれ、無ければそこのいちばん新しいもの。hook が渡した `transcript_path` は使わない(`session.event` は token なしで誰でも送れる)。シンボリックリンクの transcript・ディレクトリは読まない。Claude Code でないエージェント(`[agents.claude]` でも command が `claude` でもない)と、まだ会話が無いときは `no transcript to read`。人間だけ(ローカル GUI の Chat) |
| `session.commands` | `{slug}` | `{agent, supported, commands: [{name, source, description?}]}`。セッションのエージェントのスラッシュコマンドの一覧(ローカル GUI の `/ Commands`)。`source` は `builtin`(Claude Code の組み込み。pekoe が持つ一覧で、版で増減する近似)・`project` / `project:<サブディレクトリ>`(worktree の `.claude/commands/*.md` と 1 段下)・`project skill`(`.claude/skills/<name>/SKILL.md`)・`user` / `user skill`(`~/.claude` か `$CLAUDE_CONFIG_DIR` の `commands`・`skills`)。同じ名前は先のものだけ。説明は frontmatter の `description:`、無ければ最初の行を 1 行・200 バイトまで(制御文字は空白)。worktree の `.claude` の下はシンボリックリンクをたどらない(リポジトリとエージェントが書ける場所なので)。名前は `[A-Za-z0-9][A-Za-z0-9._-]{0,63}` だけ、1 つのディレクトリから 300 件まで。プラグイン・MCP のプロンプトのコマンドは読まない。Claude Code でないエージェントは `supported: false` で空。人間だけ |
| `session.files` | `{slug, q?}` | `{files: [path], matches, total, truncated?, skipped?}`。worktree のファイル(`git ls-files --cached --others --exclude-standard`。追跡しているものと、無視していない未追跡のもの)のうち `q` の語をすべて含むパス(大文字小文字を区別しない)を、ファイル名が最後の語で始まるもの → ファイル名に含むもの → パスに含むもの、同じなら短い順で 50 件まで(ローカル GUI の `@ Files`)。git は pekoed のいつもの守り(gitx: リポジトリの hooks・filter・fsmonitor を使わない)で動かし、同じ worktree には 5 秒に 1 回まで。git はシンボリックリンクの先に降りず、index のパスに `..`・`.`・空の要素を含むもの(エージェントは自分の worktree の index を書ける)は数えて(`skipped`)出さないので、worktree の外のパスは出ない。pekoed はファイルの中身を読まない。追跡しているものを先に、未追跡のもので残りを埋めて 50000 件まで(超えた分は `truncated`。未追跡の大きな生成物があっても追跡しているファイルは落ちない)、制御文字を含む・UTF-8 でない・1024 バイトを超える名前は数えて(`skipped`)出さない。`q` は 256 バイトまで。`.git` が変えられたセッション(`git_blocked`)では断る。人間だけ |
| `session.panel` | `{slug}` | `{agent, supported, why?, model?, model_name?, effort?, effort_from?, mode?, tokens?, tokens_short?, running_version?, installed_version?, update_installed?}`。ローカル GUI の Session の右パネル。`model` は transcript の最後の assistant の model(`[A-Za-z0-9._:-]` の 80 文字まで)、`model_name` は `claude-<名前>-<n>-<n>` を `Opus 5.5` の形にしたもの。`effort` は hook の共通入力の `effort.level`(`effort_from: hook`)、まだ来ていなければ起動に渡したもの(`start`)。`mode` は hook の `permission_mode`(`default`・`plan`・`acceptEdits`・`auto`・`dontAsk`・`bypassPermissions` だけ)。`tokens` は最後の usage の合計(% は無い)。`running_version` は transcript の行の `version`、`installed_version` はエージェントのコマンド(名前が `claude` のときだけ)を `--version` で動かした 1 行目の `x.y.z (Claude Code)`(コマンドごとに 10 分覚える。失敗も)で、`update_installed` は前が後より古いとき。Claude Code でないエージェントは `supported: false` と `why` だけで、何も読まず何も動かさない。tapi は無い。どれもエージェントの自己申告(hook は token なしで送れ、transcript はエージェントが書ける)なので表示だけに使う。人間だけ(pekoed にプログラムを動かさせるため) |
| `session.screen` | `{slug, window?: "agent"\|"review"\|"dev"\|"run"}` | `{lines: [ansi string], cursor: {x,y,visible}, width, height, dead, exit_status, signal?}`(`exit_status` は終わった窓の終了コード。動いている間やシグナルで終わったときは -1。`signal` は `"SIGINT"` など) |
| `session.resize` | `{slug, width, height}` | pty サイズ変更 |
| `term.caps` | `{kitty_graphics}` | TUI が外側の端末にできること(今は Kitty graphics を描けるか)を知らせる。結果は空 |
| `session.seen` | `{slug}` | 未読ラインの基点を今に |
| `session.kill` | `{slug}` | セッション終了(worktree は残す)。`creating`・`queued` のセッションは断る(`… is being created; …`・`… is queued (…); …`。止めるのは `session.cancel`。上の「待ち行列」) |
| `session.undo` | `{slug, n?}` | プロンプト単位 undo(省略時は最後のプロンプト前に戻す) |
| `session.event` | `{hook, session?, payload}` | hooks から(`pekoe event` が呼ぶ)。振り分けは窓が先、なければ `payload.transcript_path` の会話の場所、`session`(`$PEKOE_SESSION`)、`payload.cwd`(上の「呼び出し元の識別」) |
| `report.post` | `{summary, status: "done"\|"blocked"\|"progress"\|"review", evidence?: [string], verdict?: "pending"\|"pass"\|"fail", reviewed_sha?}` | `Report`(下記)。呼び出し元のセッションの報告(下の「報告」)。token つきなら `slug` で相手を指定できる |
| `report.list` | `{slug?, since?: id, limit?: 20}` | `[Report]`(新しい順。`limit` は 200 まで) |
| `wait` | `{slugs?: [slug], mine?: bool, on?: [kind], since?: cursor, timeout_s?: 1800, rewait?: bool, inbox?: id}` | `{cursor, epoch, changes: [Change], timed_out?, reset?}`(下の「待ち合わせ」) |
| `send.log` | `{to?, from?, since?: id, limit?: 100}` | `[SendRecord]`(新しい順。下の「送信の記録」) |
| `inbox.post` | `{question, kind: "decision"\|"approve"\|"login"\|"fyi", options?: [string], recommend?: int, context?, links?: [string], about?: slug, decided?: int}` | `InboxItem`(下の「判断の受信箱」) |
| `inbox.list` | `{status?: "open"\|"answered"\|"withdrawn"\|"dismissed"\|"all", mine?: bool, limit?: 100}` | `[InboxItem]`(新しい順。既定は `open`。`limit` は 500 まで) |
| `inbox.get` | `{id}` | `InboxItem` |
| `inbox.withdraw` | `{id, note?}` | `InboxItem`。呼び出し元が `human` でなければ自分が出したものだけ。`human`・`unknown`・`pekoed` という名前のセッションの窓からは断る(`inbox.list --mine` も) |
| `inbox.answer` | `{id, choice?: int, note?, rev}` | `InboxItem`。人だけ。`rev` が今のものと違えば `… the item changed; look again`。選択肢があっても、`choice` を省いて `note` だけで答えられる(選択肢のどれでもない。pekoed の根拠の項目は除く)。`choice` も `note` も無いものは断る |
| `inbox.dismiss` | `{id, note?}` | `InboxItem`。人だけ(答えずに閉じる) |
| `ledger.list` | `{status?: "open"\|"all", since?: id, auto?: bool}` | `[LedgerEntry]`(古い順。既定は `open`。下の「依頼(ledger)」)。`status: open` で `auto: true` なら `LedgerView` = `{open: [LedgerEntry], auto_closed: [LedgerEntry], auto_closed_more}` を返す: `open` のもの(全文)と、pekoed が直近 24 時間に自動で閉じたもの(`status: chat`・`auto: true`)の新しい 10 件(古い順。本文は 200 文字で切り、切ったものは `cut: true`)と、残りの件数。token なしでは tapi の窓からだけ |
| `ledger.close` | `{id, as: "done"\|"dropped"\|"chat", evidence?: [string], note?}` | `LedgerEntry`。`dropped` は `note` が要る。`open` のものだけ。token なしでは tapi の窓からだけ |
| `ledger.link` | `{id, slug}` | `LedgerEntry`。セッションを依頼に結ぶ(`Session.request` も入る)。`open` のものと、pekoed が自動で閉じたもの(`auto`。`open` に戻る)だけ。token なしでは tapi の窓からだけ |
| `ledger.keep` | `{id?: id}` | `LedgerEntry`。依頼として残す: `id` なしは tapi のこのターンの入力(返事を終えても自動で閉じなくなる)、`id` つきは pekoed が自動で閉じたもの(`auto`)を `open` に戻す。`open` のものはそのまま返す。tapi か人が閉じたものは断る。token なしでは tapi の窓からだけ |
| `ledger.reopen` | `{id}` | `LedgerEntry`。人だけ(閉じたものを開き直す。誰が閉じたものでも) |
| `ledger.delete` | `{id}` | `null`。人だけ |
| `attention.next` | `{from?: slug}` | 待ち時間順で次の waiting セッション。いなければ `pr_status.attention` のあるセッション(古い順) |
| `review.next` | — | 次のレビュー待ち(done) |
| `worktree.list` | `{repo_id?, repo?}` | `[{path, branch, slug?, port?, managed, main, state?}]`。`repo` はリポジトリの中のパス。`main` は本体のチェックアウト、`state` は結びついたセッションの状態 |
| `worktree.rm` | `{slug, force}` | `{removed, worktree_deleted, port_held?: {port, repo_id, slug, until}}`。`force` が無ければ、worktree に未コミットの変更があるとき `… has uncommitted changes (use force)` で断る。これは作成を止める前に確かめ、断ったときは何も変えない。作成中で、worktree をその作成が作ったもの(`own_dir`)は、中身が setup の置いたものだけなので確かめない(`session.cancel` と同じく止めて消す)。作成中(creating)のセッションは、まず作成(setup など)を止め、それが終わるのを待ってから先へ進む(`session.cancel` も同じ)。setup などの hook は自分のプロセスグループで動かし、止めるときはグループごと止める(hook が起動した子プロセス(`cd web && npm ci` の npm など)も残らない)。マージ後の後片付け(teardown と worktree の削除)が走っている最中なら、その終わりを待つ。`land` の最中(`[hooks] test` の間など)は待たない。その land は、続きの push・base の fast-forward・記録・後片付けの前にセッションが同じものかを確かめ、消されていれば何もせずに `<slug> was removed while landing …: not landed` で終わる(同じブランチで作り直した新しいセッションに手を出さない)。dev hook を止めて `dev_stop`(dev hook を起動したことがあり、最後の `dev_stop` が成功していなければ、dev が false でも)→ 窓を閉じる → 窓が残したプロセスを止める(design.md「片付けで止めるプロセス」。land 後の片付けも同じ)→ teardown → worktree 削除(teardown と削除は pekoe が作った `~/worktrees` 配下の worktree だけ。`session.open` で開いたリポジトリ本体などは teardown も走らせず、記録を消すだけ)。`dev_stop` が成功しないまま(失敗・走らなかった)消すときは、`[hooks] dev_stop` のあるリポジトリならそのポートを 7 日間ポート台帳から外し(`held_ports`。期限が過ぎたら次の割り当てで消す)、`port_held` で返す。token なしでは landed のセッションの記録を消すだけ(上)。同じ worktree(symlink を解いて同じ場所)にほかのセッションがいれば、teardown も worktree の削除もせず記録だけ消す(land 後の片付けも同じ)。pekoe が作った worktree のディレクトリがもう無い(手で消した・再起動のときに `worktree missing` になった)ときは、git に残った worktree の登録と `refs/pekoe/<slug>/` を消す(pekoe が作っていない worktree は、ディレクトリが無くても記録を消すだけ)(同じブランチの `session.new` が `missing but already registered worktree` で落ちないように) |
| `land` | `{slug}` | `{steps, pr}`(origin の無いリポジトリでは `{steps, landed: true}`)。rebase → test → push → `gh pr create`。マージ検出後に自動 teardown。pekoe が作ったセッション(`managed`)だけを land し、ほかのもの(`session.open` したリポジトリ本体のチェックアウトなど)は `… is not a session pekoe made …`、ブランチが無いもの(detached HEAD)は `… has no branch to push …`、ブランチが base(`origin/` を外したもの)と同じものは `… is on main, its base …`、worktree でセッションと別のブランチを checkout しているものは `the worktree has … checked out, not the session's branch …` で、何もせずに断る(手元の base のコミットを PR なしで origin の base に push しないため。token なしでも人でも同じ)。base は origin から fetch してその `origin/<base>` へ rebase し、PR もその branch に向ける(base が `origin/main` なら `origin/` を外した `main`)。fetch が落ちたら `steps` にそう書き、手元の base へ rebase する。セッションに PR があっても、GitHub で今 OPEN でない(MERGED・CLOSED。読めなければ `pr_status.state`)で、base に入っていないコミットがあれば、使わずに新しい PR を作り、`pr_status` を空にする(コミットが無ければ前の PR のまま。マージ済みならその検出で後片付けする)。ただし fetch が落ちていたら、手元の base が origin より古く、前の PR のコミットまで「base に入っていない」と数えうる(そのまま `gh pr create` に進むと No commits between で落ちる)ので、手元の base と比べてコミットがあれば `gh pr create` を試し、origin の base を持つ GitHub が No commits between と答えたら、エラーにせず前の PR のままにする(`steps` に `no commits beyond origin's main: PR #41 kept`)。PR ができれば新しい PR に移る。手元の base と比べてもコミットが無ければ前の PR のまま。`gh pr create` が already exists で番号を返さなければ、そのブランチの OPEN の PR だけを拾う |
| `dev.toggle` | `{slug}` | dev server の起動/停止。答えはセッション。止めるときは `[hooks] dev_stop` を dev と同じ worktree・環境変数・sandbox で実行し(5 分まで)、結果を `dev_exit.dev_stop` とログに残す(失敗したら止めたうえでエラー)。hook が自分で終わったときも(2 秒ごとの tick が見つけたとき、または見つける前の `dev.toggle` の起動の前に)同じように `dev_stop` を実行する。作成中・queued・landed・worktree が無いセッションでは起動しない(`run.start` と同じ)。`[hooks] dev` が無いときのエラーには設定ファイルの様子(無い・読めない・`[hooks]` が無い・`[hooks]` にあるキー)を添える。token なしでは呼び出し元のセッションの分だけ(上の「人間だけの操作」) |
| `dev.log` | `{slug?, lines?}` | `{slug, path, running, exit?, lines: [string]}`。dev hook のログの末尾 `lines` 行(既定 100、5000 まで)。ログには hook の出力(エスケープシーケンスを除き、`\r` は改行に)と、pekoed の `[pekoe <時刻>] …` の行(起動・自分で終わった(終了コード)・pekoe が止めた・`dev_stop` とその出力)が入る。2 MiB を超えたら `.1` に回す。rm で消す。token なしでは呼び出し元のセッションの分だけ |
| `run.start` | `{slug, command?, width?, height?}` | `RunState` = `{running, finished, started, command, exit_status, signal?}`。worktree で `command` を 1 回だけ run 窓に起動する(dev と同じ環境変数、`$SHELL -c`)。run 窓が動いている・終わって閉じていない間は起動せずにその状態(実行中のコマンドと、終わっていれば終了コード。シグナルで終わったら `exit_status` は -1 で `signal: "SIGINT"`)を返す。`command` なしは状態を見るだけ。作成中・landed・worktree が無いセッションではエラー。呼び出しは 1 本ずつ処理する |
| `run.close` | `{slug}` | run 窓を閉じ、閉じる前の `RunState` を返す(TUI の「tell the agent?」用)。セッションの `run` を空にする |
| `changes` | `{slug}` | `[{status: "A"\|"M"\|"D", path, added, deleted, binary?}]`(base からの変更。コミット済みと未コミットのスナップショット。パスは git の引用符なし) |
| `changes.diff` | `{slug, path}` | `{slug, path, status, binary?, added, deleted, hunks: [{header, lines: [{kind: " "\|"+"\|"-", old?, new?, text, cut?}]}], lines, total, truncated?}`。1 ファイルの差分(前後 3 行)。`path` は `changes` の一覧にあるものと完全に一致したときだけ読み(無ければ `not a changed file of this session`)、git には literal の pathspec で渡す。3000 行・本文 256 KiB で切って `truncated`、1 行は 2000 バイトで切って `cut`。git の出力は流しながら読んで送る分だけを持ち、32 MiB で git を止める(`total` はそれまでに数えた行と `added + deleted` の大きいほう)。型の変更(`T`)は削除と追加の両方の hunk。UTF-8 でない名前は、一覧の JSON と同じく不正なバイトを U+FFFD にした名前が 1 つにだけ一致すれば読む。ローカル GUI の Changes の読むだけの表示 |
| `conflicts.list` | `{repo_id?}` | `[{repo_id, a, b, status: "overlap"\|"conflict", files, conflict_files?}]`。その場で計算し直し、各セッションの `overlaps` も更新する(`pekoe conflicts`)。token なしでは、前の計算(pekoed が裏で回す約 10 秒ごとの計算を含む)から 10 秒以内ならそのときの結果を返し、git を動かさない(どのエージェントも 10 秒に 1 回より多く worktree の git を走らせられない)。git は pekoed の git(下の「pekoed が動かす git」)で、diff と `merge-tree` だけ |
| `cleanup.list` | `{repo_id?, stale_after?: "72h"}` | `{stale_after, items: [CleanupItem]}`。片付けの候補(`pekoe cleanup`、TUI の `:cleanup`)。読むだけで、何も消さない。`kind` は `landed`(マージを検知した pekoed が teardown と worktree の削除を済ませ、記録だけ残る)・`merged`(PR は GitHub でマージ済みだが pekoe land を通っていないので worktree が残る)・`stale`(running・waiting・creating 以外で、dev server が止まっていて、`stale_after`(既定は `[cleanup] stale_after`、それも無ければ 72h)より長く状態が変わっていない)の順、同じ種類の中は古い順。pekoe land が PR を作ってマージ待ちのセッション(`landing`)と、リポジトリ本体のチェックアウトのセッションは出さない。worktree が残っていれば、pekoed の git(`status --porcelain` と `rev-list --count HEAD --not --remotes <base との merge-base> [<マージした PR の head>]`)で `dirty` と `unpushed` を数え、`warnings` に言葉で書く(`2 uncommitted files`・`1 unpushed commit`・`PR #12 is still open` など)。読めなければ -1 と `unknown`。token なしでは git の読み取りを worktree ごとに 10 秒に 1 回までにし、それ以内は前の結果を返す。`human_only` は token なしの `worktree.rm` が断るもの(worktree の消えた landed 以外すべて) |
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
| `session.restart` | `{slug, width?, height?}` | エージェントを再起動(Claude Code セッションを resume)。作成中(creating)・queued のセッションでは断る。worktree の無いセッション(landed・worktree を作る前に落ちた・ディレクトリが消えた)でも断る(`… has landed; its worktree is gone`・`worktree missing: <path>`。tmux は窓のディレクトリが無いと `$HOME` で起動し、エージェントの sandbox は作業ディレクトリに書けるため。エージェントを起動するほかの経路(open・pekoed の再起動後の resume)も同じ) |
| `session.progress` | `{slug}` | 作成中の手順 `{slug, steps}` |
| `review.surface` | `{slug, width, height}` | 右ペインを nvim(pekoe.nvim のレビュー面)に入れ替える。claude は裏で動き続ける。読み込むプラグインは pekoed 自身のもの(`PEKOE_NVIM_RTP`、なければ実行ファイル(symlink を解いたもの)の 1 つ上の `nvim/` か `share/pekoe/nvim/`)だけで、呼び出し元からは指定できない |
| `review.pr` | `{repo, number}` | `pekoe review gh#N`: PR ブランチの worktree を作り、同じレビュー面で開く。コメントは GitHub review に送られる |
| `pr.view` | `{slug, seen?: bool}` | `PRView`(下記)。PR が無ければ `null`(番号未登録ならブランチから探す)。`gh api graphql` 1 回で取り、ついでに `pr_status` を更新する。`seen` で attention を既読に |
| `pr.seen` | `{slug}` | `pr_status.attention` を既読にする(取得はしない) |
| `merge.check` | `{slug?}` | `[MergeCheck]`(slug 順)。landed でない自分の PR のセッション(他人の PR のレビューを除く)ごとに、マージしてよいかの条件の何が揃っていて何が足りないか。`slug` を渡すとそのセッションだけ(下の「マージしてよいかの根拠」) |
| `tips.next` | — | `{enabled, tip?: Tip, prefix?}`。まだ見せていない tip を 1 つ選び、`tips.json`(TUI の空き表示・`pekoe tip` と共通の記録)に書いて返す。`prefix` は pekoed が読んだ `[keys] prefix`(`ctrl+a` など。tip の文の `^q` を置き換える。`pekoe tip` は sandbox で config.toml を読めないのでこれを使い、無ければ自分で読んだもの)。`tips.json` は `tips.json.lock` の flock で囲んで読み・選び・書き、一意の一時ファイルから rename する(TUI と pekoed が同時に選んでも、同じ巡で同じ tip を出さない)。古い版が使っていた固定名の一時ファイル `tips.json.tmp` が残っていれば、そのとき消す(古い pekoe が書いている最中のものを消さないよう、1 分より古いものだけ)。`[tips] enable = false` なら `{enabled: false}` で何も書かない。`Tip` は `pekoe --json tip --list` の 1 件と同じ形(`id`・`short`・`detail`・`keys`・`docs`)。書けなかったらエラー |
| `tips.prefix` | — | `{prefix}`。pekoed が読んだ `[keys] prefix`(`pekoe tip --list` / `pekoe tip <id>` が tip の `^q` を置き換えるのに使う。何も書かない) |
| `stats` | `{days?: 7, repo?}` | `Stats`(下の「集計(stats)」)。ローカルの日付で今日を含む `days` 日(1〜3660)・リポジトリごとのマージ数とトークン。`repo` はリポジトリの名前か repo-id(名前はマージとトークンをリポジトリの名前で絞る。repo-id はマージの記録の repo-id と、そのリポジトリのセッション(いるものと、記録に残ったもの)のトークンで絞るので、`pekoe repo forget` の後で名前に直せない id でも両方出る。CLI は知っているリポジトリなら名前に直して渡す。省くと全部。tapi chat の分も) |
| `pr.failed_log` | `{slug, check, run_id?, job_id?, lines?: 200}` | 失敗した Actions の check のログ末尾 `{check, run_id, job_id, log, truncated}`(`gh run view --job <id> --log-failed`、取れなければ run 単位) |
| `window.kill` | `{slug, window: "review"\|"dev"\|"run"}` | レビュー面 / dev server / run のウィンドウを閉じる(run は `run.close` と同じ) |
| `browser.send_errors` | `{port, slug?}` | 溜まった console error をまとめてセッションに送る(ファイルに書いて `@` 参照の 1 行。そのポートのエラーカードは sent になる) |
| `error.list` | `{slug?, all?}` | `[ErrorReport]`(古い順。既定は未対応 = `new` だけ、`all` で送信済み・無視も) |
| `error.get` | `{id}` | `ErrorReport` + `excerpt`(先頭フレームが worktree 内なら前後 5 行の抜粋) |
| `error.send` | `{id, slug?, text?}` | エラーをエージェントに送る。1 行で済むものはそのまま、それ以外は `~/.local/share/pekoe/<repo-id>/errors/<slug>-<n>.md` に書いて `@<path> <text>` の 1 行を送る。`ErrorReport`(sent) |
| `error.ignore` | `{id}` | 無視する(同じ指紋が再発するまで出さない)。`ErrorReport`(ignored) |
| `ext.info` | — | `{port, token}`(`pekoe ext`)。`port` は実際に待ち受けている番号。待ち受けていない(bind に失敗した)ときは理由を添えて断る |
| `gui.open` | — | `{port, file, expires, browser?}`(`pekoe gui`)。ローカル GUI を `127.0.0.1` で待ち受けていなければ始め(`[gui] port`、`0` は空いているもの)、一度きりのトークン(32 バイト・60 秒・1 回)を作って、`http://127.0.0.1:<port>/#t=<トークン>` へ移るだけの HTML を `<データディレクトリ>/gui/open-<乱数>.html`(0600、ディレクトリは 0700)に書く。答えにトークンは入れない(CLI はファイルのパスだけを `open` / `xdg-open` に渡す)。bind に失敗したらファイルを書かずに断る(そのポートにいるものが偽の GUI かもしれないので、CLI はブラウザを開かない)。`[gui] enable = false` でも断る。下の「HTTP(ローカル GUI)」・[gui.md](gui.md) |
| `gui.status` | — | `{enabled, listening, port?, sessions, last_used?}`(`pekoe gui --status`)。`sessions` は有効なセッションのトークンの数、`last_used` は人が最後に操作(サインインか POST)した時刻 |
| `gui.revoke` | — | `{revoked}`(`pekoe gui --revoke`)。セッションのトークンと、まだ使われていない一度きりのトークン(とその HTML)を全部失効させる |
| `gui.stop` | — | `{stopped, revoked}`(`pekoe gui --stop`)。待ち受けを止め、トークンも全部失効させる |
| `web.status` | — | `{built, enabled, state, auth_url?, urls?, error?, backend?, backend_error?}`(`pekoe web`)。`built` はこのバイナリに Web UI が入っているか(`-tags web`)。state は `off` `unavailable`(Web UI なしのビルドで有効にされている)`starting` `needs-login` `running` `error`。`auth_url` は初回参加のログイン URL。`backend` は Tailscale の backend の状態(`NoState` `Starting` `Running` など)、`backend_error` は backend が control から受け取った最後のエラー(Running になると消える)。`starting` のまま進まない理由を見るため |
| `issues.list` | `{repo? \| repo_id?, filter?: "mine"\|"open"\|"label", label?, refresh?}` | `IssueList`(下記)。`gh issue list --state open --json number,title,labels,assignees,updatedAt,comments,url`、`mine` は `--assignee @me`。3 分キャッシュし、`refresh` で取り直す |
| `tapi.get` | — | `Tapi`(下記) |
| `tapi.open` | `{width?, height?}` | `Tapi`。tapi chat のエージェントを、窓が無ければ起動する(pekoed の起動時には起動しない)。窓が残っていれば(pekoed を再起動しても tmux サーバーは残る)そのまま |
| `tapi.restart` | `{width?, height?}` | `Tapi`。窓を作り直す(エージェントが終了した後の ↵。Claude Code は `--continue` で同じ会話を続ける) |
| `tapi.reset` | — | `Tapi`。窓を閉じ、Claude Code の会話の記録(`~/.claude/projects/<tapi の cwd>/`)を消す。次の `tapi.open` は新しい会話 |
| `tapi.screen` | — | `session.screen` と同じ形(起動前はエラー) |
| `tapi.transcript` | `{after?: cursor, before?: cursor}` | `ChatPage`(docs/web.md の「吹き出し(chat)と画面(screen)」)。tapi の hook が渡した transcript(tapi の Claude Code のプロジェクトディレクトリの中だけ)を、人・tapi のテキスト・ツールの名前と主な引数・ターンの所要時間に絞って、カーソルの後ろから返す。`before`(前の答えの `before`)を渡すと、代わりにその前のページ(6 MB・400 件まで、新しい方から)を返し、`cursor` は空。先頭まで読めば `before` は無い。会話が変わった・transcript が短くなったときは `the conversation changed` のエラー。tool_result・thinking・システムの注入は返さない。人間だけ |
| `tapi.send` | `{text, submit?}` | `session.send` と同じ(送信の排他も。`tapi.interrupt`・pekoed の配送・受信箱の答えを打つ経路と同じ排他の中で打つので、ほかの文や Escape が貼り付けと Enter の間に入らない) |
| `tapi.keys` | `{keys?, literal?, hex?}` | `session.keys` と同じ |
| `tapi.interrupt` | — | `Tapi`。tapi chat の窓に Escape を 1 回送る(`session.interrupt` と同じ条件。送った後に画面を見て `idle` にするのも同じ)。人だけ |
| `tapi.answer` | `{answer: "allow"\|"deny"\|"always", expect?: {tool}}` | tapi chat の許可待ちに答える(TUI の許可モーダル)。`session.answer` と同じく `[agents.<tapi の agent>]` のキーを打ち、`expect` が違えば `the prompt changed; look again`、待っていなければエラー。人だけ(tapi が自分の許可に答えないように)。Web UI には出さない |
| `tapi.resize` | `{width, height}` | pty サイズ変更(`session.resize "*"` でも合わせる) |
| `tapi.deliver_pause` | `{on: bool}` | tapi への配送(下の「tapi への配送」)を止めておく。TUI が tapi chat を開いて打っている間 `on: true` を 5 秒ごとに送り(pekoed は 1 回を 20 秒覚える。TUI が落ちても止まりっぱなしにならない)、離れたら `on: false` を 1 回送る。人だけ(エージェントが配送を止める・流すことのないように) |
| `issues.view` | `{repo? \| repo_id?, number, refresh?}` | `IssueDetail`(下記)。`gh issue view N --json …`。同じく 3 分キャッシュ |
| `preview.capture` | `{slug, path?, width?, height?, mobile?: bool}` | `Preview`。headless Chrome で `http://localhost:<port><path>` を撮り、終わってから返る。`path` と `mobile` は次回以降も使う(既定 `/`、デスクトップ 1280×800、モバイル 390×844)。撮れなかった理由は `error` に入る |
| `preview.get` | `{slug}` | `Preview` |
| `preview.watch` | `{slug, on: bool}` | `Preview`。表示中の印。on のセッションはエージェントが手を止めたとき(running → waiting / idle / done)に自動で撮り直す |
| `voice.status` | — | `VoiceStatus`(下の「音声(voice)」) |
| `voice.mute` | `{for?: "30m"}` / `{until?: RFC3339}` / `{off?: true}` | `VoiceStatus`。全体のミュート。どれも無ければ unmute まで。ミュートは鳴っているものを止めてキューを空にする。期限は state.db の kv(`voice.mute_until`)に残り、再起動しても続く |
| `voice.stop` | — | `null`。鳴っている発話を止める(キューは残す) |
| `voice.say` | `{text, voice?: "tapi"\|"reader", lang?: "ja"\|"en"}` | `VoiceStatus`。文をそのまま合成して鳴らす(`pekoe voice test`)。キューの先頭に積み、ミュート中も鳴らす。text は 2000 バイトまで。voice が無効(`[voice] enable = false`)・macOS 以外ではエラー(無効の間は何も動かさない) |
| `voice.focus` | `{slug: "feat-x"\|"+tapi"\|"", term_focused}` | `null`。クライアントが [4] に出しているもの。接続ごとに覚え、最後に送ってきた接続のものを使う。接続が切れたらその接続の分を消す。無効の間も覚えるだけ |
| `voice.log` | `{n?: 50}` | 発話のログ(`voice.log`)の新しい方から n 件(1000 まで) |
| `voice.backends` | — | `{backends: [{name, path, using, fallback, version?, check, voices?, warnings?}], candidates: [{name, path, line}], note?}`。同梱の say と `[voice.backends]` に `version`・`check`・`voices` を呼んだ結果と、pekoed の `PATH` にある `pekoe-voice-*` の候補(動かさない。`line` は `[voice.backends]` に書く行の例)。voice が無効の間は backend を動かさず、パスの確かめだけをして `note` に理由 |

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
  "verdict": "pass", "reviewed_sha": "3f2a…",      // status = review のときだけ(verdict は必須)。pending は「独立レビューを頼んだ・レビュー中」の印(作者が頼むときに残す)、pass / fail はレビュアーの合否
  "at": "…"
}
```

- 呼び出し元(上の「呼び出し元の識別」)の窓のセッションに付く。token なしで `slug` に別のセッションを渡すと `a session reports only on itself` で断る。窓に当たらなければ `cannot tell which session is calling` で断る。tapi chat はセッションではないので断る
- 証拠のファイルは読まない(読めば、エージェントが pekoed に任意のファイルを読ませられる)。パスと URL を文字列として残すだけ
- 上限: summary 4000 文字、evidence 20 件・各 500 文字(超えたら断る)。summary は改行とタブ以外の制御文字を、evidence はすべての制御文字を落とす
- state.db の `reports` の表に残る
- `status: review` の報告は `Session.review` にも入る(その後の done・progress の報告では消えない)。ダッシュボードの工程表と Web UI の概要は、これで review の段を決める(pending は進行中、pass は済み、fail は失敗)。要約の文は解析しない

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

- `kind`: `report`(報告が来た)・`queued`(`session.new` が作成を始めずに待ち行列に入れた。`summary` は `feat-b: queued (--hold: waiting for feat-a to land)`。セッションごとに 1 回。待ち行列から出て起動したら、そこからは `started` / `failed` が出る)・`started`(作ったセッションの setup が済み、エージェントが動き始めた: `creating` から `failed` 以外に移った。セッションごとに 1 回。setup が落ちたら `started` は出ず `failed`。作成中に cancel・rm で消されたら `removed` だけなので、setup を待つときは `--on started,failed,removed`)・`done` `waiting` `idle` `failed` `landed`(その状態に入った)・`pr`(`pr_status.attention` に項目が増えた。1 項目ごと)・`ci`(PR の CI が決着した。下)・`removed`・`inbox`(自分が出した問いに人が答えた・答えずに閉じた。`summary` は `inbox q12 answered: 1 変える`、メモがあれば `inbox q12 answered: 1 変える · note: <メモの先頭 80 文字>`、メモだけの答えは `inbox q12 answered: none of the options · note: …`。メモは人の文で、1 行に均して切り詰めてある(信頼できないデータとして読み、全体は `pekoe --json inbox <id>` の `answer.note`)。届くのは問いを出した呼び出し元だけで、`mine` にかかわらず含まれる。`slugs` は問いの `about` で絞る)。`on` を省くとすべて
- `summary` は 1 行: `feat-csv: started (running)`(プロンプトなしで作ったら `(idle)`)、`feat-csv: done`、`feat-csv: waiting (permission)`、`feat-csv: failed: <エラー>`、`feat-csv: PR #42 ci_failed`、`feat-csv: PR #42 ci green: 5 passed, head <SHA>`、`feat-csv: report done: <要約の 1 行目>`。報告なしで done になった(最後のプロンプトの後に報告が無い)ものは `feat-csv: done (no report)`
- `ci` は、`OPEN` の PR の HEAD のチェックで `pending` が 0、`pass` + `fail` が 1 以上になったときに出る(`pr_status` を取り直したときに判定するので、遅れはポーリングの間隔に見回りの刻みの 15 秒を足したくらいまで)。`summary` は `feat-csv: PR #42 ci green: 5 passed, head <PR の HEAD の SHA(40 桁)>` か `feat-csv: PR #42 ci red: 2 failed (test, lint), 3 passed, head <SHA>`(落ちた check の名前は 3 つまで、残りは `+N more`)。知らせた HEAD を `pr_status.ci_head` に、知らせた結果を `pr_status.ci_result`(green / red、`pass` / `fail` の数、どの check が通り・落ちたか)に残し、同じ HEAD・同じ結果ではもう出さない(pekoed を再起動しても、同じ HEAD で check をやり直して同じ結果になっても)。HEAD が変われば、次に決着したときにまた出る。同じ HEAD でも、決着した結果が知らせたものと違えば(先に済んだ check だけで決着した後に、別の workflow の check が遅れて足されて終わった・re-run で red が green になった)もう 1 回出る。早い check だけで出た green の後に、同じ HEAD の red が来ることがあるので、同じ HEAD の `ci` は後に来たものが今の結果。check が 1 件も無い・`pass` も `fail` も 0(全部 skip)・`OPEN` でない PR では出ない。キャンセル・skip・neutral の check は `pass` にも `fail` にも数えないので、それらがあっても残りが通れば green。`pr` の `ci_failed` は最初の 1 本が落ちた時点で出るので、同じ CI で `pr` と `ci` の両方が出ることがある
- `since` を省くと「今から」。`since` の epoch が違う(pekoed が再起動した)・輪から落ちている・知らない番号なら、すぐに `reset: true` と今の `cursor` を返す。呼び出し側は `pekoe --json ls` を読み直す。永続化はしない
- 何も起きずに `timeout_s`(既定 1800、3600 まで)が過ぎたら `timed_out: true` と今の `cursor`。条件に合わなかった変化は飛ばして `cursor` は進む
- `inbox` は受信箱の項目 1 件の `inbox` の変化だけを待つ(`ask-human --wait <id>`)。呼び出し元のほかの問いの答えは受け取らない(それぞれの `wait` か入力欄の 1 行に残る)。項目は一度しか閉じないので、その項目の変化は `since` より前に来ていても返す(問いを `inbox.get` で見てから `wait` が張られるまでの間に答えが来ても取りこぼさない)。`on` は `inbox` に固定
- `mine` は `Session.parent` が呼び出し元のもの。同じ slug を作り直したセッション(landed を `session.new` で置き換えた)には、前のセッションが作った子は入らない(`Session.parent_born` で見分ける。これを記録する前に作られた子は入る)。`unknown` の呼び出し元は使えない。pekoe の窓の中のエージェント(main のチェックアウトを取り込んだセッションも)は窓の slug が呼び出し元になるので使える。pekoe の外で起動したエージェントは `unknown`(token を読めるなら `human` で、人が作ったセッションまで混ざる)なので、`--slug` で待つ。`slugs` は `<repo>/<slug>` も受け付ける
- token なしでは呼び出し元ごとに同時に 8 本まで。接続が切れると待ちも終わる

### 送信の記録

pekoed が仲立ちしたテキストの送信を、誰が誰に送ったか付きで state.db の `sends` の表に残す。

| 記録するもの | from | kind |
| --- | --- | --- |
| `session.new` の `prompt`(`from` があれば先頭に `--from gh#N` の行) | 呼び出し元 | `new` |
| `session.send` の agent の窓への指示(`submit` つき、または token なし) | 呼び出し元(TUI・CLI は `human`) | `send` |
| 受信箱の答えの配送(下の「判断の受信箱」) | `pekoed` | `inbox` |
| tapi への配送(下の「tapi への配送」。`to` は `+tapi`) | `pekoed` | `deliver` |
| Web UI・ローカル GUI からの tapi への送信(`POST /api/tapi/send`。`to` は `+tapi`、docs/web.md「tapi chat の扱い」) | `human` | `send` |

```jsonc
// SendRecord
{"id": 3, "at": "…", "from": "+tapi", "to": "feat-csv", "kind": "new", "text": "…", "via": "socket", "ok": true, "error": ""}
```

- `via` はソケット経由なら `socket`、Web UI からなら `web:<端末名>`、ローカル GUI からなら `gui`
- 記録しないもの: 打鍵(`session.keys`・`tapi.keys`)、TUI が入力をそのまま流す `submit` なしの `session.send`、dev・run の窓への送信、TUI・CLI からの `tapi.send`、ピック・エラー・レビューの送信
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
    "pr": 42, "pr_url": "https://github.com/me/app/pull/42",   // PR を出した先(fork なら base)の PR
    "pr_repo": "github.com/up/app", "push_repo": "github.com/me/app", // fork のときだけ: PR のあるリポジトリと push 先(origin)
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
- **メモだけの答え**: 選択肢のある問いにも、人は選択肢を選ばずメモだけで答えられる(「その他」。メモが答え)。このとき `answer` に `choice` のキーは無い(0 と取り違えない。`"choice": 0` は 1 番目を選んだ答え)。選択肢もメモも空の答えは断る。pekoed の根拠の項目(`from: pekoed` の `approve`。下の「マージしてよいかの根拠」)だけは選ぶことでしか答えられず、メモだけの答えは `… takes no note-only answer` で断る(メモだけでマージ・待つ・差し戻すにはならない)
- `kind`: `decision`(選ぶ。選択肢なしなら答えはメモで)・`approve`(選択肢を省くと `approve`・`decline`)・`login`(人の手作業。選択肢は `done`・`can't` に固定)・`fyi`(エージェントが自分で決めたことの記録。`decided` に選んだもの。`answered` で作られ、`answer.by` は呼び出し元)。選択肢が 1 つだけのものは断る
- `from` は呼び出し元(上の「呼び出し元の識別」)。エージェントの窓ならそのセッション。窓がない・run 窓・review 窓で token つきなら `human`(人の問いは誰にも届けない)、token なしで窓に当たらなければ断る
- 上限(超えたら切らずに断る): question 500 文字、options 6 個・各 200 文字、context 4000 文字、links 10 件・各 500 文字、メモ 4000 文字。呼び出し元ごとに未回答 10 件まで(`fyi` は数えない。数えるのは `from` の窓ごとで、token を読めるエージェントの窓(sandbox なし・tapi)も数える。数えないのは `human`(窓のない token つきの人と、run 窓・review 窓から token つきで打った人)だけ)。保存の前に ANSI・改行とタブ以外の制御文字・見えない書式文字(bidi の上書き・ゼロ幅)を落とす。選択肢とリンクは 1 行
- **facts と rev**: `facts` は `about` のセッションを pekoed が読んだもの(HEAD は git、ほかは pekoed が持つセッションの状態)。未回答のものは `inbox.list` / `inbox.get` / `inbox.answer` のたびに読み直し、変わっていれば `rev` を上げて `inbox.updated` を出す。`inbox.answer` は読み直した後の `rev` と比べるので、人が見てから HEAD が動いた・CI が落ちたなどの答えは断られる
- `inbox.withdraw` は呼び出し元(`Caller.ID()`)が `human` でなければ自分が出したものだけ。token を読めるエージェント(sandbox なし・tapi chat)も、エージェントの窓から呼べばそのセッションなので、ほかのセッションの問いは断る。未回答のものだけ閉じられる(答え・取り下げ・閉じるのどれも、`open` 以外には断る)。人(token つきで呼び出し元が `human`)がエージェントの問いを取り下げたら、`dismiss` と同じく問いを出したエージェントに届ける(下の「答えの届け方」。`answer.by` は `human`)。エージェントが自分の問いを取り下げたときは誰にも届けない(`delivery: none` のまま)
- `inbox.answer` / `inbox.dismiss` の `answer.by` も呼び出し元(`Caller.ID()`)。token を読めるエージェント(sandbox なし・tapi chat)がエージェントの窓から答えた・閉じたものは、通りはするが(token を読める以上、認可では止められない)`answer.by` はその slug(tapi chat なら `+tapi`)で残り、人が答えたことにはならない(前例にするのは `answer.by == human` のものだけ)
- **答えの届け方**(`answer` と `dismiss`、人の `withdraw`): 問いを出したエージェントが今 `wait` で待っていて、その `wait` が `inbox` を受け取るなら、その返り値で渡す(`delivery: waited`)。待っていなければ `pending` にして、依頼元のエージェントの入力欄が空いたときに 1 行打つ: `[pekoe inbox q12] 答え: 1「変える」(メモ: …) 詳しくは pekoe --json inbox q12`(メモだけの答えは `答え: 選択肢のどれでもない(メモ: …)`、閉じたものは `答えずに閉じられました`、人が取り下げたものは `人が取り下げました`)。1 行に均し、先頭は `[`(数字にしない)
  - 打つ条件: セッションなら `idle` か `done` で agent の窓が生きている、tapi chat なら起動していて、エージェントが動いていて(終了して窓だけ残っていない)、`idle`。どちらも許可ダイアログが出ていない(hooks の状態と画面の両方。受け付けた時と、送信ロックを取って打つ直前の 2 回。`session.send` の token なしの経路と同じ)。人がその窓に打った(TUI・CLI・Web UI の `session.send`・`session.keys`・`tapi.send`・`tapi.keys`、ローカル GUI の Interrupt の `session.interrupt`・`tapi.interrupt`)のが 30 秒以内なら待つ
  - 試すのは、依頼元が `idle` / `done` に入ったとき、答えた直後、5 秒ごと。1 回に 1 件(古い順)で、次は次に空いたとき
  - 依頼元が API で答えを読んだ(`inbox.get` / `inbox.list`、`wait` の返り値)ら `waited` にして打たない(打つ直前にもう一度確かめる)。token を送るエージェントの窓から読んでも同じ(人とみなすのは窓のない token つきの呼び出しだけ)
  - 依頼元のセッションが消えたら(`worktree.rm`、landed の slug を `session.new` で置き換えたとき)、そのセッションが出した未回答の項目は pekoed が `withdrawn` にし(`answer.by: "pekoed"`、`answer.note: "session removed"`)、そのセッションに打つはずだった答え(`pending`。pekoed のマージの項目の差し戻しも)は `none` にする。項目は `from` の slug で持ち主を表すので、こうしないと後で同じ slug で作り直したセッションが、前のセッションの問いの答えを受け取り、取り下げ、未回答の上限に数えられてしまう
  - 打ったものは送信の記録に `from: pekoed, kind: inbox` で残る。Enter の直前に許可ダイアログが出た(文字は入力欄に残った)ときは打ち直さず、記録に `ok: false` で残す
- `[orchestrator] deliver = "idle"` のとき、tapi が出した問いの答えはこの 1 行ではなく、tapi への配送の 1 行にまとめて入る(下の「tapi への配送」。`delivery` は同じく `delivered`)
- `inbox.updated` は token つきの購読にだけ届く。エージェントは `pekoe wait --on inbox`(や `ask-human --wait`)で自分宛ての答えだけを受け取る
- pekoed 自身も項目を作る(`from: "pekoed"`)。v1 では下の「マージしてよいかの根拠」だけ

### マージしてよいかの根拠

pekoed は `pekoe land` 済みのセッション(`landing`。push して PR を作り、人のマージを待っている)を 10 秒ごとに見て、下の条件が全部揃ったら受信箱に `from: "pekoed"`・`kind: "approve"` の項目を 1 件作る: `evidence ready to merge #42 (feat-csv · HEAD 3f2a1b0)`。マージを決めるのは人で、pekoed がマージするのは人がこの項目に `merge` と答えたときだけ(下の「答え」。人の答えなしの自動マージはしない)。

| 条件(`name`) | 揃っている |
| --- | --- |
| `land` | `pekoe land` が PR を作った(`landing`)、PR が `OPEN` |
| `review` | 最新の `status: review` の報告が `verdict: pass` で(`pending` は揃わない)、その `reviewed_sha` が PR の HEAD(`pr_status.head_sha`)と同じ(短い SHA は前方一致)か、PR の HEAD と差分の中身が同じ(下) |
| `ci` | PR の HEAD のチェックに失敗も実行中もない。チェックが 1 件も無い PR(CI の無いリポジトリ)は「該当なし」で揃う(`detail` は `no CI checks (not required)`)。チェックがあって全部 skip・キャンセルなどで通ったものが 0 件なら揃わない |
| `merge_state` | `pr_status.merge_state` が `CLEAN` |
| `conflicts` | 衝突予報(`overlaps`)に `conflict` がない(自動で合わさる重なりはよい) |
| `after` | `after` のセッションがすべて `landed`。landed になってから `pekoe rm` された相手も揃ったものとして扱う(`<slug> landed and was removed`。消えたときに landed だったかを state.db に残すので、pekoed を再起動しても変わらない)。landed になる前に rm された相手は `is gone` で揃わない。同じ slug で作り直されたセッションがあれば、そのセッションの状態で見る(作り直した方も landed の前に rm されたら `is gone`) |
| `settled` | セッションの作業が済んでいる: エージェントが `running`・`waiting` でない、worktree にコミットしていない変更・追跡していないファイルがない(`git status --porcelain`。読めなければ揃わない)、worktree の HEAD に PR の head(`pr_status.head_sha`)に無いコミットがない(land の後にコミットして push していないもの。`HEAD has 1 commit not in PR #42 (run pekoe land)`。PR の head が手元に無ければ `origin/<branch>` = land が最後に push したものと比べ、それも無ければ見ない。`origin/<base>` に入っているコミットは数えない)、最後の `done`・`blocked` の報告の後にそのセッションへ届いた送信(送信の記録の `kind: send`(人・tapi・ほかのセッションの `pekoe send`)と `kind: inbox`(受信箱の答え・差し戻し)で `ok: true` のもの)がない。報告が無ければ、`send`・`inbox` が 1 件でも届いていれば揃わない。worktree を読むのは `landing` のセッションだけ(ほかは状態だけを見る) |

```jsonc
// MergeCheck(merge.check の要素、InboxItem.facts.merge)
{
  "slug": "feat-csv", "pr": 42, "pr_head": "3f2a…",
  "ready": false,
  "conds": [
    {"name": "land", "ok": true, "detail": "pekoe land done (PR #42)"},
    {"name": "review", "ok": false, "detail": "no independent review reported"},
    {"name": "ci", "ok": true, "detail": "all 5 passed"},
    {"name": "merge_state", "ok": true, "detail": "CLEAN"},
    {"name": "conflicts", "ok": true, "detail": "no conflicts forecast"},
    {"name": "after", "ok": false, "detail": "feat-a is running"},
    {"name": "settled", "ok": false, "detail": "+tapi sent it more (send, 14:02:11) after its last report p468"}
  ],
  "missing": ["review", "after", "settled"],     // 揃っていない条件の name(conds の順)
  "review_stale": false,              // 最新のレビューは pass だが、PR の HEAD がレビューしたものと中身の違う変更になっている(再レビューが要る)。無ければ省く
  "inbox": 0                          // 開いている根拠の項目の id(merge.check だけ。無ければ省く)
}
```

- **review が見る報告**: そのセッションに付いた報告(`report.list {slug}`)だけ。`report.post` は呼び出し元のセッションにしか書けないので、作業セッションの中のサブエージェント(U3 の (a)。窓が同じなのでそのセッションに付く)の合格は数えるが、別のセッション(`pekoe new review/<slug>`。U3 の (b))が出した合格はそのレビューのセッションに付き、数えない。その場合 `review` は `no independent review reported` のまま揃わず、根拠の項目は作られない(人は項目なしでマージできる)。別のセッションのレビューを数えるかは、U3 の (b) を実際に使い始めてから決める(docs/orchestration.md の段階 3 の「実装:」)
- **rebase の後の合格の引き継ぎ**: `pekoe land` は `origin/<base>` に rebase してから push するので、レビューした後に land すると PR の HEAD の SHA が変わる。`reviewed_sha` と PR の HEAD が違うとき、pekoed はレビューしたコミットの変更(base(`origin/<base>`、無ければ `<base>`)との merge-base から)を、PR の HEAD の merge-base に rebase と同じ 3-way マージで載せ直し(`git merge-tree --write-tree`。worktree にも ref にも触らない)、できた tree が PR の HEAD の tree と完全に一致したときだけ合格を引き継ぐ(`detail` は「… は rebase し直しただけで差分の中身が同じ」)。ファイル・バイト・場所のどれかが違えば(同じ行を文脈の同じ別の場所に足したものも)揃わず、`review_stale: true` で「再レビューが要る」と出る。載せ直しが衝突したときは、衝突がすべて「両側が同じ場所に行を足しただけ」(diff3 で base 側が空。同じファイルの末尾に、base とレビューした変更がそれぞれテストを足したなど)で、PR の HEAD がその両方をそのまま(どちらかを先に)残し、ほかに何も変えていないときだけ同じと見なす。両側が同じ行を書き換えた衝突(解決した行はどちらにも無い新しい文になる)・片側がファイルを消した・名前を変えた衝突、衝突したファイルのどれかの版に衝突マーカーに似た行(`<<<<<<<` などで始まる行)があるもの(git の印と見分けられない)は揃わない。PR の HEAD がこの worktree に無い(よそから push された)など比べられないときも揃わず、`detail` に比べられなかった理由が出る(安全側)。結果はコミットの組と、そのときの base(`origin/<base>`)のコミットごとに覚える(読んだ `origin/<base>` が古くて違うと出ても、base が進めば比べ直す)。引き継ぐのは「レビューした変更と同じ変更」であることまでで、新しい base との組み合わせは(CI のあるリポジトリなら)CI が確かめる
- `conds` は上の表の順で常に 7 つ。`detail` は pekoed が書く 1 行(英語)。`review` の `detail` には誰のレビューか(作者のセッションの中のレビューならそう書く。docs/orchestration.md の U3)を入れる
- **判定のログ**: `pekoe land` の後のセッションの判定(揃った、または揃っていない条件と `detail`)は、変わるたびに pekoed.log に 1 行出る(`merge evidence feat-csv: PR #42 HEAD 3f2a1b0: not ready: ci: 1 pending`・`…: evidence ready`)。`merge.check` は landed になったセッションを出さないので、項目が出なかった理由はマージの後はここで読む
- 項目: `options` は `["merge", "wait", "send back"]`、`recommend` はなし。`facts` は他の項目と同じ(`facts.merge` に上の判定)、`links` は PR の URL、`context` は条件ごとの `detail` と、`merge` で走るもの(方法と HEAD)。前の `merge` が失敗していればその理由も。`facts` が変われば(CI が落ちた・HEAD が動いた)他の項目と同じく `rev` が上がり、古い `rev` への答えは断られる
- **作業中のセッション**(`settled`): review と CI が通っても、その後に追加の作業を頼まれた(`pekoe send`)・エージェントがまた動いている・コミットしていない変更がある・land の後のコミットが PR に入っていないセッションの PR は、作業の途中なので頼まない。頼んだ後にそうなれば次の判定(10 秒ごと)で取り消し、それより先に `merge` と答えられても、マージの前の確かめ直し(下の 2)で `not merged: evidence no longer holds: settled: …` と断る
- **取り消し**: 開いている項目は、条件が 1 つでも崩れる・PR の HEAD が動く・セッションが landed になる(答える前に GitHub でマージされた)・消えると、pekoed が `withdrawn` にする(`answer.by: "pekoed"`、`answer.note` に理由)
- **重ねて作らない**: 同じ PR・同じ HEAD には開いている項目を 1 件だけ。人が答えた(閉じた)ものは、HEAD が動くまで作り直さない。pekoed が自分で取り消したもの(条件が崩れた)と、`merge` の答えでのマージが失敗したものは、同じ HEAD で条件が揃えばもう一度作る。最後に作った PR・HEAD・id は state.db の `kv` に残るので、pekoed を再起動しても重ならない
- **答え**: 選択肢を選ぶことでだけ答える(ほかの問いと違い、メモだけの答えは断る)。`merge`(0)は pekoed がマージする(下)。`wait`(1)は記録だけ。`send back`(2)は `note` が必須で、メモをそのセッションに送る: 受信箱の答えと同じ経路(エージェントが `idle` か `done`・許可ダイアログなし・人が 30 秒打っていない、受け付け時と打つ直前の 2 回の確認)で `[pekoe inbox q12] 人が PR #42 のマージを差し戻しました(メモ: …) 詳しくは pekoe --json inbox q12` を 1 行打つ。送信の記録には `from: pekoed, kind: inbox` で残る
- **前の選択肢の項目**: この版より前に作られた項目は 0 番が `merged`(人が GitHub でマージした、の記録)。添字の意味は選択肢の文で決まり、0 番が `merge` の項目だけがマージを走らせる。前の項目に 0 と答えても、state.db に残った答えを読み直しても、何もマージしない
- **`merge` の答え**(token つきの `inbox.answer` だけ。TUI・CLI・Web UI のどれからでも同じ。エージェントは答えられない): 答えを保存するのと同じ変更で `answer.merge` を `running` にして返し、その後ろで pekoed が:
  1. その項目が `kv` に残した、そのセッションの最新の根拠の項目(id・PR・HEAD)であることを確かめる(違えば断る)。`gh` が無い・origin が GitHub のリポジトリを指さない・`[merge] method` が不正でも断る
  2. PR を GitHub から読み直し(`gh api graphql`、`--repo` に当たる owner と name は main のチェックアウトの origin から)、`pr_status` を更新してから 7 つの条件をもう一度判定する。1 つでも崩れている・PR の HEAD が項目の HEAD と違う・PR の base がセッションの base(land が rebase した先)と違えば、マージしない。ただし崩れているのが `merge_state` だけで、GitHub がまだマージできるかを計算している(`mergeStateStatus` が `UNKNOWN`、または無くて `mergeable` が null か `UNKNOWN`。同じ base に別の PR がマージされた直後など)ときは、断らずに 5 秒おきに読み直す(合計 1 分まで)。待っている間は `answer.merge` を `running` のまま、`detail` を `waiting: GitHub is still computing whether PR #42 can merge (merge_state UNKNOWN); …` にして `rev` を上げる。読み直すたびに、項目が最新か・セッションと PR の番号・base・7 つの条件・HEAD をすべて確かめ直す(待っている間に HEAD が動けば `PR HEAD moved`、CI が落ちればその理由で、すぐ断る)。1 分たっても `UNKNOWN` なら `not merged: evidence no longer holds: merge_state: merge_state is UNKNOWN (GitHub was still computing it after 1m0s)` で断る。`UNKNOWN` のほかに崩れた条件がある・`BLOCKED` などのときは待たない。origin の host・owner・name は英数字と `.` `_` `-` だけのものに限る(gh がフラグや `@ファイル` と読むものを渡さない)
  3. `gh pr merge <番号> --repo <host/owner/name> --merge|--squash|--rebase --match-head-commit <項目の HEAD>` を人の gh で、どのリポジトリでもない空のディレクトリで実行する(sandbox の外。上の「pekoed が動かす git」の gh の項。`GH_PROMPT_DISABLED=1`。`--admin`・`--auto`・`--delete-branch` は付けない。`--repo` を付けるので gh は手元のブランチに触らない)。方法はリポジトリの設定(`repos/<repo-id>.toml`)の `[merge] method`(`merge`・`squash`・`rebase`。既定 `merge`)
  4. 結果を `answer.merge` に書いて `rev` を上げ、`inbox.updated` を出す。成功したら PR をもう一度読み、`MERGED` ならその場で後片付け(下の「PR の状態のポーリング」と同じ)を始める
  - 実行は送信の記録に `from: pekoed, to: <slug>, kind: merge` で残る(`text` に実行した gh のコマンドと受信箱の id、`via` は答えた経路、失敗なら `ok: false` と `error`)
  - 失敗(`failed`)はマージ済みとして扱わず、`detail` に理由(`not merged: evidence no longer holds: ci: 1 pending`・`not merged: PR HEAD moved (…)`・`not merged: gh pr merge: <GitHub の言葉>` など)。同じ HEAD で条件が揃っていれば、根拠の項目がもう一度作られる(人はもう一度 `merge` と答えられる)
  - gh が 2 分で終わらなければ止め、`failed` の `detail` を `unknown: …` にする(GitHub がマージしたかは分からない。マージされていればポーリングが landed にする)
  - 実行中(`running`)の `detail` は、GitHub の計算を待っている間だけ入る(上の 2)
  - pekoed が `running` のまま止まったら、次の起動で `failed`(`pekoed stopped during the merge …`)にする。GitHub でマージされていれば、ポーリングがいつもどおり landed にする

```jsonc
// InboxAnswer.merge(pekoed の根拠の項目に merge と答えたときだけ)、MergeCheck.run(merge.check。最後のものを、次に項目を作るまで)
{"state": "failed",          // running | merged | failed(running の間も、GitHub の計算待ちなら detail に waiting: …)
 "pr": 42, "head": "3f2a…",  // --match-head-commit に渡す HEAD(項目のもの)
 "method": "merge",          // merge | squash | rebase(決まる前に失敗したら省く)
 "detail": "not merged: evidence no longer holds: ci: 1 pending",
 "at": "…"}
```

### 片付けの候補(cleanup)

統括役でセッションを回すとたまる、landed・マージ済み・止まって長いセッションを一覧にする(`cleanup.list`、`pekoe cleanup`、TUI の `:cleanup`)。pekoed は一覧を出すだけで、消すのは人が選んだときの `worktree.rm`(`pekoe cleanup --rm <slug>…` / `--rm --landed`、TUI の `D`。どちらも確認を挟む)。安全の線は `pekoe rm` と同じで、token なしで消せるのは worktree の消えた landed だけ(上の「人間だけの操作」)。

```jsonc
// CleanupList(cleanup.list の結果)
{
  "stale_after": "72h0m0s",           // 使った閾値
  "items": [
    {
      "kind": "stale",                // landed | merged | stale(この順に並ぶ)
      "slug": "feat-csv", "repo": "app", "repo_id": "github.com_me_app",
      "branch": "feat/csv", "state": "idle",
      "since": "2026-11-06T09:12:00Z",  // 最後に状態が変わった時刻
      "pr": 42,                       // 無ければ省く
      "path": "/Users/me/worktrees/app/feat-csv",
      "worktree": true,               // ディレクトリが残っている
      "deletes_worktree": true,       // rm がそれを消す(~/worktrees 配下で pekoe が作ったもの)
      "dirty": 2,                     // 未コミットのファイル数(読めなければ -1)
      "unpushed": 1,                  // どのリモートにも base にもマージした PR にもないコミット数(読めなければ -1)
      "unknown": "",                  // 読めなかった理由
      "warnings": ["2 uncommitted files", "1 unpushed commit", "PR #42 is still open"],
      "human_only": true              // token なしの worktree.rm は断る
    }
  ]
}
```

- **閾値**: `[cleanup] stale_after`(Go の duration。既定 72h)。金曜の夕方に done にして月曜の朝に戻る(約 63 時間)セッションは出さず、まる 3 日だれも触っていないものを出す。`stale_after` の引数(`pekoe cleanup --stale 24h`)でその場だけ変えられる
- **`unpushed` の数え方**: `HEAD` から届き、リモート追跡ブランチ(`--remotes`)にも base との merge-base にも無いコミット。GitHub で squash merge されてリモートのブランチが消えたあとでも、PR の head(`pr_status.head_sha`)まではマージ済みとして数えない。base が分からないときはリモートに無いコミットをすべて数える(少なく見せるより多く見せる)
- 自動では何も消さない。pekoed がローカルのブランチ(`git branch`)を消すこともない(`pekoe rm` と同じ)

### 依頼(ledger)

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
  "closed_at": "…",
  "repeats": 1,                              // 同じ文が続けて来てまとめた回数(下の「まとめる」)。0 なら省略
  "auto": true                               // pekoed が自動で chat に閉じた(下の「自動で閉じる」)。status が chat のときだけ。違えば省略
}
```

- **記録**: tapi の UserPromptSubmit hook(`pekoe event`)が tapi の窓から来たとき(上の「呼び出し元の識別」で窓が `+tapi` と分かったとき)に、`prompt` を `open` で記録する。`$PEKOE_SESSION=+tapi` を名乗るだけの呼び出し(窓に当たらない・別のセッションの窓)は tapi の状態は動かしても依頼としては記録しない(依頼の文は tapi が「人に頼まれたこと」として読むため)。人が打ったのではないもの(pekoed 自身が tapi の入力欄に打った `[pekoe` で始まる受信箱の答え、Claude Code がバックグラウンドのタスク(tapi の `pekoe wait` など)の完了を新しいターンとして渡す `<task-notification>` で始まる文)と空の入力は入れない。どちらも前後の空白を除いた先頭で見て、人が打った文の途中に出てくるだけなら記録する。保存の前に ANSI・改行とタブ以外の制御文字・見えない書式文字を落とす。hooks の無いエージェント(codex など)の tapi では記録されない
- **まとめる**: 直前の依頼(id が最大のもの)が `open` で、前後の空白を除いて同じ文が、その `at` から 1 分以内に来たら、新しい依頼を作らずにその依頼の `repeats` を 1 増やす(`ledger.updated` が出る)。閉じた依頼にはまとめない
- **自動で閉じる**: tapi の窓から Stop hook が来たとき(tapi が返事を終えたとき)に、そのターンの入力を `chat` で閉じて `auto: true` を付ける。条件は次のすべて。外れたものは `open` のまま残る
  - そのターンを始めた UserPromptSubmit で記録した(またはそのターンの中でまとめた)1 件であること。前のターンからある依頼、Stop が来なかったターン(中断・会話の終了や開始(SessionEnd / SessionStart。会話の圧縮の後の SessionStart も)・pekoed の再起動・`tapi.reset`)の入力、ターンの途中で次の入力が来たときの前の入力、記録しない入力(受信箱の答え・タスクの完了通知)で始まったターンは対象にならない
  - `open` で、`sessions` が空であること
  - そのターンの間(UserPromptSubmit から Stop まで。前のターンが Stop なしで終わっていれば、そこから)に、tapi の窓から `session.new`・`session.send`・`inbox.post`・`land`・`wait` が呼ばれていないこと(断られた呼び出しも数える。tapi が誰かに何かを頼んだ・待ち始めたターンの入力は依頼として残す。待ちを張り直したターンの雑談も残るので、それは tapi が `--as chat` で閉じる。ターンの外の呼び出しは数えない。`pekoe ask-human --wait` が 1 分ごとに呼び直す `wait` は、CLI が `rewait: true` の印を付け、ターンの中に来ても数えない(バックグラウンドに置いたものが、たまたまターンに重なることがあるため)。最初の 1 回の `wait` は数える。印を付けない古い CLI の呼び直しは今までどおり数える)
  - `ledger.keep` で残されていないこと。`keep` が効くのは、そのとき「このターンの入力」だった 1 件だけ(Stop なしで終わったターンで `keep` したものは残り、次のターンの入力には及ばない)
  - そのターンの間に、人が `ledger.reopen` でその入力を開き直していないこと(人が「依頼だ」と戻したものを、pekoed の推測で閉じ直さない)
- **Stop hook の差し戻し**: Claude Code は Stop hook を並べて走らせるので、ほかの Stop hook が止めを差し戻した(exit 2・`decision: "block"`)ターンでも pekoe の Stop は届き、上の条件でそのターンの入力を閉じる。その後、次の UserPromptSubmit より前に tapi の窓から PreToolUse が来たら、ターンが続いたと見なし、Stop の前の続きとして扱う: その間の `session.new` などの呼び出しは数え、閉じた入力を `open` に戻す(`chat` で `auto` のままのときだけ)。`ledger.keep`(`id` なし)もその入力に効く。続いたターンの終わりの Stop(`stop_hook_active: true`)で、もう一度上の条件で決める(何も頼まなければ `chat` のまま)
- **残す・戻す**: `ledger.keep`(`pekoe ledger keep [<id>]`)。`id` なしはこのターンの入力を閉じる対象から外す(依頼なのにこのターンで誰にも頼まないとき。このターンの入力が無ければ断る)。`id` つきは、pekoed が自動で閉じたもの(`chat` で `auto`)を `open` に戻す(`closed_at` と `auto` を消す)。`session.new` の `request` と `ledger.link` も、自動で閉じたものなら `open` に戻して結ぶ。tapi か人が閉じたもの(`auto` でないもの)はどれも断る
- **自動で閉じたものを見せる**: 自動で閉じるのは pekoed の推測なので、閉じてから 24 時間(`ledgerAutoShown`。定数で、設定にはしていない)は、open のものと一緒に出す。`ledger.list` の `auto: true` がそれで、`pekoe ledger` / `pekoe --json ledger`(tapi が会話の最初に読む)と TUI のダッシュボードの Requests が使う。出すのは新しい 10 件(`ledgerAutoMax`)だけで、本文は 200 文字(`ledgerAutoText`)で切り、残りは件数だけ(tapi は会話の最初と依頼の id を調べるたびにこれを読み、雑談を閉じるのはトークンを減らすため)。全文は `pekoe ledger <id>`(1 件表示。`ledger.list` の `status: all`・`since: id-1` で読む)。`pekoe ledger` は open のものの後に `closed by pekoe as no request in the last 24h (N)` の見出しで分けて出し(切ったものは見出しの行に `cut (pekoe ledger <id>)`、残りは `+N older`)、`--json` は `LedgerView` をそのまま出す(`--all` は今までどおり配列)。古い pekoed は配列を返すので、CLI とダッシュボードは open だけとして読む。ダッシュボードは open の依頼の下に薄い色で出し、残りを `+N older` で出す。`Tapi.ledger_open`・Overview の件数・Web UI は open だけのまま。24 時間を過ぎたものは `pekoe ledger --all` で見える。`keep` の打ち忘れで閉じた依頼を、次の tapi(リセットの後も)と人が見つけて戻すためのもの
- **閉じる**: tapi が `ledger.close`(`pekoe ledger done <id> --evidence …`・`drop <id> --note …`・`close <id> --as chat`)。`evidence` は `report.post` と同じ上限(20 件・各 500 文字、1 行)。`reopen` の前から残っている `evidence` も合わせて 20 件までで、新しく足す分で超える呼び出しは断る(`… has N evidence entries already … give at most M new ones, or none`)。足す分が無ければ(`evidence` なし・もうあるものだけ)いつでも閉じられる。もうあるものと同じ文字列、同じ呼び出しの中で重なったものは 1 つにする。`note` は 4000 文字まで。`open` 以外は断る(`… is done already (a person can reopen it …)`。自動で閉じたものには `pekoe ledger keep <id>` で戻せることを添える)
- **結ぶ**: `session.new` の `request`(`pekoe new … --request <id>`)か `ledger.link`。どちらも `Session.request` を入れ、依頼の `sessions` に slug を足す(同じものは 1 回)。`request` の依頼が無い・閉じているとき(自動で閉じたものを除く)は、セッションを作らずに断る
- **人だけ**: `ledger.reopen`(閉じたものを `open` に戻す。`closed_at` と `auto` を消し、`note` と `evidence` は残す。tapi のいまのターンの入力なら、そのターンの終わりに自動で閉じない)・`ledger.delete`。tapi が自分の取りこぼしを消したり、自分や人が閉じたものを戻したりできないように(tapi が戻せるのは、pekoed が推測で閉じた `auto` のものだけ)
- token なしでは `ledger.*` はすべて tapi の窓からだけ(main セッションのハブは依頼(ledger)を使わない。docs/orchestration.md の U13)
- 依頼か受信箱が変わると `tapi.updated` も出る(`Tapi.ledger_open` / `inbox_open`)

### 集計(stats)

マージした PR と使ったトークンを、ローカルの日付・リポジトリごとに出す(`pekoe stats [--days N] [--repo R] [--json]`)。セッションを rm した後も残る。

```jsonc
// Stats
{
  "from": "2026-11-05", "to": "2026-11-11",   // 数えた最初の日と今日(pekoed のローカルの日付)
  "rows": [                                   // 新しい日から、同じ日はリポジトリ名の順
    {"day": "2026-11-11", "repo": "pekoe", "merged": 2,
     "tokens": {"input": 1200, "output": 85000, "cache_creation": 410000, "cache_read": 9800000}},
    {"day": "2026-11-11", "repo": "", "merged": 0, "tokens": {…}}   // repo "" は tapi chat
  ],
  "merges": [                                 // 範囲内のマージ、古い順
    {"day": "2026-11-11", "slug": "fix-csv", "repo": "pekoe", "pr": 42, "merged_at": "…",
     "tokens": {…}}                           // そのセッションが作られてから使った分すべて(範囲の外の日も)
  ],
  "merged": 2, "tokens": {…}                  // rows の合計
}
```

- **トークン**: hooks(PostToolUse・Stop。tapi chat も)が渡す `transcript_path` の Claude Code の transcript から、assistant の行の `message.usage` を種類別(`input_tokens`・`output_tokens`・`cache_creation_input_tokens`・`cache_read_input_tokens`)に足す。Claude Code は 1 つのメッセージを content block ごとに複数行に書き、どの行にも同じ usage が付くので、同じ `message.id` が続く行は 1 回だけ数える。`claude --continue`(tapi chat の再起動 `tapi.restart`、resume できないセッションの再起動)は新しい transcript を作り、それまでの会話を同じ `message.id`・同じ usage・元の `timestamp` のまま写すので、数えた `message.id` を生(slug と作成時刻)ごとに state.db の `usage_ids` に覚え、同じ生で一度数えた id の行は、どの transcript にあっても数えない(セッションを消すと `usage_cursors` と一緒に消す)。id にはそれを最後に見た transcript を添え、写しで同じ id を見たら写しの方に付け替える(transcript は更新時刻の古い順に読むので、写しは元の transcript の後に読む。元に再起動の前に読み残した行があっても、その id は写しの方に付き、元が消えても忘れない)。サブエージェントの transcript(`<transcript から .jsonl を除いたもの>/subagents/*.jsonl`)も同じセッションに足す。行はその `timestamp` のローカルの日付に入るので、日付をまたいで動いたセッションは両方の日に出る。どこまで読んだか(バイト位置と最後のメッセージの id)を state.db の `usage_cursors` に、日ごとの和を `usage_days` に残し、次は増えた行だけを読む(1 回に 32 MiB まで、書きかけの最後の行は次に回す)。読んだことのある transcript がディスクから消えていたら(Claude Code の古い会話の掃除、`tapi.reset`)、その読み位置と、最後にそこで見た id を消す(数えるたびに開けないファイルを試し、その `subagents/` を探し直さないように)。消すのは、ほかの transcript に新しい行が無かった回(写しを最後まで読み終えた後。1 回に 32 MiB までしか読まないので、大きな写しは何回かに分けて読む)だけなので、消えたものの写しにある id は写しの方に付け替わって残り、写しをさらに `--continue` で写しても二重には数えない。その生の transcript が 1 つも残らなければ(`tapi.reset` の後など)、id はすべて消す。tapi chat は生が 1 つ(リセットをまたいで `+tapi` に足す)でセッションのように rm で消えないので、こうしないと読み位置と id が増え続ける。`stats` を呼ぶと、動いているセッションと tapi の分をその時点まで読んでから数える。`Session.tokens`(直前の文脈の大きさ)とは別の数で、そちらの意味は変えていない
- **マージ**: pekoe が landed にしたとき(PR の状態のポーリングか `watchMerges` が PR のマージを見て teardown するとき、origin のないリポジトリの land)に `session_log` に記録する。マージの日時は pekoed がマージを見た時刻(PR の状態のポーリングが PR ごとに 60 秒に 1 回なので、1 分強遅れることがある。pekoed が止まっていた間のマージは、起動直後の `watchMerges` が見た時刻)。作業中で後片付けを待った(`land_held`)セッションは、待ちが解けた時刻ではなく最初にマージを見た時刻(`land_held_at`)。GitHub に問い合わせて過去のマージを遡ることはしない(pekoe が見届けたものだけ)。他人の PR のレビュー(`pekoe review gh#N`)は数えない
- **残す記録**: `session_log` は slug・リポジトリ(名前と repo-id)・ブランチ・PR 番号・作成日時・マージ日時・終了日時(landed になった時刻。land せずに rm したら rm の時刻)。セッションが landed になったときと rm されたとき(`worktree.rm`・landed の slug を `session.new` で置き換えたとき)に書き、rm の前に transcript の残りを読む。トークンの累計は `usage_days` の和。セッションの 1 生は slug と作成日時で区別するので、同じ slug を後で使い直しても別の記録になる。rm では読み位置(`usage_cursors`)だけを消し、日ごとの和と記録は残す
- **token なしで読める**: 数・日付・リポジトリ名・slug・PR 番号だけで、エージェントが書いた文を含まない(`session.list` で見えるものより少ない)。読む transcript は hooks が渡したもの(`readTokens` が既に読んでいるもの)の増えた行だけで、新しく何かを動かしたり、ほかのファイルを読んだりはしない。tapi chat が「今日いくつマージした? どれくらい使った?」に答えるために要る
- **数の確かさ**: `transcript_path` は hooks の payload の値で、`session.event` は token なしで誰でも送れる。ほかのファイルを名乗られるとそのセッションの数が増えうる(通常のファイルで `.jsonl` で終わるものだけを読み、中身は数として足すだけ)。重複は同じ生の中の `message.id` で除くので、`--continue` や resume で履歴を別の transcript に写しても二重には数えない。ただし `usage_ids` が無かった版(この表を足す前)で数えた生は、数えた id の記録が無いので、その後に写された分を一度だけ二重に数えうる。id に transcript を添える前の版で覚えた id は、どの transcript のものか分からないので、その生の transcript が 1 つも残らなくなるまで消さない。写しの元が消えずに写しの方だけが消え、その後に元をまた写したときは、元の id はもう覚えていないので二重に数えうる(`--continue` はいちばん新しい会話を写し、Claude Code は古い会話から消すので、ふつうは起きない)。`message.id` の無い行は除けない。hooks の無いエージェント(codex など)のトークンは数えられない。料金への換算はしない

### Tapi オブジェクト

tapi chat(TUI の `t`、docs/tapi-chat.md)。統括役のエージェントで、セッションではないので `session.*` には出てこない(`session.list` に載らず、tapi 自身の状態は state.db にも保存しない(依頼は上の「依頼(ledger)」)。Web UI は `GET /api/tapi` で状態・最後の返事・件数を、`tapi.transcript` で会話を読む。送るのは docs/web.md の「tapi chat の扱い」の確認を経たものだけ)。tmux の窓は `+tapi~agent`、hook は `$PEKOE_SESSION=+tapi`(`+` はセッションの slug に現れない)か、cwd が tapi の稼働ディレクトリの中なら tapi に振り分ける。

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
  "ledger_open": 1,                        // open の依頼の件数
  "deliver": {                             // tapi への配送([orchestrator] deliver。下)。off のときは {"mode": "off"} だけ
    "mode": "idle",                        // off | idle
    "pending": 2,                          // 次の 1 行に入る変化と受信箱の答えの数
    "held": "typing",                      // pending があるのに送っていない理由(最後に試したとき): stopped | busy | prompt | typing | chat | interval
    "last_at": "…",                        // 最後に打った時刻
    "last_error": ""                       // 最後に打てなかった理由
  },
  "outside": {                             // tapi の会話が tapi の窓の外で動いている(tapi を起動し直すまで。無ければ省く)
    "claude_session_id": "…", "window": "", // window: 動いている pekoe の窓(空なら窓の外)
    "claimed": "pekoe-theme", "since": "…"  // claimed: その hooks の $PEKOE_SESSION
  }
}
```

- tapi は統括役(docs/orchestration.md「tapi を統括役にする」)。pekoed が稼働ディレクトリに書く `CLAUDE.md` / `AGENTS.md` と `--append-system-prompt` の人格に、自分では編集せずセッションに任せること・会話の最初に `pekoe --json ledger`・`inbox`・`ls` を読むことを書き、`CLAUDE.md` の末尾に起動した時点の依頼・受信箱・任せたセッションの件数(中身は書かない)を入れる
- Claude Code の tapi には、セッションと同じ `--settings` に加えて、`--add-dir` で読ませている各リポジトリと `~/worktrees`(まだ無くても。シンボリックリンクを解いたものも)を `sandbox.filesystem.denyWrite` と `permissions.deny` の `Edit(...)` に入れる。Bash でも Edit / Write でも書けない。稼働ディレクトリは書ける。`[agents.<tapi の agent>] sandbox = false` ならこれも渡さない(ほかのエージェントにも渡さない)
- `tapi.reset` は会話だけを消し、依頼・受信箱・報告・送信の記録(state.db)には触らない

#### tapi への配送

`[orchestrator] deliver = "idle"`(既定は `"off"`)のとき、pekoed は tapi に起きたことを、tapi の手が空いたときに入力欄へ 1 行で打つ。`pekoe wait` を使わないエージェントを tapi にしたときや、tapi が wait を張り直し損ねたときにも報告が届くように。off では何もしない(off の間に起きたことを後で on にしても送らない。config.toml を書き換えて on にしたときも、読み直した時点より前のものは送らない)。

- 送るもの: tapi が任せたセッション(`parent` が `+tapi`)の `report`・`failed`・`waiting`・`ci`(`wait --mine --on report,failed,waiting,ci` が返すもの)と、tapi が出した問いの答え・答えずに閉じたもの(`delivery: pending` のもの)
- 送らないもの: tapi の `wait` が返したもの(待たずに `--since` で読んだものも含む)と、今ブロックしている tapi の `wait` が受け取るもの(その wait が tapi を起こすので、送ると二重に起きる)。受信箱の答えは、tapi が wait で待っていれば答えた時点で `waited` になり送らない。wait が受け取らない種類(`--on report` だけで待っている間の `failed` など)と、その wait が返す範囲より前の変化(`--since` なしで張った wait なら、張る前に起きたもの)は送る
- 打つ条件(すべて): tapi の窓があり、エージェントが動いている(終了して tmux が窓だけ残していたら `held: stopped`。死んだ pane への send-keys は成功してしまい、届けた扱いで失われるため)、tapi が `idle`、許可ダイアログが出ていない(hooks の状態と画面。受け付けた時と Enter の直前の 2 回。`session.send` の token なしの経路と同じ)、人が tapi chat に打っていない(`tapi.send`・`tapi.keys`・ローカル GUI の Interrupt の `tapi.interrupt` から 30 秒、`tapi.deliver_pause` の間。人の `tapi.send` と同じ送信の排他の中で、打つ直前にもう一度確かめる)、前に打ってから 30 秒以上。試すのは tapi が `idle` に入ったとき・5 秒ごと・`tapi.deliver_pause` が off になったとき
- 本文は 1 行: `[pekoe] 3 件の変化(feat-csv report done、fix-ci ci red、受信箱 q12 に答え(メモ: 画像を送りたい))。詳しくは pekoe --json wait --mine --since <epoch>-<seq>、pekoe --json inbox q12`。書くのは pekoed の言葉(slug・種類・報告の status・CI の green / red)と、受信箱の答えのメモの先頭(人の文。80 文字で切り、1 行に均す。tapi は信頼できないデータとして読み、全体は `pekoe --json inbox <id>` で読む)だけで、エージェントの要約や CI のチェック名は入れない。先頭は `[pekoe]`(数字にしない。依頼の記録にも入らない)。名前を挙げるのは 8 件までで、残りは `ほか N 件`。`--since` は 1 行に入れた最初の変化の直前の cursor で、受信箱の答えが 2 件以上なら `pekoe --json inbox --mine --all`
- 打ったものは送信の記録に `from: pekoed, to: +tapi, kind: deliver` で残る。Enter の直前に許可ダイアログが出た(文字は入力欄に残った)ときは打ち直さず、`ok: false` で残し、送ったものとして扱う(受信箱の答えと同じ)
- 状態は `Tapi.deliver`。`pekoe tapi` は on のとき `deliver: idle · 2 pending (held: typing) · last 21:37:30` の行を足す

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

PR の状態のポーリング(`watchPRStatus`。`OPEN` の PR を PR ごとに 60 秒(`[pr] poll_every`。下限 30 秒)に 1 回。`pr.view` も同じ扱い)が `landing` のセッション(pekoe land が PR を作った)の PR を `MERGED` と見たら、その回で landed にする後片付け(窓を閉じ、窓が残したプロセスを止め(design.md「片付けで止めるプロセス」)、teardown と worktree の削除。後の 2 つは pekoe が `~/worktrees` 配下に作った worktree のときだけ。land は pekoe が作ったセッションしか受けない)を別のゴルーチンで始める。マージから landed までの遅れは 1 分強まで(15 秒ごとに見て、PR ごとに 60 秒空ける)。`MERGED` になった PR は次から取りに行かないので、取りこぼした分(pekoed が止まっていた間のマージなど)は `watchMerges`(`landing` のセッションごとに `gh pr view <番号> --json state` を 1 回。pekoed の起動直後に 1 回と、その後 2 分ごと)が拾う。両方から同時に始まっても、後片付けはセッションごとに 1 回しか走らない。ただし、マージを見たときにセッションがまだ作業中(エージェントが `running`・`waiting`、または worktree にコミットしていない変更・追跡していないファイルがある、または HEAD にマージされた PR の head に無いコミットがある(マージの後にコミットした続き。比べ方は `merge.check` の `settled` と同じ)。git が読めないときも)なら、後片付けをしない: エージェントの窓を閉じず、teardown も worktree の削除もせず、`pr_status.state` を `MERGED` にして `landing` のまま残し、`land_held` に理由(`PR #42 merged while the session is still at work (agent is running; …): window and worktree kept; …`)を、`land_held_at` に最初に見た時刻を書き、`wait` に `kind: pr` の変化を 1 回出す(pekoed のログにも残す)。エージェントが `idle`・`done` になったとき、と `watchMerges`(2 分ごと。`land_held` のあるものは gh を呼ばずに)のたびに確かめ直し、作業中でなくなったら後片付けをして landed にする(`land_held` は消える)。マージ後の変更は人が扱う(別の PR にする・捨てる)。PR に入っていないコミットは、エージェントが手を止めても(`done` で人に問いを出した、など)worktree ごと消さず、`land_held` に `HEAD has 1 commit not in the merged PR #42 (pekoe land them as a new PR, or drop them)` と出して待つ(比べる PR の head はポーリングが最後に読んだものなので、その head に無いコミットがあるときは、マージを見るたびに GitHub から PR の head を読み直してから比べる。同じ PR への 2 回目の land の後、読み直す前にマージされても、push 済みのコミットで止まらない)。`pekoe land` すれば新しい PR になり(`land_held` は消える)、HEAD から外せば(捨てる)次の確かめ直しで landed になる。origin のないリポジトリで land がその場で後片付けするときは、land を呼んだエージェント自身が動いているので、これに当たらない。`landing` でないセッション(pekoe land を通さずにマージされたもの)は `pr_status.state` が `MERGED` になるだけで、後片付けはしない(`cleanup.list` の `merged` の候補になる)。

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
| `ledger.updated` | `LedgerEntry`(記録した・まとめた・閉じた(自動も)・結んだ・開き直した。token つきの購読にだけ届く) |
| `ledger.removed` | `{id}`(人が消した。token つきの購読にだけ届く) |
| `review.updated` | `{slug}` |
| `review.answer` | `{slug, hunk_id, text}` |
| `pick.received` | `Pick` |
| `preview.updated` | `Preview`(撮影開始・完了・失敗のたび。`rev` の小さいものは古い) |
| `browser.updated` | `[tabs]` |
| `error.updated` | `{report: ErrorReport, fresh}`(`fresh` は新規か、送信・無視後の再発。回数の更新や送信・無視では false) |
| `editor.open` | `{path, line, note, slug}` |
| `tapi.updated` | `Tapi`(受信箱・依頼の件数が変わったときも) |
| `tapi.output` | `{}`(tapi chat の画面更新の合図。30ms で間引く) |
| `term.passthrough` | `{slug, seq}`(OSC 52 / OSC 9 / 777 など、外側の端末にそのまま流すシーケンス) |
| `voice.updated` | `VoiceStatus`(鳴り始め・鳴り終わり・ミュート・backend の切り替え・エラー。変わったときだけ。token つきの購読にだけ届く) |
| `voice.logged` | `voice.log` の 1 件(ログに書くたび。token つきの購読にだけ届く) |
| `config.reloaded` | `{error?, restart?: [name]}`(config.toml が変わって読み直した。`error` があれば読めず、前の設定のまま。ファイルの中身は載せない。token つきの購読にだけ届く) |

### 音声(voice)

docs/voice.md の pekoe voice。`voice.*` はすべて人間だけ(エージェントが Mac を喋らせられないように)。`[voice] enable = false`(既定)の間、pekoed は backend を探さず・呼ばず、`afplay` も呼ばない。

`VoiceStatus`:

```jsonc
{
  "enabled": true,                 // [voice] enable
  "supported": true,               // macOS だけ true
  "backend": "say", "using": "say",  // 設定の名前 / 今使っているもの(fallback 中は fallback の名前、使えなければ "")
  "error": "",                     // 何も喋れない理由: どの backend も使えない、または合成に続けて失敗して止めている(Q11)
  "paused_until": "2026-11-23T15:31:00+09:00", // Q11 で合成を止めている間だけ: いつまで
  "backend_error": "",             // 設定の backend を使っていない理由(fallback 中など)
  "muted_until": "forever",        // "" はミュートなし、"forever" は unmute まで、それ以外は RFC3339
  "focus": "feat-x",               // 最後に voice.focus を送ったクライアントが [4] に出しているもの
  "queue": 2,
  "speaking": {"slug": "feat-x", "kind": "wait", "text": "csv export、確認待ちだよ"} // 無ければ null
}
```

`voice.log` の 1 件: `{time, slug?, kind, text, result, reason?, latency_ms, backend?}`(docs/voice.md 14.4)。ファイルは `~/.local/share/pekoe/voice.log`(0600、1MB で `voice.log.1` に回す)。

`session.event` の `hook: "MessageDisplay"` は受け付けて何もせずに `null` を返す(read で使う。P3)。

### config.toml の再読み込み

pekoed は 1 秒ごとに `~/.config/pekoe/config.toml` を読み、中身が前と違えば再起動なしで読み直して `config.reloaded` を出す。書くのは `pekoe config edit`(pekoed を通さない。エージェントの sandbox からは `~/.config/pekoe` を読み書きできず、RPC で書けるようにするとその抜け道になる)か人の手で、pekoed は読むだけ。読んだ設定を返すのも人間だけの `config.show`(ローカル GUI の Settings)だけ。TUI は `config.reloaded` を受けて自分でファイルを読み直す(テーマ・アイコン・マスコット・TIPS・アニメーションを作り直し、ほかは使うたびに読む)。

- すぐ効く: `[theme]` `[mascot]` `[tips]` `animate` `[picks]` `[orchestrator]` `[notify]` `[cleanup]` `[pr]`(次のポーリングから) `[keys]` `[review]` `[agents.*]` `[tapi]`(次に tapi chat を始めるとき) `[ports]`(次に割り当てるポートから) `[voice]`(変わったら作り直す: 鳴っているものを止め、backend を確かめ直し、今の状態を基準に取る)
- 再起動まで効かない(`config.RestartNeeded`): `[http] port`(待ち受け)・`[web]`(tsnet と sandbox の拒否リスト)・`[sandbox]`(hooks と git の包み方)。pekoed は起動したときの値のまま動き、変わった名前を `config.reloaded` の `restart` と `daemon.status` の `config_restart` に出す(`pekoe daemon status` と TUI のメッセージ行にも)。元の値に戻せば消える
- 読めない(構文エラー・型の違い): 前の設定のまま動き続け、`error` 付きの `config.reloaded` を出し、ログに理由を書く。TUI はファイルを自分で読んで理由をメッセージ行に出す。ファイルが消えたら既定値に戻る

## 2. HTTP(Chrome 拡張)

`http://127.0.0.1:<port>`(既定 47777、`~/.config/pekoe/config.toml` の `[http] port`)。

起動時に bind できなければ(ほかの pekoed が同じ port を使っているなど)拡張なしで動き続け、ログに理由を書く。そのとき `daemon.status` は `http_port: 0` と `http_error`、`ext.info` は断る(設定の番号を出すと、拡張がその番号で別の pekoed につながってしまう)。

- すべてのリクエストに `Authorization: Bearer <token>` が必要。token は初回起動時に `~/.local/share/pekoe/http-token` に生成される(`pekoe ext` で port と token を表示)
- `Origin` ヘッダがあり、`chrome-extension://` で始まらないリクエストは 403。例外として、`Origin` の無いリクエスト(curl などブラウザ以外)は token の確認だけで通す(ブラウザのページからの要求には `Origin` が付くので断れる。token が要ることは変わらない)
- 一方向。拡張は「その場で POST、送ったら終わり」

| path | body | 処理 |
| --- | --- | --- |
| `POST /pick` | `Pick` 入力(下記) | 保存して URL のポートからセッションを解決。指示つきで解決できれば即 pty へ。できなければ TUI に「どのセッションに送る?」 |
| `POST /console` | `{url, errors: [{message, source?, line?, stack?, at}]}` | Browser パネルにエラー数、`E` でセッションに送れる。ポートの持ち主のセッションがあれば、スタックかスクリプト位置のあるものはエラーカード(`error.*`)にもなる。localhost のページなら何でも送れるので上限がある: 本文 1 MiB、1 回 50 件(超えた分は捨てる)、message 2000 文字・stack 8000 文字・source / at / url 500 文字で切る、1 ポートにつき 1 分 120 件(超えた分は捨てる)。拡張も中継の前に同じ件数と長さに切る |
| `POST /tabs` | `{tabs: [{url, title}]}` | 開いている localhost タブの一覧 |
| `GET /health` | — | `{ok: true, version}`(token 必須) |

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
| `GET /api/sessions/{slug}/screen` | — | `session.screen` の最後 80 行。`{slug, rows, lines, dead}` で、`rows` は色つき、`lines` は同じ行のテキストだけ([web.md](web.md)) |
| `POST /api/sessions/{slug}/keys` | `{keys}` | `session.keys`。質問待ちのときだけ、決まったキーを 1〜8 個 |
| `POST /api/sessions/{slug}/answer` | `{answer: "allow"\|"deny", tool}` | `session.answer`。`always` は 400(ローカル GUI だけは受け付ける。下の「4. HTTP(ローカル GUI、127.0.0.1)」)、待っていない・質問・プロンプトが変わったら 409 |
| `POST /api/sessions/{slug}/send` | `{text}` | `session.send`(`submit: true`)。答えは `{ok: true, images, text}`(`text` は画像の行を足した打った文。docs/web.md の表)。allow / deny で答える許可待ちの間は 409。改行・タブ以外の制御文字を含むと 400 |
| `GET /api/inbox` | — | `inbox.list`(未回答、古い順)。`{items: [InboxItem], merges: [InboxItem], at}`。`merges` は人が `merge` と答えた根拠の項目のうち、24 時間以内に変わったもの([web.md](web.md)) |
| `POST /api/inbox/{id}/answer` | `{choice?, note?, rev}` | `inbox.answer`(`answer.via` は `web:<端末名>`)。`rev` が今のものと違う・答え済みなら 409、無い id は 404、選び方・メモの誤り(選択肢もメモも無い、など)は 400。`choice` を省いてメモだけで答えられる(pekoed の根拠の項目は除く) |
| `GET /api/tapi` | — | `tapi.get` の表示用(状態・待ち時間・最後の返事・受信箱と依頼の件数。許可待ちのツール・Claude のセッション ID・稼働ディレクトリは出さない) |
| `GET /api/tapi/screen` | — | `tapi.screen`(セッションの `/screen` と同じ形)。409: tapi が起動していない |
| `GET /api/tapi/transcript?after=<cursor>` | — | `tapi.transcript`(吹き出しの差分)。404: transcript が無い / 409: tapi が起動していない |
| `POST /api/tapi/send` | `{text}` | `tapi.send`(`submit: true`)。`allow_devices` が無ければ 403 |
| `POST /api/tapi/keys` | `{keys}` | `tapi.keys`。tapi が質問待ちのときだけ |
| `GET /api/overview` | — | 件数と、セッションごとの工程(`impl → review → PR → CI → merge`)を依頼元・依頼でまとめたもの。足りない条件は `merge.check` から(読むだけ) |

POST には `X-Pekoe-Web: 1` と `Content-Type: application/json` が要る(CSRF 対策)。

## 4. HTTP(ローカル GUI、127.0.0.1)

`pekoe gui`(`gui.open`)が初めて呼ばれたときだけ、`127.0.0.1:<port>` で待ち受ける(既定のビルドにも入っている。`::1`・`0.0.0.0` には開かない)。パス・中身は上の「3. HTTPS」と同じハンドラで、違うのは入口の確認(`internal/daemon/gui.go` の `guiGuard`)と次の点だけ。設計と脅威モデルは [gui.md](gui.md)。

| method / path | body | 処理 |
| --- | --- | --- |
| `POST /api/gui/session` | `{token}` | 一度きりのトークンを、セッションのトークン(32 バイト、pekoed のメモリにだけ持つ)に替える。`{token}` を返す。無い・失効した(60 秒)トークンは 401。**使用済みのトークン**がもう一度来たら、誰かが人より先に使ったとみなしてセッションのトークンを全部失効させ、401 とログに残す |
| 上の「3. HTTPS」の `/api/*` | | 同じもの。ただし `GET /api/tapi` の `sendable` は常に `true`、`confirm_send` は常に `false`(TUI と同じく確認なしで送れる。`allow_devices` は見ない)。`GET /api/me` は `{login: "local", device: "gui"}`。送信の記録と受信箱の答えの `via` は `gui`。`POST /api/sessions/{slug}/answer` は `always` も受け付ける(TUI の `A` と同じ。「always」という語だけで、`a`・`A`・`y` などのキーの別名は 400)。`/` はデスクトップ向けのページ(`webui/gui/`) |
| ローカル GUI だけの `/api/*` | | Dashboard の集計と台帳(`/api/stats`・`/api/ledger`)、New session(issue から作るものを含む)・Interrupt・tapi の Reset と Start(`/api/repos`・`/api/repos/{repo_id}/issues`・`…/issues/{n}`・`POST /api/sessions`・`…/interrupt`・`/api/tapi/reset`・`/api/tapi/start`)、セッションの吹き出しと送信欄の補助(`GET /api/sessions/{slug}/transcript`・`…/commands`・`…/files`)。サイドバーの Worktrees(`GET /api/worktrees`。`repo.list` と `worktree.list`)、Settings の設定の読み出し(`GET /api/config`)。どれも人として(GUI の Caller)上の人間だけのメソッドを呼ぶ。形は docs/web.md の API の表 |

すべてのリクエストで確かめること(違えば理由を `pekoed.log` に `gui: <code> <method> <path>: <理由>`):

- 接続元がループバックでなければ 403
- `Host` が `127.0.0.1:<port>` と完全一致でなければ 421(`localhost:<port>` も断る。DNS rebinding)
- `Origin` があれば `http://127.0.0.1:<port>` と完全一致でなければ 403。`/api/*` は `Sec-Fetch-Site` があれば `same-origin` でなければ 403
- GET・HEAD・POST 以外(`OPTIONS` を含む)は 403。`Access-Control-Allow-*` は返さない
- `/api/*` は(`POST /api/gui/session` を除き)`Authorization: Bearer <セッションのトークン>` が要る(定数時間で比べる)。無い・違う・失効した(`[gui] session_ttl`、人が最後に操作(サインインかそのトークンでの POST)してから。既定 12h。GET(画面の更新)では延びない。使われ続けても作ってから 24 時間(`session_ttl` のほうが長ければそれ)で失効する)ものは 401。静的ファイル(`/`(デスクトップ向けの `gui/index.html`)・`gui/`・`lib/`・`app.css`・`theme.css`・アイコン)はトークンなしで返す
- POST は `Content-Type: application/json` が要る(415)。`/api/` の外への POST は 403
- 応答には Web UI と同じ CSP・`X-Frame-Options: DENY`・`nosniff`・`Referrer-Policy: no-referrer`、`/api/*` には `Cache-Control: no-store`

トークンはどれも pekoed のメモリにだけあるので、pekoed の再起動で全部失効する(開いていたタブは `pekoe gui` で入り直す)。起動時に前の pekoed が残した `open-*.html` を消す。

## 5. MCP(エージェント → nvim)

`pekoe mcp` が stdio の MCP サーバとして動き、デーモンに中継する。`claude mcp add pekoe -- pekoe mcp` で登録する。ツールは `open_in_editor(path, line, note?)` と `session_list()`。
