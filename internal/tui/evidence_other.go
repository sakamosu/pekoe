//go:build !darwin

package tui

// macOpensItsOwnWay is macOS's (evidence_darwin.go): nothing to look at here.
func macOpensItsOwnWay(string) bool { return false }
