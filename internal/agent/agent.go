// Package agent is the server side of burro: the short calls the client makes
// over ssh as `burro agent <action>`. Each call prints one JSON object.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/timhartmann7/burro/internal/output"
)

const (
	// keepLogs is how many exec logs stay on the server.
	keepLogs = 20
	// maxShown keeps the reply under the 1 MiB the client accepts, whatever
	// --max-lines asks for.
	maxShown = 512 << 10
	// execTimeout bounds one exec call, matching the default step timeout.
	execTimeout = 60 * time.Minute
	// logFolder is where exec logs live, relative to the project folder.
	logFolder  = ".burro/exec"
	actionExec = "exec"
	// RunnerCall is the agent call that starts a runner: `burro agent
	// runner …`. The executor starts it, cli dispatches it.
	RunnerCall = "runner"
)

var logName = regexp.MustCompile(`^e(\d+)\.log$`)

// Reply is the one JSON object an agent call prints. wait with --progress
// prints progress replies before it.
type Reply struct {
	Error    *Failure         `json:"error,omitempty"`
	Exec     *output.Exec     `json:"exec,omitempty"`
	Started  *output.Started  `json:"started,omitempty"`
	Summary  *output.Summary  `json:"summary,omitempty"`
	Log      *output.Log      `json:"log,omitempty"`
	Progress *output.Progress `json:"progress,omitempty"`
	// Tree is the full tree hash of the run; the summary shows it short.
	Tree string `json:"tree,omitempty"`
}

// Failure is an error the client reports as its own, with the same exit code
// and fix line.
type Failure struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// Shell runs script through bash in dir with args, writes its output to out
// and returns its exit code. runner.Exec is the one implementation, passed
// in because the executor may not import the runner.
type Shell func(ctx context.Context, dir, script string, args []string, out io.Writer) (int, error)

// Executor runs agent calls on the server.
type Executor struct {
	stdin   io.Reader
	stdout  io.Writer
	home    string
	runner  []string
	secrets string
	shell   Shell
}

// New returns an executor that reads scripts from stdin, writes its reply to
// stdout and resolves relative project paths against home. runner is the
// command line that starts a runner, secrets the tmpfs folder that holds the
// secrets folder of each run.
func New(stdin io.Reader, stdout io.Writer, home string, runner []string, secrets string, shell Shell) *Executor {
	return &Executor{stdin: stdin, stdout: stdout, home: home, runner: runner, secrets: secrets, shell: shell}
}

// Run handles one agent call. Failures go into the reply; only a reply that
// can't be written is an error.
func (e *Executor) Run(args []string) error {
	if len(args) == 0 {
		args = []string{""}
	}
	reply, err := e.dispatch(args[0], args[1:])
	if err != nil {
		reply = &Reply{Error: failureOf(err)}
	}
	return e.write(reply)
}

func (e *Executor) dispatch(action string, args []string) (*Reply, error) {
	var reply Reply
	var err error
	switch action {
	case actionExec:
		reply.Exec, err = e.exec(args)
	case "prepare":
		err = e.prepare(args)
	case "start":
		reply.Started, err = e.start(args)
	case "status":
		reply.Summary, reply.Tree, err = e.status(args)
	case "wait":
		reply.Summary, err = e.wait(args)
	case "log":
		reply.Log, err = e.log(args)
	case "cancel":
		reply.Summary, err = e.cancel(args)
	default:
		err = output.NewError(output.ExitServer, fmt.Sprintf("unknown agent call %q", action),
			"rerun; burro replaces the server binary when versions differ")
	}
	return &reply, err
}

func (e *Executor) write(reply *Reply) error {
	data, err := json.Marshal(reply)
	if err != nil {
		return fmt.Errorf("encode reply: %w", err)
	}
	if _, err := e.stdout.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write reply: %w", err)
	}
	return nil
}

func failureOf(err error) *Failure {
	var coded *output.Error
	if errors.As(err, &coded) {
		return &Failure{Code: int(coded.Code()), Message: err.Error(), Fix: coded.Fix()}
	}
	return &Failure{Code: int(output.ExitServer), Message: err.Error(), Fix: "run burro doctor"}
}

// execRequest is one exec call after parsing.
type execRequest struct {
	path     string
	maxLines int
	script   bool
	argv     []string
}

