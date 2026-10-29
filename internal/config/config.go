// Package config parses and validates burro.toml, validates names and keeps
// project paths inside the project root.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/timhartmann7/burro/internal/output"
)

// File is the project config at the repository root.
const File = "burro.toml"

// homePrefix marks a server path relative to the home folder.
const homePrefix = "~/"

// namePattern keeps names safe inside ssh config, compose project names and
// file paths.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Config is a validated burro.toml.
type Config struct {
	host string
	path string
}

// Load reads and validates burro.toml in root. The default path uses the
// name of root as the repository name.
func Load(root string) (*Config, error) {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, File)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, output.NewError(output.ExitUsage,
			fmt.Sprintf("no %s in %s", File, root),
			fmt.Sprintf(`create %s with host = "<name>", a name from burro host ls`, File))
	}
	if err != nil {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("read %s: %v", File, err),
			fmt.Sprintf("make %s readable", File))
	}
	return Parse(data, filepath.Base(root))
}

// Parse validates the contents of burro.toml. repo is the repository name
// that the default path ends with.
func Parse(data []byte, repo string) (*Config, error) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, syntaxError(err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if k != "host" && k != "path" {
			return nil, output.NewError(output.ExitUsage,
				fmt.Sprintf("%s: unknown key %q", File, k),
				fmt.Sprintf("remove %q from %s; it knows host and path", k, File))
		}
	}

	host, err := field(raw, "host", "")
	if err != nil {
		return nil, err
	}
	if host == "" {
		return nil, output.NewError(output.ExitUsage, File+": host is missing",
			`set host = "<name>" in `+File+", a name from burro host ls")
	}
	if err := ValidName("host", host); err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}

	path, err := field(raw, "path", homePrefix+"burro/"+repo)
	if err != nil {
		return nil, err
	}
	if !validPath(path) {
		return nil, output.NewError(output.ExitUsage,
			fmt.Sprintf("%s: path %q is not allowed", File, path),
			`set path to a folder under ~/ or an absolute path without . or .. parts, for example path = "~/burro/myapp"`)
	}
	return &Config{host: host, path: strings.TrimSuffix(path, "/")}, nil
}

func syntaxError(err error) error {
	var perr toml.ParseError
	if errors.As(err, &perr) {
		return output.NewError(output.ExitUsage,
			fmt.Sprintf("%s: line %d: %s", File, perr.Position.Line, firstLine(perr.Message)),
			fmt.Sprintf("fix the TOML syntax on line %d of %s", perr.Position.Line, File))
	}
	return output.NewError(output.ExitUsage, fmt.Sprintf("%s: %s", File, firstLine(err.Error())),
		fmt.Sprintf("fix the TOML syntax of %s", File))
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// field returns the string value of key, or def when the key is absent.
func field(raw map[string]any, key, def string) (string, error) {
	v, ok := raw[key]
	if !ok {
		return def, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", output.NewError(output.ExitUsage,
			fmt.Sprintf("%s: %s must be a string", File, key),
			fmt.Sprintf(`write %s as a quoted string, for example %s = %q`, key, key, example(key)))
	}
	return s, nil
}

func example(key string) string {
	if key == "host" {
		return "vps1"
	}
	return homePrefix + "burro/myapp"
}

// validPath accepts ~/a/b or /a/b with no empty, . or .. parts and no control
// characters. A trailing slash is fine.
func validPath(p string) bool {
	if !utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl) {
		return false
	}
	var rest string
	switch {
	case strings.HasPrefix(p, homePrefix):
		rest = p[len(homePrefix):]
	case strings.HasPrefix(p, "/"):
		rest = p[1:]
	default:
		return false
	}
	rest = strings.TrimSuffix(rest, "/")
	for part := range strings.SplitSeq(rest, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// Host is the host name.
func (c *Config) Host() string {
	return c.host
}

// Dir is the project folder in the form remote commands use: relative to
// the home folder, where every ssh command starts, or absolute.
func (c *Config) Dir() string {
	return strings.TrimPrefix(c.path, homePrefix)
}

// ValidName checks a host, pipeline, step or secret name. field names what
// the name is for in the error.
func ValidName(field, name string) error {
	if namePattern.MatchString(name) {
		return nil
	}
	return output.NewError(output.ExitUsage,
		fmt.Sprintf("%s %q is not a valid name", field, name),
		"use lowercase letters, digits, - and _, up to 63 characters, starting with a letter or digit")
}

// ReadInRoot reads the file at name, relative to dir unless absolute, and
// fails unless it resolves inside root. Symlinks that point outside fail too.
func ReadInRoot(root, dir, name string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("open project root: %v", err),
			"run burro inside the project's git repository")
	}
	defer func() { _ = r.Close() }()

	// The working folder may sit behind a symlink such as /tmp on macOS, while
	// git reports the resolved root.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	abs := name
	if !filepath.IsAbs(name) {
		abs = filepath.Join(resolved, name)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("%s: not inside the project folder %s", name, root),
			"move the script into the project folder")
	}
	data, err := r.ReadFile(rel)
	switch {
	case err == nil:
		return data, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("%s: no such file", name),
			"check the path of the script")
	default:
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("%s: %v", name, err),
			"use a readable file that stays inside the project folder")
	}
}
