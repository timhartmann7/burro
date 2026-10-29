package output_test

import (
	"encoding/json"
	"fmt"
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
	run42           = "r42"
	stepInstall     = "install"
)

func exit(n int) *int { return &n }

// runResults are the results of the S2 commands, by golden file name.
func runResults() map[string]func(p *output.Printer) error {
	return map[string]func(p *output.Printer) error{
		"run-started": func(p *output.Printer) error {
			return p.Started(&output.Started{Run: run42, Pipeline: pipelineVerify, Tree: shortTree, Steps: []string{stepE2E, stepLighthouse}, Synced: 3}, 640*time.Millisecond)
		},
		"run-started-one": func(p *output.Printer) error {
			return p.Started(&output.Started{Run: "r1", Pipeline: pipelineDefault, Tree: "abcdef1", Steps: []string{stepTest}, Synced: 1}, 12*time.Second)
		},
		"summary-failed": func(p *output.Printer) error {
			return p.Summary(&output.Summary{
				Run: run42, Pipeline: pipelineVerify, Tree: shortTree, State: output.StateDone, Exit: exit(1), Seconds: 41 * 60,
				Steps: []output.SummaryStep{
					{Name: stepInstall, State: output.StateSkip, Detail: "pnpm-lock.yaml unchanged"},
					{Name: "unit", State: output.StateOK, Seconds: 190},
					{Name: stepE2E, State: output.StateFail, Seconds: 1320, Tail: []string{"  1 failed", "    tests/site.spec.ts:252 cohorts are listed"}, More: "burro log r42 --step e2e"},
					{Name: stepLighthouse, State: output.StateNotRun},
				},
			}, 0)
		},
		"summary-running": func(p *output.Printer) error {
			return p.Summary(&output.Summary{
				Run: "r43", Pipeline: pipelineVerify, Tree: shortTree, State: output.StateRunning, Seconds: 30, Local: "a1b2c3d",
				Steps: []output.SummaryStep{
					{Name: stepInstall, State: output.StateNotRun, Detail: "--from e2e"},
					{Name: stepE2E, State: output.StateRunning, Seconds: 724},
					{Name: stepLighthouse, State: output.StatePending},
				},
			}, 0)
		},
		"summary-died": func(p *output.Printer) error {
			return p.Summary(&output.Summary{
				Run: "r7", Pipeline: pipelineDefault, Tree: shortTree, State: output.StateDone, Exit: exit(2), Seconds: 75, Reason: "runner died",
				Steps: []output.SummaryStep{{Name: stepTest, State: output.StateFail, Seconds: 70, Detail: "runner died", More: "burro log r7 --step test"}},
			}, 0)
		},
		"summary-cancel": func(p *output.Printer) error {
			return p.Summary(&output.Summary{
				Run: "r8", Pipeline: pipelineDefault, Tree: shortTree, State: output.StateCanceled, Exit: exit(1), Seconds: 12, Reason: output.StateCanceled,
				Steps: []output.SummaryStep{{Name: stepTest, State: output.StateFail, Seconds: 11, Detail: output.StateCanceled}, {Name: "lint", State: output.StateNotRun}},
			}, 0)
		},
		"summary-junit": func(p *output.Printer) error {
			return p.Summary(junitSummary(), 40)
		},
		"summary-cut": func(p *output.Printer) error {
			return p.Summary(manyFailures(300), 40)
		},
		"cleaned": func(p *output.Printer) error {
			return p.Cleaned(&output.Cleaned{Run: run42, Containers: 2, Networks: 1, Volumes: 1, Secrets: true})
		},
		"cleaned-nothing": func(p *output.Printer) error {
			return p.Cleaned(&output.Cleaned{Run: "r3"})
		},
		"init": func(p *output.Printer) error {
			return p.Init([]output.InitFile{
				{Name: "burro.toml", Change: output.Created},
				{Name: "CLAUDE.md", Change: output.Updated, Detail: "rules for your agent"},
				{Name: ".gitignore", Change: output.Updated, Detail: ".burro/"},
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

// junitSummary is the summary of docs/reference/cli.md: a step with test
// counts, failures from its report, and a step whose report was broken.
func junitSummary() *output.Summary {
	return &output.Summary{
		Run: run42, Pipeline: pipelineVerify, Tree: shortTree, State: output.StateDone, Exit: exit(1), Seconds: 41 * 60,
		Steps: []output.SummaryStep{
			{Name: stepInstall, State: output.StateSkip, Detail: "pnpm-lock.yaml unchanged"},
			{Name: "unit", State: output.StateOK, Seconds: 190, Tests: &output.Tests{Total: 2081, Passed: 2081}},
			{Name: stepE2E, State: output.StateFail, Seconds: 1320, Tests: &output.Tests{Total: 155, Passed: 152, Failed: 3}},
			{Name: "api", State: output.StateFail, Seconds: 30, Unreadable: true, Tail: []string{"Segmentation fault"}, More: "burro log r42 --step api"},
			{Name: stepLighthouse, State: output.StateOK, Seconds: 14 * 60},
		},
		Failures: []output.Failure{
			{
				Step: stepE2E, Kind: output.KindNew, File: "tests/site.spec.ts", Line: 252, Name: "cohorts are listed", Count: 3,
				Message: "strict mode violation: locator('#school [data-cohort]') resolved to 2 elements", Frame: "tests/site.spec.ts:257",
			},
			{Step: stepE2E, Kind: output.KindSeen, File: "tests/roles.spec.ts", Name: "creates roles", Count: 1, Message: "hook timed out after 120000ms"},
		},
		More: "burro log r42 --step e2e --grep cohorts",
	}
}

// manyFailures is a run with n failed tests in one step.
func manyFailures(n int) *output.Summary {
	s := &output.Summary{
		Run: run42, Pipeline: pipelineDefault, Tree: shortTree, State: output.StateDone, Exit: exit(1), Seconds: 300,
		Steps: []output.SummaryStep{{Name: stepTest, State: output.StateFail, Seconds: 290, Tests: &output.Tests{Total: n, Failed: n}}},
		More:  "burro log r42 --step test --grep case",
	}
	for i := range n {
		s.Failures = append(s.Failures, output.Failure{
			Step: stepTest, Kind: output.KindNew, File: "pkg/a_test.go", Name: fmt.Sprintf("TestCase%03d", i), Count: 1,
			Message: "got 1, want 2", Frame: fmt.Sprintf("pkg/a_test.go:%d", 10+i),
		})
	}
	return s
}

// acceptance: S3-05
func TestSummaryFitsMaxLines(t *testing.T) {
	agent := modes(t)["agent"]
	for _, maxLines := range []int{40, 7, 100, 1000} {
		var b strings.Builder
		if err := output.NewPrinter(&b, false, agent).Summary(manyFailures(300), maxLines); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
		shown := strings.Count(b.String(), "  NEW   ")
		last := lines[len(lines)-1]
		if len(lines) > maxLines || shown == 0 || last != fmt.Sprintf("+%d more: burro failures r42 --max-lines %d", 300-shown, 10*maxLines) && shown < 300 {
			t.Fatalf("max %d: %d lines, %d shown, last %q", maxLines, len(lines), shown, last)
		}
		var j strings.Builder
		if err := output.NewPrinter(&j, true, agent).Summary(manyFailures(300), maxLines); err != nil {
			t.Fatal(err)
		}
		var got output.Summary
		if err := json.Unmarshal([]byte(j.String()), &got); err != nil || len(got.Failures) != shown || got.Hidden != 300-shown {
			t.Fatalf("max %d json: %d failures, %d hidden, %v", maxLines, len(got.Failures), got.Hidden, err)
		}
	}
}

func TestSummaryCutsTails(t *testing.T) {
	tail := make([]string, 30)
	for i := range tail {
		tail[i] = fmt.Sprintf("line %d", i+1)
	}
	s := &output.Summary{
		Run: "r9", Pipeline: pipelineDefault, Tree: shortTree, State: output.StateDone, Exit: exit(1), Seconds: 60,
		Steps: []output.SummaryStep{
			{Name: "a", State: output.StateFail, Seconds: 1, Tail: tail, More: "burro log r9 --step a"},
			{Name: "b", State: output.StateFail, Seconds: 1, Unreadable: true, Tail: tail, More: "burro log r9 --step b"},
			{Name: "c", State: output.StateFail, Seconds: 1, Tail: tail, More: "burro log r9 --step c"},
		},
	}
	var b strings.Builder
	if err := output.NewPrinter(&b, false, modes(t)["agent"]).Summary(s, 40); err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "summary-tails-cut.agent.txt", b.String())
	if lines := strings.Count(b.String(), "\n"); lines != 40 {
		t.Fatalf("%d lines", lines)
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
