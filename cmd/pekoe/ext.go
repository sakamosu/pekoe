package main

// ext: port and token for the Chrome extension.

import (
	"fmt"
	"os"
	"path/filepath"
)

func cmdExt() error {
	cl := connect()
	defer cl.Close()
	var info struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := cl.Call("ext.info", nil, &info); err != nil {
		return err
	}
	if jsonOut {
		printJSON(info)
		return nil
	}
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	extDir := filepath.Join(filepath.Dir(filepath.Dir(exe)), "extension")
	if alt := filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "pekoe", "extension"); hintDir(extDir) == "" && hintDir(alt) != "" {
		extDir = alt
	}
	fmt.Printf("port   %d\ntoken  %s\n\nChrome: chrome://extensions → Developer mode → Load unpacked\n        (choose extension/ in the pekoe repository%s) → enter port and token in the extension's options\n", info.Port, info.Token, hintDir(extDir))
	return nil
}

func hintDir(d string) string {
	if _, err := os.Stat(filepath.Join(d, "manifest.json")); err == nil {
		return ": " + d
	}
	return ""
}
