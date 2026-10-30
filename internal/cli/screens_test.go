package cli

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	welcomeTitle = "Get started"
	burroModeEnv = "BURRO_MODE="
	noCheckEnv   = "BURRO_NO_UPDATE_CHECK=1"
	claudeEnv    = "CLAUDECODE=1"
	noColorEnv   = "NO_COLOR=1"
	helpTitle    = "burro · run your coding agent's tests on your own server\n"
)

// screen runs burro in process with env and returns stdout and stderr.
func screen(t *testing.T, env []string, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	a := New(strings.NewReader(""), &out, &errOut, env, "0.1.0")
	if err := a.Run(args); err != nil {
		t.Fatalf("burro %q: %v", args, err)
	}
	return out.String(), errOut.String()
}

// acceptance: S4-12
func TestBareShowsWelcomeUntilFirstHost(t *testing.T) {
	empty := t.TempDir()
	human := []string{"HOME=" + empty, burroModeEnv + humanMode, noColorEnv}
	out, _ := screen(t, human)
	testutil.Golden(t, "welcome.txt", out)

	colored, _ := screen(t, []string{"HOME=" + empty, burroModeEnv + humanMode})
	if !strings.Contains(colored, "\x1b[1m"+welcomeTitle+"\x1b[0m") || !strings.Contains(colored, "\x1b[34mburro init\x1b[0m") {
		t.Fatalf("welcome without bold titles and blue commands:\n%s", colored)
	}
	if testutil.StripANSI(colored) != out {
		t.Fatal("the colored welcome reads differently without color")
	}
	// An agent never gets the welcome, host or not.
	for _, env := range [][]string{
		{"HOME=" + empty, burroModeEnv + agentMode},
		{"HOME=" + empty, claudeEnv},
		{"HOME=" + empty},
	} {
		if out, _ := screen(t, env); !strings.HasPrefix(out, helpTitle) {
			t.Fatalf("env %q: bare burro printed\n%s", env, out)
		}
	}
	// After the first host add a human gets the help.
	withHost := withHosts(t, "vps1")
	if out, _ := screen(t, []string{"HOME=" + withHost, burroModeEnv + humanMode}); !strings.HasPrefix(out, helpTitle) {
		t.Fatalf("with a host bare burro printed\n%s", out)
	}
	if out, _ := screen(t, []string{"HOME=" + withHost, burroModeEnv + humanMode}, "--help"); !strings.HasPrefix(out, helpTitle) {
		t.Fatalf("--help printed\n%s", out)
	}
	// A broken mode variable falls back to the help, never to an error.
	if out, _ := screen(t, []string{"HOME=" + empty, burroModeEnv + "robot"}); !strings.HasPrefix(out, helpTitle) {
		t.Fatalf("BURRO_MODE=robot printed\n%s", out)
	}
}

// docsAbsent tells whether err means a CI checkout has no docs/, which stays
// out of git. The checks against docs then run wherever docs/ is present.
func docsAbsent(t *testing.T, err error) bool {
	t.Helper()
	if errors.Is(err, fs.ErrNotExist) && os.Getenv("CI") != "" {
		t.Log("docs/ is not in this checkout")
		return true
	}
	return false
}

// The welcome is the text docs/slices/s4-release-onboarding.md fixes.
func TestWelcomeMatchesSlice(t *testing.T) {
	out, _ := screen(t, []string{"HOME=" + t.TempDir(), burroModeEnv + humanMode})
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "slices", "s4-release-onboarding.md"))
	if docsAbsent(t, err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)The text, exactly:\n\n```\n(.*?)```").FindSubmatch(doc)
	if m == nil {
		t.Fatal("no welcome text in the slice file")
	}
	want := strings.ReplaceAll(string(m[1]), "burro <version>", "burro 0.1.0")
	if got := testutil.StripANSI(out); got != want {
		t.Fatalf("welcome differs from the slice\n--- got\n%s--- want\n%s", got, want)
	}
}

func checkScreen(t *testing.T, name, text string, maxLines int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(testutil.StripANSI(text), "\n"), "\n")
	if len(lines) > maxLines {
		t.Errorf("%s: %d lines, want at most %d", name, len(lines), maxLines)
	}
	for _, l := range lines {
		if utf8.RuneCountInString(l) > helpWidth {
			t.Errorf("%s: line over %d columns: %q", name, helpWidth, l)
		}
	}
}

