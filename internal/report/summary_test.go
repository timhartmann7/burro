package report_test

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/report"
)

func intp(n int) *int { return &n }

// failedRun writes a finished run whose e2e step failed after 50 lines of
// colored log, with lint failed before it wrote any.
func failedRun(t *testing.T) (*report.Project, *report.Status) {
	t.Helper()
	p := report.Open(t.TempDir())
	id, _ := p.NewRun()
	var log strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&log, "\x1b[31mline %d\x1b[0m\r\n", i)
	}
	if err := os.WriteFile(p.LogFile(id, stepE2E), []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	end := start.Add(41 * time.Minute)
	return p, &report.Status{
		Run: id, Pipeline: verify, Tree: longTree, State: output.StateDone, Exit: intp(1), Started: start, Finished: &end,
		Steps: []report.Step{
			{Name: stepUnit, State: output.StateOK, Seconds: 190, Exit: intp(0)},
			{Name: stepE2E, State: output.StateFail, Seconds: 1320, Exit: intp(1)},
			// A failed step without a log, such as one that could not start.
			{Name: "lint", State: output.StateFail, Detail: "start bash on the server: missing"},
		},
	}
}

// acceptance: S2-14
func TestSummaryTails(t *testing.T) {
	p, s := failedRun(t)
	sum, err := p.Summary(s, report.NewCleaner(nil), true, 1<<20, *s.Finished)
	if err != nil || sum.Seconds != 41*60 || sum.Tree != longTree || len(sum.Steps) != 3 || sum.Steps[0].Tail != nil {
		t.Fatalf("summary %+v, %v", sum, err)
	}
	e2e := sum.Steps[1]
	if len(e2e.Tail) != 30 || e2e.Tail[0] != "line 21" || e2e.Tail[29] != "line 50" || e2e.More != "burro log r1 --step e2e" {
		t.Fatalf("e2e tail %q more %q", e2e.Tail, e2e.More)
	}
	if lint := sum.Steps[2]; lint.Tail != nil || lint.More == "" {
		t.Fatalf("lint %+v", lint)
	}
	// Two failed steps share half the byte budget; failures from reports
	// get the other half.
	sum, err = p.Summary(s, report.NewCleaner(nil), true, 80, *s.Finished)
	if err != nil || len(sum.Steps[1].Tail) != 2 {
		t.Fatalf("budget: %q, %v", sum.Steps[1].Tail, err)
	}
}

