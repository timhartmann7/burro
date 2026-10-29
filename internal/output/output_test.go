package output_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/output"
)

func TestReport(t *testing.T) {
	usage := output.NewError(output.ExitUsage, `unknown command "x"`, "run burro --help")
	tests := []struct {
		name   string
		err    error
		code   int
		stderr string
	}{
		{name: "nil", err: nil, code: 0, stderr: ""},
		{name: "coded", err: usage, code: 4, stderr: "burro: unknown command \"x\"\nfix: run burro --help\n"},
		{
			name:   "wrapped keeps code and context",
			err:    fmt.Errorf("sync: rsync to vps1: %w", output.NewError(output.ExitServer, "connection reset", "run burro doctor vps1")),
			code:   2,
			stderr: "burro: sync: rsync to vps1: connection reset\nfix: run burro doctor vps1\n",
		},
		{
			name:   "busy",
			err:    output.NewError(output.ExitBusy, "run r3 is active", "run burro wait"),
			code:   5,
			stderr: "burro: run r3 is active\nfix: run burro wait\n",
		},
		{name: "uncoded", err: errors.New("write output: broken pipe"), code: 2, stderr: "burro: write output: broken pipe\n"},
		{name: "silent", err: output.Silent(output.ExitFailed), code: 1, stderr: ""},
		{name: "silent wrapped", err: fmt.Errorf("exec: %w", output.Silent(output.ExitServer)), code: 2, stderr: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr strings.Builder
			if code := output.Report(&stderr, tt.err); code != tt.code {
				t.Errorf("code = %d, want %d", code, tt.code)
			}
			if stderr.String() != tt.stderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.stderr)
			}
		})
	}
}

func TestErrorParts(t *testing.T) {
	e := output.NewError(output.ExitBusy, "run r3 is active", "run burro wait")
	if e.Code() != output.ExitBusy || e.Fix() != "run burro wait" || e.Error() != "run r3 is active" {
		t.Fatalf("got %d %q %q", e.Code(), e.Fix(), e.Error())
	}
}

func TestExitCodes(t *testing.T) {
	// The numbers are a public contract in docs/reference/cli.md.
	got := []output.Code{output.ExitPassed, output.ExitFailed, output.ExitServer, output.ExitRunning, output.ExitUsage, output.ExitBusy}
	for want, code := range got {
		if int(code) != want {
			t.Errorf("exit code %d at position %d", code, want)
		}
	}
}
