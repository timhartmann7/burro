package agent_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/agent"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/runner"
)

// project makes a home folder with a project in it and returns both.
func project(t *testing.T) (home, dir string) {
	t.Helper()
	home = t.TempDir()
	dir = filepath.Join(home, "burro", "app")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func call(t *testing.T, home, stdin string, args ...string) *agent.Reply {
	t.Helper()
	var out strings.Builder
	if err := agent.New(strings.NewReader(stdin), &out, home, nil, "", runner.Exec, runner.Clean).Run(args); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("reply is not one JSON line: %q", out.String())
	}
	var reply agent.Reply
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q: %v", out.String(), err)
	}
	return &reply
}

// execCall is the argument list of one exec call with a command.
func execCall(path, lines string, argv ...string) []string {
	return append([]string{action, argPath, path, "--max-lines", lines, "--"}, argv...)
}

// scriptCall is the argument list of one exec call with a script on stdin.
func scriptCall(argv ...string) []string {
	return append([]string{action, "--path", appPath, "--max-lines", "9", "--file", "--"}, argv...)
}

const (
	appPath = "burro/app"
	action  = "exec"
)

func execOK(t *testing.T, reply *agent.Reply) *output.Exec {
	t.Helper()
	if reply.Error != nil || reply.Exec == nil {
		t.Fatalf("reply %+v, error %+v", reply.Exec, reply.Error)
	}
	return reply.Exec
}

func TestExecArgumentsByteForByte(t *testing.T) {
	home, _ := project(t)
	args := []string{"a b", "$HOME", "é ü \U0001F642", `x"y`, "it's", "`id`", "$(id)", "*", "", "-n", "a\nb", "\\"}
	reply := call(t, home, "", execCall(appPath, "60", append([]string{"printf", "[%s]\n"}, args...)...)...)
	var want strings.Builder
	for _, a := range args {
		fmt.Fprintf(&want, "[%s]\n", a)
	}
	if got := execOK(t, reply); got.Output != want.String() || got.Exit != 0 || got.Hidden != 0 {
		t.Fatalf("got %+v\nwant %q", got, want.String())
	}
}

func TestExecRunsInProjectFolder(t *testing.T) {
	home, dir := project(t)
	got := execOK(t, call(t, home, "", execCall(dir, "5", "pwd")...))
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got.Output) != resolved && strings.TrimSpace(got.Output) != dir {
		t.Fatalf("pwd = %q, want %q", got.Output, dir)
	}
}

func TestExecCutsAndKeepsFullLog(t *testing.T) {
	home, dir := project(t)
	got := execOK(t, call(t, home, "", execCall(appPath, "3", "bash", "-c", "seq 1 10; echo err >&2; exit 3")...))
	if got.Output != "1\n2\n3\n" || got.Hidden != 8 || got.Exit != 3 || got.Log != ".burro/exec/e1.log" {
		t.Fatalf("got %+v", got)
	}
	full, err := os.ReadFile(filepath.Join(dir, got.Log))
	if err != nil {
		t.Fatal(err)
	}
	if string(full) != "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\nerr\n" {
		t.Fatalf("log = %q", full)
	}
}

