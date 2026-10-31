//go:build integration

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	latency = 40 * time.Millisecond
	runsDir = appDir + "/.burro/runs"
)

// pipeline is a burro.toml for the test server with the given tables.
func pipeline(tables string) map[string]string {
	return map[string]string{tomlFile: tomlHead + tables}
}

// ready is a world with a host and a burro.toml whose steps are tables.
func ready(t *testing.T, tables string, files map[string]string) *world {
	t.Helper()
	all := pipeline(tables)
	for k, v := range files {
		all[k] = v
	}
	w := newWorld(t, all)
	w.addHost(v1)
	return w
}

// pidsScript prints the pids whose command line contains $1, leaving out
// itself. Zombies have an empty command line and never match. A process may
// exit between the glob and the read; the braces keep the shell's own
// "cannot open" out of the output, which a plain 2>/dev/null after the
// input redirect doesn't.
const pidsScript = `for p in /proc/[0-9]*; do
  [ "${p#/proc/}" = "$$" ] && continue
  c=$( { tr '\0' ' ' <"$p/cmdline"; } 2>/dev/null) || continue
  case "$c" in *"$1"*) echo "${p#/proc/}" ;; esac
done; true`

// serverPids lists the server processes whose command line has pattern.
func (w *world) serverPids(pattern string) []string {
	w.t.Helper()
	return strings.Fields(w.srv.Root(w.t, "sh", "-c", pidsScript, "sh", pattern))
}

// killServer sends SIGKILL to the server processes matching pattern.
func (w *world) killServer(pattern string) {
	w.t.Helper()
	if pids := w.serverPids(pattern); len(pids) > 0 {
		w.srv.Root(w.t, append([]string{"kill", "-9"}, pids...)...)
	}
}

// dropSSH kills every ssh session on the server, the way a dropped network
// ends them.
func (w *world) dropSSH() {
	w.t.Helper()
	w.killServer("sshd-session: burro")
}

func (w *world) runFolders() []string {
	w.t.Helper()
	return strings.Fields(w.srv.Exec(w.t, "sh", "-c", "ls "+runsDir+" 2>/dev/null; true"))
}

func (w *world) serverFile(path string) string {
	w.t.Helper()
	return w.srv.Exec(w.t, "sh", "-c", "cat "+path+" 2>/dev/null; true")
}

// waitFor polls until the server file at path contains want.
func (w *world) waitFor(path, want string) {
	w.t.Helper()
	testutil.Poll(w.t, 30*time.Second, func() error {
		if got := w.serverFile(path); !strings.Contains(got, want) {
			return fmt.Errorf("%s has %q, want %q", path, got, want)
		}
		return nil
	})
}

// cleanup cancels whatever run is still going when the test ends.
func (w *world) cleanup() {
	w.t.Cleanup(func() { w.burro(v1, argCancel) })
}

var headline = regexp.MustCompile(`^r(\d+) (\S+) · tree [0-9a-f]{7} · (running|\S+ · exit \d( · .+)?)$`)

