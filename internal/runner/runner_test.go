package runner

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/report"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	stepA   = "a"
	stepB   = "b"
	stepC   = "c"
	slow    = "slow"
	flagP   = "--path"
	flagRun = "--run"
	tree    = "5530df9c0e"
	ok      = output.StateOK
	failed  = output.StateFail
	notRun  = output.StateNotRun
)

// project is a project folder with burro.toml and a run prepared the way
// the executor leaves it.
type project struct {
	t    *testing.T
	dir  string
	proj *report.Project
}

func newProject(t *testing.T, toml string) *project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "burro.toml"), []byte("host = \"vps1\"\n"+toml), 0o600); err != nil {
		t.Fatal(err)
	}
	return &project{t: t, dir: dir, proj: report.Open(dir)}
}

// prepare creates the next run with steps pending, except those named in
// skip, and takes the lock for it.
func (p *project) prepare(steps []string, skip map[string]string) request {
	p.t.Helper()
	id, err := p.proj.NewRun()
	if err != nil {
		p.t.Fatal(err)
	}
	now := report.Stamp(time.Now())
	s := &report.Status{Run: id, Pipeline: "default", Tree: tree, State: output.StateRunning, Started: now, Heartbeat: now}
	for _, name := range steps {
		st := report.Step{Name: name, State: output.StatePending}
		if why, ok := skip[name]; ok {
			st.State, st.Detail = notRun, why
		}
		s.Steps = append(s.Steps, st)
	}
	if err := p.proj.WriteStatus(s); err != nil {
		p.t.Fatal(err)
	}
	secrets := filepath.Join(p.t.TempDir(), p.proj.Key(id))
	if err := os.Mkdir(secrets, 0o700); err != nil {
		p.t.Fatal(err)
	}
	if err := p.proj.Locked(func() error { return p.proj.Take(id) }); err != nil {
		p.t.Fatal(err)
	}
	if err := p.proj.WritePid(id, os.Getpid()); err != nil {
		p.t.Fatal(err)
	}
	return request{dir: p.dir, id: id, key: p.proj.Key(id), secrets: secrets}
}

func testRunner() *Runner {
	r := New(os.Environ())
	r.beat, r.grace = 100*time.Millisecond, time.Second
	return r
}

// run runs the prepared run to its end and returns its status.
func (p *project) run(ctx context.Context, r *Runner, req request) *report.Status {
	p.t.Helper()
	if err := r.run(ctx, req); err != nil {
		p.t.Fatal(err)
	}
	s, err := p.proj.Status(req.id)
	if err != nil {
		p.t.Fatal(err)
	}
	return s
}

func (p *project) log(id, step string) string {
	p.t.Helper()
	data, err := os.ReadFile(p.proj.LogFile(id, step))
	if err != nil {
		p.t.Fatal(err)
	}
	return string(data)
}

func (p *project) locked() bool {
	p.t.Helper()
	held := false
	if err := p.proj.Locked(func() error { held = p.proj.Holder() != ""; return nil }); err != nil {
		p.t.Fatal(err)
	}
	return held
}

// states is "name:state" for every step.
func states(s *report.Status) string {
	parts := make([]string, 0, len(s.Steps))
	for _, st := range s.Steps {
		parts = append(parts, st.Name+":"+st.State)
	}
	return strings.Join(parts, " ")
}

const abc = `
[pipelines]
default = ["a", "b", "c"]

[steps.a]
run = "echo a"

[steps.b]
run = "echo b out; echo b err >&2; exit 3"
%s

[steps.c]
run = "echo c"
`

