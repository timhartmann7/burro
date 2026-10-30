package scripts_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// script returns the absolute path of a script in this folder.
func script(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Strings the cases below share.
const (
	trailers     = "trailers are not allowed"
	needsType    = "Conventional Commits type"
	head         = "HEAD"
	goFile       = "a.go"
	testFile     = "x/a_test.go"
	makefile     = "Makefile"
	coveredTest  = "internal/a/a_test.go"
	quotePaths   = "fix: quote paths"
	halfCoverage = "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A(1) != 1 {\n\t\tt.Fatal()\n\t}\n}\n"
)

// run executes name in dir with extra environment and returns its exit code
// and combined output.
func run(t *testing.T, dir string, env []string, name string, args ...string) (code int, out string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	combined, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(combined)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(combined)
	default:
		t.Fatalf("run %s: %v\n%s", name, err, combined)
		return 0, ""
	}
}

func mustRun(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	code, out := run(t, dir, env, name, args...)
	if code != 0 {
		t.Fatalf("%s %s: exit %d\n%s", name, strings.Join(args, " "), code, out)
	}
	return out
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// gitRepo returns a fresh repository whose commits use the project identity
// unless env overrides it.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, nil, "git", "init", "-q")
	mustRun(t, dir, nil, "git", "config", "user.name", "Tim Hartmann")
	mustRun(t, dir, nil, "git", "config", "user.email", "timhartmann7@proton.me")
	mustRun(t, dir, nil, "git", "config", "commit.gpgsign", "false")
	return dir
}

func commit(t *testing.T, dir string, env []string, msg string) {
	t.Helper()
	mustRun(t, dir, env, "git", "commit", "-q", "--allow-empty", "--no-verify", "-m", msg)
}

func TestEnglish(t *testing.T) {
	tests := []struct {
		name    string
		tracked map[string]string
		other   map[string]string
		code    int
		want    string
	}{
		{name: "clean", tracked: map[string]string{goFile: "package a\n// plain English\n"}, code: 0},
		{name: "Cyrillic letter", tracked: map[string]string{goFile: "package a\n\n// \u0436\n"}, code: 1, want: goFile + ":3:"},
		{name: "yo", tracked: map[string]string{"docs/x.md": "ok\n\u0451\n"}, code: 1, want: "docs/x.md:2:"},
		{name: "space in name", tracked: map[string]string{"a b.txt": "\u0410"}, code: 1, want: "a b.txt:1:"},
		{name: "supplement block", tracked: map[string]string{"s.txt": "\u0500"}, code: 1, want: "s.txt:1:"},
		{name: "extended block", tracked: map[string]string{"e.txt": "x\ua640"}, code: 1, want: "e.txt:1:"},
		{name: "Greek and Latin accents pass", tracked: map[string]string{"g.txt": "\u03b1 \u00e9 \u00b7\n"}, code: 0},
		{name: "untracked file is ignored", tracked: map[string]string{goFile: "package a\n"}, other: map[string]string{"new.txt": "\u0436"}, code: 0},
		{name: "binary file is ignored", tracked: map[string]string{"bin.dat": "\x00\xd0\xb6"}, code: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := gitRepo(t)
			writeFiles(t, dir, tt.tracked)
			mustRun(t, dir, nil, "git", "add", "-A")
			writeFiles(t, dir, tt.other)
			code, out := run(t, dir, nil, script(t, "english.sh"))
			if code != tt.code || !strings.Contains(out, tt.want) {
				t.Fatalf("exit %d, want %d with %q:\n%s", code, tt.code, tt.want, out)
			}
		})
	}
}

