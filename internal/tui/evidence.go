package tui

// Evidence (docs/orchestration.md「ダッシュボード」→「evidence を開く」): o on
// a pipeline row of the dashboard lists what the session's reports named as
// evidence (screenshots, videos, logs, the PR's URL) in a modal; ↵ opens the
// chosen one. A PNG is drawn in place when the terminal draws Kitty
// graphics; anything else goes to an app outside (open / xdg-open), a URL
// to the browser.
//
// Evidence is a string an agent wrote: untrusted. Nothing here runs it. What
// opens is an http(s) URL, or a regular file that exists now, whose
// extension says image, video, PDF or text and whose first bytes agree.
// Any other file (a .command, a script, an .html, a file that only looks
// like an image …) and a directory are only shown where they are (open
// -R), never opened. The kind is that of the file behind every symlink, and
// that path is the one opened.
//
// macOS decides for itself what a file is and what opens it (Launch
// Services: a name that is only ".jpg" with the executable bit is a program
// for Terminal; a file can carry its own "Open With"; an app lying around
// can claim a type nobody owns). So on macOS the app is named, by its path
// on the system volume: Preview for images and PDFs, QuickTime Player for
// videos, TextEdit for text. What a file says about itself then changes
// nothing; a file that says such things is not opened anyway
// (evidence_darwin.go).

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/ids"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

const (
	evReports = 20 // reports read (report.list's default)
	// a PNG drawn in place: the terminal reads and decodes the file, so
	// neither a huge file nor a huge canvas (4 bytes a pixel, decoded) is
	// handed to it
	evInlineBytes  = 32 << 20
	evInlineSide   = 16384
	evInlinePixels = 50_000_000
	evSniff        = 4096 // bytes read to see that a file starts like its extension says
)

type evKind int

const (
	evNone  evKind = iota // nothing to open: evItem.why says why
	evLink                // an http(s) URL
	evImage               // by extension, like the three below
	evVideo
	evPDF
	evText
	evFile   // a regular file of another kind: shown where it is, not opened
	evFolder // a directory: shown where it is
)

var evKindWords = map[evKind]string{
	evLink: "link", evImage: "image", evVideo: "video", evPDF: "pdf", evText: "text", evFile: "file", evFolder: "folder",
}

// The apps macOS opens evidence with, by path: on the sealed system volume,
// so nothing an agent can write stands in for them.
const (
	macPreview   = "/System/Applications/Preview.app"
	macQuickTime = "/System/Applications/QuickTime Player.app"
	macTextEdit  = "/System/Applications/TextEdit.app"
)

// evMacNoApp are the videos QuickTime Player does not play: on macOS they
// are shown where they are, for the person to open with what they have.
var evMacNoApp = map[string]bool{".webm": true, ".mkv": true, ".avi": true}

// evExts is what opens, by extension. Everything else is evFile.
var evExts = map[string]evKind{
	".png": evImage, ".jpg": evImage, ".jpeg": evImage, ".gif": evImage, ".webp": evImage,
	".bmp": evImage, ".tif": evImage, ".tiff": evImage, ".heic": evImage, ".avif": evImage,
	".mp4": evVideo, ".mov": evVideo, ".m4v": evVideo, ".webm": evVideo, ".mkv": evVideo, ".avi": evVideo,
	".pdf": evPDF,
	".txt": evText, ".md": evText, ".log": evText, ".json": evText, ".jsonl": evText,
	".diff": evText, ".patch": evText, ".csv": evText, ".tsv": evText, ".yaml": evText, ".yml": evText, ".toml": evText,
}

// evItem is one evidence string and what it turned out to be.
type evItem struct {
	raw    string // as the agent wrote it
	report int    // index of its report in evidenceModal.reports
	kind   evKind
	target string // the URL, or the file's path behind every symlink
	size   int64
	imgW   int // a PNG's pixels when it can be drawn in place (0: it cannot)
	imgH   int
	why    string // evNone: why nothing opens; evFile: why it is not opened, when its extension said it would be
}

// inline: the terminal can draw it from its path (a PNG within the limits).
func (it evItem) inline() bool { return it.kind == evImage && it.imgW > 0 && it.imgH > 0 }

// name is what the row shows: the file's name, or the string as written.
func (it evItem) name() string {
	s := firstLine(inboxText(it.raw))
	switch it.kind {
	case evNone, evLink:
		return s
	}
	return filepath.Base(strings.TrimRight(s, "/"))
}

