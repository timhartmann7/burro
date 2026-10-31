package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/config"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

// minimal is the pipeline part every valid burro.toml needs.
const minimal = "\n[pipelines]\ndefault = [\"test\"]\n\n[steps.test]\nrun = \"make test\"\n"

const (
	fieldTimeout = "steps.test.timeout"
	fieldDrop    = "filters.drop"
	fieldRun     = "steps.test.run"
	fieldSkip    = "steps.test.skip_if_unchanged"
	fieldJUnit   = "steps.test.junit"
	npmToken     = "npm-token\n"
	stepTest     = "test"
	verify       = "verify"
	install      = "install"
	unit         = "unit"
	e2e          = "e2e"
	vps1         = "vps1"
	fieldHost    = "host"
	fieldPath    = "path"
	echoOK       = "echo ok\n"
	echoTop      = "echo top\n"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		toml string
		host string
		dir  string
	}{
		{name: "default path", toml: `host = "vps1"`, host: vps1, dir: "burro/myapp"},
		{name: "home path", toml: "host = \"vps1\"\npath = \"~/work/app\"", host: vps1, dir: "work/app"},
		{name: "absolute path", toml: "host = \"vps1\"\npath = \"/srv/app\"", host: vps1, dir: "/srv/app"},
		{name: "trailing slash", toml: "host = \"vps1\"\npath = \"/srv/app/\"", host: vps1, dir: "/srv/app"},
		{name: "spaces in path", toml: "host = \"a_b-1\"\npath = \"~/my projects/app\"", host: "a_b-1", dir: "my projects/app"},
		{name: "comments", toml: "# server\nhost = \"vps1\" # trailing", host: vps1, dir: "burro/myapp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := config.Parse([]byte(tt.toml+minimal), "myapp")
			if err != nil {
				t.Fatal(err)
			}
			if c.Host() != tt.host || c.Dir() != tt.dir {
				t.Fatalf("got host %q dir %q, want %q %q", c.Host(), c.Dir(), tt.host, tt.dir)
			}
		})
	}
}

// acceptance: S1-18
func TestParseRejects(t *testing.T) {
	tests := []struct {
		name  string
		toml  string
		field string
	}{
		{name: "unknown key", toml: "host = \"vps1\"\nhots = \"vps2\"", field: `"hots"`},
		{name: "unknown table", toml: "host = \"vps1\"\n[stepz.unit]\nrun = \"x\"", field: `"stepz"`},
		{name: "missing host", toml: `path = "~/x"`, field: fieldHost},
		{name: "empty host", toml: `host = ""`, field: fieldHost},
		{name: "upper case host", toml: `host = "VPS1"`, field: fieldHost},
		{name: "host with space", toml: `host = "vps 1"`, field: fieldHost},
		{name: "host with newline", toml: `host = "vps1\nHost *"`, field: fieldHost},
		{name: "host starting with dash", toml: `host = "-oProxyCommand=x"`, field: fieldHost},
		{name: "host too long", toml: `host = "` + strings.Repeat("a", 64) + `"`, field: fieldHost},
		{name: "host not a string", toml: `host = 5`, field: fieldHost},
		{name: "path not a string", toml: "host = \"vps1\"\npath = [\"/a\"]", field: fieldPath},
		{name: "relative path", toml: "host = \"vps1\"\npath = \"burro/app\"", field: fieldPath},
		{name: "dot dot", toml: "host = \"vps1\"\npath = \"~/burro/../../etc\"", field: fieldPath},
		{name: "dot", toml: "host = \"vps1\"\npath = \"/srv/./app\"", field: fieldPath},
		{name: "double slash", toml: "host = \"vps1\"\npath = \"/srv//app\"", field: fieldPath},
		{name: "bare home", toml: "host = \"vps1\"\npath = \"~/\"", field: fieldPath},
		{name: "root", toml: "host = \"vps1\"\npath = \"/\"", field: fieldPath},
		{name: "other user's home", toml: "host = \"vps1\"\npath = \"~root/app\"", field: fieldPath},
		{name: "newline in path", toml: "host = \"vps1\"\npath = \"/srv/a\\nb\"", field: fieldPath},
		{name: "syntax", toml: "host = \"vps1", field: "line 1"},
		{name: "duplicate key", toml: "host = \"a\"\nhost = \"b\"", field: "line 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Parse([]byte(tt.toml), "myapp")
			msg, fix := usageError(t, err)
			if !strings.Contains(msg+" "+fix, tt.field) {
				t.Fatalf("error %q / fix %q doesn't name %s", msg, fix, tt.field)
			}
		})
	}
}

