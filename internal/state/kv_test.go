package state

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The prefix is matched by characters, not bytes: a repo id made from a
// non-ASCII path (path_Users_me_プロジェクト_app) must still match.
func TestPrefixedNonASCII(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, k := range []string{"dur:path_プロ_app:worktree", "dur:path_プロ_app:env", "dur:path_プロ_other:env", "dur:ascii:env"} {
		if err := db.Set(k, "1s"); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, kv := range db.Prefixed("dur:path_プロ_app:") {
		got = append(got, kv[0])
	}
	if want := []string{"dur:path_プロ_app:env", "dur:path_プロ_app:worktree"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if n := len(db.Prefixed("dur:ascii:")); n != 1 {
		t.Fatalf("ascii prefix: %d", n)
	}
}
