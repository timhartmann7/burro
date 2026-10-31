// Package config parses and validates burro.toml, validates names and keeps
// project paths inside the project root.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
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

// Default limits of a step from docs/reference/config.md.
const (
	defaultTimeout = 60 * time.Minute
	minTimeout     = time.Second
	maxTimeout     = 24 * time.Hour
	// DefaultPipeline is the pipeline burro run starts without a name.
	DefaultPipeline = "default"
)

// envPattern keeps step variables to plain shell names.
var envPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// runPattern matches run ids: r1, r2, ...
var runPattern = regexp.MustCompile(`^r[1-9]\d{0,8}$`)

// Config is a validated burro.toml.
type Config struct {
	host      string
	path      string
	pipelines map[string][]string
	steps     map[string]Step
	drop      []*regexp.Regexp
	// secrets maps a secret name to the local file that holds it.
	secrets map[string]string
}

// Step is one validated step of burro.toml.
type Step struct {
	Name            string
	Run             string
	Env             map[string]string
	Timeout         time.Duration
	ContinueOnFail  bool
	SkipIfUnchanged []string
	// JUnit is a slash-separated path or glob inside the project, empty
	// without a report.
	JUnit   string
	Secrets []string
}

// Selected is one pipeline step of a run. Skip says why the step stays out
// of the run, such as "--from e2e"; it is empty when the step runs.
type Selected struct {
	Step Step
	Skip string
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
	top := table{raw: raw}
	if err := top.only("host", "path", "pipelines", "steps", "filters", "secrets"); err != nil {
		return nil, err
	}

	host, err := top.str("host", "", "vps1")
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

	path, err := top.str("path", homePrefix+"burro/"+repo, homePrefix+"burro/myapp")
	if err != nil {
		return nil, err
	}
	if !validPath(path) {
		return nil, output.NewError(output.ExitUsage,
			fmt.Sprintf("%s: path %q is not allowed", File, path),
			`set path to a folder under ~/ or an absolute path without . or .. parts, for example path = "~/burro/myapp"`)
	}
	c := &Config{host: host, path: strings.TrimSuffix(path, "/")}
	if c.steps, err = parseSteps(top); err != nil {
		return nil, err
	}
	if c.pipelines, err = parsePipelines(top, c.steps); err != nil {
		return nil, err
	}
	if c.drop, err = parseFilters(top); err != nil {
		return nil, err
	}
	if c.secrets, err = parseSecrets(top, c.steps); err != nil {
		return nil, err
	}
	return c, nil
}

func parseSteps(top table) (map[string]Step, error) {
	t, ok, err := top.sub("steps")
	if err != nil {
		return nil, err
	}
	if !ok || len(t.raw) == 0 {
		return nil, output.NewError(output.ExitUsage, File+": steps is missing",
			`add a step to `+File+`, for example [steps.test] with run = "make test"`)
	}
	steps := map[string]Step{}
	for _, name := range t.keys() {
		if err := ValidName("step", name); err != nil {
			return nil, fmt.Errorf("%s: steps: %w", File, err)
		}
		st, _, err := t.sub(name)
		if err != nil {
			return nil, err
		}
		step, err := parseStep(name, st)
		if err != nil {
			return nil, err
		}
		steps[name] = step
	}
	return steps, nil
}

func parseStep(name string, t table) (Step, error) {
	s := Step{Name: name}
	if err := t.only("run", "env", "timeout", "continue_on_fail", "skip_if_unchanged", "junit", "secrets"); err != nil {
		return s, err
	}
	var err error
	if s.Run, err = t.str("run", "", "make test"); err != nil {
		return s, err
	}
	if strings.TrimSpace(s.Run) == "" {
		return s, output.NewError(output.ExitUsage, fmt.Sprintf("%s: %s is empty", File, t.key("run")),
			fmt.Sprintf(`set %s to your test command, for example run = "make test"`, t.key("run")))
	}
	if s.Timeout, err = parseTimeout(t); err != nil {
		return s, err
	}
	if s.ContinueOnFail, err = t.boolean("continue_on_fail"); err != nil {
		return s, err
	}
	if s.SkipIfUnchanged, err = t.list("skip_if_unchanged", "pnpm-lock.yaml"); err != nil {
		return s, err
	}
	for _, f := range s.SkipIfUnchanged {
		if !filepath.IsLocal(f) {
			return s, output.NewError(output.ExitUsage,
				fmt.Sprintf("%s: %s: %q is not a path inside the project", File, t.key("skip_if_unchanged"), f),
				fmt.Sprintf("list files in %s relative to the project root, without .. parts", t.key("skip_if_unchanged")))
		}
	}
	if s.JUnit, err = parseJUnit(t); err != nil {
		return s, err
	}
	if s.Secrets, err = t.list("secrets", "npmrc"); err != nil {
		return s, err
	}
	s.Env, err = parseEnv(t)
	return s, err
}

