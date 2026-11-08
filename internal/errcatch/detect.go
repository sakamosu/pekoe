// Package errcatch finds errors in a dev server's output (and in the browser
// console errors the extension forwards), folds repeats into one report by
// fingerprint, and remembers which ones were sent or ignored.
//
// Detection is table driven: Rules lists one entry per output format. Each
// rule says which line opens an error, which following lines belong to it
// (stack frames, code frames), and optionally which line closes it. Output is
// stripped of ANSI sequences before matching.
package errcatch

import (
	"regexp"
	"strings"
	"time"
)

// Rule recognises one error format.
type Rule struct {
	Name  string
	Start *regexp.Regexp // the line that opens an error block
	Cont  *regexp.Regexp // lines that continue the block (frames, code frames)
	End   *regexp.Regexp // optional: a line that is part of the block and closes it
	// Blank lines continue the block instead of closing it (Go panics put a
	// blank line between the message and the goroutine dump).
	Blank bool
	// MinFrames: blocks with fewer parsed frames are dropped (a bare
	// "Error: …" log line is not worth a card; a thrown one has "at" lines).
	MinFrames int
	// LastFrame: the most relevant frame is the last one (Python prints the
	// most recent call last).
	LastFrame bool
	// Message picks the one-line message from the block; nil = first line
	// that is not a location or a frame.
	Message func(lines []string) string
}

var (
	jsErrorRe = regexp.MustCompile(`^\s*(?:Uncaught\s+(?:\(in promise\)\s+)?)?(?:[A-Z][A-Za-z0-9]*)?(?:Error|Exception)(?:\s*\[[\w-]+\])?:\s+\S`)
	jsFrameRe = regexp.MustCompile(`^\s+at\s`)
)

// Rules is the detector table, tried in order. Add a format by adding a row.
var Rules = []Rule{
	{
		// Next.js: " ⨯ Error: boom" / " ⨯ ./app/page.tsx:3:1" + Module not found …
		Name:  "next",
		Start: regexp.MustCompile(`^\s*⨯\s+\S`),
		Cont:  regexp.MustCompile(`^(?:\s{2,}\S|\t|>|\s?[{}]|Module not found|Import trace|Caused by|https?://\S+$|\./\S|\S+\.[cm]?[jt]sx?:\d+)`),
	},
	{
		// Vite: "[vite] Internal server error: …", "[plugin:vite:react-babel] …"
		Name:  "vite",
		Start: regexp.MustCompile(`(?:^|\s)\[(?:vite\]\s+(?:Internal server error|Pre-transform error|error)|plugin:[^\]]+\])[:\s]`),
		Cont:  regexp.MustCompile(`^(?:\s{2,}\S|\t|>|\s*\d+\s*\|)`),
	},
	{
		// tsc --pretty false: "src/a.ts(10,5): error TS2322: …"
		Name:  "ts",
		Start: regexp.MustCompile(`^\S.*\.[cm]?tsx?\(\d+,\d+\): error TS\d+:`),
		Cont:  regexp.MustCompile(`^\s{2,}\S`),
	},
	{
		// tsc pretty: "src/a.ts:10:5 - error TS2322: …" + code frame
		Name:  "ts",
		Start: regexp.MustCompile(`^\S.*\.[cm]?tsx?:\d+:\d+ - error TS\d+:`),
		Cont:  regexp.MustCompile(`^(?:\s*\d+\s|\s+[~^]+\s*$|\s{2,}\S)`),
	},
	{
		Name:  "go",
		Start: regexp.MustCompile(`^(?:panic: |fatal error: )`),
		Cont:  regexp.MustCompile(`^(?:goroutine \d+ |\t|\s+\S|\[signal |created by |[\w./*()\-]+\(.*\)$|exit status \d+$)`),
		Blank: true,
	},
	{
		Name:      "python",
		Start:     regexp.MustCompile(`^Traceback \(most recent call last\):`),
		Cont:      regexp.MustCompile(`^\s+\S`),
		End:       regexp.MustCompile(`^[A-Za-z_][\w.]*(?::.*)?$`),
		LastFrame: true,
		Message: func(lines []string) string {
			return lines[len(lines)-1]
		},
	},
	{
		// Node: "TypeError: x is undefined" + "    at …" frames
		Name:      "node",
		Start:     jsErrorRe,
		Cont:      jsFrameRe,
		MinFrames: 1,
	},
}

