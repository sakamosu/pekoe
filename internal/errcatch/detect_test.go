package errcatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

// scan feeds the chunks (each may split lines anywhere), then lets the
// output go quiet, and returns every block closed.
func scan(chunks ...string) []Block {
	s := NewScanner()
	now := t0
	var out []Block
	for _, c := range chunks {
		now = now.Add(10 * time.Millisecond)
		out = append(out, s.Feed([]byte(c), now)...)
	}
	out = append(out, s.Flush(now.Add(time.Second))...)
	return out
}

func TestDetectors(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		kind     string
		message  string
		file     string // top frame (before resolving into a worktree)
		line     int
		nLines   int // lines in the block
		noReport bool
	}{
		{
			name: "node uncaught",
			out: "/Users/me/app/server.js:3\nthrow new Error(\"boom\");\n^\n\n" +
				"Error: boom\n    at Object.<anonymous> (/Users/me/app/server.js:3:7)\n    at Module._compile (node:internal/modules/cjs/loader:1256:14)\n\nNode.js v20.10.0\n",
			kind: "node", message: "Error: boom", file: "/Users/me/app/server.js", line: 3, nLines: 3,
		},
		{
			name: "node TypeError",
			out:  "TypeError: Cannot read properties of undefined (reading 'id')\n    at handler (/Users/me/app/src/api.ts:42:17)\n    at process.processTicksAndRejections (node:internal/process/task_queues:95:5)\nlistening on :3000\n",
			kind: "node", message: "TypeError: Cannot read properties of undefined (reading 'id')", file: "/Users/me/app/src/api.ts", line: 42, nLines: 3,
		},
		{
			name:     "a logged Error: line without a stack is not reported",
			out:      "Error: could not connect, retrying\nready\n",
			noReport: true,
		},
		{
			name: "next.js runtime error",
			out: " ⨯ Error: boom\n    at Home (app/page.tsx:2:9)\n  1 | export default function Home() {\n> 2 |   throw new Error(\"boom\")\n    |         ^\n  3 |   return <main/>\n {\n  digest: '2611089043'\n}\n" +
				" GET / 500 in 120ms\n",
			kind: "next", message: "Error: boom", file: "app/page.tsx", line: 2, nLines: 9,
		},
		{
			name: "next.js module not found",
			out: " ⨯ ./app/page.tsx:1:1\nModule not found: Can't resolve 'lodashx'\n> 1 | import x from 'lodashx'\n    | ^\n\nhttps://nextjs.org/docs/messages/module-not-found\n" +
				" ○ Compiling /_error ...\n",
			kind: "next", message: "Module not found: Can't resolve 'lodashx'", file: "./app/page.tsx", line: 1, nLines: 4,
		},
		{
			name: "vite internal server error",
			out: "10:23:45 PM [vite] Internal server error: Failed to resolve import \"./Foo\" from \"src/App.tsx\". Does the file exist?\n" +
				"  Plugin: vite:import-analysis\n  File: /Users/me/app/src/App.tsx:3:20\n  1  |  import React from 'react'\n  3  |  import Foo from './Foo'\n     |                   ^\n" +
				"10:23:46 PM [vite] page reload src/App.tsx\n",
			kind: "vite", message: "Failed to resolve import \"./Foo\" from \"src/App.tsx\". Does the file exist?", file: "/Users/me/app/src/App.tsx", line: 3, nLines: 6,
		},
		{
			name: "vite plugin error",
			out:  "[plugin:vite:react-babel] /Users/me/app/src/App.tsx: Unexpected token (5:3)\n  3 | function App() {\n> 5 |   return <div\n",
			kind: "vite", message: "[plugin:vite:react-babel] /Users/me/app/src/App.tsx: Unexpected token (5:3)", file: "/Users/me/app/src/App.tsx", line: 5, nLines: 3,
		},
		{
			name: "tsc",
			out:  "src/a.ts(10,5): error TS2322: Type 'string' is not assignable to type 'number'.\nFound 1 error.\n",
			kind: "ts", message: "src/a.ts(10,5): error TS2322: Type 'string' is not assignable to type 'number'.", file: "src/a.ts", line: 10, nLines: 1,
		},
		{
			name: "tsc pretty",
			out:  "src/a.ts:10:5 - error TS2322: Type 'string' is not assignable to type 'number'.\n\n10     x = \"s\"\n       ~\n",
			kind: "ts", message: "src/a.ts:10:5 - error TS2322: Type 'string' is not assignable to type 'number'.", file: "src/a.ts", line: 10, nLines: 1,
		},
		{
			name: "go panic",
			out: "panic: runtime error: index out of range [3] with length 2\n\ngoroutine 1 [running]:\nmain.lookup(...)\n\t/Users/me/app/main.go:12 +0x1d\nmain.main()\n\t/Users/me/app/main.go:7 +0x18\nexit status 2\n" +
				"[pekoe] dev server exited (1)\n",
			kind: "go", message: "panic: runtime error: index out of range [3] with length 2", file: "/Users/me/app/main.go", line: 12, nLines: 8,
		},
		{
			name: "python traceback",
			out: "Traceback (most recent call last):\n  File \"/Users/me/app/app.py\", line 9, in <module>\n    main()\n  File \"/Users/me/app/app.py\", line 6, in main\n    print(d[\"x\"])\nKeyError: 'x'\n" +
				"INFO: reloading\n",
			kind: "python", message: "KeyError: 'x'", file: "/Users/me/app/app.py", line: 6, nLines: 6,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bs := scan(c.out)
			if c.noReport {
				if len(bs) != 0 {
					t.Fatalf("want nothing, got %+v", bs)
				}
				return
			}
			if len(bs) != 1 {
				t.Fatalf("want 1 block, got %d: %+v", len(bs), bs)
			}
			b := bs[0]
			if b.Kind != c.kind || b.Message != c.message {
				t.Errorf("kind/message = %q / %q, want %q / %q", b.Kind, b.Message, c.kind, c.message)
			}
			if len(b.Lines) != c.nLines {
				t.Errorf("lines = %d, want %d:\n%s", len(b.Lines), c.nLines, strings.Join(b.Lines, "\n"))
			}
			top := pickTop("", &b)
			if top < 0 {
				t.Fatalf("no frame in %q", b.Lines)
			}
			if f := b.Frames[top]; f.File != c.file || f.Line != c.line {
				t.Errorf("top frame = %s:%d, want %s:%d", f.File, f.Line, c.file, c.line)
			}
		})
	}
}

