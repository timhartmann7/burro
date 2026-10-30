package main_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const installCommand = "curl -fsSL https://github.com/timhartmann7/burro/releases/latest/download/install.sh | sh"

func readme(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// acceptance: S4-16
func TestReadme(t *testing.T) {
	text := readme(t)
	lines := strings.Split(text, "\n")
	if lines[0] != "# Your agent reads 30 lines, not 30 MB." {
		t.Errorf("first line %q is not the slogan", lines[0])
	}
	// The first screen: slogan, description, the one install command, then
	// the rules block. The GIF and the measured number go between the
	// install command and the rules, with nothing to move.
	const description = "**burro** runs your coding agent's tests on your own server. One call to start, one short summary back. Works with Claude Code, Codex and Cursor."
	firstScreen := "# Your agent reads 30 lines, not 30 MB.\n\n" + description + "\n\n```sh\n" + installCommand + "\n```\n\n## Your agent drives it\n\n`burro init`"
	if !strings.HasPrefix(text, firstScreen) {
		t.Errorf("README doesn't start with slogan, description, install command and burro init:\n%s", text[:min(len(text), 600)])
	}
	if n := strings.Count(text, "install.sh"); n != 2 || strings.Count(text, installCommand) != 1 {
		t.Errorf("README names install.sh %d times; want the one install command and the privacy line", n)
	}

	rules, err := os.ReadFile(filepath.Join("..", "..", "internal", "initcmd", "rules.md"))
	if err != nil {
		t.Fatal(err)
	}
	from, to := strings.Index(text, "<!-- burro:begin -->"), strings.Index(text, "<!-- burro:end -->")
	if from < 0 || to < 0 || text[from:to]+"<!-- burro:end -->\n" != string(rules) {
		t.Error("the rules block in README differs from internal/initcmd/rules.md inside the markers")
	}

	sections := []string{
		"## Your agent drives it", "## Quick start", "## Config", "## Exit codes", "## Other install methods",
		"## Docker isolation", "## Secrets", "## Privacy", "## License",
	}
	last := -1
	for _, s := range sections {
		i := strings.Index(text, "\n"+s+"\n")
		if i <= last {
			t.Errorf("section %q is missing or out of order", s)
		}
		last = i
	}
	for _, want := range []string{
		"brew install timhartmann7/tap/burro", "Homebrew 6", "go install github.com/timhartmann7/burro/cmd/burro@latest",
		"mise ", "aqua", "ubi ", "--label burro.run=$BURRO_RUN", "4 bytes", "DO_NOT_TRACK=1", "BURRO_NO_UPDATE_CHECK=1",
		"The only official repository is `github.com/timhartmann7/burro`.", "Apache 2.0, see [LICENSE](LICENSE).",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("README lacks %q", want)
		}
	}
}

// Numbers in the README come from real runs, which the human adds.
func TestReadmeInventsNothing(t *testing.T) {
	text := readme(t)
	for _, banned := range []string{"revolutionary", "game-changer", "game changer", "supercharge", "seamless", "successfully", "!["} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(banned)) {
			t.Errorf("README has %q", banned)
		}
	}
	for _, placeholder := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(todo|tbd|placeholder|lorem|coming soon|xx+)\b`),
		regexp.MustCompile(`(?i)<img`),
		regexp.MustCompile(`\]\(\s*\)`),
	} {
		if m := placeholder.FindString(text); m != "" {
			t.Errorf("README has a placeholder: %q", m)
		}
	}
	// A claim about saved tokens, calls or speed needs a measurement.
	claim := regexp.MustCompile(`(?i)\d[\d.,]*\s*(%|x\b|×|times|tokens|calls per|characters|faster|less output)`)
	if m := claim.FindString(text); m != "" {
		t.Errorf("README states a number no run measured: %q", m)
	}
	if strings.Contains(text, "| rr") || strings.Contains(text, " rr ") {
		t.Error("README compares with rr before the human checked it")
	}
}