func parseExec(args []string) (*execRequest, error) {
	var r execRequest
	flags := flag.NewFlagSet(actionExec, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&r.path, "path", "", "project folder")
	flags.IntVar(&r.maxLines, "max-lines", 0, "lines in the reply")
	flags.BoolVar(&r.script, "file", false, "run a script read from stdin")
	if err := flags.Parse(args); err != nil {
		return nil, output.NewError(output.ExitUsage, "agent exec: "+err.Error(), "rerun with the same burro version")
	}
	r.argv = flags.Args()
	if r.path == "" || r.maxLines < 1 || (len(r.argv) == 0 && !r.script) {
		return nil, output.NewError(output.ExitUsage, "agent exec: want --path, --max-lines and a command",
			"rerun with the same burro version")
	}
	return &r, nil
}

// resolve turns path into an absolute folder against the home folder, and
// the way the user wrote it.
func (e *Executor) resolve(path string) (dir, shown string) {
	if filepath.IsAbs(path) {
		return path, path
	}
	return filepath.Join(e.home, path), "~/" + path
}

// projectDir resolves path and checks that the folder is there.
func (e *Executor) projectDir(path string) (string, error) {
	dir, shown := e.resolve(path)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", output.NewError(output.ExitServer, fmt.Sprintf("project folder %s does not exist on the server", shown),
			"run burro sync first")
	}
	return dir, nil
}

// exec runs a command, or a script read from stdin, in the project folder.
// The whole output goes to the next e<n>.log; the reply carries its head.
func (e *Executor) exec(args []string) (*output.Exec, error) {
	req, err := parseExec(args)
	if err != nil {
		return nil, err
	}
	dir, err := e.projectDir(req.path)
	if err != nil {
		return nil, err
	}
	logs := filepath.Join(dir, logFolder)
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", logFolder, err)
	}
	log, n, err := nextLog(logs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = log.Close() }()

	argv := req.argv
	if req.script {
		path, header, err := saveScript(logs, e.stdin)
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.Remove(path) }()
		argv = scriptArgv(path, header, argv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	code, err := e.shell(ctx, dir, `exec "$@"`, argv, log)
	if err != nil {
		return nil, err
	}
	if err := prune(logs, n); err != nil {
		return nil, err
	}
	if _, err := log.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("read exec log: %w", err)
	}
	head, hidden, err := output.Head(log, req.maxLines, maxShown)
	if err != nil {
		return nil, err
	}
	return &output.Exec{Output: head, Hidden: hidden, Log: fmt.Sprintf("%s/e%d.log", logFolder, n), Exit: code}, nil
}

// nextLog creates e<n>.log with n one above the highest present. O_EXCL
// keeps two calls at once from sharing a file.
func nextLog(dir string) (*os.File, int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("list %s: %w", logFolder, err)
	}
	last := 0
	for _, entry := range entries {
		if n, ok := logNumber(entry.Name()); ok {
			last = max(last, n)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("open %s: %w", logFolder, err)
	}
	defer func() { _ = root.Close() }()
	for n := last + 1; ; n++ {
		f, err := root.OpenFile(fmt.Sprintf("e%d.log", n), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, 0, fmt.Errorf("create exec log: %w", err)
		}
		return f, n, nil
	}
}

// prune removes the logs that fell out of the last keepLogs.
func prune(dir string, newest int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", logFolder, err)
	}
	for _, entry := range entries {
		if n, ok := logNumber(entry.Name()); ok && n <= newest-keepLogs {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return fmt.Errorf("remove old exec log: %w", err)
			}
		}
	}
	return nil
}

func logNumber(name string) (int, bool) {
	m := logName.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// saveScript writes the script from r to an executable temp file and returns
// its path and first two bytes.
func saveScript(dir string, r io.Reader) (path, header string, err error) {
	f, err := os.CreateTemp(dir, "script-*")
	if err != nil {
		return "", "", fmt.Errorf("save script: %w", err)
	}
	defer func() { _ = f.Close() }()
	br := bufio.NewReader(r)
	// A short script has fewer than two bytes to peek; that is no shebang.
	peek, _ := br.Peek(2)
	if _, err := io.Copy(f, br); err != nil {
		return "", "", errors.Join(fmt.Errorf("save script: %w", err), os.Remove(f.Name()))
	}
	if err := f.Chmod(0o700); err != nil {
		return "", "", errors.Join(fmt.Errorf("save script: %w", err), os.Remove(f.Name()))
	}
	return f.Name(), string(peek), nil
}

// scriptArgv runs a script through its shebang line, or through bash when it
// has none.
func scriptArgv(path, header string, args []string) []string {
	if header == "#!" {
		return append([]string{path}, args...)
	}
	return append([]string{"bash", path}, args...)
}
