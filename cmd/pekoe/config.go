package main

// pekoe config: the way into config.toml. The file is the only source of
// the settings; these commands open, check and print it. They work on the
// file directly, never through pekoed: ~/.config/pekoe is out of an agent's
// sandbox on purpose, and a request to pekoed would be a way around that.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"pekoe/internal/config"
	"pekoe/internal/paths"
)

func cmdConfig(args []string) error {
	sub, name := "", "config"
	if len(args) > 0 && !isFlag(args[0]) {
		sub, args = args[0], args[1:]
		name = "config " + sub
	}
	if _, ok := specs[name]; !ok {
		name = "config" // unknown subcommand: --help still answers
	}
	_, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return &usageError{use: specs[name].use, msg: "unexpected argument " + pos[0]}
	}
	path := paths.GlobalConfig()
	switch sub {
	case "edit":
		return editConfig(path, os.Stdin, os.Stdout, runEditor)
	case "check":
		return checkConfig(path, os.Stdout)
	case "show":
		return showConfig(os.Stdout)
	case "path":
		if jsonOut {
			_, err := os.Stat(path)
			printJSON(map[string]any{"path": path, "exists": err == nil, "repos": paths.ReposConfigDir()})
			return nil
		}
		fmt.Println(path)
		return nil
	case "":
		return &usageError{use: specs["config"].use, msg: "subcommand required"}
	}
	return &usageError{use: specs["config"].use, msg: "unknown subcommand " + sub}
}

// runEditor opens file in $EDITOR (vi when unset). $EDITOR may carry
// arguments ("code -w"), so it goes through the shell, the file as $1.
func runEditor(file string) error {
	ed := strings.TrimSpace(os.Getenv("EDITOR"))
	if ed == "" {
		ed = "vi"
	}
	cmd := exec.Command("/bin/sh", "-c", ed+` "$1"`, "sh", file)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", ed, err)
	}
	return nil
}

// editConfig is visudo for config.toml: the editor works on a copy next to
// it, the copy is checked when the editor closes, and only then does it
// replace config.toml (in one rename: pekoed and the TUI, which read it
// for changes, never see half a file). With problems it asks: edit again,
// save anyway or quit without saving. A missing config.toml starts from
// config.Template and is created even when left as it is.
func editConfig(path string, in io.Reader, out io.Writer, edit func(string) error) error {
	// a config.toml linked from dotfiles stays a link: the file it points
	// at is the one replaced
	target := path
	if t, err := filepath.EvalSymlinks(path); err == nil {
		target = t
	}
	if err := paths.Ensure(filepath.Dir(target)); err != nil {
		return err
	}
	orig, err := os.ReadFile(target)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	perm := paths.FilePerm
	if fi, err := os.Stat(target); err == nil {
		perm = fi.Mode().Perm()
	}
	start := orig
	if !exists {
		start = []byte(config.Template)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".config-*.toml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpName)
		}
	}()
	_, err = tmp.Write(start)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	rd := bufio.NewReader(in)
	var b []byte
editing:
	for {
		if err := edit(tmpName); err != nil {
			return err
		}
		if b, err = os.ReadFile(tmpName); err != nil {
			return err
		}
		if exists && bytes.Equal(b, orig) {
			fmt.Fprintln(out, "no changes:", path)
			return nil
		}
		ps := config.Check(b)
		if len(ps) == 0 {
			break
		}
		fmt.Fprintf(out, "%s has %s:\n", path, problemCount(len(ps)))
		printProblems(out, ps)
		if ps[0].Fatal {
			fmt.Fprintln(out, "saved like this, pekoe cannot read it: a running pekoed keeps its settings, a new one does not start, the TUI uses the defaults")
		}
		fmt.Fprint(out, "(e)dit again, (s)ave anyway, (q)uit without saving? [e] ")
		line, rerr := rd.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "s", "save":
			break editing
		case "q", "quit":
			fmt.Fprintln(out, "not saved; config.toml is as it was")
			return nil
		case "", "e", "edit":
			if rerr != nil { // no answer coming (stdin closed): keep the edit
				keep = true
				return fmt.Errorf("not saved; your edit is in %s", tmpName)
			}
		default:
			fmt.Fprintln(out, "editing again")
		}
	}
	if now, err := os.ReadFile(target); err == nil && (!exists || !bytes.Equal(now, orig)) {
		// changed while the editor was open (another edit, a dotfiles sync)
		fmt.Fprintf(out, "%s changed while you were editing. Overwrite it? [y/N] ", path)
		line, _ := rd.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			keep = true
			return fmt.Errorf("not saved; your edit is in %s", tmpName)
		}
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	keep = true // renamed: nothing left to remove
	fmt.Fprintln(out, "saved", path)
	return nil
}

func printProblems(out io.Writer, ps []config.Problem) {
	for _, p := range ps {
		fmt.Fprintln(out, "  "+p.String())
	}
}

func problemCount(n int) string {
	return fmt.Sprintf("%d problem%s", n, plural(n))
}

// checkConfig lists what is wrong in config.toml; problems make it fail.
func checkConfig(path string, out io.Writer) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if jsonOut {
			printJSON(map[string]any{"path": path, "exists": false, "problems": []config.Problem{}})
			return nil
		}
		fmt.Fprintln(out, "no config.toml: every setting is at its default ("+path+")")
		return nil
	}
	if err != nil {
		return err
	}
	ps := config.Check(b)
	if jsonOut {
		type problem struct {
			Key   string `json:"key,omitempty"`
			Msg   string `json:"message"`
			Fatal bool   `json:"fatal,omitempty"`
		}
		list := []problem{}
		for _, p := range ps {
			list = append(list, problem{p.Key, p.Msg, p.Fatal})
		}
		printJSON(map[string]any{"path": path, "exists": true, "problems": list})
	} else if len(ps) == 0 {
		fmt.Fprintln(out, "ok:", path)
		return nil
	} else {
		fmt.Fprintf(out, "%s has %s:\n", path, problemCount(len(ps)))
		printProblems(out, ps)
	}
	if len(ps) > 0 {
		return fmt.Errorf("%s in %s", problemCount(len(ps)), path)
	}
	return nil
}

// showConfig prints the settings in use: config.toml with the defaults
// filled in and secrets hidden.
func showConfig(out io.Writer) error {
	g, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	b, err := config.EncodeTOML(config.Effective(g))
	if err != nil {
		return err
	}
	if jsonOut {
		var v map[string]any
		if _, err := toml.Decode(string(b), &v); err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	fmt.Fprintf(out, "# the settings in use: %s with the defaults filled in\n\n", paths.GlobalConfig())
	_, err = out.Write(b)
	return err
}
