package hosts

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/testutil"
)

func TestQuote(t *testing.T) {
	tests := map[string]string{
		"":                 "''",
		"ls":               "ls",
		".burro/bin/burro": ".burro/bin/burro",
		"a b":              "'a b'",
		"it's":             `'it'\''s'`,
		"$HOME":            `'$HOME'`,
		"A=1":              `'A=1'`,
		"~/x":              `'~/x'`,
		"--max-lines":      "--max-lines",
	}
	for in, want := range tests {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
	if got := commandLine([]string{"printf", "%s\n", "a b"}); got != `printf '%s`+"\n"+`' 'a b'` {
		t.Errorf("commandLine = %q", got)
	}
}

// echoThroughShell runs the command line a remote login shell would get for
// bash -c 'printf %s "$1"' _ s, and returns what the inner bash printed.
func echoThroughShell(t *testing.T, s string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	line := commandLine([]string{"bash", "-c", `printf %s "$1"`, "_", s})
	out, err := exec.CommandContext(ctx, "bash", "-c", line).Output()
	if err != nil {
		t.Fatalf("bash -c %q: %v", line, err)
	}
	return string(out)
}

func FuzzRemoteQuote(f *testing.F) {
	for _, seed := range testutil.FuzzSeeds(f, "FuzzRemoteQuote") {
		f.Add(string(seed))
	}
	f.Fuzz(func(t *testing.T, s string) {
		if strings.ContainsRune(s, 0) {
			// argv can't carry NUL, so no quoting has to.
			return
		}
		if got := echoThroughShell(t, s); got != s {
			t.Fatalf("round trip of %q gave %q", s, got)
		}
	})
}
