package output_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/mode"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	kindDoctor = "doctor"
	kindHosts  = "hosts"
	kindExec   = "exec"
	kindSync   = "sync"
	ab         = "a\nb\n"
)

func modes(t *testing.T) map[string]*mode.Mode {
	t.Helper()
	m := map[string]*mode.Mode{}
	for _, name := range []string{"agent", "human"} {
		var err error
		if m[name], err = mode.New(name, func(string) string { return "" }, false); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func doctorChecks() []output.Check {
	return []output.Check{
		{Name: "ssh", OK: true, Detail: "82ms round trip"},
		{Name: "rsync", Detail: "not found", Hint: "install rsync on the server, for example apt install rsync"},
		{Name: "bash", OK: true, Detail: "5.2.37"},
		{Name: "docker", OK: true, Detail: "not found, needed only if steps use docker"},
		{Name: "disk", OK: true, Detail: "41.2 GB free in /home/burro"},
		{Name: "burro", OK: true, Detail: "0.1.0"},
	}
}

// render prints one result through every printer method and returns stdout.
func render(t *testing.T, kind string, asJSON bool, m *mode.Mode) string {
	t.Helper()
	var b strings.Builder
	p := output.NewPrinter(&b, asJSON, m)
	var err error
	switch kind {
	case kindDoctor:
		err = p.Doctor(doctorChecks())
	case kindHosts:
		err = p.Hosts([]output.Host{
			{Name: "vps1", Address: "root@203.0.113.10:2222", Version: "0.1.0"},
			{Name: "build-box", Address: "ci@198.51.100.7", Version: "not installed"},
			{Name: "old", Address: "old", Version: "unreachable"},
		})
	case "hosts-empty":
		err = p.Hosts(nil)
	case kindExec:
		err = p.Exec(output.Exec{Output: "line 1\nline 2\n", Hidden: 812, Log: ".burro/exec/e7.log", Exit: 127})
	case "exec-short":
		err = p.Exec(output.Exec{Output: "no newline at the end", Log: ".burro/exec/e1.log", Exit: 1})
	case kindSync:
		err = p.Sync(output.Sync{Tree: shortTree, Changed: 12, Took: 1430 * time.Millisecond})
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestRenderGolden(t *testing.T) {
	for _, kind := range []string{kindDoctor, kindHosts, "hosts-empty", kindExec, "exec-short", kindSync} {
		for name, m := range modes(t) {
			testutil.Golden(t, kind+"."+name+".txt", render(t, kind, false, m))
		}
		testutil.Golden(t, kind+".json", render(t, kind, true, modes(t)["human"]))
	}
}

// acceptance: S1-15
func TestModesShareText(t *testing.T) {
	m := modes(t)
	for _, kind := range []string{kindDoctor, kindHosts, kindExec, kindSync} {
		agent, human := render(t, kind, false, m["agent"]), render(t, kind, false, m["human"])
		if testutil.HasANSI(agent) {
			t.Errorf("%s: agent mode printed ANSI codes: %q", kind, agent)
		}
		if testutil.StripANSI(human) != agent {
			t.Errorf("%s: human text without colors differs from agent text:\n%s\n%s", kind, human, agent)
		}
		if testutil.HasANSI(render(t, kind, true, m["human"])) {
			t.Errorf("%s: JSON carries ANSI codes", kind)
		}
	}
	if !testutil.HasANSI(render(t, kindDoctor, false, m["human"])) {
		t.Error("human doctor output has no color")
	}
}

func TestHead(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		lines  int
		bytes  int
		head   string
		hidden int
	}{
		{name: "empty", in: "", lines: 3, bytes: 100},
		{name: "fits", in: ab, lines: 3, bytes: 100, head: ab},
		{name: "exact", in: "a\nb\nc\n", lines: 3, bytes: 100, head: "a\nb\nc\n"},
		{name: "cut", in: "a\nb\nc\nd\ne\n", lines: 2, bytes: 100, head: ab, hidden: 3},
		{name: "last line without newline", in: "a\nb\nc", lines: 2, bytes: 100, head: ab, hidden: 1},
		{name: "partial line shown", in: "a\nb", lines: 5, bytes: 100, head: "a\nb"},
		{name: "byte cap", in: "aaaa\nb\ncc\n", lines: 10, bytes: 6, head: "aaaa\n", hidden: 2},
		{name: "byte cap keeps one head", in: "aaaa\nbbbbbbbb\nc\n", lines: 10, bytes: 7, head: "aaaa\n", hidden: 2},
		{name: "blank lines count", in: "\n\n\n", lines: 1, bytes: 100, head: "\n", hidden: 2},
		{name: "binary bytes", in: "\x00\xff\n\x1b[31m\n", lines: 1, bytes: 100, head: "\x00\xff\n", hidden: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			head, hidden, err := output.Head(strings.NewReader(tt.in), tt.lines, tt.bytes)
			if err != nil || head != tt.head || hidden != tt.hidden {
				t.Fatalf("Head = %q, %d, %v; want %q, %d", head, hidden, err, tt.head, tt.hidden)
			}
		})
	}
	if _, _, err := output.Head(failingReader{}, 1, 1); err == nil {
		t.Fatal("Head swallowed a read error")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestExecHint(t *testing.T) {
	tests := []struct {
		hidden int
		want   string
	}{
		{hidden: 45, want: "+45 lines hidden · full output: burro exec -- tail -n 45 .burro/exec/e3.log\n"},
		{hidden: 200, want: "+200 lines hidden · full output: burro exec -- tail -n 200 .burro/exec/e3.log\n"},
		{hidden: 812, want: "+812 lines hidden · full output: burro exec -- tail -n 200 .burro/exec/e3.log\n"},
	}
	for _, tt := range tests {
		var b strings.Builder
		if err := output.NewPrinter(&b, false, modes(t)["agent"]).Exec(output.Exec{Hidden: tt.hidden, Log: ".burro/exec/e3.log"}); err != nil {
			t.Fatal(err)
		}
		if b.String() != tt.want {
			t.Errorf("hidden %d: got %q, want %q", tt.hidden, b.String(), tt.want)
		}
	}
}

func TestExecExitLine(t *testing.T) {
	for code, want := range map[int]string{0: "x\n", 1: "x\n", 2: "x\nexit 2\n", 143: "x\nexit 143\n"} {
		var b strings.Builder
		if err := output.NewPrinter(&b, false, modes(t)["agent"]).Exec(output.Exec{Output: "x\n", Exit: code}); err != nil {
			t.Fatal(err)
		}
		if b.String() != want {
			t.Errorf("exit %d: got %q, want %q", code, b.String(), want)
		}
	}
}

func TestSyncOneFile(t *testing.T) {
	var b strings.Builder
	if err := output.NewPrinter(&b, false, modes(t)["agent"]).Sync(output.Sync{Tree: "abcdef1", Changed: 1, Took: 75 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if want := "tree abcdef1 · 1 file changed · 1m15s\n"; b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func TestDuration(t *testing.T) {
	tests := map[time.Duration]string{
		0:                                     "0.0s",
		430 * time.Millisecond:                "0.4s",
		9940 * time.Millisecond:               "9.9s",
		12 * time.Second:                      "12s",
		59*time.Second + 999*time.Millisecond: "59s",
		time.Minute:                           "1m",
		3*time.Minute + 10*time.Second:        "3m10s",
		22 * time.Minute:                      "22m",
		time.Hour + 5*time.Minute + 59*time.Second: "1h05m",
		26 * time.Hour: "26h00m",
	}
	for d, want := range tests {
		if got := output.Duration(d); got != want {
			t.Errorf("Duration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestPrinterWriteError(t *testing.T) {
	p := output.NewPrinter(brokenWriter{}, false, modes(t)["agent"])
	for name, err := range map[string]error{
		kindDoctor: p.Doctor(doctorChecks()),
		kindHosts:  p.Hosts(nil),
		kindExec:   p.Exec(output.Exec{Output: "x"}),
		kindSync:   p.Sync(output.Sync{}),
	} {
		if err == nil || !strings.Contains(err.Error(), "broken pipe") {
			t.Errorf("%s: got %v, want the write error", name, err)
		}
	}
	if err := output.NewPrinter(brokenWriter{}, true, modes(t)["agent"]).Sync(output.Sync{}); err == nil {
		t.Error("json: write error lost")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
