package backend

// The contract tests: what docs/voice.md 10.3 asks of every backend, run
// against a Backend the same way pekoed calls it.
//
//	go test ./internal/voice/backend -run TestContract
//
// covers the fake backend and the bundled say (on macOS with /usr/bin/say).
// To check another backend (pekoe-voice-voicevox in P2, or your own):
//
//	PEKOE_VOICE_CONTRACT=/opt/homebrew/bin/pekoe-voice-voicevox \
//	  go test ./internal/voice/backend -run TestContractExternal -v

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// contract runs the protocol's rules against b. A backend whose check
// fails here (no engine running, no voice installed) is skipped with its
// reason: the rules cannot be checked without it.
func contract(t *testing.T, b *Backend) {
	ctx := context.Background()
	t.Run("version prints 1", func(t *testing.T) {
		if v, err := b.Version(ctx); err != nil || v != Protocol {
			t.Fatalf("version %q: %v", v, err)
		}
	})
	t.Run("unknown subcommand fails", func(t *testing.T) {
		if _, err := b.run(ctx, "frobnicate", b.Timeouts.orDefault().Check, nil, "frobnicate"); err == nil {
			t.Fatal("an unknown subcommand exited 0")
		}
	})
	if err := b.CheckUsable(ctx); err != nil {
		t.Skipf("check fails here, so the rest cannot be checked: %v", err)
	}
	var vs []Voice
	t.Run("voices is a JSON list", func(t *testing.T) {
		var err error
		if vs, err = b.Voices(ctx); err != nil {
			t.Fatal(err)
		}
		if len(vs) == 0 {
			t.Fatal("no voices")
		}
		for _, v := range vs {
			if v.Name == "" || (v.Lang != "ja" && v.Lang != "en") {
				t.Errorf("voice %+v: name must be set, lang ja or en", v)
			}
		}
	})
	langs := map[string]bool{}
	for _, v := range vs {
		if v.Name == "tapi" {
			langs[v.Lang] = true
		}
	}
	text := map[string]string{"ja": "テスト、確認待ちだよ", "en": "test needs you"}
	for _, lang := range []string{"ja", "en"} {
		if !langs[lang] {
			continue
		}
		t.Run("synth "+lang, func(t *testing.T) {
			for _, voice := range []string{"tapi", "reader", "a-voice-pekoe-adds-later"} {
				out := filepath.Join(t.TempDir(), "out.wav")
				if err := b.Synth(ctx, Request{Text: text[lang], Voice: voice, Lang: lang}, out); err != nil {
					t.Errorf("voice %s: %v", voice, err)
				}
			}
			// shell and path characters are only text
			out := filepath.Join(t.TempDir(), "out.wav")
			if err := b.Synth(ctx, Request{Text: `"$(touch x)"; ../../etc` + "`id`", Voice: "tapi", Lang: lang}, out); err != nil {
				t.Errorf("text with shell characters: %v", err)
			}
		})
	}
	if len(langs) == 0 {
		t.Fatal("voices lists no tapi voice")
	}
	t.Run("unsupported language fails", func(t *testing.T) {
		err := b.Synth(ctx, Request{Text: "x", Voice: "tapi", Lang: "xx"}, filepath.Join(t.TempDir(), "out.wav"))
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("--lang xx: %v", err)
		}
	})
	t.Run("two synth at once", func(t *testing.T) {
		lang := "en"
		if !langs[lang] {
			lang = "ja"
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = b.Synth(ctx, Request{Text: text[lang], Voice: "tapi", Lang: lang}, filepath.Join(t.TempDir(), "out.wav"))
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
	})
}

func TestContractFake(t *testing.T) {
	b, _ := newFake(t, "ok")
	contract(t, b)
}

// The bundled say, called as pekoed calls it (pekoe voice-backend-say …,
// here the test binary standing in for pekoe), with the real /usr/bin/say.
func TestContractBundledSay(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the bundled say needs macOS (" + runtime.GOOS + ")")
	}
	if _, err := os.Stat(SayBin); err != nil {
		t.Skip(SayBin + " is not here: " + err.Error())
	}
	dir := t.TempDir()
	self := fakeBackend(t, filepath.Join(dir, "bin"), "fake-pekoe-say")
	b, err := Resolve(Builtin, Options{Self: self, Dir: filepath.Join(dir, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if why := slowSay(t); why != "" {
		t.Skip(why)
	}
	contract(t, b)
}

// Any backend: PEKOE_VOICE_CONTRACT=<absolute path>.
func TestContractExternal(t *testing.T) {
	p := os.Getenv("PEKOE_VOICE_CONTRACT")
	if p == "" {
		t.Skip("set PEKOE_VOICE_CONTRACT=<absolute path of a backend> to check it")
	}
	b, err := Resolve("external", Options{Backends: map[string]string{"external": p}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	contract(t, b)
}

// slowSay measures this Mac's say itself, not the bundled backend (a hang
// in the backend must still fail): one sentence to warm it up, then one
// timed. Where say cannot finish a sentence well inside the protocol's 3
// seconds even warm (GitHub's macOS runners take about 10 seconds), the
// contract cannot be held to it here; the reason says so. "" when say is
// fast enough.
func slowSay(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(i int) (time.Duration, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, SayBin, "--file-format=WAVE", "--data-format=LEI16@24000", "-o", filepath.Join(dir, strconv.Itoa(i)+".wav"), "-f", "-")
		cmd.Stdin = strings.NewReader("test")
		start := time.Now()
		err := cmd.Run()
		return time.Since(start), err
	}
	if d, err := run(0); err != nil {
		return fmt.Sprintf("%s cannot write a sentence here (%v after %s)", SayBin, err, d.Round(time.Millisecond))
	}
	d, err := run(1)
	if err != nil {
		return fmt.Sprintf("%s cannot write a sentence here (%v after %s)", SayBin, err, d.Round(time.Millisecond))
	}
	if limit := DefaultTimeouts.Synth / 2; d > limit {
		return fmt.Sprintf("%s takes %s for one sentence here even warmed up, too close to the protocol's %s to check the bundled say against it (limit %s)", SayBin, d.Round(time.Millisecond), DefaultTimeouts.Synth, limit)
	}
	t.Logf("%s: %s for one sentence", SayBin, d.Round(time.Millisecond))
	return ""
}