// classifyEvidence reads what raw is now. base is the session's worktree,
// where a relative path starts ("" when unknown).
func classifyEvidence(raw, base string) evItem {
	return classifyEvidenceOn(runtime.GOOS, raw, base)
}

// classifyEvidenceOn is classifyEvidence for goos (what opens differs).
func classifyEvidenceOn(goos, raw, base string) evItem {
	it := evItem{raw: raw}
	s := strings.TrimSpace(raw)
	switch {
	case s == "":
		it.why = "empty"
		return it
	case strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://"):
		if checkWebURL(s) != nil {
			it.why = "not a plain http(s) URL"
			return it
		}
		it.kind, it.target = evLink, s
		return it
	}
	p := s
	if !filepath.IsAbs(p) {
		if base == "" || !filepath.IsAbs(base) {
			it.why = "a relative path, and the worktree is not known"
			return it
		}
		p = filepath.Join(base, p)
	}
	real, err := filepath.EvalSymlinks(p)
	var fi os.FileInfo
	if err == nil {
		fi, err = os.Stat(real)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		it.why = "missing"
		return it
	case err != nil || !filepath.IsAbs(real):
		it.why = "cannot be read"
		return it
	}
	it.target = real
	switch {
	case fi.IsDir():
		it.kind = evFolder
	case !fi.Mode().IsRegular():
		it.target, it.why = "", "not a regular file"
	default:
		it.size = fi.Size()
		name := filepath.Base(real)
		ext := asciiLower(filepath.Ext(name))
		it.kind = evExts[ext]
		if it.kind == evNone {
			it.kind = evFile
			break
		}
		head, ok := evHead(real)
		switch {
		case !ok:
			it.kind, it.target, it.why = evNone, "", "cannot be read"
		case strings.Trim(strings.TrimSuffix(name, filepath.Ext(name)), ".") == "":
			// ".jpg": to macOS a file without an extension, a program when it may be run
			it.kind, it.why = evFile, "its name is only an extension"
		case !evContentOK(ext, head):
			it.kind, it.why = evFile, "its content is not what "+ext+" says"
		case goos == "darwin" && macOpensItsOwnWay(real):
			it.kind, it.why = evFile, "it names what opens it itself (a Finder alias or its own Open With)"
		case goos == "darwin" && evMacNoApp[ext]:
			it.kind, it.why = evFile, "QuickTime Player does not play "+ext
		case ext == ".png":
			it.imgW, it.imgH = pngSize(real, it.size)
		}
	}
	return it
}

// asciiLower lowers A–Z only: "x.gİf" is not a .gif to the system, so it
// is not one here (strings.ToLower folds İ to i).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// evHead is the first bytes of a regular file; ok is false when it cannot
// be read.
func evHead(path string) (head []byte, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	h := make([]byte, evSniff)
	n, err := io.ReadFull(f, h)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false
	}
	return h[:n], true
}

// evContentOK: a file with extension ext starts like one (h is its first
// bytes). open and xdg-open may look past the name (a Finder alias is
// found by what is in it; xdg-open can go by content), so a name alone
// does not make a file an image. Text has no signature: it must hold no
// NUL and not start like a script.
func evContentOK(ext string, h []byte) bool {
	has := func(off int, sig string) bool { return len(h) >= off+len(sig) && string(h[off:off+len(sig)]) == sig }
	switch ext {
	case ".png":
		return has(0, "\x89PNG\r\n\x1a\n")
	case ".jpg", ".jpeg":
		return has(0, "\xff\xd8\xff")
	case ".gif":
		return has(0, "GIF87a") || has(0, "GIF89a")
	case ".webp":
		return has(0, "RIFF") && has(8, "WEBP")
	case ".bmp":
		return has(0, "BM")
	case ".tif", ".tiff":
		return has(0, "II*\x00") || has(0, "MM\x00*")
	case ".heic", ".avif", ".mp4", ".m4v":
		return has(4, "ftyp")
	case ".mov": // older QuickTime files open with another atom
		return has(4, "ftyp") || has(4, "moov") || has(4, "mdat") || has(4, "wide") || has(4, "free") || has(4, "skip")
	case ".webm", ".mkv":
		return has(0, "\x1a\x45\xdf\xa3")
	case ".avi":
		return has(0, "RIFF") && has(8, "AVI ")
	case ".pdf":
		return has(0, "%PDF-")
	}
	return evExts[ext] == evText && !bytes.ContainsRune(h, 0) && !has(0, "#!")
}

