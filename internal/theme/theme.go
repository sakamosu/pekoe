// Package theme maps roles to colors. Drawing code only ever names roles
// (theme.Primary …); hex values live here and in the user's theme file.
// See docs/ui-guidelines.md.
package theme

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"pekoe/internal/config"
	"pekoe/internal/state"
)

type Role string

const (
	Bg        Role = "bg"
	Surface   Role = "surface"
	Border    Role = "border"
	Dim       Role = "dim"
	Text      Role = "text"
	Primary   Role = "primary"
	Secondary Role = "secondary"
	Warn      Role = "warn"
	Ok        Role = "ok"
	Err       Role = "err"
	Select    Role = "select"
)

// Default palette (OKLCH-designed; contrast checked against bg #151421).
var defaultHex = map[Role]string{
	Bg: "#151421", Surface: "#1e1d2c", Border: "#3b3b4d", Dim: "#848495", Text: "#e0e0eb",
	Primary: "#ff87da", Secondary: "#11cded", Warn: "#edc812", Ok: "#51d75e", Err: "#ff7172", Select: "#441e39",
}

var defaultANSI = map[Role]int{Border: 8, Dim: 8, Primary: 5, Secondary: 6, Warn: 3, Ok: 2, Err: 1}

type Mode string

const (
	Truecolor Mode = "truecolor"
	ANSI      Mode = "ansi"
	None      Mode = "none"
)

type Theme struct {
	Mode   Mode
	colors map[Role]lipgloss.TerminalColor
	ramp   *ramp // focus-border gradient; nil ⇒ solid primary
}

// StdoutIsTerminal decides whether auto mode may color. A variable so tests
// can pretend either way.
var StdoutIsTerminal = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// DetectMode: auto → NO_COLOR or stdout not a terminal (pipe, redirect) ⇒
// none, COLORTERM=truecolor|24bit ⇒ truecolor, else ansi. An explicit mode wins.
func DetectMode(m string) Mode {
	switch m {
	case "truecolor", "ansi", "none":
		return Mode(m)
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return None
	}
	if !StdoutIsTerminal() {
		return None
	}
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	if ct == "truecolor" || ct == "24bit" {
		return Truecolor
	}
	if os.Getenv("TERM_PROGRAM") == "ghostty" || strings.Contains(os.Getenv("TERM"), "ghostty") {
		return Truecolor
	}
	return ANSI
}