// acceptance: S0-03
func TestCommitMsg(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "accepted example", msg: "feat: add host add command\n"},
		{name: "scope and body", msg: "chore(deps): bump go to 1.27.3\n\nThe toolchain moves to the latest stable release.\n"},
		{name: "breaking change marker", msg: "feat!: rename the run command\n"},
		{name: "comment lines are dropped", msg: "fix: keep exit code 3 on Ctrl+C\n# Please enter the commit message\n# Co-Authored-By: someone\n"},
		{name: "domain words", msg: "docs: explain how an AI agent reads the summary\n"},
		{name: "72 characters", msg: "test: " + strings.Repeat("a", 66) + "\n"},
		{name: "72 characters with a middle dot", msg: "test: " + strings.Repeat("a", 65) + "\u00b7\n"},
		{name: "Co-Authored-By trailer", msg: "feat: add sync\n\nCo-Authored-By: Someone <someone@example.com>\n", want: trailers},
		{name: "Co-Authored-By in the body", msg: "feat: add sync\n\nco-authored-by someone, with more words\n", want: `"co-authored-by" is not allowed`},
		{name: "Signed-off-by trailer", msg: "fix: quote paths\n\nSigned-off-by: Someone <someone@example.com>\n", want: trailers},
		{name: "any trailer", msg: "fix: quote paths\n\nReviewed-by: Someone\n", want: trailers},
		{name: "Generated with", msg: "feat: add sync\n\nGenerated with a tool\n", want: `"generated with" is not allowed`},
		{name: "Claude", msg: "feat: add sync with Claude\n", want: `the word "claude" is not allowed`},
		{name: "claude lower case", msg: "docs: note for claude users\n", want: `the word "claude" is not allowed`},
		{name: "Anthropic", msg: "docs: thank Anthropic\n", want: `the word "anthropic" is not allowed`},
		{name: "ChatGPT", msg: "docs: compare with ChatGPT\n", want: `the word "chatgpt" is not allowed`},
		{name: "Copilot", msg: "docs: compare with Copilot\n", want: `the word "copilot" is not allowed`},
		{name: "noreply address", msg: "fix: x\n\nby noreply@anthropic.com\n", want: `"noreply@anthropic.com" is not allowed`},
		{name: "robot emoji", msg: "feat: add sync \U0001F916\n", want: "the robot emoji is not allowed"},
		{name: "Cyrillic", msg: "feat: add sync \u0436\n", want: "Cyrillic characters are not allowed"},
		{name: "Cyrillic in body", msg: "feat: add sync\n\n\u0451\n", want: "Cyrillic characters are not allowed"},
		{name: "73 characters", msg: "test: " + strings.Repeat("a", 67) + "\n", want: "subject is 73 characters"},
		{name: "long body line", msg: "fix: x\n\n" + strings.Repeat("b", 73) + "\n", want: "line 3 is longer than 72"},
		{name: "no type", msg: "Add host add command\n", want: needsType},
		{name: "unknown type", msg: "feature: add host add command\n", want: needsType},
		{name: "capital type", msg: "Feat: add host add command\n", want: needsType},
		{name: "no space after colon", msg: "feat:add host add command\n", want: needsType},
		{name: "empty subject", msg: "feat: \n", want: needsType},
		{name: "empty message", msg: "", want: needsType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"MSG": tt.msg})
			code, out := run(t, dir, nil, script(t, "commit-msg.sh"), "MSG")
			if tt.want == "" && code != 0 {
				t.Fatalf("rejected, want accepted:\n%s", out)
			}
			if tt.want != "" && (code != 1 || !strings.Contains(out, tt.want)) {
				t.Fatalf("exit %d, want 1 with %q:\n%s", code, tt.want, out)
			}
		})
	}
}

// acceptance: S0-03
func TestCommitMsgHook(t *testing.T) {
	// A copy of the hook setup: lefthook from the tools module runs the
	// commit-msg job from lefthook.yml on a real git commit.
	dir := gitRepo(t)
	for _, f := range []string{"go.mod", "lefthook.yml", "tools/go.mod", "tools/go.sum", "scripts/commit-msg.sh", "scripts/english.sh"} {
		content, err := os.ReadFile(filepath.Join("..", f))
		if err != nil {
			t.Fatal(err)
		}
		writeFiles(t, dir, map[string]string{f: string(content)})
	}
	mustRun(t, dir, nil, "chmod", "+x", "scripts/commit-msg.sh", "scripts/english.sh")
	mustRun(t, dir, nil, "go", "tool", "-modfile=tools/go.mod", "lefthook", "install")

	code, out := run(t, dir, nil, "git", "commit", "-q", "--allow-empty", "-m", "feat: add sync\n\nCo-Authored-By: Someone <someone@example.com>")
	if code == 0 || !strings.Contains(out, trailers) {
		t.Fatalf("hook let a Co-Authored-By trailer through: exit %d\n%s", code, out)
	}
	mustRun(t, dir, nil, "git", "commit", "-q", "--allow-empty", "-m", "feat: add host add command")
}

type fakeCommit struct {
	env []string
	msg string
}

