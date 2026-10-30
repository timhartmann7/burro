package update

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	envHome = "HOME"
	current = "0.1.0"
	tag020  = `{"tag_name":"v0.2.0"}`
)

// clock is a settable now.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// api serves a latest-release reply and counts the requests.
func api(t *testing.T, reply string) (string, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Accept") != "application/vnd.github+json" {
			http.Error(w, "headers", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

func newState(t *testing.T, c *clock) (s *State, home string) {
	t.Helper()
	home = t.TempDir()
	s, err := New(home, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return s, home
}

// acceptance: S4-08
func TestCheckOncePerDay(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	s, home := newState(t, c)
	url, calls := api(t, tag020)
	want := "burro 0.2.0 is out, you have 0.1.0. Update: brew upgrade burro\n"
	if got := s.Check(context.Background(), url, current, Brew); got != want {
		t.Fatalf("first check = %q, want %q", got, want)
	}
	c.t = c.t.Add(23 * time.Hour)
	if got := s.Check(context.Background(), url, current, Brew); got != "" || calls.Load() != 1 {
		t.Fatalf("second check within a day = %q after %d calls", got, calls.Load())
	}
	c.t = c.t.Add(time.Hour)
	if got := s.Check(context.Background(), url, current, Brew); got != want || calls.Load() != 2 {
		t.Fatalf("check a day later = %q after %d calls", got, calls.Load())
	}
	// A clock set back must not silence the check for good.
	c.t = c.t.Add(-72 * time.Hour)
	s.Check(context.Background(), url, current, Brew)
	if calls.Load() != 3 {
		t.Fatalf("a check time in the future blocked the check: %d calls", calls.Load())
	}
	fi, err := os.Stat(filepath.Join(home, ".burro", "state.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.json: %v %v", fi, err)
	}
	dir, err := os.Stat(filepath.Join(home, ".burro"))
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf(".burro: %v %v", dir, err)
	}
}

// acceptance: S4-08
func TestCheckOffline(t *testing.T) {
	c := &clock{t: time.Now()}
	s, home := newState(t, c)
	// A listener that never answers is a network that hangs.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	start := time.Now()
	if got := s.Check(context.Background(), "http://"+ln.Addr().String()+"/latest", current, Script); got != "" {
		t.Fatalf("hung network printed %q", got)
	}
	if took := time.Since(start); took > checkTimeout+time.Second {
		t.Fatalf("check took %s, want at most %s", took, checkTimeout)
	}
	// The failed attempt counts, so the next command doesn't wait again.
	start = time.Now()
	if got := s.Check(context.Background(), "http://"+ln.Addr().String()+"/latest", current, Script); got != "" || time.Since(start) > time.Second {
		t.Fatalf("second check after a failure = %q in %s", got, time.Since(start))
	}
	data, err := os.ReadFile(filepath.Join(home, ".burro", "state.json"))
	var st state
	if err != nil || json.Unmarshal(data, &st) != nil || st.Checked.IsZero() {
		t.Fatalf("state.json after a failed check: %q %v", data, err)
	}
}

func TestCheckReplies(t *testing.T) {
	tests := []struct {
		name, reply, version, want string
	}{
		{"newer minor", tag020, current, "burro 0.2.0 is out"},
		{"newer patch", `{"tag_name":"v0.1.1"}`, current, "burro 0.1.1 is out"},
		{"newer major", `{"tag_name":"v1.0.0"}`, "0.9.9", "burro 1.0.0 is out"},
		{"same", `{"tag_name":"v0.1.0"}`, current, ""},
		{"older", `{"tag_name":"v0.0.9"}`, current, ""},
		{"no v", `{"tag_name":"0.2.0"}`, current, "burro 0.2.0 is out"},
		{"pre-release tag", `{"tag_name":"v0.2.0-rc1"}`, current, ""},
		{"leading zero", `{"tag_name":"v0.02.0"}`, current, ""},
		{"negative", `{"tag_name":"v0.-2.0"}`, current, ""},
		{"broken json", `{"tag_name":`, current, ""},
		{"no tag", `{}`, current, ""},
		{"dev build", tag020, "dev", ""},
		{"snapshot build", tag020, "0.1.0-SNAPSHOT-abc1234", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newState(t, &clock{t: time.Now()})
			url, _ := api(t, tt.reply)
			got := s.Check(context.Background(), url, tt.version, Script)
			if tt.want == "" && got != "" || !strings.HasPrefix(got, tt.want) {
				t.Fatalf("got %q, want prefix %q", got, tt.want)
			}
		})
	}
}

func TestCheckHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	s, _ := newState(t, &clock{t: time.Now()})
	if got := s.Check(context.Background(), srv.URL, current, Go); got != "" {
		t.Fatalf("403 printed %q", got)
	}
	s, _ = newState(t, &clock{t: time.Now()})
	if got := s.Check(context.Background(), "http://bad host", current, Go); got != "" {
		t.Fatalf("a broken URL printed %q", got)
	}
}

