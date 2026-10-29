package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/timhartmann7/burro/internal/output"
)

// Labels that tie docker resources to a run: compose sets the first on
// everything of its project, steps set the second on what they start with
// docker run.
const (
	composeLabel = "com.docker.compose.project"
	runLabel     = "burro.run"
)

// Clean removes what the run with key left behind: its secrets folder, and
// the containers, networks and volumes of its compose project or with its
// burro.run label. Nothing else is touched. A host without docker, or
// whose daemon doesn't answer, has no docker leftovers to remove.
func Clean(ctx context.Context, key, secrets string) (*output.Cleaned, error) {
	cleaned := &output.Cleaned{}
	if _, err := os.Lstat(secrets); err == nil {
		cleaned.Secrets = true
	}
	if err := os.RemoveAll(secrets); err != nil {
		return nil, fmt.Errorf("remove secrets folder: %w", err)
	}
	d := &docker{key: key}
	if !d.reachable(ctx) {
		// Without docker, or with a daemon that doesn't answer, there is
		// nothing a step could have created.
		return cleaned, nil
	}
	containers, err := d.list(ctx, "ps", "-aq")
	if err != nil {
		return nil, err
	}
	networks, err := d.list(ctx, "network", "ls", "-q")
	if err != nil {
		return nil, err
	}
	volumes, err := d.list(ctx, "volume", "ls", "-q")
	if err != nil {
		return nil, err
	}
	// rm -f kills at once, where compose down would wait for each container
	// to stop.
	if len(containers) > 0 {
		if err := d.run(ctx, append([]string{"rm", "-f", "-v"}, containers...)...); err != nil {
			return nil, err
		}
	}
	// Compose removes its networks and volumes; it may be missing, and
	// then the labels below catch them.
	_ = d.run(ctx, "compose", "-p", key, "down", "-v", "--remove-orphans")
	if err := d.removeLeft(ctx, []string{"network", "ls", "-q"}, []string{"network", "rm"}); err != nil {
		return nil, err
	}
	if err := d.removeLeft(ctx, []string{"volume", "ls", "-q"}, []string{"volume", "rm", "-f"}); err != nil {
		return nil, err
	}
	cleaned.Containers, cleaned.Networks, cleaned.Volumes = len(containers), len(networks), len(volumes)
	return cleaned, nil
}

// docker runs the docker CLI for one run key.
type docker struct {
	key string
}

func (d *docker) reachable(ctx context.Context) bool {
	_, err := d.output(ctx, "version", "--format", "{{.Server.Version}}")
	return err == nil
}

// list runs a docker listing once per label and returns the ids of both.
func (d *docker) list(ctx context.Context, args ...string) ([]string, error) {
	var ids []string
	for _, label := range []string{composeLabel, runLabel} {
		out, err := d.output(ctx, append(args, "--filter", "label="+label+"="+d.key)...)
		if err != nil {
			return nil, err
		}
		for id := range strings.FieldsSeq(out) {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// removeLeft removes what list still finds.
func (d *docker) removeLeft(ctx context.Context, list, remove []string) error {
	ids, err := d.list(ctx, list...)
	if err != nil || len(ids) == 0 {
		return err
	}
	return d.run(ctx, append(remove, ids...)...)
}

func (d *docker) run(ctx context.Context, args ...string) error {
	_, err := d.output(ctx, args...)
	return err
}

func (d *docker) output(ctx context.Context, args ...string) (string, error) {
	//nolint:gosec // G204: the docker CLI with an argument slice; the run key is a hash and a checked run id.
	cmd := exec.CommandContext(ctx, "docker", args...)
	// Out of the project folder, compose reads no compose file of its own.
	cmd.Dir = os.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, lastLine(stderr.String()))
	}
	return stdout.String(), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