func Load(cfg *config.Theme) *Theme {
	mode := ""
	if cfg != nil {
		mode = cfg.Mode
	}
	if env := os.Getenv("PEKOE_COLOR"); env != "" {
		mode = env
	}
	t := &Theme{Mode: DetectMode(mode), colors: map[Role]lipgloss.TerminalColor{}}
	switch t.Mode {
	case Truecolor:
		lipgloss.SetColorProfile(termenv.TrueColor)
		for r, h := range defaultHex {
			if cfg != nil && cfg.Colors[string(r)] != "" {
				h = cfg.Colors[string(r)]
			}
			t.colors[r] = lipgloss.Color(h)
		}
		// The ramp is primary's derivative: overriding primary alone falls
		// back to solid primary; primary_end opts back in (= primary ⇒ solid).
		end := defaultPrimaryEnd
		if cfg != nil && cfg.Colors[string(Primary)] != "" {
			end = ""
		}
		if cfg != nil && cfg.Colors[PrimaryEndKey] != "" {
			end = cfg.Colors[PrimaryEndKey]
		}
		if from := string(t.colors[Primary].(lipgloss.Color)); end != "" && !strings.EqualFold(from, end) {
			t.ramp = newRamp(from, end)
		}
	case ANSI:
		lipgloss.SetColorProfile(termenv.ANSI)
		for r, n := range defaultANSI {
			if cfg != nil {
				if v, ok := cfg.ANSI[string(r)]; ok {
					n = v
				}
			}
			t.colors[r] = lipgloss.Color(fmt.Sprint(n))
		}
		// bg / surface / select are not painted: respect the terminal background
	case None:
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	return t
}

func (t *Theme) Color(r Role) lipgloss.TerminalColor {
	if c, ok := t.colors[r]; ok {
		return c
	}
	return lipgloss.NoColor{}
}

// Has reports whether a role is painted in this mode (bg roles are not in ansi).
func (t *Theme) Has(r Role) bool {
	_, ok := t.colors[r]
	return ok
}

// FG renders s in role r.
func (t *Theme) FG(r Role, s string) string {
	if t.Mode == None || s == "" {
		return s
	}
	return lipgloss.NewStyle().Foreground(t.Color(r)).Render(s)
}

// Bold is only for focused titles and key letters (guidelines).
func (t *Theme) Bold(r Role, s string) string {
	if t.Mode == None {
		return s
	}
	return lipgloss.NewStyle().Foreground(t.Color(r)).Bold(true).Render(s)
}

func (t *Theme) Style(fg Role) lipgloss.Style {
	st := lipgloss.NewStyle()
	if t.Mode != None {
		st = st.Foreground(t.Color(fg))
	}
	return st
}

// SelectStyle paints the selected-row background, or reverse video in ansi/none.
func (t *Theme) SelectStyle() lipgloss.Style {
	if t.Mode == Truecolor {
		return lipgloss.NewStyle().Background(t.Color(Select))
	}
	return lipgloss.NewStyle().Reverse(true)
}

// ---- icons: nerd | unicode | ascii ----

type IconSet struct {
	Name                                                                string
	Sessions, Worktrees, Changes, Agent, Diff, Browser, Picks, Comments string
	Vim, Run, Wait, Idle, Creating, Done, Fail, Current, Warning        string
	Check, Here, Unsent, Sent                                           string
	PR, Pending, ChangesReq, Merged, Closed                             string // GitHub PR state
	CapL, CapR, Sep                                                     string
	Spinner                                                             []string

	Issues string // Issues panel / a GitHub issue
}

var braille = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func IconsFromEnv() string {
	g, err := config.LoadGlobal()
	if err != nil {
		return IconsName("")
	}
	return IconsName(g.Theme.Icons)
}

// IconsName is the icon set to use for [theme] icons = cfg: $PEKOE_ICONS
// first, nerd when neither is set.
func IconsName(cfg string) string {
	if v := os.Getenv("PEKOE_ICONS"); v != "" {
		return v
	}
	if cfg != "" {
		return cfg
	}
	return "nerd"
}

func Icons(name string) IconSet {
	switch name {
	case "ascii":
		return IconSet{Name: "ascii", Comments: "#", Run: "*", Wait: "!", Idle: "o", Creating: ".", Done: "v", Fail: "x",
			Current: "*", Warning: "!", Check: "v", Here: ">", Unsent: "*", Sent: "v", Sep: "|",
			Pending: "~", ChangesReq: "!", Merged: "m", Closed: "-", Spinner: []string{"-", "\\", "|", "/"}}
	case "unicode":
		return IconSet{Name: "unicode", Sessions: "◆", Worktrees: "⎇", Changes: "≡", Agent: ">", Diff: "±", Browser: "◎", Picks: "⌖",
			Comments: "✎", Vim: "V", Run: "●", Wait: "●", Idle: "○", Creating: "◌", Done: "✓", Fail: "✗", Current: "*", Warning: "⚠",
			Check: "✓", Here: "▶", Unsent: "●", Sent: "✓", Sep: "│", Spinner: braille,
			PR: "⇄", Pending: "…", ChangesReq: "!", Merged: "↣", Closed: "-", Issues: "⊙"}
	}
	return IconSet{Name: "nerd",
		Sessions: "\U000F06A9", Worktrees: "", Changes: "", Agent: "", Diff: "",
		Browser: "", Picks: "", Comments: "", Vim: "",
		Run: "", Wait: "", Idle: "", Creating: "◌", Done: "", Fail: "",
		Current: "", Warning: "", Check: "✓", Here: "▶", Unsent: "●", Sent: "✓",
		PR: "\ue726", Pending: "\uf017", ChangesReq: "\uf06a", Merged: "\ue727", Closed: "\uf05e",
		CapL: "", CapR: "", Sep: "│", Spinner: braille, Issues: "\uf192"}
}

// TestRunningMark leads the "test" badge while a session's tests run.
const TestRunningMark = "…"

// Prefix renders an icon followed by one space, or nothing in ascii.
func Prefix(icon string) string {
	if icon == "" {
		return ""
	}
	return icon + " "
}

// ---- PR state (the #42 tag in the session list) ----

// PRLook is the one icon after a session's PR number: merged / closed win,
// then CI failure, changes requested, CI running, all green. ok is false
// when there is nothing to show yet (no summary, no checks, no review).
func PRLook(ic IconSet, st *state.PRStatus) (StateLook, bool) {
	if st == nil {
		return StateLook{}, false
	}
	switch {
	case st.State == "MERGED":
		return StateLook{ic.Merged, Secondary, "merged"}, true
	case st.State == "CLOSED":
		return StateLook{ic.Closed, Dim, "closed"}, true
	case st.Fail > 0:
		return StateLook{ic.Fail, Err, "ci fail"}, true
	case st.ReviewDecision == "CHANGES_REQUESTED":
		return StateLook{ic.ChangesReq, Warn, "changes"}, true
	case st.Pending > 0:
		return StateLook{ic.Pending, Dim, "ci run"}, true
	case st.Pass > 0:
		return StateLook{ic.Check, Ok, "ci pass"}, true
	}
	return StateLook{}, false
}

// ---- states: symbol + color + word, always together ----

type StateLook struct {
	Icon string
	Role Role
	Word string
}

func Look(ic IconSet, s state.State, frame int, animate bool) StateLook {
	spin := func(fallback string) string {
		if animate && len(ic.Spinner) > 0 {
			return ic.Spinner[frame%len(ic.Spinner)]
		}
		return fallback
	}
	switch s {
	case state.Running:
		return StateLook{spin(ic.Run), Ok, "run"}
	case state.Waiting:
		return StateLook{ic.Wait, Warn, "wait"}
	case state.Idle:
		return StateLook{ic.Idle, Dim, "idle"}
	case state.Creating:
		return StateLook{spin(ic.Creating), Dim, "creating"}
	case state.Done:
		return StateLook{ic.Done, Secondary, "done"}
	case state.Reviewing:
		return StateLook{ic.Here, Primary, "review"}
	case state.Approved:
		return StateLook{ic.Check, Ok, "approved"}
	case state.Landed:
		return StateLook{ic.Check, Dim, "landed"}
	case state.Failed:
		return StateLook{ic.Fail, Err, "fail"}
	}
	return StateLook{ic.Idle, Dim, string(s)}
}

func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// SessionLine is the one-line summary used by `pekoe ls`:
// ● feat/csv  wait 2m  +120 -8  ✓test  :3003
func SessionLine(t *Theme, ic IconSet, s *state.Session, now time.Time, frame int) string {
	lk := Look(ic, s.State, frame, false)
	parts := []string{
		t.FG(lk.Role, lk.Icon), fmt.Sprintf("%-22s", s.Branch), t.FG(lk.Role, fmt.Sprintf("%-8s", lk.Word)),
		t.FG(Dim, fmt.Sprintf("%-4s", Ago(now.Sub(s.StateSince)))),
	}
	if s.Diffstat.Added+s.Diffstat.Deleted > 0 {
		parts = append(parts, t.FG(Ok, fmt.Sprintf("+%d", s.Diffstat.Added))+" "+t.FG(Err, fmt.Sprintf("-%d", s.Diffstat.Deleted)))
	}
	switch s.Test.Status {
	case "pass":
		parts = append(parts, t.FG(Ok, ic.Check)+"test")
	case "fail":
		parts = append(parts, t.FG(Err, ic.Fail)+"test")
	case "running":
		parts = append(parts, t.FG(Dim, TestRunningMark+"test"))
	}
	if s.Port > 0 {
		parts = append(parts, t.FG(Dim, fmt.Sprintf(":%d", s.Port)))
	}
	if s.State == state.Waiting && s.PendingTool != nil {
		parts = append(parts, t.FG(Dim, s.PendingTool.Name+" "+s.PendingTool.Input))
	}
	if s.Error != "" {
		parts = append(parts, t.FG(Err, strings.SplitN(s.Error, "\n", 2)[0]))
	}
	return strings.Join(parts, "  ")
}
