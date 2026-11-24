package voice

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What Shape leaves out (6.2). DefaultSkip is [voice.read] skip when unset.
const (
	SkipCode  = "code"
	SkipTable = "table"
	SkipURL   = "url"
	SkipPath  = "path"
)

// DefaultSkip is the skip list when [voice.read] skip is unset.
var DefaultSkip = []string{SkipCode, SkipTable, SkipURL, SkipPath}

// Sentence lengths (6.2): a sentence longer than MaxSentenceRunes is cut
// after 、 or , (and hard at MaxSentenceRunes when it has neither); inline
// code longer than InlineCodeRunes is not read.
const (
	MaxSentenceRunes = 200
	InlineCodeRunes  = 24
)

// ShapeState is where ShapeLines stopped: inside a code fence or not, and
// the sentence begun on earlier lines that no terminator has ended yet.
// The zero value is the start of a message.
type ShapeState struct {
	// Fence is the opening fence's character (` or ~) and FenceLen its
	// length while inside a fenced code block; Fence 0 is outside.
	Fence    rune
	FenceLen int
	// Pending is the unfinished sentence (an en sentence runs on to the
	// next line).
	Pending string
}

// Shape turns a whole Markdown text into the sentences to read (6.2).
// skip lists what is not read as it is (SkipCode…); nil reads everything,
// and callers pass DefaultSkip for the default. lang (ja or en) picks the
// words a URL or long inline code is read as.
func Shape(text string, skip []string, lang string) []string {
	st, out := ShapeLines(ShapeState{}, strings.Split(text, "\n"), skip, lang)
	_, rest := st.Flush()
	return append(out, rest...)
}

// ShapeLines shapes whole lines (no newline in them) from st on and returns
// the state after them and the sentences they finished. Feeding the lines
// of a text in any split gives the same sentences as one call: the read
// buffer (Reader) feeds only complete lines and Flushes at the end of the
// message.
func ShapeLines(st ShapeState, lines []string, skip []string, lang string) (ShapeState, []string) {
	sk := skipSet(skip, lang)
	var out []string
	for _, l := range lines {
		st, out = st.line(strings.TrimSuffix(l, "\r"), sk, out)
	}
	return st, out
}

// Flush ends the message: the pending sentence is finished.
func (st ShapeState) Flush() (ShapeState, []string) {
	var out []string
	st, out = st.flush(out)
	return ShapeState{}, out
}

// skips is what is not read as it is, and the words read in place of a
// URL (link) and of long inline code (code).
type skips struct {
	code, table, url, path bool
	link, codeWord         string
}

// The words a URL and long inline code are read as (6.2).
const (
	linkJa = "リンク"
	linkEn = "a link"
	codeJa = "コード"
	codeEn = "code"
)

func skipSet(skip []string, lang string) skips {
	s := skips{link: linkEn, codeWord: codeEn}
	if lang == LangJa {
		s.link, s.codeWord = linkJa, codeJa
	}
	for _, k := range skip {
		switch k {
		case SkipCode:
			s.code = true
		case SkipTable:
			s.table = true
		case SkipURL:
			s.url = true
		case SkipPath:
			s.path = true
		}
	}
	return s
}

func (st ShapeState) flush(out []string) (ShapeState, []string) {
	if st.Pending != "" {
		out = appendSentence(out, st.Pending)
		st.Pending = ""
	}
	return st, out
}

var (
	reHeading  = regexp.MustCompile(`^#{1,6}(\s+|$)`)
	reBullet   = regexp.MustCompile(`^([-*+]|\d{1,9}[.)])(\s+|$)`)
	reRule     = regexp.MustCompile(`^(\*\s*){3,}$|^(-\s*){3,}$|^(_\s*){3,}$`)
	reTableSep = regexp.MustCompile(`^\|?[\s:|-]+\|?$`)
)

