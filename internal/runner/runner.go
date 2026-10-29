// Package runner runs the steps of one run on the server, detached from any
// client: each step in its own session, with its timeout, while status.json
// and its heartbeat record the progress.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/timhartmann7/burro/internal/config"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/report"
)

const (
	// heartbeat is how often status.json proves the runner lives.
	heartbeat = 5 * time.Second
	// grace is how long a stopped step gets between SIGTERM and SIGKILL.
	grace = 10 * time.Second
	// poll is how often a stopping step is checked for leftover processes.
	poll        = 100 * time.Millisecond
	cacheFolder = ".burro/cache"
	// drain bounds how long a finished step's output may still arrive: a
	// process the step left running may hold its output open for good.
	drain = 2 * time.Second
	// cleanTimeout bounds the docker cleanup at the end of a run.
	cleanTimeout = 2 * time.Minute
)

// burroVar matches $BURRO_X and ${BURRO_X} in step variables.
var burroVar = regexp.MustCompile(`\$\{(BURRO_[A-Z0-9_]+)\}|\$(BURRO_[A-Z0-9_]+)`)

// Runner runs one run.
type Runner struct {
	env   []string
	beat  time.Duration
	grace time.Duration
}

// New returns a runner whose steps start from the environment env.
func New(env []string) *Runner {
	return &Runner{env: env, beat: heartbeat, grace: grace}
}

// ReadyFile is the descriptor the runner closes once SIGTERM cancels it
// instead of killing it. The executor waits for that before it returns, so a
// cancel right after a run never kills a runner that can't clean up yet.
const ReadyFile = 3

// request is one runner call after parsing.
type request struct {
	dir, id, key, secrets string
	ready                 bool
}

// Run runs the pending steps of a run the executor prepared. SIGTERM
// cancels it.
func (r *Runner) Run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	var req request
	flags := flag.NewFlagSet("runner", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&req.dir, "path", "", "project folder")
	flags.StringVar(&req.id, "run", "", "run id")
	flags.StringVar(&req.key, "key", "", "run key")
	flags.StringVar(&req.secrets, "secrets", "", "secrets folder")
	flags.BoolVar(&req.ready, "ready", false, "close the ready descriptor")
	err := flags.Parse(args)
	if req.ready {
		// The executor learns of a failure here from runner.log.
		_ = os.NewFile(ReadyFile, "ready").Close()
	}
	if err != nil || req.dir == "" || config.ValidRun(req.id) != nil || req.key == "" || req.secrets == "" {
		return output.NewError(output.ExitUsage, fmt.Sprintf("runner: bad arguments %q", args),
			"rerun with the same burro version")
	}
	return r.run(ctx, req)
}

// execution is the state of one run while its steps go.
type execution struct {
	r    *Runner
	req  request
	proj *report.Project
	// secrets are the values the step logs mask.
	secrets [][]byte
	// mu guards status and stepStart, which the heartbeat reads.
	mu        sync.Mutex
	status    *report.Status
	stepStart time.Time
}

func (r *Runner) run(ctx context.Context, req request) error {
	proj := report.Open(req.dir)
	status, err := proj.Status(req.id)
	if err != nil {
		return errors.Join(err, os.RemoveAll(req.secrets))
	}
	x := &execution{r: r, req: req, proj: proj, status: status, secrets: report.ReadSecrets(req.secrets)}

	cfg, err := config.Load(req.dir)
	if err != nil {
		return errors.Join(err, x.clean(ctx), x.finish(output.StateDone, int(output.ExitServer), err.Error()))
	}
	beatCtx, stopBeat := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { x.heartbeat(beatCtx) })
	err = x.steps(ctx, cfg)
	stopBeat()
	wg.Wait()

	state, code, reason := output.StateDone, int(output.ExitPassed), ""
	if x.failed() {
		code = int(output.ExitFailed)
	}
	if ctx.Err() != nil {
		state, code, reason = output.StateCanceled, int(output.ExitFailed), output.StateCanceled
	}
	// Cleanup comes before the final status, so whoever sees the run end
	// sees its leftovers gone too.
	return errors.Join(err, x.clean(ctx), x.finish(state, code, reason))
}

