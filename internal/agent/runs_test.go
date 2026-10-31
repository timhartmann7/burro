package agent_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/agent"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/report"
	"github.com/timhartmann7/burro/internal/runner"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	argPath   = "--path"
	pipeline  = "default"
	actStatus = "status"
	actWait   = "wait"
	actLog    = "log"
	actCancel = "cancel"
	actStart  = "start"
	argStep   = "--step"
	argTail   = "--tail"
	argCtx    = "--context"
	run1      = "r1"
	stepUnit  = "unit"
	fullTree  = "5530df9c0e1a2b3c4d5e6f708192a3b4c5d6e7f8"
)

// server is one home folder with a project, an executor and the burro
// binary as its runner.
type server struct {
	t       *testing.T
	home    string
	dir     string
	runner  []string
	secrets string
	// stdin is what the next call reads, such as the secrets of a start.
	stdin string
}

func newServer(t *testing.T, bin, toml string) *server {
	t.Helper()
	home, dir := project(t)
	if err := os.WriteFile(filepath.Join(dir, "burro.toml"), []byte("host = \"vps1\"\n"+toml), 0o600); err != nil {
		t.Fatal(err)
	}
	return &server{t: t, home: home, dir: dir, runner: []string{bin, "agent", agent.RunnerCall}, secrets: t.TempDir()}
}

// call runs one agent call and returns every reply line it printed.
func (s *server) call(args ...string) []agent.Reply {
	s.t.Helper()
	var out strings.Builder
	if err := agent.New(strings.NewReader(s.stdin), &out, s.home, s.runner, s.secrets, runner.Exec, runner.Clean).Run(args); err != nil {
		s.t.Fatal(err)
	}
	var replies []agent.Reply
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		var r agent.Reply
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			s.t.Fatalf("reply line %q: %v", line, err)
		}
		replies = append(replies, r)
	}
	return replies
}

// ok runs a call that must print one reply without an error.
func (s *server) ok(args ...string) agent.Reply {
	s.t.Helper()
	replies := s.call(args...)
	last := replies[len(replies)-1]
	if last.Error != nil {
		s.t.Fatalf("%q: %+v", args, last.Error)
	}
	return last
}

// fails runs a call that must fail with code.
func (s *server) fails(code output.Code, args ...string) *agent.Failure {
	s.t.Helper()
	replies := s.call(args...)
	f := replies[len(replies)-1].Error
	if len(replies) != 1 || f == nil || output.Code(f.Code) != code || f.Fix == "" {
		s.t.Fatalf("%q: got %+v, want exit %d", args, f, code)
	}
	return f
}

func (s *server) start(args ...string) *output.Started {
	s.t.Helper()
	return s.ok(append([]string{actStart, argPath, appPath, "--tree", fullTree}, args...)...).Started
}

// done waits for a run to end and returns its summary.
func (s *server) done(id string) *output.Summary {
	s.t.Helper()
	sum := s.ok(actWait, argPath, appPath, "--max", "30s", id).Summary
	if sum.State == output.StateRunning {
		s.t.Fatalf("%s still running", id)
	}
	return sum
}

func (s *server) runs() []string {
	s.t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.dir, ".burro", "runs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

const pipelineTOML = `
[pipelines]
default = ["unit", "e2e", "lint"]
slow = ["nap"]

[steps.unit]
run = "echo unit ok"

[steps.e2e]
run = '''for i in $(seq 1 40); do printf '\033[31mline %%d\033[0m\n' $i; done; echo cohorts are listed; exit 1'''
continue_on_fail = true

[steps.lint]
run = "true"

[steps.nap]
run = "echo napping; sleep %s"

[filters]
drop = ['^line 3\d$']
`

func toml(nap string) string { return fmt.Sprintf(pipelineTOML, nap) }

func TestStartAndWait(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("30"))
	started := s.start("--from", "e2e", pipeline)
	if started.Run != run1 || started.Tree != fullTree[:7] || strings.Join(started.Steps, " ") != "e2e lint" || started.Pipeline != pipeline {
		t.Fatalf("started %+v", started)
	}
	sum := s.done("")
	if sum.Run != run1 || *sum.Exit != 1 || sum.Tree != fullTree[:7] || len(sum.Steps) != 3 {
		t.Fatalf("summary %+v", sum)
	}
	unit, lint := sum.Steps[0], sum.Steps[2]
	if unit.State != output.StateNotRun || unit.Detail != "--from e2e" || lint.State != output.StateOK {
		t.Fatalf("steps %+v %+v", unit, lint)
	}
	// The tail is clean and leaves out the lines filters.drop removes.
	if e2e := sum.Steps[1]; len(e2e.Tail) != 30 || e2e.Tail[0] != "line 2" || e2e.Tail[8] != "line 10" ||
		e2e.Tail[29] != "cohorts are listed" || e2e.More != "burro log r1 --step e2e" {
		t.Fatalf("e2e %+v", e2e)
	}
	status := s.ok(actStatus, argPath, appPath)
	if status.Tree != fullTree || status.Summary.Steps[1].Tail != nil || *status.Summary.Exit != 1 {
		t.Fatalf("status %+v", status)
	}
	if entries, _ := os.ReadDir(s.secrets); len(entries) != 0 {
		t.Fatalf("secrets folders kept: %v", entries)
	}
}

