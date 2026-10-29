package report_test

import (
	"fmt"
	"os"
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
	// Two failed steps share the byte budget.
	sum, err = p.Summary(s, report.NewCleaner(nil), true, 40, *s.Finished)
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
