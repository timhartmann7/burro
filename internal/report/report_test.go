package report_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/report"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	longTree = "5530df9c0e"
	stepUnit = "unit"
	r1       = "r1"
	r2       = "r2"
	verify   = "verify"
	stepE2E  = "e2e"
)

func usage(t *testing.T, err error, code output.Code) {
	t.Helper()
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != code || coded.Fix() == "" {
		t.Fatalf("got %v, want exit %d with a fix line", err, code)
	}
}

func TestKey(t *testing.T) {
	a, b := report.Open("/home/u/burro/a"), report.Open("/home/u/burro/b")
	if !regexp.MustCompile(`^burro-[0-9a-f]{8}-r42$`).MatchString(a.Key("r42")) {
		t.Fatalf("key %q", a.Key("r42"))
	}
	if a.Key(r1) == b.Key(r1) || a.Key(r1) != report.Open("/home/u/burro/a").Key(r1) {
		t.Fatal("keys must differ per project and stay stable")
	}
}

func TestNewRunAndLast(t *testing.T) {
	p := report.Open(t.TempDir())
	_, err := p.Last()
	usage(t, err, output.ExitUsage)
	for _, want := range []string{r1, r2} {
		if id, err := p.NewRun(); err != nil || id != want {
			t.Fatalf("NewRun = %q, %v; want %q", id, err, want)
		}
	}
	// Strays don't count, and a gap continues from the highest id.
	for _, name := range []string{"r9", "r09", "x5", "r0"} {
		if err := os.Mkdir(filepath.Join(filepath.Dir(p.RunDir(r1)), name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(p.RunDir(r1)), "r20"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := p.Last(); err != nil || id != "r9" {
		t.Fatalf("Last = %q, %v", id, err)
	}
	if id, err := p.NewRun(); err != nil || id != "r10" {
		t.Fatalf("NewRun after a gap = %q, %v", id, err)
	}
}

func TestNewRunConcurrent(t *testing.T) {
	p := report.Open(t.TempDir())
	ids := make(chan string, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			id, err := p.NewRun()
			if err != nil {
				t.Error(err)
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("id %s handed out twice", id)
		}
		seen[id] = true
	}
}

func TestStatusRoundTrip(t *testing.T) {
	p := report.Open(t.TempDir())
	id, err := p.NewRun()
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Status(r2)
	usage(t, err, output.ExitUsage)
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	s := &report.Status{
		Run: id, Pipeline: verify, Tree: longTree, State: output.StateRunning, Started: now, Heartbeat: now,
		Steps: []report.Step{{Name: stepUnit, State: output.StateOK}, {Name: stepE2E, State: output.StatePending}},
	}
	if err := p.WriteStatus(s); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.RunDir(id), "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"run":"r1","pipeline":"verify","tree":"5530df9c0e","state":"running","exit":null,"reason":"",` +
		`"started":"2026-10-29T10:00:00Z","finished":null,"heartbeat":"2026-10-29T10:00:00Z","steps":[` +
		`{"name":"unit","state":"ok","detail":"","seconds":0,"exit":null},{"name":"e2e","state":"pending","detail":"","seconds":0,"exit":null}]}` + "\n"
	if string(data) != want {
		t.Fatalf("status.json\n%s\nwant\n%s", data, want)
	}
	s.Finish(output.StateDone, 1, "", now.Add(90*time.Second+300*time.Millisecond))
	if err := p.WriteStatus(s); err != nil {
		t.Fatal(err)
	}
	got, err := p.Status(id)
	if err != nil || *got.Exit != 1 || got.Steps[1].State != output.StateNotRun || !got.Finished.Equal(now.Add(90*time.Second)) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(p.RunDir(id), "status.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Status(id); err == nil {
		t.Fatal("broken status.json passed")
	}
	if matches, _ := filepath.Glob(filepath.Join(p.RunDir(id), ".tmp-*")); len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}

// sleeper starts a process that lives until the test ends.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// deadPid is the pid of a process that has exited and been reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// holder is the live holder of p's lock.
func holder(t *testing.T, p *report.Project) string {
	t.Helper()
	var id string
	if err := p.Locked(func() error { id = p.Holder(); return nil }); err != nil {
		t.Fatal(err)
	}
	return id
}

// take takes the lock for id and records pid as its runner.
func take(t *testing.T, p *report.Project, id string, pid int) {
	t.Helper()
	if err := p.Locked(func() error { return p.Take(id) }); err != nil {
		t.Fatal(err)
	}
	if pid != 0 {
		if err := p.WritePid(id, pid); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLock(t *testing.T) {
	p := report.Open(t.TempDir())
	if holder(t, p) != "" {
		t.Fatal("fresh project is locked")
	}
	id, _ := p.NewRun()
	take(t, p, id, 0)
	// No pid yet means the executor never got the runner going.
	if holder(t, p) != "" {
		t.Fatal("lock without a pid counts as held")
	}
	take(t, p, id, sleeper(t).Process.Pid)
	if holder(t, p) != id {
		t.Fatalf("holder %q, want %s", holder(t, p), id)
	}
	usage(t, report.Busy(id), output.ExitBusy)

	// Another run releasing doesn't free the lock; the holder does.
	if err := p.Release("r7"); err != nil || holder(t, p) != id {
		t.Fatalf("release by another run: %v", err)
	}
	if err := p.Release(id); err != nil || holder(t, p) != "" {
		t.Fatalf("release: %v", err)
	}
	if err := p.Release(id); err != nil {
		t.Fatalf("second release: %v", err)
	}
}

func TestStaleLock(t *testing.T) {
	p := report.Open(t.TempDir())
	id, _ := p.NewRun()
	take(t, p, id, deadPid(t))
	if holder(t, p) != "" {
		t.Fatal("lock of a dead runner counts as held")
	}
	next, _ := p.NewRun()
	take(t, p, next, sleeper(t).Process.Pid)
	if holder(t, p) != next {
		t.Fatalf("holder %q after taking over a stale lock", holder(t, p))
	}
}

// claim takes the lock of p for a new run unless a live run holds it, the
// way the executor's start does.
func claim(p *report.Project, pid int, won chan<- string) error {
	return p.Locked(func() error {
		if p.Holder() != "" {
			return report.Busy("x")
		}
		id, err := p.NewRun()
		if err != nil {
			return err
		}
		if err := p.Take(id); err != nil {
			return err
		}
		won <- id
		return p.WritePid(id, pid)
	})
}

// acceptance: S2-17
func TestLockRace(t *testing.T) {
	p := report.Open(t.TempDir())
	pid := sleeper(t).Process.Pid
	won := make(chan string, 10)
	errs := make(chan error, 10)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { errs <- claim(p, pid, won) })
	}
	wg.Wait()
	close(won)
	close(errs)
	for err := range errs {
		var coded *output.Error
		if err != nil && (!errors.As(err, &coded) || coded.Code() != output.ExitBusy) {
			t.Error(err)
		}
	}
	if n := len(won); n != 1 {
		t.Fatalf("%d callers took the lock", n)
	}
}

func TestLockedErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".burro"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := report.Open(dir)
	if err := p.Locked(func() error { return nil }); err == nil {
		t.Fatal("Locked passed without a .burro folder")
	}
	if _, err := p.NewRun(); err == nil {
		t.Fatal("NewRun passed without a .burro folder")
	}
	if err := p.Signal(r1, syscall.SIGTERM); err == nil {
		t.Fatal("Signal passed without a pid")
	}
}

// runningRun writes a run that says running, with its heartbeat age old
// at now and pid as its runner.
func runningRun(t *testing.T, p *report.Project, now time.Time, age time.Duration, pid int) (string, time.Time) {
	t.Helper()
	id, _ := p.NewRun()
	beat := report.Stamp(now.Add(-age))
	s := &report.Status{
		Run: id, Pipeline: verify, State: output.StateRunning, Started: beat.Add(-time.Minute), Heartbeat: beat,
		Steps: []report.Step{{Name: stepUnit, State: output.StateOK}, {Name: stepE2E, State: output.StateRunning}, {Name: "lint", State: output.StatePending}},
	}
	if err := p.WriteStatus(s); err != nil {
		t.Fatal(err)
	}
	if err := p.WritePid(id, pid); err != nil {
		t.Fatal(err)
	}
	return id, beat
}

func TestCurrentKeepsLiveRuns(t *testing.T) {
	now := time.Now()
	for name, run := range map[string]struct {
		age time.Duration
		pid int
	}{
		"fresh heartbeat, dead runner": {age: 10 * time.Second, pid: deadPid(t)},
		"stale heartbeat, live runner": {age: time.Minute, pid: sleeper(t).Process.Pid},
	} {
		p := report.Open(t.TempDir())
		id, _ := runningRun(t, p, now, run.age, run.pid)
		if got, err := p.Current(id, now); err != nil || got.State != output.StateRunning {
			t.Fatalf("%s: %+v, %v", name, got, err)
		}
	}
}

func TestCurrentDetectsDeadRunner(t *testing.T) {
	now := time.Now()
	p := report.Open(t.TempDir())
	id, beat := runningRun(t, p, now, 16*time.Second, deadPid(t))
	got, err := p.Current(id, now)
	if err != nil || got.State != output.StateDone || *got.Exit != 2 || got.Reason != report.Died || !got.Finished.Equal(beat) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if e2e, lint := got.Steps[1], got.Steps[2]; e2e.State != output.StateFail || e2e.Detail != report.Died || lint.State != output.StateNotRun {
		t.Fatalf("steps %+v", got.Steps)
	}
	if saved, _ := p.Status(id); saved.State != output.StateDone {
		t.Fatal("the dead run was not written back")
	}
}

func TestSignal(t *testing.T) {
	p := report.Open(t.TempDir())
	id, _ := p.NewRun()
	cmd := sleeper(t)
	if err := p.WritePid(id, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(id, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil || !strings.Contains(err.Error(), "terminated") {
		t.Fatalf("sleep ended with %v", err)
	}
	// Signaling a runner that is gone is not an error.
	if err := p.Signal(id, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
}

func TestAlive(t *testing.T) {
	if !report.Alive(os.Getpid()) || report.Alive(deadPid(t)) || report.Alive(0) || report.Alive(-1) {
		t.Fatal("Alive is wrong")
	}
	// A child that exited but was never reaped is a zombie.
	cmd := exec.CommandContext(context.Background(), "true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return
	}
	testutil.Poll(t, 5*time.Second, func() error {
		if report.Alive(cmd.Process.Pid) {
			return errors.New("a zombie counts as alive")
		}
		return nil
	})
}

func TestMembers(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		if report.Members(os.Getpid()) != nil {
			t.Fatal("members found without /proc")
		}
		return
	}
	cmd := exec.CommandContext(context.Background(), "sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	if got := report.Members(cmd.Process.Pid); len(got) != 1 || got[0] != cmd.Process.Pid {
		t.Fatalf("members %v, want [%d]", got, cmd.Process.Pid)
	}
}

func TestWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c.json")
	if err := report.WriteFile(path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{}" {
		t.Fatalf("got %q, %v", data, err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteFile(filepath.Join(blocked, "x.json"), nil); err == nil {
		t.Fatal("WriteFile under a file passed")
	}
}
