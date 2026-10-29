// Package testutil holds test-only helpers: the sshd harness, the burro binary
// runner and polling. Only _test.go files import it.
package testutil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// commandTimeout bounds every external command a helper runs.
const commandTimeout = 2 * time.Minute

// Poll calls cond until it returns nil. After timeout it fails the test with
// the last error cond returned.
func Poll(tb testing.TB, timeout time.Duration, cond func() error) {
	tb.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		err := cond()
		if err == nil {
			return
		}
		select {
		case <-deadline.C:
			tb.Fatalf("not ready after %s: %v", timeout, err)
		case <-tick.C:
		}
	}
}

// Result is what one run of the burro binary produced.
type Result struct {
	Code   int
	Stdout string
	Stderr string
}

// BuildBurro builds cmd/burro into a temp folder and returns the binary path.
// flags go to go build, for example -ldflags.
func BuildBurro(tb testing.TB, flags ...string) string {
	tb.Helper()
	bin := filepath.Join(tb.TempDir(), "burro")
	args := append([]string{"build", "-o", bin}, flags...)
	args = append(args, "./cmd/burro")
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = moduleRoot(tb)
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// RunBurro runs the binary with args and captures exit code, stdout and stderr.
func RunBurro(tb testing.TB, bin string, args ...string) Result {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		tb.Fatalf("run %s: %v", bin, err)
	}
	return Result{Code: cmd.ProcessState.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String()}
}

// moduleRoot walks up from the test's working directory to the folder that
// holds go.mod.
func moduleRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatalf("no go.mod above the test folder")
		}
		dir = parent
	}
}
