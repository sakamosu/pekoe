# 設定

README の「[設定](../README.ja.md#設定)」の詳しい版です。リポジトリごとの設定と `config.toml` のすべての節を、注釈付きで示します。

`~/.config/pekoe/repos/<repo-id>.toml`(repo-id は、origin の URL を正規化したものです。clone し直しても同じ設定が適用されます。`pekoe init` で下書きできます):

```toml
agent = "claude"                  # 既定のエージェント

[worktree]
copy  = [".env*", ".envrc"]
clone = ["node_modules"]          # APFS クローン(cp -c)
base  = "main"                    # 既定: origin の HEAD か main

[hooks]
setup    = ["mise install", "pnpm install --offline", "createdb app_{{slug}}"]
dev      = "pnpm dev --port {{port}}"
dev_stop = "docker compose down"  # pekoe が dev を止めるとき(pekoe dev・rm・dev 窓を閉じる・--kill-agents)と dev が自分で終わったとき
test     = "pnpm test"
teardown = ["dropdb app_{{slug}}"]

[env]
PORT = "{{port}}"
DATABASE_URL = "postgres:///app_{{slug}}"

[review]
gatekeeper = "claude -p '危ない箇所を path:line: text で挙げて'"   # diff を stdin で受ける

[merge]
method = "merge"                  # 受信箱で merge と答えたときの pekoed のマージの方法: merge | squash | rebase
```

変数は `{{slug}}` `{{branch}}` `{{port}}` `{{path}}` `{{repo}}` です。setup は順に実行し、失敗したらそこで止めて、左ペインに赤で表示します。`[hooks]` のコマンドに値を埋め込むとき、シェルが特別に扱う文字(空白・`$`・`` ` ``・`;` など)を含む値は、単引用符で囲んで 1 語として渡します(`feat/x` のような普通の値は、そのままです)。必要なときは pekoe が囲むので、**`{{…}}` を自分で引用符で囲まないでください**(`createdb "app_{{branch}}"` ではなく、`createdb app_{{branch}}` と書きます。二重引用符の中では、単引用符が効きません)。ブランチ名に、シェルが特別に扱う文字(`$` `(` `)` `;` `|` `{` `}` 空白など)が含まれていると、`pekoe new` が拒否します。`[env]` の値には、引用符を付けません。

`[worktree] copy` / `clone` のファイルは、本体のチェックアウトから持ってきます。そこで何にも一致しないパターンと、0 バイトのファイルは警告になります。警告は、`pekoe new` と New session の画面の env の行の下、`pekoe --json ls` の `warnings`、エージェントのパネルのタイトルの「N warnings at creation」に表示されます。セッションはそのまま作られます。

`dev` は tmux の専用の窓で、pekoed の sandbox の中で動きます。出力は、エスケープシーケンスを除いた形でファイルにも残ります(`pekoe --json ls` の `dev_log`。末尾は `pekoe dev --log` で読めます)。pekoed が止まっている間の出力は、窓にだけ残ります。`pekoe --json ls` の `dev` は、hook のプロセスが動いている間だけ true です。自分で終了すると false になり、`dev_exit` にどう終了したかが入ります(`status`・`signal`・`at`。pekoe が止めた場合は `stopped`、`[hooks] dev_stop` の結果は `dev_stop`)。`dev_stop` は、`dev` が自分のプロセスの外に立ち上げたもの(Makefile の `docker compose up -d` など)を後片付けするためのもので、pekoed が `dev` と同じ条件で(同じ worktree・環境変数・sandbox で)実行します。実行するのは、動いている hook を止めるとき(`pekoe dev`・dev 窓を閉じる・`x`・rm・マージ後の teardown・`pekoe daemon stop --kill-agents`)と、hook が自分で終了したときです(終了コードは問いません。途中で失敗した `make dev` も、コンテナを作りかけていることがあるためです。したがって、`dev` にはフォアグラウンドで動き続けるものを指定し、起動だけして終わるものは指定しないでください)。rm・マージ後の teardown・`--kill-agents` では、dev hook を起動したことがあり、かつ最後の `dev_stop` が成功していない(失敗した、または実行されなかった: 古い pekoed のときに hook が終了した、worktree がなかった)セッションで、もう一度実行します。一度も `dev` を起動していないセッションでは、実行しません。`dev_stop` が成功しないまま削除されたセッションのポートは、7 日間、ほかのセッションに割り当てません(`dev` が残したものが、そのポートを基に決めたポート(+20000 の Postgres など)を使い続けているかもしれないためです)。`pekoe rm` がそのことを知らせ、7 日たつと自動で解除されます(それまでに、残ったものを `docker compose -p <project> down` などで片付けてください)。`dev_stop` のないリポジトリでは、ポートを避けません。単なる `pekoe daemon stop` は dev server を動かしたままにするので、`dev_stop` も実行しません。`teardown` は、これまでどおり worktree の後片付け(rm とマージ後)に使います。`pekoe dev` が `[hooks] dev` を見つけられないときは、エラーにファイルの状況(ない・読めない・`[hooks]` の節がない・`[hooks]` にあるキー)を添えます。

`~/.config/pekoe/config.toml`(全体の設定です。`pekoe config edit` か TUI の `^q ,` で開けます。`pekoe config show` で、既定値を含めた現在の設定を確認できます):

```toml
animate = true              # 状態アイコンのアニメーション
[http]
port = 47777                # Chrome 拡張用(127.0.0.1)
[ports]
base = 3001                 # ポート台帳の開始番号
[keys]
prefix = "ctrl+q"           # 入力から抜けるキー: ctrl+ 英字(i・m を除く)・ctrl+@ \ ] ^ _・F1〜F20、それらや 1 文字への alt+。C-a / Ctrl+A / ^a も ctrl+a と読む
[review]
surface = "nvim"            # nvim | tui
[picks]
auto_send = true            # 指示つきのピックを、ページのポートを持つセッションへすぐ送る
[web]                       # Web UI(make install WEB=1 のビルドのみ、docs/web.md)
enable        = true
allow_users   = ["you@github"]      # Tailscale のログイン名
allow_devices = ["iphone-15"]       # 任意。この端末だけに絞る
hostname      = "pekoe"             # tailnet のマシン名
confirm_tapi_send = true            # tapi への send の前に確認シート。false ですぐ送る(再起動は要らない)
[gui]                       # 127.0.0.1 のローカル GUI(pekoe gui、docs/gui.md)
enable      = true          # false で pekoe gui を断る。true でも最初の pekoe gui までは待ち受けない
port        = 0             # 0 で待ち受けを始めるたびに空いているもの。始めるときに読む(pekoe gui --stop の後の pekoe gui)
session_ttl = "12h"         # ブラウザのサインインは、操作しないままこの時間で切れる(画面の更新は数えない)
browser     = ""            # 開く macOS のアプリ(open -a)。例 "Safari"。空なら既定のブラウザ
[theme]
mode  = "auto"              # truecolor | ansi | none | auto
icons = "nerd"              # nerd | unicode | ascii
ambiguous = "auto"          # 端末が曖昧幅の字(● · ─)を何セルで数えるか: auto(TUI を開くときに測る) | narrow(1 セル) | wide(2 セル。枠線を + - |、記号を ... -> <- にし、アイコンと tapi は ascii。TUI を開くときに読む。docs/ambiguous-width.md)
[theme.colors]              # 役割単位で上書き(docs/ui-guidelines.md のトークン)
primary = "#ff87da"
[theme.ansi]                # mode = "ansi" のときの役割ごとの色番号(0〜255。docs/ui-guidelines.md)
primary = 5
[mascot]                    # マスコット tapi(docs/mascot.md)
shape   = "auto"            # auto | round(六分ブロック) | block(四分ブロック)
glyphs  = "auto"            # auto([theme] ambiguous が wide なら ascii) | unicode | ascii(narrow は以前の値で、もう要らない。auto と同じ)
palette = "dark"            # dark(ミルキーな粒。暗い背景向け) | light(キャラメル色の粒。明るい背景向け)
splash  = true              # 起動時のスプラッシュ
sleep_after = "3m"          # 全セッションが idle でキー入力もなくこれだけ経つと tapi が眠る(下限 10s)
[mascot.colors]             # hi / bd / sh / eye を上書き
bd = "#e4c49a"
[agents.claude]
sandbox = true              # Claude Code に pekoe の sandbox の設定(--settings)を渡す。false で渡さない
model   = "claude-opus-5-5" # 起動のたび(新規・resume・R・tapi)に --model で渡す。無ければ ~/.claude/settings.json に従う
effort  = "medium"          # --effort で渡す: low | medium | high | xhigh | max
[agents.codex]              # Claude Code 以外のエージェント(sandbox の設定は渡さない)
command = "codex"
allow   = "y"
deny    = "n"
[tapi]                      # tapi chat(TUI の t)
agent = "claude"            # [agents.*] の名前。claude 以外は --add-dir なし(cwd だけ)
[orchestrator]              # 統括役としての tapi(docs/orchestration.md)
notify = "escalate"         # tapi の許可待ちと受信箱の問いを、セッションの wait と同じに知らせる(トースト → 点滅 → OS 通知)| "quiet": 最下行・メッセージ行だけ
inbox_popup = true          # エージェントが答えを待つ新しい問いを、答える画面としてその場で前に出す(1 回だけ。esc / ↵ でスキップすると受信箱に残り ]a で答える)。notify = "quiet" では出さない
deliver = "off"             # "idle": tapi の手が空いたときに、任せたセッションの報告・failed・waiting・ci と受信箱の答えを 1 行で tapi の入力欄に送る(pekoe wait を使わないエージェント向け)。tapi chat に打っている間・許可ダイアログの間は送らず、tapi の pekoe wait が受け取るもの(受け取ったもの)も送らない。30 秒に 1 回まで
max_running = 0             # 同時に作業させるセッションの上限(creating・running・waiting)。超えた pekoe new は待ち行列に入り、古い順に始まる。0 は上限なし
[notify]                    # 待ちの知らせの間合い(セッションの wait。notify = "quiet" でなければ tapi と受信箱の問いも)
toast_for   = "5s"          # トーストを出しておく長さ(PR の反応も。下限 1s)
blink_after = "30s"         # 待ちがこれだけ続くと最下行の台詞が点滅する(下限 1s)
os_after    = "2m"          # 待ちがこれだけ続くと、ターミナルにフォーカスが無ければ OS の通知(下限 10s)
[pr]                        # pekoed がセッションの PR を見に行く間合い
poll_every  = "1m"          # 開いている PR ごとに、GitHub に問い合わせるのはこれに 1 回まで(下限 30s)
[cleanup]                   # pekoe cleanup / :cleanup
stale_after = "72h"         # 止まっている(done・idle・failed など)セッションを、状態が変わらずにこれだけ経ったら stale として出す(Go の duration)
[tips]                      # TIPS(4 の空き表示と tapi chat のあいさつ)
enable = true               # false で両方とも出さない
[sandbox]                   # worktree から来るコマンド([hooks]・門番)と pekoed の git を sandbox で動かす
enable = true               # false で sandbox なしに動かす(使えない環境では動かさない。git はそれでも動かす)
[voice]                     # 状態が変わったら tapi が喋る(macOS だけ。docs/voice.md)
enable = false              # 既定はオフ。オフの間は何も喋らず、backend も動かさず、何も鳴らさない
lang = "auto"               # auto(LC_ALL → LC_MESSAGES → LANG)| ja | en
backend = "say"             # "say"(同梱。macOS の say)か [voice.backends] の名前。PATH からは探さない
fallback = "say"            # backend が使えないとき。"" でフォールバックしない
focus_mode = "read"         # TUI で見ているセッション: read | narrate | mute(read は Claude Code の返事も読む)
background_mode = "narrate" # それ以外: read | narrate | mute
[voice.read]                # read で読む量(コード・表・URL・パスは飛ばす)
max_sentences = 5           # 1 メッセージで読む文の数(1〜50)
max_turn_sentences = 10     # 1 ターンで読む文の数(1〜100。これとは別に 800 文字まで)
skip = ["code", "table", "url", "path"]   # 読まずに飛ばすもの(既定はこの 4 つ。docs/voice.md)
[voice.phrases]             # 喋る文の上書き: wait done error ask idle_all と wait_many など({name} / {names})
wait = "{name}、確認待ちだよ"
[voice.backends]            # 外部の backend: 名前 = 絶対パス(か ~/ で始まるパス)。ここに書いたものだけを動かす
voicevox = "/opt/homebrew/bin/pekoe-voice-voicevox"
[voice.say]                 # 同梱の say の声。"" は Kyoko(無ければ最初の ja_JP の声)/ システムの既定の声
ja = "Kyoko"
```

時間の設定(`[notify]`・`[mascot] sleep_after`・`[pr] poll_every`・`[cleanup] stale_after`・`[gui] session_ttl`)は、Go の duration(`30s`・`2m`・`1h30m`)で書きます。空の値や読めない値は既定値になり、下限より短い値は下限になります。

保存した変更は、再起動なしで反映されます。pekoed は変更から 1 秒以内に `config.toml` を読み直し、TUI もそれに続きます。`[voice]` は節ごと読み直し、変わったら鳴っているものを止めて、今の状態から始め直します。例外は `[http] port`・`[web]`・`[sandbox]` で、`pekoe daemon restart` するまでは起動時の値のままです(TUI のメッセージ行と `pekoe daemon status` に、該当する設定の名前が表示されます)。ファイルを読めない場合、設定は前のままになり、TUI が理由を表示します。知らないキーや受け付けない値は、`pekoe config check` で確認できます([コマンド](commands.md))。

`[agents.<name>]` には、`command` `resume` `prompt`(`arg` | `paste`)`allow` `always` `deny` `hooks` `sandbox` `model` `effort` を書けます。`sandbox`(既定は `true`)は、`command` が `claude` で始まるものにだけ効きます([エージェントに pekoe を使わせるときの権限](agent-permissions.md))。`arg` は、最初のプロンプトを `--` の後ろに置いて渡します(`claude -- '<プロンプト>'`)。そのため、`-` で始まるプロンプトがオプションとして解釈されることはありません。`--` を受け付けないエージェントには、`paste` を使ってください。Claude Code の許可メニューへの回答としては、既定で `1`(許可)/ `2`(常に許可)/ `Esc`(拒否)を送ります。Claude Code の挙動が変わった場合は、`[agents.claude]` で直せます。

`model` と `effort` は、Claude Code のエージェント(`command` が `claude` で始まるもの。ほかのエージェントでは無視されます)のモデルを固定します。pekoe は、起動のたび(新規・`open`・resume・R での再起動・デーモン再起動後の再開・tapi chat)に `--model <model> --effort <effort>` を付けます。**設定しておいてください。** 設定がないと、Claude Code 自身の既定(`~/.claude/settings.json` の `"model"`)に従いますが、このファイルは、どの Claude Code のセッションで `/model` を実行しても書き換わります。つまり、1 つのセッションを大きいモデルに切り替えただけで、その後に pekoe が起動するセッションが、すべてそのモデルになってしまいます。`model` を設定しておけば、セッションの中の `/model` は、そのセッションだけを(再起動するまで)切り替えます(R や resume では、設定したモデルをもう一度渡します)。pekoe が `~/.claude/settings.json` を書き換えることはありません。設定がないときに pekoe が既定のモデルを決めないのは、意図的なものです。どのモデルが使えて、どれだけ費用がかかるかは使う人が決めることですし、pekoe に組み込んだモデル名は、いずれ古くなります。そのため、推測で渡すよりも、Claude Code の既定に任せています。モデルは、`command` ではなく `model` に書いてください(`command = "claude --model …"` と `model` の両方を書くと、`--model` が 2 回渡ります)。`pekoe new --model M --effort E` で、1 つのセッションだけ上書きできます(resume や再起動の後も保たれます)。リポジトリは `repos/<name>.toml` の `agent` でエージェントを選ぶので、別のモデルで動かしたいリポジトリには、別名のエージェントを定義して(`[agents.claude-big] command = "claude"`・`model = "…"`。リポジトリ側は `agent = "claude-big"`)、それを指定します。tapi は、`[tapi] agent` のエージェントに従います。`pekoe --json ls` には、各セッションが最後に応答したモデル(`last_model`。会話記録の最後の assistant のメッセージから取ります)と、上書きした値(`model` / `effort`)が表示されます。