// fence reports a fence line: its character and length, and the rest
// after the run (the info string).
func fence(l string) (rune, int, string) {
	t := strings.TrimLeft(l, " ")
	if len(l)-len(t) > 3 || t == "" {
		return 0, 0, ""
	}
	c := rune(t[0])
	if c != '`' && c != '~' {
		return 0, 0, ""
	}
	n := 0
	for n < len(t) && rune(t[n]) == c {
		n++
	}
	if n < 3 {
		return 0, 0, ""
	}
	return c, n, t[n:]
}

func (st ShapeState) line(l string, sk skips, out []string) (ShapeState, []string) {
	// Inside a fence: nothing but the closing fence counts.
	if st.Fence != 0 {
		if c, n, rest := fence(l); c == st.Fence && n >= st.FenceLen && strings.TrimSpace(rest) == "" {
			st.Fence, st.FenceLen = 0, 0
			return st, out
		}
		if sk.code {
			return st, out
		}
		st, out = st.flush(out)
		out = sentences(inline(l, sk), true, out, &st)
		return st, out
	}
	if c, n, rest := fence(l); c != 0 && !(c == '`' && strings.ContainsRune(rest, '`')) {
		st, out = st.flush(out)
		st.Fence, st.FenceLen = c, n
		return st, out
	}
	if strings.TrimSpace(l) == "" {
		return st.flush(out)
	}
	// Indented code: four spaces (or a tab) that are not a nested list item.
	if strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t") {
		if t := strings.TrimSpace(l); !reBullet.MatchString(t) {
			st, out = st.flush(out)
			if sk.code {
				return st, out
			}
			out = sentences(inline(t, sk), true, out, &st)
			return st, out
		}
	}
	t := strings.TrimSpace(l)
	// Block quotes: the markers go, the text is read.
	for strings.HasPrefix(t, ">") {
		t = strings.TrimSpace(t[1:])
	}
	if t == "" {
		return st.flush(out)
	}
	if strings.HasPrefix(t, "|") {
		st, out = st.flush(out)
		if sk.table || reTableSep.MatchString(t) {
			return st, out
		}
		cells := strings.Split(strings.Trim(t, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		out = sentences(inline(strings.Join(cells, "、"), sk), true, out, &st)
		return st, out
	}
	if reRule.MatchString(t) {
		return st.flush(out)
	}
	whole := false // the line is a sentence of its own
	if m := reHeading.FindString(t); m != "" {
		t, whole = strings.TrimRight(strings.TrimSpace(t[len(m):]), "# "), true
	} else if m := reBullet.FindString(t); m != "" {
		t, whole = t[len(m):], true
		if strings.HasPrefix(t, "[ ] ") || strings.HasPrefix(t, "[x] ") || strings.HasPrefix(t, "[X] ") {
			t = t[4:]
		}
	}
	if whole {
		st, out = st.flush(out)
	}
	text := inline(t, sk)
	out = sentences(text, whole, out, &st)
	return st, out
}

// sentences splits text (one line, already cleaned) into sentences after
// st.Pending. endLine: the end of the line ends the sentence too (a
// heading, a list item, a ja line); otherwise an en sentence runs on.
func sentences(text string, endLine bool, out []string, st *ShapeState) []string {
	cur := st.Pending
	if cur != "" && text != "" {
		cur += " "
	}
	rs := []rune(text)
	for i, r := range rs {
		cur += string(r)
		end := false
		switch r {
		case '。', '！', '？':
			end = true
		case '.', '!', '?':
			// en: the mark ends a sentence before a space or the end of the
			// line; ! and ? also before ja text.
			if i+1 == len(rs) || unicode.IsSpace(rs[i+1]) {
				end = true
			} else if r != '.' && rs[i+1] > unicode.MaxASCII {
				end = true
			}
		}
		if end {
			out = appendSentence(out, cur)
			cur = ""
		}
	}
	if endLine || hasJa(cur) {
		out = appendSentence(out, cur)
		cur = ""
	}
	st.Pending = strings.TrimSpace(cur)
	return out
}

// hasJa: s has kana or kanji (a ja line ends its sentence at the newline).
func hasJa(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han) {
			return true
		}
	}
	return false
}