// parseJUnit reads the report glob of a step. It stays inside the project,
// so the runner can open its matches through os.Root.
func parseJUnit(t table) (string, error) {
	junit, err := t.str("junit", "", ".burro/out/unit.xml")
	if err != nil || junit == "" {
		return "", err
	}
	clean := filepath.ToSlash(filepath.Clean(junit))
	_, badPattern := filepath.Match(clean, "")
	if !filepath.IsLocal(junit) || !fs.ValidPath(clean) || badPattern != nil {
		return "", output.NewError(output.ExitUsage,
			fmt.Sprintf("%s: %s %q is not a path or glob inside the project", File, t.key("junit"), junit),
			fmt.Sprintf(`set %s relative to the project root, for example junit = "target/surefire-reports/*.xml"`, t.key("junit")))
	}
	return clean, nil
}

// parseSecrets reads [secrets] and checks that every secret a step names
// is there.
func parseSecrets(top table, steps map[string]Step) (map[string]string, error) {
	secrets, err := parseSecretFiles(top)
	if err != nil {
		return nil, err
	}
	for _, step := range slices.Sorted(maps.Keys(steps)) {
		for _, name := range steps[step].Secrets {
			if _, ok := secrets[name]; !ok {
				return nil, output.NewError(output.ExitUsage,
					fmt.Sprintf("%s: steps.%s.secrets: unknown secret %q", File, tomlKey(step), name),
					fmt.Sprintf("add %s = \"<local file>\" under [secrets] or remove %q from steps.%s.secrets", tomlKey(name), name, tomlKey(step)))
			}
		}
	}
	return secrets, nil
}

func parseSecretFiles(top table) (map[string]string, error) {
	secrets := map[string]string{}
	t, ok, err := top.sub("secrets")
	if err != nil || !ok {
		return secrets, err
	}
	for _, name := range t.keys() {
		if err := ValidName("secret", name); err != nil {
			return nil, fmt.Errorf("%s: secrets: %w", File, err)
		}
		file, err := t.str(name, "", "~/.npmrc")
		if err != nil {
			return nil, err
		}
		if file == "" {
			return nil, output.NewError(output.ExitUsage, fmt.Sprintf("%s: %s is empty", File, t.key(name)),
				fmt.Sprintf(`set %s to a local file, for example %s = "~/.npmrc"`, t.key(name), name))
		}
		secrets[name] = file
	}
	return secrets, nil
}

func parseTimeout(t table) (time.Duration, error) {
	timeout, err := t.str("timeout", defaultTimeout.String(), "20m")
	if err != nil {
		return 0, err
	}
	d, err := time.ParseDuration(timeout)
	if err != nil || d < minTimeout || d > maxTimeout {
		return 0, output.NewError(output.ExitUsage,
			fmt.Sprintf("%s: %s %q is not a duration from 1s to 24h", File, t.key("timeout"), timeout),
			fmt.Sprintf(`set %s to a duration such as "20m" or "1h30m"`, t.key("timeout")))
	}
	return d, nil
}

func parseEnv(step table) (map[string]string, error) {
	vars := map[string]string{}
	env, ok, err := step.sub("env")
	if err != nil || !ok {
		return vars, err
	}
	for _, k := range env.keys() {
		if !envPattern.MatchString(k) || strings.HasPrefix(k, "BURRO_") {
			return nil, output.NewError(output.ExitUsage,
				fmt.Sprintf("%s: %s is not an allowed variable name", File, env.key(k)),
				fmt.Sprintf("rename %s to upper case letters, digits and _, not starting with BURRO_", env.key(k)))
		}
		if vars[k], err = env.str(k, "", "true"); err != nil {
			return nil, err
		}
	}
	return vars, nil
}

