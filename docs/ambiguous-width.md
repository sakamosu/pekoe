# 曖昧幅の文字の扱い(設計メモ)

issue #85 の調査と案。まだ実装していない。方針を決めるための材料として、今の使用箇所・代わりの文字・幅計算・検出・端末の事情を調べ、「決めること」ごとにおすすめを書く。数字は 2026-11 時点の main(`4c4a74c`)と、手元の Mac で測った結果。

## 要点(おすすめ)

1. 状態表示(状態アイコン・未読の印・マスコットの目と 1 セル表示)は、既定で曖昧幅(East Asian Width が A)の字を使わない。曖昧幅でなくても日本語フォントで全角に描かれる字があるので、置き換える字は EAW が N で、かつ下の表で崩れないと確かめたものにする
2. 枠線・ブロック要素と nerd font の私用領域は例外として許す。前者は主要な端末が自前で描き、後者はアイコンの後ろに必ず空白を置く(nerd font と端末がその空白まで使って描く前提で作られている)
3. 本文の記号(`· … → ← ×` など)は、端末が曖昧幅を 1 セルで数える普通の環境では置き換えない。端末が 2 セルで数える環境(経路 A)でだけ、ASCII を中心にした表に切り替える
4. 経路 A では、pekoe が描く字は置き換えで済ませ(枠線も ASCII にする)、置き換えられないエージェントの画面の取り込みだけ幅を 2 で数える
5. 設定は `[theme] ambiguous = "auto" | "narrow" | "wide"` にまとめる。`mascot.glyphs = "narrow"` は既定の表が崩れなくなるので不要になり、読むだけ読んで何もしない(`pekoe config check` で案内する)

## 2 つの経路

| | 端末の数え方 | フォントの描き方 | 症状 | 検出 |
| --- | --- | --- | --- | --- |
| A | 2 セル(iTerm2 の「Ambiguous characters are double-width」、WezTerm の `treat_east_asian_ambiguous_width_as_wide`、Windows Terminal の `compatibility.ambiguousWidth = "wide"`) | — | pekoe は 1 セルとして並べるので、カーソルが 1 字ごとにずれ、枠線・行末を含めて崩れる | CPR で測れる(今の `auto`) |
| B | 1 セル | 2 セル幅の字形(Moralerspace JPDOC など) | 字がはみ出す・隣のセルの背景で欠ける(目が大きな半円になる) | 端末からは測れない(下の「検出」) |

## 1. 今の使用箇所

テストを除く `internal/` と `cmd/` の Go のリテラル(文字列と rune。コメントは除く)と、埋め込みの `tapi.json`・`tips.toml` を走査し、`golang.org/x/text/width` で A と判定された字を出した(調べ方は末尾)。79 字。数は「リテラルの数」で、エージェントに渡す説明文や解析用の正規表現も含む。Web UI(`internal/daemon/webui`)はブラウザが描くので対象外。