// pngSize is a PNG's size in pixels from its header, 0×0 when it is not one
// the terminal should be handed (not a PNG, too large).
func pngSize(path string, size int64) (int, int) {
	if size > evInlineBytes {
		return 0, 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	c, err := png.DecodeConfig(io.LimitReader(f, 1<<20))
	if err != nil || c.Width < 1 || c.Height < 1 || c.Width > evInlineSide || c.Height > evInlineSide ||
		c.Width*c.Height > evInlinePixels {
		return 0, 0
	}
	return c.Width, c.Height
}

// evidenceOpener is the command that opens it outside the TUI on goos, and
// what to say when it ran. The path or URL is one argument of open /
// xdg-open, never a shell's; a path starts with "/", so it is no flag. On
// macOS a file goes to a named app (open -a <the app's path>), not to
// whatever Launch Services would pick for it.
func evidenceOpener(goos string, it evItem) (name string, args []string, said string, err error) {
	mac := goos == "darwin"
	name = "xdg-open"
	if mac {
		name = "open"
	}
	if it.kind != evNone && it.kind != evLink && !filepath.IsAbs(it.target) {
		return "", nil, "", errors.New("not opened: not an absolute path")
	}
	switch it.kind {
	case evLink:
		if err := checkWebURL(it.target); err != nil {
			return "", nil, "", err
		}
		return name, []string{it.target}, "opened " + it.target, nil
	case evImage, evVideo, evPDF, evText:
		if mac {
			app := evMacApp(it.kind)
			return name, []string{"-a", app, it.target}, "opened " + it.name() + " in " + evAppName(app), nil
		}
		return name, []string{it.target}, "opened " + it.name(), nil
	case evFile, evFolder:
		if mac {
			return name, []string{"-R", it.target}, "showed " + it.name() + " in Finder (not opened)", nil
		}
		dir := it.target
		if it.kind == evFile {
			dir = filepath.Dir(dir)
		}
		return name, []string{dir}, "opened the folder of " + it.name() + " (the file itself is not opened)", nil
	}
	why := it.why
	if why == "" {
		why = "nothing to open"
	}
	return "", nil, "", errors.New(why + ": " + firstLine(inboxText(it.raw)))
}

// evMacApp is the app macOS opens a kind with.
func evMacApp(k evKind) string {
	switch k {
	case evVideo:
		return macQuickTime
	case evText:
		return macTextEdit
	}
	return macPreview
}

// evAppName is an app's name from its path ("Preview").
func evAppName(app string) string {
	return strings.TrimSuffix(filepath.Base(app), ".app")
}

// runOpener runs open / xdg-open (tests put a recorder here). A failure
// carries the command's own first line ("Unable to find application …"),
// which says more than its exit status. The output goes to a file, not a
// pipe: xdg-open may hand its stderr on to the app it starts, and a pipe
// closed under that app would end it at its next word (SIGPIPE), or keep
// this waiting until the app is closed.
var runOpener = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, terr := os.CreateTemp("", "pekoe-open-*")
	if terr == nil {
		defer os.Remove(out.Name())
		defer out.Close()
		cmd.Stdout, cmd.Stderr = out, out
	}
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if terr == nil {
		b := make([]byte, 4096)
		n, _ := out.ReadAt(b, 0)
		if l := firstLine(string(b[:n])); l != "" {
			return fmt.Errorf("%s: %s", name, l)
		}
	}
	return fmt.Errorf("%s: %w", name, err)
}

// evLine is text made safe to draw on one line: inboxText, and a line
// break or tab (a file's name may hold them: the name behind a symlink is
// the agent's to choose) becomes a space.
func evLine(s string) string {
	return strings.Join(strings.Fields(inboxText(s)), " ")
}

// evWhat says what ↵ does with it, for the line under the path.
func evWhat(it evItem, graphics bool) string { return evWhatOn(runtime.GOOS, it, graphics) }

// evWhatOn is evWhat on goos (the app named differs).
func evWhatOn(goos string, it evItem, graphics bool) string {
	switch it.kind {
	case evLink:
		return "↵ opens it in the browser"
	case evImage, evVideo, evPDF, evText:
		app := "the default app"
		if goos == "darwin" {
			app = evAppName(evMacApp(it.kind))
		}
		if graphics && it.inline() {
			return "↵ shows it here · o opens it in " + app
		}
		return "↵ opens it in " + app
	case evFile:
		if it.why != "" {
			return "not opened: " + it.why + " · ↵ shows where it is"
		}
		return "not opened (only images, videos, PDFs and text are) · ↵ shows where it is"
	case evFolder:
		return "a folder · ↵ shows where it is"
	}
	return "nothing to open"
}

