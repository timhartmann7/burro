//go:build integration

package runner

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/output"
)

// image is the test server's base image, the one image tests may run.
const image = "debian:stable-slim@sha256:13e0a2e78aa0554ad5ff66e4e260c6dbcdff807eb1f39d43b9fb0288aae88b04"

// dockerCmd returns what docker prints on stdout. stderr carries pull
// progress when the image isn't there yet, so it only explains failures.
func dockerCmd(t *testing.T, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), "docker", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// acceptance: S3-07
func TestCleanDocker(t *testing.T) {
	key := "burro-" + marker(t)[4:] + "ab-r1"
	foreign := dockerCmd(t, "run", "-d", "--rm", image, "sleep", "600")
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", foreign).Run() })
	labeled := dockerCmd(t, "run", "-d", "--label", runLabel+"="+key, image, "sleep", "600")
	composed := dockerCmd(t, "run", "-d", "--label", composeLabel+"="+key, image, "sleep", "600")
	dockerCmd(t, "network", "create", "--label", runLabel+"="+key, key+"-net")
	dockerCmd(t, "volume", "create", "--label", composeLabel+"="+key, key+"-data")
	dockerCmd(t, "volume", "create", "--label", runLabel+"=burro-other-r1", key+"-other")
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "docker", "volume", "rm", "-f", key+"-other").Run()
	})

	c, err := Clean(context.Background(), key, t.TempDir()+"/gone")
	if err != nil || *c != (output.Cleaned{Containers: 2, Networks: 1, Volumes: 1}) {
		t.Fatalf("got %+v, %v", c, err)
	}
	all := dockerCmd(t, "ps", "-aq", "--no-trunc")
	if !strings.Contains(all, foreign) || strings.Contains(all, labeled) || strings.Contains(all, composed) {
		t.Fatalf("containers left: %s", all)
	}
	if vols := dockerCmd(t, "volume", "ls", "-q"); strings.Contains(vols, key+"-data") || !strings.Contains(vols, key+"-other") {
		t.Fatalf("volumes: %s", vols)
	}
	if c, err := Clean(context.Background(), key, t.TempDir()); err != nil || *c != (output.Cleaned{Secrets: true}) {
		t.Fatalf("second clean: %+v, %v", c, err)
	}
}