// acceptance: S0-04
func TestCommitsSkipMergeButton(t *testing.T) {
	github := []string{"GIT_COMMITTER_NAME=GitHub", "GIT_COMMITTER_EMAIL=noreply@github.com"}
	stranger := []string{"GIT_AUTHOR_NAME=Someone", "GIT_AUTHOR_EMAIL=someone@example.com"}
	tests := []struct {
		name   string
		branch fakeCommit
		want   string
	}{
		{name: "merged good commit", branch: fakeCommit{msg: quotePaths}},
		{name: "merged bad commit", branch: fakeCommit{stranger, quotePaths}, want: "author is Someone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := gitRepo(t)
			commit(t, dir, nil, "build: initialize the module")
			mustRun(t, dir, nil, "git", "switch", "-q", "-c", "feature")
			commit(t, dir, tt.branch.env, tt.branch.msg)
			mustRun(t, dir, nil, "git", "switch", "-q", "-")
			mustRun(t, dir, github, "git", "merge", "-q", "--no-ff", "--no-verify", "-m", "Merge pull request #6 from timhartmann7/feature", "feature")
			code, out := run(t, dir, nil, script(t, "commits.sh"), "HEAD~1..HEAD")
			if tt.want == "" && code != 0 {
				t.Fatalf("failed on a merge of correct commits:\n%s", out)
			}
			if tt.want != "" && (code == 0 || !strings.Contains(out, tt.want)) {
				t.Fatalf("exit %d, want failure with %q:\n%s", code, tt.want, out)
			}
		})
	}
	// Outside a merge, GitHub as committer still fails.
	dir := gitRepo(t)
	commit(t, dir, github, quotePaths)
	if code, out := run(t, dir, nil, script(t, "commits.sh"), head); code == 0 || !strings.Contains(out, "committer is GitHub <noreply@github.com>") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestCommits(t *testing.T) {
	stranger := []string{"GIT_AUTHOR_NAME=Someone", "GIT_AUTHOR_EMAIL=someone@example.com"}
	committer := []string{"GIT_COMMITTER_NAME=Tim Hartmann", "GIT_COMMITTER_EMAIL=tim@example.com"}
	good := fakeCommit{msg: quotePaths}
	const sync = "feat: add sync"
	tests := []struct {
		name    string
		commits []fakeCommit
		rng     string
		want    string
	}{
		{name: "correct commits", rng: head, commits: []fakeCommit{good, {msg: sync}}},
		{name: "foreign author", rng: head, want: "author is Someone <someone@example.com>", commits: []fakeCommit{{stranger, sync}}},
		{name: "foreign committer email", rng: head, want: "committer is Tim Hartmann <tim@example.com>", commits: []fakeCommit{{committer, sync}}},
		{name: "bad message", rng: head, want: trailers, commits: []fakeCommit{{msg: sync + "\n\nSigned-off-by: Tim Hartmann <timhartmann7@proton.me>"}}},
		{name: "bad commit outside the range", rng: "HEAD~1..HEAD", commits: []fakeCommit{{stranger, sync}, good}},
		{name: "bad commit deep in the range", rng: head, want: "author is Someone", commits: []fakeCommit{{stranger, sync}, good}},
		{name: "unknown range", rng: "nosuchref..HEAD", want: "nosuchref", commits: []fakeCommit{good}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := gitRepo(t)
			commit(t, dir, nil, "build: initialize the module")
			for _, c := range tt.commits {
				commit(t, dir, c.env, c.msg)
			}
			code, out := run(t, dir, nil, script(t, "commits.sh"), tt.rng)
			if tt.want == "" && code != 0 {
				t.Fatalf("failed on correct commits:\n%s", out)
			}
			if tt.want != "" && (code == 0 || !strings.Contains(out, tt.want)) {
				t.Fatalf("exit %d, want failure with %q:\n%s", code, tt.want, out)
			}
		})
	}
}

