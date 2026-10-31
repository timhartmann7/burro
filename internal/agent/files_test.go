package agent_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/agent"
	"github.com/timhartmann7/burro/internal/output"
)

const (
	noMatch  = "nothing in the project folder"
	dashFile = "-dash.txt"
	actFiles = "files"
)

func filesCall(patterns ...string) []string {
	return append([]string{actFiles, argPath, appPath, "--"}, patterns...)
}

// writeFiles writes each file of files, a path inside dir or an absolute
// one, with its content.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := name
		if !filepath.IsAbs(name) {
			path = filepath.Join(dir, name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// artifacts is a project with test results, odd names and links: one link
// inside the results to a file out of the project, one to a folder out of
// it. It returns the home folder and that file.
func artifacts(t *testing.T) (home, outside string) {
	t.Helper()
	home, dir := project(t)
	outside = filepath.Join(home, "secret.txt")
	writeFiles(t, dir, map[string]string{
		"test-results/login/trace.zip":       "trace",
		"test-results/login/failed-1.png":    "\x89PNG",
		"test-results/cart/error-context.md": "# ctx",
		dashFile:                             "d",
		"odd name\nline.txt":                 "o",
		outside:                              "keep out",
	})
	if err := os.Symlink(outside, filepath.Join(dir, "test-results", "login", "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(dir, "up")); err != nil {
		t.Fatal(err)
	}
	return home, outside
}

func TestFiles(t *testing.T) {
	home, _ := artifacts(t)
	tests := []struct {
		name     string
		patterns []string
		files    string
		bytes    int64
	}{
		{name: "folder", patterns: []string{"test-results"}, files: "test-results/cart/error-context.md test-results/login/failed-1.png test-results/login/trace.zip", bytes: 14},
		{name: "glob", patterns: []string{"test-results/*/*.png"}, files: "test-results/login/failed-1.png", bytes: 4},
		{name: "overlap counts once", patterns: []string{"test-results/login", "test-results/*/trace.zip"}, files: "test-results/login/failed-1.png test-results/login/trace.zip", bytes: 9},
		{name: "dash", patterns: []string{dashFile}, files: dashFile, bytes: 1},
		{name: "newline", patterns: []string{"odd*"}, files: "odd name\nline.txt", bytes: 1},
	}
	for _, tt := range tests {
		reply := call(t, home, "", filesCall(tt.patterns...)...)
		if reply.Error != nil || reply.Pulled == nil || strings.Join(reply.Pulled.Files, " ") != tt.files || reply.Pulled.Bytes != tt.bytes {
			t.Fatalf("%s: %+v %+v", tt.name, reply.Pulled, reply.Error)
		}
	}
}

func TestFilesStayInProject(t *testing.T) {
	home, outside := artifacts(t)
	for _, bad := range []struct {
		pattern string
		msg     string
	}{
		{pattern: "../secret.txt", msg: noMatch + ` on the server matches "../secret.txt"`},
		{pattern: outside, msg: noMatch},
		{pattern: "up/secret.txt", msg: noMatch},
		{pattern: "up", msg: noMatch},
		{pattern: "test-results/[", msg: `pattern "test-results/[" is not valid`},
	} {
		failsWith(t, call(t, home, "", filesCall(bad.pattern)...).Error, bad.msg)
	}
	for _, args := range [][]string{{actFiles, argPath, appPath}, {actFiles, "x"}} {
		failsWith(t, call(t, home, "", args...).Error, "agent files: bad arguments")
	}
}

// failsWith checks that f is a usage failure whose message has msg.
func failsWith(t *testing.T, f *agent.Failure, msg string) {
	t.Helper()
	if f == nil || output.Code(f.Code) != output.ExitUsage || !strings.Contains(f.Message, msg) || f.Fix == "" {
		t.Fatalf("got %+v, want exit 4 with %q", f, msg)
	}
}

func TestFilesTooBig(t *testing.T) {
	home, dir := project(t)
	big := filepath.Join(dir, "build", "huge.bin")
	writeFiles(t, dir, map[string]string{big: ""})
	// A sparse file is big without using the disk.
	if err := os.Truncate(big, 257<<20); err != nil {
		t.Fatal(err)
	}
	failsWith(t, call(t, home, "", filesCall("build")...).Error, `"build" matches over 5000 files or 256 MiB`)
	many := map[string]string{}
	for i := range 5001 {
		many["many/f"+strconv.Itoa(i)] = ""
	}
	writeFiles(t, dir, many)
	failsWith(t, call(t, home, "", filesCall("many")...).Error, "over 5000 files")
}
