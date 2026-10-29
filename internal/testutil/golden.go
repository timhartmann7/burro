package testutil

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files with the current output") //nolint:gochecknoglobals // test flags register at package init, before flag.Parse

// Golden compares got with testdata/golden/<name> at the module root. With
// -update it rewrites the file instead, so the diff shows up in review.
func Golden(tb testing.TB, name, got string) {
	tb.Helper()
	path := filepath.Join(moduleRoot(tb), "testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			tb.Fatalf("create golden folder: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			tb.Fatalf("write golden file: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read golden file: %v", err)
	}
	if got != string(want) {
		tb.Fatalf("output differs from %s\n--- got\n%s--- want\n%s", path, got, want)
	}
}
