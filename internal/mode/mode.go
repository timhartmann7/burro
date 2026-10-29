// Package mode picks agent or human mode and owns color.
package mode

import "fmt"

// ANSI codes for the status words. Nothing else in burro holds color codes.
const (
	green = "\x1b[32m"
	red   = "\x1b[31m"
	grey  = "\x1b[90m"
	reset = "\x1b[0m"
)

const (
	agent = "agent"
	human = "human"
)

// Mode is the output mode of one burro call. Only human mode gets color.
type Mode struct {
	human bool
	color bool
}

// New picks the mode by the first rule that matches: the --mode flag,
// BURRO_MODE, an agent variable, then whether stdout is a terminal. flag is
// the --mode value or empty, getenv reads the environment and terminal tells
// whether stdout is a terminal.
func New(flag string, getenv func(string) string, terminal bool) (*Mode, error) {
	if flag != "" && flag != agent && flag != human {
		return nil, fmt.Errorf("--mode must be agent or human, not %q", flag)
	}
	human := isHuman(flag, getenv, terminal)
	color := human && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
	return &Mode{human: human, color: color}, nil
}

func isHuman(flag string, getenv func(string) string, terminal bool) bool {
	for _, v := range []string{flag, getenv("BURRO_MODE")} {
		if v == agent || v == human {
			return v == human
		}
	}
	for _, name := range []string{"AGENT", "AI_AGENT", "CLAUDECODE", "CODEX_SANDBOX", "CURSOR_AGENT"} {
		if getenv(name) != "" {
			return false
		}
	}
	return terminal
}

// Human tells whether a person reads this call, so stderr may carry a
// progress line.
func (m *Mode) Human() bool {
	return m.human
}

// OK colors a passing status word green.
func (m *Mode) OK(s string) string {
	return m.paint(green, s)
}

// Fail colors a failing status word red.
func (m *Mode) Fail(s string) string {
	return m.paint(red, s)
}

// Skip colors a skipped status word grey.
func (m *Mode) Skip(s string) string {
	return m.paint(grey, s)
}

func (m *Mode) paint(code, s string) string {
	if !m.color {
		return s
	}
	return code + s + reset
}
