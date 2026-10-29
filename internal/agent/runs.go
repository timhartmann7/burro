package agent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/timhartmann7/burro/internal/config"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/report"
	"github.com/timhartmann7/burro/internal/tree"
)

const (
	// waitMax is how long one wait call blocks by default; the client calls
	// again until the run ends.
	waitMax = 4 * time.Minute
	// progressEvery is how often wait --progress prints while nothing
	// changes.
	progressEvery = 10 * time.Second
	// cancelTimeout bounds how long cancel waits for the runner, which gives
	// the step 10 seconds between SIGTERM and SIGKILL.
	cancelTimeout = 20 * time.Second
	// rsyncTemp keeps rsync's temp files out of the synced folders, so two
	// syncs at once can't delete each other's files.
	rsyncTemp = ".burro/tmp"
	// readyTimeout bounds how long start waits for the runner to take over.
	readyTimeout = 10 * time.Second
	// cleanTimeout bounds one clean call.
	cleanTimeout = 2 * time.Minute
	// maxSecrets caps the secrets start reads from stdin.
	maxSecrets = 8 << 20
)

// flags is the flag set of one run call; every call takes --path.
type flags struct {
	set  *flag.FlagSet
	path string
}

func newFlags(action string) *flags {
	f := &flags{set: flag.NewFlagSet(action, flag.ContinueOnError)}
	f.set.SetOutput(io.Discard)
	f.set.StringVar(&f.path, "path", "", "project folder")
	return f
}

// parse reads args and returns the positional arguments, at most one.
func (f *flags) parse(args []string) (string, error) {
	if err := f.set.Parse(args); err != nil || f.path == "" || f.set.NArg() > 1 {
		return "", output.NewError(output.ExitUsage, fmt.Sprintf("agent %s: bad arguments %q", f.set.Name(), args),
			"rerun with the same burro version")
	}
	return f.set.Arg(0), nil
}

// run resolves the project folder and the run id, the last run when id is
// empty.
func (e *Executor) run(path, id string) (dir string, proj *report.Project, runID string, err error) {
	if dir, err = e.projectDir(path); err != nil {
		return "", nil, "", err
	}
	proj = report.Open(dir)
	if id == "" {
		id, err = proj.Last()
		return dir, proj, id, err
	}
	return dir, proj, id, config.ValidRun(id)
}

// prepare makes the project folder ready for rsync, unless a run holds the
// lock.
func (e *Executor) prepare(args []string) error {
	f := newFlags("prepare")
	if _, err := f.parse(args); err != nil {
		return err
	}
	dir, _ := e.resolve(f.path)
	if err := os.MkdirAll(filepath.Join(dir, rsyncTemp), 0o700); err != nil {
		return fmt.Errorf("create project folder: %w", err)
	}
	proj := report.Open(dir)
	return proj.Locked(func() error {
		if id := proj.Holder(); id != "" {
			return report.Busy(id)
		}
		return nil
	})
}

// start takes the lock, creates the run folder and starts the runner
// detached in its own session, then returns at once.
func (e *Executor) start(args []string) (*output.Started, error) {
	f := newFlags("start")
	var treeHash, from, only string
	f.set.StringVar(&treeHash, "tree", "", "tree hash")
	f.set.StringVar(&from, "from", "", "first step")
	f.set.StringVar(&only, "only", "", "steps to run")
	pipeline, err := f.parse(args)
	if err != nil {
		return nil, err
	}
	secrets, err := e.readSecrets()
	if err != nil {
		return nil, err
	}
	dir, selected, err := e.selection(f.path, pipeline, from, only)
	if err != nil {
		return nil, err
	}
	proj := report.Open(dir)
	started := &output.Started{Pipeline: pipeline, Tree: short(treeHash), Steps: []string{}}
	err = proj.Locked(func() error {
		if id := proj.Holder(); id != "" {
			return report.Busy(id)
		}
		id, err := proj.NewRun()
		if err != nil {
			return err
		}
		started.Run = id
		status := newStatus(id, pipeline, treeHash, selected)
		for _, st := range status.Steps {
			if st.State == output.StatePending {
				started.Steps = append(started.Steps, st.Name)
			}
		}
		if err := proj.WriteStatus(status); err != nil {
			return err
		}
		return e.launch(proj, dir, id, secrets)
	})
	return started, err
}