// Colors are stripped before matching; the block keeps plain text.
func TestDetectStripsANSI(t *testing.T) {
	out := "\x1b[31m\x1b[1m ⨯\x1b[22m\x1b[39m \x1b[31mTypeError: x is not a function\x1b[39m\n" +
		"\x1b[90m    at Page (app/page.tsx:4:3)\x1b[39m\n\x1b[32m ✓\x1b[39m Compiled in 80ms\n"
	bs := scan(out)
	if len(bs) != 1 {
		t.Fatalf("want 1 block, got %d", len(bs))
	}
	if bs[0].Message != "TypeError: x is not a function" {
		t.Errorf("message = %q", bs[0].Message)
	}
	for _, l := range bs[0].Lines {
		if strings.Contains(l, "\x1b") {
			t.Errorf("escape left in %q", l)
		}
	}
	// a \r-overwritten progress line shows what is left visible
	bs = scan("compiling 40%\rTypeError: boom\n    at f (/a/b.js:1:1)\n")
	if len(bs) != 1 || bs[0].Message != "TypeError: boom" {
		t.Fatalf("got %+v", bs)
	}
}

// tmux delivers output in arbitrary chunks: lines and escape sequences may be
// split anywhere, and the stack may trail the message by a while.
func TestDetectChunked(t *testing.T) {
	full := "TypeError: Cannot read properties of null (reading 'map')\n    at List (/Users/me/app/src/List.tsx:8:20)\n    at renderWithHooks (/Users/me/app/node_modules/react-dom/cjs/react-dom.development.js:16305:18)\n"
	for _, size := range []int{1, 3, 7, 16, 50} {
		var chunks []string
		for i := 0; i < len(full); i += size {
			chunks = append(chunks, full[i:min(i+size, len(full))])
		}
		bs := scan(chunks...)
		if len(bs) != 1 || len(bs[0].Lines) != 3 || len(bs[0].Frames) != 2 {
			t.Fatalf("chunk %d: got %+v", size, bs)
		}
	}
	// split inside an escape sequence
	bs := scan("\x1b[3", "1mTypeError: boom\x1b[3", "9m\n    at f (/a/b.js:1:1)\n")
	if len(bs) != 1 || bs[0].Message != "TypeError: boom" {
		t.Fatalf("split escape: %+v", bs)
	}
}

