package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/timhartmann7/burro/internal/config"
	"github.com/timhartmann7/burro/internal/hosts"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/tree"
)

// minFreeDisk is the free space below which doctor fails the disk check.
const minFreeDisk = 1e9

// Names of the doctor checks that build their line in more than one place.
const (
	checkDocker = "docker"
	checkDisk   = "disk"
)

// treeScript writes the full tree hash to <dir>/.burro/tree through a temp
// file of its own, so a reader never sees half a hash and two syncs at once
// don't share one.
const treeScript = `t="$1/.burro/tree.$$" && printf '%s\n' "$2" >"$t" && mv -f "$t" "$1/.burro/tree"`

// doctorScript prints one "name value" line per fact doctor checks. A
// missing tool prints an empty value; docker prints nothing when absent.
const doctorScript = `printf 'rsync %s\n' "$(rsync --version 2>/dev/null | head -n 1)"
printf 'bash %s\n' "$(bash --version 2>/dev/null | head -n 1)"
if command -v docker >/dev/null 2>&1; then
  v=$(docker version --format '{{.Server.Version}}' 2>/dev/null) || v=unreachable
  printf 'docker %s\n' "$v"
fi
printf 'disk %s\n' "$(df -Pk . 2>/dev/null | tail -n 1)"
printf 'home %s\n' "$HOME"`

var toolVersion = regexp.MustCompile(`version\s+v?(\d[0-9A-Za-z.+-]*)`)

func (a *App) hostAdd(ctx context.Context, p *parsed) error {
	name, target := p.args[0], p.args[1]
	if err := config.ValidName("host name", name); err != nil {
		return err
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	h, undo, err := store.Add(ctx, name, target, p.flags["i"], p.flags["force"] == flagSet)
	if err != nil {
		return err
	}
	checks, err := a.checks(ctx, h)
	if err != nil {
		if uerr := undo(); uerr != nil {
			return errors.Join(err, uerr)
		}
		return err
	}
	return printChecks(p.print, checks)
}

func (a *App) hostList(ctx context.Context, p *parsed) error {
	store, err := a.store()
	if err != nil {
		return err
	}
	list, err := store.List()
	if err != nil {
		return err
	}
	lines := make([]output.Host, 0, len(list))
	for _, h := range list {
		version := "unreachable"
		if _, v, err := a.probe(ctx, h); err == nil {
			version = v
			if v == "" {
				version = "not installed"
			}
		}
		lines = append(lines, output.Host{Name: h.Name(), Address: h.Address(ctx), Version: version})
	}
	return p.print.Hosts(lines)
}

func (a *App) doctor(ctx context.Context, p *parsed) error {
	h, err := a.doctorHost(ctx, p.args)
	if err != nil {
		return err
	}
	checks, err := a.checks(ctx, h)
	if err != nil {
		return err
	}
	return printChecks(p.print, checks)
}

// doctorHost is the host named on the command line, or the project's host.
func (a *App) doctorHost(ctx context.Context, args []string) (*hosts.Host, error) {
	if len(args) == 0 {
		proj, err := a.project(ctx)
		if err != nil {
			return nil, err
		}
		return proj.host, nil
	}
	if err := config.ValidName("host", args[0]); err != nil {
		return nil, err
	}
	store, err := a.store()
	if err != nil {
		return nil, err
	}
	return store.Lookup(args[0])
}

func printChecks(printer *output.Printer, checks []output.Check) error {
	if err := printer.Doctor(checks); err != nil {
		return err
	}
	for _, c := range checks {
		if !c.OK {
			return output.Silent(output.ExitServer)
		}
	}
	return nil
}

// checks runs doctor on h. An ssh failure is an error; everything after a
// working connection becomes a check line.
func (a *App) checks(ctx context.Context, h *hosts.Host) ([]output.Check, error) {
	machine, version, err := a.probe(ctx, h)
	if err != nil {
		return nil, err
	}
	burro := output.Check{Name: binaryName, OK: true, Detail: a.version}
	if version != a.version {
		if err := a.install(ctx, h, machine); err != nil {
			burro = failedCheck(binaryName, err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	start := time.Now()
	if _, err := h.Run(ctx, nil, "true"); err != nil {
		return nil, err
	}
	rtt := time.Since(start)
	out, err := h.Run(ctx, nil, "sh", "-c", doctorScript)
	if err != nil {
		return nil, err
	}
	facts := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			facts[key] = strings.TrimSpace(value)
		}
	}
	_, hasDocker := facts["docker"]
	return []output.Check{
		{Name: "ssh", OK: true, Detail: fmt.Sprintf("%dms round trip", rtt.Milliseconds())},
		toolCheck("rsync", facts["rsync"], "install rsync on the server, for example apt install rsync"),
		toolCheck("bash", facts["bash"], "install bash on the server"),
		dockerCheck(facts["docker"], hasDocker),
		diskCheck(facts["disk"], facts["home"]),
		burro,
	}, nil
}

func failedCheck(name string, err error) output.Check {
	c := output.Check{Name: name, Detail: err.Error()}
	var coded *output.Error
	if errors.As(err, &coded) {
		c.Hint = coded.Fix()
	}
	return c
}

func toolCheck(name, versionLine, hint string) output.Check {
	if versionLine == "" {
		return output.Check{Name: name, Detail: "not found", Hint: hint}
	}
	detail := versionLine
	if m := toolVersion.FindStringSubmatch(versionLine); m != nil {
		detail = m[1]
	}
	return output.Check{Name: name, OK: true, Detail: detail}
}

func dockerCheck(version string, found bool) output.Check {
	const note = "needed only if steps use docker"
	switch {
	case !found:
		return output.Check{Name: checkDocker, OK: true, Detail: "not found, " + note}
	case version == "unreachable":
		return output.Check{Name: checkDocker, OK: true, Detail: "daemon not reachable, " + note}
	default:
		return output.Check{Name: checkDocker, OK: true, Detail: version}
	}
}

// diskCheck reads a `df -Pk` line: the fourth field is free 1024-byte blocks.
func diskCheck(dfLine, home string) output.Check {
	unknown := output.Check{Name: checkDisk, Detail: "free space unknown", Hint: "check that df works on the server"}
	fields := strings.Fields(dfLine)
	if len(fields) < 4 {
		return unknown
	}
	kb, err := strconv.ParseFloat(fields[3], 64)
	if err != nil {
		return unknown
	}
	free := kb * 1024
	c := output.Check{Name: checkDisk, OK: free >= minFreeDisk, Detail: fmt.Sprintf("%.1f GB free in %s", free/1e9, home)}
	if !c.OK {
		c.Hint = "free at least 1 GB on the server"
	}
	return c
}

func (a *App) exec(ctx context.Context, p *parsed) error {
	maxLines, err := strconv.Atoi(p.flags["max-lines"])
	if err != nil || maxLines < 1 {
		return output.NewError(output.ExitUsage, fmt.Sprintf("exec: --max-lines %q is not a positive number", p.flags["max-lines"]),
			"pass a number of lines, for example --max-lines 100")
	}
	file := p.flags["file"]
	if file == "" && len(p.args) == 0 {
		return output.NewError(output.ExitUsage, "exec: no command given",
			"run burro exec -- <command>, or burro exec --file <script>")
	}
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	args := proj.call("exec", "--max-lines", strconv.Itoa(maxLines))
	var stdin io.Reader
	if file != "" {
		dir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("working folder: %w", err)
		}
		script, err := config.ReadInRoot(proj.repo.Root(), dir, file)
		if err != nil {
			return err
		}
		stdin = bytes.NewReader(script)
		args = append(args, "--file")
	}
	args = append(append(args, "--"), p.args...)

	if err := a.connect(ctx, proj.host); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, longTimeout)
	defer cancel()
	reply, err := a.call(ctx, proj.host, stdin, args...)
	if err != nil {
		return err
	}
	if reply.Exec == nil {
		return noResult(proj.host, "exec")
	}
	if err := p.print.Exec(*reply.Exec); err != nil {
		return err
	}
	if reply.Exec.Exit != 0 {
		return output.Silent(output.ExitFailed)
	}
	return nil
}

