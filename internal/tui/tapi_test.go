package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

func tapiModel(t *testing.T, color string, mc config.Mascot) *model {
	t.Helper()
	t.Setenv("PEKOE_COLOR", color)
	m := newModel(nil, "")
	m.g.Mascot = mc
	m.buildTapi(nil)
	m.width, m.height = 100, 16
	m.tapi.splash = false
	return m
}

func TestTapiViewKeepsWidth(t *testing.T) {
	for _, c := range []struct {
		color string
		mc    config.Mascot
	}{{"truecolor", config.Mascot{Shape: "round"}}, {"ansi", config.Mascot{Shape: "block"}}, {"none", config.Mascot{Glyphs: "ascii"}}} {
		m := tapiModel(t, c.color, c.mc)
		now := time.Now()
		m.sessions = []*state.Session{
			{Slug: "a", Branch: "a", State: state.Failed, StateSince: now},
			{Slug: "b", Branch: "b", State: state.Creating, StateSince: now},
		}
		m.sel = 1
		m.tapiOnSession(state.Running, m.sessions[0], "b")
		if m.tapi.toast == nil {
			t.Fatal("unseen failure should toast")
		}
		views := []string{m.View()}
		m.modal = newConfirmModal(m, "Delete worktree", "x", "/tmp/b", true, nil).about("b")
		views = append(views, m.View())
		m.tapi.splash = true
		views = append(views, m.View())
		for vi, out := range views {
			lines := strings.Split(out, "\n")
			if len(lines) != m.height {
				t.Errorf("%s view %d: %d lines", c.color, vi, len(lines))
			}
			for i, l := range lines {
				if ansi.StringWidth(l) != m.width {
					t.Errorf("%s view %d line %d: width %d", c.color, vi, i, ansi.StringWidth(l))
				}
			}
		}
		if !strings.Contains(ansi.Strip(views[1]), "delete worktree b? [y/n]") {
			t.Errorf("%s: pekoe asks line missing", c.color)
		}
	}
}

func TestTapiToastRules(t *testing.T) {
	m := tapiModel(t, "none", config.Mascot{})
	s := &state.Session{Slug: "fix-ci", Branch: "fix-ci", State: state.Waiting, StateSince: time.Now()}
	m.tapiOnSession(state.Waiting, s, "") // no transition
	if m.tapi.toast != nil {
		t.Fatal("no toast without a state change")
	}
	m.tapiOnSession(state.Running, s, "fix-ci") // on screen
	if m.tapi.toast != nil {
		t.Fatal("no toast for the session on screen")
	}
	m.tapiOnSession(state.Running, s, "other")
	if m.tapi.toast == nil || m.tapi.toast.text != "fix-ci needs you" {
		t.Fatalf("toast=%+v", m.tapi.toast)
	}
	m.tapi.toast = nil
	m.tapiOnSession(state.Running, s, "other") // same episode
	if m.tapi.toast != nil {
		t.Fatal("same session episode toasts once")
	}
}

func TestNotifySeq(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "ghostty")
	if got := notifySeq("pekoe", "a;b\x1b"); got != "\x1b]777;notify;pekoe;a b \x07" {
		t.Fatalf("%q", got)
	}
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if got := notifySeq("pekoe", "x"); got != "\x1b]9;pekoe: x\x07" {
		t.Fatalf("%q", got)
	}
}

// 選択行の背景は、中の色付き部分(状態の点)のリセットで途切れず行末まで続く。
func TestSelectedRowKeepsBackground(t *testing.T) {
	m := tapiModel(t, "truecolor", config.Mascot{Shape: "round"})
	now := time.Now()
	m.sessions = []*state.Session{{Slug: "a", Branch: "fix/a", State: state.Waiting, StateSince: now}}
	m.sel = 0
	rows, _ := m.sessionRows(40)
	out := m.box(44, 7, boxOpt{rows: rows})
	line := out[4] // under the orchestrator heading, the tapi row and the repository heading
	bg := bgSeq(m.th, theme.Select)
	if !strings.Contains(line, bg) {
		t.Fatalf("no select background: %q", line)
	}
	// 最後のリセットを除き、\x1b[0m の直後には必ず背景が塗り直されている
	body := strings.TrimSuffix(line, "\x1b[0m")
	for i := strings.Index(body, "\x1b[0m"); i >= 0; {
		rest := body[i+len("\x1b[0m"):]
		if strings.Contains(rest, "fix/a") && !strings.HasPrefix(rest, bg) {
			t.Fatalf("background dropped before the name: %q", line)
		}
		j := strings.Index(rest, "\x1b[0m")
		if j < 0 {
			break
		}
		i += len("\x1b[0m") + j
	}
}

