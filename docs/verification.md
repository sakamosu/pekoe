# 設計書・デザイン原則・モックとの対応表

Nov 7, 2026 時点。各項目の実装箇所と、どう確かめたかの記録。

- テスト: `make test`(go vet + `go test ./...` + nvim ヘッドレステスト)
- 隔離 e2e: `BOBA_HOME` / `BOBA_SOCK` / `BOBA_TMUX_SOCKET` / `BOBA_WORKTREES` で隔離し、偽エージェントで端から端まで流した。今後は `scripts/sandbox.sh` で立てる(終わると `boba daemon stop --kill-agents --yes` で tmux サーバーごと片付く)
- 実機: 本物の Claude Code 2.1.319 と、実機の Chrome に読み込んだ拡張

## 設計原則

| 原則 | 実装 | 確認 |
| --- | --- | --- |
| デーモンに賢さ、クライアントは薄く | `internal/daemon` が唯一の状態の持ち主。TUI・CLI・nvim・拡張・MCP は `docs/api.md` を叩くだけ | TUI のレビュー面で付けた承認とコメントが nvim のレビュー面にそのまま出た |
| ただのシェル | `[hooks]` はシェルコマンドの列で、`{{slug}} {{branch}} {{port}} {{path}} {{repo}}` を埋めて `$SHELL -c` で実行。`boba init` の検出は下書きだけ | `TestNewRunsSetupAndFailsVisibly`(変数展開、失敗したら止めて赤で表示) |
| リポジトリを汚さない | 設定・状態は `~/.config/boba`・`~/.local/share/boba`、スナップショットは `refs/boba/` | `TestSnapshotAndRestore`(index と `git log` に出ない、ignored を含めない)、land 後に `refs/boba` が 0 件 |
| Claude Code の使い方を変えない | pty にキー入力と同じ形で書き込む(複数行は bracketed paste)。状態は hooks から受け取る | 実機の Claude Code で、プロンプト・許可・Stop が hooks から反映された |
| 段階的に差し替えられる | `engine.Engine` インターフェースの裏に tmux エンジン | daemon テストは偽エンジンで動き、本番は tmux エンジン |
| レビューを中心に置く | レビュー地点 `review-<n>`、hunk 単位の承認、hunk とプロンプトの対応 | e2e と `TestSessionLifecycleAndReview` |

## セッションと状態

