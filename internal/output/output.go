// Package output owns the exit codes and the error type that carries them.
package output

import (
	"errors"
	"fmt"
	"io"
)

// Code is a process exit code. The agent reads it to decide what to fix.
type Code int

// Exit codes from docs/reference/cli.md.
const (
	ExitPassed  Code = 0
	ExitFailed  Code = 1
	ExitServer  Code = 2
	ExitRunning Code = 3
	ExitUsage   Code = 4
	ExitBusy    Code = 5
)

// Error is a failure with its exit code and a concrete next step for the user.
type Error struct {
	code Code
	msg  string
	fix  string
}

// NewError returns an error that exits with code and prints fix as its fix line.
func NewError(code Code, msg, fix string) *Error {
	return &Error{code: code, msg: msg, fix: fix}
}

func (e *Error) Error() string {
	return e.msg
}

// Report writes err to w as burro's error lines and returns the exit code for
// it. Write errors are dropped: when stderr fails there is nowhere left to
// report them.
func Report(w io.Writer, err error) int {
	if err == nil {
		return int(ExitPassed)
	}
	var e *Error
	if !errors.As(err, &e) {
		// Without a code the cause is the machine burro runs on, such as a
		// closed stdout, so it counts as an environment problem.
		_, _ = fmt.Fprintf(w, "burro: %s\n", err)
		return int(ExitServer)
	}
	_, _ = fmt.Fprintf(w, "burro: %s\nfix: %s\n", err, e.fix)
	return int(e.code)
}
