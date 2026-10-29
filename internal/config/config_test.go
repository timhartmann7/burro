package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/config"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	vps1      = "vps1"
	fieldHost = "host"
	fieldPath = "path"
	echoOK    = "echo ok\n"
	echoTop   = "echo top\n"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		toml string
		host string
		dir  string
	}{
		{name: "default path", toml: `host = "vps1"`, host: vps1, dir: "burro/myapp"},
		{name: "home path", toml: "host = \"vps1\"\npath = \"~/work/app\"", host: vps1, dir: "work/app"},
		{name: "absolute path", toml: "host = \"vps1\"\npath = \"/srv/app\"", host: vps1, dir: "/srv/app"},
		{name: "trailing slash", toml: "host = \"vps1\"\npath = \"/srv/app/\"", host: vps1, dir: "/srv/app"},
		{name: "spaces in path", toml: "host = \"a_b-1\"\npath = \"~/my projects/app\"", host: "a_b-1", dir: "my projects/app"},
		{name: "comments", toml: "# server\nhost = \"vps1\" # trailing", host: vps1, dir: "burro/myapp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := config.Parse([]byte(tt.toml), "myapp")
			if err != nil {
				t.Fatal(err)
			}
			if c.Host() != tt.host || c.Dir() != tt.dir {
				t.Fatalf("got host %q dir %q, want %q %q", c.Host(), c.Dir(), tt.host, tt.dir)
			}
		})
	}
}

// acceptance: S1-18
func TestParseRejects(t *testing.T) {
	tests := []struct {
		name  string
		toml  string
		field string
	}{
		{name: "unknown key", toml: "host = \"vps1\"\nhots = \"vps2\"", field: `"hots"`},
		{name: "unknown table", toml: "host = \"vps1\"\n[steps.unit]\nrun = \"x\"", field: `"steps"`},
		{name: "missing host", toml: `path = "~/x"`, field: fieldHost},
		{name: "empty host", toml: `host = ""`, field: fieldHost},
		{name: "upper case host", toml: `host = "VPS1"`, field: fieldHost},
		{name: "host with space", toml: `host = "vps 1"`, field: fieldHost},
		{name: "host with newline", toml: `host = "vps1\nHost *"`, field: fieldHost},
		{name: "host starting with dash", toml: `host = "-oProxyCommand=x"`, field: fieldHost},
		{name: "host too long", toml: `host = "` + strings.Repeat("a", 64) + `"`, field: fieldHost},
		{name: "host not a string", toml: `host = 5`, field: fieldHost},
		{name: "path not a string", toml: "host = \"vps1\"\npath = [\"/a\"]", field: fieldPath},
		{name: "relative path", toml: "host = \"vps1\"\npath = \"burro/app\"", field: fieldPath},
		{name: "dot dot", toml: "host = \"vps1\"\npath = \"~/burro/../../etc\"", field: fieldPath},
		{name: "dot", toml: "host = \"vps1\"\npath = \"/srv/./app\"", field: fieldPath},
		{name: "double slash", toml: "host = \"vps1\"\npath = \"/srv//app\"", field: fieldPath},
		{name: "bare home", toml: "host = \"vps1\"\npath = \"~/\"", field: fieldPath},
		{name: "root", toml: "host = \"vps1\"\npath = \"/\"", field: fieldPath},
		{name: "other user's home", toml: "host = \"vps1\"\npath = \"~root/app\"", field: fieldPath},
		{name: "newline in path", toml: "host = \"vps1\"\npath = \"/srv/a\\nb\"", field: fieldPath},
		{name: "syntax", toml: "host = \"vps1", field: "line 1"},
		{name: "duplicate key", toml: "host = \"a\"\nhost = \"b\"", field: "line 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Parse([]byte(tt.toml), "myapp")
			msg, fix := usageError(t, err)
			if !strings.Contains(msg+" "+fix, tt.field) {
				t.Fatalf("error %q / fix %q doesn't name %s", msg, fix, tt.field)
			}
		})
	}
}

func usageError(t *testing.T, err error) (msg, fix string) {
	t.Helper()
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != output.ExitUsage || coded.Fix() == "" {
		t.Fatalf("got %v, want exit 4 with a fix line", err)
	}
	return err.Error(), coded.Fix()
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	_, err := config.Load(root)
	if msg, _ := usageError(t, err); !strings.Contains(msg, "no burro.toml") {
		t.Fatalf("missing file: %q", msg)
	}
	if err := os.WriteFile(filepath.Join(root, config.File), []byte("host = \"vps1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(root)
	if err != nil || c.Dir() != "burro/"+filepath.Base(root) {
		t.Fatalf("Load = %v, %v", c, err)
	}
	if err := os.Chmod(filepath.Join(root, config.File), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(root); os.Geteuid() != 0 {
		usageError(t, err)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "vps1", "0", "a-b_c", strings.Repeat("z", 63)} {
		if err := config.ValidName(fieldHost, ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "_a", "A", "a.b", "a b", "a/b", strings.Repeat("z", 64), "é", "a\n"} {
		if err := config.ValidName("step", bad); err == nil {
			t.Errorf("%q accepted", bad)
		} else {
			usageError(t, err)
		}
	}
}

func TestReadInRoot(t *testing.T) {
	root := testutil.GitRepo(t, map[string]string{"scripts/check.sh": echoOK, "top.sh": echoTop})
	outside := filepath.Join(t.TempDir(), "outside.sh")
	if err := os.WriteFile(outside, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.sh")); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "scripts")
	tests := []struct {
		name, dir, file, want string
	}{
		{name: "relative", dir: root, file: "scripts/check.sh", want: echoOK},
		{name: "from a subfolder", dir: sub, file: "check.sh", want: echoOK},
		{name: "up and back in", dir: sub, file: "../top.sh", want: echoTop},
		{name: "absolute inside", dir: "/", file: filepath.Join(root, "top.sh"), want: echoTop},
	}
	for _, tt := range tests {
		data, err := config.ReadInRoot(root, tt.dir, tt.file)
		if err != nil || string(data) != tt.want {
			t.Errorf("%s: got %q, %v", tt.name, data, err)
		}
	}
	for name, file := range map[string]string{
		"dot dot":          "../" + filepath.Base(root) + "-other/x",
		"absolute outside": outside,
		"symlink out":      "escape.sh",
		"missing":          "nope.sh",
		"folder":           "scripts",
	} {
		_, err := config.ReadInRoot(root, root, file)
		if msg, _ := usageError(t, err); strings.Contains(msg, "TOPSECRET") {
			t.Errorf("%s: error leaks content: %q", name, msg)
		}
	}
	if _, err := config.ReadInRoot(filepath.Join(root, "missing"), root, "top.sh"); err == nil {
		t.Error("a missing root passed")
	}
}

func FuzzConfigParse(f *testing.F) {
	for _, seed := range testutil.FuzzSeeds(f, "FuzzConfigParse") {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := config.Parse(data, "app")
		if err == nil {
			if config.ValidName(fieldHost, c.Host()) != nil || c.Dir() == "" {
				t.Fatalf("accepted an invalid config: %q", data)
			}
			return
		}
		usageError(t, err)
	})
}
