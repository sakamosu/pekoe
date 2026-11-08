---
name: pekoe
description: pekoe で別の worktree とエージェントセッションを作り、作業を並行して任せる。「別ブランチで並行して」「これは別エージェントに任せて」「issue #N をやらせて」「worktree 切ってやって」など、今の会話とは別の場所で作業を進めたいときに使う。git worktree add を直接叩く代わりにこれを使う。
---

# pekoe で並行セッションを作る

pekoe は worktree ごとに Claude Code(や codex)を走らせるツール。`pekoe new` 1 回で
worktree 作成 → ファイルのコピー → setup → ポート割り当て → エージェント起動まで済む。
`git worktree add` を自分で叩かず、必ず `pekoe new` を使う(setup や env、ポート台帳が pekoe 側にあるため)。

## 前提の確認

- 対象リポジトリの中で実行する(`pekoe new` は cwd のリポジトリに worktree を作る)
- 設定がまだなければ `pekoe new` が素の worktree を作るだけになる。setup が要りそうなリポジトリなら、先に `pekoe init` で下書きを作ってユーザーに確認してもらう
- デーモンは自動で起動するので、普段は気にしなくていい

## セッションを作る

```sh
pekoe --json new feat/csv-export --prompt "$(cat <<'EOF'
<指示本文>
EOF
)"
```

- `--json` を付ける。付けないと setup 完了まで進捗を追い続けて戻ってこない。`--json` なら作成を受け付けた時点でセッション情報(`slug` `path` `port` など)を返す
- ブランチ名は `feat/` `fix/` `chore/` などで始めた kebab-case。slug はブランチ名の `/` を `-` にしたもの(`feat/csv-export` → `feat-csv-export`)
- GitHub issue から始めるときは `--from gh#128`。issue 本文が初期プロンプトになる(`--prompt` を足せば追加指示になる)
- codex など別エージェントに任せるときは `--agent codex`
- 派生元ブランチを変えたいときは `--base <branch>`

### プロンプトの書き方

新しいエージェントにはこの会話の文脈がない。`--prompt` だけで仕事が完結するように書く:

- 何をするか、なぜするか(背景)
- 関係するファイルや、ここまでに分かっていること・試して駄目だったこと
- 完了条件(テストが通る、どのコマンドで確認するか など)
- やらないこと(範囲外の変更、push しない など)

## 作った後

- 状態の確認: `pekoe --json ls`。`state` は `creating` → `running` → `waiting`(許可待ち・入力待ち)/ `idle` / `done` → `reviewing` → `approved` → `landed`、失敗は `failed`(`error` に理由)
- 追加の指示: `pekoe send <slug> -` に stdin で本文を渡す(長文や改行を含むときは必ず stdin で)
- ユーザーには slug・ブランチ・worktree のパス・ポートを伝え、TUI(`pekoe`)で様子を見られると添える

## やらないこと

- `pekoe land`(push と PR 作成)、`pekoe rm` / worktree 削除、`pekoe answer`(他セッションの許可に代わりに答える)は、ユーザーに明示的に頼まれたときだけ
- 自分自身のセッション(環境変数 `$PEKOE_SESSION` の slug)に `pekoe send` しない
- 同じ作業のために何個もセッションを作らない。並行させる理由がある単位でだけ分ける
- 本物のデーモンを止めたり壊したりしない。引数なしの `pekoe daemon stop` は、ユーザーに頼まれたときだけ実行する。`tmux -L pekoe kill-server` や、`PEKOE_TMUX_SOCKET` を空や `pekoe` にしたままの `pekoe daemon stop --kill-agents` は使わない(全エージェントが止まる)

## 隔離した環境で動作確認するとき

本物のデーモンを汚さずに pekoe の動きを確かめたいときは、`PEKOE_HOME` / `PEKOE_SOCK` / `PEKOE_TMUX_SOCKET` / `PEKOE_WORKTREES` を一時的な値にして、別のデーモンを立てる。

- pekoe リポジトリなら `scripts/sandbox.sh` を使う(抜けると tmux サーバーごと片付く)
- 手で立てたときは、最後に同じ環境変数のまま `pekoe daemon stop --kill-agents --yes` で片付ける。`pekoe daemon stop` だけでは tmux サーバーが残る
- 片付けた後、`ps ax | grep 'tmux -L <ソケット名>'` で残っていないことを確かめる
- 新しいセッションに隔離環境での確認を任せるときも、プロンプトにこの片付けを書く
