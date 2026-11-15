package tui

import "golang.org/x/sys/unix"

// macOpensItsOwnWay: the file tells Launch Services itself what opens it,
// so `open <path>` would not go by its extension. An agent can write all of
// these on a file it owns:
//
//   - a Finder alias (the alias flag in com.apple.FinderInfo): open follows
//     it, to a .command as readily as to an image
//   - a per-file "Open With" (com.apple.LaunchServices.OpenWith; the older
//     form lives in the resource fork): open hands the file to that app,
//     Terminal included
//   - type / creator codes in com.apple.FinderInfo
//
// The other Finder flags (a hidden extension, as on a screenshot) change
// nothing about what opens. An attribute that is there but cannot be read
// counts as set.
func macOpensItsOwnWay(path string) bool {
	// no such attribute, or a file system that keeps none
	absent := func(err error) bool { return err == unix.ENOATTR || err == unix.ENOTSUP }
	for _, a := range []string{"com.apple.LaunchServices.OpenWith", "com.apple.ResourceFork"} {
		if _, err := unix.Getxattr(path, a, nil); !absent(err) {
			return true
		}
	}
	info := make([]byte, 32) // FileInfo: type, creator, flags (big endian) …
	n, err := unix.Getxattr(path, "com.apple.FinderInfo", info)
	if absent(err) {
		return false
	}
	if err != nil || n < 10 {
		return true
	}
	const isAlias = 0x80 // kIsAlias 0x8000, in the flags' high byte
	return string(info[:8]) != "\x00\x00\x00\x00\x00\x00\x00\x00" || info[8]&isAlias != 0
}
