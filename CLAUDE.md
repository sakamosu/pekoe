# pekoe — エージェント向けメモ

## 動作確認は隔離環境で

- 動作確認は `scripts/sandbox.sh` で行う。一時ディレクトリに `PEKOE_HOME` / `PEKOE_SOCK` / `PEKOE_TMUX_SOCKET` / `PEKOE_WORKTREES` を切り、今のソースをビルドした `pekoe` でデーモンを立てる。抜けると(失敗や Ctrl-C でも)tmux サーバーごと止めて一時ディレクトリを消す
  ```sh
  scripts/sandbox.sh                      # サブシェル。exit で片付く
  scripts/sandbox.sh sh -c 'pekoe ls; …'  # コマンドを 1 つだけ
  ```
- 本物のデーモンには触らない: `tmux -L pekoe`、`~/.local/share/pekoe/`(`state.db`・`pekoed.sock`)、`~/.config/pekoe/`、`~/worktrees/`。`tmux -L pekoe kill-server` や引数なしの `pekoe daemon stop` / `pekoe daemon restart` も実行しない(ユーザーのエージェントが止まる・つながらなくなる)
- 手で隔離環境を立てたときは、最後に同じ環境変数のまま `pekoe daemon stop --kill-agents --yes` で片付け、`ps ax | grep 'tmux -L <そのソケット名>'` で tmux サーバーが残っていないことを確かめる。`pekoe daemon stop` だけでは tmux サーバーと偽エージェントが残る
- `PEKOE_TMUX_SOCKET` を空や `pekoe` にしたまま `--kill-agents` を使わない。全エージェントが止まる
- unix ソケットのパスは macOS で約 104 バイトまで。隔離用のディレクトリは `/tmp` 直下など短い場所に作る

## テスト

```sh
make test   # go vet + go test ./...(既定と -tags web の両方)+ nvim のヘッドレステスト
```

エージェントの sandbox の中では unix ソケット・TCP ポートの bind が塞がれているので、本物のソケット・ポート・tmux・入れ子の sandbox(sandbox-exec / bwrap)が要る統合テストと nvim のヘッドレステストは理由を添えて飛ぶ(`make test` の最後に note が出る)。飛んだものは sandbox の外(人間の端末)で `make test` を回して確かめてもらう。新しいテストは `/tmp` を直書きせず、偽デーモン・HTTP はメモリ上でつなぎ(`internal/testenv` の `Listen` + `client.DialSocket`、`NewServer`)、本物が要るときだけ `testenv.NeedUnixSocket` / `NeedTCP` / `NeedTmux` で飛ばす。

Web UI(`internal/daemon/webts.go`)は `-tags web` のビルドにだけ入る。既定のビルドに Tailscale のコードを持ち込まない(`webts.go` 以外から `tailscale.com` を import しない)。

daemon のテストは偽エンジン(`internal/daemon/daemon_test.go` の `fakeEngine`)で動く。`engine.Engine` にメソッドを足したら偽エンジンにも足す。

## TIPS(機能を足したら tip も足す)

- 機能(キー・サブコマンド・config.toml の節)を足したら `internal/tips/tips.toml` に tip を足す。文面は日本語(TIPS だけの例外。キー名・コマンド・設定名はそのまま)、id は一度決めたら変えない(見せた記録 `tips.json` に残る)、書いたキー・コマンドは実装と照らして確かめる
- 網羅テスト(`internal/tui/tipcover_test.go`・`cmd/pekoe/tipcover_test.go`・`internal/config` の `TestTipsCoverSections`)が、ヘルプのキー一覧(`helpSets` / `globalHelpKeys`)・サブコマンド(`specs`)・設定の節(`config.Sections`)と tip の keys を両方向で突き合わせる。落ちたら tip を足すか、`internal/tips/cover.go` の `NoTip` に理由を添えて入れる。キーを消したら、それを書いた tip の keys も直す
- キーやコマンドを伴わない表示だけの機能(バッジ・表示の変化など)はテストで拾えないので、紹介する価値があれば自分で tip を足す