func TestAcceptance(t *testing.T) {
	spec := "# S9\n\n## Acceptance\n\n" +
		"- [ ] **S9-01** first\n- [ ] **S9-02** second\n- [x] **S9-03** gate\n" +
		"- [ ] **S9-04** [release] smoke\n- [ ] **S9-10** tenth\n"
	full := map[string]string{
		"docs/slices/s9-test.md":              spec,
		testFile:                              "package x\n\n// acceptance: S9-01, S9-02\nfunc TestA(t *testing.T) {}\n\n// acceptance: S9-10\nfunc FuzzB(f *testing.F) {}\n",
		makefile:                              "# acceptance: S9-03\ncheck:\n\ttrue\n",
		".github/workflows/release-smoke.yml": "jobs:\n  smoke:\n    steps:\n      - name: S9-04 install on a clean runner\n        run: true\n",
	}
	tests := []struct {
		name   string
		change map[string]string
		args   []string
		code   int
		want   string
	}{
		{name: "all covered", code: 0, want: "all 5 IDs covered"},
		{name: "test reference removed", change: map[string]string{testFile: "package x\n\n// acceptance: S9-02\nfunc TestA(t *testing.T) {}\n\n// acceptance: S9-10\nfunc FuzzB(f *testing.F) {}\n"}, code: 1, want: "S9-01 has no reference"},
		{name: "comment not right above the func", change: map[string]string{testFile: "package x\n\n// acceptance: S9-01, S9-02, S9-10\n\nfunc TestA(t *testing.T) {}\n"}, code: 1, want: "3 of 5 IDs not covered"},
		{name: "reference outside a test file", change: map[string]string{testFile: "", "x/a.go": "package x\n\n// acceptance: S9-01, S9-02, S9-10\nfunc TestA() {}\n"}, code: 1, want: "3 of 5 IDs not covered"},
		{name: "S9-1 does not cover S9-10", change: map[string]string{testFile: "package x\n\n// acceptance: S9-01 S9-02 S9-1\nfunc TestA(t *testing.T) {}\n"}, code: 1, want: "S9-10 has no reference"},
		{name: "gate comment removed", change: map[string]string{makefile: "check:\n\ttrue\n"}, code: 1, want: "S9-03 has no reference in a test or gate"},
		{name: "gate comment in a workflow", change: map[string]string{makefile: "check:\n\ttrue\n", ".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n      # acceptance: S9-03\n      - run: make check\n"}, code: 0},
		{name: "release item needs a smoke step", change: map[string]string{makefile: "# acceptance: S9-03 S9-04\n", ".github/workflows/release-smoke.yml": ""}, code: 1, want: "S9-04 has no reference in a release-smoke.yml step name"},
		{name: "no slice file", args: []string{"s8"}, code: 4, want: "found 0"},
		{name: "no argument", args: []string{}, code: 4, want: "usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, full)
			writeFiles(t, dir, tt.change)
			args := tt.args
			if args == nil {
				args = []string{"s9"}
			}
			code, out := run(t, dir, nil, script(t, "acceptance.sh"), args...)
			if code != tt.code || !strings.Contains(out, tt.want) {
				t.Fatalf("exit %d, want %d with %q:\n%s", code, tt.code, tt.want, out)
			}
		})
	}
}

func TestCoverage(t *testing.T) {
	module := map[string]string{
		"go.mod":                    "module example.com/m\n\ngo 1.27.0\n",
		"internal/a/a.go":           "package a\n\nfunc A(n int) int {\n\tif n > 0 {\n\t\treturn n\n\t}\n\treturn -n\n}\n",
		coveredTest:                 "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A(1) != 1 || A(-1) != 1 {\n\t\tt.Fatal()\n\t}\n}\n",
		"internal/empty/empty.go":   "// Package empty has no statements yet.\npackage empty\n\nconst Name = \"empty\"\n",
		"internal/testutil/util.go": "package testutil\n\nfunc Never(n int) int {\n\tif n > 0 {\n\t\treturn n\n\t}\n\treturn 0\n}\n",
	}
	tests := []struct {
		name   string
		change map[string]string
		code   int
		want   []string
	}{
		{name: "passing", code: 0, want: []string{"example.com/m/internal/a", "100.0%", "total"}},
		{
			name: "integration tests count",
			change: map[string]string{
				coveredTest:                  halfCoverage,
				"internal/a/a_integ_test.go": "//go:build integration\n\npackage a\n\nimport \"testing\"\n\nfunc TestNegative(t *testing.T) {\n\tif A(-1) != 1 {\n\t\tt.Fatal()\n\t}\n}\n",
			},
			code: 0,
			want: []string{"100.0%"},
		},
		{
			name:   "package below its gate",
			change: map[string]string{coveredTest: halfCoverage},
			code:   1,
			want:   []string{"example.com/m/internal/a is below 80%", "total is below 85%"},
		},
		{
			name:   "failing test",
			change: map[string]string{coveredTest: "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tt.Fatal(\"broken\")\n}\n"},
			code:   1,
			want:   []string{"broken"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, module)
			writeFiles(t, dir, tt.change)
			code, out := run(t, dir, nil, script(t, "coverage.sh"))
			if code != tt.code {
				t.Fatalf("exit %d, want %d:\n%s", code, tt.code, out)
			}
			checkCoverageOutput(t, out, tt.want)
		})
	}
}

func checkCoverageOutput(t *testing.T, out string, want []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
	for _, skipped := range []string{"internal/testutil", "internal/empty"} {
		if strings.Contains(out, "coverage: example.com/m/"+skipped) {
			t.Fatalf("coverage lists %s, which it must skip:\n%s", skipped, out)
		}
	}
}

// acceptance: S0-11
func TestWorkflowsPinnedAndReadOnly(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", ".github", "workflows", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	uses := regexp.MustCompile(`^\s*(- )?uses:\s*(\S+)`)
	pinned := regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "\npermissions:\n  contents: read\n") {
			t.Errorf("%s: workflow default permissions are not contents: read", f)
		}
		for i, line := range strings.Split(string(content), "\n") {
			if m := uses.FindStringSubmatch(line); len(m) == 3 && !pinned.MatchString(m[2]) {
				t.Errorf("%s:%d: %s is not pinned to a full commit SHA", f, i+1, m[2])
			}
		}
	}
}
