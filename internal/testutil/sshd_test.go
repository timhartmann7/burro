//go:build integration

package testutil

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// acceptance: S0-08
func TestServer(t *testing.T) {
	s := StartServer(t)

	config, err := os.ReadFile(filepath.Join(s.Home(), ".ssh", "config"))
	if err != nil {
		t.Fatalf("read ssh config: %v", err)
	}
	if !strings.Contains(string(config), "Host "+sshHost+"\n") {
		t.Fatalf("ssh config has no entry for %s:\n%s", sshHost, config)
	}
	if out, err := s.SSH("whoami"); err != nil || out != "burro" {
		t.Fatalf("ssh whoami = %q, %v; want burro", out, err)
	}
	if _, err := s.SSH("true"); err != nil {
		t.Fatalf("ssh true: %v", err)
	}
	if _, err := s.SSH("false"); err == nil {
		t.Fatal("ssh false succeeded; remote exit status is lost")
	}

	s.SetLatency(t, 40*time.Millisecond)
	rtt := medianRoundTrip(t, s, 5)
	t.Logf("round trip with 40ms latency: %s", rtt)
	if rtt < 70*time.Millisecond || rtt > 150*time.Millisecond {
		t.Fatalf("round trip with latency = %s, want 70ms..150ms", rtt)
	}

	s.Stop(t)
	if out, err := run("docker", "ps", "--all", "--quiet", "--filter", "id="+s.container); err != nil || out != "" {
		t.Fatalf("container still listed after Stop: %q, %v", out, err)
	}
	if _, err := s.SSH("true"); err == nil {
		t.Fatal("ssh succeeded after Stop")
	}
}

func medianRoundTrip(t *testing.T, s *Server, n int) time.Duration {
	t.Helper()
	rtts := make([]time.Duration, 0, n)
	for range n {
		start := time.Now()
		if _, err := s.SSH("true"); err != nil {
			t.Fatalf("ssh true: %v", err)
		}
		rtts = append(rtts, time.Since(start))
	}
	slices.Sort(rtts)
	return rtts[n/2]
}

// acceptance: S0-09
func TestServerWithoutDocker(t *testing.T) {
	tests := []struct {
		name     string
		ci       string
		wantCode int
		want     string
	}{
		{name: "CI fails", ci: "true", wantCode: 1, want: "--- FAIL: TestServer "},
		{name: "local skips", ci: "", wantCode: 0, want: "--- SKIP: TestServer "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// An empty PATH hides docker. The test binary re-runs only
			// TestServer, so the harness itself decides fail or skip.
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestServer$", "-test.v", "-test.count=1")
			cmd.Env = append(withoutVars(os.Environ(), "PATH", "CI"), "PATH="+t.TempDir(), "CI="+tt.ci)
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("run test binary: %v", err)
				}
				code = exit.ExitCode()
			}
			if code != tt.wantCode || !strings.Contains(string(out), tt.want) {
				t.Fatalf("exit %d, want %d with %q in output:\n%s", code, tt.wantCode, tt.want, out)
			}
		})
	}
}

func withoutVars(env []string, names ...string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(names, name)
	})
}