| 区分 | 字 | 主な場所 | A のとき | B(JPDOC で測った字形)のとき | 影響 |
| --- | --- | --- | --- | --- | --- |
| 状態アイコン(unicode のセット) | `◆ ● ○ ◎ ▶ ⊙ ± ≡`、区切り `│` | `internal/theme/theme.go:220-223`(Sessions・Run・Wait・Idle・Unsent・Browser・Here・Issues・Diff・Changes・Sep) | 崩れる | `● ○ ◆ ◎ ≡ ±` がはみ出す。`▶ ⊙` は収まる | 大。セッション一覧の行頭で状態を見分ける字 |
| 状態アイコン(nerd のセット) | `●`(Unsent)、`▶`(Here)、`│`(Sep)と私用領域 25 字 | `internal/theme/theme.go:226-231` | 崩れる | `●` がはみ出す | 大 |
| ほかの状態表示 | `•`(未読の印)、`○`(レビューの見出し)、`●`(ロゴの点) | `internal/tui/view.go:324`、`review.go:638`、`render.go:304` | 崩れる | `● ○` がはみ出す。`•` は収まる | 中 |
| マスコット | 目と 1 セル表示の `• ●`(idle・work・done)、`˙`(Peek)、`╥`(ci_failed の目)、吹き出しの泡 `° ·` | `internal/mascot/tapi.json:17-26`、`mascot.go:306`、`internal/tui/tapi.go:380` | 崩れる(今は `auto` が ascii にする) | `●` がはみ出す(今の `narrow` の対象)。`• ˙ ° ·` は収まる。`╥` は JPDOC に無い | 大。顔は 1 か所の状態表示 |
| マスコットの輪郭 | `█ ▌ ▀ ▄`(`▐` と六分ブロックは N) | `tapi.json:9,13`、`mascot.go:101-105,349-382` | 崩れる(今は ascii の輪郭になる) | 端末が自前で描く | 中 |
| 本文の記号 | `·`(193 か所)、`…`(60)、`→`(32)、`←`(8)、`×`(7)、`—`(3)、`⇧`(3)、`↔`・`’`・`°`(各 1) | `·` は区切り(`view.go`・`dashboard.go`・`inbox.go`…)、`…` は `clip`(`render.go:21` の `ansi.Truncate` の末尾)と省略、`→ ←` は tips・ダッシュボード・CLI、`×` は寸法と閉じるボタン(`errcard.go`・`preview.go`) | 崩れる。`clip` の `…` は切り詰めた行すべてを 1 セル延ばすので、枠のある画面がほぼ全部ずれる | `… → ← × ±` がはみ出す(0.2〜0.5 セル)。`· — ˙ ° ’ ↔ ⇧` は収まる | A では大、B では小(次が空白なら読める) |
| 枠線・ブロック要素 | `─ ━ │ ┃ ┄ ┆ ┌ ┐ └ ┘ ┏ ┓ ┗ ┛ ├ ┤ ┼ ╭ ╮ ╯ ╰`、`▁〜▇ █ ▌ ▎ ▏ ▀ ▄` | `render.go:82-84`(箱)、`modals.go`、`errcard.go`、`markdown.go`(表)、`tour.go:496`、`render.go:245`(スパークライン) | 崩れる | 端末が自前で描く(下の「端末」) | A では大 |
| nerd font の私用領域 | U+E0B4・E0B6(キャップ)、U+E725〜F28B、U+F06A9 | `theme.go:226-231` | 端末による | nerd font の字形は 1.5 セルほど(後ろの空白まで使う作り) | 小(後ろが空白なら) |
| その他 | U+10EEEE | `internal/tui/kitty.go:200`。kitty の画像の置き場所を示す字で、字形は描かれない | — | — | なし |

曖昧幅でない(N)のに B で崩れる字もある。JPDOC で `⚠`(unicode の Warning)と `▱`(ダッシュボードの進み具合のバー)は全角の字形で、`▰` は入っていない(`dashboard.go:506` のコメントは「N なので崩れない」としているが、JPDOC では成り立たない)。HackGen35 Console では `✓ ✗` が 1.4 セルにはみ出す。EAW だけでは B を防げない。

## 2. 代わりの文字の候補

各フォントの cmap と字形の外接矩形を fontTools で読み、字形の右端が 1 セル(`0` の送り幅)に収まるかを見た。`o` は入っていて 1 セルに収まる、数字ははみ出す右端(セル単位)、`-` はフォントに無い(端末やOSの代替フォントで描かれ、大きさは環境次第)。

- Menlo・SF Mono・Monaco・Apple Symbols は macOS のもの。DejaVu Sans Mono 2.37・Cascadia Mono 2407.24・JetBrains Mono・Noto Sans Mono は公式の配布物を取ってきて測った。Moralerspace Neon JPDOC・UDEV Gothic 35NF・HackGen35 Console は手元に入っているもの
- **Consolas は手元に無く、確かめていない**(Windows 専用。Windows Terminal の既定は Cascadia Mono)
- Ghostty の既定の JetBrains Mono はバイナリに埋め込まれているので、配布物の JetBrains Mono で代用した

