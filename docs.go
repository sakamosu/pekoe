// Package pekoe holds the documents that ship inside the binary: the README
// and docs/ that a tip points to (`pekoe tip --doc`), so tapi chat can read
// them wherever pekoe was installed from.
package pekoe

import "embed"

//go:embed README.md docs/*.md nvim/README.md extension/README.md
var Docs embed.FS