// acceptance: S2-04
func TestFailedStepStopsPipeline(t *testing.T) {
	p := newProject(t, fmt.Sprintf(abc, ""))
	s := p.run(context.Background(), testRunner(), p.prepare([]string{stepA, stepB, stepC}, nil))
	if states(s) != "a:ok b:fail c:not run" || *s.Exit != 1 || s.State != output.StateDone || s.Finished == nil {
		t.Fatalf("got %s exit %v state %s", states(s), s.Exit, s.State)
	}
	if *s.Steps[1].Exit != 3 || s.Steps[2].Exit != nil {
		t.Fatalf("step exits %v %v", *s.Steps[1].Exit, s.Steps[2].Exit)
	}
	if got := p.log(s.Run, stepB); got != "b out\nb err\n" {
		t.Fatalf("log of b %q", got)
	}
	if _, err := os.Stat(p.proj.LogFile(s.Run, stepC)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a step that didn't run has a log")
	}
	if p.locked() {
		t.Fatal("lock kept after the run")
	}
}

// acceptance: S2-04
func TestContinueOnFail(t *testing.T) {
	p := newProject(t, fmt.Sprintf(abc, "continue_on_fail = true"))
	s := p.run(context.Background(), testRunner(), p.prepare([]string{stepA, stepB, stepC}, nil))
	if states(s) != "a:ok b:fail c:ok" || *s.Exit != 1 {
		t.Fatalf("got %s exit %d", states(s), *s.Exit)
	}
	s = p.run(context.Background(), testRunner(), p.prepare([]string{stepA, stepB, stepC}, map[string]string{stepB: "--only a,c"}))
	if states(s) != "a:ok b:not run c:ok" || *s.Exit != 0 || s.Steps[1].Detail != "--only a,c" {
		t.Fatalf("passing run: %s exit %d", states(s), *s.Exit)
	}
}

// write puts content into the project file name.
func (p *project) write(name, content string) {
	p.t.Helper()
	path := filepath.Join(p.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		p.t.Fatal(err)
	}
}

// link replaces the project file name with a symlink to a file outside
// the project.
func (p *project) link(name string) {
	p.t.Helper()
	outside := filepath.Join(p.t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("v2"), 0o600); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p.dir, name)); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.dir, name)); err != nil {
		p.t.Fatal(err)
	}
}

func (p *project) remove(name string) {
	p.t.Helper()
	if err := os.Remove(filepath.Join(p.dir, filepath.FromSlash(name))); err != nil {
		p.t.Fatal(err)
	}
}

const (
	lockFile  = "lock.txt"
	otherFile = "sub/other.txt"
)

// acceptance: S2-08
func TestSkipIfUnchanged(t *testing.T) {
	p := newProject(t, `
[pipelines]
default = ["install"]

[steps.install]
run = "cat lock.txt >> runs.txt; test \"$(cat lock.txt)\" != broken"
skip_if_unchanged = ["lock.txt", "sub/other.txt"]
`)
	p.write(otherFile, "x")
	skip := output.StateSkip
	runs := []struct {
		name   string
		before func()
		want   string
	}{
		{name: "first run", before: func() { p.write(lockFile, "v1") }, want: ok},
		{name: "nothing changed", before: func() {}, want: skip},
		{name: "lockfile changed", before: func() { p.write(lockFile, "v2") }, want: ok},
		{name: "broken lockfile", before: func() { p.write(lockFile, "broken") }, want: failed},
		// A failed run must not enable skipping.
		{name: "broken lockfile again", before: func() {}, want: failed},
		// Nor may it drop the cache of the last good run.
		{name: "back to the good lockfile", before: func() { p.write(lockFile, "v2") }, want: skip},
		{name: "a listed file is gone", before: func() { p.remove(otherFile) }, want: ok},
		{name: "symlink out of the project", before: func() { p.write(otherFile, "x"); p.link(lockFile) }, want: ok},
		{name: "broken cache", before: func() { p.write(".burro/cache/install.json", "{") }, want: ok},
	}
	for _, r := range runs {
		r.before()
		got := p.run(context.Background(), testRunner(), p.prepare([]string{"install"}, nil)).Steps[0]
		if got.State != r.want {
			t.Fatalf("%s: %+v, want %s", r.name, got, r.want)
		}
	}
	if s, _ := p.proj.Status("r2"); s.Steps[0].Detail != "lock.txt, sub/other.txt unchanged" {
		t.Fatalf("skip detail %q", s.Steps[0].Detail)
	}
	if got, _ := os.ReadFile(filepath.Join(p.dir, "runs.txt")); string(got) != "v1v2brokenbrokenv2v2v2" {
		t.Fatalf("the step ran with %q", got)
	}
}