今使っている字:

| 字 | EAW | Menlo | SF Mono | Monaco | DejaVu | Cascadia | JetBrains | Noto Mono | JPDOC | UDEV | HackGen35C |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `●` | A | o | o | - | o | o | o | o | 1.5 | o | o |
| `•` | A | o | o | o | o | o | o | o | o | o | o |
| `◆` | A | o | - | o | o | o | o | 1.5 | 1.6 | o | o |
| `○` | A | o | o | - | o | o | o | o | 1.5 | o | o |
| `◎` | A | o | - | - | o | o | o | o | 1.5 | o | o |
| `▶` | A | o | o | - | o | o | o | 1.5 | o | o | o |
| `⊙` | A | o | - | - | o | - | o | 1.6 | o | o | o |
| `≡` | A | o | - | o | o | o | o | o | 1.3 | o | o |
| `±` | A | o | o | o | o | o | o | o | 1.2 | o | o |
| `·` | A | o | o | o | o | o | o | o | o | o | o |
| `…` | A | o | o | o | o | o | o | o | 1.5 | o | o |
| `→` | A | o | o | o | o | o | o | o | 1.5 | o | o |
| `×` | A | o | o | o | o | o | o | o | 1.2 | o | o |
| `◉` | N | o | - | - | o | o | o | o | o | o | o |
| `◕` | N | o | - | - | o | o | o | o | o | o | o |
| `⨯` | N | o | - | - | o | o | - | - | o | - | o |
| `◦` | N | o | o | - | o | o | o | o | o | o | o |
| `✓` | N | o | o | - | o | o | o | - | o | o | 1.4 |
| `✗` | N | o | o | - | o | - | o | - | o | o | 1.4 |
| `⚠` | N | o | - | - | o | - | o | - | 1.6 | o | 1.6 |
| `▰` | N | o | - | - | o | o | - | 1.8 | - | - | o |
| `▱` | N | o | - | - | o | o | - | 1.8 | 1.5 | o | o |

候補(issue に挙がったものと、そのほか):

| 字 | EAW | Menlo | SF Mono | Monaco | DejaVu | Cascadia | JetBrains | Noto Mono | JPDOC | UDEV | HackGen35C | 向く用途 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `∙` U+2219 | N | o | o | o | o | o | o | o | o | o | o | `·` `•` の代わり。**測った 10 書体すべてで収まる唯一の点**。小さいので `●` の代わりには弱い |
| `◦` U+25E6 | N | o | o | - | o | o | o | o | o | o | o | `○`(Idle)の代わり |
| `▸` U+25B8 | N | o | o | - | o | o | o | o | o | o | o | `▶`(Here)の代わり |
| `◊` U+25CA | N | o | o | o | o | o | o | o | o | o | o | `◆`(Sessions)の代わり。10 書体すべてで収まる |
| `◉` U+25C9 | N | o | - | - | o | o | o | o | o | o | o | `●`(Wait・Unsent)、`◎`(Browser)、`⊙`(Issues)の代わり。SF Mono・Monaco に無い |
| `◕` U+25D5 | N | o | - | - | o | o | o | o | o | o | o | (今の wait の目) |
| `‣` U+2023 | N | o | o | o | o | - | - | o | o | - | o | 箇条書きの印 |
| `∓` U+2213 | N | o | o | o | o | - | o | - | o | o | o | `±` の代わりには形が違う。ASCII の `+-` か `Δ` を検討 |
| `≣` U+2263 | N | o | - | - | o | o | o | - | o | o | o | `≡`(Changes)の代わり |
| `⋅` U+22C5 | N | o | - | - | o | - | - | - | o | - | o | 弱い(入っている書体が少ない) |
| `⋯` U+22EF | N | o | - | - | o | - | o | - | o | o | o | `…` の代わりには弱い |
| `➜` U+279C | N | o | - | - | o | - | o | - | o | o | o | `→` の代わりには弱い |
| `✕` U+2715 | N | o | - | - | o | - | o | - | - | o | 1.3 | 弱い |
| `✔ ✘` | N | o | - | - | o | - | - | - | o | - | 1.4〜1.5 | 弱い |
| `◘` U+25D8 | N | o | - | - | o | o | - | o | o | - | o | 弱い |
| `⊚` U+229A | N | o | - | - | o | - | - | 1.6 | o | - | o | 弱い |
| `🞄` U+1F784 | N | - | - | - | - | - | - | - | o | - | - | JPDOC 専用(今の `narrow`) |
| `⬤` `⬥` | N | - | - | - | - | o | - | - | o | - | - | 弱い |
| `⦁` `⏺` | N | - | - | - | - | - | - | - | - | - | - | 使えない |

