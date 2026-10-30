//go:build integration

package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/testutil"
)

// snapshotVersion is the version make snapshot linked into dist/.
func snapshotVersion(t *testing.T, dist string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dist, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &meta); err != nil || meta.Version == "" {
		t.Fatalf("dist/metadata.json: %q %v", data, err)
	}
	return meta.Version
}

// untar returns the files at the root of a tar.gz archive.
func untar(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	r := tar.NewReader(gz)
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = data
	}
}

// checkArchive fails unless the client archive at path has its sum in
// checksums.txt, holds only burro, LICENSE and NOTICE at its root, and its
// burro embeds every server binary.
func checkArchive(t *testing.T, path string, sums []byte, server map[string][]byte) {
	t.Helper()
	name := filepath.Base(path)
	archive, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sums, []byte(testutil.ChecksumLine(archive, name))) {
		t.Errorf("checksums.txt lacks the sum of %s", name)
	}
	files := untar(t, path)
	if got := slices.Sorted(maps.Keys(files)); strings.Join(got, " ") != "LICENSE NOTICE burro" {
		t.Errorf("%s holds %q, want burro, LICENSE and NOTICE at its root", name, got)
	}
	for arch, bin := range server {
		if !bytes.Contains(files[binaryName], bin) {
			t.Errorf("the client in %s doesn't embed the linux/%s server binary", name, arch)
		}
	}
}

// acceptance: S4-01
func TestSnapshotRelease(t *testing.T) {
	dist := testutil.Dist(t)
	version := snapshotVersion(t, dist)
	sums, err := os.ReadFile(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	server := map[string][]byte{}
	for _, arch := range []string{"amd64", "arm64"} {
		data, err := os.ReadFile(filepath.Join("..", "serverbin", "bin", "burro-linux-"+arch))
		if err != nil || !bytes.Contains(data, []byte(version)) {
			t.Fatalf("server binary for %s of version %s: %v", arch, version, err)
		}
		server[arch] = data
	}
	var names []string
	for _, platform := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		name := "burro_" + platform + ".tar.gz"
		names = append(names, name)
		checkArchive(t, filepath.Join(dist, name), sums, server)
	}
	script, err := os.ReadFile(filepath.Join(dist, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if mine, _ := os.ReadFile(filepath.Join("..", "..", "install.sh")); !bytes.Equal(script, mine) ||
		!bytes.Contains(sums, []byte(testutil.ChecksumLine(script, "install.sh"))) {
		t.Error("dist/install.sh differs from install.sh or has no checksum")
	}
	if n := strings.Count(string(sums), "\n"); n != len(names)+1 {
		t.Errorf("checksums.txt has %d lines, want %d", n, len(names)+1)
	}
	cask, err := os.ReadFile(filepath.Join(dist, "homebrew", "Casks", "burro.rb"))
	if err != nil || !bytes.Contains(cask, []byte(`"com.apple.quarantine"`)) || !bytes.Contains(cask, []byte("Run burro to get started.")) {
		t.Errorf("cask: %v\n%s", err, cask)
	}
}

// journey runs the burro a user installed, the way they type it.
type journey struct {
	t   *testing.T
	bin string
	env []string
}

func (j *journey) burro(dir string, args ...string) testutil.Result {
	j.t.Helper()
	r := testutil.RunBurroIn(j.t, j.bin, dir, j.env, args...)
	if r.Code != 0 {
		j.t.Fatalf("burro %q: exit %d\nstdout:\n%s\nstderr:\n%s", args, r.Code, r.Stdout, r.Stderr)
	}
	return r
}

// shows fails the test unless the output the user just read holds the
// command they type next.
func (j *journey) shows(out, command string) {
	j.t.Helper()
	if !strings.Contains(out, command) {
		j.t.Fatalf("the user can't find %q in what burro printed:\n%s", command, out)
	}
}

// acceptance: S4-17
func TestJourney(t *testing.T) {
	dist := testutil.Dist(t)
	version := snapshotVersion(t, dist)
	srv := testutil.StartServer(t)
	installDir := filepath.Join(t.TempDir(), "bin")
	j := &journey{t: t, bin: filepath.Join(installDir, "burro"), env: []string{
		"HOME=" + srv.Home(),
		"PATH=" + installDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"BURRO_MODE=human",
		noColorEnv,
		noCheckEnv,
	}}

	// Step 1: the install command from the README, then the welcome.
	r := testutil.RunInstall(t, append(slices.Clone(j.env),
		"BURRO_BASE_URL="+testutil.ServeRelease(t, dist, "v"+version),
		"BURRO_INSTALL_DIR="+installDir))
	if r.Code != 0 {
		t.Fatalf("install.sh: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	j.shows(r.Stdout, "downloading burro "+version+" for "+runtime.GOOS+"/"+runtime.GOARCH+"\nchecksum ok\n")
	j.shows(r.Stdout, "burro "+version+"\n\nburro runs your coding agent's tests on your own server.")
	j.shows(r.Stdout, "burro host add myserver root@203.0.113.10 -i ~/.ssh/id_ed25519")

	// Step 2: the welcome's command with the user's own server and key.
	r = j.burro(srv.Home(), "host", "add", "myserver", srv.Address(), "-i", srv.Key())
	j.shows(r.Stdout, "\nburro   ok    "+version+"\n")
	j.shows(r.Stderr, "next: cd into your project and run burro init")

	// Step 3: init in the project.
	repo := testutil.GitRepo(t, map[string]string{"package.json": "{}\n"})
	r = j.burro(repo, "init")
	j.shows(r.Stdout, "created    burro.toml")
	j.shows(r.Stderr, `say "fill in burro.toml and run the tests with burro"`)
	j.shows(r.Stderr, "burro help config")

	// Step 4 is the agent's: it reads help config and the rules block
	// init wrote, fills in burro.toml and starts the run.
	help := j.burro(repo, "help", "config").Stdout
	j.shows(help, "[steps.unit]\n  run = ")
	rules, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	j.shows(string(rules), "`burro run <pipeline>`")
	writeFile(t, filepath.Join(repo, tomlFile), "host = \"myserver\"\n\n[pipelines]\ndefault = [\"unit\"]\n\n[steps.unit]\nrun = \"echo unit ok\"\n")
	r = j.burro(repo, "run")
	j.shows(r.Stderr, "next: burro wait")

	// Step 5: the user watches the run.
	r = j.burro(repo, "wait")
	j.shows(r.Stdout, "r1 default · tree ")
	j.shows(r.Stdout, "\nunit  ok")
}
