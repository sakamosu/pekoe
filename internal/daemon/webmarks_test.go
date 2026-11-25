package daemon

import (
	"encoding/json"
	"regexp"
	"strconv"
	"testing"
)

// The phone page's marks (webui/marks.js, docs/web.md 「画面の構成」): the
// counts as one band of links, a request's steps as dots (the person's
// turn warn), and the merge conditions and the CI as bars, one a
// condition / a check, with their counts. Run in node over a fake DOM
// (skipped without node).
func TestWebMarks(t *testing.T) {
	out := runWebModule(t, fakeDOM+"const mergeWaits = "+strconv.Quote(mergeWaitsDetail)+";\n"+`
import { countBand, stepLook, stepper, gateBars, ciBar, mergeMeters } from "marks.js";
const cls = (n, re) => find(n, (x) => re.test(x.className));
const r = {};

const b0 = countBand({ waiting: 2, questions: 1, ready: 2, running: 0, queued: 0, review: 7 });
r.band = cls(b0, /^bc\b/).map((c) => [c.href, c.className, c.textContent]);
r.bandQueued = cls(countBand({ queued: 3 }), /^bc\b/).map((c) => c.textContent);

const S = (name, status, detail) => ({ name, status, detail });
r.looks = [
  stepLook(S("impl", "done")),
  stepLook(S("impl", "now", "running"), false),
  stepLook(S("impl", "now", "waiting"), true),
  stepLook(S("ci", "now", "2 pending"), true),
  stepLook(S("merge", "now", mergeWaits), false),
  stepLook(S("merge", "now", "pekoed is merging (checking again first)"), true),
  stepLook(S("review", "fail")),
  stepLook(S("pr", "todo")),
  stepLook(S("pr", "odd")),
];
const st = stepper([S("impl", "done", "reported"), S("review", "now", "independent review in progress"), S("pr", "todo"), S("ci", "todo"), S("merge", "todo")], true);
r.steps = st.children.map((li) => [li.className, li.title, li.textContent]);

const conds = [["pr", true], ["ci", false], ["review", true], ["merge_state", false], ["conflicts", true], ["settled", true]]
  .map(([name, ok]) => ({ name, ok, detail: "x" }));
const g = gateBars({ conds });
r.gates = [cls(g, /^mseg /).map((b) => b.className), cls(g, /^mn$/)[0].textContent, cls(g, /^bars$/)[0].attrs["aria-label"]];
r.noGates = gateBars({ conds: [] }) === null && gateBars(undefined) === null;

const c = ciBar({ pass: 3, fail: 1, pending: 2 });
r.ci = [cls(c, /^mseg /).map((b) => b.className), cls(c, /^mn$/)[0].textContent];
r.ciAll = cls(ciBar({ pass: 5, fail: 0, pending: 0 }), /^mn$/)[0].textContent;
r.noCI = ciBar({ pass: 0, fail: 0, pending: 0 }) === null && ciBar(null) === null;

r.meters = [
  mergeMeters({ merge: { conds }, pr_status: { pass: 1, fail: 0, pending: 0 } }).children.map((x) => x.className),
  mergeMeters({ pr_status: { pass: 1, fail: 0, pending: 0 } }).children.map((x) => x.className),
  mergeMeters({ test: { status: "pass" } }),
  mergeMeters(undefined),
];
console.log(JSON.stringify(r));
`)
	var r struct {
		Band       [][3]string
		BandQueued []string
		Looks      [][2]string
		Steps      [][3]string
		Gates      []json.RawMessage
		NoGates    bool
		CI         []json.RawMessage
		CIAll      string
		NoCI       bool
		Meters     []json.RawMessage
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("node output: %v\n%s", err, out)
	}
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	// the band: waiting · inbox (questions + ready) · running · to review,
	// a 0 dim (zero); queued only when it is not 0, before to review
	if got, want := j(r.Band), j([][3]string{
		{"#/sessions/active", "bc", "2◉ waiting"},
		{"#/inbox", "bc", "3? inbox"},
		{"#/sessions/active", "bc zero", "0● running"},
		{"#/sessions/review", "bc", "7✓ to review"},
	}); got != want {
		t.Errorf("band:\n got %s\nwant %s", got, want)
	}
	if got, want := j(r.BandQueued), j([]string{"0◉ waiting", "0? inbox", "0● running", "3◌ queued", "0✓ to review"}); got != want {
		t.Errorf("band with queued:\n got %s\nwant %s", got, want)
	}

	// the person's turn is warn (you): a waiting session's step but CI's,
	// and a merge waiting for a person; pekoed merging is under way
	if got, want := j(r.Looks), j([][2]string{
		{"done", "✓"}, {"now", "◉"}, {"you", "◉"}, {"now", "◉"}, {"you", "◉"}, {"now", "◉"}, {"fail", "✗"}, {"todo", ""}, {"todo", ""},
	}); got != want {
		t.Errorf("step looks:\n got %s\nwant %s", got, want)
	}
	if got, want := j(r.Steps), j([][3]string{
		{"sp st-done", "reported", "✓impl"},
		{"sp st-you", "independent review in progress", "◉review"},
		{"sp st-todo", "", "PR"},
		{"sp st-todo", "", "CI"},
		{"sp st-todo", "", "merge"},
	}); got != want {
		t.Errorf("stepper:\n got %s\nwant %s", got, want)
	}

	// a bar a condition, in order; the counts beside
	if got, want := j(r.Gates), `[["mseg b-ok","mseg b-err","mseg b-ok","mseg b-err","mseg b-ok","mseg b-ok"],"4/6 gates","4 of 6 merge conditions met"]`; got != want {
		t.Errorf("gates:\n got %s\nwant %s", got, want)
	}
	if !r.NoGates {
		t.Error("gates without conditions: want null")
	}
	// a part a check: passed, failed, running
	if got, want := j(r.CI), `[["mseg b-ok","mseg b-ok","mseg b-ok","mseg b-err","mseg b-pend","mseg b-pend"],"CI 3/6 · 1 failed · 2 running"]`; got != want {
		t.Errorf("ci:\n got %s\nwant %s", got, want)
	}
	if r.CIAll != "CI 5/5" {
		t.Errorf("ci all passed: %q", r.CIAll)
	}
	if !r.NoCI {
		t.Error("ci with no checks: want null")
	}
	if got, want := j(r.Meters), `[["meter gates","meter ci"],["meter ci"],null,null]`; got != want {
		t.Errorf("meters:\n got %s\nwant %s", got, want)
	}
}

// The marks are drawn with classes only: no width or color is written
// from the script (docs/gui-design.md 「実装の制約との整合」: CSP's
// style-src 'self'; the bars are equal parts of a grid or flex, app.css).
func TestWebMarksNoStyles(t *testing.T) {
	js, err := webFiles.ReadFile("webui/marks.js")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`\.style\b|setAttribute\(\s*["']style|cssText|setProperty`).FindAllString(string(js), -1); len(m) > 0 {
		t.Errorf("marks.js sets styles: %v", m)
	}
}
