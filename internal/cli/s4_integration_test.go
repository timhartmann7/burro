//go:build integration

package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/serverbin"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	starLine = "If burro saves you time, a star helps: https://github.com/timhartmann7/burro\n"
	nextInit = "next: cd into your project and run burro init\n"
)

// expect runs one command and fails unless it exits with code and prints
// exactly stderr.
func (w *world) expect(code int, stderr string, args ...string) {
	w.t.Helper()
	if r := w.burro(v1, args...); r.Code != code || r.Stderr != stderr {
		w.t.Fatalf("burro %q: exit %d stderr %q, want exit %d stderr %q", args, r.Code, r.Stderr, code, stderr)
	}
}

// acceptance: S4-11 S4-15
func TestStarAndNextHints(t *testing.T) {
	w := newWorld(t, map[string]string{tomlFile: hostTOML})
	add := func(name string, extra ...string) []string {
		return append([]string{argHost, argAdd, name, w.srv.Address(), "-i", w.srv.Key()}, extra...)
	}
	w.expect(0, starLine+nextInit, add("vps1", argMode, humanMode)...)
	// The star shows once ever, the hint only after the first host.
	w.expect(0, "", add("vps2", argMode, humanMode)...)
	state := filepath.Join(w.srv.Home(), ".burro", "state.json")
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	// A failed host add shows neither.
	w.expect(4, "burro: host vps1 already exists\nfix: pick another name, or pass --force to replace it\n", add("vps1", argMode, humanMode)...)
	// An agent never sees them, and never uses up the star request.
	w.expect(0, "", add("vps3")...)
	if _, err := os.Stat(state); err == nil {
		t.Fatal("agent mode recorded the star request")
	}
	w.expect(0, starLine, add("vps4", argMode, humanMode)...)

	w.expect(0, "next: burro wait\n", argRun, argMode, humanMode)
	w.must(v1, argWait)
	w.expect(0, "", argRun)
	w.must(v1, argWait)
}

// releaseAssets serves the assets of release v1 under /v1.0.0/.
func releaseAssets(t *testing.T, assets map[string][]byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		data, ok := assets[strings.TrimPrefix(r.URL.Path, "/v"+v1+"/")]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// downloadWorld is a world whose client has no embedded binaries, served the
// release assets of v1 for the server's architecture, the archive passed
// through tamper.
func downloadWorld(t *testing.T, tamper func(good []byte) []byte) (w *world, run func(args ...string) testutil.Result) {
	t.Helper()
	w = newWorld(t, nil)
	arch := goarchOf(strings.TrimSpace(w.srv.Exec(t, "uname", "-m")))
	name := "burro_linux_" + arch + ".tar.gz"
	good := testutil.TarGz(t, map[string][]byte{binaryName: testutil.BuildServerBurro(t, v1, arch)})
	base := releaseAssets(t, map[string][]byte{
		name:            tamper(bytes.Clone(good)),
		"checksums.txt": []byte(testutil.ChecksumLine(good, name)),
	})
	run = func(args ...string) testutil.Result {
		var stdout, stderr strings.Builder
		a := New(strings.NewReader(""), &stdout, &stderr, w.env, v1)
		a.binaries = serverbin.New(fstest.MapFS{}, v1, base)
		code := output.Report(&stderr, a.Run(args))
		return testutil.Result{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
	}
	return w, run
}

// acceptance: S4-07
func TestTamperedReleaseUploadsNothing(t *testing.T) {
	w, run := downloadWorld(t, func(b []byte) []byte {
		b[len(b)/2] ^= 0xff
		return b
	})
	r := run(argHost, argAdd, "vps1", w.srv.Address(), "-i", w.srv.Key())
	if r.Code != 2 || !strings.Contains(r.Stdout, "does not match its checksum") || !strings.Contains(r.Stdout, "nothing was installed") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.Code, r.Stdout, r.Stderr)
	}
	if got := w.srv.Exec(t, "sh", "-c", "ls -A ~/.burro/bin 2>/dev/null; true"); got != "" {
		t.Fatalf("the server got files: %q", got)
	}
	if r := run(argExec, "echo"); r.Code != 2 || !strings.Contains(r.Stderr, "does not match its checksum") {
		t.Fatalf("exec: exit %d stderr %q", r.Code, r.Stderr)
	}
}

func TestDownloadedServerBinary(t *testing.T) {
	w, run := downloadWorld(t, func(good []byte) []byte { return good })
	if r := run(argHost, argAdd, "vps1", w.srv.Address(), "-i", w.srv.Key()); r.Code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.Code, r.Stdout, r.Stderr)
	}
	if got := w.srv.Exec(t, "sh", "-c", "~/.burro/bin/burro --version"); !strings.HasPrefix(got, "burro "+v1+" linux/") {
		t.Fatalf("server binary: %q", got)
	}
}