// appendSentence adds s cut to MaxSentenceRunes, leaving out the empty and
// the punctuation-only.
func appendSentence(out []string, s string) []string {
	s = strings.Join(strings.Fields(s), " ")
	if !hasWord(s) {
		return out
	}
	for utf8.RuneCountInString(s) > MaxSentenceRunes {
		rs := []rune(s)
		cut := -1
		for i := MaxSentenceRunes - 1; i > 0; i-- {
			if rs[i] == '、' || rs[i] == ',' {
				cut = i + 1
				break
			}
		}
		if cut < 0 {
			cut = MaxSentenceRunes
		}
		if head := strings.TrimSpace(string(rs[:cut])); hasWord(head) {
			out = append(out, head)
		}
		s = strings.TrimSpace(string(rs[cut:]))
	}
	if hasWord(s) {
		out = append(out, s)
	}
	return out
}

func hasWord(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

var (
	reInlineCode = regexp.MustCompile("`+")
	reImage      = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLink       = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)
	reAutoLink   = regexp.MustCompile(`<(https?://[^>]*)>`)
	reTag        = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9-]*(\s[^<>]*)?/?>`)
	reURL        = regexp.MustCompile(`https?://[!-~]*`)
	// a run of characters a path is made of
	rePathish = regexp.MustCompile(`[A-Za-z0-9_.~/@+\-]+`)
	reExt     = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)
	// inline code that is one word: letters, digits, - and _
	reCodeWord = regexp.MustCompile(`^[A-Za-z0-9_-]*[A-Za-z0-9][A-Za-z0-9_-]*$`)
	reMarks    = regexp.MustCompile(`\*\*|__|~~|\*`)
	// a space left before punctuation where a word was taken out
	reSpacePunct = regexp.MustCompile(`\s+([,.;:!?)]+(?:\s|$)|[、。！？」])`)
)

