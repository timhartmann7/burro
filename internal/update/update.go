// Package update tells how burro was installed, checks once a day for a newer
// release and keeps ~/.burro/state.json, which also remembers the star
// request.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LatestRelease is the GitHub API call that names the newest release.
const LatestRelease = "https://api.github.com/repos/timhartmann7/burro/releases/latest"

const (
	// checkEvery is how often the version check may go online.
	checkEvery = 24 * time.Hour
	// checkTimeout bounds the whole request, so an offline machine waits no
	// longer than this.
	checkTimeout = 2 * time.Second
	// maxReply caps the API reply, which is a few kilobytes.
	maxReply = 1 << 20
)

// Method is how this binary was installed. It picks the update command.
type Method string

// The install methods burro tells apart.
const (
	Brew   Method = "brew"
	Go     Method = "go"
	Script Method = "script"
)

// Command updates a binary installed this way.
func (m Method) Command() string {
	switch m {
	case Brew:
		return "brew upgrade burro"
	case Go:
		return "go install github.com/timhartmann7/burro/cmd/burro@latest"
	case Script:
		return "curl -fsSL https://github.com/timhartmann7/burro/releases/latest/download/install.sh | sh"
	}
	// Any other binary was installed by hand, as the script does it.
	return Script.Command()
}

// Detect tells the install method from the executable path, with symlinks
// resolved: Homebrew keeps it under Caskroom or Cellar, go install puts it
// into GOBIN or the bin folder of GOPATH.
func Detect(exe string, getenv func(string) string) Method {
	exe = resolve(exe)
	if strings.Contains(exe, "/Caskroom/") || strings.Contains(exe, "/Cellar/") {
		return Brew
	}
	var dirs []string
	if gobin := getenv("GOBIN"); gobin != "" {
		dirs = append(dirs, gobin)
	}
	gopath := getenv("GOPATH")
	if gopath == "" && getenv("HOME") != "" {
		gopath = filepath.Join(getenv("HOME"), "go")
	}
	for _, p := range filepath.SplitList(gopath) {
		if p != "" {
			dirs = append(dirs, filepath.Join(p, "bin"))
		}
	}
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		if rel, err := filepath.Rel(resolve(d), exe); err == nil && !strings.HasPrefix(rel, "..") {
			return Go
		}
	}
	return Script
}

// resolve follows symlinks when the path exists and cleans it either way.
func resolve(path string) string {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		return target
	}
	return filepath.Clean(path)
}

// state is ~/.burro/state.json.
type state struct {
	Checked   time.Time `json:"version_checked,omitzero"`
	StarShown bool      `json:"star_shown,omitempty"`
}

// State is ~/.burro/state.json of one client and the checks that use it.
type State struct {
	path   string
	now    func() time.Time
	client *http.Client
}

// New returns the state under the absolute home folder. now is the clock.
func New(home string, now func() time.Time) (*State, error) {
	if !filepath.IsAbs(home) {
		return nil, errors.New("HOME is not an absolute path")
	}
	return &State{
		path:   filepath.Join(home, ".burro", "state.json"),
		now:    now,
		client: &http.Client{Timeout: checkTimeout},
	}, nil
}

// Off tells whether the environment turns the version check off.
func Off(getenv func(string) string) bool {
	return getenv("CI") != "" || getenv("DO_NOT_TRACK") == "1" || getenv("BURRO_NO_UPDATE_CHECK") == "1"
}

// Check asks api for the latest release at most once per day and returns the
// line that tells a binary of version, installed by method, about a newer
// one, or "". The attempt counts even when it
// fails, so an offline machine doesn't retry on every command. Every failure
// is silent: the check must never get in the way of the command.
func (s *State) Check(ctx context.Context, api, version string, method Method) string {
	current, ok := parse(version)
	if !ok {
		return ""
	}
	st := s.read()
	now := s.now()
	if !st.Checked.IsZero() && now.Sub(st.Checked) < checkEvery && !st.Checked.After(now) {
		return ""
	}
	st.Checked = now
	if s.write(st) != nil {
		return ""
	}
	latest, ok := s.latest(ctx, api)
	if !ok || !newer(latest, current) {
		return ""
	}
	return fmt.Sprintf("burro %s is out, you have %s. Update: %s\n", format(latest), version, method.Command())
}

func (s *State) latest(ctx context.Context, api string) ([3]int, bool) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, http.NoBody)
	if err != nil {
		return [3]int{}, false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "burro")
	resp, err := s.client.Do(req)
	if err != nil {
		return [3]int{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return [3]int{}, false
	}
	var reply struct {
		Tag string `json:"tag_name"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxReply)).Decode(&reply) != nil {
		return [3]int{}, false
	}
	return parse(strings.TrimPrefix(reply.Tag, "v"))
}

// StarOnce tells whether the star request should show now, and records
// that it did. It shows once ever.
func (s *State) StarOnce() bool {
	st := s.read()
	if st.StarShown {
		return false
	}
	st.StarShown = true
	return s.write(st) == nil
}

// read returns the saved state; a missing or broken file is a fresh start.
func (s *State) read() state {
	var st state
	data, err := os.ReadFile(s.path)
	if err == nil && json.Unmarshal(data, &st) != nil {
		st = state{}
	}
	return st
}

// write replaces the file atomically: temp file in the same folder, fsync,
// rename. The old mode stays; a new file gets 0600 and a new folder 0700.
func (s *State) write(st state) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(s.path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	_, err = f.Write(append(data, '\n'))
	err = errors.Join(err, f.Chmod(mode), f.Sync(), f.Close())
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", s.path, err), os.Remove(f.Name()))
	}
	return nil
}

// parse reads a release version X.Y.Z.
func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != len(out) {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func format(v [3]int) string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}
