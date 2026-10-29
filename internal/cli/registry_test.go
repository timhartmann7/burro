package cli

import (
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

// Arguments that several tests of this package pass.
const (
	argAdd       = "add"
	argSync      = "sync"
	argExec      = "exec"
	argJSON      = "--json"
	argMode      = "--mode"
	argMaxLines  = "--max-lines"
	agentMode    = "agent"
	humanMode    = "human"
	ignoreLogs   = "*.log\n"
	argHost      = "host"
	argDoctor    = "doctor"
	flagMaxLines = "max-lines"
	gitignore    = ".gitignore"
	someTarget   = "u@h"
	argRun       = "run"
	argWait      = "wait"
	argStatus    = "status"
	argE2E       = "e2e"
	argCancel    = "cancel"
	argStep      = "--step"
	argOnly      = "--only"
	argTimeout   = "--timeout"
	argNope      = "nope"
	argBad       = "Bad"
	argVerify    = "verify"
	flagJSON     = "json"
	tomlFile     = "burro.toml"
	argClean     = "clean"
	argFails     = "failures"
)

// acceptance: S1-19 S2-18 S3-16
func TestCommandHelp(t *testing.T) {
	app := New(nil, &strings.Builder{}, io.Discard, nil, "")
	if len(app.commands) != 12 {
		t.Fatalf("%d commands, want the five of S1, the five of S2, failures and clean", len(app.commands))
	}
	for i := range app.commands {
		c := &app.commands[i]
		t.Run(c.name, func(t *testing.T) {
			var stdout strings.Builder
			a := New(nil, &stdout, io.Discard, nil, "")
			if err := a.Run(append(strings.Fields(c.name), "--help")); err != nil {
				t.Fatal(err)
			}
			testutil.Golden(t, "help-"+strings.ReplaceAll(c.name, " ", "-")+".txt", stdout.String())
			checkHelpShape(t, stdout.String())
			checkExample(t, a, c)
		})
	}
}

func checkHelpShape(t *testing.T, help string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(help, "\n"), "\n")
	if len(lines) > 15 {
		t.Errorf("%d lines, want at most 15", len(lines))
	}
	for _, l := range lines {
		if utf8.RuneCountInString(l) > helpWidth {
			t.Errorf("line over %d columns: %q", helpWidth, l)
		}
	}
}

// checkExample makes sure the example names c and that its arguments pass
// the same parser a real call goes through.
func checkExample(t *testing.T, a *App, c *command) {
	t.Helper()
	example := strings.Fields(c.example)
	if example[0] != "burro" {
		t.Fatalf("example %q doesn't start with burro", c.example)
	}
	found, rest := a.find(example[1:])
	if found == nil || found.name != c.name {
		t.Fatalf("example %q runs another command", c.example)
	}
	p, err := found.parse(rest)
	if err != nil {
		t.Fatalf("example %q: %v", c.example, err)
	}
	if p.help {
		t.Fatalf("example %q only shows help", c.example)
	}
}

