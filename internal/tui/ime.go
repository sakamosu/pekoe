package tui

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// IME の変換ウィンドウ(preedit / 候補窓)は端末の「本物のカーソル」の位置に出る。
// Bubble Tea v1 はフレームごとにカーソルを最終行の左端へ戻すので、そのままだと
// 変換中の文字が左下(ステータスバー)に出てしまう。
//
// そこで、偽カーソルを描く場所に幅 0 のマーカー(APC 文字列)を埋め込み、View の
// 最後でその座標を拾って取り除く。出力 writer は描画のたびに本物のカーソルを
// その座標へ移す。alt screen の描画は毎回 CursorHome から始まるので、末尾で
// カーソルを動かしても次のフレームは崩れない。

// cursorMark は入力位置を示す幅 0 のマーカー。ansi.Truncate / StringWidth は
// APC を制御列として扱うので、レイアウト処理を素通りする。
const cursorMark = "\x1b_boba-cursor\x1b\\"

// extractCursor は lines からマーカーを取り除き、最初に見つかった位置(0 始まりの
// 列・行)を返す。見つからなければ ok=false。
func extractCursor(lines []string) (x, y int, ok bool) {
	for i, l := range lines {
		j := strings.Index(l, cursorMark)
		if j < 0 {
			continue
		}
		if !ok {
			x, y, ok = ansi.StringWidth(l[:j]), i, true
		}
		lines[i] = strings.ReplaceAll(l, cursorMark, "")
	}
	return x, y, ok
}

// imeCursor は View が決めた入力位置を、描画 goroutine の writer に渡す。
type imeCursor struct {
	mu   sync.Mutex
	x, y int
	ok   bool
}

func (c *imeCursor) set(x, y int, ok bool) {
	c.mu.Lock()
	c.x, c.y, c.ok = x, y, ok
	c.mu.Unlock()
}

func (c *imeCursor) get() (int, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.x, c.y, c.ok
}

// imeWriter は Bubble Tea の出力をそのまま流し、書き込みのたびに本物の
// カーソルを入力位置へ移す(カーソル自体は Bubble Tea が隠したまま)。
// *os.File を埋め込むのは、Bubble Tea が term.File(Fd)として TTY 判定・
// サイズ取得を続けられるようにするため。
//
// フレームとカーソル移動は 1 回の write にまとめ、同期出力(DEC 2026)で囲む。
// 分けて書くと、端末がその間で描画したときにカーソル(= IME の preedit)が
// 一瞬左下へ飛び、書き換え途中の行も見えてチカチカする。2026 を知らない
// 端末はこのモード指定を無視する。
type imeWriter struct {
	*os.File
	cur *imeCursor
}

const (
	syncBegin = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
)

func (w imeWriter) Write(p []byte) (int, error) {
	buf := make([]byte, 0, len(p)+len(syncBegin)+len(syncEnd)+16)
	buf = append(buf, syncBegin...)
	buf = append(buf, p...)
	if x, y, ok := w.cur.get(); ok {
		buf = fmt.Appendf(buf, "\x1b[%d;%dH", y+1, x+1)
	}
	buf = append(buf, syncEnd...)
	if _, err := w.File.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}
