package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ServeRelease serves the files in dir as GitHub serves release tag:
// /latest redirects to /tag/<tag>, and /download/<tag>/<name> is the asset.
// It returns the base URL that install.sh takes as BURRO_BASE_URL.
func ServeRelease(tb testing.TB, dir, tag string) string {
	tb.Helper()
	assets := http.StripPrefix("/download/"+tag+"/", http.FileServer(http.Dir(dir)))
	mux := http.NewServeMux()
	mux.Handle("/latest", http.RedirectHandler("/tag/"+tag, http.StatusFound))
	mux.HandleFunc("/tag/"+tag, func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/download/"+tag+"/", func(w http.ResponseWriter, r *http.Request) {
		// Only plain files of the release, never a folder listing.
		if strings.Contains(strings.TrimPrefix(r.URL.Path, "/download/"+tag+"/"), "/") {
			http.NotFound(w, r)
			return
		}
		assets.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	tb.Cleanup(srv.Close)
	return srv.URL
}

// TarGz packs files, name to content, into a tar.gz archive with mode 0755.
func TarGz(tb testing.TB, files map[string][]byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			tb.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			tb.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

// ChecksumLine is the checksums.txt line of an asset.
func ChecksumLine(data []byte, name string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// RunInstall runs install.sh from the module root with sh and exactly env,
// in a fresh working folder, and captures its exit code and output.
func RunInstall(tb testing.TB, env []string) Result {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", filepath.Join(moduleRoot(tb), "install.sh"))
	cmd.Dir = tb.TempDir()
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		tb.Fatalf("run install.sh: %v", err)
	}
	return Result{Code: cmd.ProcessState.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String()}
}

// Dist is the dist/ folder that make snapshot fills. Tests that install
// from a snapshot release fail without it.
func Dist(tb testing.TB) string {
	tb.Helper()
	dir := filepath.Join(moduleRoot(tb), "dist")
	if _, err := os.Stat(filepath.Join(dir, "checksums.txt")); err != nil {
		tb.Fatalf("no snapshot release in %s, run make snapshot first: %v", dir, err)
	}
	return dir
}
