# pekoe — エージェント向けメモ

## 動作確認は隔離環境で

- 動作確認は `scripts/sandbox.sh` で行う。一時ディレクトリに `PEKOE_HOME` / `PEKOE_SOCK` / `PEKOE_TMUX_SOCKET` / `PEKOE_WORKTREES` を切り、今のソースをビルドした `pekoe` でデーモンを立てる。抜けると(失敗や Ctrl-C でも)tmux サーバーごと止めて一時ディレクトリを消す
  ```sh
  scripts/sandbox.sh                      # サブシェル。exit で片付く
  scripts/sandbox.sh sh -c 'pekoe ls; …'  # コマンドを 1 つだけ
  ```
- 本物のデーモンには触らない: `tmux -L pekoe`、`~/.local/share/pekoe/`(`state.db`・`pekoed.sock`)、`~/.config/pekoe/`、`~/worktrees/`。`tmux -L pekoe kill-server` や引数なしの `pekoe daemon stop` も実行しない(ユーザーのエージェントが止まる・つながらなくなる)
- 手で隔離環境を立てたときは、最後に同じ環境変数のまま `pekoe daemon stop --kill-agents --yes` で片付け、`ps ax | grep 'tmux -L <そのソケット名>'` で tmux サーバーが残っていないことを確かめる。`pekoe daemon stop` だけでは tmux サーバーと偽エージェントが残る
- `PEKOE_TMUX_SOCKET` を空や `pekoe` にしたまま `--kill-agents` を使わない。全エージェントが止まる
- unix ソケットのパスは macOS で約 104 バイトまで。隔離用のディレクトリは `/tmp` 直下など短い場所に作る

## テスト

```sh
make test   # go vet + go test ./...(既定と -tags web の両方)+ nvim のヘッドレステスト
```

Web UI(`internal/daemon/webts.go`)は `-tags web` のビルドにだけ入る。既定のビルドに Tailscale のコードを持ち込まない(`webts.go` 以外から `tailscale.com` を import しない)。

daemon のテストは偽エンジン(`internal/daemon/daemon_test.go` の `fakeEngine`)で動く。`engine.Engine` にメソッドを足したら偽エンジンにも足す。