読み取れること:

- issue の候補 `∙ ◦ ◉ ◕ ⨯ ✓ ✗` のうち、そのまま既定にできるのは `∙`(全書体)と `◦`(Monaco 以外)。`◉ ◕` は SF Mono・Monaco に無い(Terminal.app の既定は SF Mono、Monaco は古い iTerm2 の既定)。`⨯` は JetBrains Mono(Ghostty の既定)・Noto・SF Mono に無いので、今の error の目 `⨯` もすでに代替フォント頼み
- `●` の代わりに「どの書体にもあって、大きくて、1 セルに収まる黒丸」は見つからなかった。状態アイコンは「`◉`(SF Mono では代替)」か「`∙`(小さい)」のどちらかを選ぶことになる。マスコットの目は表情の設計なので、マスコットの担当の判断にする
- Noto Sans Mono は `◆ ▶ ⊙ ▱` などを 2 セル分の送り幅で持っていて、日本語フォントでなくても B が起きる

## 3. 幅計算

pekoe の幅の数え方はすべて `charmbracelet/x/ansi` に集まっている。

- `lipgloss.Width`・枠線・`Place`・`JoinHorizontal` などは `ansi.StringWidth`。pekoe の `w()`(`internal/tui/render.go:14`)と `clip`(`ansi.Truncate`)も同じ。bubbletea の描画(`standard_renderer.go`)も行を `ansi.StringWidth` / `ansi.Truncate` で端末の幅に切る
- `ansi.StringWidth` は書記素ごとに `clipperhouse/displaywidth` で数える。曖昧幅を 2 にするのは `displaywidth.Options.EastAsianWidth`。x/ansi はこれを**パッケージ内の変数に持ち、`init` で環境変数 `RUNEWIDTH_EASTASIAN` を読んだときだけ true にする**(`x/ansi@v0.11.8/method.go`)。外から切り替える API は無い
- `go-runewidth` は x/ansi の `StringWidthWc` 経由でだけ入っていて、こちらも x/ansi 自前の `Condition`(同じ環境変数)を使う。runewidth の `DefaultCondition` はロケールが `ja_JP` だと曖昧幅を 2 にするが、pekoe からは使われていない
- `rivo/uniseg` は `withCursor`(`view.go:723`)で使っていて、公開の変数 `uniseg.EastAsianAmbiguousWidth` で実行中に切り替えられる

手元で実測した(`LANG=ja_JP.UTF-8`):

| | `●` `·` `→` `─` `█` 私用領域 | 角丸の枠 `ab` |
| --- | --- | --- |
| 既定 | `ansi.StringWidth` = 1、`lipgloss.Width` = 1、`uniseg` = 1、`runewidth.StringWidth`(DefaultCondition)= 2 | 4 |
| `RUNEWIDTH_EASTASIAN=1` | `ansi` = 2、`lipgloss` = 2、`uniseg` = 1 のまま | 6(枠線も 2 と数える) |