func TestSummaryWithoutTails(t *testing.T) {
	p, s := failedRun(t)
	// As status prints it, and a running run counts up to now.
	s.State, s.Exit, s.Finished = output.StateRunning, nil, nil
	sum, err := p.Summary(s, report.NewCleaner(nil), false, 1<<20, s.Started.Add(5*time.Minute))
	if err != nil || sum.Steps[1].Tail != nil || sum.Steps[1].More != "" || sum.Seconds != 300 {
		t.Fatalf("no tails: %+v, %v", sum, err)
	}
	if os.Geteuid() == 0 {
		return
	}
	if err := os.Chmod(p.LogFile(s.Run, stepE2E), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Summary(s, report.NewCleaner(nil), true, 1<<20, s.Started); err == nil {
		t.Fatal("unreadable log passed")
	}
}

// Fixture reports from testdata/junit.
const (
	pytestXML = "pytest.xml"
	jestXML   = "jest.xml"
)

// junitFixture is a real runner's report from testdata/junit.
func junitFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "junit", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// reportRun is a finished run of pipeline whose steps wrote the named
// fixtures, saved the way the runner saves them. A step listed with no
// fixture failed without a report.
func reportRun(t *testing.T, p *report.Project, dir, pipeline string, steps map[string][]string) *report.Status {
	t.Helper()
	id, err := p.NewRun()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s := &report.Status{Run: id, Pipeline: pipeline, State: output.StateDone, Exit: intp(1), Started: start, Finished: &start}
	for _, name := range slices.Sorted(maps.Keys(steps)) {
		s.Steps = append(s.Steps, report.Step{Name: name, State: output.StateFail})
		out := filepath.Join(dir, "out", id, name)
		if err := os.MkdirAll(out, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, fx := range steps[name] {
			if err := os.WriteFile(filepath.Join(out, fx), junitFixture(t, fx), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.SaveReports(id, name, "out/"+id+"/"+name+"/*.xml", start.Add(-time.Minute), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.WriteStatus(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func details(t *testing.T, p *report.Project, s *report.Status) *output.Summary {
	t.Helper()
	sum, err := p.Summary(s, report.NewCleaner(nil), true, 1<<20, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// acceptance: S3-02
func TestSummaryMergesProjects(t *testing.T) {
	dir := t.TempDir()
	p := report.Open(dir)
	sum := details(t, p, reportRun(t, p, dir, verify, map[string][]string{stepE2E: {"playwright.xml"}}))
	e2e := sum.Steps[0]
	if e2e.Tests == nil || *e2e.Tests != (output.Tests{Total: 12, Passed: 3, Failed: 6, Skipped: 3}) || e2e.Tail != nil || e2e.Unreadable {
		t.Fatalf("e2e %+v tests %+v", e2e, e2e.Tests)
	}
	if len(sum.Failures) != 2 {
		t.Fatalf("failures %+v", sum.Failures)
	}
	f := sum.Failures[0]
	if f.Name != "cohorts are listed" || f.Count != 3 || f.Kind != output.KindNew || f.File != "site.spec.js" || f.Line != 8 ||
		f.Message != "expect(received).toHaveLength(expected)" || sum.Failures[1].Count != 3 {
		t.Fatalf("first failure %+v", f)
	}
	if sum.More != "burro log r1 --step e2e --grep cohorts" {
		t.Fatalf("more %q", sum.More)
	}
}

// acceptance: S3-06
func TestSummaryNewAndSeen(t *testing.T) {
	dir := t.TempDir()
	p := report.Open(dir)
	reportRun(t, p, dir, verify, map[string][]string{stepUnit: {pytestXML}})
	reportRun(t, p, dir, verify, map[string][]string{stepUnit: {jestXML}})
	// Another pipeline and a run still going don't count.
	reportRun(t, p, dir, "nightly", map[string][]string{stepUnit: {pytestXML}})
	running := reportRun(t, p, dir, verify, map[string][]string{stepUnit: {pytestXML}})
	running.State, running.Finished = output.StateRunning, nil
	if err := p.WriteStatus(running); err != nil {
		t.Fatal(err)
	}
	sum := details(t, p, reportRun(t, p, dir, verify, map[string][]string{stepUnit: {jestXML, pytestXML}}))
	kinds := map[string]string{}
	for _, f := range sum.Failures {
		kinds[f.Name] = f.Kind
	}
	want := map[string]string{
		"calc divides numbers": output.KindSeen, "calc parses the config": output.KindSeen,
		"test_divides": output.KindNew, "test_parses_config": output.KindNew, "test_reads_rows": output.KindNew,
	}
	if !maps.Equal(kinds, want) {
		t.Fatalf("kinds %v", kinds)
	}
	if first := details(t, p, &report.Status{Run: r1, Pipeline: verify, State: output.StateDone, Exit: intp(1), Steps: []report.Step{{Name: stepUnit, State: output.StateFail}}}); first.Failures[0].Kind != output.KindNew {
		t.Fatalf("first run %+v", first.Failures[0])
	}
}

func TestSummaryPartialRunIsNoBaseline(t *testing.T) {
	dir := t.TempDir()
	p := report.Open(dir)
	reportRun(t, p, dir, verify, map[string][]string{stepUnit: {jestXML}})
	// A run of a few tests passes them on and a canceled run stops before
	// them: neither says anything of the others.
	partial := reportRun(t, p, dir, verify, map[string][]string{stepUnit: {pytestXML}})
	partial.Args = []string{"tests/test_calc.py"}
	if err := p.WriteStatus(partial); err != nil {
		t.Fatal(err)
	}
	canceled := reportRun(t, p, dir, verify, map[string][]string{stepUnit: {}})
	canceled.State, canceled.Reason = output.StateCanceled, output.StateCanceled
	if err := p.WriteStatus(canceled); err != nil {
		t.Fatal(err)
	}
	sum := details(t, p, reportRun(t, p, dir, verify, map[string][]string{stepUnit: {jestXML}}))
	for _, f := range sum.Failures {
		if f.Kind != output.KindSeen {
			t.Fatalf("%s is %s after the full run that had it", f.Name, f.Kind)
		}
	}
}

// acceptance: S3-04
func TestSummaryBrokenReports(t *testing.T) {
	dir := t.TempDir()
	p := report.Open(dir)
	s := reportRun(t, p, dir, verify, map[string][]string{
		"broken": {"broken-truncated.xml", "broken-empty.xml", "broken-invalid.xml", "broken-wrong-root.xml"},
		"half":   {"broken-empty.xml", "gotestsum.xml"},
		"none":   nil,
	})
	for _, st := range s.Steps {
		if err := os.WriteFile(p.LogFile(s.Run, st.Name), []byte("last words of "+st.Name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum := details(t, p, s)
	broken, half, none := sum.Steps[0], sum.Steps[1], sum.Steps[2]
	if !broken.Unreadable || broken.Tests != nil || strings.Join(broken.Tail, "|") != "last words of broken" || broken.More != "burro log r1 --step broken" {
		t.Fatalf("broken %+v", broken)
	}
	if !half.Unreadable || half.Tail != nil || half.Tests.Total != 4 || len(sum.Failures) != 2 || sum.Failures[0].Step != "half" {
		t.Fatalf("half %+v, failures %+v", half, sum.Failures)
	}
	if none.Unreadable || strings.Join(none.Tail, "|") != "last words of none" {
		t.Fatalf("none %+v", none)
	}
	if *sum.Exit != 1 {
		t.Fatalf("exit %d", *sum.Exit)
	}
}

func TestSummaryPassedStepCounts(t *testing.T) {
	dir := t.TempDir()
	p := report.Open(dir)
	s := reportRun(t, p, dir, verify, map[string][]string{stepUnit: {"gotestsum.xml"}})
	s.Steps[0].State = output.StateOK
	sum := details(t, p, s)
	if st := sum.Steps[0]; st.Tests == nil || st.Tests.Passed != 1 || st.Tail != nil || len(sum.Failures) != 0 || sum.More != "" {
		t.Fatalf("ok step %+v, failures %+v", st, sum.Failures)
	}
}

func TestSummaryReplyBudget(t *testing.T) {
	dir := t.TempDir()
	p := report.Open(dir)
	s := reportRun(t, p, dir, verify, map[string][]string{stepE2E: {pytestXML}})
	sum, err := p.Summary(s, report.NewCleaner(nil), true, 200, time.Now())
	if err != nil || len(sum.Failures) == 0 || len(sum.Failures)+sum.Hidden != 3 || sum.Hidden == 0 {
		t.Fatalf("failures %d hidden %d, %v", len(sum.Failures), sum.Hidden, err)
	}
}

func TestSaveReports(t *testing.T) {
	dir := t.TempDir()
	p := report.Open(dir)
	id, err := p.NewRun()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(filepath.Join(out, "sub.xml"), 0o700); err != nil {
		t.Fatal(err)
	}
	fresh := bytes.ReplaceAll(junitFixture(t, pytestXML), []byte("database is down"), []byte("password hunter22 leaked"))
	for name, data := range map[string][]byte{"new.xml": fresh, "old.xml": junitFixture(t, jestXML)} {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(out, "old.xml"), old, old); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.xml")
	if err := os.WriteFile(outside, junitFixture(t, "vitest.xml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(out, "link.xml")); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveReports(id, stepUnit, "out/*.xml", time.Now().Add(-time.Minute), [][]byte{[]byte("hunter22")}); err != nil {
		t.Fatal(err)
	}
	copies, err := filepath.Glob(filepath.Join(p.RunDir(id), "junit", stepUnit, "*"))
	if err != nil || len(copies) != 1 {
		t.Fatalf("copies %v, %v", copies, err)
	}
	data, err := os.ReadFile(copies[0])
	if err != nil || bytes.Contains(data, []byte("hunter22")) || !bytes.Contains(data, []byte("password *** leaked")) {
		t.Fatalf("copy %q, %v", data, err)
	}
	if err := p.SaveReports(id, "none", "missing/*.xml", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveReports(id, "bad", "[", time.Now(), nil); err == nil {
		t.Fatal("bad pattern passed")
	}
}

// A report over 64 MiB is copied only far enough to tell, and then shows
// as unreadable.
func TestSaveReportsTooBig(t *testing.T) {
	dir := t.TempDir()
	p := report.Open(dir)
	s := reportRun(t, p, dir, verify, map[string][]string{stepUnit: nil})
	huge := filepath.Join(dir, "huge.xml")
	if err := os.WriteFile(huge, junitFixture(t, "gotestsum.xml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(huge, 64<<20+10); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveReports(s.Run, stepUnit, "huge.xml", time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if sum := details(t, p, s); !sum.Steps[0].Unreadable || sum.Steps[0].Tests != nil {
		t.Fatalf("step %+v", sum.Steps[0])
	}
}
