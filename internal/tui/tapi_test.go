package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"boba/internal/config"
	"boba/internal/state"
	"boba/internal/theme"
)

func tapiModel(t *testing.T, color string, mc config.Mascot) *model {
	t.Helper()
	t.Setenv("BOBA_COLOR", color)
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
				if vi < 2 && ansi.StringWidth(l) != m.width {
					t.Errorf("%s view %d line %d: width %d", c.color, vi, i, ansi.StringWidth(l))
				}
			}
		}
		if !strings.Contains(ansi.Strip(views[1]), "delete worktree b? [y/n]") {
			t.Errorf("%s: boba asks line missing", c.color)
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
	if got := notifySeq("boba", "a;b\x1b"); got != "\x1b]777;notify;boba;a b \x07" {
		t.Fatalf("%q", got)
	}
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if got := notifySeq("boba", "x"); got != "\x1b]9;boba: x\x07" {
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
	out := m.box(44, 4, boxOpt{rows: rows})
	line := out[1]
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