// evSize is a file size in words: 950 B, 212 kB, 3.4 MB.
func evSize(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1_000_000:
		return fmt.Sprintf("%d kB", (n+500)/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}

// ---- loading ----

type evReport struct {
	id      int64
	status  string // "done", "review pass" …
	summary string
	at      time.Time
}

// evidenceMsg is a session's evidence, read and classified.
type evidenceMsg struct {
	slug, base string
	reports    []evReport
	items      []evItem
	total      int // reports read, with or without evidence
	err        error
}

// loadEvidence reads slug's reports and what their evidence is now.
func (m *model) loadEvidence(slug string) tea.Cmd {
	base := ""
	for _, s := range m.sessions { // not m.find: the dashboard lists sessions the [1] filter hides
		if s.Slug == slug {
			base = s.Path
			break
		}
	}
	cl := m.cl
	return func() tea.Msg {
		out := evidenceMsg{slug: slug, base: base}
		if cl == nil {
			out.err = errors.New("not connected to pekoed")
			return out
		}
		var rs []state.Report
		if err := cl.Call("report.list", map[string]any{"slug": slug, "limit": evReports}, &rs); err != nil {
			out.err = err
			return out
		}
		out.total = len(rs)
		out.reports, out.items = evidenceItems(rs, base)
		return out
	}
}

// evidenceItems lists the evidence of rs (newest first), each string once,
// under the newest report that names it; reports without any are left out.
func evidenceItems(rs []state.Report, base string) ([]evReport, []evItem) {
	var reports []evReport
	var items []evItem
	seen := map[string]bool{}
	for _, r := range rs {
		at := -1
		for _, e := range r.Evidence {
			k := strings.TrimSpace(e)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			if at < 0 {
				st := r.Status
				if r.Verdict != "" {
					st += " " + r.Verdict
				}
				at = len(reports)
				reports = append(reports, evReport{id: r.ID, status: st, summary: r.Summary, at: r.At})
			}
			it := classifyEvidence(e, base)
			it.report = at
			items = append(items, it)
		}
	}
	return reports, items
}

// onEvidence opens the modal, or says there is nothing to list.
func (m *model) onEvidence(msg evidenceMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.setMsg(msg.err.Error(), true)
	case msg.total == 0:
		m.setMsg(msg.slug+" has not reported yet", false)
	case len(msg.items) == 0:
		m.setMsg("no evidence in "+msg.slug+"'s reports", false)
	case m.modal == nil: // something else came up meanwhile: it keeps the screen
		m.typing = false
		m.modal = &evidenceModal{m: m, slug: msg.slug, base: msg.base, reports: msg.reports, items: msg.items}
	}
	return nil
}

// ---- the modal ----

type evidenceModal struct {
	m       *model
	slug    string
	base    string // the session's worktree
	reports []evReport
	items   []evItem
	sel     int
	top     int  // first list line drawn
	viewing bool // the chosen image is drawn in place
	note    string
	noteErr bool
}

// evOpenedMsg: an item was looked at again and (unless show) opened.
type evOpenedMsg struct {
	em   *evidenceModal
	i    int
	it   evItem
	show bool // draw it in place
	said string
	err  error
}

func (em *evidenceModal) mode() (string, string) { return "EVIDENCE", "" }

func (em *evidenceModal) hints() [][2]string {
	if em.viewing {
		return [][2]string{{"j/k", "next / previous image"}, {"o", "open in app"}, {"esc", "back"}}
	}
	return [][2]string{{"j/k", "move"}, {"↵", "open"}, {"o", "open in app"}, {"esc", "close"}}
}

// canShow: images can be drawn in place at all (a terminal with Kitty
// graphics and room for one).
func (em *evidenceModal) canShow() bool {
	cw, ch := em.area()
	return em.m.graphics && cw >= 4 && ch >= 3
}

// area is the cells an image gets while viewing: the modal takes the
// screen, less its frame, the caption and the keys.
func (em *evidenceModal) area() (int, int) {
	m := em.m
	return m.modalWidth(m.width-4) - 6, m.height - 9
}

