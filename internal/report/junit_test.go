package report

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/testutil"
)

// fixtureRoot is the project folder the fixture reports were made in.
const fixtureRoot = "/app"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "junit", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// describeJUnit prints a parsed report the way the golden files hold it.
func describeJUnit(r *junit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "total %d · failed %d · skipped %d\n", r.total, r.failed, r.skipped)
	for _, c := range r.cases {
		fmt.Fprintf(&b, "%s:%d  %s\n  message: %s\n  frame: %s\n", c.File, c.Line, c.Name, c.Message, c.Frame)
	}
	return b.String()
}

// checkCase fails on a frame outside the project, or on a case without a
// message or a project file.
func checkCase(t *testing.T, c *Case) {
	t.Helper()
	for _, part := range []string{"node_modules", "site-packages", ".venv", "/usr/", "jest-circus", "java.base", "Assertions.java", "testing.go"} {
		if strings.Contains(c.Frame, part) {
			t.Errorf("%s: frame %q is not a project file", c.Name, c.Frame)
		}
	}
	if c.Message == "" || c.File == "" || strings.HasPrefix(c.File, "/") {
		t.Errorf("case %+v lacks a message or a project file", c)
	}
}

// acceptance: S3-01 S3-03
func TestJUnitFixtures(t *testing.T) {
	for _, name := range []string{"vitest", "playwright", "jest", "pytest", "gotestsum", "nextest", "phpunit", "surefire"} {
		t.Run(name, func(t *testing.T) {
			r, err := parseJUnit(bytes.NewReader(fixture(t, name+".xml")), fixtureRoot)
			if err != nil {
				t.Fatal(err)
			}
			if r.total < 3 || r.failed < 2 || len(r.cases) != r.failed {
				t.Fatalf("total %d, failed %d, %d cases", r.total, r.failed, len(r.cases))
			}
			for i := range r.cases {
				checkCase(t, &r.cases[i])
			}
			testutil.Golden(t, "junit-"+name+".txt", describeJUnit(r))
		})
	}
}

// acceptance: S3-04
func TestJUnitBroken(t *testing.T) {
	for _, name := range []string{"broken-truncated", "broken-empty", "broken-invalid", "broken-wrong-root"} {
		t.Run(name, func(t *testing.T) {
			r, err := parseJUnit(bytes.NewReader(fixture(t, name+".xml")), fixtureRoot)
			if !errors.Is(err, errUnreadable) || r != nil {
				t.Fatalf("got %v, %v; want report unreadable", r, err)
			}
		})
	}
}

// acceptance: S3-04
func FuzzJUnit(f *testing.F) {
	for _, seed := range testutil.FuzzSeeds(f, "FuzzJUnit") {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := parseJUnit(bytes.NewReader(data), fixtureRoot)
		if err != nil {
			if !errors.Is(err, errUnreadable) || r != nil {
				t.Fatalf("got %v, %v; want entries or report unreadable", r, err)
			}
			return
		}
		if r.failed != len(r.cases) || r.failed+r.skipped > r.total {
			t.Fatalf("counts %+v", r)
		}
		for _, c := range r.cases {
			if len(c.Message) > maxMessage {
				t.Fatalf("message of %d bytes", len(c.Message))
			}
		}
	})
}