// acceptance: S4-13
func TestScreenSizes(t *testing.T) {
	human := []string{"HOME=" + t.TempDir(), burroModeEnv + humanMode}
	welcome, _ := screen(t, human)
	checkScreen(t, "welcome", welcome, 30)
	help, _ := screen(t, nil, "--help")
	checkScreen(t, "--help", help, 35)
	config, _ := screen(t, nil, "help", "config")
	checkScreen(t, "help config", config, 50)
	app := New(nil, io.Discard, io.Discard, nil, "")
	for i := range app.commands {
		c := &app.commands[i]
		out, _ := screen(t, nil, append(strings.Fields(c.name), "--help")...)
		checkScreen(t, c.name+" --help", out, 15)
	}
}

// The step fields and JUnit rows of docs/reference/config.md, which stays
// out of git.
func configFields() (fields []string, junit map[string][]string) {
	fields = []string{"run", "env", "timeout", "continue_on_fail", "skip_if_unchanged", "junit", "secrets"}
	junit = map[string][]string{
		"Python":       {"pytest --junitxml=.burro/out/py.xml"},
		"Go":           {"gotestsum --junitfile .burro/out/go.xml ./..."},
		"Rust":         {"cargo nextest run --profile ci", "[profile.ci.junit]", ".config/nextest.toml"},
		"PHP":          {"phpunit --log-junit .burro/out/php.xml"},
		"Java, Kotlin": {`junit = "target/surefire-reports/*.xml"`, `"build/test-results/test/*.xml"`},
		"Jest":         {"jest-junit", "JEST_JUNIT_OUTPUT_FILE=.burro/out/jest.xml"},
		"Vitest":       {"--reporter=junit --outputFile=.burro/out/unit.xml"},
		"Playwright":   {"--reporter=junit", "PLAYWRIGHT_JUNIT_OUTPUT_NAME=.burro/out/e2e.xml"},
	}
	return fields, junit
}

// acceptance: S4-18
func TestHelpConfig(t *testing.T) {
	out, stderr := screen(t, nil, "help", "config")
	testutil.Golden(t, "help-config.txt", out)
	if stderr != "" {
		t.Fatalf("stderr %q", stderr)
	}
	fields, junit := configFields()
	for _, f := range fields {
		if !regexp.MustCompile(`(?m)^  ` + f + ` +\S`).MatchString(out) {
			t.Errorf("no line for step field %s", f)
		}
	}
	for stack, spans := range junit {
		if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(stack) + `  `).MatchString(out) {
			t.Errorf("no JUnit row for %s", stack)
		}
		for _, s := range spans {
			if !strings.Contains(out, s) {
				t.Errorf("JUnit row %s lacks %q", stack, s)
			}
		}
	}
	// Other arguments after help are not a help screen.
	for _, args := range [][]string{{"help"}, {"help", "config", "x"}} {
		if code := output.Report(io.Discard, New(nil, io.Discard, io.Discard, nil, "").Run(args)); code != 4 {
			t.Fatalf("burro %q: exit %d", args, code)
		}
	}
}

// The minimal config in help config is itself a valid burro.toml.
func TestHelpConfigExampleWorks(t *testing.T) {
	out, _ := screen(t, nil, "help", "config")
	start := strings.Index(out, "  host = ")
	end := strings.Index(out, "\nTop level")
	toml := strings.ReplaceAll(out[start:end], "\n  ", "\n")
	repo := testutil.GitRepo(t, map[string]string{tomlFile: strings.TrimPrefix(toml, "  ")})
	t.Chdir(repo)
	a := New(nil, io.Discard, io.Discard, []string{"HOME=" + withHosts(t, "vps1")}, "")
	proj, err := a.project(t.Context())
	if err != nil {
		t.Fatalf("the minimal config in help config fails: %v", err)
	}
	if _, err := proj.cfg.Select("default", "", nil); err != nil {
		t.Fatalf("its default pipeline: %v", err)
	}
}