// shown is the image the terminal should hold now (previewSync transmits
// it and frees it when this stops naming it).
func (em *evidenceModal) shown() (gfxKey, bool) {
	if !em.viewing || !em.canShow() || em.sel >= len(em.items) || !em.items[em.sel].inline() {
		return gfxKey{}, false
	}
	it := em.items[em.sel]
	cw, ch := em.area()
	cols, rows := fitCells(it.imgW, it.imgH, cw, ch, em.m.pv.cellW, em.m.pv.cellH)
	if cols < 1 || rows < 1 {
		return gfxKey{}, false
	}
	return gfxKey{file: it.target, cols: cols, rows: rows}, true
}

func (em *evidenceModal) view() []string {
	if em.viewing {
		return em.viewImage()
	}
	m, th := em.m, em.m.th
	width := m.modalWidth(min(m.width*70/100, 110))
	cw := width - 6
	dim := func(s string) string { return th.FG(theme.Dim, s) }
	title := th.Bold(theme.Primary, "Evidence") + " " + th.FG(theme.Text, clip(inboxText(em.slug), 30)) +
		dim(fmt.Sprintf(" · %d in %d report%s", len(em.items), len(em.reports), plural(len(em.reports))))

	// the list: a head for each report, its evidence under it
	var list []string
	at := make([]int, len(em.items)) // the list line of each item
	rep := -1
	for i, it := range em.items {
		if it.report != rep {
			rep = it.report
			r := em.reports[rep]
			head := fmt.Sprintf("%s %s · %s", ids.Report(r.id), r.status, theme.Ago(time.Since(r.at)))
			if s := firstLine(inboxText(r.summary)); s != "" {
				head += " · " + s
			}
			list = append(list, dim(fit(head, cw)))
		}
		word, role := evKindWords[it.kind], theme.Dim
		detail := ""
		switch {
		case it.kind == evNone: // why is on the line under the path
			word, role = "refused", theme.Err
			if it.why == "missing" {
				word = it.why
			}
		case it.inline():
			detail = fmt.Sprintf("  png %d×%d · %s", it.imgW, it.imgH, evSize(it.size))
		case it.kind != evLink && it.kind != evFolder:
			detail = "  " + evSize(it.size)
		}
		name := it.name()
		if room := cw - 11 - w(detail); it.kind == evNone {
			name = clipLeft(name, room) // a path: its end names the file
		} else {
			name = clip(name, room)
		}
		l := fit("  "+th.FG(role, fit(word, 7))+"  "+th.FG(theme.Text, name)+dim(detail), cw)
		if i == em.sel { // plain under the fill: a color's reset would end it mid-row
			l = th.SelectStyle().Render(fit(ansi.Strip(l), cw))
		}
		at[i] = len(list)
		list = append(list, l)
	}
	// keep the chosen line on screen, with its report's head when it is the
	// first under it
	room := max(m.height-13, 3)
	if len(em.items) > 0 {
		line := at[em.sel]
		lead := line
		if em.sel == 0 || em.items[em.sel-1].report != em.items[em.sel].report {
			lead--
		}
		if lead < em.top {
			em.top = lead
		}
		if line >= em.top+room {
			em.top = line - room + 1
		}
	}
	em.top = clampi(em.top, 0, max(len(list)-room, 0))
	lines := []string{""}
	lines = append(lines, list[em.top:min(em.top+room, len(list))]...)
	if more := len(list) - em.top - room; more > 0 {
		lines = append(lines, dim(fmt.Sprintf("  … %d more below", more)))
	}

	// what ↵ opens, sunk; then what happens
	if em.sel < len(em.items) {
		it := em.items[em.sel]
		target := it.target
		if target == "" {
			target = firstLine(inboxText(it.raw))
		}
		shown := clipLeft(evLine(target), cw) // a path: its end names the file
		if it.kind == evLink {
			shown = fit(evLine(target), cw) // a URL: its start names the host
		}
		lines = append(lines, "", sunk(shown))
		what := dim(evWhat(it, em.canShow()))
		switch {
		case it.kind == evNone:
			what = th.FG(theme.Err, it.why) + dim(" · nothing to open")
		case it.kind == evFile && it.why != "": // its name said it would open
			what = th.FG(theme.Err, "not opened: "+it.why) + dim(" · ↵ shows where it is")
		}
		lines = append(lines, what)
	}
	if em.note != "" {
		r := theme.Dim
		if em.noteErr {
			r = theme.Err
		}
		lines = append(lines, th.FG(r, fit(evLine(em.note), cw)))
	}
	return m.modalFrame(width, title, lines, em.hints(), false)
}

