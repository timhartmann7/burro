package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/timhartmann7/burro/internal/output"
)

// flagSet is the value a boolean flag gets when it is present.
const flagSet = "true"

// flagSpec describes one flag. A flag without arg is boolean.
type flagSpec struct {
	name  string
	short bool
	arg   string
	def   string
	usage string
}

func (f flagSpec) label() string {
	dashes := "--"
	if f.short {
		dashes = "-"
	}
	if f.arg == "" {
		return dashes + f.name
	}
	return dashes + f.name + " " + f.arg
}

// globalFlags are accepted by every command.
func globalFlags() []flagSpec {
	return []flagSpec{
		{name: "json"},
		{name: "mode", arg: "agent|human"},
		{name: "help"},
	}
}

// command is one registry entry. Dispatch, parsing and both help screens
// read it, so a command can't exist without its help.
type command struct {
	name string
	// brief is the usage in burro --help; empty means the name alone.
	brief   string
	summary string
	group   group
	about   string
	usage   []string // empty means the name alone
	flags   []flagSpec
	minArgs int
	maxArgs int
	// raw hands everything from the first argument on to the command
	// untouched, flags included.
	raw     bool
	example string
	exits   []exitLabel
	run     func(ctx context.Context, p *parsed) error
}

// parsed is one command line after parsing.
type parsed struct {
	args  []string
	flags map[string]string
	help  bool
	print *output.Printer
	// human is set in human mode, where stderr may carry progress.
	human bool
}

func (c *command) flag(name string) *flagSpec {
	for _, f := range slices.Concat(c.flags, globalFlags()) {
		if f.name == name {
			return &f
		}
	}
	return nil
}

// parse reads flags anywhere among the arguments, so `host add a b -i key`
// works. -- ends the flags, and so does the first argument of a raw command.
func (c *command) parse(args []string) (*parsed, error) {
	p := &parsed{flags: map[string]string{}}
	if err := c.scan(p, args); err != nil {
		return nil, err
	}
	if p.flags["help"] == flagSet {
		p.help = true
		return p, nil
	}
	for _, f := range c.flags {
		if _, ok := p.flags[f.name]; !ok && f.def != "" {
			p.flags[f.name] = f.def
		}
	}
	if len(p.args) < c.minArgs || (c.maxArgs >= 0 && len(p.args) > c.maxArgs) {
		return nil, c.usageError("wrong number of arguments, usage: burro " + c.usageLines()[0])
	}
	return p, nil
}

// scan sorts args into flags and positional arguments.
func (c *command) scan(p *parsed, args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case c.raw && len(p.args) > 0:
			p.args = append(p.args, args[i:]...)
			return nil
		case a == "--":
			p.args = append(p.args, args[i+1:]...)
			return nil
		case !strings.HasPrefix(a, "-") || a == "-":
			p.args = append(p.args, a)
		default:
			next, err := c.readFlag(p, args, i)
			if err != nil {
				return err
			}
			i = next
		}
	}
	return nil
}

// readFlag stores the flag at args[i] in p and returns the index of the last
// argument it used: i, or i+1 when the value is the next argument.
func (c *command) readFlag(p *parsed, args []string, i int) (int, error) {
	name, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
	f := c.flag(name)
	switch {
	case f == nil:
		return 0, c.usageError("unknown flag " + args[i])
	case f.arg == "" && hasValue:
		return 0, c.usageError(fmt.Sprintf("flag %s takes no value", f.label()))
	case f.arg == "":
		value = flagSet
	case !hasValue && i+1 == len(args):
		return 0, c.usageError(fmt.Sprintf("flag %s needs a value", f.label()))
	case !hasValue:
		i++
		value = args[i]
	}
	p.flags[name] = value
	return i, nil
}

func (c *command) usageError(msg string) error {
	return output.NewError(output.ExitUsage, c.name+": "+msg, "run burro "+c.name+" --help")
}

func (c *command) briefText() string {
	if c.brief == "" {
		return c.name
	}
	return c.brief
}

// usageLines defaults to the bare name for a command without arguments.
func (c *command) usageLines() []string {
	if c.usage == nil {
		return []string{c.name}
	}
	return c.usage
}

// helpText is the command's --help screen: what it does, usage, flags with
// defaults, one example and its exit codes.
func (c *command) helpText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "burro %s · %s\n\n", c.name, c.about)
	for i, u := range c.usageLines() {
		prefix := "Usage: "
		if i > 0 {
			prefix = "       "
		}
		b.WriteString(prefix + "burro " + u + "\n")
	}
	if len(c.flags) > 0 {
		b.WriteString("\n")
		width := 0
		for _, f := range c.flags {
			width = max(width, len(f.label()))
		}
		for _, f := range c.flags {
			usage := f.usage
			if f.def != "" {
				usage += fmt.Sprintf(" (default %s)", f.def)
			}
			fmt.Fprintf(&b, "  %-*s    %s\n", width, f.label(), usage)
		}
	}
	fmt.Fprintf(&b, "\nExample\n  %s\n\n", c.example)
	b.WriteString(exitCodes(c.exits))
	return b.String()
}

// runExits are the exit codes of commands that report a run's result.
// running labels code 3.
func runExits(running string) []exitLabel {
	return []exitLabel{
		{output.ExitPassed, "passed"},
		{output.ExitFailed, "tests failed"},
		{output.ExitServer, "server problem or runner died"},
		{output.ExitRunning, running},
		{output.ExitUsage, "bad flags or unknown run"},
	}
}

