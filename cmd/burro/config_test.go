package main_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/testutil"
)

// acceptance: S1-18
func TestBadConfigNeverTouchesNetwork(t *testing.T) {
	bin := testutil.BuildBurro(t)
	home := t.TempDir()
	hostsFile := filepath.Join(home, ".ssh", "config.d", "burro")
	if err := os.MkdirAll(filepath.Dir(hostsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsFile, []byte("# burro-host vps1\nHost vps1\n  HostName 192.0.2.1\n  User root\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// ssh and rsync stand-ins that only record that someone called them.
	fakes := t.TempDir()
	record := filepath.Join(t.TempDir(), "calls")
	for _, tool := range []string{"ssh", "rsync"} {
		script := "#!/bin/sh\necho \"$0 $*\" >>'" + record + "'\nexit 255\n"
		path := filepath.Join(fakes, tool)
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // G302: the stand-in has to be executable.
			t.Fatal(err)
		}
	}
	env := []string{"HOME=" + home, "PATH=" + fakes + string(os.PathListSeparator) + os.Getenv("PATH")}

	tests := []struct {
		name  string
		toml  string
		field string
	}{
		{name: "unknown key", toml: "host = \"vps1\"\ncontinue_on_fial = true\n", field: `"continue_on_fial"`},
		{name: "bad host name", toml: "host = \"vps1; rm -rf ~\"\n", field: "host"},
		{name: "host not added", toml: "host = \"vps2\"\n", field: "vps2"},
		{name: "path outside the allowed form", toml: "host = \"vps1\"\npath = \"~/../../etc\"\n", field: "path"},
		{name: "relative path", toml: "host = \"vps1\"\npath = \"app\"\n", field: "path"},
		{name: "broken TOML", toml: "host = [\n", field: "line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.GitRepo(t, map[string]string{"burro.toml": tt.toml, "check.sh": "true\n"})
			for _, args := range [][]string{{"sync"}, {"exec", "--", "true"}, {"exec", "--file", "check.sh"}, {"doctor"}} {
				checkRefused(t, testutil.RunBurroIn(t, bin, repo, env, args...), tt.field)
				if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
					calls, _ := os.ReadFile(record)
					t.Fatalf("burro %q reached the network:\n%s", args, calls)
				}
			}
		})
	}
}

// checkRefused wants exit 4, nothing on stdout and two stderr lines that name
// field and end with a fix line.
func checkRefused(t *testing.T, got testutil.Result, field string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(got.Stderr, "\n"), "\n")
	if got.Code != 4 || got.Stdout != "" || len(lines) != 2 || !strings.HasPrefix(lines[1], "fix: ") {
		t.Fatalf("exit %d stdout %q stderr %q", got.Code, got.Stdout, got.Stderr)
	}
	if !strings.Contains(got.Stderr, field) {
		t.Fatalf("stderr doesn't name %s: %q", field, got.Stderr)
	}
}
