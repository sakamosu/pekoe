package daemon

// A Claude Code transcript read as a chat (docs/web.md 「tapi chat の扱い」):
// what a person wrote, what the agent answered, the tools it called (name
// and one main argument) and how long each turn took. Nothing else leaves
// pekoed: no tool results (the "user" lines that carry them included), no
// thinking, no system injections (reminders, compact summaries, command
// output, task notifications). Lines pekoed itself typed into the input box
// ("[pekoe inbox q3] …") are kept apart from the person's words.
//
// It takes a path, not a session: tapi chat uses it now (tapi.transcript),
// a session's transcript can use it later. The path comes from a hook
// payload, which any process of the user can send, so the caller checks it
// (tapiTranscriptOK) and only a regular file is read (O_NONBLOCK, as
// tailFile), in bounded chunks.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	// chatTail: on a first read (or another conversation), only the end of
	// the transcript is read; earlier lines are left out (Earlier).
	chatTail = 6 << 20
	// chatChunk: the most read after a cursor in one call; the rest comes
	// with the next.
	chatChunk = 16 << 20
	// chatMaxLine: longer lines (request dumps, huge pastes) are skipped.
	chatMaxLine = 2 << 20
	// chatMaxItems: the most items one first read returns (the newest).
	chatMaxItems = 400
	// chatMaxText: a message longer than this is cut (the screen has it).
	chatMaxText = 32 << 10
	// chatMaxPekoe: a line pekoed typed is shown on one line.
	chatMaxPekoe = 500
	// chatMaxQuestion: an AskUserQuestion's questions and options.
	chatMaxQuestion = 2000
)

// ChatItem is one thing in the conversation.
//   - human: the person's words (text)
//   - pekoe: a line pekoed typed into the input box (text, one line)
//   - agent: the agent's words (text, markdown as the agent wrote it)
//   - tool: a tool call (name, input: its main argument, 200 bytes at most;
//     question: an AskUserQuestion's questions and options)
//   - turn: a turn ended (ms: how long it took)
//
// A human or pekoe line typed at the prompt also has its id (the line's
// uuid) and parent (the uuid it follows). A prompt the person interrupted
// (ctrl+c, esc) before the first word of the reply leaves the
// conversation, and Claude Code writes the prompt sent after it as another
// child of the same parent: the earlier one was taken back and never
// reached the agent (docs/web.md 「取り消された発言」). The page leaves it
// out (markRewound in app.js); the later prompt can come in a later read,
// so the reader cannot. A line the agent took in the middle of a turn
// (typed while it worked, Claude Code's queue) has its id only: it was
// delivered, and is never taken back.
type ChatItem struct {
	Kind     string    `json:"kind"`
	ID       string    `json:"id,omitempty"`
	Parent   string    `json:"parent,omitempty"`
	Text     string    `json:"text,omitempty"`
	Name     string    `json:"name,omitempty"`
	Input    string    `json:"input,omitempty"`
	Question string    `json:"question,omitempty"`
	MS       int64     `json:"ms,omitempty"`
	At       time.Time `json:"at"`
}

// ChatPage is what one read returns. Cursor goes back as after for the
// next read. Reset: Items start over (the first read, another
// conversation, or a transcript that got shorter); the page drops what it
// had. NewConversation: after named another conversation (/clear, a reset).
// Earlier: older items were left out.
type ChatPage struct {
	Conversation    string     `json:"conversation"`
	Cursor          string     `json:"cursor"`
	Items           []ChatItem `json:"items"`
	Reset           bool       `json:"reset,omitempty"`
	NewConversation bool       `json:"new_conversation,omitempty"`
	Earlier         bool       `json:"earlier,omitempty"`
}

var errNoTranscript = errors.New("no transcript to read")

// chatCursor is "<conversation>:<offset>": the transcript's file name
// (Claude Code's session id) and the byte after the last whole line read.
func chatCursor(conv string, off int64) string { return conv + ":" + strconv.FormatInt(off, 10) }

func parseChatCursor(s string) (conv string, off int64, ok bool) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(s[i+1:], 10, 64)
	if err != nil || n < 0 || !validSessionID(s[:i]) {
		return "", 0, false
	}
	return s[:i], n, true
}

