package tui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/state"
)

// evDir is a directory of evidence files: a real PNG, a text, a video, a
// script, a symlink named like an image that leads to the script, a folder.
// The path returned has its symlinks followed (macOS: /var → /private/var).
func evDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "shot.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 320, 200))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for name, body := range map[string]string{
		"before.txt": "before\n", "hello.mp4": "\x00\x00\x00\x18ftypmp42", "evil.command": "#!/bin/sh\ntouch pwned\n",
		"fake.png": "#!/bin/sh\ntouch pwned\n", "script.txt": "#!/bin/sh\ntouch pwned\n", "binary.log": "a\x00b",
		"alias.png": "book\x00\x00\x00\x00mark\x00\x00\x00\x00", "page.html": "<script>alert(1)</script>", "noext": "x", "a.out": "x",
		// a JPEG's first bytes, then a shell's lines: named only ".jpg" and runnable, macOS takes it for a program
		".jpg": "\xff\xd8\xff\ntouch pwned\n", "..jpg": "\xff\xd8\xff\ntouch pwned\n",
		"x.gİf": "GIF89a", "clip.mkv": "\x1a\x45\xdf\xa3", "SHOT.JPG": "\xff\xd8\xff\xe0",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "evil.command"), filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "Evil.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// What an evidence string is: only an http(s) URL and an existing file of a
// known kind open; the kind is the one behind a symlink.
func TestClassifyEvidence(t *testing.T) {
	dir := evDir(t)
	p := func(name string) string { return filepath.Join(dir, name) }
	for _, c := range []struct {
		raw, base string
		kind      evKind
		target    string
		inline    bool
		why       string
	}{
		{raw: "https://github.com/me/app/pull/42", kind: evLink, target: "https://github.com/me/app/pull/42"},
		{raw: " http://localhost:3000/x ", kind: evLink, target: "http://localhost:3000/x"},
		{raw: "https://", why: "not a plain http(s) URL"},
		{raw: "javascript:alert(1)", base: dir, why: "missing"},
		{raw: "file://" + p("evil.command"), base: dir, why: "missing"},
		{raw: p("shot.png"), kind: evImage, target: p("shot.png"), inline: true},
		{raw: "shot.png", base: dir, kind: evImage, target: p("shot.png"), inline: true},
		{raw: "shot.png", why: "a relative path, and the worktree is not known"},
		// named like an image or a text, but not one inside (a script, the data of a Finder alias): never opened
		{raw: p("fake.png"), kind: evFile, target: p("fake.png"), why: "its content is not what .png says"},
		{raw: p("alias.png"), kind: evFile, target: p("alias.png"), why: "its content is not what .png says"},
		{raw: p("script.txt"), kind: evFile, target: p("script.txt"), why: "its content is not what .txt says"},
		{raw: p("binary.log"), kind: evFile, target: p("binary.log"), why: "its content is not what .log says"},
		{raw: p("a.out"), kind: evFile, target: p("a.out")},
		{raw: p(".jpg"), kind: evFile, target: p(".jpg"), why: "its name is only an extension"},
		{raw: p("..jpg"), kind: evFile, target: p("..jpg"), why: "its name is only an extension"},
		{raw: p("x.gİf"), kind: evFile, target: p("x.gİf")}, // not .gif to the system
		{raw: p("SHOT.JPG"), kind: evImage, target: p("SHOT.JPG")},
		{raw: p("before.txt"), kind: evText, target: p("before.txt")},
		{raw: p("hello.mp4"), kind: evVideo, target: p("hello.mp4")},
		{raw: p("evil.command"), kind: evFile, target: p("evil.command")},
		{raw: p("page.html"), kind: evFile, target: p("page.html")},
		{raw: p("noext"), kind: evFile, target: p("noext")},
		{raw: p("link.png"), kind: evFile, target: p("evil.command")}, // the script behind the name
		{raw: p("Evil.app"), kind: evFolder, target: p("Evil.app")},
		{raw: p("gone.png"), why: "missing"},
		{raw: os.DevNull, why: "not a regular file"},
		{raw: "", why: "empty"},
	} {
		it := classifyEvidence(c.raw, c.base)
		if it.kind != c.kind || it.target != c.target || it.inline() != c.inline || it.why != c.why {
			t.Errorf("%q (base %q): kind %d target %q inline %v why %q, want kind %d target %q inline %v why %q",
				c.raw, c.base, it.kind, it.target, it.inline(), it.why, c.kind, c.target, c.inline, c.why)
		}
	}
	if it := classifyEvidence(p("shot.png"), ""); it.imgW != 320 || it.imgH != 200 || it.size == 0 {
		t.Errorf("the PNG's size: %d×%d, %d bytes", it.imgW, it.imgH, it.size)
	}
	// a video QuickTime Player does not play: shown where it is on macOS
	if it := classifyEvidenceOn("darwin", p("clip.mkv"), ""); it.kind != evFile || it.why != "QuickTime Player does not play .mkv" {
		t.Errorf("an .mkv on macOS: kind %d why %q", it.kind, it.why)
	}
	if it := classifyEvidenceOn("linux", p("clip.mkv"), ""); it.kind != evVideo {
		t.Errorf("an .mkv on Linux: kind %d why %q", it.kind, it.why)
	}
	// a file that cannot be read is not handed on unseen
	if os.Getuid() != 0 {
		if err := os.Chmod(p("before.txt"), 0); err != nil {
			t.Fatal(err)
		}
		if it := classifyEvidence(p("before.txt"), ""); it.kind != evNone || it.why != "cannot be read" || it.target != "" {
			t.Errorf("an unreadable text: %+v", it)
		}
	}
}