// registry lists every public command in help order.
func (a *App) registry() []command {
	return []command{
		{
			name:    "host add",
			brief:   "host add <name> <user@host>",
			summary: "add a server",
			group:   groupSetup,
			about:   "add a server burro can run on",
			usage:   []string{"host add <name> <user@host[:port]> [-i key] [--force]"},
			flags: []flagSpec{
				{name: "i", short: true, arg: "key", usage: "ssh key for this server, skip it if plain ssh already works"},
				{name: "force", usage: "replace a server with the same name"},
			},
			minArgs: 2,
			maxArgs: 2,
			example: "burro host add myserver root@203.0.113.10 -i ~/.ssh/id_ed25519",
			exits: []exitLabel{
				{output.ExitPassed, "added"},
				{output.ExitServer, "server unreachable or host key unknown"},
				{output.ExitUsage, "name taken"},
			},
			run: a.hostAdd,
		},
		{
			name:    "run",
			brief:   "run [pipeline]",
			summary: "sync the code and start a run, returns at once",
			group:   groupRun,
			about:   "sync the code and start a pipeline on the server, returns at once",
			usage:   []string{"run [pipeline] [--from step] [--only a,b]"},
			flags: []flagSpec{
				{name: "from", arg: "step", usage: "start at this step, the ones before it don't run"},
				{name: "only", arg: "a,b", usage: "run only these steps, in pipeline order"},
			},
			maxArgs: 1,
			example: "burro run verify --from e2e",
			exits: []exitLabel{
				{output.ExitPassed, "started"},
				{output.ExitServer, serverProblem},
				{output.ExitUsage, "bad burro.toml or step"},
				{output.ExitBusy, "another run is active"},
			},
			run: a.run,
		},
		{
			name:    "wait",
			summary: "wait for the run, then print a short summary",
			group:   groupRun,
			about:   "wait for a run to end, then print its summary",
			usage:   []string{"wait [id] [--timeout 60m]"},
			flags: []flagSpec{
				{name: "timeout", arg: "d", def: "60m", usage: "stop waiting after this long, the run goes on"},
			},
			maxArgs: 1,
			example: "burro wait r42 --timeout 30m",
			exits:   runExits("still running, wait again"),
			run:     a.wait,
		},
		{
			name:    "status",
			summary: "how the run is going",
			group:   groupRun,
			about:   "show the state of a run and of each step",
			usage:   []string{"status [id]"},
			maxArgs: 1,
			example: "burro status r42",
			exits:   runExits("still running"),
			run:     a.status,
		},
		{
			name:    cmdLog,
			brief:   "log --step <name>",
			summary: "clean log of one step, --grep to search it",
			group:   groupRun,
			about:   "print the clean log of one step: no ANSI codes, no binary bytes",
			usage:   []string{"log [id] --step s [--grep re] [--tail 60] [--context 5]"},
			flags: []flagSpec{
				{name: "step", arg: "s", usage: "step whose log to print"},
				{name: "grep", arg: "re", usage: "print only matching lines with context, Go regexp"},
				{name: "tail", arg: "n", def: "60", usage: "most lines to print"},
				{name: "context", arg: "n", def: defaultContext, usage: "lines around each match"},
			},
			maxArgs: 1,
			example: "burro log r42 --step e2e --grep login",
			exits: []exitLabel{
				{output.ExitPassed, "printed"},
				{output.ExitServer, serverProblem},
				{output.ExitUsage, "bad flags or unknown step"},
			},
			run: a.log,
		},
		{
			name:    "cancel",
			summary: "stop the run",
			group:   groupRun,
			about:   "stop a run and every process of its current step",
			usage:   []string{"cancel [id]"},
			maxArgs: 1,
			example: "burro cancel r42",
			exits: []exitLabel{
				{output.ExitPassed, "stopped"},
				{output.ExitServer, serverProblem},
				{output.ExitUsage, "unknown run"},
			},
			run: a.cancel,
		},
		{
			name:    "exec",
			brief:   "exec -- <command>",
			summary: "run one command on the server",
			group:   groupServer,
			about:   "run a command or local script in the project folder on the server",
			usage:   []string{"exec [--max-lines 60] [--] cmd args…", "exec --file path [args…]"},
			flags: []flagSpec{
				{name: "max-lines", arg: "n", def: "60", usage: "lines to show, the rest stays on the server"},
				{name: "file", arg: "path", usage: "local script to run, with its shebang or bash"},
			},
			maxArgs: -1,
			raw:     true,
			example: "burro exec -- ls -la",
			exits: []exitLabel{
				{output.ExitPassed, "command passed"},
				{output.ExitFailed, "command failed"},
				{output.ExitServer, serverProblem},
				{output.ExitUsage, "bad flags or burro.toml"},
			},
			run: a.exec,
		},
		{
			name:    "sync",
			summary: "copy the code to the server without a run",
			group:   groupServer,
			about:   "copy the code to the server and record its tree hash",
			example: "burro sync",
			exits: []exitLabel{
				{output.ExitPassed, "synced"},
				{output.ExitServer, serverProblem},
				{output.ExitUsage, "bad burro.toml"},
			},
			run: a.sync,
		},
		{
			name:    "doctor",
			summary: "check that the server is ready",
			group:   groupServer,
			about:   "check ssh, rsync, bash, docker, free disk and the server binary",
			usage:   []string{"doctor [host]"},
			maxArgs: 1,
			example: "burro doctor myserver",
			exits: []exitLabel{
				{output.ExitPassed, "all ok"},
				{output.ExitServer, "a check failed"},
				{output.ExitUsage, "unknown host or bad burro.toml"},
			},
			run: a.doctor,
		},
		{
			name:    "host ls",
			summary: "list your servers",
			group:   groupServer,
			about:   "list your servers with their server binary version",
			example: "burro host ls",
			exits: []exitLabel{
				{output.ExitPassed, "listed"},
				{output.ExitServer, "hosts file unreadable"},
			},
			run: a.hostList,
		},
	}
}
