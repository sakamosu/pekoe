package backend

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"pekoe/internal/testenv"
)

// TestMain keeps every test away from the real ~/.local/share/pekoe
// (internal/testenv). Started through a symbolic link named fake-* or
// pekoe-voice-*, the test binary is instead a fake backend (fakeMain): the
// tests make such links in t.TempDir and configure them as backends.
func TestMain(m *testing.M) {
	base := filepath.Base(os.Args[0])
	if strings.HasPrefix(base, "fake-") || strings.HasPrefix(base, CandidatePrefix) {
		os.Exit(fakeMain(base, os.Args[1:]))
	}
	testenv.Main(m)
}

// fakeBackend makes a link to the test binary named name in dir (made
// when missing) and returns its path.
func fakeBackend(t *testing.T, dir, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.Symlink(self, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// marked: what the fake at p was called with (one line per call), "" when
// it never ran.
func marked(p string) string {
	b, _ := os.ReadFile(p + ".called")
	return string(b)
}

// tinyWAV is a valid, silent RIFF/WAVE file.
func tinyWAV() []byte {
	var b []byte
	le32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	le16 := func(v uint16) { b = binary.LittleEndian.AppendUint16(b, v) }
	data := make([]byte, 480)
	b = append(b, "RIFF"...)
	le32(uint32(36 + len(data)))
	b = append(b, "WAVEfmt "...)
	le32(16)
	le16(1)
	le16(1)
	le32(24000)
	le32(48000)
	le16(2)
	le16(16)
	b = append(b, "data"...)
	le32(uint32(len(data)))
	return append(b, data...)
}

// fakeMain is a fake backend. Its mode is its name:
//
//	fake-ok, pekoe-voice-*  follows the protocol
//	fake-fail               every command fails (exit 3, a reason on stderr)
//	fake-v2                 version prints 2
//	fake-hang               synth starts a grandchild and never ends
//	fake-stubborn           synth ignores SIGTERM and never ends
//	fake-empty              synth writes an empty file
//	fake-notriff            synth writes text
//	fake-nofile             synth exits 0 without writing
//	fake-env                synth also records its environment, cwd and stdin
//	fake-pair               synth waits until another synth has started
//	fake-flaky              check and synth fail while <self>.broken exists
//	fake-ckey               follows the protocol and knows cache-key: it
//	                        prints <self>.ckey as it is; "exit1", "exit2" or
//	                        "hang" in it do that instead; no file is exit 2
//	fake-pekoe              pekoe itself: "voice-backend-say …" is fake-ok
//	fake-pekoe-say          pekoe itself with the real SayMain
//
// Every call appends its arguments to <self>.called.
func fakeMain(name string, args []string) int {
	self := os.Args[0]
	if f, err := os.OpenFile(self+".called", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	mode := strings.TrimPrefix(name, "fake-")
	if strings.HasPrefix(name, CandidatePrefix) {
		mode = "ok"
	}
	if mode == "pekoe" || mode == "pekoe-say" {
		if len(args) == 0 || args[0] != BuiltinCommand {
			fmt.Fprintln(os.Stderr, "not voice-backend-say")
			return 2
		}
		if mode == "pekoe-say" {
			return SayMain(args[1:], SayOptions{}, os.Stdin, os.Stdout, os.Stderr)
		}
		args, mode = args[1:], "ok"
	}
	if len(args) == 0 {
		return 2
	}
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "boom: the engine is down\nsecond line")
		return 3
	}
	switch args[0] {
	case "version":
		if mode == "v2" {
			fmt.Println("2")
		} else {
			fmt.Println(" 1 ")
		}
		return 0
	case "check":
		if _, err := os.Stat(self + ".broken"); err == nil && mode == "flaky" {
			fmt.Fprintln(os.Stderr, "flaky: broken")
			return 1
		}
		return 0
	case "voices":
		json.NewEncoder(os.Stdout).Encode([]Voice{{"tapi", "ja", "Fake"}, {"tapi", "en", "Fake"}})
		return 0
	case "cache-key":
		if mode != "ckey" {
			fmt.Fprintln(os.Stderr, "unknown command", args[0])
			return 2
		}
		b, err := os.ReadFile(self + ".ckey")
		switch {
		case err != nil || string(b) == "exit2":
			fmt.Fprintln(os.Stderr, "unknown command", args[0])
			return 2
		case string(b) == "exit1":
			fmt.Fprintln(os.Stderr, "ckey: cannot read the settings")
			return 1
		case string(b) == "hang":
			time.Sleep(60 * time.Second)
			return 0
		}
		os.Stdout.Write(b)
		return 0
	case "synth":
	default:
		fmt.Fprintln(os.Stderr, "unknown command", args[0])
		return 2
	}
	f := map[string]string{}
	for i := 1; i+1 < len(args); i += 2 {
		f[strings.TrimPrefix(args[i], "--")] = args[i+1]
	}
	if f["lang"] != "ja" && f["lang"] != "en" {
		fmt.Fprintln(os.Stderr, "unsupported language", f["lang"])
		return 2
	}
	text, _ := io.ReadAll(os.Stdin)
	out := f["out"]
	switch mode {
	case "hang":
		c := exec.Command("/bin/sleep", "60")
		c.Start()
		os.WriteFile(self+".child", []byte(fmt.Sprint(c.Process.Pid)), 0o600)
		time.Sleep(60 * time.Second)
		return 0
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		os.WriteFile(self+".child", []byte(fmt.Sprint(os.Getpid())), 0o600)
		time.Sleep(60 * time.Second)
		return 0
	case "empty":
		os.WriteFile(out, nil, 0o600)
		return 0
	case "notriff":
		os.WriteFile(out, []byte("hello, not a wav"), 0o600)
		return 0
	case "nofile":
		return 0
	case "env":
		wd, _ := os.Getwd()
		rec := map[string]string{"PATH": os.Getenv("PATH"), "PEKOE_VOICE_PROTOCOL": os.Getenv("PEKOE_VOICE_PROTOCOL"), "cwd": wd, "stdin": string(text), "out": out}
		b, _ := json.Marshal(rec)
		os.WriteFile(self+".env", b, 0o600)
	case "pair":
		os.WriteFile(fmt.Sprintf("%s.start.%d", self, os.Getpid()), nil, 0o600)
		deadline := time.Now().Add(5 * time.Second)
		for {
			m, _ := filepath.Glob(self + ".start.*")
			if len(m) >= 2 {
				break
			}
			if time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "no other synth ran at the same time")
				return 1
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "flaky":
		if _, err := os.Stat(self + ".broken"); err == nil {
			fmt.Fprintln(os.Stderr, "flaky: broken")
			return 1
		}
	}
	if err := os.WriteFile(out, tinyWAV(), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