func TestHelpConfigMatchesDocs(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "config.md"))
	if docsAbsent(t, err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	fields, junit := configFields()
	_, steps, _ := strings.Cut(text, "## Step fields")
	steps, _, _ = strings.Cut(steps, "## Validation")
	var docFields []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|").FindAllStringSubmatch(steps, -1) {
		docFields = append(docFields, m[1])
	}
	if strings.Join(docFields, " ") != strings.Join(fields, " ") {
		t.Fatalf("config.md step fields %q, the test knows %q", docFields, fields)
	}
	_, table, _ := strings.Cut(text, "## JUnit on other stacks")
	// The header row is the only one without a code span.
	rows := regexp.MustCompile("(?m)^\\| ([A-Z][^|]*?) \\| (.*`.*) \\|$").FindAllStringSubmatch(table, -1)
	if len(rows) != len(junit) {
		t.Fatalf("config.md has %d JUnit rows, the test knows %d", len(rows), len(junit))
	}
	for _, r := range rows {
		var spans []string
		for _, s := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(r[2], -1) {
			spans = append(spans, s[1])
		}
		if strings.Join(spans, "|") != strings.Join(junit[r[1]], "|") {
			t.Errorf("JUnit row %s: config.md %q, the test knows %q", r[1], spans, junit[r[1]])
		}
	}
}

// commandTable is the command column of docs/reference/cli.md, which stays
// out of git.
func commandTable() []string {
	return []string{
		"burro host add <name> <user@host[:port]> [-i key] [--force]",
		"burro host ls",
		"burro doctor [host]",
		"burro exec [--max-lines 60] [--] cmd args…` or `burro exec --file path [args…]",
		"burro sync",
		"burro run [pipeline] [--from step] [--only a,b]",
		"burro wait [id] [--timeout 60m]",
		"burro status [id]",
		"burro log [id] --step s [--grep re] [--tail 60] [--context 5]",
		"burro cancel [id]",
		"burro failures [id] [--max-lines 40]",
		"burro clean [id]",
		"burro init [--agents-file path] [--print]",
	}
}

// acceptance: S4-14
func TestRegistryMatchesCommandTable(t *testing.T) {
	app := New(nil, io.Discard, io.Discard, nil, "")
	var usages []string
	for i := range app.commands {
		c := &app.commands[i]
		usages = append(usages, "burro "+strings.Join(c.usageLines(), "` or `burro "))
		if c.summary == "" || c.about == "" || c.example == "" || len(c.exits) == 0 || c.run == nil {
			t.Errorf("command %s lacks its help line, help screen or handler", c.name)
		}
	}
	want := commandTable()
	if strings.Join(slices.Sorted(slices.Values(usages)), "\n") != strings.Join(slices.Sorted(slices.Values(want)), "\n") {
		t.Fatalf("registry usages\n%s\nwant the command table\n%s", strings.Join(usages, "\n"), strings.Join(want, "\n"))
	}
	help := app.help()
	for i := range app.commands {
		c := &app.commands[i]
		if !strings.Contains(help, "\n  "+c.briefText()+" ") || !strings.Contains(help, " "+c.summary+"\n") {
			t.Errorf("--help lacks %s", c.name)
		}
	}
	if !strings.Contains(help, "\nburro help config ") {
		t.Error("--help lacks the help config line")
	}

	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "cli.md"))
	if docsAbsent(t, err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var docRows []string
	for _, m := range regexp.MustCompile("(?m)^\\| `(burro .*?)` \\| .* \\| S\\d \\|$").FindAllStringSubmatch(string(doc), -1) {
		docRows = append(docRows, m[1])
	}
	if strings.Join(docRows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("cli.md command table\n%s\nthe test knows\n%s", strings.Join(docRows, "\n"), strings.Join(want, "\n"))
	}
}

// latest serves a latest-release reply naming v9.9.9 and counts calls.
func latest(t *testing.T) (url string, calls *int) {
	t.Helper()
	calls = new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, calls
}

// installed runs burro in process as a binary at exe, with the version
// check asking url.
func installed(t *testing.T, home, exe, url string, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	a := New(nil, &out, &errOut, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, "0.1.0")
	a.executable = func() (string, error) { return exe, nil }
	a.latestAPI = url
	if err := a.Run(args); err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String()
}

