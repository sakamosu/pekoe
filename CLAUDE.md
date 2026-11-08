# boba — エージェント向けメモ

## 動作確認は隔離環境で

- 動作確認は `scripts/sandbox.sh` で行う。一時ディレクトリに `BOBA_HOME` / `BOBA_SOCK` / `BOBA_TMUX_SOCKET` / `BOBA_WORKTREES` を切り、今のソースをビルドした `boba` でデーモンを立てる。抜けると(失敗や Ctrl-C でも)tmux サーバーごと止めて一時ディレクトリを消す
  ```sh
  scripts/sandbox.sh                      # サブシェル。exit で片付く
  scripts/sandbox.sh sh -c 'boba ls; …'   # コマンドを 1 つだけ
  ```
- 本物のデーモンには触らない: `tmux -L boba`、`~/.local/share/boba/`(`state.db`・`bobad.sock`)、`~/.config/boba/`、`~/worktrees/`。`tmux -L boba kill-server` や引数なしの `boba daemon stop` も実行しない(ユーザーのエージェントが止まる・つながらなくなる)
- 手で隔離環境を立てたときは、最後に同じ環境変数のまま `boba daemon stop --kill-agents --yes` で片付け、`ps ax | grep 'tmux -L <そのソケット名>'` で tmux サーバーが残っていないことを確かめる。`boba daemon stop` だけでは tmux サーバーと偽エージェントが残る
- `BOBA_TMUX_SOCKET` を空や `boba` にしたまま `--kill-agents` を使わない。全エージェントが止まる
- unix ソケットのパスは macOS で約 104 バイトまで。隔離用のディレクトリは `/tmp` 直下など短い場所に作る

## テスト

```sh
make test   # go vet + go test ./... + nvim のヘッドレステスト
```

daemon のテストは偽エンジン(`internal/daemon/daemon_test.go` の `fakeEngine`)で動く。`engine.Engine` にメソッドを足したら偽エンジンにも足す。