| 項目 | 確認 |
| --- | --- |
| SessionStart → session_id を保存して `--resume` に使う | 実機で session_id を記録。tmux ごと落とした後、`--resume <id>` で復元した(e2e) |
| UserPromptSubmit → `refs/boba/<slug>/prompt-<n>` | 実機と e2e |
| Notification → 許可待ちか質問かを区別 | `notification_type`(permission_prompt / idle_prompt / elicitation_dialog)で判別。旧形式は文面から判断。`TestClassifyNotification`、実機で Bash の許可待ちを検知 |
| Stop → done・`test` 自動実行・結果を左ペインへ | 実機と e2e(`✓test`) |
| PreToolUse → 直近のツールを左ペインへ | Sessions パネルのフッターに選択中セッションの直近ツールを表示 |
| OSC 9/99/777・タイトル・`[y/n]` のフォールバック | hooks のないエージェント向けに `output.go` で実装。Claude Code の信頼確認ダイアログは hooks より前に出るので、最初の hook が届くまでは画面を読んで検知する(実機で検知・許可まで確認) |
| 左ペインの 1 行(状態・待ち時間・diffstat・テスト・ポート) | `boba ls` と Sessions パネル |
| Attention queue `]a` / `[a` | `attention.next` と TUI の `returnTo`、e2e |
| 未読ライン | 離れた時点の行位置を記録し、戻ったときに `── new ──` を差し込む |
| 永続性 | デーモンを再起動してもセッションが残る(tmux が生存)。tmux ごと落ちたら `--resume` で復元(e2e) |
| 隔離環境の片付け(`boba daemon stop --kill-agents`) | `TestStopKillAgents`(偽エンジンで、`kill_agents` のときだけ `KillServer` が呼ばれ、既定では呼ばれない)、`TestKillServerFallsBackToPidFile`(本物の tmux を別の `TMUX_TMPDIR` で立て、`kill-server` が届かなくても記録した pid で止まる)。`scripts/sandbox.sh` で、偽エージェントの窓を足してから正常終了・コマンドの失敗(exit 3)・Ctrl-C(SIGINT、exit 130)で抜け、どれも `ps` に `tmux -L boba-sb-…` と偽エージェントが残らず、一時ディレクトリも消えた。デーモンを先に止めた後・`TMUX_TMPDIR` を変えた状態からでも tmux サーバーだけ止まり、端末でないところで `--yes` がなければ断ることも確認 |
| デーモンは 1 つだけ(#42: 再起動後に孤児の bobad が state.db を古い状態で上書きした) | `TestSecondDaemonRefused`(ロックを持つデーモンがあれば、応答中ならすぐ、起動中なら待った後に `ErrAlreadyRunning`)、`TestShutdownKeepsNewerSocket`(終了中の古いデーモンが、別のデーモンが作り直したソケットと pid ファイルを消さない。Go の `UnixListener.Close` が持ち主を確かめずに unlink していたのも原因で、`SetUnlinkOnClose(false)` にした)、`TestStopThenConcurrentStartsYieldOne`(終了処理が遅れている間に 6 つ同時に起動しても 1 つだけ)、`TestWaitExit`。`scripts/sandbox.sh` で `daemon stop && daemon start` を 10 回、stop 直後の 8 並列の自動起動(`boba ls`)を 10 回、stop の最中に 8 並列の自動起動を 10 回繰り返し、毎回 `ps` で bobad が 1 つだった |
| CLI のフラグ(`--help` で `daemon stop` が走った) | `TestHelpDoesNothing`(`daemon stop --help` など状態を変えるものを含む 23 通りで、偽デーモンに 1 件も要求が届かない)、`TestUnknownFlagRefused`(`--kill-agent` の打ち間違い・`-f`・値のない `--repo`・余分な位置引数がどれも使い方つきのエラーで、要求は届かない)、`TestDaemonStopStillStops` `TestParseFlags` `TestSplitGlobal`。`scripts/sandbox.sh` で実バイナリから `daemon stop --help` などが終了コード 0 で使い方を出し、デーモンの pid が変わらないこと、知らないフラグが終了コード 2 になること、`--json` がサブコマンドの前後どちらでも効くこと、`boba event` は知らない引数があっても 0 で終わることを確認 |
| 衝突予報(⚠N・also changed in・Changes の相手・land の予報・`boba conflicts`) | `TestConflictForecast`(同じ行 → conflict、違う行 → overlap、別ファイル → なし、未コミットは overlap のみ、許可待ちの Edit に also_in)、`TestSessionListConflictTag` `TestPermissionModalAlsoChanged` `TestChangesShowPeersAndLandForecast`。隔離 e2e で 3 セッションを並べ、TUI(140 列 unicode / 80 列 ascii)と CLI で表示を確認 |

## worktree フロー

| 項目 | 確認 |
| --- | --- |
| `boba new` の 5 歩(worktree → copy/clone → setup → port → 起動) | e2e、TUI の New モーダル(モック 04) |
| `--from gh#N`(issue 本文を初期プロンプトに) | `issuePrompt`(gh issue view)。作ったセッションに `issue` 番号を記録(`TestIssuesMatchSessions`) |
| Issue インボックス(`issues.list` / `issues.view`、[2] Issues、`boba issues`) | 偽 gh でパース・フィルタ・キャッシュ・セッションとの突き合わせ・gh が使えないとき(`TestIssuesList*` `TestIssuesView` `TestIssuesUnavailable` `TestIssuesMatchSessions`)、ブランチ名の提案(`TestSuggestBranch`)、サブタブ巡回と描画(`TestSubtabCyclesThreeWays` `TestNarrowTabsIncludeIssues` `TestIssuesPanelRender` `TestIssueKeys`)。隔離したデーモンと本物の gh で、公開リポジトリの issue 一覧・本文表示・`n` からのセッション作成・Enter でのジャンプ・90 列のタブ化・GitHub リモートの無いリポジトリでの 1 行表示を確認 |
| `boba open`(過去のセッションを resume するか選ぶ)/ `boba adopt` | `TestSessionLifecycleAndReview`(open)。adopt は候補の列挙と確認つきの取り込みとして実装 |
| `boba land`(rebase → test → `gh pr create` → マージ検出 → teardown) | `TestLandWithPRAndMergeTeardown`(偽 gh + bare origin)、ローカル fast-forward は e2e |
| `boba rm` は `~/worktrees` 配下しか消さない | `TestUnderWorktrees`、`TestSessionLifecycleAndReview`(外部リポジトリが残る) |
| 設定ファイル `~/.config/boba/repos/<repo-id>.toml` | `TestNormalizeURL`(origin URL の正規化) |
| `boba init` の自動検出(lockfile、mise、direnv、無視ファイル) | `initdetect`。ルールは `~/.config/boba/defaults/init.toml` で編集できる |
| ポート台帳 | `AllocPort`(決定的、使用中は飛ばす)。タブ → ポート → セッションの対応は実機の Chrome で確認 |
| docker compose / DB | `[env] COMPOSE_PROJECT_NAME = "{{slug}}"` と teardown で対応(リポジトリ側の設定に委ねる) |
| プロンプト単位 undo | `TestSnapshotAndRestore`、e2e |

## ターミナル層

| 項目 | 状況 |
| --- | --- |
| 段階 1: tmux エンジン(専用サーバ `-L boba`、control mode で出力を監視) | 実装・確認済み。tmux 3.5a の `window-size manual` で落ちる不具合は、ウィンドウごとの `resize-window` で回避 |
| 右ペインのサイズを pty に伝える | `session.resize` → `resize-window` |
| パススルー: OSC 52、通知 OSC、マウス(SGR) | 実装済み |
| パススルー: OSC 8 | tmux の `terminal-features` に hyperlinks を設定。`capture-pane -e` の出力に `ESC ] 8 ;; url` が残ることを確認済み |
| パススルー: Kitty keyboard | 実装済み。TUI が Ghostty に disambiguate モード(`CSI > 1 u`)を要求し、届いた CSI u を解釈する。従来形式のないキー(Shift+Enter など)は tmux のキー名で送り、tmux(`extended-keys on`、`csi-u`)がエージェントに CSI u で届ける。経路全体を確認し、本物の Claude Code で Shift+Enter が改行になった。実機の Ghostty でも確認した。エージェントが要求しなければ従来形式のまま(`TestDecodeCSIu`) |
| パススルー: Kitty graphics | 実装済み。tmux の中のアプリが使う Unicode プレースホルダ方式に対応。画像の命令(tmux の DCS で包まれたものも展開)を `%output` から取り出して Ghostty に流し、プレースホルダの文字は `capture-pane` を通って右ペインに出る。カーソル位置への直接配置はデータ転送だけにする。対応確認の問い合わせ(`a=q`)には、外側が対応端末のときだけ bobad が代わりに OK を返す(`TestSplitCarry` `TestGraphicsCommandsUnwrapsTmuxPassthrough` `TestPassGraphics`)。実機の Ghostty で、tmux 向けに包んだ形の画像がプレースホルダの位置に表示された |
| ピックのスクリーンショット表示 | 対応端末では、破線のプレースホルダの代わりに Kitty graphics で画像を表示する(ガイドライン「プレースホルダを置き換える形で」)。実機の Ghostty で、Chrome から送ったボタンの切り抜きがカードに表示された |
| ブラウザプレビュー(preview 窓) | 撮影は CDP over `--remote-debugging-pipe`。偽キャプチャで自動撮り直し・before 保持・dev 停止時のメッセージ・パスとモバイルの記憶を確認(`TestPreviewAutoRetakeOnStop` `TestPreviewDevServerDown` `TestPreviewRemembersPathAndMobile`)。実 Chrome で 1280×800 と 390×844@2x を撮り、モバイル幅でメディアクエリが効くことを確認(`TestChromeCaptureReal`)。TUI はエスケープ列・リサイズ計算・画像の差し替えと削除・破線プレースホルダ(`TestGfxSync` `TestFitCells` `TestPreviewImagesFollowLayoutAndLeaveNothing` `TestPreviewPlaceholderWithoutGraphics`)。実機 Ghostty での表示確認は未了 |
| 段階 2: libghostty-vt の自前 mux | **対象外**(同上) |

## ブラウザ拡張

| 項目 | 確認 |
| --- | --- |
| ⌘⇧E で選択 → 入力欄 → POST | 実機の Chrome でピックを送信(指示「余白を広げて」) |
| IME(Google 日本語入力)での指示入力 | 入力欄を拡張ページの iframe(`prompt.html`)に移し、変換確定の Enter(`isComposing` / keyCode 229)では送信しない。実機の Google 日本語入力で「余白を広げて」を入力して送信できた(ピック #2 が Claude に届いた) |
| `boba rm` でピックも消す | `TestHTTPGuardAndPick`(md・png・記録が残らない) |
| ペイロード(outerHTML、セレクタ、計算済みスタイル、切り抜き、コンポーネント名、ソース位置) | 実機で md と png(108×48)が保存された。React 19 で `_debugSource` がない場合は rg で検索 |
| 長い HTML を pty に流さない(`@…/picks/0012.md` の 1 行) | 実機で Claude が md と png を読んで作業を始めた |
| localhost 以外はセッションを選ばせる | e2e(未解決のピックはカードとセッションピッカーへ) |
| トークン必須 + Origin 確認 | `TestHTTPGuardAndPick`、e2e |
| console error | 実機で 1 件届いた |
| エラーの拾い上げ(dev 窓・ブラウザ → カード) | 検出器のテーブルテスト(Node / tsc / Next.js / Vite / Go / Python、ANSI 付き、チャンク分割、静かになったら確定)、指紋のまとめ・ignore・再発(`internal/errcatch`)、`TestDevErrorsBecomeCards` `TestBrowserErrorsShareTheCardFlow`、カードの描画(`TestErrorCardRenders` ほか)。隔離 e2e で TUI を起動し、dev 窓に出した Next.js 形式のエラー 3 回が `×3` の 1 枚にまとまる → `↵` で抜粋つきの詳細 → `s` でエージェントに `@…/errors/main-0001.md` の 1 行が届く → 次のエラー(Python)に繰り上がる → `i` で消える → dev を再起動すると両方がカードに戻る、ブラウザの POST がカードになり Browser パネルの `E` で送信済みになる、までを確認 |
| run 窓(`!` でコマンドを 1 回実行) | `TestRunWindow`(偽エンジン: worktree・dev と同じ環境変数・`$SHELL -c` での起動、動いている間/閉じる前の `!` は起動しない)、`TestRunSuggest`(`! <cmd>`・バッククォート・コードブロック・色だけのインラインコードからの候補)、`TestRunWindowFlow` `TestRunDeclineReport` `TestRunBangShowsExisting`(入力中の `!` はエージェントへ、候補入りの入力欄 → 起動 → 入力は run 窓へ → `exit N` → `↵` で窓を閉じて agent → `y` で `session.send`(submit)/ `n` で送らない)。`scripts/sandbox.sh` で偽エージェントの画面の `! read "x?ok? "; echo "got=$x"` が初期値に入り、run 窓で `ok? ` に `yes` と答えて `got=yes` と `exit 0`、`↵` で agent に戻って `y` で 1 行届き、run 窓が消えることを確認。`SHELL=/bin/bash` で `read -p 'ok? ' x; echo $x` も同様。tmux 3.5a は終了時の「Pane is dead」行を最下行に書いて短い出力の先頭を履歴へ押し出すため、run 窓だけ `remain-on-exit-format` を空にし、コマンドは pty のサイズが揃うまで待ってから始める | PR #52 のレビュー指摘(15 件)は再現テストを先に書いてから直した: `TestRunEscDoesNotRestartAgent`(Esc / x で閉じた後の Enter がエージェントを再起動しない)、`TestRunXClosesRunNotSession`、`TestRunEnterDoesNotSendReport`、`TestRunOnFailedSessionShowsRun`、`TestRunSuggestReview`(`!=`・`!ok`・`! for bash mode`・1 語のファイル名・画面外の開きフェンス・URL 引数・下線色の SGR)、`TestRunPasteKeepsLines`、`TestRunShowsOtherRunsCommand`、`TestRunInterruptedLine`、daemon の `TestRunStateCarriesCommandAndExit`(コマンドと終了コード・シグナルを daemon が持つ)`TestRunRefusesWithoutWorktree` `TestRunStartSerialized`。SGR パーサーは `internal/termstyle` に切り出して Web の画面と共有(`TestLine`)。`scripts/sandbox.sh` で、対話コマンドの後に Esc で閉じて Enter してもエージェントの pane pid が変わらない(再起動されない)、`^q` で抜けて Sessions から `x` を押すと run 窓だけが閉じてセッションとエージェントは残る、プロンプトに Enter で答えた後に Enter を連打しても「tell the agent?」のまま何も送られない、`sleep 30` を Ctrl-C すると `interrupted (SIGINT)` と出て報告が「`sleep 30` を実行しましたが、中断されました(SIGINT)」になる、を確認 |

## Neovim

| 項目 | 確認 |
| --- | --- |
| A. レビュー(`]h` `[h` `a` `x` `c` `?` `:BobaSend`) | nvim テスト 50 項目、TUI の `r` で右ペインが nvim に替わり `:BobaSend` で戻る |
| B. 範囲送信 / C. 承認フロート / D. Follow / E. 診断送信 / F. `open_in_editor` | nvim テスト、MCP の e2e(`editor.open` イベント) |
| 門番の結果を quickfix に | `:BobaRisks` |

## レビュー機能

| 項目 | 確認 |
| --- | --- |
| レビュー待ち行列 `]r` | `review.next` |
| 差分の差分(`review-<n>` からの差分だけ) | e2e、`TestSessionLifecycleAndReview` |
| hunk 単位の承認(パス + 内容ハッシュ) | `TestHunkIDSurvivesLineShift` |
| hunk とプロンプトの対応(`← prompt #3`) | e2e、TUI(モック 02) |
| 門番(テスト + 読み取り専用エージェント) | Stop で `test` を実行、`[review] gatekeeper` の結果を Comments と quickfix へ |
| その場で聞く `?` | `TestReviewAsk`(返事を Stop で切り出し、レビュー中のまま保つ) |
| 人の PR にも同じ UI(`gh api` で GitHub review へ) | `TestReviewSomeoneElsesPR` |
| 状態遷移 done → reviewing → run → … → approved → landed | e2e |

## CLI とキーバインド

設計書の CLI 一覧(`boba` `init` `new` `open` `adopt` `ls` `send` `event` `review` `land` `rm` `dev` `daemon`)はすべて実装済み。追加で `undo` `answer` `attach` `hooks` `ext` `mcp`。`--json` に対応。

TUI キー(`j` `k` Enter `n` `]a` `[a` `]r` `r` `b` `l` `d` `x` `D` `/` `?`、prefix + `h` `l`)はすべて実装済み。prefix は `[keys] prefix` で変更できる。既定は `ctrl+q`(JIS 配列で押しやすく、Claude Code が使わない)。入力モード中は prefix だけで抜けて Sessions に戻る。

複数リポジトリ(`repo.list`、[1] のリポジトリ見出し、今のリポジトリ、`w`): デーモン側は `TestRepoListCwdAndRemembered`(セッション・cwd(worktree の中から)・覚えたもの(`repo.resolve` でも引ける)・`path_` の設定ファイル・場所不明の設定ファイル)と main の `TestKnownReposIncludeConfigOnly` `TestResolveRepoRef`。TUI は `TestSessionsGroupedByRepo`(truecolor/ansi/none × nerd/unicode/ascii × 180/90 列で全行が端末幅ちょうど)、`TestSingleRepoHasNoHeadings` `TestSelectionSkipsHeadings` `TestSelectedSessionStaysInView` `TestCurrentRepoFollowsSelection` `TestRepoPickerModal` `TestCommandsTargetCurrentRepo`(偽 bobad で `session.new` / `worktree.list` / `issues.list` / `pick.send` の `repo` を確認) `TestOutsideRepo` `TestStartRepoWithoutSessions` `TestStartUpFollowsStartRepo`。隔離したデーモンで、一時ディレクトリの 2 リポジトリに交互にセッションを作り、web で起動 → web のセッションが選ばれ Worktrees とバーが web、`k` で見出しを飛ばして api に移ると Worktrees・バーも api に変わり `n` のセッションが api に、`w` → web → `n` のセッションが web に作られることを確認。90 列のタブ表示、リポジトリの外での起動(設定ファイルだけから `w` の一覧を作り、`n` → リポジトリ選択 → 作成)も確認。

## デザイン原則のチェックリスト

| 項目 | 結果 |
| --- | --- |
| 色の付いた要素すべてに、トークン表の「用途」に合う意味がある | ✓ 役割名だけを参照し、hex は `theme.go` にだけある |
| ネオンの面積 10% 以下(塗りはモードセグメントだけ) | ✓ |
| フォーカスは 1 つで、枠線とタイトルだけで示す。モーダル中は背後を border に落とす | ✓ `box()` |
| タイトルは `[n] アイコン 名前 位置 状態` の形式 | ✓ |
| 選択行は select の塗りで、カーソル記号なし(ansi では反転表示) | ✓ |
| ボトムバーは今のパネルのキーだけ、6 個以内、`key action` 形式。狭いときは後ろから落とし `?` を残す | ✓ `keyHints()` |
| 同じ意味には同じキー(n 新規、d 削除、/ フィルタ) | ✓ |
| すべての状態に記号・色・状態語がそろう | ✓ `TestStatesReadableWithoutColor` |
| アイコンは語彙表のものだけ。nerd / unicode / ascii の 3 段階 | ✓ |
| グラフィカル表現は枠に限り、エージェント出力と diff 本文は素のまま | ✓ |
| 破壊的操作は確認モーダルを通し、枠線は err | ✓ D / x(稼働中) / undo |
| ascii・ansi・none モードで破綻しない | ✓ 描画で確認、none はテストで確認 |
| 100 列未満でも崩れない | ✓ 80 列でモック 06 を確認 |
| 新しい色を足していない | ✓ 11 役割(テストで確認) |

## モック(要素ごとの突き合わせ)

各画面を tmux の中で 140×40(06 は 80 列)で描画し、mock.html の各要素と比べた。

| 画面 | モックの要素 | TUI |
| --- | --- | --- |
| 01 Sessions | `[1] Sessions 1/5`(フォーカス時は primary の太字タイトル) | ✓ |
| | 行: 状態アイコン(実行中は Braille スピナー)・名前・`run/wait/done/idle`・経過時間・`+n -n` | ✓(設計書の 1 行に合わせ、テスト結果の ✓/✗ を追加) |
| | フッター `1 waiting · oldest 3m    1 to review`(アイコンは状態色、文字は dim) | ✓ |
| | `[2] Worktrees`(カレントの印は text、ポートとパスは dim) | ✓ |
| | `[3] Changes 4 feat/csv`(A/M/D は ok/warn/err) | ✓ |
| | `[4] Agent feat/csv · claude · :3001`、右端にスパークラインとトークン数 | ✓ |
| | ステータスライン: モードのピル(primary の塗り)・ブランチ(select の塗り)・集計(surface の塗り)、右にキーヒント 6 個 | ✓ |
| 02 Review | `[1] Files`(● 未承認 / ✓ 承認済み、`n hunks m`)、`since review #2 · 9 hunks · 5 approved`、`+120 -8 · ✓ test` | ✓ |
| | `[2] Comments`(`path:line` は dim)、`[3] Session`(`hunk 2 ← prompt #6 "…"`) | ✓ |
| | `[4] Diff`: ✓ 承認済み(非表示)/ ▶ 現在 / ○ 未着手、ガター ▎、旧・新の行番号、インラインのコメント(secondary の `— you, unsent`)、`A show approved` | ✓(hunk の見出しに `← prompt #n` を追加。設計書の hunk ↔ プロンプト対応) |
| | 最下行のスクロールバー(│ と ┃)+ `hunk 2 of 3 · 1 unsent comment on this hunk` | ✓ |
| | ステータスライン `REVIEW` / `since #2` / `5/9 approved 2 comments` / `a approve x revert c comment ? ask ]h next S send` | ✓ |
| 03 Permission | 中央モーダル(primary の枠、surface の塗り、背後のパネルは border に落ちる) | ✓ |
| | タイトル `● Permission · fix/login · waiting 3m 12s`(待ちアイコンは warn) | ✓ |
| | 実行対象の行を bg に沈めて全幅 1 行で表示(`Bash  pnpm prisma …`) | ✓ |
| | `rule … not in allowlist · A always allow` | ✓ |
| | 最下行 `y allow n deny g go to session A always esc later`、ステータスライン `PERMISSION` | ✓ |
| | `touches …` 行(変更されるファイル) | ✓ Edit / Write 系は hooks の入力の file_path から worktree 相対で表示。Bash は取れないので行ごと出さない(`TestTouches`) |
| 04 New session | `New session · app`、branch / from(gh#) / base / prompt | ✓ |
| | 区切り線のあと、手順のリスト(✓ / スピナー / ○)と所要時間 | ✓ |
| | `esc cancel & remove worktree  ↵ open when ready  b background` | ✓ |
| | 作成中のセッション行(`creating` は dim のスピナー)、Worktrees の `creating…` | ✓ |
| | 空の Agent ペインに ASCII ロゴ(dim、primary の ● 1 つ)と `waiting for worktree…` | ✓(ロゴは boba) |
| | install 段のゲージ(━ と ─、62%) | ✓ 同じリポジトリで前回その setup にかかった時間を記録し、2 秒以上かかった段にだけ、それを基準にゲージと数値(`50% · 2s of ~3s`)を出す。完了するまでは 99% で止める。2 回目の作成で描画を確認 |
| 05 Browser pick | `[2] Browser · 2 tabs linked by port`、`:3001 /path → session`、`2 console errors … E send to session` | ✓ |
| | `[3] Picks`(未送信は secondary の ●、送信済みは dim の ✓) | ✓ |
| | Agent ペイン内のカード(border の枠、アイコンだけ secondary)、element / component / computed、破線のプレースホルダ `clip 320×96 · png`、`attached as @…/picks/0012.md` | ✓ |
| | 指示の入力行と、カード専用の `key action` | ✓(別セッション・新セッションは `^s` / `^n`。入力中の `s` `n` と衝突しないため) |
| | ステータスラインの集計ピルに `pick #0012` | ✓ |
| 06 Narrow | 80 列で左列がタブ 1 枚(`Sessions 1/5  Worktrees  Changes 4`、`[ ] tab`) | ✓ |
| | キーヒントは後ろから落ちて `?` が残る | ✓ |

ガイドラインの指示に合わせて、モックから意図的に変えた点:

- パネルのタイトルは、モックでは枠の内側の 1 行目にあるが、ガイドラインの ASCII 図どおり上の枠線に埋め込んだ。行を節約できる
- モックの名前 `am` は `boba` に置き換えた(タイトル、`refs/boba/…/prompt-<n>`、`~/.config/boba`、ロゴ)