func TestStartWithArgs(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("30"))
	// Arguments that look like flags or a second pipeline stay the step's.
	args := []string{"--grep", "a b", "slow", "--"}
	started := s.start("--only", stepUnit, pipeline, args[0], args[1], args[2], args[3])
	if strings.Join(started.Steps, " ") != stepUnit || strings.Join(started.Args, "|") != strings.Join(args, "|") {
		t.Fatalf("started %+v", started)
	}
	s.done(run1)
	var status report.Status
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(s.dir, ".burro", "runs", run1, "status.json"))), &status); err != nil ||
		strings.Join(status.Args, "|") != strings.Join(args, "|") {
		t.Fatalf("status.json args %q, %v", status.Args, err)
	}
	if log := readFile(t, filepath.Join(s.dir, ".burro", "runs", run1, "unit.log")); log != "unit ok --grep a b slow --\n" {
		t.Fatalf("unit.log %q", log)
	}
	// The client checks this too; an older or other client must not get
	// past the server either.
	f := s.fails(output.ExitUsage, actStart, argPath, appPath, "--tree", fullTree, pipeline, "x")
	if !strings.Contains(f.Message, "go to one step") || len(s.runs()) != 1 {
		t.Fatalf("args for three steps: %+v, runs %v", f, s.runs())
	}
}

func TestStartNearTimeout(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("30"))
	proj := report.Open(s.dir)
	// Earlier runs: unit took 50 of its 60 minutes in r2, 59 in a run with
	// arguments after it, and failed in the newest.
	for _, prev := range []struct {
		state   string
		seconds int
		args    []string
	}{
		{output.StateOK, 3000, nil},
		{output.StateOK, 2000, nil},
		{output.StateOK, 3540, []string{"a.spec.ts"}},
		{output.StateFail, 3599, nil},
	} {
		id, err := proj.NewRun()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		st := &report.Status{
			Run: id, Pipeline: pipeline, State: output.StateDone, Exit: new(int), Started: now, Finished: &now, Args: prev.args,
			Steps: []report.Step{{Name: stepUnit, State: prev.state, Seconds: prev.seconds}, {Name: "lint", State: output.StateOK, Seconds: 1}},
		}
		if err := proj.WriteStatus(st); err != nil {
			t.Fatal(err)
		}
	}
	started := s.start(pipeline)
	s.done(started.Run)
	if len(started.Near) != 0 {
		t.Fatalf("2000 of 3600 seconds is no risk: %+v", started.Near)
	}
	// r5 passed unit fast; take its place with a slow pass.
	st, err := proj.Status(started.Run)
	if err != nil {
		t.Fatal(err)
	}
	st.Steps[0].Seconds = 2880
	if err := proj.WriteStatus(st); err != nil {
		t.Fatal(err)
	}
	started = s.start(pipeline)
	s.done(started.Run)
	want := []output.NearTimeout{{Step: stepUnit, Run: "r5", Seconds: 2880, Timeout: 3600}}
	if !slices.Equal(started.Near, want) {
		t.Fatalf("near %+v, want %+v", started.Near, want)
	}
}

