package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/timhartmann7/burro/internal/agent"
	"github.com/timhartmann7/burro/internal/config"
	"github.com/timhartmann7/burro/internal/hosts"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/tree"
)

// waitCall is how long one executor wait blocks before the client calls
// again.
const waitCall = 4 * time.Minute

// cleanTimeout bounds clean, which waits for docker to remove what the run
// left.
const cleanTimeout = 3 * time.Minute

// erase returns to the start of the line and clears it.
const erase = "\r\x1b[K"

// defaultContext is the --context of log, which its cut line leaves out.
const defaultContext = "5"

// cmdLog names the log command and its executor call.
const cmdLog = "log"

// defaultMaxLines is how many lines the summary of wait and failures takes.
const defaultMaxLines = 40

// pauses are the waits between reconnects after ssh breaks during wait.
func pauses() []time.Duration {
	return []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
}

func (a *App) run(ctx context.Context, p *parsed) error {
	pipeline := config.DefaultPipeline
	if len(p.args) == 1 {
		pipeline = p.args[0]
	}
	from := p.flags["from"]
	only, hasOnly := p.flags["only"]
	var onlyList []string
	if hasOnly {
		onlyList = strings.Split(only, ",")
	}
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	selected, err := proj.cfg.Select(pipeline, from, onlyList)
	if err != nil {
		return err
	}
	// Secrets are read before anything reaches the server, so a missing
	// one costs nothing there.
	secrets, err := proj.cfg.Secrets(selected, proj.repo.Root(), a.getenv("HOME"))
	if err != nil {
		return err
	}
	var stdin io.Reader
	if len(secrets) > 0 {
		data, err := json.Marshal(secrets)
		if err != nil {
			return fmt.Errorf("encode secrets: %w", err)
		}
		stdin = bytes.NewReader(data)
	}
	synced, err := a.push(ctx, proj)
	if err != nil {
		return err
	}
	args := proj.call("start", "--tree", synced.Tree)
	if from != "" {
		args = append(args, "--from", from)
	}
	if hasOnly {
		args = append(args, "--only", only)
	}
	startCtx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	reply, err := a.call(startCtx, proj.host, stdin, append(args, pipeline)...)
	if err != nil {
		return err
	}
	if reply.Started == nil {
		return noResult(proj.host, "run")
	}
	reply.Started.Synced = synced.Changed
	if err := p.print.Started(reply.Started, synced.Took); err != nil {
		return err
	}
	if p.human {
		a.note("next: burro wait\n")
	}
	return nil
}

func noResult(h *hosts.Host, what string) error {
	return output.NewError(output.ExitServer,
		fmt.Sprintf("host %s: the server binary sent no %s result", h.Name(), what),
		"run burro doctor "+h.Name())
}

// runID is the optional run id argument; empty means the last run.
func runID(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], config.ValidRun(args[0])
}

// withID appends the run id to an executor call when there is one.
func withID(args []string, id string) []string {
	if id == "" {
		return args
	}
	return append(args, id)
}

// exitOf is the exit code a run gives the command that shows it.
func exitOf(s *output.Summary) error {
	if s.State == output.StateRunning {
		return output.Silent(output.ExitRunning)
	}
	if *s.Exit == 0 {
		return nil
	}
	return output.Silent(output.Code(*s.Exit))
}

// caller makes one executor call: short for one that acts, read for one
// that only reads.
type caller func(ctx context.Context, h *hosts.Host, args ...string) (*agent.Reply, error)

// summaryCall runs a status, failures or cancel call and returns its
// summary.
func (a *App) summaryCall(ctx context.Context, proj *project, call caller, action, id string) (*agent.Reply, error) {
	reply, err := call(ctx, proj.host, withID(proj.call(action), id)...)
	if err != nil {
		return nil, err
	}
	if reply.Summary == nil {
		return nil, noResult(proj.host, action)
	}
	return reply, nil
}