// clean removes the run's docker leftovers and secrets. A cancel doesn't
// stop it: cleaning up is what a cancel asks for.
func (x *execution) clean(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanTimeout)
	defer cancel()
	_, err := Clean(ctx, x.req.key, x.req.secrets)
	return err
}

func (x *execution) finish(state string, code int, reason string) error {
	x.mu.Lock()
	x.status.Finish(state, code, reason, time.Now())
	err := x.write()
	x.mu.Unlock()
	return errors.Join(err, x.proj.Release(x.req.id))
}

func (x *execution) failed() bool {
	for _, st := range x.status.Steps {
		if st.State == output.StateFail {
			return true
		}
	}
	return false
}

// write saves status.json with a fresh heartbeat. The caller holds mu.
func (x *execution) write() error {
	now := time.Now()
	x.status.Heartbeat = report.Stamp(now)
	for i := range x.status.Steps {
		if x.status.Steps[i].State == output.StateRunning {
			x.status.Steps[i].Seconds = int(now.Sub(x.stepStart).Seconds())
		}
	}
	return x.proj.WriteStatus(x.status)
}

func (x *execution) update(fn func()) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	fn()
	return x.write()
}

func (x *execution) heartbeat(ctx context.Context) {
	tick := time.NewTicker(x.r.beat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// A missed beat shows up as a stale heartbeat; there is nobody
			// to tell about it now.
			_ = x.update(func() {})
		}
	}
}

// steps runs the pending steps in order. A failed step without
// continue_on_fail stops the rest, and so does a cancel; they stay pending
// until finish marks them "not run".
func (x *execution) steps(ctx context.Context, cfg *config.Config) error {
	for i := 0; i < len(x.status.Steps) && ctx.Err() == nil; i++ {
		if x.status.Steps[i].State != output.StatePending {
			continue
		}
		step, ok := cfg.Step(x.status.Steps[i].Name)
		if !ok {
			return x.update(func() {
				x.status.Steps[i].State, x.status.Steps[i].Detail = output.StateFail, "not in "+config.File
			})
		}
		passed, err := x.step(ctx, i, &step)
		if err != nil {
			return err
		}
		if !passed && !step.ContinueOnFail {
			return nil
		}
	}
	return nil
}

// step runs one step, or skips it when its skip_if_unchanged files match
// the last successful run. It reports whether the step passed.
func (x *execution) step(ctx context.Context, i int, step *config.Step) (bool, error) {
	st := &x.status.Steps[i]
	hashes, cached := x.cache(step)
	if cached {
		return true, x.update(func() {
			st.State, st.Detail = output.StateSkip, strings.Join(step.SkipIfUnchanged, ", ")+" unchanged"
		})
	}
	if err := x.update(func() { st.State, x.stepStart = output.StateRunning, time.Now() }); err != nil {
		return false, err
	}
	code, detail, err := x.exec(ctx, step)
	if err != nil {
		detail = err.Error()
	}
	if step.JUnit != "" {
		// A report that can't be copied shows up as a step without one.
		_ = x.proj.SaveReports(x.req.id, step.Name, step.JUnit, x.stepStart.Add(-time.Second), x.secrets)
	}
	passed := err == nil && code == 0 && detail == ""
	if err := x.update(func() {
		st.Exit, st.Detail = &code, detail
		st.State = output.StateFail
		if passed {
			st.State = output.StateOK
		}
		st.Seconds = int(time.Since(x.stepStart).Seconds())
	}); err != nil {
		return false, err
	}
	if passed && hashes != nil {
		data, err := json.Marshal(hashes)
		if err != nil {
			return true, fmt.Errorf("encode cache of %s: %w", step.Name, err)
		}
		return true, report.WriteFile(x.cacheFile(step.Name), data)
	}
	return passed, nil
}