// marker is a sleep argument no other process on the machine uses.
func marker(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("300.%06d", binary.BigEndian.Uint32(b)%1e6)
}

// leftovers lists the processes still running `sleep <mark>`.
func leftovers(t *testing.T, mark string) error {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "pgrep", "-f", "sleep "+mark).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil
	}
	if err != nil {
		t.Fatalf("pgrep: %v", err)
	}
	return fmt.Errorf("still running: %s", strings.Fields(string(out)))
}

// acceptance: S2-06
func TestTimeoutKillsChildren(t *testing.T) {
	mark := marker(t)
	p := newProject(t, fmt.Sprintf(`
[pipelines]
default = ["slow", "after"]

[steps.slow]
run = "sleep %[1]s & nohup sleep %[1]s >/dev/null 2>&1 & (trap '' TERM; sleep %[1]s) & echo started; sleep %[1]s"
timeout = "1s"

[steps.after]
run = "true"
`, mark))
	start := time.Now()
	s := p.run(context.Background(), testRunner(), p.prepare([]string{slow, "after"}, nil))
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("timeout took %s", took)
	}
	if states(s) != "slow:fail after:not run" || s.Steps[0].Detail != "timed out after 1.0s" || *s.Steps[0].Exit != 143 {
		t.Fatalf("got %s %+v", states(s), s.Steps[0])
	}
	testutil.Poll(t, 5*time.Second, func() error { return leftovers(t, mark) })
	if got := p.log(s.Run, slow); got != "started\n" {
		t.Fatalf("log %q", got)
	}
}

func TestCancel(t *testing.T) {
	mark := marker(t)
	p := newProject(t, fmt.Sprintf(`
[pipelines]
default = ["a", "slow", "c"]

[steps.a]
run = "true"

[steps.slow]
run = "sleep %[1]s & echo running; sleep %[1]s"

[steps.c]
run = "true"
`, mark))
	req := p.prepare([]string{stepA, slow, stepC}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		testutil.Poll(t, 10*time.Second, func() error {
			if data, _ := os.ReadFile(p.proj.LogFile(req.id, slow)); string(data) != "running\n" {
				return errors.New("slow step not started")
			}
			return nil
		})
		cancel()
	}()
	s := p.run(ctx, testRunner(), req)
	if states(s) != "a:ok slow:fail c:not run" || s.Steps[1].Detail != output.StateCanceled || s.State != output.StateCanceled ||
		*s.Exit != 1 || s.Reason != output.StateCanceled {
		t.Fatalf("got %s %+v", states(s), s)
	}
	testutil.Poll(t, 5*time.Second, func() error { return leftovers(t, mark) })
	if p.locked() {
		t.Fatal("lock kept after cancel")
	}
	if _, err := os.Stat(req.secrets); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("secrets folder kept")
	}
}