// acceptance: S2-01
func TestRunReturnsAtOnce(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["install", "long"]

[steps.install]
run = "true"
skip_if_unchanged = ["burro.toml"]

[steps.long]
run = "sleep 600"
`, nil)
	w.cleanup()
	w.srv.SetLatency(t, latency)
	start := time.Now()
	r := w.burro(v1, argRun)
	took := time.Since(start)
	t.Logf("run of a 10 minute pipeline with 80 ms round trip returned in %s", took)
	lines := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	if r.Code != 0 || r.Stderr != "" || len(lines) != 3 || took > 3*time.Second {
		t.Fatalf("exit %d after %s\nstdout %q\nstderr %q", r.Code, took, r.Stdout, r.Stderr)
	}
	if !strings.HasPrefix(lines[0], "r1 default · tree ") || !strings.HasSuffix(lines[0], " · running") || !headline.MatchString(lines[0]) {
		t.Fatalf("first line %q", lines[0])
	}
	if !regexp.MustCompile(`^synced 1 file in \d+\.\ds$`).MatchString(lines[1]) || lines[2] != "steps: install long" {
		t.Fatalf("lines %q", lines[1:])
	}
	if r := w.burro(v1, argRun, argJSON); r.Code != 5 || r.Stdout != "" || !strings.Contains(r.Stderr, "fix: wait for it with burro wait r1") {
		t.Fatalf("second run: %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
}

// acceptance: S2-02
func TestRunOutlivesClient(t *testing.T) {
	w := newWorld(t, pipeline(`
[pipelines]
default = ["first", "second"]

[steps.first]
run = "echo started; sleep 6; echo first done"

[steps.second]
run = "echo second done"
`))
	w.addHost(v2)
	w.cleanup()
	bin := testutil.BuildBurro(t, "-ldflags", "-X main.version="+v2)
	w.must(v2, argRun)

	// A client killed in the middle of wait, and the network gone with it.
	client := exec.CommandContext(context.Background(), bin, argWait)
	client.Dir, client.Env = w.repo, w.env
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	testutil.Poll(t, 30*time.Second, func() error {
		if len(w.serverPids("agent wait")) == 0 {
			return errors.New("no executor wait on the server yet")
		}
		return nil
	})
	if err := client.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = client.Wait()
	w.closeMasters()
	w.dropSSH()
	if pids := w.serverPids("agent runner"); len(pids) != 1 {
		t.Fatalf("runner gone with the client: %v", pids)
	}

	// The next wait loses its connection too, reconnects and still ends
	// with the summary.
	done := make(chan testutil.Result, 1)
	go func() { done <- w.burro(v2, argWait) }()
	testutil.Poll(t, 30*time.Second, func() error {
		if len(w.serverPids("agent wait")) == 0 {
			return errors.New("no executor wait on the server yet")
		}
		return nil
	})
	w.dropSSH()
	r := <-done
	lines := strings.Split(r.Stdout, "\n")
	if r.Code != 0 || r.Stderr != "" || !headline.MatchString(lines[0]) || !strings.HasSuffix(lines[0], " · exit 0") ||
		!regexp.MustCompile(`^first {3}ok {2}\d+(\.\d)?s\nsecond {2}ok {2}\d\.\ds\n$`).MatchString(strings.Join(lines[1:], "\n")) {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", r.Code, r.Stdout, r.Stderr)
	}
	if log := w.serverFile(runsDir + "/r1/first.log"); log != "started\nfirst done" {
		t.Fatalf("first.log %q", log)
	}
}

// tickTOML is a step that prints a line a second for ten minutes.
const tickTOML = `
[pipelines]
default = ["tick", "after"]

[steps.tick]
run = "for i in $(seq 1 600); do echo tick $i; sleep 1; done"

[steps.after]
run = "true"
`

// acceptance: S2-03
func TestRunnerDeath(t *testing.T) {
	w := ready(t, tickTOML, nil)
	t.Cleanup(func() { w.killServer("seq 1 600") })
	w.must(v1, argRun)
	w.srv.SetLatency(t, latency)
	w.waitFor(runsDir+"/r1/tick.log", "tick 3")
	pid := w.serverFile(runsDir + "/r1/pid")
	w.srv.Exec(t, "kill", "-9", pid)
	killed := time.Now()
	var r testutil.Result
	testutil.Poll(t, 30*time.Second, func() error {
		r = w.burro(v1, argStatus)
		if r.Code != 2 {
			return fmt.Errorf("status exit %d: %s", r.Code, r.Stdout)
		}
		return nil
	})
	took := time.Since(killed)
	t.Logf("runner death reported %s after kill -9", took)
	lines := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	if took > 15*time.Second || !strings.HasSuffix(lines[0], " · exit 2 · runner died") || r.Stderr != "" {
		t.Fatalf("after %s: %q %q", took, r.Stdout, r.Stderr)
	}
	if !regexp.MustCompile(`^tick {3}FAIL {5}\d+(\.\d)?s {2}runner died$`).MatchString(lines[1]) || lines[2] != "after  not run" {
		t.Fatalf("steps %q", lines[1:])
	}
	if r := w.burro(v1, argWait); r.Code != 2 || !strings.Contains(r.Stdout, "more: burro log r1 --step tick\n") {
		t.Fatalf("wait after death: %d %q", r.Code, r.Stdout)
	}
}

// acceptance: S2-05
func TestRunSelection(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["a", "b", "c"]

[steps.a]
run = "echo a >> order.txt"

[steps.b]
run = "echo b >> order.txt"

[steps.c]
run = "echo c >> order.txt"
`, map[string]string{gitignore: "order.txt\n"})
	order := func() string { return w.serverFile(appDir + "/order.txt") }
	w.must(v1, argRun, "--from", "b")
	w.must(v1, argWait)
	if got := order(); got != "b\nc" {
		t.Fatalf("--from b ran %q", got)
	}
	if out := w.must(v1, argRun, argOnly, "c,a"); !strings.HasSuffix(out, "\nsteps: a c\n") {
		t.Fatalf("--only: %q", out)
	}
	out := w.must(v1, argWait)
	if got := order(); got != "b\nc\na\nc" {
		t.Fatalf("--only c,a ran %q", got)
	}
	if !strings.Contains(out, "\nb  not run  --only c,a\n") {
		t.Fatalf("summary %q", out)
	}
	for _, args := range [][]string{{argRun, argOnly, "a,nope"}, {argRun, "--from", "nope"}, {argRun, "nopipeline"}, {argRun, argOnly, ""}} {
		r := w.burro(v1, args...)
		if r.Code != 4 || r.Stdout != "" || !strings.Contains(r.Stderr, "\nfix: use one of: ") {
			t.Fatalf("%q: exit %d %q %q", args, r.Code, r.Stdout, r.Stderr)
		}
	}
	if got := w.runFolders(); strings.Join(got, " ") != "r1 r2" {
		t.Fatalf("runs %v", got)
	}
}

