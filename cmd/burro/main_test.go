package main_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/timhartmann7/burro/internal/testutil"
)

const helpFlag = "--help"

// acceptance: S0-05
func TestVersion(t *testing.T) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	tests := []struct {
		name  string
		flags []string
		want  string
	}{
		{name: "plain build", want: "burro dev " + platform + "\n"},
		{name: "ldflags", flags: []string{"-ldflags", "-X main.version=1.4.2"}, want: "burro 1.4.2 " + platform + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := testutil.RunBurro(t, testutil.BuildBurro(t, tt.flags...), "--version")
			if got.Code != 0 || got.Stdout != tt.want || got.Stderr != "" {
				t.Fatalf("got exit %d stdout %q stderr %q; want exit 0 stdout %q", got.Code, got.Stdout, got.Stderr, tt.want)
			}
		})
	}
}

// acceptance: S0-06
func TestUnknownCommand(t *testing.T) {
	bin := testutil.BuildBurro(t)
	tests := []struct {
		arg  string
		want string
	}{
		{arg: "nosuchcommand", want: `"nosuchcommand"`},
		{arg: "--nosuchflag", want: `"--nosuchflag"`},
		{arg: "", want: `""`},
		{arg: "a\"b\nc", want: `"a\"b\nc"`},
		{arg: "--versionx", want: `"--versionx"`},
		{arg: "-h", want: `"-h"`},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			wantErr := "burro: unknown command " + tt.want + "\nfix: run burro --help\n"
			for _, args := range [][]string{{tt.arg}, {tt.arg, helpFlag}} {
				got := testutil.RunBurro(t, bin, args...)
				if got.Code != 4 || got.Stdout != "" || got.Stderr != wantErr {
					t.Fatalf("burro %q: exit %d stdout %q stderr %q; want exit 4, empty stdout, stderr %q",
						args, got.Code, got.Stdout, got.Stderr, wantErr)
				}
			}
		})
	}
}

// acceptance: S0-07
func TestHelp(t *testing.T) {
	bin := testutil.BuildBurro(t)
	for _, args := range [][]string{{helpFlag}, {}, {helpFlag, "extra"}} {
		got := testutil.RunBurro(t, bin, args...)
		if got.Code != 0 || got.Stderr != "" {
			t.Fatalf("burro %q: exit %d stderr %q; want exit 0 and empty stderr", args, got.Code, got.Stderr)
		}
		testutil.Golden(t, "help.txt", got.Stdout)
		checkHelpShape(t, got.Stdout)
	}
}

func checkHelpShape(t *testing.T, help string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(help, "\n"), "\n")
	if len(lines) > 35 {
		t.Fatalf("help has %d lines, want at most 35", len(lines))
	}
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > 80 {
			t.Fatalf("help line is %d columns, want at most 80: %q", n, l)
		}
	}
	for _, want := range []string{
		"burro · run your coding agent's tests on your own server\n",
		"Exit codes  0 passed · 1 tests failed · 2 server problem · 3 still running\n" +
			"            4 bad config or flags · 5 another run is active\n",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help lacks %q:\n%s", want, help)
		}
	}
}

// acceptance: S0-12
func TestMainIsWiringOnly(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "\n"); n >= 40 {
		t.Fatalf("main.go has %d lines, want under 40", n)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name != "main" {
			t.Errorf("main.go declares func %s; logic belongs in internal/", fn.Name.Name)
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit, *ast.GoStmt:
			t.Errorf("main.go has a %T at offset %d; branching belongs in internal/", n, n.Pos())
		}
		return true
	})
}