// readChat reads the conversation in path after the cursor after ("" for
// the first read). A path that does not exist yet (a conversation just
// cleared, nothing said in it) is an empty conversation.
func readChat(path, after string) (ChatPage, error) {
	conv := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if path == "" || !strings.HasSuffix(path, ".jsonl") || !validSessionID(conv) {
		return ChatPage{}, errNoTranscript
	}
	pc, off, ok := parseChatCursor(after)
	page := ChatPage{Conversation: conv, Items: []ChatItem{}}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		page.Cursor = chatCursor(conv, 0)
		page.Reset = !ok || pc != conv || off > 0
		page.NewConversation = ok && pc != conv
		return page, nil
	}
	if err != nil {
		return ChatPage{}, fmt.Errorf("%w: %v", errNoTranscript, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return ChatPage{}, errNoTranscript
	}
	size := fi.Size()
	start := off
	if !ok || pc != conv || off > size {
		page.Reset = true
		page.NewConversation = ok && pc != conv
		start = max(size-chatTail, 0)
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ChatPage{}, errNoTranscript
	}
	r := bufio.NewReaderSize(io.LimitReader(f, size-start), 64<<10)
	pos := start
	if page.Reset && start > 0 {
		// started mid-line: skip to the next whole one
		n, _, err := readLine(r)
		pos += n
		if err != nil {
			page.Cursor = chatCursor(conv, start)
			page.Earlier = true
			return page, nil
		}
		page.Earlier = true
	}
	for pos-start < chatChunk {
		n, line, err := readLine(r)
		if err != nil {
			break // EOF, or a line still being written: read it next time
		}
		pos += n
		if line != nil {
			page.Items = append(page.Items, chatItems(line)...)
		}
	}
	if page.Reset && len(page.Items) > chatMaxItems {
		page.Items = page.Items[len(page.Items)-chatMaxItems:]
		page.Earlier = true
	}
	page.Cursor = chatCursor(conv, pos)
	return page, nil
}

// readLine reads one whole line: its length with the newline, and the line
// (nil when longer than chatMaxLine, which is skipped). A line without its
// newline yet is an error (io.EOF) and nothing of it counts as read.
func readLine(r *bufio.Reader) (int64, []byte, error) {
	var buf []byte
	var n int64
	long := false
	for {
		frag, err := r.ReadSlice('\n')
		n += int64(len(frag))
		if !long {
			if len(buf)+len(frag) > chatMaxLine {
				long, buf = true, nil
			} else {
				buf = append(buf, frag...)
			}
		}
		switch err {
		case nil:
			if long {
				return n, nil, nil
			}
			return n, buf, nil
		case bufio.ErrBufferFull:
			continue
		default:
			return 0, nil, err
		}
	}
}

// chatLine is the part of a transcript line chatItems looks at.
type chatLine struct {
	Type              string      `json:"type"`
	Subtype           string      `json:"subtype"`
	Timestamp         time.Time   `json:"timestamp"`
	IsMeta            bool        `json:"isMeta"`
	IsSidechain       bool        `json:"isSidechain"`
	IsCompactSummary  bool        `json:"isCompactSummary"`
	IsVisibleOnlyInTr bool        `json:"isVisibleInTranscriptOnly"`
	DurationMs        int64       `json:"durationMs"`
	UUID              string      `json:"uuid"`
	ParentUUID        string      `json:"parentUuid"`
	Origin            *chatOrigin `json:"origin"`
	Attachment        *struct {
		Type        string          `json:"type"`
		CommandMode string          `json:"commandMode"`
		Prompt      json.RawMessage `json:"prompt"`
		Origin      *chatOrigin     `json:"origin"`
	} `json:"attachment"`
	Message struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type chatOrigin struct {
	Kind string `json:"kind"`
}

type chatPart struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// chatItems turns one transcript line into what the chat shows (often
// nothing).
func chatItems(line []byte) []ChatItem {
	var e chatLine
	if json.Unmarshal(line, &e) != nil || e.IsSidechain {
		return nil
	}
	switch e.Type {
	case "system":
		if e.Subtype == "turn_duration" && e.DurationMs > 0 {
			return []ChatItem{{Kind: "turn", MS: e.DurationMs, At: e.Timestamp}}
		}
	case "user":
		if e.IsMeta || e.IsCompactSummary || e.IsVisibleOnlyInTr {
			return nil
		}
		if e.Origin != nil && e.Origin.Kind != "human" {
			return nil // task notifications and the like: Claude Code's, not the person's
		}
		return typedItem(e.Message.Content, e.UUID, e.ParentUUID, e.Timestamp)
	case "attachment":
		// what the person typed while the agent worked, taken in the
		// middle of its turn (Claude Code's queue, "absorbed_mid_turn"):
		// no user line of its own. One that waited for the turn to end
		// comes as a user line instead.
		a := e.Attachment
		if a == nil || a.Type != "queued_command" || a.CommandMode != "prompt" || a.Origin == nil || a.Origin.Kind != "human" {
			return nil
		}
		return typedItem(a.Prompt, e.UUID, "", e.Timestamp)
	case "assistant":
		if e.Message.Model == "<synthetic>" {
			return nil // "No response requested." and the like
		}
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return []ChatItem{{Kind: "agent", Text: cutBytes(s, chatMaxText), At: e.Timestamp}}
			}
			return nil
		}
		var parts []chatPart
		if json.Unmarshal(e.Message.Content, &parts) != nil {
			return nil
		}
		var out []ChatItem
		for _, p := range parts {
			switch p.Type {
			case "text":
				if t := strings.TrimSpace(p.Text); t != "" {
					out = append(out, ChatItem{Kind: "agent", Text: cutBytes(t, chatMaxText), At: e.Timestamp})
				}
			case "tool_use":
				t := summarizeTool(p.Name, p.Input)
				it := ChatItem{Kind: "tool", Name: cutBytes(p.Name, 100), Input: t.Input, At: e.Timestamp}
				if p.Name == "AskUserQuestion" {
					it.Input = ""
					it.Question = askQuestionText(p.Input)
				}
				out = append(out, it)
			}
			// thinking, redacted_thinking and anything else: not shown
		}
		return out
	}
	return nil
}

