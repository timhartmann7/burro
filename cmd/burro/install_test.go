package main_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	installTag = "v0.1.0"
	binName    = "burro"
)

// release writes a release of one client binary for this platform into a
// fresh folder, with the archive passed through tamper before it is served.
// checksums.txt always holds the sum of the untouched archive.
func release(t *testing.T, tamper func([]byte) []byte) string {
	t.Helper()
	bin, err := os.ReadFile(testutil.BuildBurro(t, "-ldflags", "-X main.version=0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	name := "burro_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	archive := testutil.TarGz(t, map[string][]byte{binName: bin, "LICENSE": []byte("license"), "NOTICE": []byte("notice")})
	dir := t.TempDir()
	for file, data := range map[string][]byte{
		name:            tamper(bytes.Clone(archive)),
		"checksums.txt": []byte(testutil.ChecksumLine([]byte("other"), "burro_plan9_amd64.tar.gz") + testutil.ChecksumLine(archive, name)),
	} {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func untouched(b []byte) []byte { return b }

// installEnv is a clean environment for install.sh: a fresh HOME and a PATH
// with the system tools only.
func installEnv(t *testing.T, extra ...string) (env []string, home string) {
	t.Helper()
	home = t.TempDir()
	return append([]string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}, extra...), home
}

// acceptance: S4-12
func TestInstallEndsWithWelcome(t *testing.T) {
	base := testutil.ServeRelease(t, release(t, untouched), installTag)
	env, home := installEnv(t, "BURRO_BASE_URL="+base, humanEnv, "NO_COLOR=1")
	r := testutil.RunInstall(t, env)
	platform := runtime.GOOS + "/" + runtime.GOARCH
	head := "downloading burro 0.1.0 for " + platform + "\n" +
		"checksum ok\n" +
		"installed to ~/.local/bin/burro\n" +
		"add to your shell profile: export PATH=\"$HOME/.local/bin:$PATH\"\n" +
		"\n"
	welcome, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "welcome.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != 0 || r.Stdout != head+string(welcome) || r.Stderr != "" {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.Code, r.Stdout, r.Stderr)
	}
	installed := filepath.Join(home, ".local", "bin")
	if got := listDir(t, installed); got != binName {
		t.Fatalf("install folder holds %q", got)
	}
	if got := listDir(t, home); got != ".local" {
		t.Fatalf("HOME holds %q after install", got)
	}
	if v := testutil.RunBurro(t, filepath.Join(installed, "burro"), "--version"); !strings.HasPrefix(v.Stdout, "burro 0.1.0 "+platform+" (script)") {
		t.Fatalf("installed binary: %q", v.Stdout)
	}

	// A folder already on PATH needs no hint, BURRO_VERSION picks the
	// release, and a second install replaces the binary in place.
	dir := filepath.Join(t.TempDir(), "bin")
	env, _ = installEnv(t, "BURRO_BASE_URL="+base, "BURRO_VERSION="+installTag, "BURRO_INSTALL_DIR="+dir)
	env[1] += ":" + dir
	for range 2 {
		r = testutil.RunInstall(t, env)
		if r.Code != 0 || !strings.Contains(r.Stdout, "installed to "+dir+"/burro\n\n") || strings.Contains(r.Stdout, "shell profile") {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.Code, r.Stdout, r.Stderr)
		}
	}
	if got := listDir(t, dir); got != binName {
		t.Fatalf("install folder holds %q", got)
	}
}

// acceptance: S4-04
func TestInstallRejectsTamperedArchive(t *testing.T) {
	tampered := release(t, func(b []byte) []byte {
		b[len(b)/2] ^= 0xff
		return b
	})
	base := testutil.ServeRelease(t, tampered, installTag)
	for _, existing := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "bin")
		if existing {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		env, _ := installEnv(t, "BURRO_BASE_URL="+base, "BURRO_INSTALL_DIR="+dir)
		r := testutil.RunInstall(t, env)
		if r.Code == 0 || !strings.Contains(r.Stderr, "does not match checksums.txt, nothing was installed") {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.Code, r.Stdout, r.Stderr)
		}
		if got := listDir(t, dir); got != "" {
			t.Fatalf("install folder holds %q", got)
		}
	}
}

func TestInstallRefuses(t *testing.T) {
	base := testutil.ServeRelease(t, release(t, untouched), installTag)
	noArchive := t.TempDir()
	noLine := release(t, untouched)
	for _, d := range []string{noArchive, noLine} {
		if err := os.WriteFile(filepath.Join(d, "checksums.txt"), []byte(testutil.ChecksumLine(nil, "x")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name string
		env  []string
		want string
	}{
		{"plain http", []string{"BURRO_BASE_URL=http://example.com/releases"}, "must start with https://"},
		{"lookalike loopback", []string{"BURRO_BASE_URL=http://127.0.0.1.example.com"}, "must start with https://"},
		{"bad version", []string{"BURRO_BASE_URL=" + base, "BURRO_VERSION=latest"}, "not a release version: latest"},
		{"unknown version", []string{"BURRO_BASE_URL=" + base, "BURRO_VERSION=v9.9.9"}, "can't download"},
		{"no archive", []string{"BURRO_BASE_URL=" + testutil.ServeRelease(t, noArchive, installTag)}, "can't download"},
		{"no checksum line", []string{"BURRO_BASE_URL=" + testutil.ServeRelease(t, noLine, installTag)}, "does not match checksums.txt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "bin")
			env, _ := installEnv(t, append(tt.env, "BURRO_INSTALL_DIR="+dir)...)
			r := testutil.RunInstall(t, env)
			if r.Code == 0 || !strings.Contains(r.Stderr, tt.want) || listDir(t, dir) != "" {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.Code, r.Stdout, r.Stderr)
			}
		})
	}
}

// listDir lists the names in dir, space-separated; a missing folder is
// empty.
func listDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}