// Block is one detected error: the raw lines (ANSI stripped) and what the
// rule made of them.
type Block struct {
	Kind    string
	Lines   []string
	Message string
	Frames  []Frame // in printed order
	Top     int     // index into Frames of the most relevant frame (-1 = none)
	// LastFrame: frames are printed most recent call last (Python).
	LastFrame bool
}

// Scanner turns a stream of output chunks into Blocks. Chunks may split lines
// anywhere; a block is closed by the next line that does not belong to it, or
// by Flush once the output has been quiet for Quiet.
type Scanner struct {
	Quiet time.Duration

	partial []byte
	cur     *open
	last    time.Time
}

type open struct {
	rule  *Rule
	lines []string
}

const maxBlockLines = 200

// NewScanner returns a scanner closing blocks after 500ms of silence.
func NewScanner() *Scanner { return &Scanner{Quiet: 500 * time.Millisecond} }

// Feed consumes a chunk of raw pty output and returns the blocks it closed.
func (s *Scanner) Feed(data []byte, now time.Time) []Block {
	s.last = now
	s.partial = append(s.partial, data...)
	var out []Block
	for {
		i := indexNL(s.partial)
		if i < 0 {
			break
		}
		line := string(s.partial[:i])
		s.partial = s.partial[i+1:]
		out = append(out, s.line(line)...)
	}
	if len(s.partial) > 64<<10 { // a runaway line without newline
		out = append(out, s.line(string(s.partial))...)
		s.partial = nil
	}
	return out
}

// Flush closes the open block when nothing arrived for Quiet. The pending
// partial line (no newline yet) is taken as complete first.
func (s *Scanner) Flush(now time.Time) []Block {
	if now.Sub(s.last) < s.Quiet {
		return nil
	}
	var out []Block
	if len(s.partial) > 0 && s.cur != nil {
		out = append(out, s.line(string(s.partial))...)
		s.partial = nil
	}
	if b := s.close(); b != nil {
		out = append(out, *b)
	}
	return out
}

// Pending reports whether a block is open (Flush may produce something).
func (s *Scanner) Pending() bool { return s.cur != nil }