func TestParse(t *testing.T) {
	app := New(nil, &strings.Builder{}, io.Discard, nil, "")
	tests := []struct {
		args  []string
		cmd   string
		pos   []string
		flags map[string]string
	}{
		{args: []string{argHost, argAdd, "a", someTarget, "-i", "k", "--force"}, cmd: "host add", pos: []string{"a", someTarget}, flags: map[string]string{"i": "k", "force": flagSet}},
		{args: []string{argHost, argAdd, "-i=k", "a", argJSON, someTarget}, cmd: "host add", pos: []string{"a", someTarget}, flags: map[string]string{"i": "k", flagJSON: flagSet}},
		{args: []string{argExec, "ls", "-la", argJSON, "--help"}, cmd: argExec, pos: []string{"ls", "-la", argJSON, "--help"}, flags: map[string]string{flagMaxLines: "60"}},
		{args: []string{argExec, argMaxLines, "5", "--", argJSON, "x"}, cmd: argExec, pos: []string{argJSON, "x"}, flags: map[string]string{flagMaxLines: "5"}},
		{args: []string{argExec, argJSON, "--", "ls", "--", "-"}, cmd: argExec, pos: []string{"ls", "--", "-"}, flags: map[string]string{flagJSON: flagSet, flagMaxLines: "60"}},
		{args: []string{argExec, "--file", "s.sh", "a", "--b"}, cmd: argExec, pos: []string{"a", "--b"}, flags: map[string]string{"file": "s.sh", flagMaxLines: "60"}},
		{args: []string{argDoctor, argMode, humanMode, "vps1"}, cmd: argDoctor, pos: []string{"vps1"}, flags: map[string]string{"mode": humanMode}},
		{args: []string{argDoctor, "-"}, cmd: argDoctor, pos: []string{"-"}, flags: map[string]string{}},
		{args: []string{argSync, "--mode=agent"}, cmd: argSync, flags: map[string]string{"mode": agentMode}},
		{args: []string{argRun}, cmd: argRun, flags: map[string]string{}},
		{args: []string{argRun, argVerify, argOnly, "a,b", "--from=e2e"}, cmd: argRun, pos: []string{argVerify}, flags: map[string]string{"only": "a,b", "from": argE2E}},
		{args: []string{argWait, "r3"}, cmd: argWait, pos: []string{"r3"}, flags: map[string]string{"timeout": "60m"}},
		{args: []string{cmdLog, argStep, argE2E, "--grep", "a b", "r2"}, cmd: cmdLog, pos: []string{"r2"}, flags: map[string]string{"step": argE2E, "grep": "a b", "tail": "60", "context": "5"}},
		{args: []string{argStatus, argJSON}, cmd: argStatus, flags: map[string]string{flagJSON: flagSet}},
		{args: []string{argCancel, "r1"}, cmd: argCancel, pos: []string{"r1"}, flags: map[string]string{}},
	}
	for _, tt := range tests {
		c, rest := app.find(tt.args)
		if c == nil || c.name != tt.cmd {
			t.Fatalf("%q: found %v", tt.args, c)
		}
		p, err := c.parse(rest)
		if err != nil {
			t.Fatalf("%q: %v", tt.args, err)
		}
		if strings.Join(p.args, "|") != strings.Join(tt.pos, "|") || len(p.flags) != len(tt.flags) {
			t.Fatalf("%q: args %q flags %v", tt.args, p.args, p.flags)
		}
		for k, v := range tt.flags {
			if p.flags[k] != v {
				t.Fatalf("%q: flag %s = %q, want %q", tt.args, k, p.flags[k], v)
			}
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{argHost, argAdd, "a"},
		{argHost, argAdd, "a", "b", "c"},
		{argHost, argAdd, "a", "b", "-i"},
		{argHost, argAdd, "a", "b", "--force=yes"},
		{argHost, argAdd, "a", "b", "--nope"},
		{argHost, "ls", "extra"},
		{argSync, "now"},
		{argDoctor, "a", "b"},
		{argExec, argMaxLines},
		{argExec, argMode, "robot", "ls"},
		{argExec, argMaxLines, "0", "ls"},
		{argExec, argMaxLines, "x", "ls"},
		{argExec},
		{argExec, argJSON},
		{argHost, argAdd, argBad, someTarget},
		{argDoctor, argBad},
		{argHost},
		{argRun, "a", "b"},
		{argRun, "--from"},
		{argWait, "r1", "r2"},
		{argWait, argNope},
		{argWait, argTimeout, "soon"},
		{argWait, argTimeout, "0s"},
		{argWait, argTimeout, "-5m"},
		{argStatus, "R1"},
		{argStatus, "r1", "r2"},
		{cmdLog},
		{cmdLog, "r1"},
		{cmdLog, argStep, argBad},
		{cmdLog, argStep, argE2E, "--tail", "0"},
		{cmdLog, argStep, argE2E, "--tail", "x"},
		{cmdLog, argStep, argE2E, "--context", "-1"},
		{cmdLog, argStep, argE2E, "--grep", "("},
		{cmdLog, argStep, argE2E, "r0"},
		{argCancel, "../r1"},
	} {
		var stdout, stderr strings.Builder
		err := New(nil, &stdout, io.Discard, nil, "").Run(args)
		if code := output.Report(&stderr, err); code != int(output.ExitUsage) {
			t.Errorf("%q: exit %d, want 4: %s", args, code, stderr.String())
			continue
		}
		lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[1], "fix: ") || stdout.Len() != 0 {
			t.Errorf("%q: stderr %q stdout %q", args, stderr.String(), stdout.String())
		}
	}
}

func TestAgentDispatch(t *testing.T) {
	var stdout strings.Builder
	if err := New(strings.NewReader(""), &stdout, io.Discard, []string{"HOME=/nonexistent"}, "").Run([]string{agentMode, argNope}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"error"`) {
		t.Fatalf("agent reply %q", stdout.String())
	}
	if strings.Contains(New(nil, &stdout, io.Discard, nil, "").help(), "\n  agent") {
		t.Fatal("help lists the internal agent command")
	}
}

func TestNoHome(t *testing.T) {
	for _, args := range [][]string{{argHost, "ls"}, {argHost, argAdd, "a", someTarget}, {argDoctor, "a"}} {
		var stderr strings.Builder
		err := New(nil, &strings.Builder{}, io.Discard, []string{"PATH=/usr/bin"}, "").Run(args)
		var coded *output.Error
		if !errors.As(err, &coded) || coded.Code() != output.ExitServer {
			t.Errorf("%q: got %v", args, err)
		}
		output.Report(&stderr, err)
	}
}