func parsePipelines(top table, steps map[string]Step) (map[string][]string, error) {
	t, ok, err := top.sub("pipelines")
	if err != nil {
		return nil, err
	}
	if !ok || len(t.raw) == 0 {
		return nil, output.NewError(output.ExitUsage, File+": pipelines is missing",
			`add a pipeline to `+File+`, for example [pipelines] with default = ["test"]`)
	}
	pipelines := map[string][]string{}
	for _, name := range t.keys() {
		if err := ValidName("pipeline", name); err != nil {
			return nil, fmt.Errorf("%s: pipelines: %w", File, err)
		}
		list, err := t.list(name, "test")
		if err != nil {
			return nil, err
		}
		if err := checkPipeline(t.key(name), list, steps); err != nil {
			return nil, err
		}
		pipelines[name] = list
	}
	return pipelines, nil
}

// checkPipeline fails on a step that doesn't exist or comes twice. key is
// the pipeline's field name.
func checkPipeline(key string, list []string, steps map[string]Step) error {
	for i, step := range list {
		if _, ok := steps[step]; !ok {
			return output.NewError(output.ExitUsage,
				fmt.Sprintf("%s: %s: unknown step %q", File, key, step),
				fmt.Sprintf("add [steps.%s] or remove %q from %s", tomlKey(step), step, key))
		}
		if slices.Contains(list[:i], step) {
			return output.NewError(output.ExitUsage,
				fmt.Sprintf("%s: %s lists step %q twice", File, key, step),
				fmt.Sprintf("list %q once in %s", step, key))
		}
	}
	return nil
}

func parseFilters(top table) ([]*regexp.Regexp, error) {
	t, ok, err := top.sub("filters")
	if err != nil || !ok {
		return nil, err
	}
	if err := t.only("drop"); err != nil {
		return nil, err
	}
	patterns, err := t.list("drop", `^\s*$`)
	if err != nil {
		return nil, err
	}
	drop := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, output.NewError(output.ExitUsage,
				fmt.Sprintf("%s: %s: %q is not a valid regular expression", File, t.key("drop"), p),
				fmt.Sprintf("fix the pattern in %s; it uses Go RE2 syntax", t.key("drop")))
		}
		drop = append(drop, re)
	}
	return drop, nil
}

// tomlKey prints a key the way it can be written back into a table header.
// Unknown steps come from the user's file and may need quotes.
func tomlKey(k string) string {
	if namePattern.MatchString(k) {
		return k
	}
	return strconv.Quote(k)
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

// table is one decoded TOML table with its dotted name, so every error names
// the full field.
type table struct {
	name string
	raw  map[string]any
}

func (t table) key(k string) string {
	if t.name == "" {
		return k
	}
	return t.name + "." + k
}

func (t table) keys() []string {
	keys := make([]string, 0, len(t.raw))
	for k := range t.raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// only fails on the first key, in sorted order, that allowed doesn't list.
func (t table) only(allowed ...string) error {
	for _, k := range t.keys() {
		if !slices.Contains(allowed, k) {
			return output.NewError(output.ExitUsage,
				fmt.Sprintf("%s: unknown key %q", File, t.key(k)),
				fmt.Sprintf("remove %q from %s; it knows %s", t.key(k), File, strings.Join(allowed, ", ")))
		}
	}
	return nil
}

// str returns the string value of k, or def when the key is absent.
func (t table) str(k, def, example string) (string, error) {
	v, ok := t.raw[k]
	if !ok {
		return def, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", t.typeError(k, "a string", fmt.Sprintf("%s = %q", tomlKey(k), example))
	}
	return s, nil
}

func (t table) boolean(k string) (bool, error) {
	v, ok := t.raw[k]
	if !ok {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, t.typeError(k, "true or false", k+" = true")
	}
	return b, nil
}

// list returns the array of strings at k, empty when the key is absent.
func (t table) list(k, example string) ([]string, error) {
	v, ok := t.raw[k]
	if !ok {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, t.typeError(k, "a list of strings", fmt.Sprintf("%s = [%q]", tomlKey(k), example))
	}
	list := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, t.typeError(k, "a list of strings", fmt.Sprintf("%s = [%q]", tomlKey(k), example))
		}
		list = append(list, s)
	}
	return list, nil
}

// sub returns the table at k and whether it is there.
func (t table) sub(k string) (table, bool, error) {
	v, ok := t.raw[k]
	if !ok {
		return table{}, false, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return table{}, false, t.typeError(k, "a table", "["+t.key(k)+"]")
	}
	return table{name: t.key(k), raw: m}, true, nil
}

