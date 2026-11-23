package voice

import (
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Read's limits and waits (6.2).
const (
	// DefaultMaxTurnSentences is [voice.read] max_turn_sentences when unset.
	DefaultMaxTurnSentences = 10
	// MaxTurnRunes is how much of one turn is read at most (fixed).
	MaxTurnRunes = 800
	// GapWait is how long a missing index is waited for; after it the rest
	// of the message is not read (the hook's timeout).
	GapWait = 2 * time.Second
	// rememberTurns is how many turn ids a session remembers per list.
	rememberTurns = 8
)

// Display is one MessageDisplay hook input (3.2): a batch of a message's
// raw Markdown.
type Display struct {
	Turn    string // turn_id
	Message string // message_id
	Index   int    // index, from 0
	Final   bool   // final
	Delta   string // delta
}

// ReadConfig is what a Reader runs on. Clock is required.
type ReadConfig struct {
	Clock Clock
	// Skip is what Shape leaves out (DefaultSkip for the default).
	Skip []string
	// MaxSentences is [voice.read] max_sentences (0 is
	// DefaultMaxSentences); MaxTurnSentences is max_turn_sentences (0 is
	// DefaultMaxTurnSentences).
	MaxSentences     int
	MaxTurnSentences int
	// Mode is the session's mode now (Policy.ModeFor); nil is read. Text
	// that comes while the mode is not read is dropped and not read later
	// (6.1). Called with the Reader's lock held; it must not call the
	// Reader.
	Mode func(slug string) Mode
}

// Reader turns the assistant text of the read hooks (MessageDisplay,
// Stop) into the read items to push, and stops reading where 6.3 says.
// Each method returns the items to push onto the Queue, in order; the
// caller pushes them with no lock of the Reader's held.
type Reader struct {
	cfg ReadConfig

	mu       sync.Mutex
	sessions map[string]*readSession
	seq      int
}

// readSession is one session's read: the turn being read and its messages,
// and the turns not to read.
type readSession struct {
	turn string // the last turn_id taken
	msgs map[string]*readMsg

	// the turn's limits (6.2)
	sent  int
	runes int
	full  bool

	// displayed: a MessageDisplay of the running turn came since the last
	// Stop, so Stop's text is not read (6.1).
	displayed bool
	// stopping: a UserPromptSubmit came while running; everything up to the
	// next Stop is dropped (6.3).
	stopping bool

	stopped []string // turns not to read (6.3)
	ended   []string // turns a Stop ended (their late batches still read)
	past    []string // turns left for a newer one (their late batches go)
}

// readMsg is one message's buffer (6.2).
type readMsg struct {
	next     int             // the next index to shape
	held     map[int]Display // batches past a missing index
	gapSince time.Time       // when the missing index was first waited for
	partial  string          // the text after the last newline
	shape    ShapeState
	sent     int  // sentences taken from this message
	done     bool // final shaped, or given up
}

// NewReader returns a Reader.
func NewReader(cfg ReadConfig) *Reader {
	if cfg.MaxSentences <= 0 {
		cfg.MaxSentences = DefaultMaxSentences
	}
	if cfg.MaxTurnSentences <= 0 {
		cfg.MaxTurnSentences = DefaultMaxTurnSentences
	}
	return &Reader{cfg: cfg, sessions: map[string]*readSession{}}
}

func (r *Reader) session(slug string) *readSession {
	s := r.sessions[slug]
	if s == nil {
		s = &readSession{msgs: map[string]*readMsg{}}
		r.sessions[slug] = s
	}
	return s
}

func (r *Reader) reads(slug string) bool {
	return r.cfg.Mode == nil || r.cfg.Mode(slug) == ModeRead
}

func remember(list []string, turn string) []string {
	if turn == "" || has(list, turn) {
		return list
	}
	list = append(list, turn)
	if len(list) > rememberTurns {
		list = list[len(list)-rememberTurns:]
	}
	return list
}

func has(list []string, turn string) bool {
	for _, t := range list {
		if t == turn {
			return true
		}
	}
	return false
}

// newTurn leaves the turn being read for turn: what is left of the old one
// is dropped (6.3) and the limits start over.
func (s *readSession) newTurn(turn string) {
	s.past = remember(s.past, s.turn)
	s.turn = turn
	s.msgs = map[string]*readMsg{}
	s.sent, s.runes, s.full = 0, 0, false
}

// drop gives up the messages being read: their later batches are not
// read (their fence state is lost).
func (s *readSession) drop() {
	for _, m := range s.msgs {
		m.done, m.held, m.partial = true, nil, ""
	}
}

// Display takes one MessageDisplay batch of the session slug and returns
// the sentences it finished.
func (r *Reader) Display(slug string, d Display) []Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.session(slug)
	if !has(s.ended, d.Turn) {
		s.displayed = true
	}
	if s.stopping {
		s.stopped = remember(s.stopped, d.Turn)
		return nil
	}
	if has(s.stopped, d.Turn) || has(s.past, d.Turn) {
		return nil
	}
	if d.Turn != s.turn {
		s.newTurn(d.Turn)
	}
	m := s.msgs[d.Message]
	if m == nil {
		m = &readMsg{}
		s.msgs[d.Message] = m
	}
	if !r.reads(slug) {
		m.done, m.held, m.partial = true, nil, ""
		return nil
	}
	if m.done || d.Index < m.next {
		return nil
	}
	now := r.cfg.Clock.Now()
	if !m.gapSince.IsZero() && now.Sub(m.gapSince) >= GapWait {
		m.done, m.held, m.partial = true, nil, ""
		return nil
	}
	if m.held == nil {
		m.held = map[int]Display{}
	}
	m.held[d.Index] = d
	var lines []string
	final := false
	for {
		b, ok := m.held[m.next]
		if !ok {
			break
		}
		delete(m.held, m.next)
		m.next++
		m.partial += b.Delta
		if b.Final {
			final = true
			break
		}
	}
	if final {
		lines = strings.Split(m.partial, "\n")
		m.partial = ""
	} else if i := strings.LastIndexByte(m.partial, '\n'); i >= 0 {
		lines = strings.Split(m.partial[:i], "\n")
		m.partial = m.partial[i+1:]
	}
	var sents []string
	m.shape, sents = ShapeLines(m.shape, lines, r.cfg.Skip)
	if final {
		var rest []string
		m.shape, rest = m.shape.Flush()
		sents = append(sents, rest...)
		m.done, m.held = true, nil
	}
	if len(m.held) == 0 {
		m.gapSince = time.Time{}
	} else if m.gapSince.IsZero() {
		m.gapSince = now
	}
	return r.take(slug, s, m, d.Turn, d.Message, sents)
}