func indexNL(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

func (s *Scanner) line(raw string) []Block {
	l := cleanLine(raw)
	var out []Block
	if s.cur != nil {
		r := s.cur.rule
		switch {
		case r.End != nil && l != "" && !startsWithSpace(l) && r.End.MatchString(l):
			s.cur.lines = append(s.cur.lines, l)
			if b := s.close(); b != nil {
				out = append(out, *b)
			}
			return out
		case l == "" && r.Blank, l != "" && r.Cont.MatchString(l):
			if len(s.cur.lines) < maxBlockLines {
				s.cur.lines = append(s.cur.lines, l)
			}
			return out
		}
		if b := s.close(); b != nil {
			out = append(out, *b)
		}
	}
	if l == "" {
		return out
	}
	for i := range Rules {
		if Rules[i].Start.MatchString(l) {
			s.cur = &open{rule: &Rules[i], lines: []string{l}}
			break
		}
	}
	return out
}

func startsWithSpace(l string) bool { return l != "" && (l[0] == ' ' || l[0] == '\t') }

func (s *Scanner) close() *Block {
	o := s.cur
	s.cur = nil
	if o == nil {
		return nil
	}
	lines := o.lines
	for len(lines) > 1 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	b := makeBlock(o.rule, lines)
	if len(b.Frames) < o.rule.MinFrames {
		return nil
	}
	return &b
}

func makeBlock(r *Rule, lines []string) Block {
	b := Block{Kind: r.Name, Lines: lines, Top: -1, LastFrame: r.LastFrame}
	for _, l := range lines {
		if f, ok := ParseFrame(l); ok {
			b.Frames = append(b.Frames, f)
		}
	}
	if len(b.Frames) > 0 {
		b.Top = 0
		if r.LastFrame {
			b.Top = len(b.Frames) - 1
		}
	}
	if r.Message != nil {
		b.Message = r.Message(lines)
	} else {
		b.Message = firstMessage(lines)
	}
	b.Message = tidyMessage(b.Message)
	return b
}

var (
	nextMark    = regexp.MustCompile(`^\s*⨯\s+`)
	timePrefix  = regexp.MustCompile(`^\d{1,2}:\d{2}:\d{2}(?:\s?[AP]M)?\s+`)
	viteMark    = regexp.MustCompile(`^\[vite\]\s+(?:Internal server error|Pre-transform error|error)?:?\s*`)
	locOnlyRe   = regexp.MustCompile(`^\S+:\d+(?::\d+)?$`)
	uncaughtPre = regexp.MustCompile(`^Uncaught\s+(?:\(in promise\)\s+)?`)
)

// firstMessage: the first line that says something (not a bare location,
// not a frame, not a code frame).
func firstMessage(lines []string) string {
	for _, l := range lines {
		t := strings.TrimSpace(nextMark.ReplaceAllString(l, ""))
		if t == "" || locOnlyRe.MatchString(t) || jsFrameRe.MatchString(l) {
			continue
		}
		return t
	}
	return strings.TrimSpace(lines[0])
}

func tidyMessage(m string) string {
	m = strings.TrimSpace(nextMark.ReplaceAllString(m, ""))
	m = timePrefix.ReplaceAllString(m, "")
	m = viteMark.ReplaceAllString(m, "")
	m = uncaughtPre.ReplaceAllString(m, "")
	return strings.TrimSpace(m)
}

// FromText builds a block from text that is already known to be one error
// (a browser console error: message + stack).
func FromText(kind, message, stack string) Block {
	// the page wrote it: clean it like a terminal line (ANSI, control
	// characters), one line at a time
	var ml []string
	for _, l := range strings.Split(message, "\n") {
		ml = append(ml, cleanLine(l))
	}
	message = strings.Join(ml, "\n")
	lines := []string{message}
	for _, l := range strings.Split(stack, "\n") {
		l = cleanLine(l)
		if strings.TrimSpace(l) == "" || strings.TrimSpace(l) == strings.TrimSpace(message) {
			continue
		}
		lines = append(lines, l)
	}
	b := Block{Kind: kind, Lines: lines, Message: tidyMessage(message), Top: -1}
	for _, l := range lines[1:] {
		if f, ok := ParseFrame(l); ok {
			b.Frames = append(b.Frames, f)
		}
	}
	if len(b.Frames) > 0 {
		b.Top = 0
	}
	return b
}

// FromConsole builds a block from a browser console error. Without a stack,
// the script location the browser reported (onerror) is the frame.
func FromConsole(message, stack, source string, line int) Block {
	b := FromText("browser", message, stack)
	if len(b.Frames) == 0 && source != "" {
		b.Frames = []Frame{{File: cleanFile(source), Line: line}}
		b.Top = 0
	}
	return b
}

// ---- ANSI ----

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;:?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[PX^_][^\x1b]*\x1b\\|\x1b[()][0-9A-Za-z]|\x1b[=>78DEHMc]`)

// StripANSI removes escape sequences (CSI, OSC, DCS, charset selection).
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// cleanLine: strip ANSI, keep what a carriage return left visible, drop
// control characters.
func cleanLine(raw string) string {
	l := StripANSI(raw)
	if i := strings.LastIndexByte(strings.TrimRight(l, "\r"), '\r'); i >= 0 {
		l = l[i+1:]
	}
	l = strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, l)
	return strings.TrimRight(l, " ")
}
