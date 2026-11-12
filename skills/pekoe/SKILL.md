---
name: pekoe
description: pekoe で worktree とエージェントセッションを作り、変更作業を任せる。pekoe を使っているリポジトリの main 側(cwd がリポジトリ本体のチェックアウトで、linked worktree ではない)や tapi chat($PEKOE_SESSION が +tapi)でコードやドキュメントの変更を頼まれたら(「〜を直して」「〜を足して」「〜を変えて」「issue #N をやって」など)、ユーザーが pekoe や別ブランチに触れていなくても、直接編集せずにこれを使う。「別ブランチで並行して」「別エージェントに任せて」「worktree 切って」のときも。git worktree add の代わりに使う。
---

# pekoe でセッションを作って任せる

pekoe は worktree ごとに Claude Code(や codex)を走らせるツール。`pekoe new` 1 回で
worktree 作成 → ファイルのコピー → setup → ポート割り当て → エージェント起動まで済む。
`git worktree add` を自分で叩かず、必ず `pekoe new` を使う(setup や env、ポート台帳が pekoe 側にあるため)。

統括役(ハブ)として複数のセッションを回す・報告を `pekoe wait` で待つ・人に判断を上げるときは、pekoe-orchestrate スキルも使う。任された作業セッションの側は pekoe-work、独立レビューは pekoe-review。

## 考え方

人間が入力する箇所はできるだけ絞る。人間が worktree を渡り歩いてそれぞれのエージェントに打ち込むのではなく、main のチェックアウトで動くエージェント(ハブ)がセッションを作り、指示も main から出す。

- main では直接編集しない。変更は `pekoe new` でセッションを作って任せる。小さな変更やドキュメントでも同じ。ユーザーが「pekoe で」「新しいセッションで」と言わなくてもそうする
- 追加の指示・修正依頼は main から `pekoe send <slug> -` で送る。ユーザーに worktree へ移動して打ち込むよう勧めない
- 調査(ファイルを読む・grep・ls・状態確認)は main で自分でやってよい。調べた結果をプロンプトに込める
- 進捗は main から見る(`pekoe --json ls`、worktree の `git log` / `git diff`)。land・マージ・rm はユーザーに頼まれてから
- 例外: cwd が linked worktree(`git worktree add` / `pekoe new` で作られた側)のとき(= 自分がセッション内のエージェント)は、その worktree で直接作業する。さらに `pekoe new` で孫セッションを作らない(並行させたい明確な理由があるときを除く)
  - 判定は `git rev-parse --path-format=absolute --git-dir --git-common-dir` の 2 行を比べる。同じ(= リポジトリ本体)なら main 側、違えば linked worktree。`--path-format=absolute` を付けないとサブディレクトリでは片方が相対パスになり、本体でも食い違って見える
  - `$PEKOE_SESSION` の有無や値では判定しない。main のチェックアウトを取り込んだセッションにも `$PEKOE_SESSION` は入る(値も `main` とは限らず、`pekoe-site-main` のようになることもある)。`$PEKOE_SESSION` が空(pekoe の外で起動した)でリポジトリ本体にいるなら、もちろん main 側
- 例外の例外: `$PEKOE_SESSION` が `+tapi` のときは tapi chat(TUI の `t` で呼ぶ、どのリポジトリにも属さない窓口)。セッションの中ではなく main と同じ窓口側なので、直接編集せずに `pekoe new` でセッションを作って任せる。cwd はリポジトリではないので、必ず `--repo <リポジトリ名かパス>` を付ける(下の「tapi chat から作るとき」)
- ユーザーが「ここで直接やって」と言ったときはそれに従う

## 前提の確認

- 対象リポジトリの中で実行する(`pekoe new` は cwd のリポジトリに worktree を作る)
- 設定がまだなければ `pekoe new` が素の worktree を作るだけになる。setup が要りそうなリポジトリなら、ユーザーに端末で `pekoe init` を実行してもらう(下書きを見て選び、書き込む。設定の下書き・書き込みは人間だけの操作で、エージェントからは断られる)。必要なら、リポジトリを読んで setup に要りそうなコマンドを先に伝えてよい
- デーモンは自動で起動するので、普段は気にしなくていい

## セッションを作る

```sh
pekoe --json new feat/csv-export --prompt "$(cat <<'EOF'
<指示本文>
EOF
)"
```

- `--json` を付ける。付けないと setup 完了まで進捗を追い続けて戻ってこない。`--json` なら作成を受け付けた時点でセッション情報(`slug` `path` `port` など)を返す
- ブランチ名は `feat/` `fix/` `chore/` `docs/` などで始めた kebab-case。slug はブランチ名の `/` を `-` にしたもの(`feat/csv-export` → `feat-csv-export`)
- GitHub issue から始めるときは `--from gh#128`。issue 本文が初期プロンプトになる(`--prompt` を足せば追加指示になる)
- codex など別エージェントに任せるときは `--agent codex`。ただしエージェント(token なし)から作れるのは、pekoe の sandbox が付くエージェント(sandbox が on の Claude Code)と自分と同じエージェントだけ。codex などは `only a person can do this` で断られるので、人に TUI か端末で作ってもらう
- 派生元ブランチを変えたいときは `--base <branch>`

### tapi chat から作るとき

tapi chat の cwd は pekoe のデータディレクトリ(リポジトリではない)なので、`pekoe new` は対象のリポジトリを決められない。`--repo` で指定する(repo-id・短い名前 = `~/worktrees/<name>` の name・パスのどれでもよい。pekoe が知っているリポジトリは tapi chat の CLAUDE.md に並んでいる):

```sh
pekoe --json new fix/bump-deps --repo api --prompt "$(cat <<'EOF'
<指示本文>
EOF
)"
```

### プロンプトの書き方

新しいエージェントにはこの会話の文脈がない。`--prompt` だけで仕事が完結するように書く:

- 何をするか、なぜするか(背景)
- 関係するファイルや、ここまでに分かっていること・試して駄目だったこと(main で調べた結果をここに込める)
- 完了条件(テストが通る、どのコマンドで確認するか、コミットまでするか など)
- やらないこと(範囲外の変更、push しない など)

## 作った後(main から操作する)

- ユーザーには slug・ブランチ・何を頼んだかを伝える。worktree のパスやポートは必要なときだけでよい。TUI(`pekoe`)で様子を見られると添えてもよい
- 状態の確認: `pekoe --json ls`。`state` は `creating` → `running` → `waiting`(許可待ち・入力待ち)/ `idle` / `done` → `reviewing` → `approved` → `landed`、失敗は `failed`(`error` に理由)
- 中身の確認: `ls` の `path` を使って `git -C <path> log` / `git -C <path> diff main...` などを main から読む
- 追加の指示・修正依頼: `pekoe send <slug> -` に stdin で本文を渡す(長文や改行を含むときは必ず stdin で)。ユーザーから修正の要望が来たら、それをまとめて送る
- 相手が許可待ち(`state` が `waiting` で `wait_kind` が `question` 以外、または相手の画面に許可ダイアログが出ている)の間は `pekoe send` が `<slug> is waiting for a permission answer: …` で断られる。許可にはユーザーが TUI などで答えるので、そう伝えて待つ

## 人間だけの操作

pekoed がエージェント(人間用トークン `human-token` を持たない呼び出し)に許すのは、`pekoe ls`・`pekoe new`・`pekoe send`・`pekoe land`・landed のセッションの `pekoe rm`・`pekoe conflicts`・自分のセッションの `pekoe dev` と hooks の `pekoe event` だけ(作業セッション用の `report`・`ask-human` などは pekoe-work スキル。`pekoe conflicts` は 10 秒以内に続けて呼ぶと前の結果が返る)。`pekoe new` は pekoe が既に知っているリポジトリ(セッションがある・あった、設定がある)でだけ通り、新しいリポジトリでは `a repository pekoe does not know yet` で断られる(最初のセッションはユーザーに TUI か端末で作ってもらう)。`pekoe land` は origin のあるリポジトリでだけ通る(origin がないと base をその場で進めるので人間だけの操作)。`pekoe rm` は `landed` のセッション(マージ済みで、pekoed が worktree をもう消したもの)を一覧から消すときだけ通る。ほかの状態のセッション・`--force`・worktree が残っている landed のセッションの rm は人間だけの操作。許可待ちへの回答(`pekoe answer`)・生キー・`pekoe init`・別のセッションの `pekoe dev`・run 窓でのコマンド実行・`pekoe open` / `review` / `issues`・`pekoe daemon`(`stop` / `restart` / `status`)などは、エージェントから実行すると `only a person can do this` で断られる。pekoe 自身の入れ替え(`pekoe update`)も同じで、`$PEKOE_SESSION` がある・`human-token` を読めないところからは断られる(`pekoe update --check` で新しい版があるか見るのは誰でもできる)。

- 断られたら、回避しようとせず(トークンのファイルを探して読む、tmux で直接キーを送る、など)、ユーザーに TUI か自分の端末でやってもらうよう伝える
- ユーザーには、`Bash(pekoe *)` のように pekoe を丸ごと許可せず、`pekoe ls` / `pekoe --json ls` / `pekoe --json new …` / `pekoe send …` だけを許可すること、Claude Code のエージェントは pekoe が渡す sandbox の設定でトークンのファイルを読めないこと(`[agents.claude] sandbox = false` にしていなければ)を伝える。sandbox の中ではビルドキャッシュへの書き込みや許していないドメインへの接続が断られる。断られたら回避せず、`~/.claude/settings.json` に `allowWrite` / `allowedDomains` を足す方法(pekoe の README.ja.md「エージェントに pekoe を使わせるときの権限」)をユーザーに伝える

## やらないこと

- `pekoe land`(push と PR 作成)、マージ、`pekoe rm` / worktree 削除、`pekoe answer`(他セッションの許可に代わりに答える。そもそもエージェントからは断られる)は、ユーザーに明示的に頼まれたときだけ。landed のセッションの片付け(`pekoe rm <slug> --yes`。一覧から消すだけ)は、頼まれればハブでできる。それ以外の rm はユーザーに TUI か端末でやってもらう
- 自分自身のセッション(環境変数 `$PEKOE_SESSION` の slug)に `pekoe send` しない
- 同じ作業のために何個もセッションを作らない。並行させる理由がある単位でだけ分ける。既存セッションの続きなら `pekoe send` で足す
- `pekoe update`(pekoe を GitHub のリリースに入れ替える)は、ユーザーに頼まれたときだけ。ただし人間だけの操作なのでエージェントからは断られる。頼まれたら `pekoe update --check` で新しい版があるかを見て、入れ替えはユーザーに自分の端末で `pekoe update` してもらう
- 本物のデーモンを止めたり壊したりしない。引数なしの `pekoe daemon stop` / `pekoe daemon restart` は、ユーザーに頼まれたときだけ実行する(どちらも人間だけの操作で、エージェントからは断られる)。`tmux -L pekoe kill-server` や、`PEKOE_TMUX_SOCKET` を空や `pekoe` にしたままの `pekoe daemon stop --kill-agents` は使わない(全エージェントが止まる)

## 隔離した環境で動作確認するとき

本物のデーモンを汚さずに pekoe の動きを確かめたいときは、`PEKOE_HOME` / `PEKOE_SOCK` / `PEKOE_TMUX_SOCKET` / `PEKOE_WORKTREES` を一時的な値にして、別のデーモンを立てる。

- pekoe リポジトリなら `scripts/sandbox.sh` を使う(抜けると tmux サーバーごと片付く)
- 手で立てたときは、最後に同じ環境変数のまま `pekoe daemon stop --kill-agents --yes` で片付ける。`pekoe daemon stop` だけでは tmux サーバーが残る
- 片付けた後、`ps ax | grep 'tmux -L <ソケット名>'` で残っていないことを確かめる
- 新しいセッションに隔離環境での確認を任せるときも、プロンプトにこの片付けを書く