// acceptance: S2-06
func TestTimeoutKillsEveryProcess(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["slow"]

[steps.slow]
run = "sleep 600.1 & nohup sleep 600.2 >/dev/null 2>&1 & (trap '' TERM; sleep 600.3) & (set -m; sleep 600.4 & wait) & echo started; sleep 600.5"
timeout = "2s"
`, nil)
	w.must(v1, argRun)
	r := w.burro(v1, argWait)
	if r.Code != 1 || !strings.Contains(r.Stdout, "\nslow  FAIL  ") || !strings.Contains(r.Stdout, "  timed out after 2.0s\n  started\n") {
		t.Fatalf("exit %d %q", r.Code, r.Stdout)
	}
	if pids := w.serverPids("sleep 600."); len(pids) != 0 {
		t.Fatalf("processes left: %v", pids)
	}
}

// acceptance: S2-07
func TestBusyProject(t *testing.T) {
	w := ready(t, tickTOML, nil)
	t.Cleanup(func() { w.killServer("seq 1 600") })
	w.must(v1, argRun)
	for _, args := range [][]string{{argRun}, {argSync}} {
		r := w.burro(v1, args...)
		if r.Code != 5 || r.Stdout != "" || r.Stderr != "burro: host vps1: run r1 is active in this project\n"+
			"fix: wait for it with burro wait r1, or stop it with burro cancel r1\n" {
			t.Fatalf("%q: exit %d %q %q", args, r.Code, r.Stdout, r.Stderr)
		}
	}
	w.killServer("agent runner")
	w.killServer("seq 1 600")
	r := w.burro(v1, argRun)
	if r.Code != 0 || !strings.HasPrefix(r.Stdout, "r2 default ") {
		t.Fatalf("run after the runner died: %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	w.must(v1, argCancel)
	w.must(v1, argSync)
}

// acceptance: S2-09
func TestWaitTimeout(t *testing.T) {
	w := ready(t, tickTOML, nil)
	w.cleanup()
	w.must(v1, argRun)
	start := time.Now()
	r := w.burro(v1, argWait, "--timeout", "3s")
	if took := time.Since(start); r.Code != 3 || r.Stdout != "" || took > 10*time.Second ||
		r.Stderr != "burro: r1 is still running\nfix: run burro wait r1 again; the run keeps going\n" {
		t.Fatalf("exit %d after %s: %q %q", r.Code, took, r.Stdout, r.Stderr)
	}
	r = w.burro(v1, argStatus, "r1")
	if r.Code != 3 || !strings.Contains(r.Stdout, "· running\ntick   running") {
		t.Fatalf("status after the timeout: %d %q", r.Code, r.Stdout)
	}
}

// recorder keeps every write apart, so a test sees when output came.
type recorder struct {
	mu     sync.Mutex
	writes []string
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, string(p))
	return len(p), nil
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.writes, "")
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.writes)
}

// acceptance: S2-10
func TestWaitOutput(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["one", "two"]

[steps.one]
run = "sleep 2"

[steps.two]
run = "sleep 2"
`, nil)
	w.must(v1, argRun)
	var stdout, stderr recorder
	done := make(chan int, 1)
	go func() { done <- w.burroTo(v1, &stdout, &stderr, argWait, argMode, agentMode) }()
	// While the run goes, agent mode prints nothing anywhere.
	testutil.Poll(t, 30*time.Second, func() error {
		if stdout.count()+stderr.count() > 0 {
			t.Fatalf("agent wait printed while waiting: %q %q", stdout.String(), stderr.String())
		}
		if s := w.serverFile(runsDir + "/r1/status.json"); !strings.Contains(s, `"name":"two","state":"running"`) {
			return errors.New("step two not running yet")
		}
		return nil
	})
	if code := <-done; code != 0 || stderr.count() != 0 || stdout.count() != 1 || !headline.MatchString(strings.Split(stdout.String(), "\n")[0]) {
		t.Fatalf("exit %d, %d writes to stdout %q, stderr %q", code, stdout.count(), stdout.String(), stderr.String())
	}

	w.must(v1, argRun)
	var hout, herr recorder
	if code := w.burroTo(v1, &hout, &herr, argWait, argMode, humanMode); code != 0 {
		t.Fatalf("human wait: exit %d %q", code, herr.String())
	}
	progress := herr.String()
	if !strings.Contains(progress, erase+"one · step 1 of 2 · ") || !strings.Contains(progress, erase+"two · step 2 of 2 · ") ||
		!strings.Contains(progress, " · Ctrl+C is safe, the run keeps going") || strings.Contains(progress, "\n") || !strings.HasSuffix(progress, erase) {
		t.Fatalf("human stderr %q", progress)
	}
	mask := regexp.MustCompile(`r\d|\d+(\.\d)?s`)
	if mask.ReplaceAllString(testutil.StripANSI(hout.String()), "N") != mask.ReplaceAllString(stdout.String(), "N") {
		t.Fatalf("human and agent summaries differ:\n%q\n%q", hout.String(), stdout.String())
	}
}

