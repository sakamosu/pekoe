package errcatch

import (
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Frame is one location in a stack trace or error header.
type Frame struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col,omitempty"`
	Func string `json:"func,omitempty"`
}

var (
	pyFrameRe   = regexp.MustCompile(`^\s*File "(.+?)", line (\d+)(?:, in (.+))?`)
	tsParenRe   = regexp.MustCompile(`^(\S.*?\.[cm]?tsx?)\((\d+),(\d+)\)`)
	pluginLocRe = regexp.MustCompile(`(\S+\.[A-Za-z]\w*): .*\((\d+):(\d+)\)\s*$`)
	jsAtRe      = regexp.MustCompile(`^\s*at\s+(?:(.+?)\s+\()?(.+?):(\d+):(\d+)\)?\s*$`)
	ffAtRe      = regexp.MustCompile(`^\s*([^@\s]*)@(.+?):(\d+):(\d+)\s*$`) // Firefox / Safari
	goFrameRe   = regexp.MustCompile(`^\t(\S+\.go):(\d+)`)
	genericLoc  = regexp.MustCompile(`((?:[A-Za-z][\w+.-]*:///?)?[^\s()'"\[\]]+?\.[A-Za-z]\w*)(?:\?[^\s:()]*)?:(\d+)(?::(\d+))?`)
)

// ParseFrame extracts a location from one line, if it has one.
func ParseFrame(l string) (Frame, bool) {
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	if m := pyFrameRe.FindStringSubmatch(l); m != nil {
		return Frame{File: m[1], Line: atoi(m[2]), Func: m[3]}, true
	}
	if m := tsParenRe.FindStringSubmatch(l); m != nil {
		return Frame{File: m[1], Line: atoi(m[2]), Col: atoi(m[3])}, true
	}
	if m := jsAtRe.FindStringSubmatch(l); m != nil {
		return Frame{File: cleanFile(m[2]), Line: atoi(m[3]), Col: atoi(m[4]), Func: m[1]}, true
	}
	if m := ffAtRe.FindStringSubmatch(l); m != nil && strings.Contains(m[2], "/") {
		return Frame{File: cleanFile(m[2]), Line: atoi(m[3]), Col: atoi(m[4]), Func: m[1]}, true
	}
	if m := goFrameRe.FindStringSubmatch(l); m != nil {
		return Frame{File: m[1], Line: atoi(m[2])}, true
	}
	if m := pluginLocRe.FindStringSubmatch(l); m != nil {
		return Frame{File: cleanFile(m[1]), Line: atoi(m[2]), Col: atoi(m[3])}, true
	}
	if m := genericLoc.FindStringSubmatch(l); m != nil {
		return Frame{File: cleanFile(m[1]), Line: atoi(m[2]), Col: atoi(m[3])}, true
	}
	return Frame{}, false
}

var webpackRe = regexp.MustCompile(`^webpack-internal:///(?:\([^)]*\)/)?`)

// cleanFile drops URL schemes, dev-server hosts, query strings and bundler
// prefixes so the path can be matched against the worktree.
func cleanFile(f string) string {
	f = strings.TrimSpace(f)
	f = webpackRe.ReplaceAllString(f, "")
	if strings.HasPrefix(f, "file://") {
		f = strings.TrimPrefix(f, "file://")
	} else if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
		if u, err := url.Parse(f); err == nil {
			f = u.Path
		}
	}
	if i := strings.IndexByte(f, '?'); i >= 0 {
		f = f[:i]
	}
	return f
}

// Resolve maps a frame's file to a path inside root (the worktree). It
// returns the path relative to root and true when the file exists there and
// is the project's own code (not node_modules, not the runtime).
func Resolve(root string, f Frame) (string, bool) {
	file := f.File
	if root == "" || file == "" || strings.HasPrefix(file, "node:") || strings.Contains(file, "node_modules/") || strings.Contains(file, "/site-packages/") {
		return "", false
	}
	var cands []string
	if filepath.IsAbs(file) {
		cands = append(cands, file)
		// a dev server serving /src/App.tsx is a path relative to the root
		cands = append(cands, filepath.Join(root, file))
	} else {
		cands = append(cands, filepath.Join(root, file))
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	for _, c := range cands {
		c = filepath.Clean(c)
		if st, err := os.Stat(c); err != nil || st.IsDir() {
			continue
		}
		if rel, ok := inside(root, c); ok {
			return rel, true
		}
		// /var vs /private/var (macOS) and other symlinked roots
		if realC, err := filepath.EvalSymlinks(c); err == nil && realRoot != "" {
			if rel, ok := inside(realRoot, realC); ok {
				return rel, true
			}
		}
	}
	return "", false
}

func inside(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// ---- fingerprint ----

var (
	hexRe   = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	hashRe  = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	numRe   = regexp.MustCompile(`\d+`)
	spaceRe = regexp.MustCompile(`\s+`)
	queryRe = regexp.MustCompile(`\?[\w=&.-]*`)
)

// NormalizeMessage removes what changes between two occurrences of the same
// error: addresses, hashes, numbers, ?t= cache busters, spacing.
func NormalizeMessage(m string) string {
	m = queryRe.ReplaceAllString(m, "")
	m = hexRe.ReplaceAllString(m, "0x")
	m = hashRe.ReplaceAllString(m, "H")
	m = numRe.ReplaceAllString(m, "N")
	m = spaceRe.ReplaceAllString(strings.TrimSpace(m), " ")
	return strings.ToLower(m)
}

// NormalizeFrame keeps the file's last path components and the function,
// and drops line and column so an edit above the throw site (HMR) does not
// split one error into two.
func NormalizeFrame(f Frame) string {
	file := cleanFile(f.File)
	parts := strings.Split(filepath.ToSlash(file), "/")
	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}
	fn := f.Func
	if i := strings.LastIndexByte(fn, ' '); i >= 0 { // "async Foo" / "new Foo"
		fn = fn[i+1:]
	}
	return strings.Join(parts, "/") + "#" + fn
}

// Fingerprint identifies "the same error": normalized message + top frame.
func Fingerprint(b Block) string {
	key := NormalizeMessage(b.Message)
	if b.Top >= 0 && b.Top < len(b.Frames) {
		key += "\n" + NormalizeFrame(b.Frames[b.Top])
	}
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}