// What runs to open each kind: the path or URL is one argument, a file that
// is not an image, video, PDF or text is never handed to open as it is, and
// on macOS a file goes to a named app on the system volume, never to
// whatever Launch Services would pick for it.
func TestEvidenceOpener(t *testing.T) {
	dir := evDir(t)
	p := func(name string) string { return filepath.Join(dir, name) }
	for _, c := range []struct {
		goos, raw string
		want      []string
	}{
		{"darwin", "https://github.com/me/app/pull/42", []string{"open", "https://github.com/me/app/pull/42"}},
		{"linux", "https://github.com/me/app/pull/42", []string{"xdg-open", "https://github.com/me/app/pull/42"}},
		{"darwin", p("shot.png"), []string{"open", "-a", "/System/Applications/Preview.app", p("shot.png")}},
		{"darwin", p("SHOT.JPG"), []string{"open", "-a", "/System/Applications/Preview.app", p("SHOT.JPG")}},
		{"linux", p("shot.png"), []string{"xdg-open", p("shot.png")}},
		{"darwin", p("hello.mp4"), []string{"open", "-a", "/System/Applications/QuickTime Player.app", p("hello.mp4")}},
		{"linux", p("hello.mp4"), []string{"xdg-open", p("hello.mp4")}},
		{"darwin", p("clip.mkv"), []string{"open", "-R", p("clip.mkv")}},
		{"linux", p("clip.mkv"), []string{"xdg-open", p("clip.mkv")}},
		{"darwin", p("before.txt"), []string{"open", "-a", "/System/Applications/TextEdit.app", p("before.txt")}},
		{"linux", p("before.txt"), []string{"xdg-open", p("before.txt")}},
		{"darwin", p("evil.command"), []string{"open", "-R", p("evil.command")}},
		{"linux", p("evil.command"), []string{"xdg-open", dir}},
		{"darwin", p("link.png"), []string{"open", "-R", p("evil.command")}},
		{"darwin", p("fake.png"), []string{"open", "-R", p("fake.png")}},
		{"darwin", p(".jpg"), []string{"open", "-R", p(".jpg")}},
		{"linux", p(".jpg"), []string{"xdg-open", dir}},
		{"darwin", p("alias.png"), []string{"open", "-R", p("alias.png")}},
		{"darwin", p("script.txt"), []string{"open", "-R", p("script.txt")}},
		{"linux", p("script.txt"), []string{"xdg-open", dir}},
		{"darwin", p("page.html"), []string{"open", "-R", p("page.html")}},
		{"darwin", p("Evil.app"), []string{"open", "-R", p("Evil.app")}},
		{"linux", p("Evil.app"), []string{"xdg-open", p("Evil.app")}},
	} {
		name, args, said, err := evidenceOpener(c.goos, classifyEvidenceOn(c.goos, c.raw, ""))
		if got := append([]string{name}, args...); err != nil || !slices.Equal(got, c.want) || said == "" {
			t.Errorf("%s %q: %v (%q, %v), want %v", c.goos, c.raw, got, said, err, c.want)
		}
	}
	// on macOS no file is ever handed to open bare: Launch Services would choose what runs
	for _, name := range []string{"shot.png", "SHOT.JPG", "hello.mp4", "before.txt", "fake.png", ".jpg", "..jpg", "alias.png", "evil.command", "link.png", "noext", "Evil.app", "clip.mkv"} {
		_, args, _, err := evidenceOpener("darwin", classifyEvidenceOn("darwin", p(name), ""))
		if err != nil || len(args) < 2 || (args[0] != "-R" && !(args[0] == "-a" && strings.HasPrefix(args[1], "/System/Applications/") && len(args) == 3)) {
			t.Errorf("%s on macOS: open %v (%v)", name, args, err)
		}
	}
	for _, raw := range []string{p("gone.png"), "javascript:alert(1)", os.DevNull, ""} {
		if name, args, _, err := evidenceOpener("darwin", classifyEvidence(raw, dir)); err == nil {
			t.Errorf("%q would run %s %v", raw, name, args)
		}
	}
	// whatever says so, nothing but an absolute path or an http(s) URL is run
	for _, it := range []evItem{
		{kind: evImage, target: "-a Calculator.png"}, {kind: evText, target: "relative.txt"},
		{kind: evLink, target: "file:///tmp/x.command"}, {kind: evLink, target: "-R"},
	} {
		if name, args, _, err := evidenceOpener("darwin", it); err == nil {
			t.Errorf("%+v would run %s %v", it, name, args)
		}
	}
}