// acceptance: S4-09
func TestInstallMethod(t *testing.T) {
	home := t.TempDir()
	platform := runtime.GOOS + "/" + runtime.GOARCH
	tests := []struct {
		method, exe, update string
	}{
		{"brew", "/opt/homebrew/Caskroom/burro/0.1.0/burro", "brew upgrade burro"},
		{"go", filepath.Join(home, "go", "bin", "burro"), "go install github.com/timhartmann7/burro/cmd/burro@latest"},
		{"script", filepath.Join(home, ".local", "bin", "burro"), "curl -fsSL https://github.com/timhartmann7/burro/releases/latest/download/install.sh | sh"},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			url, calls := latest(t)
			if out, _ := installed(t, home, tt.exe, url, versionFlag); out != "burro 0.1.0 "+platform+" ("+tt.method+")\n" {
				t.Fatalf("--version = %q", out)
			}
			if err := os.RemoveAll(filepath.Join(home, ".burro")); err != nil {
				t.Fatal(err)
			}
			out, notice := installed(t, home, tt.exe, url, argHost, "ls", argMode, humanMode)
			want := "burro 9.9.9 is out, you have 0.1.0. Update: " + tt.update + "\n"
			if out != "no hosts\n" || notice != want || *calls != 1 {
				t.Fatalf("stdout %q, notice %q after %d calls, want notice %q", out, notice, *calls, want)
			}
			if _, notice := installed(t, home, tt.exe, url, argHost, "ls", argMode, humanMode); notice != "" || *calls != 1 {
				t.Fatalf("second command the same day: notice %q after %d calls", notice, *calls)
			}
		})
	}
}

func TestMethodWithoutExecutable(t *testing.T) {
	a := New(nil, io.Discard, io.Discard, nil, "0.1.0")
	a.executable = func() (string, error) { return "", errors.New("no /proc") }
	if m := a.method(); m != "script" {
		t.Fatalf("method without an executable path = %s", m)
	}
}

// acceptance: S4-10
func TestNoCheckForAgentsOrWhenOff(t *testing.T) {
	url, calls := latest(t)
	for _, env := range [][]string{
		{burroModeEnv + agentMode},
		{claudeEnv},
		{burroModeEnv + humanMode, "CI=true"},
		{burroModeEnv + humanMode, "DO_NOT_TRACK=1"},
		{burroModeEnv + humanMode, noCheckEnv},
		// Without HOME there is no state file to keep the daily limit.
		{burroModeEnv + humanMode, "HOME="},
	} {
		home := t.TempDir()
		var stderr strings.Builder
		a := New(nil, io.Discard, &stderr, append([]string{"HOME=" + home}, env...), "0.1.0")
		a.latestAPI = url
		err := a.Run([]string{argHost, "ls"})
		if *calls != 0 || stderr.Len() != 0 || err != nil && !slices.Contains(env, "HOME=") {
			t.Fatalf("env %q: %d calls, stderr %q", env, *calls, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(home, ".burro", "state.json")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("env %q: state.json written: %v", env, err)
		}
	}
}

// acceptance: S4-15
func TestInitHint(t *testing.T) {
	hint := `next: open your agent here and say "fill in burro.toml and run the tests with burro"` + "\n" +
		"      or edit it yourself: burro help config\n"
	for _, tt := range []struct {
		env  []string
		args []string
		want string
	}{
		{[]string{burroModeEnv + humanMode, noCheckEnv}, nil, hint},
		{[]string{burroModeEnv + humanMode, noCheckEnv}, []string{argJSON}, hint},
		{[]string{burroModeEnv + agentMode}, nil, ""},
		{[]string{claudeEnv}, nil, ""},
		{[]string{burroModeEnv + humanMode, noCheckEnv}, []string{"--print"}, ""},
	} {
		repo := testutil.GitRepo(t, nil)
		t.Chdir(repo)
		var stdout, stderr strings.Builder
		env := append([]string{"HOME=" + withHosts(t, "vps1"), "PATH=" + os.Getenv("PATH")}, tt.env...)
		if err := New(nil, &stdout, &stderr, env, "").Run(append([]string{"init"}, tt.args...)); err != nil {
			t.Fatal(err)
		}
		if stderr.String() != tt.want {
			t.Fatalf("env %q args %q: stderr %q", tt.env, tt.args, stderr.String())
		}
	}
}
