package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/initcmd"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

// runIn runs one command in dir with HOME set to home and reports it the
// way cmd/burro does.
func runIn(t *testing.T, dir, home string, args ...string) testutil.Result {
	t.Helper()
	t.Chdir(dir)
	var stdout, stderr strings.Builder
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	code := output.Report(&stderr, New(strings.NewReader(""), &stdout, &stderr, env, "").Run(args))
	return testutil.Result{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// withHosts writes a hosts file with one entry per name.
func withHosts(t *testing.T, names ...string) string {
	t.Helper()
	home := t.TempDir()
	var b strings.Builder
	for _, n := range names {
		b.WriteString("# burro-host " + n + "\nHost " + n + "\n  HostName 203.0.113.10\n  User root\n")
	}
	writeFile(t, filepath.Join(home, ".ssh", "config.d", "burro"), b.String())
	return home
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInit(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{"README.md": "x\n"})
	if err := os.Mkdir(filepath.Join(repo, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	r := runIn(t, filepath.Join(repo, "sub"), withHosts(t, "vps1"), "init")
	want := "created    burro.toml\ncreated    AGENTS.md    rules for your agent\ncreated    CLAUDE.md    @AGENTS.md\ncreated    .gitignore   .burro/\n"
	if r.Code != 0 || r.Stdout != want || r.Stderr != "" {
		t.Fatalf("exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(repo, "burro.toml"))
	if err != nil || !strings.Contains(string(data), "host = \"vps1\"\n") {
		t.Fatalf("burro.toml %q, %v", data, err)
	}
	if r := runIn(t, repo, withHosts(t, "vps1"), "init", argJSON); r.Code != 0 || strings.Count(r.Stdout, `"change":"unchanged"`) != 4 {
		t.Fatalf("second init: %d %q", r.Code, r.Stdout)
	}
	// With two hosts burro can't pick one.
	repo = testutil.GitRepo(t, nil)
	runIn(t, repo, withHosts(t, "a", "b"), "init")
	if data, _ := os.ReadFile(filepath.Join(repo, "burro.toml")); !strings.Contains(string(data), "host = \"\"\n") {
		t.Fatalf("burro.toml %q", data)
	}
	r = runIn(t, repo, withHosts(t), "init", "--agents-file", "docs/RULES.md")
	if r.Code != 4 || !strings.Contains(r.Stderr, "docs/RULES.md") {
		t.Fatalf("missing folder: %d %q", r.Code, r.Stderr)
	}
	for _, outside := range []string{"../RULES.md", ".", "/etc/RULES.md"} {
		if r := runIn(t, repo, withHosts(t), "init", "--agents-file", outside); r.Code != 4 || !strings.Contains(r.Stderr, "fix: pass a file inside the project") {
			t.Fatalf("%s: %d %q", outside, r.Code, r.Stderr)
		}
	}
}

func TestInitOutsideGit(t *testing.T) {
	dir := t.TempDir()
	r := runIn(t, dir, withHosts(t), "init")
	if r.Code != 4 || !strings.Contains(r.Stderr, "is not inside a git repository\nfix: ") {
		t.Fatalf("exit %d %q", r.Code, r.Stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("init outside git wrote %v", entries)
	}
}

// acceptance: S3-14
func TestInitPrint(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{"AGENTS.md": "mine\n"})
	before := testutil.Git(t, repo, "status", "--porcelain", "--ignored")
	r := runIn(t, repo, withHosts(t, "vps1"), "init", "--print")
	if r.Code != 0 || r.Stdout != initcmd.Block() || r.Stderr != "" {
		t.Fatalf("exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	if after := testutil.Git(t, repo, "status", "--porcelain", "--ignored"); after != before {
		t.Fatalf("--print changed files: %q", after)
	}
	r = runIn(t, t.TempDir(), withHosts(t), "init", "--print", argJSON)
	var got struct{ Block string }
	if err := json.Unmarshal([]byte(r.Stdout), &got); r.Code != 0 || err != nil || got.Block != initcmd.Block() {
		t.Fatalf("--print --json outside git: %d %q", r.Code, r.Stdout)
	}
}

func TestFailuresAndCleanArguments(t *testing.T) {
	repo := testutil.GitRepo(t, nil)
	for _, args := range [][]string{
		{argFails, "--max-lines", "0"},
		{argFails, "--max-lines", "many"},
		{argFails, "../r1"},
		{argClean, "r0"},
		{argClean, "r1", "r2"},
	} {
		if r := runIn(t, repo, withHosts(t), args...); r.Code != 4 || !strings.Contains(r.Stderr, "fix: ") {
			t.Errorf("%q: %d %q", args, r.Code, r.Stderr)
		}
	}
}
