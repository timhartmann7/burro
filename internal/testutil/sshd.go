package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sshHost is the alias the harness writes into the test HOME's ssh config.
const sshHost = "burro-test"

// readyTimeout bounds how long a fresh container may take to accept ssh.
const readyTimeout = 30 * time.Second

// Server is one sshd container plus a client HOME that can reach it.
type Server struct {
	container  string
	home       string
	config     string
	controlDir string
	stopped    bool
}

// StartServer starts a fresh sshd container and a HOME with a generated key,
// known_hosts and a config entry for it. The container stops when the test
// ends. Without Docker the test skips, or fails when CI=true.
func StartServer(tb testing.TB) *Server {
	tb.Helper()
	requireDocker(tb)
	image := mustRun(tb, "docker", "build", "--quiet", filepath.Join(moduleRoot(tb), "test", "sshd"))

	home := tb.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		tb.Fatalf("create .ssh: %v", err)
	}
	key := filepath.Join(sshDir, "id_ed25519")
	mustRun(tb, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", sshHost, "-f", key)
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		tb.Fatalf("read public key: %v", err)
	}

	// ControlPath sockets are limited to about 100 bytes, and TempDir paths
	// on macOS are already longer than that.
	controlDir, err := os.MkdirTemp("/tmp", "burro-cm-")
	if err != nil {
		tb.Fatalf("create control dir: %v", err)
	}

	s := &Server{
		container: mustRun(tb, "docker", "run", "--detach", "--rm",
			"--cap-add", "NET_ADMIN",
			"--publish", "127.0.0.1::22",
			"--label", sshHost,
			"--env", "AUTHORIZED_KEY="+strings.TrimSpace(string(pub)),
			image),
		home:       home,
		config:     filepath.Join(sshDir, "config"),
		controlDir: controlDir,
	}
	tb.Cleanup(func() {
		s.Stop(tb)
		if err := os.RemoveAll(controlDir); err != nil {
			tb.Errorf("remove control dir: %v", err)
		}
	})

	port := mustRun(tb, "docker", "port", s.container, "22/tcp")
	_, port, ok := strings.Cut(port, ":")
	if !ok {
		tb.Fatalf("docker port: unexpected output %q", port)
	}
	var hostKey string
	Poll(tb, readyTimeout, func() error {
		hostKey, err = run("docker", "exec", s.container, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")
		return err
	})
	fields := strings.Fields(hostKey)
	if len(fields) < 2 {
		tb.Fatalf("host key: unexpected output %q", hostKey)
	}
	knownHosts := filepath.Join(sshDir, "known_hosts")
	writeFile(tb, knownHosts, fmt.Sprintf("[127.0.0.1]:%s %s %s\n", port, fields[0], fields[1]))
	writeFile(tb, s.config, fmt.Sprintf(`Host %s
  HostName 127.0.0.1
  Port %s
  User burro
  IdentityFile %s
  IdentitiesOnly yes
  IdentityAgent none
  UserKnownHostsFile %s
`, sshHost, port, key, knownHosts))

	Poll(tb, readyTimeout, func() error {
		_, err := s.SSH("true")
		return err
	})
	return s
}

// Home is the isolated HOME of the client side. Its .ssh/config has a ready
// entry for the server.
func (s *Server) Home() string {
	return s.home
}

// SSH runs args on the server with the options burro passes to every call,
// so repeated calls share one connection.
func (s *Server) SSH(args ...string) (string, error) {
	return run("ssh", append(s.sshOptions(), append([]string{sshHost}, args...)...)...)
}

// SetLatency delays every packet the server sends by d. 40ms gives about
// 80 ms per ssh round trip.
func (s *Server) SetLatency(tb testing.TB, d time.Duration) {
	tb.Helper()
	mustRun(tb, "docker", "exec", s.container,
		"tc", "qdisc", "replace", "dev", "eth0", "root", "netem", "delay", fmt.Sprintf("%dms", d.Milliseconds()))
}

// Stop closes the shared connection and removes the container. It is safe to
// call more than once.
func (s *Server) Stop(tb testing.TB) {
	tb.Helper()
	if s.stopped {
		return
	}
	s.stopped = true
	// The control master may already be gone; removing the container is
	// what has to succeed.
	_, _ = run("ssh", append(s.sshOptions(), "-O", "exit", sshHost)...)
	if _, err := run("docker", "rm", "--force", s.container); err != nil {
		tb.Errorf("remove container: %v", err)
	}
}

func (s *Server) sshOptions() []string {
	return []string{
		"-F", s.config,
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(s.controlDir, "%C"),
		"-o", "ControlPersist=10m",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=10",
	}
}

func requireDocker(tb testing.TB) {
	tb.Helper()
	_, err := exec.LookPath("docker")
	if err == nil {
		_, err = run("docker", "info", "--format", "{{.ServerVersion}}")
	}
	if err == nil {
		return
	}
	if os.Getenv("CI") == "true" {
		tb.Fatalf("docker is required in CI: %v", err)
	}
	tb.Skipf("docker is not available: %v", err)
}

// run executes one command with a deadline and returns its trimmed combined
// output. The error carries the output, which is where tools explain failures.
func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func mustRun(tb testing.TB, name string, args ...string) string {
	tb.Helper()
	out, err := run(name, args...)
	if err != nil {
		tb.Fatal(err)
	}
	return out
}

func writeFile(tb testing.TB, path, content string) {
	tb.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		tb.Fatalf("write %s: %v", path, err)
	}
}
