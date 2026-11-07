package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"boba/internal/daemon"
	"boba/internal/engine"
	"boba/internal/state"
)

func TestFitCells(t *testing.T) {
	cases := []struct {
		imgW, imgH, maxC, maxR, cw, ch int
		wantC, wantR                   int
	}{
		// 1280×800 in 100×40 cells of 10×20 px: 128×40 cells natural → width binds
		{1280, 800, 100, 40, 10, 20, 100, 31},
		// a phone shot (780×1688) in the same box: height binds
		{780, 1688, 100, 40, 10, 20, 37, 40},
		// unknown cell size: assume cells twice as tall as wide
		{1000, 1000, 80, 80, 0, 0, 80, 40},
		// never zero, never over the box
		{10000, 10, 50, 50, 10, 20, 50, 1},
		{0, 100, 50, 50, 10, 20, 0, 0},
	}
	for _, c := range cases {
		gc, gr := fitCells(c.imgW, c.imgH, c.maxC, c.maxR, c.cw, c.ch)
		if gc != c.wantC || gr != c.wantR {
			t.Errorf("fitCells(%d×%d in %d×%d, cell %d×%d) = %d×%d, want %d×%d", c.imgW, c.imgH, c.maxC, c.maxR, c.cw, c.ch, gc, gr, c.wantC, c.wantR)
		}
	}
}

func TestGraphicsSequences(t *testing.T) {
	if got := deleteImage(0x0C0001); got != "\x1b_Ga=d,d=I,i=786433,q=2\x1b\\" {
		t.Fatalf("delete: %q", got)
	}
	tx := transmitImage(0x0C0002, "/x/shot-1.png", 40, 12)
	if !strings.HasPrefix(tx, "\x1b_Ga=T,t=f,f=100,U=1,q=2,i=786434,c=40,r=12;") || !strings.HasSuffix(tx, "\x1b\\") {
		t.Fatalf("transmit: %q", tx)
	}
	// placeholder rows: row n carries the n-th diacritic (needs the full table past 8 rows)
	if len(placeholderDiacritics) != 297 {
		t.Fatalf("diacritics: %d", len(placeholderDiacritics))
	}
	r := []rune(placeholderRow(0x0C0002, 20, 3))
	var cells []rune
	for _, x := range r {
		if x == placeholder || (x >= 0x300 && x < 0x10000 && x != '[' && x != ';' && x != 'm' && (x < '0' || x > '9')) {
			cells = append(cells, x)
		}
	}
	if len(cells) < 3 || cells[0] != placeholder || cells[1] != placeholderDiacritics[20] || cells[2] != placeholderDiacritics[0] {
		t.Fatalf("row 20: %U", cells)
	}
	if !strings.HasPrefix(placeholderRow(0x0C0002, 0, 1), "\x1b[38;2;12;0;2m") {
		t.Fatal("the image id travels in the foreground color")
	}
}

func TestGfxSync(t *testing.T) {
	n := 0
	alloc := func() int { n++; return 100 + n }
	a := gfxKey{"/a.png", 40, 20}
	b := gfxKey{"/b.png", 40, 20}

	seq, live := gfxSync(nil, []gfxKey{a}, alloc)
	if live[a] != 101 || strings.Count(seq, "a=T") != 1 || strings.Contains(seq, "a=d") {
		t.Fatalf("first: %q %v", seq, live)
	}
	// same image, same size: nothing to send
	if seq, live2 := gfxSync(live, []gfxKey{a}, alloc); seq != "" || live2[a] != 101 {
		t.Fatalf("unchanged: %q", seq)
	}
	// new shot: the old one is deleted before the new one is sent
	seq, live = gfxSync(live, []gfxKey{b}, alloc)
	if live[b] != 102 || len(live) != 1 {
		t.Fatalf("swap: %v", live)
	}
	if d, tx := strings.Index(seq, deleteImage(101)), strings.Index(seq, "i=102"); d < 0 || tx < 0 || d > tx {
		t.Fatalf("swap order: %q", seq)
	}
	// resize: same file at another size is another image
	c := gfxKey{"/b.png", 30, 15}
	seq, live = gfxSync(live, []gfxKey{c}, alloc)
	if !strings.Contains(seq, deleteImage(102)) || live[c] != 103 {
		t.Fatalf("resize: %q", seq)
	}
	// leaving the preview: everything goes
	seq, live = gfxSync(live, nil, alloc)
	if seq != deleteImage(103) || len(live) != 0 {
		t.Fatalf("leave: %q %v", seq, live)
	}
}