// What a file must start with to count as what its extension says.
func TestEvidenceContent(t *testing.T) {
	for _, c := range []struct {
		ext, head string
		ok        bool
	}{
		{".png", "\x89PNG\r\n\x1a\n....", true}, {".png", "GIF89a", false}, {".png", "", false},
		{".jpg", "\xff\xd8\xff\xe0", true}, {".jpeg", "\xff\xd8\xff\xe1", true}, {".jpg", "#!/bin/sh", false},
		{".gif", "GIF89a", true}, {".webp", "RIFF\x00\x00\x00\x00WEBPVP8 ", true}, {".webp", "RIFF\x00\x00\x00\x00AVI ", false},
		{".bmp", "BM....", true}, {".tiff", "II*\x00", true}, {".tif", "MM\x00*", true},
		{".heic", "\x00\x00\x00\x18ftypheic", true}, {".avif", "\x00\x00\x00\x1cftypavif", true},
		{".mp4", "\x00\x00\x00\x18ftypmp42", true}, {".mp4", "#!/bin/sh\n", false},
		{".mov", "\x00\x00\x00\x14ftypqt  ", true}, {".mov", "\x00\x00\x00\x08wide", true}, {".mov", "book\x00\x00\x00\x00mark", false},
		{".webm", "\x1a\x45\xdf\xa3", true}, {".mkv", "\x1a\x45\xdf\xa3", true}, {".avi", "RIFF\x00\x00\x00\x00AVI LIST", true},
		{".pdf", "%PDF-1.7", true}, {".pdf", "<html>", false},
		{".txt", "before\nafter\n", true}, {".txt", "", true}, {".md", "# 日本語\n", true}, {".json", "{}", true},
		{".txt", "#!/bin/sh\n", false}, {".log", "a\x00b", false},
		{".command", "echo", false}, {"", "x", false}, {".html", "<p>", false},
	} {
		if got := evContentOK(c.ext, []byte(c.head)); got != c.ok {
			t.Errorf("%s starting %q: %v, want %v", c.ext, c.head, got, c.ok)
		}
	}
	// every extension that opens has a rule of its own, or is text
	for ext, kind := range evExts {
		if kind != evText && evContentOK(ext, []byte("plain words, no signature")) {
			t.Errorf("%s opens whatever it holds", ext)
		}
	}
}

// A canvas too large to hand to the terminal is not drawn in place.
func TestEvidencePNGLimits(t *testing.T) {
	dir := evDir(t)
	big := filepath.Join(dir, "big.png")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	// 8000×8000 is within a side's limit but 64 M pixels; a paletted image keeps the file and the test small
	if err := png.Encode(f, image.NewPaletted(image.Rect(0, 0, 8000, 8000), color.Palette{color.Black})); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if it := classifyEvidence(big, ""); it.kind != evImage || it.inline() {
		t.Fatalf("an 8000×8000 PNG: kind %d, drawn in place %v", it.kind, it.inline())
	}
}