func usageError(t *testing.T, err error) (msg, fix string) {
	t.Helper()
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != output.ExitUsage || coded.Fix() == "" {
		t.Fatalf("got %v, want exit 4 with a fix line", err)
	}
	return err.Error(), coded.Fix()
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	_, err := config.Load(root)
	if msg, _ := usageError(t, err); !strings.Contains(msg, "no burro.toml") {
		t.Fatalf("missing file: %q", msg)
	}
	if err := os.WriteFile(filepath.Join(root, config.File), []byte("host = \"vps1\"\n"+minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(root)
	if err != nil || c.Dir() != "burro/"+filepath.Base(root) {
		t.Fatalf("Load = %v, %v", c, err)
	}
	if err := os.Chmod(filepath.Join(root, config.File), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(root); os.Geteuid() != 0 {
		usageError(t, err)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "vps1", "0", "a-b_c", strings.Repeat("z", 63)} {
		if err := config.ValidName(fieldHost, ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "_a", "A", "a.b", "a b", "a/b", strings.Repeat("z", 64), "é", "a\n"} {
		if err := config.ValidName("step", bad); err == nil {
			t.Errorf("%q accepted", bad)
		} else {
			usageError(t, err)
		}
	}
}

func TestReadInRoot(t *testing.T) {
	root := testutil.GitRepo(t, map[string]string{"scripts/check.sh": echoOK, "top.sh": echoTop})
	outside := filepath.Join(t.TempDir(), "outside.sh")
	if err := os.WriteFile(outside, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.sh")); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "scripts")
	tests := []struct {
		name, dir, file, want string
	}{
		{name: "relative", dir: root, file: "scripts/check.sh", want: echoOK},
		{name: "from a subfolder", dir: sub, file: "check.sh", want: echoOK},
		{name: "up and back in", dir: sub, file: "../top.sh", want: echoTop},
		{name: "absolute inside", dir: "/", file: filepath.Join(root, "top.sh"), want: echoTop},
	}
	for _, tt := range tests {
		data, err := config.ReadInRoot(root, tt.dir, tt.file)
		if err != nil || string(data) != tt.want {
			t.Errorf("%s: got %q, %v", tt.name, data, err)
		}
	}
	for name, file := range map[string]string{
		"dot dot":          "../" + filepath.Base(root) + "-other/x",
		"absolute outside": outside,
		"symlink out":      "escape.sh",
		"missing":          "nope.sh",
		"folder":           "scripts",
	} {
		_, err := config.ReadInRoot(root, root, file)
		if msg, _ := usageError(t, err); strings.Contains(msg, "TOPSECRET") {
			t.Errorf("%s: error leaks content: %q", name, msg)
		}
	}
	if _, err := config.ReadInRoot(filepath.Join(root, "missing"), root, "top.sh"); err == nil {
		t.Error("a missing root passed")
	}
}

// full is a burro.toml with every field S2 knows.
const full = `host = "vps1"

[pipelines]
default = ["install", "unit"]
verify  = ["install", "unit", "e2e", "lighthouse"]

[steps.install]
run = "pnpm install --frozen-lockfile"
env = { CI = "true", CACHE = "$BURRO_RUN/x" }
skip_if_unchanged = ["pnpm-lock.yaml", "sub/dir/file"]

[steps.unit]
run = "pnpm vitest run"
continue_on_fail = true
timeout = "20m"

[steps.e2e]
run = "pnpm playwright test"
timeout = "24h"

[steps.lighthouse]
run = "pnpm lhci autorun"
timeout = "1s"

[steps.unused]
run = "true"

[filters]
drop = ['^\[pg-boss\]', '^\s*$']
`

func TestParseRun(t *testing.T) {
	c, err := config.Parse([]byte(full), "app")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Pipelines(); strings.Join(got, " ") != "default verify" {
		t.Fatalf("pipelines %q", got)
	}
	inst, ok := c.Step(install)
	if !ok || inst.Run != "pnpm install --frozen-lockfile" || inst.Timeout != time.Hour || inst.ContinueOnFail ||
		inst.Env["CI"] != "true" || inst.Env["CACHE"] != "$BURRO_RUN/x" || strings.Join(inst.SkipIfUnchanged, " ") != "pnpm-lock.yaml sub/dir/file" {
		t.Fatalf("install = %+v", inst)
	}
	if u, _ := c.Step(unit); !u.ContinueOnFail || u.Timeout != 20*time.Minute {
		t.Fatalf("unit = %+v", u)
	}
	if st, _ := c.Step(e2e); st.Timeout != 24*time.Hour {
		t.Fatalf("e2e = %+v", st)
	}
	if _, ok := c.Step("nope"); ok {
		t.Fatal("unknown step found")
	}
	drop := c.Drop()
	if len(drop) != 2 || !drop[0].MatchString("[pg-boss] tick") || !drop[1].MatchString("  ") || drop[1].MatchString("x") {
		t.Fatalf("drop = %v", drop)
	}
	drop[0] = nil
	if c.Drop()[0] == nil {
		t.Fatal("Drop hands out the config's own slice")
	}
}

// acceptance: S2-16
func TestParseRunRejects(t *testing.T) {
	const head = "host = \"vps1\"\n"
	tests := []struct {
		name  string
		toml  string
		field string
	}{
		{name: "no pipelines", toml: head + "[steps.test]\nrun = \"x\"", field: "pipelines"},
		{name: "empty pipelines", toml: head + "[pipelines]\n[steps.test]\nrun = \"x\"", field: "pipelines"},
		{name: "no steps", toml: head + "[pipelines]\ndefault = []", field: "steps"},
		{name: "unknown step in a pipeline", toml: head + "[pipelines]\ndefault = [\"test\", \"e2e\"]\n[steps.test]\nrun = \"x\"", field: "pipelines.default"},
		{name: "odd unknown step", toml: head + "[pipelines]\ndefault = [\"a b\"]\n[steps.test]\nrun = \"x\"", field: `[steps."a b"]`},
		{name: "duplicate step in a pipeline", toml: head + "[pipelines]\nverify = [\"test\", \"test\"]\n[steps.test]\nrun = \"x\"", field: "pipelines.verify"},
		{name: "bad timeout", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\ntimeout = \"20 minutes\"", field: fieldTimeout},
		{name: "timeout too short", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\ntimeout = \"999ms\"", field: fieldTimeout},
		{name: "timeout too long", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\ntimeout = \"25h\"", field: fieldTimeout},
		{name: "negative timeout", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\ntimeout = \"-5m\"", field: fieldTimeout},
		{name: "number timeout", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\ntimeout = 20", field: fieldTimeout},
		{name: "invalid drop regex", toml: head + minimal + "[filters]\ndrop = ['(unclosed']", field: fieldDrop},
		{name: "drop not a list", toml: head + minimal + "[filters]\ndrop = 'x'", field: fieldDrop},
		{name: "drop with a number", toml: head + minimal + "[filters]\ndrop = ['x', 1]", field: fieldDrop},
		{name: "unknown filter", toml: head + minimal + "[filters]\nkeep = ['x']", field: "filters.keep"},
		{name: "filters not a table", toml: head + "filters = 1\n" + minimal, field: "filters"},
		{name: "typo in a step", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\ncontinue_on_fial = true", field: "steps.test.continue_on_fial"},
		{name: "empty run", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"\"", field: fieldRun},
		{name: "blank run", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"  \"", field: fieldRun},
		{name: "missing run", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\ntimeout = \"1m\"", field: fieldRun},
		{name: "run not a string", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = [\"make\"]", field: fieldRun},
		{name: "step not a table", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps]\ntest = \"make\"", field: "steps.test"},
		{name: "bad step name", toml: head + "[pipelines]\ndefault = [\"Test\"]\n[steps.Test]\nrun = \"x\"", field: `"Test"`},
		{name: "bad pipeline name", toml: head + "[pipelines]\nVerify = [\"test\"]\n[steps.test]\nrun = \"x\"", field: `"Verify"`},
		{name: "pipeline not a list", toml: head + "[pipelines]\ndefault = \"test\"\n[steps.test]\nrun = \"x\"", field: "pipelines.default"},
		{name: "continue_on_fail not a bool", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\ncontinue_on_fail = \"yes\"", field: "steps.test.continue_on_fail"},
		{name: "skip path outside", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\nskip_if_unchanged = [\"../lock\"]", field: fieldSkip},
		{name: "skip path absolute", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\nskip_if_unchanged = [\"/etc/passwd\"]", field: fieldSkip},
		{name: "skip path empty", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\nskip_if_unchanged = [\"\"]", field: fieldSkip},
		{name: "env lower case", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\nenv = { ci = \"1\" }", field: "steps.test.env.ci"},
		{name: "env BURRO_", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\nenv = { BURRO_STEP = \"1\" }", field: "steps.test.env.BURRO_STEP"},
		{name: "env not a string", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\nenv = { CI = true }", field: "steps.test.env.CI"},
		{name: "env not a table", toml: head + "[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\nenv = \"CI=1\"", field: "steps.test.env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Parse([]byte(tt.toml), "myapp")
			msg, fix := usageError(t, err)
			if !strings.Contains(msg+" "+fix, tt.field) {
				t.Fatalf("error %q / fix %q doesn't name %s", msg, fix, tt.field)
			}
		})
	}
}

// describe lists the selected steps, with the reason in brackets for those
// that stay out.
func describe(sel []config.Selected) string {
	parts := make([]string, 0, len(sel))
	for i := range sel {
		part := sel[i].Step.Name
		if sel[i].Skip != "" {
			part += "(" + sel[i].Skip + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

// acceptance: S2-05
func TestSelect(t *testing.T) {
	c, err := config.Parse([]byte(full), "app")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		pipeline string
		from     string
		only     []string
		want     string
	}{
		{name: "all", pipeline: verify, want: "install unit e2e lighthouse"},
		{name: "from", pipeline: verify, from: e2e, want: "install(--from e2e) unit(--from e2e) e2e lighthouse"},
		{name: "from the first", pipeline: verify, from: install, want: "install unit e2e lighthouse"},
		{name: "only keeps pipeline order", pipeline: verify, only: []string{"lighthouse", unit}, want: "install(--only lighthouse,unit) unit e2e(--only lighthouse,unit) lighthouse"},
		{name: "from and only", pipeline: verify, from: unit, only: []string{install, e2e}, want: "install(--from unit) unit(--only install,e2e) e2e lighthouse(--only install,e2e)"},
		{name: "default", pipeline: config.DefaultPipeline, only: []string{unit, unit}, want: "install(--only unit,unit) unit"},
	}
	for _, tt := range tests {
		sel, err := c.Select(tt.pipeline, tt.from, tt.only, nil)
		if err != nil || describe(sel) != tt.want {
			t.Errorf("%s: got %q, %v; want %q", tt.name, describe(sel), err, tt.want)
		}
	}
}

// acceptance: S2-05
func TestSelectRejects(t *testing.T) {
	c, err := config.Parse([]byte(full), "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		pipeline, from string
		only           []string
		names          string
	}{
		{pipeline: "nope", names: "default, verify"},
		{pipeline: config.DefaultPipeline, from: e2e, names: "install, unit"},
		{pipeline: verify, only: []string{unit, "nope"}, names: "install, unit, e2e, lighthouse"},
		{pipeline: verify, only: []string{""}, names: install},
		{pipeline: verify, from: "unused", names: install},
	} {
		_, err := c.Select(bad.pipeline, bad.from, bad.only, nil)
		if _, fix := usageError(t, err); !strings.Contains(fix, bad.names) {
			t.Errorf("Select(%q, %q, %q): fix %q", bad.pipeline, bad.from, bad.only, fix)
		}
	}
}

func TestSelectArgs(t *testing.T) {
	c, err := config.Parse([]byte(full), "app")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"tests/login.spec.ts"}
	sel, err := c.Select(verify, "", []string{e2e}, args)
	if err != nil || describe(sel) != "install(--only e2e) unit(--only e2e) e2e lighthouse(--only e2e)" {
		t.Fatalf("one step with arguments: %q, %v", describe(sel), err)
	}
	if sel, err = c.Select(verify, "lighthouse", nil, args); err != nil || len(sel) != 4 {
		t.Fatalf("--from the last step: %q, %v", describe(sel), err)
	}
	for _, bad := range []struct {
		from string
		only []string
		msg  string
	}{
		{msg: "arguments after -- go to one step, and pipeline verify runs 4: install, unit, e2e, lighthouse"},
		{only: []string{unit, e2e}, msg: "arguments after -- go to one step, and pipeline verify runs 2: unit, e2e"},
		{from: e2e, msg: "arguments after -- go to one step, and pipeline verify runs 2: e2e, lighthouse"},
	} {
		_, err := c.Select(verify, bad.from, bad.only, args)
		msg, fix := usageError(t, err)
		if msg != bad.msg || !strings.Contains(fix, "--only") || !strings.HasSuffix(fix, "-- tests/login.spec.ts") {
			t.Errorf("from %q only %q: %q / %q", bad.from, bad.only, msg, fix)
		}
	}
}

func TestValidRun(t *testing.T) {
	for _, ok := range []string{"r1", "r42", "r999999999"} {
		if err := config.ValidRun(ok); err != nil {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "r", "r0", "r01", "42", "R1", "r1/../x", "r1 ", "r1234567890", "-r1"} {
		if err := config.ValidRun(bad); err == nil {
			t.Errorf("%q accepted", bad)
		} else {
			usageError(t, err)
		}
	}
}

func FuzzRunConfig(f *testing.F) {
	for _, seed := range testutil.FuzzSeeds(f, "FuzzRunConfig") {
		f.Add(seed, "e2e")
	}
	f.Fuzz(func(t *testing.T, data []byte, from string) {
		c, err := config.Parse(data, "app")
		if err != nil {
			usageError(t, err)
			return
		}
		for _, name := range c.Pipelines() {
			checkSelection(t, c, name, from)
		}
	})
}

// checkSelection selects pipeline from from and fails on a rejection
// without exit 4 or on a step that validation should have refused.
func checkSelection(t *testing.T, c *config.Config, pipeline, from string) {
	t.Helper()
	sel, err := c.Select(pipeline, from, nil, nil)
	if err != nil {
		usageError(t, err)
		return
	}
	for i := range sel {
		if st := &sel[i].Step; st.Run == "" || st.Timeout < time.Second || st.Timeout > 24*time.Hour {
			t.Fatalf("accepted an invalid step %+v", st)
		}
	}
}

func FuzzConfigParse(f *testing.F) {
	for _, seed := range testutil.FuzzSeeds(f, "FuzzConfigParse") {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := config.Parse(data, "app")
		if err == nil {
			if config.ValidName(fieldHost, c.Host()) != nil || c.Dir() == "" {
				t.Fatalf("accepted an invalid config: %q", data)
			}
			return
		}
		usageError(t, err)
	})
}

// step is a pipeline with one step test whose extra fields are fields.
func step(fields string) string {
	return "host = \"vps1\"\n[pipelines]\ndefault = [\"test\"]\n[steps.test]\nrun = \"x\"\n" + fields
}

func TestParseReportsAndSecrets(t *testing.T) {
	c, err := config.Parse([]byte(step("junit = \"./target/surefire-reports/*.xml\"\nsecrets = [\"npmrc\", \"token\"]\n"+
		"[secrets]\nnpmrc = \"~/.npmrc\"\ntoken = \"/etc/token\"\nunused = \"keys/a\"\n")), "app")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := c.Step(stepTest)
	if st.JUnit != "target/surefire-reports/*.xml" || strings.Join(st.Secrets, " ") != "npmrc token" {
		t.Fatalf("step %+v", st)
	}
	if st, _ := mustParse(t, minimal).Step(stepTest); st.JUnit != "" || st.Secrets != nil {
		t.Fatalf("defaults %+v", st)
	}
}

func mustParse(t *testing.T, toml string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("host = \"vps1\"\n"+toml), "app")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseReportsAndSecretsRejects(t *testing.T) {
	tests := []struct {
		name  string
		toml  string
		field string
	}{
		{name: "junit outside", toml: step(`junit = "../out.xml"`), field: fieldJUnit},
		{name: "junit absolute", toml: step(`junit = "/tmp/out.xml"`), field: fieldJUnit},
		{name: "junit bad glob", toml: step(`junit = "out/[.xml"`), field: fieldJUnit},
		{name: "junit not a string", toml: step(`junit = ["a.xml"]`), field: fieldJUnit},
		{name: "unknown secret", toml: step(`secrets = ["npmrc"]`), field: "steps.test.secrets"},
		{name: "secrets not a list", toml: step(`secrets = "npmrc"`), field: "steps.test.secrets"},
		{name: "bad secret name", toml: step("[secrets]\n\"../x\" = \"a\""), field: `"../x"`},
		{name: "secret not a string", toml: step("[secrets]\nnpmrc = 1"), field: "secrets.npmrc"},
		{name: "empty secret file", toml: step("[secrets]\nnpmrc = \"\""), field: "secrets.npmrc"},
		{name: "secrets not a table", toml: "secrets = 1\n" + step(""), field: "secrets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Parse([]byte(tt.toml), "myapp")
			msg, fix := usageError(t, err)
			if !strings.Contains(msg+" "+fix, tt.field) {
				t.Fatalf("error %q / fix %q doesn't name %s", msg, fix, tt.field)
			}
		})
	}
}

// acceptance: S3-15
func TestSecretsRead(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	for path, data := range map[string]string{filepath.Join(home, ".npmrc"): npmToken, filepath.Join(root, "keys", "a"): "key a"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	abs := filepath.Join(home, ".npmrc")
	c := mustParse(t, "[pipelines]\ndefault = [\"a\", \"b\", \"c\"]\n"+
		"[steps.a]\nrun = \"x\"\nsecrets = [\"npmrc\", \"key\"]\n[steps.b]\nrun = \"x\"\nsecrets = [\"npmrc\", \"abs\"]\n"+
		"[steps.c]\nrun = \"x\"\nsecrets = [\"gone\"]\n"+
		"[secrets]\nnpmrc = \"~/.npmrc\"\nkey = \"keys/a\"\nabs = \""+abs+"\"\ngone = \"~/missing\"\n")
	sel, err := c.Select(config.DefaultPipeline, "", []string{"a", "b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Secrets(sel, root, home)
	if err != nil || len(got) != 3 || string(got["npmrc"]) != npmToken || string(got["key"]) != "key a" || string(got["abs"]) != npmToken {
		t.Fatalf("secrets %q, %v", got, err)
	}
	// A step that runs needs its secret; one that doesn't run needs nothing.
	sel, err = c.Select(config.DefaultPipeline, "c", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Secrets(sel, root, home)
	if msg, fix := usageError(t, err); msg != "secret gone: ~/missing does not exist" || !strings.Contains(fix, "secrets.gone") {
		t.Fatalf("missing: %q / %q", msg, fix)
	}
	if os.Geteuid() == 0 {
		return
	}
	if err := os.Chmod(abs, 0o000); err != nil {
		t.Fatal(err)
	}
	sel, _ = c.Select(config.DefaultPipeline, "b", []string{"b"}, nil)
	if _, err := c.Secrets(sel, root, home); err == nil {
		t.Fatal("unreadable secret passed")
	} else {
		usageError(t, err)
	}
}