// readSecrets reads the secrets the client sends on stdin as one JSON
// object of name to contents. They never travel in arguments.
func (e *Executor) readSecrets() (map[string][]byte, error) {
	data, err := io.ReadAll(io.LimitReader(e.stdin, maxSecrets+1))
	if err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}
	secrets := map[string][]byte{}
	if len(data) == 0 {
		return secrets, nil
	}
	if len(data) > maxSecrets {
		return nil, output.NewError(output.ExitUsage, "the secrets of this run are over 8 MiB",
			"keep only the secrets the steps need in [secrets] of "+config.File)
	}
	if err := json.Unmarshal(data, &secrets); err != nil {
		return nil, output.NewError(output.ExitServer, "the secrets sent with the run are broken", "rerun")
	}
	for name := range secrets {
		if err := config.ValidName("secret", name); err != nil {
			return nil, err
		}
	}
	return secrets, nil
}

// selection reads burro.toml in the project folder and selects the steps
// of the run, the same way the client checked them.
func (e *Executor) selection(path, pipeline, from, only string) (string, []config.Selected, error) {
	dir, err := e.projectDir(path)
	if err != nil {
		return "", nil, err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return "", nil, err
	}
	var onlyList []string
	if only != "" {
		onlyList = strings.Split(only, ",")
	}
	selected, err := cfg.Select(pipeline, from, onlyList)
	return dir, selected, err
}

// newStatus is status.json of a run that starts now: the selected steps
// pending, the others not run with the reason.
func newStatus(id, pipeline, treeHash string, selected []config.Selected) *report.Status {
	now := report.Stamp(time.Now())
	status := &report.Status{Run: id, Pipeline: pipeline, Tree: treeHash, State: output.StateRunning, Started: now, Heartbeat: now}
	for i := range selected {
		sel := &selected[i]
		step := report.Step{Name: sel.Step.Name, State: output.StatePending}
		if sel.Skip != "" {
			step.State, step.Detail = output.StateNotRun, sel.Skip
		}
		status.Steps = append(status.Steps, step)
	}
	return status
}

// launch takes the lock for run id, writes its secrets and starts its
// runner. A runner that doesn't start leaves a run that failed with the
// reason.
func (e *Executor) launch(proj *report.Project, dir, id string, values map[string][]byte) error {
	key := proj.Key(id)
	secrets, err := e.writeSecrets(proj, key, values)
	if err != nil {
		return errors.Join(err, e.abandon(proj, id, err))
	}
	if err := proj.Take(id); err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(proj.RunDir(id), "runner.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("create runner log: %w", err)
	}
	defer func() { _ = out.Close() }()
	ready, readyW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create ready pipe: %w", err)
	}
	defer func() { _ = ready.Close() }()
	args := append(append([]string{}, e.runner[1:]...), "--path", dir, "--run", id, "--key", key, "--secrets", secrets, "--ready")
	// The runner outlives this call, so no context may end it.
	//nolint:gosec // G204: the runner is this same server binary.
	cmd := exec.CommandContext(context.Background(), e.runner[0], args...)
	cmd.Dir = dir
	// No stdin and output into the run folder: nothing of the ssh session
	// stays open, so ssh returns as soon as start does.
	cmd.Stdout, cmd.Stderr = out, out
	cmd.ExtraFiles = []*os.File{readyW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	// Only the runner keeps the write end, so its close means ready.
	_ = readyW.Close()
	if err != nil {
		return errors.Join(fmt.Errorf("start runner: %w", err), e.abandon(proj, id, err))
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release runner: %w", err)
	}
	if err := proj.WritePid(id, pid); err != nil {
		return err
	}
	// EOF comes when the runner closes it or exits; a runner that died
	// shows up later as "runner died".
	if err := ready.SetReadDeadline(time.Now().Add(readyTimeout)); err == nil {
		_, _ = io.Copy(io.Discard, ready)
	}
	return nil
}