つまり「曖昧幅を 2 と数える」ことは環境変数 1 つで pekoe 全体に効くが、`init` で読むので、CPR で測った結果に合わせるには測ってから自分を exec し直すしかない。もう一つの問題は、pekoe が枠を手で組み立てている箇所(`render.go:82` の `"╭" + strings.Repeat("─", n) + "╮"`、ツアーのカードの `inner := width - 4` など)が「枠線は 1 セル」を前提にしていること。枠線を 2 と数えるなら、これらを全部直すことになる。

エージェントの画面の取り込み: プレビューは `tmux capture-pane -p -e` の行を `clip` で切って並べる(`view.go:700` 付近)。tmux は曖昧幅を utf8proc か libc の `wcwidth()` で 1 と数え、エージェント(Claude Code の `●` `⎿` など)もその前提で画面を作る。A の端末ではこの取り込んだ行が 1 字ごとにずれるが、中身はエージェントのものなので置き換えられない。pekoe にできるのは、行を 2 で数えて切り、枠を押し出さないことだけ。tmux 3.6 の `codepoint-widths` で tmux 側の数え方を変えることもできるが、エージェントのレイアウトとずれる(手元の tmux は 3.5a で、この設定は無い)。

## 4. 検出

今の `auto`(`internal/mascot/detect.go`): TUI が端末を取る前に、行頭で `●` を出して `ESC[6n` で桁を聞き、3 桁目以降なら曖昧幅が広い(A)とみなす。300ms で返事が無ければ判定しない。結果は `[mascot] glyphs` の `auto` だけに効き、マスコットを ascii にする。ダッシュボードの絵(`dashGlyphs`)もこれを見ているが、アイコンセット・本文・枠線は変わらない。設定の再読み込みでは測り直さない(TUI が端末を持っているため)。

B を自動で見分けられるか:

- CPR は端末の数え方しか返さない。B は端末が 1 と数えるので CPR では測れない
- 主要な端末の公式資料に、グリフを描いた幅を返す仕組みは見つからなかった。取れるのは kitty の XTGETTCAP でフォント名(`font_family`)、iTerm2 の `OSC 1337 ; ReportCellSize` でセルの寸法まで(出典は次節)
- フォント名が取れても、そのフォントを探して cmap と字形を読み、代替フォントの選ばれ方まで真似る必要がある。kitty 以外ではフォント名も取れない(Ghostty は `ghostty +show-config` で設定は読めるが、端末の外から読むことになる)。費用に見合わないので、自動判定はしない

結論: B は検出しない。既定の表を B でも崩れない字にして、設定なしで済むようにする。

## 5. 端末