// A block closes on the next unrelated line, or when the output goes quiet;
// not while frames may still come.
func TestDetectQuietClose(t *testing.T) {
	s := NewScanner()
	if bs := s.Feed([]byte("Error: boom\n    at f (/a/b.js:1:1)\n"), t0); len(bs) != 0 {
		t.Fatalf("closed too early: %+v", bs)
	}
	if bs := s.Flush(t0.Add(100 * time.Millisecond)); len(bs) != 0 {
		t.Fatal("closed before the output went quiet")
	}
	if bs := s.Feed([]byte("    at g (/a/c.js:2:2)\n"), t0.Add(200*time.Millisecond)); len(bs) != 0 {
		t.Fatal("a late frame must join the open block")
	}
	bs := s.Flush(t0.Add(800 * time.Millisecond))
	if len(bs) != 1 || len(bs[0].Frames) != 2 {
		t.Fatalf("quiet close: %+v", bs)
	}
	if s.Pending() {
		t.Fatal("still pending after close")
	}
	// the trailing line without a newline is taken when the output stops
	s = NewScanner()
	s.Feed([]byte("panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/a/main.go:5 +0x1"), t0)
	bs = s.Flush(t0.Add(time.Second))
	if len(bs) != 1 || len(bs[0].Frames) != 1 || bs[0].Frames[0].Line != 5 {
		t.Fatalf("partial last line: %+v", bs)
	}
	// two errors back to back are two blocks
	bs = scan("TypeError: a\n    at f (/a/b.js:1:1)\nRangeError: b\n    at g (/a/c.js:2:2)\n")
	if len(bs) != 2 || bs[0].Message != "TypeError: a" || bs[1].Message != "RangeError: b" {
		t.Fatalf("back to back: %+v", bs)
	}
}

func TestParseFrame(t *testing.T) {
	cases := []struct {
		line, file string
		n          int
	}{
		{"    at fn (/abs/a.ts:10:5)", "/abs/a.ts", 10},
		{"    at /abs/a.js:3:1", "/abs/a.js", 3},
		{"    at Page (webpack-internal:///(rsc)/./app/page.tsx:5:9)", "./app/page.tsx", 5},
		{"    at App (http://localhost:5173/src/App.tsx?t=1712:7:3)", "/src/App.tsx", 7},
		{"    at async Promise.all (index 0)", "", 0},
		{"render@http://localhost:3000/src/main.js:4:2", "/src/main.js", 4},
		{"\t/abs/main.go:12 +0x1d", "/abs/main.go", 12},
		{`  File "/abs/app.py", line 6, in main`, "/abs/app.py", 6},
		{"  File: /abs/src/App.tsx:3:20", "/abs/src/App.tsx", 3},
		{"http://localhost:3000 is ready", "", 0},
	}
	for _, c := range cases {
		f, ok := ParseFrame(c.line)
		if c.file == "" {
			if ok {
				t.Errorf("%q: unexpected frame %+v", c.line, f)
			}
			continue
		}
		if !ok || f.File != c.file || f.Line != c.n {
			t.Errorf("%q: got %+v, want %s:%d", c.line, f, c.file, c.n)
		}
	}
}

