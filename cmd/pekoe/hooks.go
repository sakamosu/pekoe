package main

// hooks: install Claude Code hooks.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pekoe/internal/hooks"
)

func cmdHooks(args []string) error {
	f, pos, err := parseCmd("hooks", args)
	if err != nil {
		return err
	}
	sub := "status"
	if len(pos) > 0 {
		sub = pos[0]
	}
	bin, _ := os.Executable()
	if b, err := filepath.EvalSymlinks(bin); err == nil {
		bin = b
	}
	switch sub {
	case "status":
		if hooks.Installed() {
			fmt.Println("installed in", hooks.SettingsPath())
		} else {
			fmt.Println("not installed · run `pekoe hooks install`")
		}
		return nil
	case "install", "uninstall":
		path, before, after, changed, err := hooks.Plan(bin, sub == "uninstall")
		if err != nil {
			return err
		}
		if !changed {
			fmt.Println("already up to date:", path)
			return nil
		}
		fmt.Printf("%s will be updated (%s pekoe hooks for %s).\n", path, map[bool]string{true: "remove", false: "add"}[sub == "uninstall"], strings.Join(hooks.Events, ", "))
		if f["yes"] == "" && !confirm("write it? (a backup is kept)") {
			return nil
		}
		backup, err := hooks.Apply(path, before, after)
		if err != nil {
			return err
		}
		if backup != "" {
			fmt.Println("backup:", backup)
		}
		fmt.Println("done:", path)
		return nil
	}
	return fmt.Errorf("usage: pekoe hooks install|uninstall|status")
}
