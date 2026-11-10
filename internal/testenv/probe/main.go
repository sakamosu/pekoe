// Command probe ends make test with one line when this environment cannot
// bind sockets (an agent's sandbox): the tests that need them were skipped.
package main

import (
	"fmt"
	"strings"

	"pekoe/internal/testenv"
)

func main() {
	if m := testenv.Missing(); len(m) > 0 {
		fmt.Printf("note: no %s here, so tests that need one were skipped; run make test outside the agent's sandbox to cover them\n", strings.Join(m, " / "))
	}
}
