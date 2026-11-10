package daemon

// Session.after (docs/orchestration.md「Session に足す項目」): pekoe new
// --after <slug> records that the new session comes after others, for the
// person and the orchestrator to see (the TUI, session.list) and, in stage
// 3, for the merge conditions. v1 only records it: nothing waits.
//
// A dependency must be a session pekoed has when the new one is made: a
// slug that does not exist (a typo) would be recorded as a dependency that
// is never met, so it is refused, and the orchestrator makes the session it
// depends on first. The list is fixed at creation, so a cycle can only come
// from replacing a landed session with one of the same slug (A landed, B
// after A, a new A after B); that, and a session after itself, are refused.
// A dependency removed later (pekoe rm) stays in the list; the TUI shows it
// as gone.

import (
	"fmt"
	"strings"
)

// afterMax is how many sessions one may come after.
const afterMax = 10

// resolveAfter turns the --after refs (<slug> or <repo>/<slug>) into slugs,
// in order and without repeats, for the session slug being made.
func (d *Daemon) resolveAfter(refs []string, slug string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		dep, err := d.resolveSessionRef(ref)
		if err != nil {
			return nil, fmt.Errorf("--after: %w", err)
		}
		if dep == slug {
			return nil, fmt.Errorf("--after %s: a session cannot come after itself", ref)
		}
		if _, err := d.snapshot(dep); err != nil {
			return nil, fmt.Errorf("--after %s: no such session (make the session it comes after first)", ref)
		}
		if seen[dep] {
			continue
		}
		seen[dep] = true
		out = append(out, dep)
	}
	if len(out) > afterMax {
		return nil, fmt.Errorf("--after: at most %d sessions", afterMax)
	}
	if path := d.afterCycle(out, slug); path != nil {
		return nil, fmt.Errorf("--after: %s would come after itself (%s)", slug, strings.Join(path, " → "))
	}
	return out, nil
}

// afterCycle is a chain of after links from one of deps back to slug, or
// nil: slug first, then the sessions it would come after, ending in slug.
func (d *Daemon) afterCycle(deps []string, slug string) []string {
	d.mu.Lock()
	links := map[string][]string{}
	for k, s := range d.sessions {
		if k != slug { // the landed session slug replaces does not count
			links[k] = s.After
		}
	}
	d.mu.Unlock()
	done := map[string]bool{}
	var walk func(k string, path []string) []string
	walk = func(k string, path []string) []string {
		path = append(path[:len(path):len(path)], k)
		if k == slug {
			return path
		}
		if done[k] {
			return nil
		}
		done[k] = true
		for _, n := range links[k] {
			if p := walk(n, path); p != nil {
				return p
			}
		}
		return nil
	}
	for _, dep := range deps {
		if p := walk(dep, []string{slug}); p != nil {
			return p
		}
	}
	return nil
}