func TestBusyAndCancel(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("300"))
	started := s.start("slow")
	if f := s.fails(output.ExitBusy, actStart, argPath, appPath, "--tree", fullTree, pipeline); !strings.Contains(f.Message, "run r1 is active") {
		t.Fatalf("busy: %+v", f)
	}
	s.fails(output.ExitBusy, "prepare", argPath, appPath)
	if got := s.runs(); strings.Join(got, " ") != run1 {
		t.Fatalf("runs %v", got)
	}
	replies := s.call(actWait, argPath, appPath, "--max", "2s", "--progress", started.Run)
	if first, last := replies[0], replies[len(replies)-1]; len(replies) < 2 || first.Progress == nil || first.Progress.Step != "nap" ||
		first.Progress.Total != 1 || last.Summary == nil || last.Summary.State != output.StateRunning {
		t.Fatalf("wait --progress: %+v", replies)
	}
	if got := s.ok(actLog, argPath, appPath, argStep, "nap", argTail, "5", argCtx, "0").Log; strings.Join(got.Lines, "|") != "napping" {
		t.Fatalf("log of a running step: %+v", got)
	}
	start := time.Now()
	sum := s.ok(actCancel, argPath, appPath).Summary
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("cancel took %s", took)
	}
	if sum.State != output.StateCanceled || *sum.Exit != 1 || sum.Steps[0].State != output.StateFail || sum.Steps[0].Detail != output.StateCanceled {
		t.Fatalf("cancel %+v", sum)
	}
	// Cancel of a finished run only reports it.
	if again := s.ok(actCancel, argPath, appPath, run1).Summary; again.State != output.StateCanceled {
		t.Fatalf("second cancel %+v", again)
	}
	s.ok("prepare", argPath, appPath)
	if next := s.start("slow"); next.Run != "r2" {
		t.Fatalf("after cancel: %+v", next)
	}
	s.ok(actCancel, argPath, appPath)
}

func TestLogCall(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("0"))
	s.start(pipeline)
	s.done(run1)
	tail := s.ok(actLog, argPath, appPath, argStep, "e2e", argTail, "3", argCtx, "5", run1).Log
	if strings.Join(tail.Lines, "|") != "line 29|line 40|cohorts are listed" || tail.Hidden != 28 || tail.Grep != "" {
		t.Fatalf("tail %+v", tail)
	}
	grep := s.ok(actLog, argPath, appPath, argStep, "e2e", argTail, "60", argCtx, "1", "--grep", "line [12]$|cohorts").Log
	if strings.Join(grep.Lines, "|") != "line 1|line 2|line 3|--|line 40|cohorts are listed" || grep.Matches != 3 || grep.Hidden != 0 {
		t.Fatalf("grep %+v", grep)
	}
	s.start("--only", "unit", pipeline)
	s.done("r2")
	if got := s.ok(actLog, argPath, appPath, argStep, "lint", argTail, "5", argCtx, "0").Log; len(got.Lines) != 0 || got.Hidden != 0 || got.Run != "r2" {
		t.Fatalf("a step that didn't run: %+v", got)
	}
}

func TestBadRunCalls(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("0"))
	s.fails(output.ExitUsage, actStatus, argPath, appPath)
	s.fails(output.ExitUsage, actStart, argPath, appPath, "--tree", fullTree, "nope")
	s.fails(output.ExitUsage, actStart, argPath, appPath, "--tree", fullTree, "--from", "nap", pipeline)
	s.fails(output.ExitUsage, actStart, argPath, appPath, "--tree", fullTree, "--only", "unit,nope", pipeline)
	if got := s.runs(); len(got) != 0 {
		t.Fatalf("a refused start left runs %v", got)
	}
	s.fails(output.ExitServer, actStart, argPath, "burro/none", "--tree", fullTree, pipeline)
	s.start("--only", "unit", pipeline)
	s.done(run1)
	for _, args := range [][]string{
		{actStatus, argPath, appPath, "r9"},
		{actStatus, argPath, appPath, "../r1"},
		{actStatus, appPath},
		{actStatus, argPath, appPath, run1, "r2"},
		{actLog, argPath, appPath, argStep, "unit", argTail, "0", argCtx, "0"},
		{actLog, argPath, appPath, argStep, "unit", argTail, "5", argCtx, "0", "--grep", "("},
		{actWait, argPath, appPath, "--max", "soon"},
	} {
		s.fails(output.ExitUsage, args...)
	}
	f := s.fails(output.ExitUsage, actLog, argPath, appPath, argStep, "nope", argTail, "5", argCtx, "0")
	if f.Fix != "use one of: unit, e2e, lint" {
		t.Fatalf("unknown step fix %q", f.Fix)
	}
}

func TestRunnerThatCannotStart(t *testing.T) {
	bin := testutil.BuildBurro(t)
	s := newServer(t, bin, toml("0"))
	s.runner = []string{filepath.Join(t.TempDir(), "missing")}
	s.fails(output.ExitServer, actStart, argPath, appPath, "--tree", fullTree, pipeline)
	sum := s.ok(actStatus, argPath, appPath).Summary
	if sum.State != output.StateDone || *sum.Exit != 2 || !strings.HasPrefix(sum.Reason, "runner did not start") {
		t.Fatalf("summary %+v", sum)
	}
	// Its lock never had a live runner, so the next run may start.
	s.runner = []string{bin, "agent", agent.RunnerCall}
	if next := s.start(pipeline); next.Run != "r2" {
		t.Fatalf("next %+v", next)
	}
	s.done("r2")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