// writeSecrets creates the tmpfs folder of run key with one file per
// secret, after removing the folders that dead runs of the project left.
// The caller holds the lock, so no run of the project is alive.
func (e *Executor) writeSecrets(proj *report.Project, key string, values map[string][]byte) (string, error) {
	if err := os.Mkdir(e.secrets, 0o700); err == nil {
		// Every user's runs share the folder, the way they share /dev/shm;
		// the sticky bit keeps each user's run folders their own.
		if err := os.Chmod(e.secrets, 0o777|os.ModeSticky); err != nil {
			return "", fmt.Errorf("create secrets folder: %w", err)
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("create secrets folder: %w", err)
	}
	dead, _ := filepath.Glob(filepath.Join(e.secrets, proj.KeyPrefix()+"*"))
	for _, d := range dead {
		if err := os.RemoveAll(d); err != nil {
			return "", fmt.Errorf("remove secrets of a dead run: %w", err)
		}
	}
	dir := filepath.Join(e.secrets, key)
	// Mkdir fails on a folder that is there already, so nobody else's
	// folder can receive the secrets.
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create secrets folder: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("open secrets folder: %w", err)
	}
	defer func() { _ = root.Close() }()
	for name, value := range values {
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", fmt.Errorf("write secret %s: %w", name, err)
		}
		_, err = f.Write(value)
		if err := errors.Join(err, f.Close()); err != nil {
			return "", fmt.Errorf("write secret %s: %w", name, err)
		}
	}
	return dir, nil
}

// abandon records a run whose runner never started.
func (e *Executor) abandon(proj *report.Project, id string, cause error) error {
	status, err := proj.Status(id)
	if err != nil {
		return err
	}
	status.Finish(output.StateDone, int(output.ExitServer), "runner did not start: "+cause.Error(), time.Now())
	return proj.WriteStatus(status)
}

func (e *Executor) status(args []string) (*output.Summary, string, error) {
	return e.current("status", args, false)
}

// failures is the summary wait prints at the end, for any run.
func (e *Executor) failures(args []string) (*output.Summary, error) {
	sum, _, err := e.current("failures", args, true)
	return sum, err
}

// current returns the summary of a run as it is now, and its full tree
// hash.
func (e *Executor) current(action string, args []string, details bool) (*output.Summary, string, error) {
	f := newFlags(action)
	id, err := f.parse(args)
	if err != nil {
		return nil, "", err
	}
	dir, proj, id, err := e.run(f.path, id)
	if err != nil {
		return nil, "", err
	}
	status, err := proj.Current(id, time.Now())
	if err != nil {
		return nil, "", err
	}
	sum, err := summary(dir, proj, status, details)
	if err != nil {
		return nil, "", err
	}
	return sum, status.Tree, nil
}

