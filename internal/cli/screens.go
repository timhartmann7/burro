package cli

import (
	"strings"

	"github.com/timhartmann7/burro/internal/mode"
)

// welcome is what a human sees after install, and from burro with no
// arguments until the first host exists. Titles are bold and commands blue
// when the terminal takes color.
func (a *App) welcome(m *mode.Mode) string {
	t, c := m.Title, m.Command
	return strings.Join([]string{
		"burro " + a.version,
		"",
		"burro runs your coding agent's tests on your own server.",
		"The agent starts a run with one call and gets back a 30-line summary",
		"instead of megabytes of logs. Works with Claude Code, Codex and Cursor.",
		"",
		t("Get started"),
		"",
		"  1. Add your server (once)",
		"     " + c("burro host add myserver root@203.0.113.10 -i ~/.ssh/id_ed25519"),
		"     Server already in ~/.ssh/config? " + c("burro host add myserver <its alias>"),
		"",
		"  2. Set up a project (once per project)",
		"     " + c("cd ~/code/your-project"),
		"     " + c("burro init"),
		"",
		"  3. Hand it to your agent",
		"     Open Claude Code, Codex or Cursor in the project and say:",
		`     "Fill in burro.toml and run the tests with burro"`,
		"",
		t("Your agent drives burro"),
		"  " + c("burro init") + " adds a short rules block to CLAUDE.md or AGENTS.md.",
		"  From then on your agent runs tests through burro on its own.",
		"  Rather paste the block yourself? " + c("burro init --print"),
		"",
		t("Help anytime"),
		"  " + c("burro --help") + "         every command on one screen",
		"  " + c("burro help config") + "    what goes into burro.toml",
		"  https://github.com/timhartmann7/burro",
		"",
	}, "\n")
}

// configHelp is burro help config: what an agent needs to write burro.toml
// and nothing more.
const configHelp = `burro help config · what goes into burro.toml

burro.toml sits at the repository root. A minimal one that works:

  host = "vps1"

  [pipelines]
  default = ["unit"]

  [steps.unit]
  run = "pytest --junitxml=.burro/out/py.xml"
  junit = ".burro/out/py.xml"

Top level
  host               a name from burro host ls, required
  path               folder on the server, default ~/burro/<repo name>
  [pipelines]        name = ["step", ...]; burro run <name>, default if none
  [secrets]          name = "~/.npmrc", a local file, on the server in tmpfs
  [filters] drop     RE2 patterns; matching lines leave the clean logs

Step fields
  run                command for bash -lc in the project folder, required
  env                step variables; $BURRO_* references expand
  timeout            kill the step after this, 1s to 24h, default 60m
  continue_on_fail   true: a failed step doesn't stop the pipeline
  skip_if_unchanged  files; skip when their sha256 match the last good run
  junit              path or glob of JUnit XML; without it burro failures
                     shows the log tail
  secrets            names from [secrets]; the files are in $BURRO_SECRETS

Names: lowercase letters, digits, - and _. Unknown keys are errors.
docker run in a step needs --label burro.run=$BURRO_RUN for burro clean.

JUnit per stack, reports under .burro/out/
  Python        pytest --junitxml=.burro/out/py.xml
  Go            gotestsum --junitfile .burro/out/go.xml ./...
  Rust          cargo nextest run --profile ci with a [profile.ci.junit]
                section in .config/nextest.toml
  PHP           phpunit --log-junit .burro/out/php.xml
  Java, Kotlin  junit = "target/surefire-reports/*.xml" for Maven,
                "build/test-results/test/*.xml" for Gradle
  Jest          jest-junit with JEST_JUNIT_OUTPUT_FILE=.burro/out/jest.xml
  Vitest        --reporter=junit --outputFile=.burro/out/unit.xml
  Playwright    --reporter=junit with
                PLAYWRIGHT_JUNIT_OUTPUT_NAME=.burro/out/e2e.xml
`