func TestExecKeepsLastTwentyLogs(t *testing.T) {
	home, dir := project(t)
	logs := filepath.Join(dir, ".burro", "exec")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	// Strays the agent must leave alone, and a gap in the numbers.
	for _, name := range []string{"notes.txt", "e5.log.bak", "e30.log"} {
		if err := os.WriteFile(filepath.Join(logs, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 25 {
		got := execOK(t, call(t, home, "", execCall(appPath, "1", "echo", fmt.Sprint(i))...))
		if want := fmt.Sprintf(".burro/exec/e%d.log", 31+i); got.Log != want {
			t.Fatalf("log %q, want %q", got.Log, want)
		}
	}
	names, err := filepath.Glob(filepath.Join(logs, "e*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 20 {
		t.Fatalf("%d logs kept, want 20: %v", len(names), names)
	}
	for _, keep := range []string{"notes.txt", "e5.log.bak", "e55.log", "e36.log"} {
		if _, err := os.Stat(filepath.Join(logs, keep)); err != nil {
			t.Errorf("%s removed: %v", keep, err)
		}
	}
	if _, err := os.Stat(filepath.Join(logs, "e35.log")); !errors.Is(err, os.ErrNotExist) {
		t.Error("e35.log kept")
	}
}

func TestExecScript(t *testing.T) {
	home, dir := project(t)
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{name: "no shebang runs in bash", script: "echo \"[$1]\" \"$#\" \"${BASH_VERSION:+bash}\"\n", want: "[two words] 1 bash\n"},
		{name: "shebang", script: "#!/bin/sh\nprintf '<%s>' \"$@\"\n", want: "<two words>"},
		{name: "quotes of every kind", script: "#!/bin/bash\necho 'single' \"double\" `echo back` $'ansi\\'s' \"$1\"\n", want: "single double back ansi's two words\n"},
		{name: "one byte", script: "#", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := execOK(t, call(t, home, tt.script, scriptCall("two words")...))
			if got.Output != tt.want || got.Exit != 0 {
				t.Fatalf("got %+v, want %q", got, tt.want)
			}
		})
	}
	scripts, err := filepath.Glob(filepath.Join(dir, ".burro", "exec", "script-*"))
	if err != nil || len(scripts) != 0 {
		t.Fatalf("scripts left behind: %v %v", scripts, err)
	}
}

func TestExecExitCodes(t *testing.T) {
	home, _ := project(t)
	tests := map[string]int{
		"exit 0":              0,
		"exit 1":              1,
		"exit 42":             42,
		"kill -TERM $$":       143,
		"nosuchcommand-burro": 127,
	}
	for script, want := range tests {
		got := execOK(t, call(t, home, "", execCall(appPath, "5", "bash", "-c", script)...))
		if got.Exit != want {
			t.Errorf("%s: exit %d, want %d", script, got.Exit, want)
		}
	}
}

func TestExecFailures(t *testing.T) {
	home, dir := project(t)
	tests := []struct {
		name string
		args []string
		code output.Code
		msg  string
	}{
		{name: "missing folder", args: execCall("burro/none", "5", "true"), code: output.ExitServer, msg: "~/burro/none does not exist"},
		{name: "missing absolute folder", args: execCall("/no/such/dir", "5", "true"), code: output.ExitServer, msg: "/no/such/dir does not exist"},
		{name: "no command", args: execCall(appPath, "5"), code: output.ExitUsage},
		{name: "no path", args: execCall("", "5", "true"), code: output.ExitUsage},
		{name: "bad flag", args: []string{action, "--nope"}, code: output.ExitUsage},
		{name: "zero lines", args: execCall(appPath, "0", "true"), code: output.ExitUsage},
		{name: "unknown call", args: []string{"begin"}, code: output.ExitServer, msg: "unknown agent call"},
		{name: "no call", args: nil, code: output.ExitServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := call(t, home, "", tt.args...)
			if reply.Exec != nil || reply.Error == nil || output.Code(reply.Error.Code) != tt.code || reply.Error.Fix == "" ||
				!strings.Contains(reply.Error.Message, tt.msg) {
				t.Fatalf("got %+v", reply.Error)
			}
		})
	}

	// A file where the log folder should be.
	if err := os.WriteFile(filepath.Join(dir, ".burro"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if reply := call(t, home, "", execCall(appPath, "5", "true")...); reply.Error == nil {
		t.Fatal("exec passed without a log folder")
	}
}

func TestReplyWriteError(t *testing.T) {
	err := agent.New(strings.NewReader(""), brokenWriter{}, t.TempDir(), nil, "", runner.Exec, runner.Clean).Run(nil)
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("got %v", err)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
