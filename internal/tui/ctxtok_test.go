package tui

import "testing"

// the context size beside the activity spark reads like the other token
// counts (state.ShortTokens): 999,600 is not "1000k"
func TestCtxTokensShort(t *testing.T) {
	for n, want := range map[int]string{950: "950", 12345: "12.3k", 999_600: "999.6k", 999_960: "1.00M", 1_234_567: "1.23M"} {
		if got := ktok(n); got != want {
			t.Errorf("ktok(%d) = %q, want %q", n, got, want)
		}
	}
}
