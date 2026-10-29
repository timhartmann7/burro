package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/timhartmann7/burro/internal/agent"
	"github.com/timhartmann7/burro/internal/config"
	"github.com/timhartmann7/burro/internal/hosts"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/tree"
)

const (
	// shortTimeout bounds one short ssh call. ssh gives up connecting after
	// 10 seconds on its own.
	shortTimeout = 30 * time.Second
	// uploadTimeout bounds copying the server binary over a slow link.
	uploadTimeout = 5 * time.Minute
	// longTimeout bounds exec and rsync, matching the default step timeout.
	longTimeout = 60 * time.Minute
	// executor is the server binary, relative to the home folder where every
	// ssh command starts.
	executor = ".burro/bin/burro"
	// maxReply caps one reply line, matching what the executor stays under.
	maxReply = 1 << 20
)

// probeScript prints the server's architecture and the version line of its
// server binary, empty when there is none.
const probeScript = executor + ` --version 2>/dev/null || true; uname -m`

// uploadScript writes the binary from stdin under a temp name and renames it,
// so a broken upload never replaces a working binary.
const uploadScript = `umask 077 && mkdir -p .burro/bin && t=.burro/bin/.burro.$$ && ` +
	`trap 'rm -f "$t"' EXIT && cat >"$t" && chmod 700 "$t" && mv -f "$t" ` + executor

// project is what a command needs to work on the current project.
type project struct {
	repo  *tree.Repo
	cfg   *config.Config
	store *hosts.Store
	host  *hosts.Host
}

// project reads burro.toml from the repository root and finds its host.
// Everything here is local, so a bad config fails before any network call.
func (a *App) project(ctx context.Context) (*project, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("working folder: %v", err),
			"run burro inside the project's git repository")
	}
	repo, err := tree.Open(ctx, dir, a.env)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(repo.Root())
	if err != nil {
		return nil, err
	}
	store, err := a.store()
	if err != nil {
		return nil, err
	}
	host, err := store.Lookup(cfg.Host())
	if err != nil {
		return nil, err
	}
	return &project{repo: repo, cfg: cfg, store: store, host: host}, nil
}

// call is the argument list of an executor call on the project folder.
func (p *project) call(action string, args ...string) []string {
	return append([]string{action, "--path", p.cfg.Dir()}, args...)
}

func (a *App) store() (*hosts.Store, error) {
	return hosts.NewStore(a.getenv("HOME"), a.env)
}

// probe returns the server's `uname -m` and the version of its server
// binary, empty when it has none.
func (a *App) probe(ctx context.Context, h *hosts.Host) (machine, version string, err error) {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	out, err := h.Run(ctx, nil, "sh", "-c", probeScript)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	machine = strings.TrimSpace(lines[len(lines)-1])
	if len(lines) > 1 {
		// The version line reads "burro <version> <os>/<arch>".
		if fields := strings.Fields(lines[0]); len(fields) == 3 && fields[0] == binaryName {
			version = fields[1]
		}
	}
	return machine, version, nil
}

// install uploads this client's server binary for machine.
func (a *App) install(ctx context.Context, h *hosts.Host, machine string) error {
	bin, err := a.binaries.ForMachine(machine)
	if err != nil {
		return fmt.Errorf("host %s: %w", h.Name(), err)
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	if _, err := h.Run(ctx, bytes.NewReader(bin), "sh", "-c", uploadScript); err != nil {
		return fmt.Errorf("install server binary: %w", err)
	}
	return nil
}

// connect makes sure the host runs the server binary of this client's
// version, replacing it when the versions differ.
func (a *App) connect(ctx context.Context, h *hosts.Host) error {
	machine, version, err := a.probe(ctx, h)
	if err != nil || version == a.version {
		return err
	}
	return a.install(ctx, h, machine)
}

// call runs one agent call on the host and returns its reply. A failure the
// executor reports becomes this command's error.
func (a *App) call(ctx context.Context, h *hosts.Host, stdin io.Reader, args ...string) (*agent.Reply, error) {
	reply, err := exchange(ctx, h, stdin, nil, args...)
	if err != nil {
		return nil, err
	}
	return reply, replyError(h, reply)
}

// short is a call without stdin that must finish within shortTimeout.
func (a *App) short(ctx context.Context, h *hosts.Host, args ...string) (*agent.Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	return a.call(ctx, h, nil, args...)
}

// exchange runs one agent call and parses its last line as the reply.
// Earlier lines are progress replies, handed to progress as they arrive.
// The error is about reaching the executor; what the executor reports
// stays in the reply.
func exchange(ctx context.Context, h *hosts.Host, stdin io.Reader, progress func(output.Progress), args ...string) (*agent.Reply, error) {
	lines := &replyLines{progress: progress}
	if err := h.Stream(ctx, stdin, lines, append([]string{executor, agentCommand}, args...)...); err != nil {
		return nil, err
	}
	var reply agent.Reply
	if err := json.Unmarshal(lines.reply(), &reply); err != nil || lines.over {
		return nil, output.NewError(output.ExitServer,
			fmt.Sprintf("host %s: the server binary sent a broken reply", h.Name()),
			"run burro doctor "+h.Name())
	}
	return &reply, nil
}

// replyError is the failure the executor put into reply, as this command's
// error with the same exit code and fix line.
func replyError(h *hosts.Host, reply *agent.Reply) error {
	f := reply.Error
	if f == nil {
		return nil
	}
	code := output.Code(f.Code)
	if code < output.ExitFailed || code > output.ExitBusy {
		code = output.ExitServer
	}
	return output.NewError(code, fmt.Sprintf("host %s: %s", h.Name(), f.Message), f.Fix)
}

// replyLines splits executor output into lines as it arrives. Each line is
// capped like a whole reply.
type replyLines struct {
	progress func(output.Progress)
	partial  []byte
	last     []byte
	over     bool
}

func (r *replyLines) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			r.add(p)
			return n, nil
		}
		r.add(p[:i])
		r.line()
		p = p[i+1:]
	}
}

func (r *replyLines) add(p []byte) {
	if len(r.partial)+len(p) > maxReply {
		r.over = true
		return
	}
	r.partial = append(r.partial, p...)
}

func (r *replyLines) line() {
	line := r.partial
	r.partial = nil
	var reply agent.Reply
	if r.progress != nil && json.Unmarshal(line, &reply) == nil && reply.Progress != nil {
		r.progress(*reply.Progress)
		return
	}
	r.last = line
}

// reply is the last line, which may lack its newline.
func (r *replyLines) reply() []byte {
	if len(r.partial) > 0 {
		r.line()
	}
	return r.last
}
