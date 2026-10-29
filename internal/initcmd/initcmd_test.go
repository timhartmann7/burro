package initcmd_test

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/initcmd"
	"github.com/timhartmann7/burro/internal/output"
)

const (
	agents   = "AGENTS.md"
	claude   = "CLAUDE.md"
	toml     = "burro.toml"
	ignore   = ".gitignore"
	rulesFor = "rules for your agent"
	begin    = "<!-- burro:begin -->"
	end      = "<!-- burro:end -->"
)

// files writes files into a fresh folder and returns it.
func files(t *testing.T, contents map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range contents {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func run(t *testing.T, root, host, agentsFile string) []output.InitFile {
	t.Helper()
	got, err := initcmd.New(root, host).Run(agentsFile)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// lines prints the init result one "change name detail" per file.
func lines(files []output.InitFile) string {
	parts := make([]string, 0, len(files))
	for _, f := range files {
		parts = append(parts, strings.TrimSpace(f.Change+" "+f.Name+" "+f.Detail))
	}
	return strings.Join(parts, "\n")
}

// acceptance: S3-11
func TestPlacement(t *testing.T) {
	block := initcmd.Block()
	tests := []struct {
		name   string
		before map[string]string
		want   string
		// after holds each file's content; "" means it doesn't exist.
		after map[string]string
	}{
		{
			name:  "neither file",
			want:  "created burro.toml\ncreated AGENTS.md rules for your agent\ncreated CLAUDE.md @AGENTS.md\ncreated .gitignore .burro/",
			after: map[string]string{agents: block, claude: "@AGENTS.md\n", ignore: ".burro/\n"},
		},
		{
			name:   "only AGENTS.md",
			before: map[string]string{agents: "# Agents\n"},
			want:   "created burro.toml\nupdated AGENTS.md rules for your agent\ncreated .gitignore .burro/",
			after:  map[string]string{agents: "# Agents\n\n" + block, claude: ""},
		},
		{
			name:   "only CLAUDE.md",
			before: map[string]string{claude: "# Claude\n", ignore: "node_modules"},
			want:   "created burro.toml\nupdated CLAUDE.md rules for your agent\nupdated .gitignore .burro/",
			after:  map[string]string{claude: "# Claude\n\n" + block, agents: "", ignore: "node_modules\n.burro/\n"},
		},
		{
			name:   "both",
			before: map[string]string{agents: "# Agents", claude: "# Claude\n\n"},
			want:   "created burro.toml\nupdated AGENTS.md rules for your agent\nupdated CLAUDE.md rules for your agent\ncreated .gitignore .burro/",
			after:  map[string]string{agents: "# Agents\n\n" + block, claude: "# Claude\n\n" + block},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := files(t, tt.before)
			if got := lines(run(t, root, "", "")); got != tt.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tt.want)
			}
			for name, want := range tt.after {
				data, err := os.ReadFile(filepath.Join(root, name))
				if want == "" && !errors.Is(err, fs.ErrNotExist) || want != "" && string(data) != want {
					t.Errorf("%s: %q, %v; want %q", name, data, err, want)
				}
			}
		})
	}
}

// snapshot reads every file init may write.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, f := range []string{toml, agents, claude, ignore} {
		got[f] = read(t, root, f)
	}
	return got
}

// checkFirst checks what the first init wrote: an own burro.toml kept,
// one block per file that has one, one .burro/ line.
func checkFirst(t *testing.T, before, first map[string]string) {
	t.Helper()
	if before[toml] != "" && first[toml] != before[toml] {
		t.Errorf("burro.toml rewritten: %q", first[toml])
	}
	// Both files get the block when both were there; a new CLAUDE.md
	// imports AGENTS.md instead.
	blocks := 1
	if before[agents] != "" && before[claude] != "" {
		blocks = 2
	}
	if n := strings.Count(first[agents]+first[claude], begin); n != blocks {
		t.Errorf("%d blocks, want %d", n, blocks)
	}
	if n := strings.Count(first[ignore], ".burro/\n"); n != 1 {
		t.Errorf(".gitignore has %d .burro/ lines", n)
	}
}

// acceptance: S3-12
func TestSecondInit(t *testing.T) {
	const own = "host = \"mine\"\n# my own config\n"
	for name, before := range map[string]map[string]string{
		"new project":   nil,
		"own files":     {agents: "intro\n", claude: "claude intro\n", toml: own, ignore: "dist/\n"},
		"own toml only": {toml: own},
	} {
		t.Run(name, func(t *testing.T) {
			root := files(t, before)
			run(t, root, "vps1", "")
			first := snapshot(t, root)
			if got := lines(run(t, root, "vps1", "")); got != "unchanged burro.toml\nunchanged AGENTS.md\nunchanged CLAUDE.md\nunchanged .gitignore" {
				t.Errorf("second init:\n%s", got)
			}
			if again := snapshot(t, root); !maps.Equal(first, again) {
				t.Errorf("files changed:\n%q\n%q", first, again)
			}
			checkFirst(t, before, first)
		})
	}
}