func (t table) typeError(k, want, example string) error {
	return output.NewError(output.ExitUsage,
		fmt.Sprintf("%s: %s must be %s", File, t.key(k), want),
		fmt.Sprintf("write %s as %s, for example %s", t.key(k), want, example))
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

// Pipelines lists the pipeline names in sorted order.
func (c *Config) Pipelines() []string {
	names := make([]string, 0, len(c.pipelines))
	for name := range c.pipelines {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Step returns the step called name.
func (c *Config) Step(name string) (Step, bool) {
	s, ok := c.steps[name]
	return s, ok
}

// Drop is the filters.drop patterns: matching lines leave clean logs.
func (c *Config) Drop() []*regexp.Regexp {
	return slices.Clone(c.drop)
}

// Select returns the steps of pipeline in order. A step before from, or one
// that only doesn't name, stays out of the run. Empty from and nil only
// select every step. args go to the end of one step's command, so with args
// exactly one step may run.
func (c *Config) Select(pipeline, from string, only, args []string) ([]Selected, error) {
	names, ok := c.pipelines[pipeline]
	if !ok {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("%s has no pipeline %q", File, pipeline),
			"use one of: "+strings.Join(c.Pipelines(), ", "))
	}
	wanted := slices.Clone(only)
	if from != "" {
		wanted = append(wanted, from)
	}
	for _, want := range wanted {
		if !slices.Contains(names, want) {
			return nil, output.NewError(output.ExitUsage, fmt.Sprintf("pipeline %s has no step %q", pipeline, want),
				"use one of: "+strings.Join(names, ", "))
		}
	}
	selected := make([]Selected, 0, len(names))
	var running []string
	started := from == ""
	for _, name := range names {
		started = started || name == from
		sel := Selected{Step: c.steps[name]}
		switch {
		case !started:
			sel.Skip = "--from " + from
		case only != nil && !slices.Contains(only, name):
			sel.Skip = "--only " + strings.Join(only, ",")
		default:
			running = append(running, name)
		}
		selected = append(selected, sel)
	}
	if len(args) > 0 && len(running) != 1 {
		step := "<step>"
		if len(running) > 0 {
			step = running[0]
		}
		return nil, output.NewError(output.ExitUsage,
			fmt.Sprintf("arguments after -- go to one step, and pipeline %s runs %d: %s", pipeline, len(running), strings.Join(running, ", ")),
			fmt.Sprintf("pick the step with --only, for example burro run %s --only %s -- %s", pipeline, step, strings.Join(args, " ")))
	}
	return selected, nil
}

// Secrets reads the local files of the secrets that the running steps of
// selected need. root is the project root that relative paths start from,
// home the folder ~/ stands for. A missing file is exit 4, before anything
// reaches the server.
func (c *Config) Secrets(selected []Selected, root, home string) (map[string][]byte, error) {
	values := map[string][]byte{}
	for i := range selected {
		if selected[i].Skip != "" {
			continue
		}
		for _, name := range selected[i].Step.Secrets {
			if _, done := values[name]; done {
				continue
			}
			data, err := os.ReadFile(c.secretFile(name, root, home))
			if err != nil {
				return nil, c.secretError(name, err)
			}
			values[name] = data
		}
	}
	return values, nil
}

func (c *Config) secretFile(name, root, home string) string {
	file := c.secrets[name]
	switch {
	case strings.HasPrefix(file, homePrefix):
		return filepath.Join(home, file[len(homePrefix):])
	case filepath.IsAbs(file):
		return filepath.Clean(file)
	default:
		return filepath.Join(root, file)
	}
}

func (c *Config) secretError(name string, err error) error {
	file := c.secrets[name]
	if errors.Is(err, fs.ErrNotExist) {
		return output.NewError(output.ExitUsage, fmt.Sprintf("secret %s: %s does not exist", name, file),
			fmt.Sprintf("create %s, or point secrets.%s in %s to the file that holds it", file, name, File))
	}
	return output.NewError(output.ExitUsage, fmt.Sprintf("secret %s: read %s: %v", name, file, errors.Unwrap(err)),
		fmt.Sprintf("make %s readable", file))
}

// ValidRun checks a run id such as r42. It ends up in file paths on the
// server.
func ValidRun(id string) error {
	if runPattern.MatchString(id) {
		return nil
	}
	return output.NewError(output.ExitUsage, fmt.Sprintf("%q is not a run id", id),
		"pass a run id such as r42, or leave it out for the last run")
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

// InRoot returns name, relative to dir unless absolute, as a path relative
// to root, and whether it stays inside root.
func InRoot(root, dir, name string) (string, bool) {
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
		return "", false
	}
	return rel, true
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

	rel, ok := InRoot(root, dir, name)
	if !ok {
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