// lockedBuffer is a bytes.Buffer that a running process and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// acceptance: S2-11
func TestWaitCtrlC(t *testing.T) {
	w := newWorld(t, pipeline(tickTOML))
	w.addHost(v2)
	w.cleanup()
	bin := testutil.BuildBurro(t, "-ldflags", "-X main.version="+v2)
	w.must(v2, argRun)
	var stdout, stderr lockedBuffer
	cmd := exec.CommandContext(context.Background(), bin, argWait, argMode, humanMode)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = w.repo, w.env, &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	testutil.Poll(t, 30*time.Second, func() error {
		if !strings.Contains(stderr.String(), "tick · step 1 of 2 · ") {
			return fmt.Errorf("no progress line yet: %q", stderr.String())
		}
		return nil
	})
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("wait after Ctrl+C: %v", err)
	}
	if got := stderr.String(); !strings.HasSuffix(got, erase+"still running · come back with burro wait\n") || stdout.String() != "" {
		t.Fatalf("stderr %q stdout %q", got, stdout.String())
	}
	if r := w.burro(v2, argStatus); r.Code != 3 || !strings.Contains(r.Stdout, "tick   running") {
		t.Fatalf("status after Ctrl+C: %d %q", r.Code, r.Stdout)
	}
}

// acceptance: S2-12
func TestLogGrepNoisyLog(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["dump"]

[steps.dump]
run = "cat noisy.log"
`, map[string]string{"noisy.log": string(testutil.NoisyLog(8<<20, 7))})
	w.must(v1, argRun)
	w.must(v1, argWait)
	if size, err := strconv.Atoi(w.srv.Exec(t, "stat", "-c", "%s", runsDir+"/r1/dump.log")); err != nil || size < 8<<20 {
		t.Fatalf("log is %d bytes: %v", size, err)
	}
	w.srv.SetLatency(t, latency)
	start := time.Now()
	r := w.burro(v1, cmdLog, argStep, "dump", "--grep", "cohorts")
	took := time.Since(start)
	t.Logf("log --grep on an 8 MB log with 80 ms round trip: %s", took)
	if r.Code != 0 || took > time.Second || !regexp.MustCompile(`^r1 dump · \d+ matches\n`).MatchString(r.Stdout) {
		t.Fatalf("exit %d after %s: %q", r.Code, took, r.Stderr)
	}
	if !utf8.ValidString(r.Stdout) || strings.ContainsAny(r.Stdout, "\x1b\r") || strings.ContainsRune(r.Stdout, 0) {
		t.Fatalf("dirty output %q", r.Stdout)
	}
	lines := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	if len(lines) != 62 || !regexp.MustCompile(`^\+\d+ more matches: burro log r1 --step dump --grep cohorts --tail 120$`).MatchString(lines[61]) {
		t.Fatalf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}
	more := strings.Fields(strings.SplitN(lines[61], ": burro ", 2)[1])
	if r := w.burro(v1, more...); r.Code != 0 || len(strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")) != 122 {
		t.Fatalf("more: %d %q", r.Code, r.Stderr)
	}
}

// acceptance: S2-13
func TestCancelStopsEverything(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["slow", "after"]

[steps.slow]
run = "sleep 600.1 & nohup sleep 600.2 >/dev/null 2>&1 & (trap '' TERM; sleep 600.3) & (set -m; sleep 600.4 & wait) & echo started; sleep 600.5"

[steps.after]
run = "true"
`, nil)
	w.must(v1, argRun)
	w.waitFor(runsDir+"/r1/slow.log", "started")
	w.srv.SetLatency(t, latency)
	start := time.Now()
	r := w.burro(v1, argCancel)
	took := time.Since(start)
	t.Logf("cancel took %s", took)
	if r.Code != 0 || took > 15*time.Second {
		t.Fatalf("exit %d after %s: %q %q", r.Code, took, r.Stdout, r.Stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	if !strings.HasSuffix(lines[0], " · exit 1 · "+output.StateCanceled) || !regexp.MustCompile(`^slow {3}FAIL {5}\d+(\.\d)?s {2}`+output.StateCanceled+`$`).MatchString(lines[1]) ||
		lines[2] != "after  not run" || len(lines) != 3 {
		t.Fatalf("cancel printed %q", r.Stdout)
	}
	if pids := w.serverPids("sleep 600."); len(pids) != 0 {
		t.Fatalf("processes left: %v", pids)
	}
	if lock := w.serverFile(appDir + "/.burro/lock"); lock != "" {
		t.Fatalf("lock kept: %q", lock)
	}
	if r := w.burro(v1, argSync); r.Code != 0 {
		t.Fatalf("sync after cancel: %d %q", r.Code, r.Stderr)
	}
}

// acceptance: S2-14
func TestFailedStepSummary(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["unit", "lint"]

[steps.unit]
run = '''for i in $(seq 1 100); do printf '\033[32mline %d\033[0m\r\n' $i; done; echo noise; exit 1'''

[steps.lint]
run = "true"

[filters]
drop = ['^noise$']
`, nil)
	w.must(v1, argRun)
	r := w.burro(v1, argWait)
	lines := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	if r.Code != 1 || len(lines) != 34 || !strings.HasSuffix(lines[0], " · exit 1") || !regexp.MustCompile(`^unit {2}FAIL {5}\d\.\ds$`).MatchString(lines[1]) {
		t.Fatalf("exit %d\n%s", r.Code, r.Stdout)
	}
	for i := range 30 {
		if want := fmt.Sprintf("  line %d", 71+i); lines[2+i] != want {
			t.Fatalf("line %d: %q, want %q", 2+i, lines[2+i], want)
		}
	}
	if lines[32] != "more: burro log r1 --step unit" || lines[33] != "lint  not run" {
		t.Fatalf("tail end %q", lines[32:])
	}
	more := w.burro(v1, strings.Fields(strings.TrimPrefix(lines[32], "more: burro "))...)
	got := strings.Split(strings.TrimSuffix(more.Stdout, "\n"), "\n")
	if more.Code != 0 || len(got) != 61 || got[0] != "line 41" || got[59] != "line 100" || got[60] != "+40 earlier lines: burro log r1 --step unit --tail 120" {
		t.Fatalf("more: exit %d %q", more.Code, more.Stdout)
	}
	if r := w.burro(v1, argWait, argJSON); r.Code != 1 || !strings.Contains(r.Stdout, `"tail":["line 71",`) || strings.Count(r.Stdout, "\n") != 1 {
		t.Fatalf("wait --json: %q", r.Stdout)
	}
}

// acceptance: S2-15
func TestStatusComparesTrees(t *testing.T) {
	w := ready(t, "\n[pipelines]\ndefault = [\"test\"]\n\n[steps.test]\nrun = \"true\"\n", map[string]string{"main.go": "package main\n"})
	w.must(v1, argRun)
	w.must(v1, argWait)
	r := w.burro(v1, argStatus)
	if r.Code != 0 || strings.Contains(r.Stdout, "local tree") || !regexp.MustCompile(`^r1 default · tree [0-9a-f]{7} · \d\.\ds · exit 0\ntest  ok  \d\.\ds\n$`).MatchString(r.Stdout) {
		t.Fatalf("matching trees: %d %q", r.Code, r.Stdout)
	}
	writeFile(t, filepath.Join(w.repo, "main.go"), "package main\n\nfunc main() {}\n")
	r = w.burro(v1, argStatus, "r1")
	lines := strings.Split(r.Stdout, "\n")
	m := regexp.MustCompile(`^local tree ([0-9a-f]{7}) differs from run tree$`).FindStringSubmatch(lines[1])
	if r.Code != 0 || m == nil || strings.Contains(lines[0], m[1]) {
		t.Fatalf("after an edit: %q", r.Stdout)
	}
	if r := w.burro(v1, argStatus, argJSON); !strings.Contains(r.Stdout, `"local_tree":"`+m[1]+`"`) {
		t.Fatalf("status --json: %q", r.Stdout)
	}
	if err := os.Remove(filepath.Join(w.repo, "main.go")); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, w.repo, "checkout", "main.go")
	if r := w.burro(v1, argStatus); strings.Contains(r.Stdout, "local tree") {
		t.Fatalf("after reverting the edit: %q", r.Stdout)
	}
}

// acceptance: S2-17
func TestConcurrentRuns(t *testing.T) {
	w := ready(t, tickTOML, map[string]string{"src/a.txt": "a", "src/b/c.txt": "c"})
	t.Cleanup(func() { w.killServer("seq 1 600") })
	w.cleanup()
	results := make([]testutil.Result, 2)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range results {
		wg.Go(func() {
			<-gate
			results[i] = w.burro(v1, argRun)
		})
	}
	close(gate)
	wg.Wait()
	codes := []int{results[0].Code, results[1].Code}
	slices.Sort(codes)
	if codes[0] != 0 || codes[1] != 5 {
		t.Fatalf("exit codes %v:\n%+v\n%+v", codes, results[0], results[1])
	}
	if got := w.runFolders(); len(got) != 1 {
		t.Fatalf("runs on the server: %v", got)
	}
	if pids := w.serverPids("agent runner"); len(pids) != 1 {
		t.Fatalf("runners: %v", pids)
	}
}

func TestRunNoDefaultPipeline(t *testing.T) {
	w := ready(t, "\n[pipelines]\nverify = [\"test\"]\n\n[steps.test]\nrun = \"true\"\n", nil)
	r := w.burro(v1, argRun)
	if r.Code != 4 || r.Stderr != "burro: burro.toml has no pipeline \"default\"\nfix: use one of: verify\n" {
		t.Fatalf("exit %d %q", r.Code, r.Stderr)
	}
	for _, args := range [][]string{{argStatus}, {argWait}, {argCancel}, {cmdLog, argStep, "test"}} {
		if r := w.burro(v1, args...); r.Code != 2 && r.Code != 4 {
			t.Fatalf("%q without runs: exit %d %q", args, r.Code, r.Stderr)
		}
	}
	w.must(v1, argSync)
	for _, args := range [][]string{{argStatus}, {argWait}, {argCancel}, {cmdLog, argStep, "test"}} {
		if r := w.burro(v1, args...); r.Code != 4 || r.Stderr != "burro: host vps1: this project has no runs yet\nfix: start one with burro run\n" {
			t.Fatalf("%q without runs: exit %d %q", args, r.Code, r.Stderr)
		}
	}
	w.must(v1, argRun, "verify")
	w.must(v1, argWait)
	if r := w.burro(v1, cmdLog, argStep, "nope"); r.Code != 4 || !strings.Contains(r.Stderr, "fix: use one of: test\n") {
		t.Fatalf("unknown step: %d %q", r.Code, r.Stderr)
	}
}

// failTOML is a step that fails without a report, so its summary is the
// tail of its log.
const failTOML = `
[pipelines]
default = ["e2e"]

[steps.e2e]
run = "echo cohorts are listed; exit 1"
`

func TestReadsSurviveDroppedSSH(t *testing.T) {
	w := ready(t, failTOML, nil)
	w.must(v1, argRun)
	if r := w.burro(v1, argWait); r.Code != 1 {
		t.Fatalf("wait: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	log := runsDir + "/r1/e2e.log"
	for _, tt := range []struct {
		args []string
		code int
	}{
		{args: []string{argFails, "r1"}, code: 1},
		{args: []string{cmdLog, "r1", argStep, argE2E, argGrep, "cohorts"}, code: 0},
		{args: []string{argStatus, "r1"}, code: 1},
	} {
		// Opening a FIFO blocks until someone writes it, so the call is
		// still on the server when the connection drops.
		blocked := log
		if tt.args[0] == argStatus {
			blocked = runsDir + "/r1/status.json"
		}
		saved := w.srv.Exec(t, "cat", blocked)
		w.srv.Exec(t, "sh", "-c", `rm "$1" && mkfifo "$1"`, "sh", blocked)
		done := make(chan testutil.Result, 1)
		go func() { done <- w.burro(v1, tt.args...) }()
		testutil.Poll(t, 30*time.Second, func() error {
			if len(w.serverPids("agent "+tt.args[0])) == 0 {
				return errors.New("no executor call on the server yet")
			}
			return nil
		})
		w.dropSSH()
		w.srv.Exec(t, "sh", "-c", `rm "$1" && printf '%s\n' "$2" >"$1"`, "sh", blocked, saved)
		r := <-done
		if r.Code != tt.code || r.Stderr != "" || !strings.Contains(r.Stdout, "cohorts are listed") && tt.args[0] != argStatus {
			t.Fatalf("%q after a dropped connection: exit %d\nstdout %q\nstderr %q", tt.args, r.Code, r.Stdout, r.Stderr)
		}
	}
}

// samePulled checks that each file, a path in the project, arrived in
// local byte for byte.
func (w *world) samePulled(local string, files ...string) {
	w.t.Helper()
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(local, filepath.FromSlash(name)))
		if err != nil {
			w.t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if want := strings.Fields(w.srv.Exec(w.t, "sha256sum", appDir+"/"+name))[0]; hex.EncodeToString(sum[:]) != want {
			w.t.Fatalf("%s arrived changed", name)
		}
	}
}

func TestPull(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["e2e"]

[steps.e2e]
run = "mkdir -p test-results/login && head -c 70000 /dev/urandom >test-results/login/trace.zip && printf '\\211PNG\\r\\n' >test-results/login/failed-1.png && ln -s /etc/passwd test-results/login/leak; exit 1"
`, map[string]string{gitignore: "test-results/\n.burro/\n"})
	w.must(v1, argRun)
	if r := w.burro(v1, argWait); r.Code != 1 {
		t.Fatalf("wait: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	local := filepath.Join(w.repo, ".burro", "pull")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "old.txt"), []byte("from an older pull"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := w.burro(v1, "pull", "test-results")
	if r.Code != 0 || r.Stderr != "" || r.Stdout != "pulled 2 files, 0.1 MB, into .burro/pull\n"+
		".burro/pull/test-results/login/failed-1.png\n.burro/pull/test-results/login/trace.zip\n" {
		t.Fatalf("pull: exit %d\nstdout %q\nstderr %q", r.Code, r.Stdout, r.Stderr)
	}
	w.samePulled(local, "test-results/login/trace.zip", "test-results/login/failed-1.png")
	if _, err := os.Lstat(filepath.Join(local, "test-results", "login", "leak")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a link came along: %v", err)
	}
	if _, err := os.Stat(filepath.Join(local, "old.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an older pull stayed: %v", err)
	}
	// What a pull brings stays out of the tree hash and the next sync, even
	// in a project whose .gitignore lacks .burro/.
	if err := os.WriteFile(filepath.Join(w.repo, gitignore), []byte("test-results/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.must(v1, "pull", "test-results/*/*.png")
	if st := testutil.Git(t, w.repo, "status", "--porcelain", "--untracked-files=all"); strings.Contains(st, ".burro/") {
		t.Fatalf("git sees the pull:\n%s", st)
	}
	if r := w.burro(v1, "pull", "nope"); r.Code != 4 || !strings.Contains(r.Stderr, `matches "nope"`) {
		t.Fatalf("no match: exit %d %q", r.Code, r.Stderr)
	}
}

func TestRunArgs(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["install", "e2e"]

[steps.install]
run = "true"

[steps.e2e]
run = "printf '[%s]' e2e"
`, nil)
	args := []string{"tests/a b.spec.ts", "$(touch pwned)", "it's", "--grep=x", "--"}
	r := w.burro(v1, append([]string{argRun, argOnly, argE2E, "--"}, args...)...)
	if r.Code != 0 || !strings.Contains(r.Stdout, "\nargs: tests/a b.spec.ts $(touch pwned) it's --grep=x --\n") {
		t.Fatalf("run: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	if r := w.burro(v1, argWait); r.Code != 0 {
		t.Fatalf("wait: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	if log := w.serverFile(runsDir + "/r1/e2e.log"); log != "[e2e][tests/a b.spec.ts][$(touch pwned)][it's][--grep=x][--]" {
		t.Fatalf("e2e.log %q", log)
	}
	if w.srv.Exec(t, "sh", "-c", "ls "+appDir+"/pwned 2>/dev/null; true") != "" {
		t.Fatal("an argument ran as a command on the server")
	}
	// Arguments for a pipeline of two steps fail before anything reaches
	// the server.
	r = w.burro(v1, argRun, "--", "x")
	if r.Code != 4 || r.Stderr != "burro: arguments after -- go to one step, and pipeline default runs 2: install, e2e\n"+
		"fix: pick the step with --only, for example burro run default --only install -- x\n" || len(w.runFolders()) != 1 {
		t.Fatalf("two steps: exit %d %q, runs %v", r.Code, r.Stderr, w.runFolders())
	}
}