func (a *App) sync(ctx context.Context, p *parsed) error {
	proj, err := a.project(ctx)
	if err != nil {
		return err
	}
	s, err := a.push(ctx, proj)
	if err != nil {
		return err
	}
	s.Tree = s.Tree[:tree.ShortLen]
	return p.print.Sync(*s)
}

// push syncs the tree to the server unless a run holds the lock there, and
// records the full tree hash.
func (a *App) push(ctx context.Context, proj *project) (*output.Sync, error) {
	if err := proj.store.CheckRsync(ctx); err != nil {
		return nil, err
	}
	start := time.Now()
	hash, err := proj.repo.Hash(ctx)
	if err != nil {
		return nil, err
	}
	exclude, err := proj.repo.ExcludeFile(ctx)
	if err != nil {
		return nil, err
	}
	h, dir := proj.host, proj.cfg.Dir()
	if err := a.connect(ctx, h); err != nil {
		return nil, err
	}
	if _, err := a.short(ctx, h, proj.call("prepare")...); err != nil {
		return nil, err
	}
	longCtx, cancel := context.WithTimeout(ctx, longTimeout)
	defer cancel()
	changed, err := h.Sync(longCtx, proj.repo.Root(), dir, exclude)
	if err != nil {
		return nil, err
	}
	if err := runShort(ctx, h, "sh", "-c", treeScript, "sh", dir, hash); err != nil {
		return nil, err
	}
	return &output.Sync{Tree: hash, Changed: changed, Took: time.Since(start)}, nil
}

// runShort runs one short command on h whose output doesn't matter.
func runShort(ctx context.Context, h *hosts.Host, argv ...string) error {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	_, err := h.Run(ctx, nil, argv...)
	return err
}