// A file's name is the agent's to choose, line breaks included (the name
// behind a symlink is not cleaned by pekoed): it is drawn on one line.
func TestEvidenceNameOnOneLine(t *testing.T) {
	m, _, dir, _ := evidenceDash(t, func(dir string) []state.Report {
		odd := filepath.Join(dir, "a\nFAKE LINE  ↵ opens it in the browser\tb.txt")
		if err := os.WriteFile(odd, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(odd, filepath.Join(dir, "notes.txt")); err != nil {
			t.Fatal(err)
		}
		return []state.Report{{ID: 7, Slug: "feat-csv", Status: "done", At: time.Now(), Evidence: []string{filepath.Join(dir, "notes.txt")}}}
	})
	runCmd(m, m.onKey(key("o")))
	em, ok := m.modal.(*evidenceModal)
	if !ok {
		t.Fatalf("modal %T, msg %q", m.modal, m.msg)
	}
	if em.items[0].kind != evText || !strings.Contains(em.items[0].target, "\n") {
		t.Fatalf("the item: %+v", em.items[0])
	}
	runCmd(m, m.onKey(key("enter"))) // the note names the file too
	lines := em.view()
	for _, l := range lines {
		if w(l) != w(lines[0]) || strings.ContainsAny(l, "\n\t") {
			t.Fatalf("a modal line is %d cells (not %d) or holds a break: %q", w(l), w(lines[0]), ansi.Strip(l))
		}
	}
	out := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(out, filepath.Base(dir)+"/a FAKE LINE ↵ opens it in the browser b.txt") || strings.Count(out, "\n") != len(lines)-1 {
		t.Errorf("the name on one line:\n%s", out)
	}
}

// What ↵ is said to do, by kind, on macOS (the app named) and elsewhere
// (xdg-open's default).
func TestEvidenceWhat(t *testing.T) {
	png := evItem{kind: evImage, imgW: 10, imgH: 10}
	jpg := evItem{kind: evImage}
	for _, c := range []struct {
		goos     string
		it       evItem
		graphics bool
		want     string
	}{
		{"darwin", jpg, false, "↵ opens it in Preview"},
		{"linux", jpg, false, "↵ opens it in the default app"},
		{"darwin", png, false, "↵ opens it in Preview"},
		{"darwin", png, true, "↵ shows it here · o opens it in Preview"},
		{"linux", png, true, "↵ shows it here · o opens it in the default app"},
		{"linux", png, false, "↵ opens it in the default app"},
		{"darwin", evItem{kind: evPDF}, false, "↵ opens it in Preview"},
		{"darwin", evItem{kind: evVideo}, false, "↵ opens it in QuickTime Player"},
		{"linux", evItem{kind: evVideo}, false, "↵ opens it in the default app"},
		{"darwin", evItem{kind: evText}, false, "↵ opens it in TextEdit"},
		{"linux", evItem{kind: evText}, false, "↵ opens it in the default app"},
		{"darwin", evItem{kind: evLink}, false, "↵ opens it in the browser"},
		{"linux", evItem{kind: evLink}, false, "↵ opens it in the browser"},
		{"linux", evItem{kind: evFile}, false, "not opened (only images, videos, PDFs and text are) · ↵ shows where it is"},
		{"linux", evItem{kind: evFolder}, false, "a folder · ↵ shows where it is"},
		{"linux", evItem{kind: evNone, why: "missing"}, false, "nothing to open"},
	} {
		if got := evWhatOn(c.goos, c.it, c.graphics); got != c.want {
			t.Errorf("%s kind %d graphics %v: %q, want %q", c.goos, c.it.kind, c.graphics, got, c.want)
		}
	}
}

// The list: newest report first, each string once under the newest report
// naming it, reports without evidence left out, a review's verdict shown.
func TestEvidenceItems(t *testing.T) {
	now := time.Now()
	reports, items := evidenceItems([]state.Report{
		{ID: 9, Status: "review", Verdict: "pass", Evidence: []string{"https://github.com/me/app/pull/42"}, At: now},
		{ID: 8, Status: "progress", Summary: "no evidence", Evidence: []string{}, At: now},
		{ID: 7, Status: "done", Summary: "直した", Evidence: []string{"https://github.com/me/app/pull/42", " ", "/nowhere/shot.png"}, At: now},
	}, "")
	if len(reports) != 2 || reports[0].id != 9 || reports[0].status != "review pass" || reports[1].id != 7 {
		t.Fatalf("reports: %+v", reports)
	}
	if len(items) != 2 || items[0].report != 0 || items[0].kind != evLink || items[1].report != 1 || items[1].why != "missing" {
		t.Fatalf("items: %+v", items)
	}
}

// evidenceDash is the dashboard on feat-csv's pipeline row, with reports
// naming the files of evDir; opened records what would have run.
func evidenceDash(t *testing.T, reports func(dir string) []state.Report) (m *model, f *fakePekoed, dir string, opened *[][]string) {
	t.Helper()
	dir = evDir(t)
	m, f = dashModel(t)
	old := f.reply
	f.reply = func(method string) any {
		if method == "report.list" {
			return reports(dir)
		}
		return old(method)
	}
	m.sessions[0].Path = dir // feat-csv's worktree
	opened = &[][]string{}
	run := runOpener
	runOpener = func(name string, args ...string) error {
		*opened = append(*opened, append([]string{name}, args...))
		return nil
	}
	t.Cleanup(func() { runOpener = run })
	m.graphics = false
	runCmd(m, m.onKey(key("4")))
	m.viewAgent(120, 60)
	for m.dash.sel != "s:feat-csv" {
		before := m.dash.sel
		runCmd(m, m.onKey(key("j")))
		if m.dash.sel == before {
			t.Fatalf("j stuck at %q", before)
		}
	}
	return m, f, dir, opened
}

func evReports2(dir string) []state.Report {
	now := time.Now()
	return []state.Report{
		{ID: 9, Slug: "feat-csv", Status: "review", Verdict: "pass", Summary: "LGTM", Evidence: []string{"https://github.com/me/app/pull/42"}, At: now},
		{ID: 7, Slug: "feat-csv", Status: "done", Summary: "直した " + hostile + "\n二行目", At: now.Add(-time.Hour), Evidence: []string{
			filepath.Join(dir, "shot.png"), "before.txt", filepath.Join(dir, "hello.mp4"),
			filepath.Join(dir, "evil.command"), filepath.Join(dir, "gone.png"), "https://github.com/me/app/pull/42",
			"/x/" + hostile + ".png",
		}},
	}
}

// o on a pipeline row lists the session's evidence; ↵ opens the chosen one
// outside (no Kitty graphics here), shows a script only where it is, and
// says so when the file is gone. The agents' words are drawn as text.
func TestDashboardEvidence(t *testing.T) {
	m, f, dir, opened := evidenceDash(t, evReports2)
	if out := ansi.Strip(strings.Join(m.viewAgent(120, 60), "\n")); !strings.Contains(out, "o evidence") {
		t.Errorf("the pipeline's head does not name the key:\n%s", out)
	}
	runCmd(m, m.onKey(key("o")))
	em, ok := m.modal.(*evidenceModal)
	if !ok {
		t.Fatalf("o on a pipeline row: modal %T, msg %q", m.modal, m.msg)
	}
	if ps := f.find("report.list"); len(ps) != 1 || ps[0]["slug"] != "feat-csv" {
		t.Fatalf("report.list: %v", ps)
	}
	if name, _ := m.modeName(); name != "EVIDENCE" {
		t.Errorf("mode %q", name)
	}
	raw := strings.Join(m.modal.view(), "\n")
	if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "‮") {
		t.Fatalf("an agent's escape or bidi override was drawn: %q", raw)
	}
	out := ansi.Strip(raw)
	t.Log("\n" + out)
	at := 0
	for _, want := range []string{"Evidence feat-csv · 7 in 2 reports", "p9 review pass", "LGTM", "link", "https://github.com/me/app/pull/42",
		"p7 done", "直した <script>", "image", "shot.png", "png 320×200", "text", "before.txt", "7 B", "video", "hello.mp4", "file", "evil.command",
		"missing", "gone.png", "missing", "<script>alert(1)</script>x.png",
		"https://github.com/me/app/pull/42", "↵ opens it in the browser", "j/k move", "↵ open", "o open in app", "esc close"} {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order (after byte %d):\n%s", want, at, out)
		}
		at += i + len(want)
	}
	if strings.Contains(out, "二行目") || strings.Count(out, "pull/42") != 2 { // the row and the sunk line: the older report's copy is not listed
		t.Errorf("a folded line or a repeated string shown:\n%s", out)
	}
	width := w(strings.Split(raw, "\n")[0])
	for _, l := range strings.Split(raw, "\n") {
		if w(l) != width {
			t.Fatalf("a modal line is %d cells, not %d: %q", w(l), width, ansi.Strip(l))
		}
	}

	last := func() []string {
		if len(*opened) == 0 {
			return nil
		}
		return (*opened)[len(*opened)-1]
	}
	press := func(k string) { t.Helper(); runCmd(m, m.onKey(key(k))) }
	// the link
	press("enter")
	if got := last(); len(got) != 2 || got[1] != "https://github.com/me/app/pull/42" || !strings.Contains(em.note, "opened https://github.com/me/app/pull/42") {
		t.Fatalf("↵ on the link ran %v, note %q", got, em.note)
	}
	// the image: no graphics here, so an app outside
	press("j")
	if em.note != "" {
		t.Errorf("the note stayed after moving: %q", em.note)
	}
	// the sunk line names the file (cut from the left when long: a temp
	// dir's path is long on macOS, short on Linux), what ↵ does under it
	view := ansi.Strip(strings.Join(em.view(), "\n"))
	shotPath := filepath.Join(dir, "shot.png")
	var sunkLine, whatLine string
	for i, l := range strings.Split(view, "\n") {
		l = strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "│"))
		if strings.HasSuffix(l, string(filepath.Separator)+"shot.png") && strings.HasSuffix(shotPath, strings.TrimPrefix(l, "…")) {
			sunkLine, whatLine = l, strings.TrimSpace(strings.Trim(strings.TrimSpace(strings.Split(view, "\n")[i+1]), "│"))
		}
	}
	if want := evWhatOn(runtime.GOOS, em.items[em.sel], false); sunkLine == "" || whatLine != want {
		t.Errorf("the image's path (%q) and what ↵ does (%q, want %q):\n%s", sunkLine, whatLine, want, view)
	}
	press("enter")
	if got := last(); len(got) < 2 || got[len(got)-1] != filepath.Join(dir, "shot.png") || em.viewing {
		t.Fatalf("↵ on the image without graphics ran %v (viewing %v)", got, em.viewing)
	}
	// the text, written relative to the worktree
	press("j")
	press("enter")
	if got := last(); got[len(got)-1] != filepath.Join(dir, "before.txt") {
		t.Fatalf("↵ on the text ran %v", got)
	}
	// the script: never handed to open as it is
	press("j")
	press("j")
	if out := ansi.Strip(strings.Join(em.view(), "\n")); !strings.Contains(out, "not opened (only images, videos, PDFs and text are)") {
		t.Errorf("the script's line:\n%s", out)
	}
	n := len(*opened)
	press("enter")
	script := filepath.Join(dir, "evil.command")
	shownOnly := slices.Equal(last(), []string{"open", "-R", script}) || slices.Equal(last(), []string{"xdg-open", dir})
	if got := last(); len(*opened) != n+1 || !shownOnly || !strings.Contains(em.note, "not opened") {
		t.Fatalf("↵ on the script ran %v, note %q", got, em.note)
	}
	// gone since the list was read: nothing runs, the row says missing
	press("k")
	if err := os.Remove(filepath.Join(dir, "hello.mp4")); err != nil {
		t.Fatal(err)
	}
	n = len(*opened)
	press("o")
	if len(*opened) != n || !em.noteErr || !strings.Contains(em.note, "missing") || em.items[em.sel].kind != evNone {
		t.Fatalf("o on a file that is gone ran %v, note %q, kind %d", last(), em.note, em.items[em.sel].kind)
	}
	// never there
	press("j")
	press("j")
	press("enter")
	if len(*opened) != n || !strings.Contains(em.note, "missing") {
		t.Fatalf("↵ on a missing file ran %v, note %q", last(), em.note)
	}
	press("esc")
	if m.modal != nil {
		t.Fatalf("esc left %T", m.modal)
	}
}