func (a *App) wait(ctx context.Context, p *parsed) error {
	id, err := runID(p.args)
	if err != nil {
		return err
	}
	timeout, err := time.ParseDuration(p.flags["timeout"])
	if err != nil || timeout <= 0 {
		return output.NewError(output.ExitUsage, fmt.Sprintf("wait: --timeout %q is not a positive duration", p.flags["timeout"]),
			"pass a duration, for example --timeout 30m")
	}
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	if err := a.connect(ctx, proj.host); err != nil {
		return err
	}
	w := &waiter{app: a, proj: proj, id: id, human: p.human, line: &progressLine{w: a.stderr, on: p.human}}
	if p.human {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
	}
	sum, err := w.loop(ctx, time.Now().Add(timeout))
	w.line.clear()
	if errors.Is(err, errInterrupted) {
		// stderr is the terminal of the person who pressed Ctrl+C; when it
		// fails there is nobody to tell.
		_, _ = io.WriteString(a.stderr, "still running · come back with burro wait\n")
		return output.Silent(output.ExitRunning)
	}
	if err != nil {
		return err
	}
	if err := p.print.Summary(sum, defaultMaxLines); err != nil {
		return err
	}
	return exitOf(sum)
}

var (
	// errInterrupted ends a wait that Ctrl+C stopped.
	errInterrupted = errors.New("interrupted")
	// errRetry asks for the next wait call after a broken one.
	errRetry = errors.New("try again")
)

// waiter repeats executor wait calls until the run ends.
type waiter struct {
	app      *App
	proj     *project
	id       string
	human    bool
	line     *progressLine
	failures int
}

// loop returns the summary of the finished run. A canceled ctx, which is
// Ctrl+C in human mode, or the deadline end it early; the run goes on
// either way.
func (w *waiter) loop(ctx context.Context, deadline time.Time) (*output.Summary, error) {
	for {
		left := time.Until(deadline).Round(time.Second)
		if left <= 0 {
			return nil, output.NewError(output.ExitRunning, fmt.Sprintf("%s is still running", w.shown()),
				fmt.Sprintf("run burro wait %s again; the run keeps going", w.id))
		}
		sum, err := w.next(ctx, min(left, waitCall))
		if ctx.Err() != nil {
			return nil, errInterrupted
		}
		if errors.Is(err, errRetry) {
			continue
		}
		if err != nil || sum.State != output.StateRunning {
			return sum, err
		}
	}
}

// next makes one wait call. A broken connection is no error until it
// stays broken through every pause; wait only reads, so trying again is
// safe. After a broken call it pauses and returns errRetry.
func (w *waiter) next(ctx context.Context, maxWait time.Duration) (*output.Summary, error) {
	h := w.proj.host
	reply, err := w.call(ctx, maxWait)
	if err != nil {
		if w.failures == len(pauses()) || ctx.Err() != nil {
			return nil, err
		}
		pause(ctx, pauses()[w.failures])
		w.failures++
		return nil, errRetry
	}
	w.failures = 0
	if err := replyError(h, reply); err != nil {
		return nil, err
	}
	if reply.Summary == nil {
		return nil, noResult(h, "wait")
	}
	// Later calls ask for this run, even when a newer one starts.
	w.id = reply.Summary.Run
	return reply.Summary, nil
}

func (w *waiter) call(ctx context.Context, maxWait time.Duration) (*agent.Reply, error) {
	args := w.proj.call("wait", "--max", maxWait.String())
	if w.human {
		args = append(args, "--progress")
	}
	ctx, cancel := context.WithTimeout(ctx, maxWait+shortTimeout)
	defer cancel()
	return exchange(ctx, w.proj.host, nil, w.line.draw, withID(args, w.id)...)
}

