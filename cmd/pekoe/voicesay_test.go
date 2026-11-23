package main

import (
	"os"
	"path/filepath"
	"testing"

	"pekoe/internal/paths"
	"pekoe/internal/voice/backend"
)

// pekoe voice-backend-say reads [voice.say] itself (docs/voice.md 10.6);
// a config.toml that does not read leaves the default voices.
func TestVoiceBackendSayReadsVoiceSay(t *testing.T) {
	t.Setenv("PEKOE_HOME", t.TempDir())
	if got := sayOptions(); got != (backend.SayOptions{}) {
		t.Fatalf("no config.toml: %+v", got)
	}
	os.MkdirAll(filepath.Dir(paths.GlobalConfig()), 0o700)
	os.WriteFile(paths.GlobalConfig(), []byte("[voice.say]\nja = \" Otoya \"\nen = \"Samantha\"\n"), 0o600)
	if got := sayOptions(); got != (backend.SayOptions{JA: "Otoya", EN: "Samantha"}) {
		t.Fatalf("[voice.say]: %+v", got)
	}
	os.WriteFile(paths.GlobalConfig(), []byte("[voice.say\nja = \"Otoya\"\n"), 0o600)
	if got := sayOptions(); got != (backend.SayOptions{}) {
		t.Fatalf("unreadable config.toml: %+v", got)
	}
}