func (x *execution) cacheFile(step string) string {
	return filepath.Join(x.req.dir, cacheFolder, step+".json")
}

// cache hashes the skip_if_unchanged files and tells whether they match the
// last successful run. Without such files, or when one can't be read, the
// hashes are nil and the step runs.
func (x *execution) cache(step *config.Step) (map[string]string, bool) {
	if len(step.SkipIfUnchanged) == 0 {
		return nil, false
	}
	root, err := os.OpenRoot(x.req.dir)
	if err != nil {
		return nil, false
	}
	defer func() { _ = root.Close() }()
	hashes := map[string]string{}
	for _, name := range step.SkipIfUnchanged {
		sum, err := hashFile(root, name)
		if err != nil {
			return nil, false
		}
		hashes[name] = sum
	}
	data, err := os.ReadFile(x.cacheFile(step.Name))
	if err != nil {
		return hashes, false
	}
	var last map[string]string
	if json.Unmarshal(data, &last) != nil {
		return hashes, false
	}
	return hashes, maps.Equal(hashes, last)
}

// hashFile opens name through root, so a path or symlink that leaves the
// project fails.
func hashFile(root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// exec runs the step's command until it exits, times out or the run is
// canceled. detail says why a stopped step stopped.
func (x *execution) exec(ctx context.Context, step *config.Step) (code int, detail string, err error) {
	out, err := x.output(step.Name)
	if err != nil {
		return 0, "", err
	}
	defer func() { err = errors.Join(err, out.close()) }()
	// The step stops through its process group, not through the context.
	cmd := shell(context.WithoutCancel(ctx), x.req.dir, step.Run, nil)
	cmd.Env = append(append(append([]string{}, x.r.env...), x.vars(step.Name)...), x.stepEnv(step)...)
	cmd.Stdout, cmd.Stderr = out.w, out.w
	// Its own session makes the step the leader of a new process group, so
	// children started with & or nohup stay in reach.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	out.started()
	if err != nil {
		_, err = exitCode(err)
		return 0, "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timeout := time.NewTimer(step.Timeout)
	defer timeout.Stop()
	var waitErr error
	select {
	case waitErr = <-done:
		code, err = exitCode(waitErr)
		return code, "", err
	case <-timeout.C:
		detail = "timed out after " + output.Duration(step.Timeout)
	case <-ctx.Done():
		detail = output.StateCanceled
	}
	code, err = exitCode(x.r.stop(cmd.Process.Pid, done))
	return code, detail, err
}

// stepOutput is where a step writes: its log file, or, when the run has
// secrets, a pipe whose reader masks them on the way into the log. Secrets
// never reach the disk unmasked.
type stepOutput struct {
	// w is what the step gets as stdout and stderr.
	w    *os.File
	r    *os.File
	log  *os.File
	done chan error
}

func (x *execution) output(step string) (*stepOutput, error) {
	log, err := os.OpenFile(x.proj.LogFile(x.req.id, step), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create log: %w", err)
	}
	if len(x.secrets) == 0 {
		return &stepOutput{w: log, log: log}, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create log pipe: %w", err), log.Close())
	}
	o := &stepOutput{w: w, r: r, log: log, done: make(chan error, 1)}
	go func() {
		m := report.NewMasker(log, x.secrets)
		_, err := io.Copy(m, r)
		if errors.Is(err, os.ErrClosed) {
			// close gave up waiting for a leftover process.
			err = nil
		}
		o.done <- errors.Join(err, m.Flush())
	}()
	return o, nil
}

// started drops the runner's own copy of the pipe's write end, so the
// reader ends when the step's processes are gone.
func (o *stepOutput) started() {
	if o.r != nil {
		_ = o.w.Close()
	}
}

// close waits until the masker has written what the step printed, at most
// drain past the step's end, then closes the log.
func (o *stepOutput) close() error {
	var err error
	if o.r != nil {
		timer := time.NewTimer(drain)
		select {
		case err = <-o.done:
		case <-timer.C:
			_ = o.r.Close()
			err = <-o.done
		}
		timer.Stop()
		_ = o.r.Close()
	}
	return errors.Join(err, o.log.Close())
}

// stop ends the step whose session is sid: SIGTERM to all of it, SIGKILL to
// whatever is left after the grace period. It returns once bash is reaped.
func (r *Runner) stop(sid int, done <-chan error) error {
	signalSession(sid, syscall.SIGTERM)
	deadline := time.NewTimer(r.grace)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	var waitErr error
	exited := false
	for !exited || !gone(sid) {
		select {
		case waitErr = <-done:
			exited, done = true, nil
		case <-tick.C:
		case <-deadline.C:
			signalSession(sid, syscall.SIGKILL)
			if !exited {
				waitErr = <-done
			}
			return waitErr
		}
	}
	return waitErr
}

// signalSession signals the step's process group and every process left in
// its session, which catches children that moved to a group of their own.
func signalSession(sid int, sig syscall.Signal) {
	// ESRCH only means that part is gone already.
	_ = syscall.Kill(-sid, sig)
	for _, pid := range report.Members(sid) {
		_ = syscall.Kill(pid, sig)
	}
}

// gone tells whether nothing of the step's session is left. /proc sees past
// zombies that nobody reaped; without it the group signal check decides.
func gone(sid int) bool {
	if _, err := os.Stat("/proc/self/stat"); err == nil {
		return len(report.Members(sid)) == 0
	}
	return errors.Is(syscall.Kill(-sid, 0), syscall.ESRCH)
}

// vars are the BURRO_* variables of docs/reference/config.md.
func (x *execution) vars(step string) []string {
	return []string{
		"BURRO_RUN_ID=" + x.req.id,
		"BURRO_RUN=" + x.req.key,
		"BURRO_STEP=" + step,
		"BURRO_TREE=" + x.status.Tree,
		"BURRO_SECRETS=" + x.req.secrets,
		"COMPOSE_PROJECT_NAME=" + x.req.key,
	}
}

// stepEnv is the step's env with $BURRO_* references expanded. Other $
// references stay as written.
func (x *execution) stepEnv(step *config.Step) []string {
	known := map[string]string{}
	for _, kv := range x.vars(step.Name) {
		k, v, _ := strings.Cut(kv, "=")
		known[k] = v
	}
	env := make([]string, 0, len(step.Env))
	for _, k := range slices.Sorted(maps.Keys(step.Env)) {
		v := burroVar.ReplaceAllStringFunc(step.Env[k], func(ref string) string {
			m := burroVar.FindStringSubmatch(ref)
			if value, ok := known[m[1]+m[2]]; ok {
				return value
			}
			return ref
		})
		env = append(env, k+"="+v)
	}
	return env
}

// Exec runs script through a login bash in dir, with args as $1 and on,
// output to out, and returns its exit code. The executor's exec runs here
// too, so one function starts bash.
func Exec(ctx context.Context, dir, script string, args []string, out io.Writer) (int, error) {
	cmd := shell(ctx, dir, script, args)
	cmd.Stdout, cmd.Stderr = out, out
	// A process the command left running must not hold exec's output open.
	cmd.WaitDelay = drain
	return exitCode(cmd.Run())
}

// shell runs script in dir through a login bash, so PATH and the rest of
// the user's profile apply the way they do in an ssh session.
func shell(ctx context.Context, dir, script string, args []string) *exec.Cmd {
	//nolint:gosec // G204: running the user's command on their own server is the job.
	cmd := exec.CommandContext(ctx, "bash", append([]string{"-lc", script, "bash"}, args...)...)
	cmd.Dir = dir
	return cmd
}

// exitCode turns the result of a finished bash into its exit code. A
// command killed by a signal reports 128 plus the signal, the way shells do.
func exitCode(err error) (int, error) {
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		// ErrWaitDelay means bash exited 0 and left a process holding its
		// output.
		return 0, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return 0, output.NewError(output.ExitServer, fmt.Sprintf("start bash on the server: %v", err),
			"install bash on the server")
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	return exit.ExitCode(), nil
}