// With Kitty graphics, ↵ on a PNG draws it in the modal: the terminal is
// sent the file's path once, placeholder cells mark where it goes, j/k go to
// the next image, o opens it outside, and leaving frees the image.
func TestEvidenceImageInPlace(t *testing.T) {
	m, _, dir, opened := evidenceDash(t, func(dir string) []state.Report {
		f, err := os.Create(filepath.Join(dir, "after.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 100, 400))); err != nil {
			t.Fatal(err)
		}
		return []state.Report{{ID: 7, Slug: "feat-csv", Status: "done", Summary: "直した", At: time.Now(), Evidence: []string{
			filepath.Join(dir, "shot.png"), filepath.Join(dir, "before.txt"), filepath.Join(dir, "fake.png"), filepath.Join(dir, "after.png"),
		}}}
	})
	m.graphics = true
	var term lockedBuf // what the outer terminal is sent
	oldOut := termOut
	termOut = &term
	t.Cleanup(func() { termOut = oldOut })
	runCmd(m, m.onKey(key("o")))
	em, ok := m.modal.(*evidenceModal)
	if !ok {
		t.Fatalf("modal %T, msg %q", m.modal, m.msg)
	}
	if out := ansi.Strip(strings.Join(em.view(), "\n")); !strings.Contains(out, "↵ shows it here · o opens it in ") {
		t.Errorf("what ↵ does on a PNG:\n%s", out)
	}
	upd := func(k string) { t.Helper(); _, c := m.Update(key(k)); runCmd(m, c) }
	upd("enter")
	shot, after := filepath.Join(dir, "shot.png"), filepath.Join(dir, "after.png")
	held := func() []string {
		var fs []string
		for k := range m.pv.live {
			fs = append(fs, k.file)
		}
		slices.Sort(fs)
		return fs
	}
	if !em.viewing || len(*opened) != 0 || !slices.Equal(held(), []string{shot}) {
		t.Fatalf("↵ on the PNG: viewing %v, ran %v, the terminal holds %v", em.viewing, *opened, held())
	}
	k, _ := em.shown()
	id := m.pv.live[k]
	if got := term.take(); got != transmitImage(id, shot, k.cols, k.rows) {
		t.Fatalf("the terminal was sent %q", got)
	}
	cw, ch := em.area()
	if k.cols < 1 || k.rows < 1 || k.cols > cw || k.rows > ch || (k.cols != cw && k.rows != ch) {
		t.Fatalf("the image takes %d×%d cells of %d×%d", k.cols, k.rows, cw, ch)
	}
	lines := m.modal.view()
	raw := strings.Join(lines, "\n")
	if n := strings.Count(raw, string(placeholder)); n != k.cols*k.rows {
		t.Fatalf("%d placeholder cells drawn, want %d×%d", n, k.cols, k.rows)
	}
	if len(lines) > m.height-1 {
		t.Fatalf("the modal is %d lines on a screen of %d", len(lines), m.height)
	}
	for _, l := range lines {
		if w(l) != w(lines[0]) {
			t.Fatalf("a modal line is %d cells, not %d", w(l), w(lines[0]))
		}
	}
	if out := ansi.Strip(raw); !strings.Contains(out, "Evidence feat-csv · shot.png") || !strings.Contains(out, "png 320×200") ||
		!strings.Contains(out, "report p7 done · 1/2") || !strings.Contains(out, "j/k next / previous image") {
		t.Errorf("the image's caption and keys:\n%s", out)
	}
	if !strings.Contains(m.View(), string(placeholder)) {
		t.Error("the placeholder cells did not reach the screen")
	}
	// j skips the text and the file that only looks like a PNG
	upd("j")
	if em.sel != 3 || !slices.Equal(held(), []string{after}) {
		t.Fatalf("j: item %d, the terminal holds %v", em.sel, held())
	}
	if got := term.take(); !strings.HasPrefix(got, deleteImage(id)) || !strings.Contains(got, "a=T,t=f,f=100,U=1") {
		t.Fatalf("j: the terminal was sent %q", got)
	}
	upd("j") // no further image
	upd("k")
	if em.sel != 0 || !slices.Equal(held(), []string{shot}) {
		t.Fatalf("k: item %d, the terminal holds %v", em.sel, held())
	}
	// a resize draws it again at the new size and frees the old one
	m.width, m.height = 100, 30
	upd("x")
	if k2, _ := em.shown(); k2 == k || len(m.pv.live) != 1 || m.pv.live[k2] == 0 {
		t.Fatalf("after a resize the terminal holds %v (was %v)", m.pv.live, k)
	}
	// o: an app outside, still viewing
	upd("o")
	if got := *opened; len(got) != 1 || got[0][len(got[0])-1] != shot || !em.viewing {
		t.Fatalf("o while viewing ran %v (viewing %v)", got, em.viewing)
	}
	if out := ansi.Strip(strings.Join(em.view(), "\n")); !strings.Contains(out, "opened shot.png") {
		t.Errorf("what o did is not said:\n%s", out)
	}
	// back to the list: the image is freed
	upd("esc")
	if em.viewing || len(m.pv.live) != 0 || m.modal == nil {
		t.Fatalf("esc: viewing %v, the terminal holds %v, modal %T", em.viewing, held(), m.modal)
	}
	if got := term.take(); !strings.HasSuffix(got, "a=d,d=I,i="+fmt.Sprint(m.pv.nextID|0x0C0000)+",q=2\x1b\\") {
		t.Fatalf("esc: the terminal was sent %q", got)
	}
	// o in the list opens a PNG outside even with graphics
	upd("o")
	if len(*opened) != 2 || em.viewing {
		t.Fatalf("o in the list ran %v (viewing %v)", *opened, em.viewing)
	}
	// closing while viewing frees it too
	upd("enter")
	if !em.viewing || len(m.pv.live) != 1 {
		t.Fatalf("↵ again: viewing %v, holds %v", em.viewing, held())
	}
	m.modal = nil
	runCmd(m, m.previewSync())
	if len(m.pv.live) != 0 {
		t.Fatalf("the image outlived the modal: %v", held())
	}
}

// lockedBuf collects what writeTerm's commands write.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// take returns what was written since the last take.
func (l *lockedBuf) take() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.b.String()
	l.b.Reset()
	return s
}

