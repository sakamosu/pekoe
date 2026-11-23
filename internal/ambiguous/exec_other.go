//go:build !unix

package ambiguous

import "errors"

func execve(string, []string, []string) error { return errors.New("ambiguous: no exec") }