// acceptance: S3-13
func TestOutdatedBlock(t *testing.T) {
	const before, after = "# Project\n\nSome rules.\n\n", "\n## Later\nmore text\n"
	old := begin + "\n## Remote runs\n\n- old rule\n" + end
	root := files(t, map[string]string{agents: before + old + after})
	if got := lines(run(t, root, "", ""))[len("created burro.toml\n"):]; !strings.HasPrefix(got, "updated AGENTS.md "+rulesFor) {
		t.Fatalf("got %q", got)
	}
	if got := read(t, root, agents); got != before+strings.TrimSuffix(initcmd.Block(), "\n")+after {
		t.Fatalf("AGENTS.md %q", got)
	}
}

func TestTemplate(t *testing.T) {
	root := files(t, nil)
	run(t, root, "vps1", "")
	want := "# burro.toml: https://github.com/timhartmann7/burro#config\nhost = \"vps1\"\n\n[pipelines]\ndefault = [\"test\"]\n\n" +
		"[steps.test]\nrun = \"\"   # your test command with a JUnit report, see burro help config\n"
	if got := read(t, root, toml); got != want {
		t.Fatalf("got %q", got)
	}
	root = files(t, nil)
	run(t, root, "", "")
	if got := read(t, root, toml); !strings.Contains(got, "host = \"\"\n") {
		t.Fatalf("without one host: %q", got)
	}
}

func TestAgentsFileAndModes(t *testing.T) {
	root := files(t, map[string]string{claude: "keep me\n", "rules.md": "x\n"})
	if got := lines(run(t, root, "", "rules.md")); got != "created burro.toml\nupdated rules.md "+rulesFor+"\ncreated .gitignore .burro/" {
		t.Fatalf("got %q", got)
	}
	if read(t, root, claude) != "keep me\n" {
		t.Fatal("CLAUDE.md touched")
	}
	for name, want := range map[string]fs.FileMode{"rules.md": 0o600, toml: 0o644, ignore: 0o644} {
		if info, err := os.Stat(filepath.Join(root, name)); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s mode %v, %v; want %v", name, info.Mode().Perm(), err, want)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, ".burro-init-*")); len(leftovers) != 0 {
		t.Fatalf("temp files left: %v", leftovers)
	}
}

// A CLAUDE.md that links to AGENTS.md stays a link, and the block lands once.
func TestLinkedClaude(t *testing.T) {
	root := files(t, map[string]string{agents: "# Agents\n"})
	if err := os.Symlink(agents, filepath.Join(root, claude)); err != nil {
		t.Fatal(err)
	}
	run(t, root, "", "")
	if info, err := os.Lstat(filepath.Join(root, claude)); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("CLAUDE.md is no longer a link: %v", err)
	}
	if n := strings.Count(read(t, root, agents), begin); n != 1 {
		t.Fatalf("%d blocks", n)
	}
}

func TestBrokenMarkers(t *testing.T) {
	root := files(t, map[string]string{agents: "a\n" + begin + "\nno end\n"})
	_, err := initcmd.New(root, "").Run("")
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != output.ExitUsage || !strings.Contains(coded.Fix(), end) {
		t.Fatalf("got %v", err)
	}
	if read(t, root, agents) != "a\n"+begin+"\nno end\n" {
		t.Fatal("AGENTS.md changed")
	}
}

func TestUnwritable(t *testing.T) {
	for _, name := range []string{agents, ignore} {
		root := files(t, nil)
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := initcmd.New(root, "").Run("")
		var coded *output.Error
		if !errors.As(err, &coded) || coded.Code() != output.ExitUsage || !strings.Contains(coded.Fix(), name) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
}

// acceptance: S3-14
func TestBlockMatchesDocs(t *testing.T) {
	block := initcmd.Block()
	if !strings.HasPrefix(block, begin+"\n") || !strings.HasSuffix(block, end+"\n") || strings.Count(block, begin) != 1 {
		t.Fatalf("block %q", block)
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "agent-rules.md"))
	if errors.Is(err, fs.ErrNotExist) && os.Getenv("CI") == "true" {
		// docs/ stays out of git, so a CI checkout has only the embedded
		// copy; this half runs wherever docs/ is checked out.
		t.Log("docs/reference/agent-rules.md is not in this checkout")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	from, to := strings.Index(text, begin), strings.Index(text, end)
	if from < 0 || to < 0 || text[from:to+len(end)]+"\n" != block {
		t.Fatalf("docs/reference/agent-rules.md differs from rules.md inside the markers")
	}
}