// inline cleans one line's inline Markdown (6.2): code spans, images,
// links, tags, URLs, paths, emphasis and emoji. A URL is read as a link
// word and a path as its last element.
func inline(s string, sk skips) string {
	s = codeSpans(s, sk)
	s = reImage.ReplaceAllString(s, " ")
	s = reAutoLink.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
	s = reTag.ReplaceAllString(s, " ")
	if sk.url {
		s = reURL.ReplaceAllStringFunc(s, func(u string) string {
			// trailing punctuation belongs to the sentence
			return " " + sk.link + " " + u[len(strings.TrimRight(u, ".,;:!?)")):]
		})
	}
	if sk.path {
		s = outsideURLs(s, func(part string) string {
			return rePathish.ReplaceAllStringFunc(part, func(w string) string {
				// a trailing . is the sentence's end, not the path's
				p := strings.TrimRight(w, ".")
				if isPath(p) {
					return " " + pathName(p) + " " + w[len(p):]
				}
				return w
			})
		})
	}
	s = reMarks.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if isEmoji(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return reSpacePunct.ReplaceAllString(s, "$1")
}

// outsideURLs applies f to the parts of s that are not URLs (a URL read
// as it is keeps its path).
func outsideURLs(s string, f func(string) string) string {
	var b strings.Builder
	at := 0
	for _, loc := range reURL.FindAllStringIndex(s, -1) {
		b.WriteString(f(s[at:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		at = loc[1]
	}
	b.WriteString(f(s[at:]))
	return b.String()
}

// codeSpans replaces each `code span` with what is read for it (6.2): its
// content when code is not skipped or it is short and without /; else a
// path's last element (path skipped; as it is when not), the link word for
// a URL (url skipped; as it is when not) and the code word for the rest.
// An unclosed run of backticks is left as it is.
func codeSpans(s string, sk skips) string {
	var b strings.Builder
	for {
		loc := reInlineCode.FindStringIndex(s)
		if loc == nil {
			b.WriteString(s)
			return b.String()
		}
		tick := s[loc[0]:loc[1]]
		rest := s[loc[1]:]
		end := findRun(rest, tick)
		if end < 0 {
			b.WriteString(s[:loc[1]])
			s = rest
			continue
		}
		b.WriteString(s[:loc[0]])
		code := strings.TrimSpace(rest[:end])
		if r := codeSpan(code, sk); r == code {
			b.WriteString(code)
		} else {
			// a word in place of the code stands apart from the text
			b.WriteString(" " + r + " ")
		}
		s = rest[end+len(tick):]
	}
}

func codeSpan(code string, sk skips) string {
	if !sk.code {
		return code
	}
	if reCodeWord.MatchString(code) {
		// one word (a slug, a branch, a name) is read at any length, its
		// - and _ as spaces: fix-idle-all → fix idle all
		return strings.Join(strings.FieldsFunc(code, func(r rune) bool { return r == '-' || r == '_' }), " ")
	}
	if utf8.RuneCountInString(code) <= InlineCodeRunes && !strings.Contains(code, "/") {
		return code
	}
	if isCodePath(code) {
		if !sk.path {
			return code
		}
		return pathName(code)
	}
	if isPath(code) {
		if !sk.path {
			return code
		}
		// a path with nothing to read (../), as in the text
		return ""
	}
	if u := reURL.FindString(code); u != "" && u == code {
		if !sk.url {
			return code
		}
		return sk.link
	}
	return sk.codeWord
}

// isCodePath: inline code that is a path: a path in text (isPath), or a
// word of path characters with a / in it (internal/voice) whose last
// element has a letter or digit.
func isCodePath(code string) bool {
	if pathName(code) == "" {
		return false
	}
	return isPath(code) || (strings.Contains(code, "/") && rePathish.FindString(code) == code)
}

// pathName is what a path is read as (6.2): its last element (the one
// before a trailing /) without the extension or a leading dot;
// internal/voice/narrate.go → narrate, ~/.config/ → config. "" when that
// has no letter or digit (../).
func pathName(p string) string {
	p = strings.TrimRight(p, "/")
	name := p[strings.LastIndex(p, "/")+1:]
	if n := reExt.ReplaceAllString(name, ""); hasExt(name) && strings.TrimLeft(n, ".") != "" {
		name = n
	}
	name = strings.TrimLeft(name, ".")
	if !hasWord(name) {
		return ""
	}
	return name
}

// findRun is the index of a run of backticks exactly as long as tick.
func findRun(s, tick string) int {
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], tick)
		if j < 0 {
			return -1
		}
		at := i + j
		end := at + len(tick)
		if end < len(s) && s[end] == '`' {
			for end < len(s) && s[end] == '`' {
				end++
			}
			i = end
			continue
		}
		return at
	}
	return -1
}

// isPath: a word is a file path (6.2): it starts with ./ ../ ~/ or /, or
// it has a / and ends with an extension or a / (a directory).
func isPath(w string) bool {
	w = strings.TrimRight(w, ".")
	for _, p := range []string{"./", "../", "~/", "/"} {
		if strings.HasPrefix(w, p) && len(w) > 1 {
			return true
		}
	}
	return strings.Contains(w, "/") && (hasExt(w) || (strings.HasSuffix(w, "/") && len(strings.Trim(w, "/")) > 0))
}

// hasExt: w ends with an extension that has a letter (1/2.5 and
// v1.2/v1.3 are numbers, not paths).
func hasExt(w string) bool {
	return strings.ContainsFunc(reExt.FindString(w), unicode.IsLetter)
}

// isEmoji: pictographs, dingbats and the joiners and selectors that go
// with them.
func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF,
		r >= 0x2600 && r <= 0x27BF,
		r >= 0x2B00 && r <= 0x2BFF,
		r >= 0xFE00 && r <= 0xFE0F,
		r == 0x200D, r == 0x20E3,
		r >= 0xE0020 && r <= 0xE007F:
		return true
	}
	return false
}
