package hosts

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/output"
)

// newTestStore returns a store over a fresh HOME. ssh -G runs for real; it
// reads only that HOME's config.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "burro-store-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	s, err := NewStore(home, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeTestFile(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func wantCode(t *testing.T, err error, code output.Code) {
	t.Helper()
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != code || coded.Fix() == "" {
		t.Fatalf("got %v, want exit %d with a fix line", err, code)
	}
}

func TestNewStore(t *testing.T) {
	if _, err := NewStore("", nil); err == nil {
		t.Fatal("empty HOME accepted")
	}
	if _, err := NewStore("relative", nil); err == nil {
		t.Fatal("relative HOME accepted")
	}
}

func TestAddWritesBlockAndInclude(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	key := filepath.Join(s.home, "id_test")
	writeTestFile(t, key, "key", 0o600)
	h, _, err := s.Add(ctx, "vps1", "root@203.0.113.10:2222", key, false)
	if err != nil {
		t.Fatal(err)
	}
	if h.Name() != "vps1" || h.Address(ctx) != "root@203.0.113.10:2222" {
		t.Fatalf("host %q at %q", h.Name(), h.Address(ctx))
	}
	want := "# burro-host vps1\nHost vps1\n  HostName 203.0.113.10\n  User root\n  Port 2222\n" +
		`  IdentityFile "` + key + "\"\n\n"
	if got := readTestFile(t, s.hostsPath()); got != want {
		t.Fatalf("hosts file:\n%s\nwant:\n%s", got, want)
	}
	if _, _, err := s.Add(ctx, "v6", "admin@[2001:db8::1]", "", false); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 || list[1].Address(ctx) != "admin@[2001:db8::1]" {
		t.Fatalf("List = %v, %v", list, err)
	}
	// ssh itself must resolve the new block through the Include line.
	lines, err := s.resolve(ctx, "vps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hostname 203.0.113.10", "user root", "port 2222"} {
		if !contains(lines, want) {
			t.Errorf("ssh -G vps1 lacks %q", want)
		}
	}
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// acceptance: S1-06
func TestAddTwiceAndIncludeOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	writeTestFile(t, s.configPath(), "Host other\n  User me\n", 0o600)
	if _, _, err := s.Add(ctx, "vps1", "root@203.0.113.10", "", false); err != nil {
		t.Fatal(err)
	}
	before := readTestFile(t, s.hostsPath())
	_, _, err := s.Add(ctx, "vps1", "root@203.0.113.11", "", false)
	wantCode(t, err, output.ExitUsage)
	if readTestFile(t, s.hostsPath()) != before {
		t.Fatal("a refused add changed the hosts file")
	}
	for range 3 {
		if _, _, err := s.Add(ctx, "vps1", "root@203.0.113.12", "", true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Add(ctx, "vps2", "root@203.0.113.13", "", true); err != nil {
			t.Fatal(err)
		}
	}
	config := readTestFile(t, s.configPath())
	if strings.Count(config, includeLine) != 1 || !strings.HasPrefix(config, includeLine+"\n") {
		t.Fatalf("config:\n%s", config)
	}
	if !strings.HasSuffix(config, "Host other\n  User me\n") {
		t.Fatalf("user config lost: %q", config)
	}
	hostsFile := readTestFile(t, s.hostsPath())
	if strings.Count(hostsFile, "# burro-host vps1\n") != 1 || !strings.Contains(hostsFile, "203.0.113.12") {
		t.Fatalf("--force did not replace the block:\n%s", hostsFile)
	}
}

func TestAddKeepsOtherBlocksAsWritten(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	edited := "# my notes\n# burro-host a\nHost a\n  HostName 192.0.2.1\n  User u\n  ServerAliveInterval 30\n\n"
	writeTestFile(t, s.hostsPath(), edited, 0o600)
	if _, _, err := s.Add(ctx, "b", "u@192.0.2.2", "", false); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, s.hostsPath()); !strings.HasPrefix(got, edited) {
		t.Fatalf("edited block changed:\n%s", got)
	}
}

// acceptance: S1-07
func TestAddRefusesUserHosts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	userConfig := "Host vps1\n  HostName 198.51.100.4\n\nHost bare\n  User admin\n"
	writeTestFile(t, s.configPath(), userConfig, 0o644)

	_, _, err := s.Add(ctx, "vps1", "root@203.0.113.10", "", false)
	wantCode(t, err, output.ExitUsage)
	var coded *output.Error
	errors.As(err, &coded)
	if coded.Fix() != "reuse it with burro host add vps1 vps1, or pick another name" {
		t.Fatalf("fix = %q", coded.Fix())
	}
	_, _, err = s.Add(ctx, "bare", "root@203.0.113.10", "", true)
	wantCode(t, err, output.ExitUsage)
	if readTestFile(t, s.configPath()) != userConfig {
		t.Fatal("~/.ssh/config changed")
	}
	if _, err := os.Stat(s.hostsPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("hosts file written: %v", err)
	}

	h, _, err := s.Add(ctx, "vps1", "vps1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, s.hostsPath()); got != "# burro-host vps1 alias\n\n" {
		t.Fatalf("alias marker: %q", got)
	}
	// ssh fills in the local user name, since the alias sets none.
	if !strings.HasSuffix(h.Address(ctx), "@198.51.100.4") {
		t.Fatalf("alias address = %q", h.Address(ctx))
	}
	for _, bad := range []struct{ name, target, key string }{
		{"vps2", "vps1", ""},
		{"nosuch", "nosuch", ""},
		{"bare", "bare", "/tmp/key"},
	} {
		_, _, err := s.Add(ctx, bad.name, bad.target, bad.key, false)
		wantCode(t, err, output.ExitUsage)
	}
}

func TestAddRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, target := range []string{
		"@host", "root@", "root@-oProxyCommand=x", "root@host:0", "root@host:65536", "root@host:22x",
		"root@host:022", "root@ho st", "ro ot@host", "root@host\nHost *", "root@[::1", "root@[::1]x",
		"root@[zz::1]", "root@host:1:2", "root@[::1]:", "root@host:",
	} {
		_, _, err := s.Add(ctx, "vps1", target, "", false)
		wantCode(t, err, output.ExitUsage)
	}
	for _, key := range []string{"/no/such/key", "/tmp/a\nb", `/tmp/"key"`} {
		_, _, err := s.Add(ctx, "vps1", "root@192.0.2.1", key, false)
		wantCode(t, err, output.ExitUsage)
	}
	if _, err := os.Stat(s.hostsPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a rejected add wrote the hosts file")
	}
}

func TestUndo(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, undo, err := s.Add(ctx, "a", "u@192.0.2.1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.hostsPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("undo of the first block left the hosts file")
	}
	if _, _, err := s.Add(ctx, "a", "u@192.0.2.1", "", false); err != nil {
		t.Fatal(err)
	}
	before := readTestFile(t, s.hostsPath())
	_, undo, err = s.Add(ctx, "b", "u@192.0.2.2", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, s.hostsPath()) != before {
		t.Fatal("undo did not restore the hosts file")
	}
	if _, err := s.Lookup("b"); err == nil {
		t.Fatal("undone host still found")
	}
	if h, err := s.Lookup("a"); err != nil || h.Name() != "a" {
		t.Fatalf("Lookup(a) = %v", err)
	}
	_, err = s.Lookup("zzz")
	wantCode(t, err, output.ExitUsage)
}

// acceptance: S1-08
func TestWritesKeepModesAndSurviveCrash(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	writeTestFile(t, s.configPath(), "Host x\n  User y\n", 0o640)
	if _, _, err := s.Add(ctx, "a", "u@192.0.2.1", "", false); err != nil {
		t.Fatal(err)
	}
	modes := map[string]fs.FileMode{
		s.configPath():              0o640,
		s.hostsPath():               0o600,
		filepath.Dir(s.hostsPath()): 0o700 | fs.ModeDir,
	}
	for path, want := range modes {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode() != want {
			t.Errorf("%s: mode %v, want %v (%v)", path, fi.Mode(), want, err)
		}
	}
	writeTestFile(t, s.hostsPath(), readTestFile(t, s.hostsPath()), 0o604)
	if _, _, err := s.Add(ctx, "b", "u@192.0.2.2", "", false); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(s.hostsPath()); fi.Mode() != 0o604 {
		t.Errorf("hosts file mode %v after an edit, want 0604", fi.Mode())
	}

	before := readTestFile(t, s.hostsPath())
	s.rename = func(string, string) error { return errors.New("power cut") }
	_, _, err := s.Add(ctx, "c", "u@192.0.2.3", "", false)
	wantCode(t, err, output.ExitServer)
	if readTestFile(t, s.hostsPath()) != before {
		t.Fatal("a failed rename changed the hosts file")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(s.hostsPath()), ".burro.*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left: %v", leftovers)
	}

	// A fresh HOME gets the Include line in a new 0600 config.
	s2 := newTestStore(t)
	s2.rename = func(string, string) error { return errors.New("power cut") }
	if _, _, err := s2.Add(ctx, "c", "u@192.0.2.3", "", false); err == nil {
		t.Fatal("add passed with a failing rename")
	}
	s2.rename = os.Rename
	if _, _, err := s2.Add(ctx, "c", "u@192.0.2.3", "", false); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(s2.configPath()); fi.Mode() != 0o600 {
		t.Errorf("new config mode %v", fi.Mode())
	}
}

func TestWriteThroughSymlink(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	target := filepath.Join(s.home, "dotfiles", "ssh_config")
	writeTestFile(t, target, "Host x\n", 0o600)
	if err := os.MkdirAll(filepath.Dir(s.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.configPath()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Add(ctx, "a", "u@192.0.2.1", "", false); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(s.configPath()); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a file")
	}
	if got := readTestFile(t, target); got != includeLine+"\nHost x\n" {
		t.Fatalf("target: %q", got)
	}
}

func TestUnreadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	ctx := context.Background()
	s := newTestStore(t)
	writeTestFile(t, s.hostsPath(), "x", 0o000)
	if _, err := s.List(); err == nil {
		t.Error("List read an unreadable file")
	}
	if _, _, err := s.Add(ctx, "a", "u@192.0.2.1", "", false); err == nil {
		t.Error("Add read an unreadable file")
	}
	s2 := newTestStore(t)
	writeTestFile(t, s2.configPath(), "x", 0o000)
	if _, _, err := s2.Add(ctx, "a", "u@192.0.2.1", "", false); err == nil {
		t.Error("Add passed with an unreadable config")
	}
}

func TestParseHostsFile(t *testing.T) {
	data := "# header\n# burro-host a\nHost a\n  HostName h\n  User u\n  Port 2200\n  IdentityFile \"/k y\"\n\n# burro-host b alias\n\n# burro-host \n"
	f := parseHostsFile(data)
	if len(f.entries) != 2 || f.String() != data {
		t.Fatalf("round trip: %d entries\n%q", len(f.entries), f.String())
	}
	a := f.entries[0]
	if a.address() != "u@h:2200" || a.key != "/k y" || a.alias {
		t.Fatalf("entry a: %+v", a)
	}
	if !f.entries[1].alias || f.entries[1].address() != "" {
		t.Fatalf("entry b: %+v", f.entries[1])
	}
}