func shots() *daemon.Preview {
	now := time.Now()
	return &daemon.Preview{
		Slug: "a", Port: 3001, Path: "/settings", Rev: 5,
		After:  &daemon.Shot{File: "/s/shot-2.png", Width: 1280, Height: 800, At: now},
		Before: &daemon.Shot{File: "/s/shot-1.png", Width: 1280, Height: 800, At: now.Add(-time.Minute)},
	}
}

func TestPreviewLayout(t *testing.T) {
	p := shots()
	one := previewLayout(p, false, 100, 40, 10, 20)
	if len(one) != 1 || one[0].shot != p.After || one[0].cols != 100 || one[0].rows != 31 || one[0].x != 0 || one[0].y != 4 {
		t.Fatalf("single: %+v", one)
	}
	two := previewLayout(p, true, 100, 40, 10, 20)
	if len(two) != 2 || two[0].shot != p.Before || two[1].shot != p.After || two[0].label != "before" {
		t.Fatalf("compare: %+v", two)
	}
	if two[0].x+two[0].cols > two[1].x || two[1].x+two[1].cols > 100 || two[0].y < 1 {
		t.Fatalf("compare overlaps or overflows: %+v", two)
	}
	// too narrow to place side by side: c shows before alone
	narrowL := previewLayout(p, true, 40, 40, 10, 20)
	if len(narrowL) != 1 || narrowL[0].shot != p.Before {
		t.Fatalf("narrow compare: %+v", narrowL)
	}
	// compare without an earlier shot: just after
	p.Before = nil
	if l := previewLayout(p, true, 100, 40, 10, 20); len(l) != 1 || l[0].shot != p.After {
		t.Fatalf("no before: %+v", l)
	}
}

func previewModel(t *testing.T, graphics bool) *model {
	t.Helper()
	m := screenModel(t)
	m.sessions = []*state.Session{
		{Slug: "a", Branch: "a", State: state.Done, Port: 3001},
		{Slug: "b", Branch: "b", State: state.Done, Port: 3002},
	}
	m.graphics = graphics
	m.pv.cellW, m.pv.cellH = 10, 20
	m.tapi.splash = false
	return m
}

func viewLines(m *model) []string { return strings.Split(m.View(), "\n") }

func plain(lines []string) string { return ansi.Strip(strings.Join(lines, "\n")) }