func TestStepEnvironment(t *testing.T) {
	p := newProject(t, `
[pipelines]
default = ["env"]

[steps.env]
run = "echo $BURRO_RUN_ID $BURRO_STEP $BURRO_TREE; test \"$BURRO_RUN\" = \"$COMPOSE_PROJECT_NAME\" && test -d \"$BURRO_SECRETS\" && echo \"$A|$B|$C|$OUTER\"; pwd"
env = { A = "${BURRO_RUN}/x", B = "$BURRO_STEP-$HOME", C = "$BURRO_NOPE" }
`)
	r := testRunner()
	r.env = append(r.env, "OUTER=from the executor")
	req := p.prepare([]string{"env"}, nil)
	s := p.run(context.Background(), r, req)
	if s.Steps[0].State != ok {
		t.Fatalf("got %+v: %s", s.Steps[0], p.log(req.id, "env"))
	}
	dir, err := filepath.EvalSymlinks(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("r1 env %s\n%s/x|env-$HOME|$BURRO_NOPE|from the executor\n%s\n", tree, req.key, dir)
	if got := p.log(req.id, "env"); got != want {
		t.Fatalf("log\n%q\nwant\n%q", got, want)
	}
}

func TestHeartbeat(t *testing.T) {
	p := newProject(t, "[pipelines]\ndefault = [\"slow\"]\n[steps.slow]\nrun = \"sleep 2.5\"\n")
	req := p.prepare([]string{slow}, nil)
	done := make(chan *report.Status, 1)
	go func() { done <- p.run(context.Background(), testRunner(), req) }()
	testutil.Poll(t, 10*time.Second, func() error {
		s, err := p.proj.Status(req.id)
		if err != nil {
			return err
		}
		if s.Steps[0].State != output.StateRunning || s.Steps[0].Seconds < 1 || !s.Heartbeat.After(s.Started) {
			return fmt.Errorf("no heartbeat yet: %+v", s)
		}
		return nil
	})
	if s := <-done; s.Steps[0].State != ok || s.Steps[0].Seconds < 2 {
		t.Fatalf("got %+v", s.Steps[0])
	}
}

func TestRunnerFailures(t *testing.T) {
	r := New(nil)
	for _, args := range [][]string{nil, {flagP, "/x", flagRun, "r1"}, {flagP, "/x", flagRun, "bad", "--key", "k", "--secrets", "s"}, {"--nope"}} {
		var coded *output.Error
		if err := r.Run(args); !errors.As(err, &coded) || coded.Code() != output.ExitUsage {
			t.Errorf("Run(%q) = %v", args, err)
		}
	}
	// No status.json: nothing to run and nowhere to report.
	if err := r.Run([]string{flagP, t.TempDir(), flagRun, "r1", "--key", "k", "--secrets", t.TempDir()}); err == nil {
		t.Fatal("a run without status.json passed")
	}

	// A broken burro.toml ends the run as a server problem.
	p := newProject(t, "[pipelines]\ndefault = [\"a\"]\n[steps.a]\nrun = \"true\"\n")
	req := p.prepare([]string{stepA, "gone"}, nil)
	if err := os.WriteFile(filepath.Join(p.dir, "burro.toml"), []byte("host = 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.run(context.Background(), req); err == nil {
		t.Fatal("broken burro.toml passed")
	}
	if s, _ := p.proj.Status(req.id); *s.Exit != 2 || !strings.Contains(s.Reason, "host must be a string") || states(s) != "a:not run gone:not run" {
		t.Fatalf("got %+v", s)
	}

	// A step that burro.toml lost since the run started fails.
	p = newProject(t, "[pipelines]\ndefault = [\"a\"]\n[steps.a]\nrun = \"true\"\n")
	s := p.run(context.Background(), testRunner(), p.prepare([]string{stepA, "gone", stepC}, nil))
	if states(s) != "a:ok gone:fail c:not run" || s.Steps[1].Detail != "not in burro.toml" {
		t.Fatalf("got %s %+v", states(s), s.Steps[1])
	}
}

func TestExec(t *testing.T) {
	var out strings.Builder
	code, err := Exec(context.Background(), t.TempDir(), `echo "$1"; exit 4`, []string{"a b"}, &out)
	if err != nil || code != 4 || out.String() != "a b\n" {
		t.Fatalf("code %d out %q err %v", code, out.String(), err)
	}
	if _, err := Exec(context.Background(), "/no/such/dir", "true", nil, &out); err == nil {
		t.Fatal("a missing folder passed")
	}
}

const secretValue = "hunter2-correct-horse"

// withSecret writes the secret token of req, the way the executor does.
func withSecret(t *testing.T, req request) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(req.secrets, "token"), []byte(secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acceptance: S3-09 S3-10
func TestSecretsMaskedAndRemoved(t *testing.T) {
	p := newProject(t, `
[pipelines]
default = ["leak", "after"]

[steps.leak]
run = '''cat "$BURRO_SECRETS/token"; v=$(cat "$BURRO_SECRETS/token"); for i in $(seq 1 2000); do printf 'line %d\n' $i; done; printf '%s' "$v" | fold -w 3; echo; echo "[$v]" >&2'''

[steps.after]
run = "ls \"$BURRO_SECRETS\""
`)
	req := p.prepare([]string{"leak", "after"}, nil)
	withSecret(t, req)
	s := p.run(context.Background(), testRunner(), req)
	if states(s) != "leak:ok after:ok" {
		t.Fatalf("got %s", states(s))
	}
	leak := p.log(req.id, "leak")
	if strings.Contains(leak, secretValue) || !strings.HasPrefix(leak, report.Mask+"\nline 1\n") || !strings.HasSuffix(leak, "["+report.Mask+"]\n") {
		t.Fatalf("leak log starts %q ends %q", leak[:40], leak[len(leak)-60:])
	}
	if got := p.log(req.id, "after"); got != "token\n" {
		t.Fatalf("after log %q", got)
	}
	if _, err := os.Stat(req.secrets); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secrets folder left: %v", err)
	}
}

// A process the step left running keeps the masked pipe open; the run ends
// anyway.
func TestLeftoverHoldsMaskedOutput(t *testing.T) {
	mark := marker(t)
	p := newProject(t, fmt.Sprintf("[pipelines]\ndefault = [\"a\"]\n[steps.a]\nrun = \"(sleep %s; echo late) & echo early\"\n", mark))
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "pkill", "-f", "sleep "+mark).Run() })
	req := p.prepare([]string{stepA}, nil)
	withSecret(t, req)
	start := time.Now()
	s := p.run(context.Background(), testRunner(), req)
	if took := time.Since(start); took > 10*time.Second || states(s) != "a:ok" || p.log(req.id, stepA) != "early\n" {
		t.Fatalf("took %s: %s %q", took, states(s), p.log(req.id, stepA))
	}
}

