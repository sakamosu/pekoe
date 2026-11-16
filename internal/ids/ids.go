// Package ids writes and reads the numbers pekoe shows for its own records,
// each with a letter for its kind, so they are not taken for a GitHub PR
// or issue (#185):
//
//   - r651: a request (the ledger)
//   - q12: a question in the inbox
//   - p468: a report (pekoe report)
//
// Only how a number is shown changes: the numbers themselves, what is
// stored and the --json output (a plain number) stay as they are.
package ids

import (
	"fmt"
	"strconv"
	"strings"
)

// The letters, one per kind.
const (
	RequestLetter  = 'r'
	QuestionLetter = 'q'
	ReportLetter   = 'p'
)

// Request is a ledger entry's number as shown: r651.
func Request(id int64) string { return format(RequestLetter, id) }

// Question is an inbox question's number as shown: q12.
func Question(id int64) string { return format(QuestionLetter, id) }

// Report is a report's number as shown: p468.
func Report(id int64) string { return format(ReportLetter, id) }

func format(letter byte, id int64) string {
	return string(letter) + strconv.FormatInt(id, 10)
}

// ParseRequest reads a request number as typed: 651, r651 (or #651, as
// it was shown before).
func ParseRequest(s string) (int64, error) { return parse(RequestLetter, "request", s) }

// ParseQuestion reads an inbox question's number: 12, q12 (or #12).
func ParseQuestion(s string) (int64, error) { return parse(QuestionLetter, "inbox question", s) }

// ParseReport reads a report's number: 468, p468 (or #468).
func ParseReport(s string) (int64, error) { return parse(ReportLetter, "report", s) }

// Numbered tells whether s is written like one of these numbers (of any
// kind): 12, #12, q12, r651, P468. A caller that takes either a number or
// free text reads s as a number when it is.
func Numbered(s string) bool {
	t := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if t != "" && kinds[t[0]|0x20] != "" {
		t = t[1:]
	}
	if t == "" {
		return false
	}
	for _, c := range t {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// names the kind each letter stands for, for the error on a wrong letter.
var kinds = map[byte]string{RequestLetter: "a request", QuestionLetter: "an inbox question", ReportLetter: "a report"}

func parse(letter byte, kind, s string) (int64, error) {
	t := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if t != "" && (t[0] < '0' || t[0] > '9') {
		got := t[0] | 0x20 // R651 reads as r651
		if got != letter {
			if k, ok := kinds[got]; ok {
				return 0, fmt.Errorf("%q is %s number (%c…), not %s number (%c…)", s, k, got, kinds[letter], letter)
			}
			return 0, fmt.Errorf("bad %s number %q (like %c12 or 12)", kind, s, letter)
		}
		t = t[1:]
	}
	id, err := strconv.ParseInt(t, 10, 64)
	if err != nil || id < 1 || t[0] == '+' {
		return 0, fmt.Errorf("bad %s number %q (like %c12 or 12)", kind, s, letter)
	}
	return id, nil
}
