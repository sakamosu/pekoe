package ids

import (
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{Request(651), "r651"},
		{Question(12), "q12"},
		{Report(468), "p468"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	ok := []struct {
		parse func(string) (int64, error)
		in    string
		want  int64
	}{
		{ParseRequest, "651", 651},
		{ParseRequest, "r651", 651},
		{ParseRequest, "R651", 651},
		{ParseRequest, "#651", 651},
		{ParseRequest, " r651 ", 651},
		{ParseQuestion, "12", 12},
		{ParseQuestion, "q12", 12},
		{ParseReport, "468", 468},
		{ParseReport, "p468", 468},
	}
	for _, c := range ok {
		got, err := c.parse(c.in)
		if err != nil || got != c.want {
			t.Errorf("parse %q = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	bad := []struct {
		parse func(string) (int64, error)
		in    string
		says  string
	}{
		{ParseRequest, "q12", "is an inbox question number"},
		{ParseRequest, "p12", "is a report number"},
		{ParseQuestion, "r12", "is a request number"},
		{ParseReport, "q12", "is an inbox question number"},
		{ParseRequest, "x12", "bad request number"},
		{ParseRequest, "r", "bad request number"},
		{ParseRequest, "", "bad request number"},
		{ParseRequest, "0", "bad request number"},
		{ParseRequest, "r-1", "bad request number"},
		{ParseRequest, "+3", "bad request number"},
		{ParseRequest, "rr1", "bad request number"},
		{ParseQuestion, "12a", "bad inbox question number"},
	}
	for _, c := range bad {
		_, err := c.parse(c.in)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("parse %q: err %v, want one saying %q", c.in, err, c.says)
		}
	}
}

func TestNumbered(t *testing.T) {
	for s, want := range map[string]bool{
		"12": true, "#12": true, "q12": true, "Q12": true, "r651": true, "p468": true, " q1 ": true,
		"": false, "q": false, "x12": false, "q12a": false, "このまま進めてよい?": false, "1.5": false, "rr1": false,
	} {
		if got := Numbered(s); got != want {
			t.Errorf("Numbered(%q) = %v, want %v", s, got, want)
		}
	}
}
