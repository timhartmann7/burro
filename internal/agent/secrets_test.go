package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/report"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	tokenValue = "s3cret-token-value"
	actFails   = "failures"
	actClean   = "clean"
)

func secretsJSON(t *testing.T, secrets map[string]string) string {
	t.Helper()
	values := map[string][]byte{}
	for k, v := range secrets {
		values[k] = []byte(v)
	}
	data, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// acceptance: S3-09 S3-10
func TestStartWritesSecrets(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("300"))
	proj := report.Open(s.dir)
	// Leftovers of a dead run of this project go, another project's stay.
	dead := filepath.Join(s.secrets, proj.KeyPrefix()+"r7")
	other := filepath.Join(s.secrets, "burro-00000000-r1")
	for _, d := range []string{dead, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s.stdin = secretsJSON(t, map[string]string{"token": tokenValue + "\n", "npmrc": "//registry/:_authToken=abcdef"})
	started := s.start("slow")
	s.stdin = ""
	folder := filepath.Join(s.secrets, proj.Key(started.Run))
	data, err := os.ReadFile(filepath.Join(folder, "token"))
	if err != nil || string(data) != tokenValue+"\n" || mode(t, folder) != 0o700 || mode(t, filepath.Join(folder, "token")) != 0o600 {
		t.Fatalf("token %q, %v, folder %v", data, err, mode(t, folder))
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead run's secrets kept: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other project's secrets gone: %v", err)
	}
	// exec output and its log on disk mask the secrets of the live run.
	out := execOK(t, s.ok(execCall(appPath, "9", "sh", "-c", "cat \"$1\"/*; echo", "sh", folder)...))
	if strings.Contains(out.Output, tokenValue) || strings.Contains(out.Output, "abcdef") || strings.Count(out.Output, report.Mask) != 2 {
		t.Fatalf("exec output %q", out.Output)
	}
	logged, err := os.ReadFile(filepath.Join(s.dir, out.Log))
	if err != nil || strings.Contains(string(logged), tokenValue) {
		t.Fatalf("exec log %q, %v", logged, err)
	}
	s.fails(output.ExitBusy, actClean, argPath, appPath)
	s.ok(actCancel, argPath, appPath)
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("secrets kept after cancel: %v", err)
	}
	// Without a live run, exec has nothing to mask and prints as it is.
	if out := execOK(t, s.ok(execCall(appPath, "9", "echo", tokenValue)...)); out.Output != tokenValue+"\n" {
		t.Fatalf("exec without a run %q", out.Output)
	}
}

func TestStartRejectsSecrets(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("0"))
	start := []string{actStart, argPath, appPath, "--tree", fullTree, pipeline}
	for stdin, code := range map[string]output.Code{
		"{broken":                         output.ExitServer,
		`{"../x":"YQ=="}`:                 output.ExitUsage,
		strings.Repeat(" ", 8<<20) + "{}": output.ExitUsage,
	} {
		s.stdin = stdin
		s.fails(code, start...)
	}
	if got := s.runs(); len(got) != 0 {
		t.Fatalf("refused secrets left runs %v", got)
	}
}

func TestFailuresAndClean(t *testing.T) {
	s := newServer(t, testutil.BuildBurro(t), toml("0"))
	s.start(pipeline)
	s.done(run1)
	sum := s.ok(actFails, argPath, appPath).Summary
	if sum.Run != run1 || len(sum.Steps[1].Tail) != 30 || sum.Steps[1].More != "burro log r1 --step e2e" || sum.Failures == nil {
		t.Fatalf("failures %+v", sum)
	}
	cleaned := s.ok(actClean, argPath, appPath, run1).Cleaned
	if cleaned == nil || cleaned.Run != run1 || cleaned.Secrets {
		t.Fatalf("clean %+v", cleaned)
	}
	s.fails(output.ExitUsage, actClean, argPath, appPath, "r5")
	s.fails(output.ExitUsage, actFails, argPath, appPath, "r5")
	s.fails(output.ExitUsage, actClean, argPath, appPath, run1, "r2")
}