// typedItem is a line typed at the prompt: the person's words, or a line
// pekoed typed (nothing for a tool result or what Claude Code injects).
func typedItem(content json.RawMessage, id, parent string, at time.Time) []ChatItem {
	text, ok := userText(content)
	if !ok || text == "" || injected(text) {
		return nil
	}
	text = unwrapPasted(text)
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "[pekoe") {
		return []ChatItem{{Kind: "pekoe", ID: id, Parent: parent, Text: cutBytes(oneLine(text), chatMaxPekoe), At: at}}
	}
	return []ChatItem{{Kind: "human", ID: id, Parent: parent, Text: cutBytes(text, chatMaxText), At: at}}
}

// userText is the person's words in a user line. ok is false for a line
// that carries a tool result: those are the tools' output, not the
// person's, whatever else they hold.
func userText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), true
	}
	var parts []chatPart
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "tool_result":
			return "", false
		case "text":
			t := strings.TrimSpace(p.Text)
			if t == "" || strings.HasPrefix(t, "<system-reminder>") {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(t)
		}
	}
	return b.String(), true
}

// unwrapPasted takes off the wrapping Claude Code records a paste in. A
// send of more than one line (an image's line makes every send with images
// one) reaches Claude Code as a paste from tmux, and the transcript has it
// as <pasted_content id="…">\n…\n</pasted_content id="…">: the bubble
// showed the tags, and did not match the words the page had sent. Only a
// text that is one such paste, from its first byte to its last, is
// unwrapped (the words inside as they were, but for the blank around
// them); a paste among typed words, a part of one, or a body that holds its
// own closing tag again stays as it is.
func unwrapPasted(text string) string {
	const open, closeTag = `<pasted_content id="`, `</pasted_content id="`
	if !strings.HasPrefix(text, open) {
		return text
	}
	rest := text[len(open):]
	q := strings.Index(rest, `">`)
	if q <= 0 {
		return text
	}
	id := rest[:q]
	if strings.ContainsAny(id, "\"<>\n") {
		return text
	}
	end := closeTag + id + `">`
	body := rest[q+len(`">`):]
	if !strings.HasSuffix(body, end) {
		return text
	}
	body = body[:len(body)-len(end)]
	if strings.Contains(body, end) {
		return text
	}
	return strings.TrimSpace(body)
}

// injectedPrefixes start the user lines Claude Code writes itself: slash
// command records and their output, ! commands, hook and system text.
var injectedPrefixes = []string{
	"<command-", "<local-command-", "<bash-", "<task-notification>",
	"<system-reminder>", "<user-prompt-submit-hook>", "[Request interrupted",
	"Caveat: The messages below",
}

func injected(text string) bool {
	for _, p := range injectedPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// askQuestionText: an AskUserQuestion's questions, each with its options
// numbered as the menu shows them.
func askQuestionText(input json.RawMessage) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var b strings.Builder
	for i, q := range in.Questions {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(q.Question))
		for j, o := range q.Options {
			fmt.Fprintf(&b, "\n%d. %s", j+1, oneLine(o.Label))
		}
	}
	return cutBytes(b.String(), chatMaxQuestion)
}

// cutBytes cuts s to at most n bytes on a rune boundary, with "…".
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