// viewImage draws the chosen image as large as the screen lets it, its
// placeholder cells centered in a fixed area (so the modal keeps its size
// from one image to the next).
func (em *evidenceModal) viewImage() []string {
	m, th := em.m, em.m.th
	width := m.modalWidth(m.width - 4)
	cw, ch := em.area()
	ch = max(ch, 1)
	it := em.items[em.sel]
	title := th.Bold(theme.Primary, "Evidence") + " " + th.FG(theme.Text, clip(inboxText(em.slug), 30)) +
		th.FG(theme.Dim, " · "+clip(it.name(), 40))
	body := make([]string, ch)
	if k, ok := em.shown(); ok {
		pad := strings.Repeat(" ", (cw-k.cols)/2)
		y0 := (ch - k.rows) / 2
		if id, live := m.pv.live[k]; live {
			for r := 0; r < k.rows; r++ {
				body[y0+r] = pad + placeholderRow(id, r, k.cols)
			}
		}
	} else {
		body[ch/2] = th.FG(theme.Dim, "no room to draw it here · o opens it in an app")
	}
	n, of := 0, 0
	for i, x := range em.items {
		if x.inline() {
			of++
			if i <= em.sel {
				n = of
			}
		}
	}
	r := em.reports[it.report]
	caption := fmt.Sprintf("png %d×%d · %s · report %s %s · %d/%d", it.imgW, it.imgH, evSize(it.size), ids.Report(r.id), r.status, n, of)
	last := th.FG(theme.Dim, fit(caption, cw))
	if em.note != "" { // what o did, in the caption's place
		r := theme.Dim
		if em.noteErr {
			r = theme.Err
		}
		last = th.FG(r, fit(evLine(em.note), cw))
	}
	lines := append(append([]string{""}, body...), last)
	return m.modalFrame(width, title, lines, em.hints(), false)
}

func (em *evidenceModal) key(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if em.viewing {
		switch key {
		case "j", "down", "l", "right":
			em.step(1)
		case "k", "up", "h", "left":
			em.step(-1)
		case "o":
			return em.open(em.sel, false)
		case "esc", "q", "enter":
			em.viewing = false
		}
		return nil
	}
	switch key {
	case "j", "down":
		em.sel, em.note = min(em.sel+1, len(em.items)-1), ""
	case "k", "up":
		em.sel, em.note = max(em.sel-1, 0), ""
	case "enter":
		return em.open(em.sel, em.canShow())
	case "o":
		return em.open(em.sel, false)
	case "esc", "q":
		return done(nil)
	}
	return nil
}

// step moves to the next (or previous) image that can be drawn in place.
func (em *evidenceModal) step(d int) {
	for i := em.sel + d; i >= 0 && i < len(em.items); i += d {
		if em.items[i].inline() {
			em.sel, em.note = i, ""
			return
		}
	}
}

// open looks at item i again (the file may be gone by now) and opens it:
// in place when show and it can be drawn there, else outside the TUI.
func (em *evidenceModal) open(i int, show bool) tea.Cmd {
	if i < 0 || i >= len(em.items) {
		return nil
	}
	old, base := em.items[i], em.base
	return func() tea.Msg {
		it := classifyEvidence(old.raw, base)
		it.report = old.report
		out := evOpenedMsg{em: em, i: i, it: it}
		if show && it.inline() {
			out.show = true
			return out
		}
		name, args, said, err := evidenceOpener(runtime.GOOS, it)
		if err == nil {
			err = runOpener(name, args...)
		}
		out.said, out.err = said, err
		return out
	}
}

func (em *evidenceModal) other(msg tea.Msg) tea.Cmd {
	o, ok := msg.(evOpenedMsg)
	if !ok || o.em != em || o.i >= len(em.items) {
		return nil
	}
	em.items[o.i] = o.it
	switch {
	case o.show:
		em.sel, em.viewing, em.note = o.i, true, ""
	case o.err != nil:
		em.note, em.noteErr = o.err.Error(), true
		if em.viewing && !o.it.inline() { // the image is gone: back to the list
			em.viewing = false
		}
	default:
		em.note, em.noteErr = o.said, false
	}
	return nil
}