// Nothing to list: no modal, a line saying which.
func TestDashboardEvidenceNone(t *testing.T) {
	m, _, _, _ := evidenceDash(t, func(string) []state.Report {
		return []state.Report{{ID: 7, Slug: "feat-csv", Status: "progress", Summary: "途中", Evidence: []string{}}}
	})
	runCmd(m, m.onKey(key("o")))
	if m.modal != nil || m.msg != "no evidence in feat-csv's reports" {
		t.Fatalf("modal %T, msg %q", m.modal, m.msg)
	}
	m2, _, _, _ := evidenceDash(t, func(string) []state.Report { return nil })
	runCmd(m2, m2.onKey(key("o")))
	if m2.modal != nil || m2.msg != "feat-csv has not reported yet" {
		t.Fatalf("modal %T, msg %q", m2.modal, m2.msg)
	}
}

// The dashboard's timeline lists sessions the [1] filter hides; o on one
// still finds its worktree, so evidence given relative to it opens.
func TestDashboardEvidenceUnderFilter(t *testing.T) {
	m, _, _, _ := evidenceDash(t, evReports2)
	m.filter = "fix" // feat-csv is hidden from [1], not from the timeline
	m.viewAgent(120, 60)
	if m.dash.sel != "s:feat-csv" {
		t.Fatalf("sel %q", m.dash.sel)
	}
	runCmd(m, m.onKey(key("o")))
	em, ok := m.modal.(*evidenceModal)
	if !ok {
		t.Fatalf("modal %T msg %q", m.modal, m.msg)
	}
	found := false
	for _, it := range em.items {
		if it.raw == "before.txt" {
			found = true
			if it.kind != evText {
				t.Errorf("before.txt (relative to the worktree) under a filter: kind %v, why %q (base %q)", it.kind, it.why, em.base)
			}
		}
	}
	if !found {
		t.Fatal("before.txt not listed")
	}
}