func splashModel(t *testing.T, color, glyphs string, width, height int) *model {
	t.Helper()
	m := tapiModel(t, color, config.Mascot{Glyphs: glyphs})
	m.width, m.height = width, height
	m.sessions = []*state.Session{{Slug: "a", Branch: "a", State: state.Running}}
	m.tapi.splash = true
	return m
}

// スプラッシュは全行が画面幅ちょうどで、背景色が行末まで塗られる。
// 以前は各行が「余白＋絵＋文字」の幅で止まり、paintBg（lipgloss）が
// 最長行までしか背景を塗らないので、文字の右端で背景が途切れていた。
// 幅3以上なら左右1桁は空ける（曖昧幅を全角で描くフォントのはみ出し対策）。
func TestSplashFillsScreen(t *testing.T) {
	for _, g := range []string{"unicode", "narrow", "ascii"} {
		for _, color := range []string{"truecolor", "none"} {
			for _, width := range []int{1, 2, 3, 10, 16, 20, 26, 30, 34, 45, 60, 131, 226} {
				for _, height := range []int{1, 4, 7, 9, 40} {
					m := splashModel(t, color, g, width, height)
					bg := bgSeq(m.th, theme.Bg)
					lines := strings.Split(m.View(), "\n")
					if len(lines) != height {
						t.Fatalf("%s/%s %dx%d: %d lines", g, color, width, height, len(lines))
					}
					for i, l := range lines {
						where := fmt.Sprintf("%s/%s %dx%d line %d", g, color, width, height, i)
						if got := ansi.StringWidth(l); got != width {
							t.Fatalf("%s: width %d: %q", where, got, l)
						}
						if s := ansi.Strip(l); width >= 3 && (s[0] != ' ' || s[len(s)-1] != ' ') {
							t.Fatalf("%s: touches the edge: %q", where, s)
						}
						if color != "truecolor" {
							continue
						}
						// リセットの直後には必ず背景が塗り直されている（最後のリセットを除く）
						body := strings.TrimSuffix(l, "\x1b[0m")
						if !strings.HasPrefix(body, bg) {
							t.Fatalf("%s: no background: %q", where, l)
						}
						for _, part := range strings.Split(body, "\x1b[0m")[1:] {
							if !strings.HasPrefix(part, bg) {
								t.Fatalf("%s: background dropped: %q", where, l)
							}
						}
					}
				}
			}
		}
	}
}

// 幅に応じて「たぴちゃん＋文字」→「文字を…で省略」→「たぴちゃんだけ」→「文字だけ」と縮む。
func TestSplashSnapshot(t *testing.T) {
	// the version is injected at build time; pin it so the snapshot is stable
	v := daemon.Version
	daemon.Version = "0.1.0"
	t.Cleanup(func() { daemon.Version = v })
	cases := []struct {
		glyphs string
		width  int
		want   string
	}{
		{"unicode", 60, `
····························································
····························································
················🬞🬹🬹🬹🬏····pekoe·v0.1.0·······················
················█●·●█····agents·in·worktrees················
················🬁🬎🬎🬎🬀·······································
·························1·session·alive····················
····························································
····························································
····························································`},
		{"narrow", 60, `
····························································
····························································
················🬞🬹🬹🬹🬏····pekoe·v0.1.0·······················
················█🞄·🞄█····agents·in·worktrees················
················🬁🬎🬎🬎🬀·······································
·························1·session·alive····················
····························································
····························································
····························································`},
		{"ascii", 60, `
····························································
····························································
················.---.····pekoe·v0.1.0·······················
················|o·o|····agents·in·worktrees················
················'---'·······································
·························1·session·alive····················
····························································
····························································
····························································`},
		{"narrow", 24, `
························
························
·🬞🬹🬹🬹🬏····pekoe·v0.1.0··
·█🞄·🞄█····agents·in·wo…·
·🬁🬎🬎🬎🬀··················
··········1·session·al…·
························
························
························`},
		{"narrow", 16, `
················
················
················
·····🬞🬹🬹🬹🬏······
·····█🞄·🞄█······
·····🬁🬎🬎🬎🬀······
················
················
················`},
		{"narrow", 6, `
······
······
·pek…·
·age…·
······
·1·s…·
······
······
······`},
	}
	for _, c := range cases {
		got := strings.ReplaceAll(ansi.Strip(splashModel(t, "none", c.glyphs, c.width, 9).View()), " ", "·")
		if want := strings.TrimPrefix(c.want, "\n"); got != want {
			t.Errorf("%s %d:\n%s\nwant:\n%s", c.glyphs, c.width, got, want)
		}
	}
}
