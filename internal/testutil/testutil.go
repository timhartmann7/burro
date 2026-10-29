// Package testutil holds test-only helpers: the sshd harness, the burro binary
// runner and polling. Only _test.go files import it.
package testutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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

// moduleRoot is the folder that holds go.mod. Tests may change their working
// folder, so the source location of this file comes first; a -trimpath build
// falls back to walking up from the working folder.
func moduleRoot(tb testing.TB) string {
	tb.Helper()
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		root := filepath.Join(filepath.Dir(file), "..", "..")
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
	}
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

// BuildServerBurro builds cmd/burro for linux/goarch with version linked in,
// the way make build makes the binaries a release embeds.
func BuildServerBurro(tb testing.TB, version, goarch string) []byte {
	tb.Helper()
	bin := filepath.Join(tb.TempDir(), "burro-linux-"+goarch)
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X main.version="+version, "-o", bin, "./cmd/burro")
	cmd.Dir = moduleRoot(tb)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("go build for linux/%s: %v\n%s", goarch, err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		tb.Fatalf("read server binary: %v", err)
	}
	return data
}

// RunBurroIn runs the binary in dir with exactly env and captures exit code,
// stdout and stderr.
func RunBurroIn(tb testing.TB, bin, dir string, env []string, args ...string) Result {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		tb.Fatalf("run %s: %v", bin, err)
	}
	return Result{Code: cmd.ProcessState.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String()}
}

// RunBurroTTY runs the binary in dir with env on a pseudo-terminal through
// script(1) and returns what the terminal showed.
func RunBurroTTY(tb testing.TB, bin, dir string, env []string, args ...string) string {
	tb.Helper()
	argv := append([]string{"-q", "/dev/null", bin}, args...)
	if runtime.GOOS == "linux" {
		// util-linux script takes one command line instead of argv.
		line := bin
		for _, a := range args {
			line += " '" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		argv = []string{"-q", "-e", "-c", line, "/dev/null"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "script", argv...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		tb.Fatalf("script %s: %v", bin, err)
	}
	return string(out)
}

// ansi matches the escape sequences burro could print.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// StripANSI removes ANSI escape sequences from s.
func StripANSI(s string) string {
	return ansi.ReplaceAllString(s, "")
}

// HasANSI tells whether s holds an escape character.
func HasANSI(s string) bool {
	return strings.Contains(s, "\x1b")
}

// GitRepo creates a git repository in a fresh folder with files committed
// and returns the folder. Keys are slash-separated paths.
func GitRepo(tb testing.TB, files map[string]string) string {
	tb.Helper()
	dir := tb.TempDir()
	// git reports the resolved path, so the tests work with it too.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		tb.Fatalf("resolve temp dir: %v", err)
	}
	Git(tb, dir, "init", "-q", "-b", "main")
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			tb.Fatalf("create folder: %v", err)
		}
		writeFile(tb, path, content)
	}
	Git(tb, dir, "add", "-A")
	Git(tb, dir, "commit", "-q", "--allow-empty", "-m", "fixture")
	return dir
}

// Git runs git in dir with a fixed identity and returns its trimmed output.
func Git(tb testing.TB, dir string, args ...string) string {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// FuzzSeeds returns the seed inputs in testdata/fuzz/<name> at the module
// root, one file per input.
func FuzzSeeds(tb testing.TB, name string) [][]byte {
	tb.Helper()
	paths, err := filepath.Glob(filepath.Join(moduleRoot(tb), "testdata", "fuzz", name, "*"))
	if err != nil || len(paths) == 0 {
		tb.Fatalf("no fuzz seeds for %s: %v", name, err)
	}
	seeds := make([][]byte, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			tb.Fatalf("read seed: %v", err)
		}
		seeds = append(seeds, data)
	}
	return seeds
}

// NoisyLog generates about size bytes of a test runner's log the way a
// terminal-aware tool writes it: colors, \r progress redraws, cursor codes,
// raw binary bytes and broken UTF-8. Every 500th line mentions "cohorts".
// The same seed gives the same log.
func NoisyLog(size int, seed uint64) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < size; i++ {
		// Hashing the seed and the line number gives bytes that look random
		// and are the same on every run.
		noise := sha256.Sum256(fmt.Appendf(nil, "%d/%d", seed, i))
		switch {
		case i%500 == 0:
			fmt.Fprintf(&b, "\x1b[31m  x\x1b[0m cohorts are listed (%d)\n", i)
		case i%7 == 0:
			for p := 0; p <= 100; p += 25 {
				fmt.Fprintf(&b, "\x1b[2K\rdownloading %3d%% \x1b[?25l", p)
			}
			b.WriteString("\x1b[?25h\n")
		case i%11 == 0:
			b.Write(noise[:16+int(noise[0])%16])
			b.WriteString("\n")
		case i%13 == 0:
			b.WriteString("\x1b]0;title\x07\x1b[1;32m\u2713\x1b[m caf\xc3 \xff\xfe test done\r\n")
		default:
			fmt.Fprintf(&b, "\x1b[90m[%06d]\x1b[39m step output line with some text %x\n", i, noise[:4])
		}
	}
	return b.Bytes()
}
