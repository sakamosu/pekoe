// Package pekoe holds the documents that ship inside the binary: the READMEs
// and docs/ that a tip points to (`pekoe tip --doc`), so tapi chat can read
// them wherever pekoe was installed from, and the Claude Code skills that
// `pekoe skills install` writes out (internal/skills).
package pekoe

import "embed"

//go:embed README.md README.ja.md docs/*.md nvim/README.md extension/README.md skills
var Docs embed.FS