// cleanRun removes the docker leftovers and secrets of a run that no
// longer runs; those of a live run are still in use.
func (e *Executor) cleanRun(args []string) (*output.Cleaned, error) {
	f := newFlags("clean")
	id, err := f.parse(args)
	if err != nil {
		return nil, err
	}
	_, proj, id, err := e.run(f.path, id)
	if err != nil {
		return nil, err
	}
	if _, err := proj.Status(id); err != nil {
		return nil, err
	}
	err = proj.Locked(func() error {
		if proj.Holder() == id {
			return report.Busy(id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	key := proj.Key(id)
	ctx, cancel := context.WithTimeout(context.Background(), cleanTimeout)
	defer cancel()
	cleaned, err := e.clean(ctx, key, filepath.Join(e.secrets, key))
	if err != nil {
		return nil, err
	}
	cleaned.Run = id
	return cleaned, nil
}

// wait checks status.json once a second and returns the summary when the
// run ends or --max passes. With --progress it prints a progress reply on
// every step change and every 10 seconds.
func (e *Executor) wait(args []string) (*output.Summary, error) {
	f := newFlags("wait")
	maxWait := f.set.Duration("max", waitMax, "longest wait")
	progress := f.set.Bool("progress", false, "print progress")
	id, err := f.parse(args)
	if err != nil {
		return nil, err
	}
	dir, proj, id, err := e.run(f.path, id)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(*maxWait)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	reporter := &progressReporter{e: e, on: *progress}
	for {
		now := time.Now()
		status, err := proj.Current(id, now)
		if err != nil {
			return nil, err
		}
		if status.State != output.StateRunning {
			return summary(dir, proj, status, true)
		}
		if err := reporter.report(status, now); err != nil {
			return nil, err
		}
		if !now.Before(deadline) {
			return summary(dir, proj, status, false)
		}
		<-tick.C
	}
}

// progressReporter prints a progress reply when the running step changes
// and every 10 seconds.
type progressReporter struct {
	e    *Executor
	on   bool
	last string
	sent time.Time
}

func (r *progressReporter) report(s *report.Status, now time.Time) error {
	if !r.on {
		return nil
	}
	for i, st := range s.Steps {
		if st.State != output.StateRunning || (st.Name == r.last && now.Sub(r.sent) < progressEvery) {
			continue
		}
		r.last, r.sent = st.Name, now
		return r.e.write(&Reply{Progress: &output.Progress{Step: st.Name, Index: i + 1, Total: len(s.Steps), Seconds: st.Seconds}})
	}
	return nil
}

// cancel sends SIGTERM to the runner and waits until it has stopped the
// step and recorded the run as canceled.
func (e *Executor) cancel(args []string) (*output.Summary, error) {
	f := newFlags("cancel")
	id, err := f.parse(args)
	if err != nil {
		return nil, err
	}
	dir, proj, id, err := e.run(f.path, id)
	if err != nil {
		return nil, err
	}
	status, err := proj.Current(id, time.Now())
	if err != nil {
		return nil, err
	}
	if status.State == output.StateRunning {
		if err := proj.Signal(id, syscall.SIGTERM); err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(cancelTimeout)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for status.State == output.StateRunning {
		if time.Now().After(deadline) {
			return nil, output.NewError(output.ExitServer, fmt.Sprintf("the runner of %s did not stop in %s", id, cancelTimeout),
				"run burro status "+id+" in a minute")
		}
		<-tick.C
		if status, err = proj.Current(id, time.Now()); err != nil {
			return nil, err
		}
	}
	return summary(dir, proj, status, false)
}

// summary builds what status, wait, failures and cancel return. Details
// are failures and log tails; tails need the drop filters of burro.toml.
func summary(dir string, proj *report.Project, status *report.Status, details bool) (*output.Summary, error) {
	cleaner := report.NewCleaner(nil)
	if details {
		cfg, err := config.Load(dir)
		if err != nil {
			return nil, err
		}
		cleaner = report.NewCleaner(cfg.Drop())
	}
	sum, err := proj.Summary(status, cleaner, details, maxShown, time.Now())
	if err != nil {
		return nil, err
	}
	sum.Tree = short(sum.Tree)
	return sum, nil
}

func short(hash string) string {
	return hash[:min(len(hash), tree.ShortLen)]
}

// log returns the clean log of one step: its last lines, or the matches of
// --grep with context.
func (e *Executor) log(args []string) (*output.Log, error) {
	f := newFlags("log")
	step := f.set.String("step", "", "step")
	grep := f.set.String("grep", "", "pattern")
	tail := f.set.Int("tail", 0, "lines")
	around := f.set.Int("context", 0, "lines around a match")
	id, err := f.parse(args)
	if err != nil {
		return nil, err
	}
	if *tail < 1 || *around < 0 {
		return nil, output.NewError(output.ExitUsage, "agent log: want --tail above 0 and --context of 0 or more",
			"rerun with the same burro version")
	}
	re, err := regexp.Compile(*grep)
	if err != nil {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("--grep %q is not a valid regular expression", *grep),
			"pass a Go RE2 pattern, for example --grep 'timed out|FAIL'")
	}
	dir, proj, id, err := e.run(f.path, id)
	if err != nil {
		return nil, err
	}
	status, err := proj.Status(id)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(status.Steps))
	for _, st := range status.Steps {
		names = append(names, st.Name)
	}
	if !slices.Contains(names, *step) {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("run %s has no step %q", id, *step),
			"use one of: "+strings.Join(names, ", "))
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	result := &output.Log{Run: id, Step: *step, Grep: *grep, Lines: []string{}}
	file, err := os.Open(proj.LogFile(id, *step))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open log of %s: %w", *step, err)
	}
	defer func() { _ = file.Close() }()
	cleaner := report.NewCleaner(cfg.Drop())
	if *grep != "" {
		result.Lines, result.Matches, result.Hidden, err = cleaner.Grep(file, re, *around, *tail, maxShown)
	} else {
		result.Lines, result.Hidden, err = cleaner.Tail(file, *tail, maxShown)
	}
	if result.Lines == nil {
		result.Lines = []string{}
	}
	return result, err
}