func TestReportsCopied(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "junit", "gotestsum.xml"))
	if err != nil {
		t.Fatal(err)
	}
	p := newProject(t, fmt.Sprintf(`
[pipelines]
default = ["unit", "plain"]

[steps.unit]
run = 'mkdir -p .burro/out && cp "%s" .burro/out/unit.xml && exit 1'
junit = ".burro/out/*.xml"
continue_on_fail = true

[steps.plain]
run = "true"
`, fixture))
	req := p.prepare([]string{"unit", "plain"}, nil)
	p.run(context.Background(), testRunner(), req)
	copies, err := filepath.Glob(filepath.Join(p.proj.RunDir(req.id), "junit", "*", "*"))
	if err != nil || len(copies) != 1 || !strings.HasSuffix(copies[0], "/junit/unit/1.xml") {
		t.Fatalf("copies %v, %v", copies, err)
	}
}

func TestCleanWithoutDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	secrets := filepath.Join(t.TempDir(), "burro-1a2b3c4d-r1")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, false} {
		c, err := Clean(context.Background(), "burro-1a2b3c4d-r1", secrets)
		if err != nil || *c != (output.Cleaned{Secrets: want}) {
			t.Fatalf("got %+v, %v; want secrets %v", c, err, want)
		}
	}
}

func TestExecLeftover(t *testing.T) {
	mark := marker(t)
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "pkill", "-f", "sleep "+mark).Run() })
	var out strings.Builder
	start := time.Now()
	code, err := Exec(context.Background(), t.TempDir(), "sleep "+mark+" & echo started", nil, &out)
	if err != nil || code != 0 || out.String() != "started\n" || time.Since(start) > 10*time.Second {
		t.Fatalf("code %d out %q err %v after %s", code, out.String(), err, time.Since(start))
	}
}