| 端末 | 曖昧幅を 2 にする設定 | 枠線・ブロックを自前で描くか | 私用領域(nerd font) | 出典 |
| --- | --- | --- | --- | --- |
| Ghostty | 無い(設定の一覧に ambiguous が無い。`grapheme-width-method` は書記素の数え方) | 描く。1.2.0 のリリースノートで U+2500–25FF の範囲、U+1FB00–1FBFF(六分ブロック)などを自前のスプライトで描くとしている。切る設定は無い | 次のセルが空白なら 2 セルまで広げて描く。1.2 から nerd font の字形をセルに合わせて縮める | [設定](https://ghostty.org/docs/config/reference)、[1.2.0](https://ghostty.org/docs/install/release-notes/1-2-0)、[cell.zig の constraintWidth](https://github.com/ghostty-org/ghostty/blob/main/src/renderer/cell.zig) |
| kitty | 無い | 描く(罫線・Symbols for Legacy Computing・Powerline)。太さは `box_drawing_scale` | 後ろが空白なら 2 セルで描く。`narrow_symbols` で字ごとに止められる | [kitty.conf](https://sw.kovidgoyal.net/kitty/conf/)、[changelog](https://sw.kovidgoyal.net/kitty/changelog/)、[FAQ](https://sw.kovidgoyal.net/kitty/faq/) |
| WezTerm | `treat_east_asian_ambiguous_width_as_wide`(既定 false)。nightly の `cell_widths` で字ごとにも | `custom_block_glyphs`(既定 true。U+2500・U+2580・U+1FB00 の各ブロック、点字、Powerline) | `allow_square_glyphs_to_overflow_width`(既定 `WhenFollowedBySpace`) | [ambiguous](https://wezterm.org/config/lua/config/treat_east_asian_ambiguous_width_as_wide.html)、[custom_block_glyphs](https://wezterm.org/config/lua/config/custom_block_glyphs.html)、[overflow](https://github.com/wezterm/wezterm/blob/main/docs/config/lua/config/allow_square_glyphs_to_overflow_width.md) |
| iTerm2 | 「Ambiguous characters are double-width」(既定値は資料に無く、未確認) | 罫線とブロック要素は常に自前(ソース)。「Use built-in Powerline glyphs」。六分ブロックは未確認 | 資料に無い(未確認) | [Profiles > Text](https://iterm2.com/documentation-preferences-profiles-text.html)、[iTermBoxDrawingBezierCurveFactory.m](https://github.com/gnachman/iTerm2/blob/master/sources/Drawing/iTermBoxDrawingBezierCurveFactory.m) |
| Alacritty | 無い(文字幅の設定自体が無い) | `font.builtin_box_drawing`(既定 true。U+2500–259F、U+1FB00–1FB3B、Powerline の一部) | 資料に無い(未確認) | [設定](https://alacritty.org/config-alacritty.html) |
| Windows Terminal | `compatibility.ambiguousWidth`(`narrow` 既定 / `wide`。1.25 から、安定版は v1.25.2733.0) | `builtinGlyphs`(既定 true。罫線・ブロック・Powerline・legacy computing) | 未確認 | [v1.25.2733.0](https://github.com/microsoft/terminal/releases/tag/v1.25.2733.0)、[profiles.schema.json](https://github.com/microsoft/terminal/blob/main/doc/cascadia/profiles.schema.json) |
| tmux | 専用の設定は無く、utf8proc か `wcwidth()` で決まる。3.6 から `codepoint-widths` で字ごとに上書き | — | — | [CHANGES](https://github.com/tmux/tmux/blob/master/CHANGES)、[utf8.c](https://github.com/tmux/tmux/blob/master/utf8.c) |

枠線とブロック要素は、6 つとも既定で自前で描く(iTerm2 は六分ブロックだけ未確認)。B の例外にしてよい。ただし A の端末(iTerm2・WezTerm・Windows Terminal で設定を入れたとき)では、自前で描いても 2 セルと数えるので崩れる。

私用領域は、Ghostty・kitty・WezTerm が「後ろが空白なら 2 セルまで使って描く」作りで、nerd font の非 Mono 版も字形が 1.5 セルほどある(Hack Nerd Font で 1890/1233、JPDOC に入っている nerd の字形も 920/600)。Mono 版は 1 セルに収まる。

## 決めること

### (1) 本文の記号をどこまで対象にするか

| 案 | 内容 | 長所 | 短所 |
| --- | --- | --- | --- |
| a | 状態表示だけ置き換える。本文は今のまま | 変更が小さい。見た目がほぼ変わらない | A では本文と `clip` の `…` で全体が崩れたまま |
| b | 本文も既定で N の字か ASCII に置き換える | A でも B でも崩れない | `…` `→` を ASCII(`...` `->`)にすると見た目と幅が変わる。N の代わりの字は書体の対応が弱い(`⋯` `➜` は SF Mono・Cascadia・Noto に無い)。`· → ∙` だけは全書体にあるが、そもそも `·` は B で崩れない |
| c | 既定は a。A と判定(または設定)したときだけ、本文・枠線を含めて A の字を一切使わない表に切り替える | 普通の環境の見た目を変えず、A で崩れない | 表を 2 つ持ち、テストで両方を守る |

おすすめ: **c**。B での本文の記号のはみ出しは 0.2〜0.5 セルで、次が空白なら読める。A では `clip` の `…` だけで枠のある画面が全部ずれるので、本文まで替えないと意味がない。

### (2) A のとき、幅を 2 として数えるか、置き換えるか

| 案 | 内容 | 長所 | 短所 |
| --- | --- | --- | --- |
| a | 幅計算を 2 にする(`RUNEWIDTH_EASTASIAN=1` を立てて exec し直す) | lipgloss・bubbletea・`w()`・`clip` に一度に効く。エージェントの画面の取り込みも正しく切れる | 枠線も 2 と数えるので、枠を手で組んでいる箇所(`render.go`・`modals.go`・`errcard.go`・`tour.go` …)を全部直すことになる。枠が 2 セル幅になり画面が狭くなる |
| b | pekoe が描く字をすべて置き換える(枠線は ASCII の `+ - |` かアイコンの ascii セット相当に) | 幅計算に触らない。今の `glyphs = "ascii"` の延長 | エージェントの画面の取り込みは置き換えられないので、プレビューの行がずれて枠を押し出す |
| c | b に加えて、`RUNEWIDTH_EASTASIAN=1` で exec し直す(pekoe の字はもう曖昧幅を含まないので、2 と数えて困るのは取り込んだ画面だけになる) | 自前の画面もプレビューも崩れない。枠の計算は直さなくてよい | 起動時の exec し直し(CPR → 環境変数を立てて `syscall.Exec`)。設定の再読み込みでは切り替えられない(今の `auto` と同じ) |

おすすめ: **c**。exec し直しを避けたいなら、プレビューの `clip` だけ自前の切り詰め(`displaywidth.Options{EastAsianWidth: true}` で数える)にする手もあるが、bubbletea の行の切り詰めには効かないので、exec し直しのほうが確実。

### (3) nerd font の私用領域

| 案 | 内容 | 長所 | 短所 |
| --- | --- | --- | --- |
| a | 例外として許す。アイコンの後ろは必ず空白にする(`theme.Prefix` で今もそうしている)のをテストで守る | nerd font と端末(Ghostty・kitty・WezTerm)の作りに合っている。1.5 セルの字形は後ろの空白に収まる | 空白を挟まない使い方を足すと崩れる(テストで防ぐ) |
| b | A の表では nerd を ascii(か曖昧幅を使わない unicode)のセットに落とす | A で崩れない | A の人は nerd のアイコンを使えない |
| c | nerd をやめる | — | 既定の `icons = "nerd"` の利用者の見た目が変わる |

おすすめ: **a + b**。nerd のセットにある私用領域でない曖昧幅の字(`Unsent ●`・`Here ▶`)は、unicode のセットと同じ置き換え(`◉`・`▸`)をする。区切りの `│` は枠線の例外。

## 設定の形と移行

```toml
[theme]
icons     = "nerd"   # nerd | unicode | ascii(今のまま)
ambiguous = "auto"   # auto | narrow | wide
```

- 置き場所は issue の案の `[ui]` ではなく `[theme]` をすすめる。字の表を選ぶ `icons` がすでにここにあり、新しい節を増やさずに済む(節を増やすと tips の網羅テストにも節が 1 つ増える)
- `narrow`: 端末が曖昧幅を 1 と数える。既定の表(状態表示に曖昧幅なし、本文・枠線・私用領域は許す)
- `wide`: 端末が 2 と数える。曖昧幅を一切使わない表(本文は `... -> <- x`、枠線は ASCII、アイコンは nerd でも ascii 相当、マスコットは今の ascii)にして、取り込んだ画面を 2 と数える
- `auto`: 今の CPR で決める。測れなければ `narrow`
- B(フォントだけが全角)のための値は置かない。既定の表で崩れないようにするのが方針で、本文の記号のはみ出しまで消したい人は `wide` を選べば消える(取り込んだ画面が少し早めに切れるだけ)

`mascot.glyphs` からの移行:

| 今の値 | これから |
| --- | --- |
| `auto` | 既定のまま。マスコットは `[theme] ambiguous` の結果(と `icons = "ascii"`)に従う |
| `unicode` | 意味は同じ(ascii に落とさない)。ただし既定の表の字になる |
| `narrow` | 読み続けるが何もしない(既定の表で `●` を使わなくなるので不要)。`pekoe config check` で「不要になった」と出す |
| `ascii` | そのまま(見た目の好みとして残す) |

`[mascot] glyphs` そのものを消すかは、`unicode` と `ascii` の選択肢を残す価値があるかで決める(今は残す案)。

検出できない B を「見て選んでもらう」画面(issue の方針 3)は、既定の表が崩れなくなれば要らなくなるので、最初はやらない。`wide` を B の人にもすすめたくなったら、ツアーの先頭に `● ◉ ∙` を並べる案を検討する。

## 実装の段階(PR の単位)

1. **テストの土台**(振る舞いは変えない): マスコットの spec、`IconSet` の unicode / nerd、未読の印などの既定の字に曖昧幅が入ったら落ちるテスト。枠線(U+2500–257F)・ブロック要素(U+2580–259F)・私用領域を許可リストに、今ある違反を「既知の違反」のリストに入れ、後の PR で減らす。曖昧幅でなくても日本語フォントで全角に描かれる字(`⚠ ▱ ▪`)は、EAW だけでは拾えないので、字ごとに確かめた許可リスト(この文書の表)にする案も合わせて決める。`theme.Confetti` のテストが先例
2. **状態アイコンの置き換え**: unicode / nerd のセットの `● ○ ◆ ◎ ▶ ⊙ ± ≡`、未読の `•`(B では崩れないので `wide` の表だけでもよい)、ロゴの `●`、`⚠`。アイコンの後ろの空白のテスト
3. **マスコットの置き換え**(`internal/mascot` の担当と相談): 目と 1 セル表示の `● •`、`˙`、`╥`。`narrowGlyph` をなくし、`mascot.glyphs = "narrow"` を読むだけにする。チュートリアルのカードの顔をステップごとの表情に戻す。`docs/mascot.md` の互換性の表を書き直す
4. **`[theme] ambiguous` と `wide` の表**: 検出を `internal/mascot` から、マスコット以外も使える場所に移す。本文の記号・枠線・アイコンを `wide` の表に切り替える仕組み(`clip` の `…` を含む)。`RUNEWIDTH_EASTASIAN` を立てて exec し直す。`pekoe config check`、tips、README の設定の説明
5. **ドキュメント**: `docs/mascot.md`・`docs/ui-guidelines.md`・README を特定のフォントを前提にしない書き方に(JPDOC は例として残す)。`dashboard.go:506` のコメントの直し(▰ ▱ は JPDOC で崩れる)

1 は単独で入れられる。2 と 3 は互いに独立で、1 の既知の違反を減らす。4 は 2・3 の後(`wide` の表が置き換え後の字を前提にするため)。

## 付録: 調べ方

いずれも使い捨てのスクリプト(リポジトリには入れていない)。

- 使用箇所: `go/parser` でテスト以外の `.go` を読み、文字列と rune のリテラル(コメントは除く)を取り出し、`tapi.json`・`tips.toml` は行ごとに、`golang.org/x/text/width.LookupRune(r).Kind() == width.EastAsianAmbiguous` の字を場所と一緒に出した
- フォント: fontTools で `getBestCmap()` と字形の外接矩形(`BoundsPen`)を読み、右端 ÷ `0` の送り幅を出した。1.05 以下を「収まる」とした。端末が字形を縮めるか(Ghostty は nerd font の字形を縮める)はこの数字に入っていない
- 幅計算: pekoe と同じ版の依存(x/ansi v0.11.8、lipgloss v1.1.0、uniseg v0.4.7、go-runewidth v0.0.24)で `ansi.StringWidth`・`lipgloss.Width`・`uniseg.StringWidth`・`runewidth.StringWidth` を、`RUNEWIDTH_EASTASIAN` あり・なしで比べた
