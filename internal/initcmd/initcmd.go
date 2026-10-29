// Package initcmd sets a project up for burro: burro.toml from the
// template, the agent rules block in AGENTS.md or CLAUDE.md, and .burro/ in
// .gitignore.
package initcmd

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/timhartmann7/burro/internal/output"
)

// rules is the agent rules block with its markers, the one copy that
// docs/reference/agent-rules.md and the README follow.
//
//go:embed rules.md
var rules string

const (
	beginMarker = "<!-- burro:begin -->"
	endMarker   = "<!-- burro:end -->"
	agentsName  = "AGENTS.md"
	claudeName  = "CLAUDE.md"
	tomlName    = "burro.toml"
	ignoreName  = ".gitignore"
	ignoreLine  = ".burro/"
	// importLine makes Claude Code read AGENTS.md.
	importLine = "@" + agentsName
	rulesDone  = "rules for your agent"
	// newMode is the mode of files init creates: plain files of the
	// repository.
	newMode fs.FileMode = 0o644
)

// Block is the agent rules block with its markers, as --print shows it.
func Block() string {
	return rules
}

// Project is the git repository init sets up.
type Project struct {
	root string
	host string
}

// New returns the project at root, the top folder of the repository. host
// goes into a new burro.toml; it is empty unless exactly one host exists.
func New(root, host string) *Project {
	return &Project{root: root, host: host}
}

// Run writes every file and returns one line per file. agentsFile, relative
// to the root, is where the rules go instead of AGENTS.md or CLAUDE.md.
func (p *Project) Run(agentsFile string) ([]output.InitFile, error) {
	toml, err := p.toml()
	if err != nil {
		return nil, err
	}
	files := []output.InitFile{toml}
	for _, t := range p.targets(agentsFile) {
		f, err := p.write(t.name, t.change)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	ignore, err := p.write(ignoreName, ignored)
	if err != nil {
		return nil, err
	}
	return append(files, ignore), nil
}

// change turns a file's content into the new content and says what it
// changed. A file that doesn't exist comes in as "".
type change func(name, content string) (next, detail string, err error)

type target struct {
	name   string
	change change
}

// targets are the files that get the rules: AGENTS.md and CLAUDE.md as they
// exist, AGENTS.md and an importing CLAUDE.md when neither does. A
// CLAUDE.md that imports AGENTS.md has the rules already.
func (p *Project) targets(agentsFile string) []target {
	if agentsFile != "" {
		return []target{{agentsFile, withRules}}
	}
	agents, claude := p.exists(agentsName), p.exists(claudeName)
	switch {
	case agents && claude:
		return []target{{agentsName, withRules}, {claudeName, keepImport}}
	case claude:
		return []target{{claudeName, withRules}}
	case agents:
		return []target{{agentsName, withRules}}
	default:
		return []target{{agentsName, withRules}, {claudeName, imports}}
	}
}

func (p *Project) exists(name string) bool {
	_, err := os.Stat(filepath.Join(p.root, name))
	return err == nil
}

// toml creates burro.toml from the template unless there is one, which it
// never touches.
func (p *Project) toml() (output.InitFile, error) {
	path := filepath.Join(p.root, tomlName)
	if _, err := os.Lstat(path); err == nil {
		return output.InitFile{Name: tomlName, Change: output.Unchanged}, nil
	}
	if err := writeAtomic(path, []byte(template(p.host)), newMode); err != nil {
		return output.InitFile{}, err
	}
	return output.InitFile{Name: tomlName, Change: output.Created}, nil
}

// template is the burro.toml of docs/reference/config.md. host is a
// checked name, so it needs no escaping.
func template(host string) string {
	return "# burro.toml: https://github.com/timhartmann7/burro#config\n" +
		"host = \"" + host + "\"\n\n" +
		"[pipelines]\ndefault = [\"test\"]\n\n" +
		"[steps.test]\nrun = \"\"   # your test command with a JUnit report, see burro help config\n"
}

// write applies fn to the file name under the root and writes the result
// when it differs. A link is followed, so a CLAUDE.md that links to
// AGENTS.md stays a link.
func (p *Project) write(name string, fn change) (output.InitFile, error) {
	path := filepath.Join(p.root, name)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	data, err := os.ReadFile(filepath.Clean(path))
	created := errors.Is(err, fs.ErrNotExist)
	if err != nil && !created {
		return output.InitFile{}, fileError(name, err)
	}
	next, detail, err := fn(name, string(data))
	if err != nil || next == string(data) {
		return output.InitFile{Name: name, Change: output.Unchanged}, err
	}
	mode := newMode
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(path, []byte(next), mode); err != nil {
		return output.InitFile{}, fileError(name, err)
	}
	f := output.InitFile{Name: name, Change: output.Updated, Detail: detail}
	if created {
		f.Change = output.Created
	}
	return f, nil
}

// withRules puts the rules block into content: in place of an old block
// between the markers, or at the end. Text outside the markers stays as it
// is.
func withRules(name, content string) (next, detail string, err error) {
	begin := strings.Index(content, beginMarker)
	if begin < 0 {
		return appendText(content, rules), rulesDone, nil
	}
	end := strings.Index(content[begin:], endMarker)
	if end < 0 {
		return "", "", output.NewError(output.ExitUsage, fmt.Sprintf("%s has %s without %s", name, beginMarker, endMarker),
			fmt.Sprintf("add %s after the rules block in %s, or remove %s, then rerun burro init", endMarker, name, beginMarker))
	}
	end += begin + len(endMarker)
	return content[:begin] + strings.TrimSuffix(rules, "\n") + content[end:], rulesDone, nil
}

// imports is a new CLAUDE.md that reads AGENTS.md.
func imports(_, content string) (next, detail string, err error) {
	return appendText(content, importLine+"\n"), importLine, nil
}

// keepImport leaves a CLAUDE.md that imports AGENTS.md alone and gives any
// other the rules.
func keepImport(name, content string) (next, detail string, err error) {
	for line := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(line) == importLine {
			return content, "", nil
		}
	}
	return withRules(name, content)
}

// ignored adds .burro/ to .gitignore unless a line has it already.
func ignored(_, content string) (next, detail string, err error) {
	for line := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(line) == ignoreLine {
			return content, "", nil
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + ignoreLine + "\n", ignoreLine, nil
}

// appendText adds text at the end of content, after a blank line when
// content holds text of its own.
func appendText(content, text string) string {
	switch {
	case content == "":
		return text
	case strings.HasSuffix(content, "\n\n"):
		return content + text
	case strings.HasSuffix(content, "\n"):
		return content + "\n" + text
	default:
		return content + "\n\n" + text
	}
}

// writeAtomic replaces path in one rename: temp file in the same folder,
// fsync, rename. mode is the mode the file keeps or gets.
func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".burro-init-*")
	if err != nil {
		return fileError(filepath.Base(path), err)
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Chmod(mode), f.Sync(), f.Close())
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return errors.Join(fileError(filepath.Base(path), err), os.Remove(f.Name()))
	}
	return nil
}

func fileError(name string, err error) error {
	return output.NewError(output.ExitUsage, fmt.Sprintf("%s: %v", name, err),
		fmt.Sprintf("make %s and its folder writable, then rerun burro init", name))
}
