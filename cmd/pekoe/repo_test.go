package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// --yes skips the dry run and the question; --config is passed on.
func TestRepoForgetYes(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"repo.forget": map[string]any{"repo_id": "github.com_me_app", "name": "app"}}
	if err := dispatch("repo", "forget", "app", "--config", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := fd.calls(); !reflect.DeepEqual(got, []string{"repo.forget"}) {
		t.Fatalf("calls %v", got)
	}
	var p map[string]any
	if err := json.Unmarshal(fd.params["repo.forget"], &p); err != nil {
		t.Fatal(err)
	}
	if p["repo"] != "app" || p["config"] != true || p["dry_run"] != nil || p["cwd"] == "" {
		t.Fatalf("params %v", p)
	}
}

// A refusal of the dry run (sessions left, a config file) ends it before
// the question.
func TestRepoForgetRefusedBeforeAsking(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.errs = map[string]string{"repo.forget": "the repository still has sessions: app; remove them first (pekoe rm <slug>): feat-x (running)"}
	err := dispatch("repo", "forget", "app")
	if err == nil {
		t.Fatal("no error")
	}
	if got := fd.calls(); !reflect.DeepEqual(got, []string{"repo.forget"}) {
		t.Fatalf("calls %v", got)
	}
	var p map[string]any
	_ = json.Unmarshal(fd.params["repo.forget"], &p)
	if p["dry_run"] != true {
		t.Fatalf("first call is not a dry run: %v", p)
	}
}
