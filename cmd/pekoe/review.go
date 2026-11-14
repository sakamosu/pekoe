package main

// review: open the review surface.

import (
	"fmt"
	"strconv"
	"strings"

	"pekoe/internal/state"
	"pekoe/internal/tui"
)

func cmdReview(args []string) error {
	f, pos, err := parseCmd("review", args)
	if err != nil {
		return err
	}
	cl := connect()
	defer cl.Close()
	if len(pos) > 0 && strings.HasPrefix(pos[0], "gh#") {
		n, err := strconv.Atoi(strings.TrimPrefix(pos[0], "gh#"))
		if err != nil {
			return fmt.Errorf("bad PR number: %s", pos[0])
		}
		dir, err := repoDir(cl, f)
		if err != nil {
			return err
		}
		var s state.Session
		if err := cl.Call("review.pr", map[string]any{"repo": dir, "number": n}, &s); err != nil {
			return err
		}
		return tui.RunReview(cl, s.Slug)
	}
	slug := ""
	if len(pos) > 0 {
		// <repo>/<slug> → the slug the review surface keys everything on
		var s state.Session
		if err := cl.Call("session.get", map[string]any{"slug": pos[0]}, &s); err != nil {
			return err
		}
		slug = s.Slug
	} else {
		var s *state.Session
		if err := cl.Call("review.next", nil, &s); err != nil {
			return err
		}
		if s == nil {
			return fmt.Errorf("nothing to review")
		}
		slug = s.Slug
	}
	return tui.RunReview(cl, slug)
}