func TestPreviewPlaceholderWithoutGraphics(t *testing.T) {
	m := previewModel(t, false)
	m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m.window != winPreview || m.focus != pAgent {
		t.Fatalf("v: window %q focus %d", m.window, m.focus)
	}
	m.previewSync()
	if m.pv.watching != "a" {
		t.Fatalf("watching %q", m.pv.watching)
	}
	m.setPreview(shots())
	out := plain(viewLines(m))
	for _, want := range []string{"[4]", "Preview a :3001/settings", "after 1280×800 · png", "┄┄┄", "┆", "before 1280×800"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.ContainsRune(out, placeholder) {
		t.Error("placeholder cells drawn on a terminal without graphics")
	}
	if len(m.pv.live) != 0 {
		t.Error("images transmitted to a terminal without graphics")
	}
	// mobile and busy show in the title; bottom bar keys are this window's
	p := shots()
	p.Mobile, p.Busy, p.Rev = true, true, 6
	m.setPreview(p)
	out = plain(viewLines(m))
	if !strings.Contains(out, ":3001/settings · mobile · capturing") || !strings.Contains(out, "R retake") || !strings.Contains(out, "m desktop") {
		t.Errorf("title / bar:\n%s", out)
	}
	// an older event does not overwrite a newer state
	old := shots()
	old.Rev = 1
	m.setPreview(old)
	if !m.pv.view.Mobile {
		t.Error("a stale event replaced the preview")
	}
}

func TestPreviewDevServerDownMessage(t *testing.T) {
	m := previewModel(t, false)
	m.openPreview()
	m.previewSync()
	m.setPreview(&daemon.Preview{Slug: "a", Port: 3001, Path: "/", Error: "dev server is not running · d to start"})
	if out := plain(viewLines(m)); !strings.Contains(out, "dev server is not running · d to start") {
		t.Fatalf("no dev-down message:\n%s", out)
	}
}

func TestPreviewImagesFollowLayoutAndLeaveNothing(t *testing.T) {
	m := previewModel(t, true)
	m.openPreview()
	m.previewSync()
	m.setPreview(shots())
	m.previewSync()
	if len(m.pv.live) != 1 {
		t.Fatalf("live after open: %v", m.pv.live)
	}
	var id int
	for _, v := range m.pv.live {
		id = v
	}
	out := strings.Join(viewLines(m), "\n")
	if !strings.Contains(out, fmt.Sprintf("\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)) || !strings.ContainsRune(out, placeholder) {
		t.Fatal("the image's placeholder cells are not drawn")
	}
	// c: before and after side by side → two images
	m.keyPreview("c")
	m.previewSync()
	if len(m.pv.live) != 2 {
		t.Fatalf("compare: %v", m.pv.live)
	}
	// resize: new sizes replace the old images
	before := m.pv.live
	m.width, m.height = 150, 40
	m.previewSync()
	for k := range before {
		if _, ok := m.pv.live[k]; ok {
			t.Fatalf("image kept at its old size after resize: %+v", k)
		}
	}
	// another session: the preview follows, the old images are dropped
	m.focus = pSessions
	m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.window != winPreview {
		t.Fatal("the preview should follow the selection")
	}
	m.previewSync()
	if m.pv.watching != "b" || len(m.pv.live) != 0 || m.pv.view != nil {
		t.Fatalf("after switch: watching %q live %v view %v", m.pv.watching, m.pv.live, m.pv.view)
	}
	// esc goes back to the agent window; nothing is watched any more
	m.focus = pAgent
	m.onKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.previewSync()
	if m.window != engine.WinAgent || m.pv.watching != "" {
		t.Fatalf("esc: window %q watching %q", m.window, m.pv.watching)
	}
}

func TestPreviewWindowCycle(t *testing.T) {
	m := previewModel(t, false)
	m.sessions[0].Dev = true
	m.focus = pAgent
	key := func(k string) { m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}) }
	key("v")
	if m.window != winPreview {
		t.Fatalf("agent → %q", m.window)
	}
	key("v")
	if m.window != engine.WinDev {
		t.Fatalf("preview → %q", m.window)
	}
	key("v")
	if m.window != engine.WinAgent {
		t.Fatalf("dev → %q", m.window)
	}
	// typing from the sessions panel leaves the preview for the agent's pty
	key("v")
	m.focus = pSessions
	m.onKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.window != engine.WinAgent || !m.typing {
		t.Fatalf("enter: window %q typing %v", m.window, m.typing)
	}
}

func TestPreviewNeedsPort(t *testing.T) {
	m := previewModel(t, false)
	m.sessions[0].Port = 0
	if m.openPreview() == nil || m.window == winPreview {
		t.Fatal("a session without a port has nothing to preview")
	}
}
