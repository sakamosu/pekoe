package voice

// Mode is how a session speaks (1.2).
type Mode string

const (
	ModeRead    Mode = "read"
	ModeNarrate Mode = "narrate"
	ModeMute    Mode = "mute"
	// ModeAuto is "not decided" in pekoe voice mode: the policy decides.
	ModeAuto Mode = "auto"
)

// ParseMode reads a mode setting; ok is false for anything else.
func ParseMode(s string) (Mode, bool) {
	switch m := Mode(s); m {
	case ModeRead, ModeNarrate, ModeMute, ModeAuto:
		return m, true
	}
	return "", false
}

// Allows: an utterance of kind k may be spoken in mode m. Narrate kinds
// speak in read too (read also says state changes); read needs read; test
// speaks always (pekoe voice test plays even when muted).
func (m Mode) Allows(k Kind) bool {
	switch {
	case k == KindTest:
		return true
	case m == ModeMute:
		return false
	case k == KindRead:
		return m == ModeRead
	}
	return m == ModeRead || m == ModeNarrate
}

// Policy decides a session's mode (7). The zero FocusMode and
// BackgroundMode are the defaults read and narrate.
type Policy struct {
	Muted bool
	// Modes are the modes set with pekoe voice mode, by slug. ModeAuto or
	// no entry is "not set".
	Modes map[string]Mode
	// Focus is the session the last client that sent voice.focus shows in
	// [4] ("+tapi" for tapi chat, "" for none). Terminal focus does not
	// matter here.
	Focus          string
	FocusMode      Mode
	BackgroundMode Mode
}

// ModeFor is the mode of the session slug: the first rule that applies of
// global mute, the session's own mode, focus_mode for the focused session,
// background_mode for the rest. slug "" (idle_all) is never focused.
func (p Policy) ModeFor(slug string) Mode {
	if p.Muted {
		return ModeMute
	}
	if m, ok := p.Modes[slug]; ok && m != ModeAuto && m != "" {
		return m
	}
	if slug != "" && slug == p.Focus {
		if p.FocusMode == "" || p.FocusMode == ModeAuto {
			return ModeRead
		}
		return p.FocusMode
	}
	if p.BackgroundMode == "" || p.BackgroundMode == ModeAuto {
		return ModeNarrate
	}
	return p.BackgroundMode
}