func TestStateFile(t *testing.T) {
	if _, err := New("relative/home", time.Now); err == nil {
		t.Fatal("a relative HOME passed")
	}
	c := &clock{t: time.Now()}
	s, home := newState(t, c)
	path := filepath.Join(home, ".burro", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A broken file is a fresh start, and a kept mode stays.
	if err := os.WriteFile(path, []byte("{broken"), 0o400); err != nil {
		t.Fatal(err)
	}
	url, calls := api(t, tag020)
	if got := s.Check(context.Background(), url, current, Script); got == "" || calls.Load() != 1 {
		t.Fatalf("check with a broken state file = %q", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("mode after write: %v %v", fi, err)
	}
	// A state file that can't be written means no request: otherwise an
	// unwritable HOME would go online on every command.
	ro, _ := newState(t, c)
	if err := os.WriteFile(filepath.Dir(ro.path), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ro.Check(context.Background(), url, current, Script); got != "" || calls.Load() != 1 {
		t.Fatalf("check with an unwritable HOME = %q after %d calls", got, calls.Load())
	}
	if ro.StarOnce() {
		t.Fatal("the star request showed although it can't be recorded")
	}
}

// acceptance: S4-11
func TestStarOnce(t *testing.T) {
	c := &clock{t: time.Now()}
	s, _ := newState(t, c)
	if !s.StarOnce() {
		t.Fatal("first StarOnce = false")
	}
	if s.StarOnce() {
		t.Fatal("second StarOnce = true")
	}
	// The version check keeps the star flag when it rewrites the file.
	url, _ := api(t, tag020)
	s.Check(context.Background(), url, current, Script)
	if s.StarOnce() {
		t.Fatal("StarOnce after a version check = true")
	}
}

// acceptance: S4-09
func TestDetect(t *testing.T) {
	home := t.TempDir()
	gobin := filepath.Join(t.TempDir(), "gobin")
	gopath2 := t.TempDir()
	// Homebrew links the binary from its prefix into the cask.
	cask := filepath.Join(t.TempDir(), "Caskroom", "burro", "0.1.0")
	if err := os.MkdirAll(cask, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cask, "burro"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "burro")
	if err := os.Symlink(filepath.Join(cask, "burro"), link); err != nil {
		t.Fatal(err)
	}
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	tests := []struct {
		exe  string
		env  map[string]string
		want Method
	}{
		{link, nil, Brew},
		{"/opt/homebrew/Caskroom/burro/0.1.0/burro", nil, Brew},
		{"/usr/local/Cellar/burro/0.1.0/bin/burro", nil, Brew},
		{filepath.Join(home, "go", "bin", "burro"), map[string]string{envHome: home}, Go},
		{filepath.Join(gobin, "burro"), map[string]string{envHome: home, "GOBIN": gobin}, Go},
		{filepath.Join(gopath2, "bin", "burro"), map[string]string{"GOPATH": "/nowhere" + string(filepath.ListSeparator) + gopath2}, Go},
		{filepath.Join(home, "go", "bin", "burro"), map[string]string{envHome: home, "GOPATH": "/elsewhere"}, Script},
		{filepath.Join(home, ".local", "bin", "burro"), map[string]string{envHome: home}, Script},
		{filepath.Join(home, "gobin", "burro"), map[string]string{"GOBIN": "gobin"}, Script},
		{"/usr/local/bin/burro", nil, Script},
		{filepath.Join(home, "go", "binary", "burro"), map[string]string{envHome: home}, Script},
	}
	for _, tt := range tests {
		if got := Detect(tt.exe, env(tt.env)); got != tt.want {
			t.Errorf("Detect(%q, %v) = %s, want %s", tt.exe, tt.env, got, tt.want)
		}
	}
	for m, want := range map[Method]string{
		Brew:   "brew upgrade burro",
		Go:     "go install github.com/timhartmann7/burro/cmd/burro@latest",
		Script: "curl -fsSL https://github.com/timhartmann7/burro/releases/latest/download/install.sh | sh",
	} {
		if got := m.Command(); got != want {
			t.Errorf("%s.Command() = %q, want %q", m, got, want)
		}
	}
}

// acceptance: S4-10
func TestOff(t *testing.T) {
	for _, tt := range []struct {
		env  map[string]string
		want bool
	}{
		{nil, false},
		{map[string]string{"CI": "true"}, true},
		{map[string]string{"CI": "1"}, true},
		{map[string]string{"DO_NOT_TRACK": "1"}, true},
		{map[string]string{"DO_NOT_TRACK": "0"}, false},
		{map[string]string{"BURRO_NO_UPDATE_CHECK": "1"}, true},
	} {
		if got := Off(func(k string) string { return tt.env[k] }); got != tt.want {
			t.Errorf("Off(%v) = %v, want %v", tt.env, got, tt.want)
		}
	}
}
