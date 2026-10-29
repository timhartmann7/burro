// Package cli holds the command registry, dispatch and help rendering.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/timhartmann7/burro/internal/agent"
	"github.com/timhartmann7/burro/internal/mode"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/serverbin"
)

// helpWidth is the widest line any help screen may print.
const helpWidth = 80

// serverProblem labels exit code 2 in help screens.
const serverProblem = "server problem"

type group int

const (
	groupSetup group = iota
	groupRun
	groupServer
)

// serverBinaries hands out the executor for a server's architecture.
// Tests swap in binaries they build themselves.
type serverBinaries interface {
	ForMachine(machine string) ([]byte, error)
}

// App is burro's command line.
type App struct {
	stdin    io.Reader
	stdout   io.Writer
	env      []string
	version  string
	terminal bool
	binaries serverBinaries
	commands []command
}

// New returns the app. env is the process environment; linkedVersion is the
// version set with -ldflags -X, or empty in a plain build.
func New(stdin io.Reader, stdout io.Writer, env []string, linkedVersion string) *App {
	a := &App{
		stdin:    stdin,
		stdout:   stdout,
		env:      env,
		version:  resolveVersion(linkedVersion),
		terminal: isTerminal(stdout),
		binaries: serverbin.New(),
	}
	a.commands = a.registry()
	return a
}

// isTerminal tells whether w is a character device such as a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// getenv reads the environment the app was built with; the last entry wins,
// as in the process environment.
func (a *App) getenv(name string) string {
	for i := len(a.env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(a.env[i], "="); ok && k == name {
			return v
		}
	}
	return ""
}

// Run executes the command named by args.
func (a *App) Run(args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		return a.write(a.help())
	}
	switch args[0] {
	case "--version":
		return a.write(fmt.Sprintf("burro %s %s/%s\n", a.version, runtime.GOOS, runtime.GOARCH))
	case "agent":
		return agent.New(a.stdin, a.stdout, a.getenv("HOME")).Run(args[1:])
	}
	c, rest := a.find(args)
	if c == nil {
		return output.NewError(output.ExitUsage,
			fmt.Sprintf("unknown command %q", args[0]),
			"run burro --help")
	}
	p, err := c.parse(rest)
	if err != nil {
		return err
	}
	if p.help {
		return a.write(c.helpText())
	}
	m, err := mode.New(p.flags["mode"], a.getenv, a.terminal)
	if err != nil {
		return output.NewError(output.ExitUsage, err.Error(), "pass --mode agent or --mode human")
	}
	p.print = output.NewPrinter(a.stdout, p.flags["json"] == flagSet, m)
	return c.run(context.Background(), p)
}

// find returns the command whose words start args, and the arguments after
// them.
func (a *App) find(args []string) (c *command, rest []string) {
	for i := range a.commands {
		words := strings.Fields(a.commands[i].name)
		if len(args) >= len(words) && strings.Join(args[:len(words)], " ") == a.commands[i].name {
			return &a.commands[i], args[len(words):]
		}
	}
	return nil, nil
}

func (a *App) write(s string) error {
	if _, err := io.WriteString(a.stdout, s); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func (a *App) help() string {
	var b strings.Builder
	b.WriteString("burro · run your coding agent's tests on your own server\n\n")
	b.WriteString("Usage: burro <command> [flags]\n")

	width := 0
	for i := range a.commands {
		width = max(width, len(a.commands[i].briefText()))
	}
	groups := []struct {
		group group
		title string
	}{
		{groupSetup, "Set up (once)"},
		{groupRun, "Run tests"},
		{groupServer, "Server"},
	}
	for _, g := range groups {
		title := "\n" + g.title + "\n"
		for i := range a.commands {
			c := &a.commands[i]
			if c.group != g.group {
				continue
			}
			b.WriteString(title)
			title = ""
			fmt.Fprintf(&b, "  %-*s   %s\n", width, c.briefText(), c.summary)
		}
	}

	b.WriteString("\n")
	b.WriteString(exitCodes([]exitLabel{
		{output.ExitPassed, "passed"},
		{output.ExitFailed, "tests failed"},
		{output.ExitServer, serverProblem},
		{output.ExitRunning, "still running"},
		{output.ExitUsage, "bad config or flags"},
		{output.ExitBusy, "another run is active"},
	}))
	b.WriteString("\nburro <command> --help   details and an example\n")
	return b.String()
}

type exitLabel struct {
	code  output.Code
	label string
}

// exitCodes renders the "Exit codes" block, starting an indented line before
// any line would pass helpWidth.
func exitCodes(labels []exitLabel) string {
	const prefix = "Exit codes  "
	const sep = " · "
	var b strings.Builder
	line := prefix
	for i, l := range labels {
		item := fmt.Sprintf("%d %s", l.code, l.label)
		switch {
		case i == 0:
			line += item
		case columns(line+sep+item) > helpWidth:
			b.WriteString(line + "\n")
			line = strings.Repeat(" ", len(prefix)) + item
		default:
			line += sep + item
		}
	}
	b.WriteString(line + "\n")
	return b.String()
}

func columns(s string) int {
	return len([]rune(s))
}

// releaseTag matches the versions `go install …@vX.Y.Z` stamps. Pseudo-versions
// and +dirty builds come from a checkout and print as dev.
var releaseTag = regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`)

func resolveVersion(linked string) string {
	if linked != "" {
		return linked
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	return buildVersion(info.Main.Version)
}

func buildVersion(v string) string {
	m := releaseTag.FindStringSubmatch(v)
	if m == nil {
		return "dev"
	}
	return m[1]
}
