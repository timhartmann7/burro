package output_test

import (
	"strings"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/mode"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	shortTree       = "5530df9"
	pipelineDefault = "default"
	grepCohorts     = "cohorts"
	stepE2E         = "e2e"
	stepTest        = "test"
	stepLighthouse  = "lighthouse"
	pipelineVerify  = "verify"
)

func exit(n int) *int { return &n }

// runResults are the results of the S2 commands, by golden file name.
func runResults() map[string]func(p *output.Printer) error {
	return map[string]func(p *output.Printer) error{
		"run-started": func(p *output.Printer) error {
			return p.Started(&output.Started{Run: "r42", Pipeline: pipelineVerify, Tree: shortTree, Steps: []string{stepE2E, stepLighthouse}, Synced: 3}, 640*time.Millisecond)
		},
		"run-started-one": func(p *output.Printer) error {
			return p.Started(&output.Started{Run: "r1", Pipeline: pipelineDefault, Tree: "abcdef1", Steps: []string{stepTest}, Synced: 1}, 12*time.Second)
		},
		"summary-failed": func(p *output.Printer) error {
			return p.Summary(&output.Summary{
				Run: "r42", Pipeline: pipelineVerify, Tree: shortTree, State: output.StateDone, Exit: exit(1), Seconds: 41 * 60,
				Steps: []output.SummaryStep{
					{Name: "install", State: output.StateSkip, Detail: "pnpm-lock.yaml unchanged"},
					{Name: "unit", State: output.StateOK, Seconds: 190},
					{Name: stepE2E, State: output.StateFail, Seconds: 1320, Tail: []string{"  1 failed", "    tests/site.spec.ts:252 cohorts are listed"}, More: "burro log r42 --step e2e"},
					{Name: stepLighthouse, State: output.StateNotRun},
				},
			})
		},
		"summary-running": func(p *output.Printer) error {
			return p.Summary(&output.Summary{
				Run: "r43", Pipeline: pipelineVerify, Tree: shortTree, State: output.StateRunning, Seconds: 30, Local: "a1b2c3d",
				Steps: []output.SummaryStep{
					{Name: "install", State: output.StateNotRun, Detail: "--from e2e"},
					{Name: stepE2E, State: output.StateRunning, Seconds: 724},
					{Name: stepLighthouse, State: output.StatePending},
				},
			})
		},
		"summary-died": func(p *output.Printer) error {
			return p.Summary(&output.Summary{
				Run: "r7", Pipeline: pipelineDefault, Tree: shortTree, State: output.StateDone, Exit: exit(2), Seconds: 75, Reason: "runner died",
				Steps: []output.SummaryStep{{Name: stepTest, State: output.StateFail, Seconds: 70, Detail: "runner died", More: "burro log r7 --step test"}},
			})
		},
		"summary-cancel": func(p *output.Printer) error {
			return p.Summary(&output.Summary{
				Run: "r8", Pipeline: pipelineDefault, Tree: shortTree, State: output.StateCanceled, Exit: exit(1), Seconds: 12, Reason: output.StateCanceled,
				Steps: []output.SummaryStep{{Name: stepTest, State: output.StateFail, Seconds: 11, Detail: output.StateCanceled}, {Name: "lint", State: output.StateNotRun}},
			})
		},
		"log-tail": func(p *output.Printer) error {
			return p.Log(&output.Log{Run: "r2", Step: stepE2E, Lines: []string{"a", "", "b"}, Hidden: 940, More: "burro log r2 --step e2e --tail 120"})
		},
		"log-tail-all": func(p *output.Printer) error {
			return p.Log(&output.Log{Run: "r2", Step: stepE2E, Lines: []string{"only line"}})
		},
		"log-grep": func(p *output.Printer) error {
			return p.Log(&output.Log{Run: "r2", Step: stepE2E, Grep: grepCohorts, Lines: []string{"x", grepCohorts, "--", grepCohorts}, Matches: 3, Hidden: 1, More: "burro log r2 --step e2e --grep cohorts --tail 120"})
		},
		"log-grep-one": func(p *output.Printer) error {
			return p.Log(&output.Log{Run: "r2", Step: stepE2E, Grep: "x", Lines: []string{"x"}, Matches: 1})
		},
	}
}

func TestRunGolden(t *testing.T) {
	m := modes(t)
	for name, result := range runResults() {
		texts := map[string]string{}
		for modeName, md := range m {
			var b strings.Builder
			if err := result(output.NewPrinter(&b, false, md)); err != nil {
				t.Fatal(err)
			}
			texts[modeName] = b.String()
			testutil.Golden(t, name+"."+modeName+".txt", b.String())
		}
		if testutil.HasANSI(texts["agent"]) || testutil.StripANSI(texts["human"]) != texts["agent"] {
			t.Errorf("%s: modes differ beyond color:\n%q\n%q", name, texts["human"], texts["agent"])
		}
		var b strings.Builder
		if err := result(output.NewPrinter(&b, true, m["human"])); err != nil {
			t.Fatal(err)
		}
		testutil.Golden(t, name+".json", b.String())
	}
}

func TestSummaryColors(t *testing.T) {
	var b strings.Builder
	human, err := mode.New("human", func(string) string { return "" }, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := runResults()["summary-failed"](output.NewPrinter(&b, false, human)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\x1b[32mok\x1b[0m  ", "\x1b[31mFAIL\x1b[0m", "\x1b[90mskip\x1b[0m"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("no %q in %q", want, b.String())
		}
	}
}

func TestProgressLine(t *testing.T) {
	got := output.Progress{Step: stepE2E, Index: 3, Total: 4, Seconds: 724}.Line()
	if want := "e2e · step 3 of 4 · 12m04s · Ctrl+C is safe, the run keeps going"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRunWriteError(t *testing.T) {
	for name, result := range runResults() {
		for _, asJSON := range []bool{false, true} {
			if err := result(output.NewPrinter(brokenWriter{}, asJSON, modes(t)["agent"])); err == nil {
				t.Errorf("%s json=%v: write error lost", name, asJSON)
			}
		}
	}
}