// pause waits d, or less when ctx ends first.
func pause(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (w *waiter) shown() string {
	if w.id == "" {
		return "the last run"
	}
	return w.id
}

// progressLine is the one redrawn stderr line of wait in human mode.
type progressLine struct {
	w     io.Writer
	on    bool
	shown bool
}

func (l *progressLine) draw(g output.Progress) {
	if !l.on {
		return
	}
	// A failed write to stderr only costs the progress line.
	_, _ = io.WriteString(l.w, erase+g.Line())
	l.shown = true
}

func (l *progressLine) clear() {
	if l.shown {
		_, _ = io.WriteString(l.w, erase)
		l.shown = false
	}
}

func (a *App) status(ctx context.Context, p *parsed) error {
	id, err := runID(p.args)
	if err != nil {
		return err
	}
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	local, err := proj.repo.Hash(ctx)
	if err != nil {
		return err
	}
	reply, err := a.summaryCall(ctx, proj, a.read, "status", id)
	if err != nil {
		return err
	}
	if local != reply.Tree {
		reply.Summary.Local = local[:tree.ShortLen]
	}
	if err := p.print.Summary(reply.Summary, 0); err != nil {
		return err
	}
	return exitOf(reply.Summary)
}

// logRequest is a log command line after its local checks.
type logRequest struct {
	id, step, grep string
	tail           int
}

func parseLog(p *parsed) (*logRequest, error) {
	id, err := runID(p.args)
	if err != nil {
		return nil, err
	}
	r := &logRequest{id: id, step: p.flags["step"], grep: p.flags["grep"]}
	if r.step == "" {
		return nil, output.NewError(output.ExitUsage, "log: --step is missing",
			"pass the step whose log you want, for example --step e2e; burro status lists the steps")
	}
	if err := config.ValidName("step", r.step); err != nil {
		return nil, err
	}
	if r.tail, err = positive(p, cmdLog, "tail"); err != nil {
		return nil, err
	}
	if lines, err := strconv.Atoi(p.flags["context"]); err != nil || lines < 0 {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("log: --context %q is not a number of lines", p.flags["context"]),
			"pass a number of lines, for example --context 3")
	}
	if _, err := regexp.Compile(r.grep); err != nil {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("log: --grep %q is not a valid regular expression", r.grep),
			"pass a Go RE2 pattern, for example --grep 'timed out|FAIL'")
	}
	return r, nil
}

func (a *App) log(ctx context.Context, p *parsed) error {
	r, err := parseLog(p)
	if err != nil {
		return err
	}
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	args := proj.call(cmdLog, "--step", r.step, "--tail", p.flags["tail"], "--context", p.flags["context"])
	if r.grep != "" {
		args = append(args, "--grep", r.grep)
	}
	reply, err := a.read(ctx, proj.host, withID(args, r.id)...)
	if err != nil {
		return err
	}
	l := reply.Log
	if l == nil {
		return noResult(proj.host, cmdLog)
	}
	if l.Hidden > 0 {
		// The command that shows the cut part asks for twice the lines.
		more := []string{binaryName, cmdLog, l.Run, "--step", l.Step}
		if r.grep != "" {
			more = append(more, "--grep", hosts.Quote(r.grep))
			if p.flags["context"] != defaultContext {
				more = append(more, "--context", p.flags["context"])
			}
		}
		l.More = strings.Join(append(more, "--tail", strconv.Itoa(2*r.tail)), " ")
	}
	return p.print.Log(l)
}

func (a *App) cancel(ctx context.Context, p *parsed) error {
	id, err := runID(p.args)
	if err != nil {
		return err
	}
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	if err := a.connect(ctx, proj.host); err != nil {
		return err
	}
	reply, err := a.summaryCall(ctx, proj, a.short, "cancel", id)
	if err != nil {
		return err
	}
	return p.print.Summary(reply.Summary, 0)
}

func (a *App) failures(ctx context.Context, p *parsed) error {
	id, err := runID(p.args)
	if err != nil {
		return err
	}
	maxLines, err := positive(p, "failures", "max-lines")
	if err != nil {
		return err
	}
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	reply, err := a.summaryCall(ctx, proj, a.read, "failures", id)
	if err != nil {
		return err
	}
	if err := p.print.Summary(reply.Summary, maxLines); err != nil {
		return err
	}
	return exitOf(reply.Summary)
}

func (a *App) clean(ctx context.Context, p *parsed) error {
	id, err := runID(p.args)
	if err != nil {
		return err
	}
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	if err := a.connect(ctx, proj.host); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cleanTimeout)
	defer cancel()
	reply, err := a.call(ctx, proj.host, nil, withID(proj.call("clean"), id)...)
	if err != nil {
		return err
	}
	if reply.Cleaned == nil {
		return noResult(proj.host, "clean")
	}
	return p.print.Cleaned(reply.Cleaned)
}

// positive reads flag of cmd as a number of lines above 0.
func positive(p *parsed, cmd, flag string) (int, error) {
	n, err := strconv.Atoi(p.flags[flag])
	if err != nil || n < 1 {
		return 0, output.NewError(output.ExitUsage, fmt.Sprintf("%s: --%s %q is not a positive number", cmd, flag, p.flags[flag]),
			fmt.Sprintf("pass a number of lines, for example --%s 100", flag))
	}
	return n, nil
}