func TestResolveAndExcerpt(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.MkdirAll(filepath.Join(root, "node_modules/x"), 0o755)
	var src []string
	for i := 1; i <= 20; i++ {
		src = append(src, "line "+string(rune('a'+i-1)))
	}
	os.WriteFile(filepath.Join(root, "src/App.tsx"), []byte(strings.Join(src, "\n")+"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "node_modules/x/i.js"), []byte("x\n"), 0o644)
	for _, c := range []struct {
		file string
		rel  string
		ok   bool
	}{
		{filepath.Join(root, "src/App.tsx"), "src/App.tsx", true},
		{"/src/App.tsx", "src/App.tsx", true}, // served path
		{"./src/App.tsx", "src/App.tsx", true},
		{"src/App.tsx", "src/App.tsx", true},
		{filepath.Join(root, "node_modules/x/i.js"), "", false},
		{"/elsewhere/App.tsx", "", false},
		{"node:internal/x", "", false},
		{"../outside.ts", "", false},
	} {
		rel, ok := Resolve(root, Frame{File: c.file, Line: 1})
		if rel != c.rel || ok != c.ok {
			t.Errorf("Resolve(%q) = %q %v, want %q %v", c.file, rel, ok, c.rel, c.ok)
		}
	}
	ex := Excerpt(root, "src/App.tsx", 10, ExcerptLines)
	lines := strings.Split(ex, "\n")
	if len(lines) != 11 || !strings.HasPrefix(lines[0], "   5 | line e") || !strings.HasPrefix(lines[5], "> 10 | line j") {
		t.Fatalf("excerpt:\n%s", ex)
	}
	if ex := Excerpt(root, "src/App.tsx", 2, ExcerptLines); !strings.HasPrefix(ex, "  1 | line a") {
		t.Fatalf("excerpt near the top:\n%s", ex)
	}
}

// A console error is the page's text: its message is cleaned like a
// terminal line, so the one-line prompt carries no control characters.
func TestFromConsoleCleansMessage(t *testing.T) {
	b := FromConsole("Boom\x1b[201~\x1b[Z\x03 here", "", "http://localhost:3000/a.js", 3)
	if b.Lines[0] != "Boom here" || b.Message != "Boom here" {
		t.Fatalf("lines %q message %q", b.Lines, b.Message)
	}
	r := Report{Source: SourceBrowser, Text: strings.Join(b.Lines, "\n"), Kind: b.Kind}
	if p := InlinePrompt(r); strings.ContainsAny(p, "\x1b\x03") {
		t.Fatalf("prompt %q", p)
	}
}

// A link in the worktree must not make a file outside it "the project's
// own": error.get and error.send would put its lines in the excerpt (an
// agent could plant x.js -> ~/.local/share/pekoe/human-token).
func TestResolveAndExcerptStayInTree(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "wt")
	outside := filepath.Join(base, "secret")
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "token"), []byte("s3cret\n"), 0o600)
	os.WriteFile(filepath.Join(root, "src/App.tsx"), []byte("ok\n"), 0o644)
	os.Symlink(filepath.Join(outside, "token"), filepath.Join(root, "x.js"))
	os.Symlink(outside, filepath.Join(root, "dir"))
	os.Symlink("../secret/token", filepath.Join(root, "rel.js"))
	os.Symlink("App.tsx", filepath.Join(root, "src/alias.tsx")) // stays inside
	for _, c := range []struct {
		file string
		rel  string
		ok   bool
	}{
		{"/x.js", "", false},
		{"x.js", "", false},
		{filepath.Join(root, "x.js"), "", false},
		{"/dir/token", "", false},
		{"rel.js", "", false},
		{"/src/alias.tsx", "src/App.tsx", true},
	} {
		rel, ok := Resolve(root, Frame{File: c.file, Line: 1})
		if rel != c.rel || ok != c.ok {
			t.Errorf("Resolve(%q) = %q %v, want %q %v", c.file, rel, ok, c.rel, c.ok)
		}
	}
	for _, rel := range []string{"x.js", "dir/token", "rel.js", "src/alias.tsx", "../secret/token"} {
		if ex := Excerpt(root, rel, 1, ExcerptLines); ex != "" {
			t.Errorf("Excerpt(%q) read through a link: %q", rel, ex)
		}
	}
	// a root reached through a link (macOS /var -> /private/var) still works
	link := filepath.Join(base, "wtlink")
	os.Symlink(root, link)
	if rel, ok := Resolve(link, Frame{File: "/src/App.tsx", Line: 1}); !ok || rel != "src/App.tsx" {
		t.Errorf("Resolve through a linked root = %q %v", rel, ok)
	}
	if ex := Excerpt(link, "src/App.tsx", 1, ExcerptLines); !strings.Contains(ex, "ok") {
		t.Errorf("Excerpt through a linked root = %q", ex)
	}
}
