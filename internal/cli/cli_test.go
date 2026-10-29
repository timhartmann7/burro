package cli

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	helpFlag    = "--help"
	versionFlag = "--version"
	dev         = "dev"
)

func TestRunHelp(t *testing.T) {
	for _, args := range [][]string{nil, {}, {helpFlag}, {helpFlag, versionFlag}} {
		var stdout strings.Builder
		if err := New(nil, &stdout, nil, "").Run(args); err != nil {
			t.Fatalf("Run(%q) = %v", args, err)
		}
		testutil.Golden(t, "help.txt", stdout.String())
	}
}

func TestRunVersion(t *testing.T) {
	var stdout strings.Builder
	if err := New(nil, &stdout, nil, "0.3.1").Run([]string{versionFlag, helpFlag}); err != nil {
		t.Fatal(err)
	}
	if want := "burro 0.3.1 " + runtime.GOOS + "/" + runtime.GOARCH + "\n"; stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr strings.Builder
	err := New(nil, &stdout, nil, "").Run([]string{argJSON})
	if code := output.Report(&stderr, err); code != 4 {
		t.Fatalf("code = %d, want 4", code)
	}
	if want := "burro: unknown command \"--json\"\nfix: run burro --help\n"; stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

func TestRunWriteError(t *testing.T) {
	for _, args := range [][]string{{helpFlag}, {versionFlag}} {
		err := New(nil, brokenWriter{}, nil, "").Run(args)
		var coded *output.Error
		if err == nil || errors.As(err, &coded) || !strings.Contains(err.Error(), "broken pipe") {
			t.Fatalf("Run(%q) = %v, want the uncoded write error", args, err)
		}
	}
}

func TestRegistryDispatchAndHelp(t *testing.T) {
	var gotArgs []string
	app := New(nil, &strings.Builder{}, nil, "")
	app.commands = []command{
		{name: argSync, group: groupServer, summary: "copy the code to the server without a run"},
		{
			name: "run", brief: "run [pipeline]", group: groupRun, summary: "sync the code and start a run, returns at once",
			maxArgs: 1,
			run:     func(_ context.Context, p *parsed) error { gotArgs = p.args; return nil },
		},
		{name: "init", group: groupSetup, summary: "set up this project and the agent rules"},
	}
	if err := app.Run([]string{"run", "verify", argJSON}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotArgs, " ") != "verify" {
		t.Fatalf("run got args %q", gotArgs)
	}

	// Groups print in their fixed order whatever the registry order, and
	// every summary starts in one column.
	want := "\nSet up (once)\n" +
		"  init             set up this project and the agent rules\n" +
		"\nRun tests\n" +
		"  run [pipeline]   sync the code and start a run, returns at once\n" +
		"\nServer\n" +
		"  sync             copy the code to the server without a run\n"
	if got := app.help(); !strings.Contains(got, "Usage: burro <command> [flags]\n"+want+"\nExit codes") {
		t.Fatalf("help:\n%s\nwant groups:\n%s", got, want)
	}
}

func TestExitCodesWrap(t *testing.T) {
	tests := []struct {
		name   string
		labels []exitLabel
		want   string
	}{
		{name: "one", labels: []exitLabel{{0, "added"}}, want: "Exit codes  0 added\n"},
		{
			name:   "fits",
			labels: []exitLabel{{0, "added"}, {2, "server unreachable or host key unknown"}, {4, "name taken"}},
			want:   "Exit codes  0 added · 2 server unreachable or host key unknown · 4 name taken\n",
		},
		{
			name:   "wraps twice",
			labels: []exitLabel{{0, strings.Repeat("a", 60)}, {1, strings.Repeat("b", 60)}, {2, "c"}, {3, strings.Repeat("d", 60)}},
			want: "Exit codes  0 " + strings.Repeat("a", 60) + "\n" +
				"            1 " + strings.Repeat("b", 60) + " · 2 c\n" +
				"            3 " + strings.Repeat("d", 60) + "\n",
		},
		{
			// Column width counts the middle dot as one column, not two bytes.
			name:   "exactly 80 columns",
			labels: []exitLabel{{0, strings.Repeat("a", 60)}, {1, "b"}},
			want:   "Exit codes  0 " + strings.Repeat("a", 60) + " · 1 b\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodes(tt.labels); got != tt.want {
				t.Fatalf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestBuildVersion(t *testing.T) {
	tests := map[string]string{
		"(devel)":                            dev,
		"":                                   dev,
		"v0.1.0":                             "0.1.0",
		"v12.30.4":                           "12.30.4",
		"v0.0.0-20261029101500-1a2b3c4d5e6f": dev,
		"v0.1.0+dirty":                       dev,
		"v0.2.1-0.20261029101500-1a2b3c4d5e6f+dirty": dev,
		"v0.1.0-rc.1": dev,
		"0.1.0":       dev,
		"v0.1":        dev,
		"v0.1.0\n":    dev,
	}
	for in, want := range tests {
		if got := buildVersion(in); got != want {
			t.Errorf("buildVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveVersion(t *testing.T) {
	if got := resolveVersion("2.0.0"); got != "2.0.0" {
		t.Fatalf("linked version: got %q", got)
	}
	// A test binary is built from a checkout, never from a release tag.
	if got := resolveVersion(""); got != dev {
		t.Fatalf("build info version: got %q, want dev", got)
	}
}
