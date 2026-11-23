package voice

import "testing"

// Rule 7: the first rule that applies decides the mode.
func TestRule7ModeFor(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
		slug string
		want Mode
	}{
		{"7.1 muted beats everything", Policy{Muted: true, Modes: map[string]Mode{"a": ModeRead}, Focus: "a"}, "a", ModeMute},
		{"7.2 own mode beats focus", Policy{Modes: map[string]Mode{"a": ModeMute}, Focus: "a"}, "a", ModeMute},
		{"7.2 own mode for a background session", Policy{Modes: map[string]Mode{"a": ModeRead}}, "a", ModeRead},
		{"7.2 auto is not set", Policy{Modes: map[string]Mode{"a": ModeAuto}, Focus: "a"}, "a", ModeRead},
		{"7.3 focus_mode default read", Policy{Focus: "a"}, "a", ModeRead},
		{"7.3 focus_mode set", Policy{Focus: "a", FocusMode: ModeNarrate}, "a", ModeNarrate},
		{"7.3 tapi chat focused", Policy{Focus: TapiSlug, FocusMode: ModeMute}, TapiSlug, ModeMute},
		{"7.4 background default narrate", Policy{Focus: "a"}, "b", ModeNarrate},
		{"7.4 background_mode set", Policy{BackgroundMode: ModeMute}, "b", ModeMute},
		{"7.4 no focus", Policy{FocusMode: ModeMute}, "b", ModeNarrate},
		{"7 idle_all (no slug) is never focused", Policy{Focus: "", FocusMode: ModeMute}, "", ModeNarrate},
	}
	for _, c := range cases {
		if got := c.p.ModeFor(c.slug); got != c.want {
			t.Errorf("%s: ModeFor(%q) = %s, want %s", c.name, c.slug, got, c.want)
		}
	}
}

// Rule 7 / 1.2: which kinds each mode speaks.
func TestRule7ModeAllows(t *testing.T) {
	cases := []struct {
		m    Mode
		k    Kind
		want bool
	}{
		{ModeNarrate, KindWait, true},
		{ModeNarrate, KindRead, false},
		{ModeRead, KindDone, true},
		{ModeRead, KindRead, true},
		{ModeMute, KindWait, false},
		{ModeMute, KindIdleAll, false},
		{ModeMute, KindRead, false},
		{ModeMute, KindTest, true},
	}
	for _, c := range cases {
		if got := c.m.Allows(c.k); got != c.want {
			t.Errorf("%s.Allows(%s) = %v, want %v", c.m, c.k, got, c.want)
		}
	}
}

func TestParseMode(t *testing.T) {
	for _, s := range []string{"read", "narrate", "mute", "auto"} {
		if m, ok := ParseMode(s); !ok || string(m) != s {
			t.Errorf("ParseMode(%q) = %q, %v", s, m, ok)
		}
	}
	if _, ok := ParseMode("loud"); ok {
		t.Error("ParseMode(loud) ok")
	}
}