// take turns sentences of message m into items within the limits (6.2):
// max_sentences per message, max_turn_sentences and MaxTurnRunes per turn.
func (r *Reader) take(slug string, s *readSession, m *readMsg, turn, msg string, sents []string) []Item {
	var out []Item
	for _, t := range sents {
		if s.full {
			break
		}
		if m.sent >= r.cfg.MaxSentences {
			break
		}
		n := utf8.RuneCountInString(t)
		if s.sent >= r.cfg.MaxTurnSentences || s.runes+n > MaxTurnRunes {
			s.full = true
			break
		}
		m.sent++
		s.sent++
		s.runes += n
		out = append(out, Item{Kind: KindRead, Slug: slug, Turn: turn, Message: msg, Text: t})
	}
	return out
}

// Stop takes the session's Stop hook: text is last_assistant_message and
// turn the turn it ends, if the caller knows it ("" when not). Its text is
// read (6.1, the Stop way) unless a MessageDisplay of the turn came, or the
// turn was stopped (6.3: the Stop that ends stopping is not read either).
func (r *Reader) Stop(slug, turn, text string) []Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.session(slug)
	s.ended = remember(s.ended, s.turn)
	s.ended = remember(s.ended, turn)
	displayed := s.displayed
	s.displayed = false
	if s.stopping {
		s.stopping = false
		s.stopped = remember(s.stopped, turn)
		return nil
	}
	if displayed || text == "" || (turn != "" && has(s.stopped, turn)) || !r.reads(slug) {
		return nil
	}
	if turn == "" {
		r.seq++
		turn = "stop-" + strconv.Itoa(r.seq)
	}
	m := &readMsg{done: true}
	// The Stop text is a turn of its own for the limits.
	sent, runes, full := s.sent, s.runes, s.full
	s.sent, s.runes, s.full = 0, 0, false
	out := r.take(slug, s, m, turn, turn, Shape(text, r.cfg.Skip))
	s.sent, s.runes, s.full = sent, runes, full
	return out
}

// UserPrompt takes the session's UserPromptSubmit. running is the
// session's hooks state before this prompt updates it: a prompt while
// running (typed ahead) stops reading until the next Stop (6.3). Either
// way the read of the turn so far is dropped and that turn is not read
// any more. The caller also tells the Queue (Queue.UserPrompt).
func (r *Reader) UserPrompt(slug string, running bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.session(slug)
	s.drop()
	s.stopped = remember(s.stopped, s.turn)
	s.displayed = false
	if running {
		s.stopping = true
	}
}

// Interrupt takes an interrupt through pekoe (session.interrupt,
// tapi.interrupt, an Escape in session.keys / tapi.keys): no Stop and no
// final come after it (3.2), so the last turn taken is stopped here and
// stopping ends.
func (r *Reader) Interrupt(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.session(slug)
	s.drop()
	s.stopped = remember(s.stopped, s.turn)
	s.stopping = false
	s.displayed = false
}

// Discard drops what is being read of the session (the focus moved away,
// its mode is no longer read): the messages begun are not read any more,
// later messages are.
func (r *Reader) Discard(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[slug]; s != nil {
		s.drop()
	}
}

// Removed forgets the session.
func (r *Reader) Removed(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, slug)
}
