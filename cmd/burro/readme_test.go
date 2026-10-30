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
	if !strings.HasPrefix(text, "<picture>\n") {
		t.Errorf("README doesn't open with the hero banner:\n%s", text[:min(len(text), 300)])
	}
	if alt := imgAlt.FindStringSubmatch(text); len(alt) < 2 || !strings.Contains(alt[1], "Your agent reads 30 lines, not 30 MB.") {
		t.Error("the hero banner's alt text lacks the slogan")
	}
	// The first screen in order: hero, description, comparison, install,
	// quick start. The measured number slots in next to the comparison,
	// with nothing to move. Then the reference sections.
	const description = "Run your coding agent's tests on your own server.<br>\n  One call to start, one short summary back. Works with Claude Code, Codex and Cursor."
	last := 0
	for _, s := range []string{
		description, "compare-light.svg", "\n## Install\n", "```sh\n" + installCommand + "\n```",
		"\n## Quick start\n", "burro host add", "`burro init`", "`burro run`", "`burro wait`",
		"\n## Config\n", "\n## Exit codes\n", "\n## Other ways to install\n",
		"\n## Docker isolation\n", "\n## Secrets\n", "\n## Privacy\n",
		"The only official repository is <code>github.com/timhartmann7/burro</code>.",
		`Apache 2.0, see <a href="LICENSE">LICENSE</a>.`,
	} {
		i := strings.Index(text[last:], s)
		if i < 0 {
			t.Errorf("README lacks %q or has it out of order", s)
			continue
		}
		last += i + len(s)
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

	for _, want := range []string{
		"brew install timhartmann7/tap/burro", "Homebrew 6", "go install github.com/timhartmann7/burro/cmd/burro@latest",
		"mise ", "aqua", "ubi ", "--label burro.run=$BURRO_RUN", "4 bytes", "DO_NOT_TRACK=1", "BURRO_NO_UPDATE_CHECK=1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("README lacks %q", want)
		}
	}
}

var (
	imgTag = regexp.MustCompile(`<img [^>]*>`)
	imgAlt = regexp.MustCompile(`<img alt="([^"]*)"`)
	imgSrc = regexp.MustCompile(`\bsrc(?:set)?="([^"]*)"`)
)

// Every image says in words what it shows and comes from the repo or
// shields.io, so the page reads the same without it.
func TestReadmeImages(t *testing.T) {
	text := readme(t)
	for _, tag := range imgTag.FindAllString(text, -1) {
		if alt := imgAlt.FindStringSubmatch(tag); len(alt) < 2 || strings.TrimSpace(alt[1]) == "" {
			t.Errorf("image without alt text: %s", tag)
		}
	}
	for _, src := range imgSrc.FindAllStringSubmatch(text, -1) {
		switch {
		case strings.HasPrefix(src[1], "https://img.shields.io/"):
		case strings.HasPrefix(src[1], ".github/readme/"):
			if _, err := os.Stat(filepath.Join("..", "..", src[1])); err != nil {
				t.Errorf("image %s: %v", src[1], err)
			}
		default:
			t.Errorf("image %q is neither in .github/readme/ nor on shields.io", src[1])
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
	for name, page := range readmePages(t, text) {
		for _, placeholder := range []*regexp.Regexp{
			regexp.MustCompile(`(?i)\b(todo|tbd|placeholder|lorem|coming soon|xx+)\b`),
			regexp.MustCompile(`\]\(\s*\)`),
			regexp.MustCompile(`\$?\[[A-Z]\]`),
			regexp.MustCompile(`src=""`),
		} {
			if m := placeholder.FindString(page); m != "" {
				t.Errorf("%s has a placeholder: %q", name, m)
			}
		}
		// A claim about saved tokens, calls or speed needs a measurement.
		claim := regexp.MustCompile(`(?i)\d[\d.,]*\s*(%|x\b|×|times|tokens|calls per|characters|faster|less output)`)
		if m := claim.FindString(page); m != "" {
			t.Errorf("%s states a number no run measured: %q", name, m)
		}
	}
	if strings.Contains(text, "| rr") || strings.Contains(text, " rr ") {
		t.Error("README compares with rr before the human checked it")
	}
}

var sizeAttr = regexp.MustCompile(` (?:width|height)="[\d.]+%?"`)

// readmePages holds the words the README and each of its banners show.
func readmePages(t *testing.T, text string) map[string]string {
	t.Helper()
	banners, err := filepath.Glob(filepath.Join("..", "..", ".github", "readme", "*.svg"))
	if err != nil || len(banners) == 0 {
		t.Fatalf("no banners in .github/readme: %v", err)
	}
	pages := map[string]string{"README": sizeAttr.ReplaceAllString(text, "")}
	for _, b := range banners {
		data, err := os.ReadFile(b)
		if err != nil {
			t.Fatal(err)
		}
		pages[filepath.Base(b)] = svgWords(string(data))
	}
	return pages
}

// svgWords keeps the text a banner shows and drops markup and fonts.
func svgWords(svg string) string {
	svg = regexp.MustCompile(`(?s)<style>.*?</style>`).ReplaceAllString(svg, "")
	return regexp.MustCompile(`<[^>]*>`).ReplaceAllString(svg, " ")
}
