package tui

// Tips (internal/tips) in the empty main pane: under tapi's big face while
// no session is selected or a worktree is being created — the moments the
// user waits anyway. One tip at a time; "." shows the next. The text is
// Japanese (one of the two exceptions to English UI text, with the
// tutorial's cards; docs/ui-guidelines.md).

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/state"
	"pekoe/internal/theme"
	"pekoe/internal/tips"
)

const (
	tipKey = "."
	// tipAgain: when the empty pane shows up again after this long, it gets
	// a fresh tip (moving the selection over sessions does not burn them)
	tipAgain    = 30 * time.Second
	tipWidthMax = 60 // the block's widest line; wider reads poorly
	tipWidthMin = 20 // narrower than this, no tip at all
)

type tipState struct {
	shown   *tips.Shown // nil: tips are off
	cur     *tips.Tip
	at      time.Time // when cur was picked
	visible bool      // the empty pane was on screen at the last update
}

// initTips opens the shown-tips record at path ("" keeps it in memory).
func (m *model) initTips(path string) {
	m.tip = tipState{}
	if m.g.TipsOn() {
		m.tip.shown = tips.Open(path)
	}
}

// tipSpot: the main panel shows the empty pane that carries a tip (no
// session selected, or the selected session's worktree is being created).
// Errors, PR, preview, an ended session and tapi chat get none.
func (m *model) tipSpot() bool {
	if m.tip.shown == nil || m.chat.open || m.issueShown() || m.review != nil || m.tapi.splash {
		return false
	}
	s := m.current()
	if s == nil {
		return true
	}
	return s.State == state.Creating && m.window != winPR && m.window != winPreview
}

// tipSync picks a tip when the empty pane appears (runs after every update).
func (m *model) tipSync() {
	if m.tip.shown == nil {
		return
	}
	on := m.tipSpot()
	if on && !m.tip.visible && (m.tip.cur == nil || time.Since(m.tip.at) >= tipAgain) {
		m.nextTip()
	}
	m.tip.visible = on
}

// nextTip shows another tip and records it.
func (m *model) nextTip() {
	t, err := m.tip.shown.Next()
	m.tip.cur, m.tip.at = &t, time.Now()
	if err != nil {
		m.setMsg("tips: "+err.Error(), true)
	}
}

// tipRows is the tip block for a pane width wide with room rows left under
// the caption: a blank row, the heading, the short line, then the detail.
// The detail goes first when rows or columns run short; nil when even the
// short line does not fit.
func (m *model) tipRows(width, room int) []row {
	t := m.tip.cur
	if t == nil || !m.tipSpot() {
		return nil
	}
	bw := min(width-2, tipWidthMax)
	if bw < tipWidthMin {
		return nil
	}
	p := m.prefixLabel()
	short := wrapTip(strings.ReplaceAll(t.Short, "^q", p), bw)
	detail := wrapTip(strings.ReplaceAll(t.Detail, "^q", p), bw)
	if len(short) > 2 || room < 2+len(short) {
		return nil
	}
	th := m.th
	lines := []string{th.FG(theme.Dim, "tip") + "  " + th.Bold(theme.Secondary, tipKey) + " " + th.FG(theme.Dim, "next")}
	for _, l := range short {
		lines = append(lines, th.FG(theme.Text, l))
	}
	if room >= 2+len(short)+len(detail) {
		for _, l := range detail {
			lines = append(lines, th.FG(theme.Dim, l))
		}
	}
	// a left-aligned block, centered as a whole under the face
	bwide := 0
	for _, l := range lines {
		bwide = max(bwide, w(l))
	}
	pad := strings.Repeat(" ", max((width-bwide)/2, 0))
	rows := []row{{}}
	for _, l := range lines {
		rows = append(rows, row{Text: pad + l})
	}
	return rows
}

// noLineStart are characters a wrapped line must not begin with (closing
// punctuation, the long vowel mark, small kana).
const noLineStart = "、。，．）」』・)ーぁぃぅぇぉっゃゅょァィゥェォッャュョ"

// wrapTip folds plain text to width cells, counting full-width characters
// as two. Japanese has no spaces: a line may break between any two
// full-width characters, but not inside an ASCII word (^q, pekoe, gh#N) nor
// before closing punctuation, ー or small kana (noLineStart).
func wrapTip(s string, width int) []string {
	var toks []string // words, single full-width characters and spaces
	var word []rune
	flush := func() {
		if len(word) > 0 {
			toks = append(toks, string(word))
			word = word[:0]
		}
	}
	for _, r := range s {
		switch {
		case r == ' ' || r == '\n':
			flush()
			toks = append(toks, " ")
		case ansi.StringWidth(string(r)) > 1 || r > 0x2000:
			flush()
			toks = append(toks, string(r))
		default:
			word = append(word, r)
		}
	}
	flush()
	var out []string
	var cur []string // tokens of the line being filled
	curW := 0
	emit := func() {
		if l := strings.TrimSpace(strings.Join(cur, "")); l != "" {
			out = append(out, l)
		}
		cur, curW = nil, 0
	}
	for _, t := range toks {
		tw := ansi.StringWidth(t)
		if t == " " && curW == 0 {
			continue
		}
		if curW+tw > width {
			// keep a character that must not begin a line with the one
			// before it, moving as many as it takes (キ ー 、 → all three)
			var carry []string
			head, cw := t, tw
			for len(cur) > 1 && strings.ContainsRune(noLineStart, []rune(head)[0]) {
				prev := cur[len(cur)-1]
				pw := ansi.StringWidth(prev)
				if prev == " " || cw+pw > width {
					break
				}
				carry = append([]string{prev}, carry...)
				cur, curW = cur[:len(cur)-1], curW-pw
				head, cw = prev, cw+pw
			}
			emit()
			for _, c := range carry {
				cur, curW = append(cur, c), curW+ansi.StringWidth(c)
			}
			if t == " " {
				continue
			}
		}
		for tw > width { // a word longer than the line: cut it
			cut := ansi.Truncate(t, width-curW, "")
			cur = append(cur, cut)
			emit()
			t = strings.TrimPrefix(t, cut)
			tw = ansi.StringWidth(t)
		}
		cur, curW = append(cur, t), curW+tw
	}
	emit()
	return out
}