// On a narrow terminal the name keeps its room: what follows it (the PNG's
// size in pixels, then its size in bytes) gives way first.
func TestEvidenceNarrowKeepsName(t *testing.T) {
	m, _, _, _ := evidenceDash(t, func(dir string) []state.Report {
		f, err := os.Create(filepath.Join(dir, "after-shot.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		// one color: small, and read in time under -race
		if err := png.Encode(f, image.NewPaletted(image.Rect(0, 0, 1400, 900), color.Palette{color.Black})); err != nil {
			t.Fatal(err)
		}
		return []state.Report{{ID: 7, Slug: "feat-csv", Status: "done", At: time.Now(), Evidence: []string{
			filepath.Join(dir, "after-shot.png"), filepath.Join(dir, "before.txt"),
		}}}
	})
	runCmd(m, m.onKey(key("o")))
	em, ok := m.modal.(*evidenceModal)
	if !ok {
		t.Fatalf("modal %T, msg %q", m.modal, m.msg)
	}
	for _, tc := range []struct {
		width      int
		want, gone []string
	}{
		{120, []string{"after-shot.png  png 1400×900 · ", "before.txt  7 B"}, nil},
		{60, []string{"after-shot.png", "before.txt  7 B"}, []string{"1400×900"}},
		{40, []string{"after-shot", "before.txt"}, []string{"1400×900", " B"}},
	} {
		m.width, m.height = tc.width, 20
		lines := em.view()
		var rows []string // the list's rows, not the path under them
		for _, l := range lines {
			if l := ansi.Strip(l); strings.Contains(l, " image ") || strings.Contains(l, " text ") {
				rows = append(rows, l)
			}
		}
		out := strings.Join(rows, "\n")
		for _, s := range tc.want {
			if !strings.Contains(out, s) {
				t.Errorf("width %d: %q is not shown:\n%s", tc.width, s, out)
			}
		}
		for _, s := range tc.gone {
			if strings.Contains(out, s) {
				t.Errorf("width %d: %q is shown, the name should have the room:\n%s", tc.width, s, out)
			}
		}
		for _, l := range lines {
			if w(l) != w(lines[0]) {
				t.Fatalf("width %d: a modal line is %d cells, not %d: %q", tc.width, w(l), w(lines[0]), ansi.Strip(l))
			}
		}
	}
}
