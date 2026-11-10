// Package hooks installs pekoe's Claude Code hooks into ~/.claude/settings.json
// (user scope: applies to every repository; project .claude/ is never touched).
// Each hook just runs `pekoe event`, which forwards stdin to pekoed.
package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var Events = []string{"SessionStart", "UserPromptSubmit", "Notification", "Stop", "PreToolUse", "PostToolUse", "SessionEnd"}

func SettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "settings.json")
}

func command(bin, ev string) string {
	return fmt.Sprintf("%s event %s", shq(bin), ev)
}

func shq(s string) string {
	if !strings.ContainsAny(s, " '\"$`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isOurs(h map[string]any) bool {
	c, _ := h["command"].(string)
	_, ok := ourEvent(c)
	return ok
}

// ourEvent reads a command written by command(): `<bin> event <ev>`, where
// bin (possibly shq-quoted) is named pekoe. It returns the event.
func ourEvent(c string) (string, bool) {
	w := shWords(c)
	if len(w) != 3 || filepath.Base(w[0]) != "pekoe" || w[1] != "event" {
		return "", false
	}
	return w[2], true
}

// shWords splits c on blanks, undoing shq's single quotes and backslash
// escapes (all command() produces).
func shWords(c string) []string {
	var words []string
	var cur strings.Builder
	in, quoted := false, false
	for i := 0; i < len(c); i++ {
		ch := c[i]
		switch {
		case quoted:
			if ch == '\'' {
				quoted = false
			} else {
				cur.WriteByte(ch)
			}
		case ch == '\'':
			quoted, in = true, true
		case ch == '\\' && i+1 < len(c):
			i++
			cur.WriteByte(c[i])
			in = true
		case ch == ' ' || ch == '\t':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(ch)
			in = true
		}
	}
	if in {
		words = append(words, cur.String())
	}
	return words
}

// Plan returns the new settings content (and whether anything changes).
func Plan(bin string, remove bool) (path string, before, after []byte, changed bool, err error) {
	path = SettingsPath()
	before, err = os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	err = nil
	root := map[string]any{}
	if len(bytes.TrimSpace(before)) > 0 {
		if err = json.Unmarshal(before, &root); err != nil {
			err = fmt.Errorf("%s is not valid JSON: %w", path, err)
			return
		}
	}
	hooksAny, _ := root["hooks"].(map[string]any)
	if hooksAny == nil {
		hooksAny = map[string]any{}
	}
	for _, ev := range Events {
		groups, _ := hooksAny[ev].([]any)
		var kept []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			hs, _ := gm["hooks"].([]any)
			var keepH []any
			for _, h := range hs {
				if hm, ok := h.(map[string]any); ok && isOurs(hm) {
					continue
				}
				keepH = append(keepH, h)
			}
			if len(keepH) == 0 && len(hs) > 0 {
				continue
			}
			gm["hooks"] = keepH
			kept = append(kept, gm)
		}
		if !remove {
			entry := map[string]any{"type": "command", "command": command(bin, ev), "timeout": 10}
			group := map[string]any{"hooks": []any{entry}}
			if ev == "PreToolUse" || ev == "PostToolUse" {
				group["matcher"] = "*"
			}
			kept = append(kept, group)
		}
		if len(kept) == 0 {
			delete(hooksAny, ev)
		} else {
			hooksAny[ev] = kept
		}
	}
	if len(hooksAny) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooksAny
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err = enc.Encode(root); err != nil {
		return
	}
	after = buf.Bytes()
	changed = !bytes.Equal(bytes.TrimSpace(before), bytes.TrimSpace(after))
	return
}

// Apply writes the planned settings, keeping a timestamped backup.
func Apply(path string, before, after []byte) (backup string, err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if len(before) > 0 {
		backup = fmt.Sprintf("%s.pekoe-backup-%s", path, time.Now().Format("20060102-150405"))
		if err = os.WriteFile(backup, before, 0o600); err != nil {
			return
		}
	}
	// settings.json kept in dotfiles is a symlink: replace the file it
	// points at, not the link (renaming over the link would cut it off)
	target := path
	if t, e := filepath.EvalSymlinks(path); e == nil {
		target = t
	}
	tmp := target + ".pekoe-tmp"
	if err = os.WriteFile(tmp, after, 0o600); err != nil {
		return
	}
	err = os.Rename(tmp, target)
	return
}

// Installed reports whether every pekoe hook is present.
func Installed() bool {
	b, err := os.ReadFile(SettingsPath())
	if err != nil {
		return false
	}
	var root struct {
		Hooks map[string][]struct {
			Hooks []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	for _, ev := range Events {
		found := false
		for _, g := range root.Hooks[ev] {
			for _, h := range g.Hooks {
				c, _ := h["command"].(string)
				if got, ok := ourEvent(c); ok && got == ev {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}
