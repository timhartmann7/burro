// Package cli holds the command registry, dispatch and help rendering.
package cli

import (
	"fmt"
	"io"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/timhartmann7/burro/internal/output"
)

// helpWidth is the widest line any help screen may print.
const helpWidth = 80

type group int

const (
	groupSetup group = iota
	groupRun
	groupServer
)

// command is one registry entry. Dispatch and help both read it, so a command
// can't exist without its help line.
type command struct {
	name    string
	args    string
	group   group
	summary string
	run     func(args []string) error
}

// App is burro's command line.
type App struct {
	stdout   io.Writer
	version  string
	commands []command
}

// New returns the app. linkedVersion is the version set with -ldflags -X, or
// empty in a plain build.
func New(stdout io.Writer, linkedVersion string) *App {
	return &App{stdout: stdout, version: resolveVersion(linkedVersion)}
}

// Run executes the command named by args.
func (a *App) Run(args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		return a.write(a.help())
	}
	if args[0] == "--version" {
		return a.write(fmt.Sprintf("burro %s %s/%s\n", a.version, runtime.GOOS, runtime.GOARCH))
	}
	for _, c := range a.commands {
		if c.name == args[0] {
			return c.run(args[1:])
		}
	}
	return output.NewError(output.ExitUsage,
		fmt.Sprintf("unknown command %q", args[0]),
		"run burro --help")
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
	for _, c := range a.commands {
		width = max(width, len(c.usage()))
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
		for _, c := range a.commands {
			if c.group != g.group {
				continue
			}
			b.WriteString(title)
			title = ""
			fmt.Fprintf(&b, "  %-*s   %s\n", width, c.usage(), c.summary)
		}
	}

	b.WriteString("\n")
	b.WriteString(exitCodes([]exitLabel{
		{output.ExitPassed, "passed"},
		{output.ExitFailed, "tests failed"},
		{output.ExitServer, "server problem"},
		{output.ExitRunning, "still running"},
		{output.ExitUsage, "bad config or flags"},
		{output.ExitBusy, "another run is active"},
	}))
	b.WriteString("\nburro <command> --help   details and an example\n")
	return b.String()
}

func (c command) usage() string {
	if c.args == "" {
		return c.name
	}
	return c.name + " " + c.args
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
