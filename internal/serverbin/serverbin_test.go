package serverbin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/timhartmann7/burro/internal/output"
)

const (
	machineAMD = "x86_64"
	version    = "1.2.3"
	assetAMD   = "burro_linux_amd64.tar.gz"
	serverBin  = "server binary"
	binName    = "burro"
)

func TestForMachine(t *testing.T) {
	s := New(fstest.MapFS{
		"bin/burro-linux-amd64": {Data: []byte("amd64 binary")},
		"bin/burro-linux-arm64": {Data: []byte("arm64 binary")},
	}, version, "http://127.0.0.1:1")
	for machine, want := range map[string]string{
		machineAMD: "amd64 binary", "amd64": "amd64 binary", "aarch64": "arm64 binary", "arm64": "arm64 binary",
	} {
		got, err := s.ForMachine(context.Background(), machine)
		if err != nil || string(got) != want {
			t.Errorf("ForMachine(%q) = %q, %v", machine, got, err)
		}
	}
	for _, machine := range []string{"", "armv7l", "riscv64", "x86_64\n"} {
		_, err := s.ForMachine(context.Background(), machine)
		wantServerError(t, err)
	}
}

func TestMissingBinary(t *testing.T) {
	// The embedded folder holds only the README unless make build ran.
	for _, v := range []string{"dev", "", "1.2", "1.2.3-SNAPSHOT-abc", "../1.2.3"} {
		s := New(fstest.MapFS{"bin/README": {Data: []byte("x")}}, v, "http://127.0.0.1:1")
		_, err := s.ForMachine(context.Background(), machineAMD)
		wantServerError(t, err)
		if !strings.Contains(err.Error(), "no release to download it from") {
			t.Errorf("version %q: %v", v, err)
		}
	}
	if _, err := New(Embedded(), "dev", ReleaseBase).ForMachine(context.Background(), "mips"); err == nil {
		t.Fatal("an unknown machine passed")
	}
	broken := New(fstest.MapFS{"bin/burro-linux-amd64": {Mode: 0o20000000000}}, version, "http://127.0.0.1:1")
	if _, err := broken.ForMachine(context.Background(), machineAMD); err == nil {
		t.Fatal("an unreadable binary passed")
	}
}

// release serves the assets of one release and records what was asked for.
type release struct {
	mu     sync.Mutex
	assets map[string][]byte
	asked  []string
}

func (r *release) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, req.URL.Path)
	data, ok := r.assets[req.URL.Path]
	if !ok {
		http.NotFound(w, req)
		return
	}
	_, _ = w.Write(data)
}

func serve(t *testing.T, assets map[string][]byte) (*Set, *release) {
	t.Helper()
	r := &release{assets: map[string][]byte{}}
	for name, data := range assets {
		r.assets["/v"+version+"/"+name] = data
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return New(fstest.MapFS{}, version, srv.URL), r
}

type entry struct {
	name string
	kind byte
	data string
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: 0o755, Size: int64(len(e.data))}
		if e.kind == tar.TypeSymlink {
			h.Linkname = "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sumLine(data []byte, name string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// release archives carry LICENSE and NOTICE beside the binary.
func goodArchive(t *testing.T) []byte {
	t.Helper()
	return archive(t, entry{"LICENSE", tar.TypeReg, "license"}, entry{binName, tar.TypeReg, serverBin}, entry{"NOTICE", tar.TypeReg, "notice"})
}

func TestDownload(t *testing.T) {
	good := goodArchive(t)
	other := archive(t, entry{binName, tar.TypeReg, "arm64"})
	s, r := serve(t, map[string][]byte{
		assetAMD:                 good,
		"checksums.txt":          []byte(sumLine(other, "burro_linux_arm64.tar.gz") + strings.ToUpper(sumLine(good, "x")[:64]) + "  " + assetAMD + "\n"),
		"burro_darwin_arm64.tar": []byte("unused"),
	})
	got, err := s.ForMachine(context.Background(), machineAMD)
	if err != nil || string(got) != serverBin {
		t.Fatalf("got %q, %v", got, err)
	}
	if want := []string{"/v1.2.3/checksums.txt", "/v1.2.3/" + assetAMD}; fmt.Sprint(r.asked) != fmt.Sprint(want) {
		t.Fatalf("asked for %v, want %v", r.asked, want)
	}
}

// acceptance: S4-07
func TestDownloadRejects(t *testing.T) {
	good := goodArchive(t)
	tampered := archive(t, entry{binName, tar.TypeReg, "evil binary"})
	notGzip := []byte("plain bytes")
	noBinary := archive(t, entry{"LICENSE", tar.TypeReg, "license"}, entry{"bin/burro", tar.TypeReg, "nested"})
	symlink := archive(t, entry{binName, tar.TypeSymlink, ""})
	huge := make([]byte, maxArchive+1)
	tests := map[string]struct {
		assets map[string][]byte
		want   string
	}{
		"tampered archive":   {map[string][]byte{assetAMD: tampered, checksumsFile: []byte(sumLine(good, assetAMD))}, "does not match its checksum"},
		"no checksum line":   {map[string][]byte{assetAMD: good, checksumsFile: []byte(sumLine(good, "burro_linux_arm64.tar.gz"))}, "no sha256 for " + assetAMD},
		"short checksum":     {map[string][]byte{assetAMD: good, checksumsFile: []byte("abc  " + assetAMD + "\n")}, "no sha256 for"},
		"no checksums.txt":   {map[string][]byte{assetAMD: good}, "404 Not Found"},
		"no archive":         {map[string][]byte{checksumsFile: []byte(sumLine(good, assetAMD))}, "404 Not Found"},
		"not gzip":           {map[string][]byte{assetAMD: notGzip, checksumsFile: []byte(sumLine(notGzip, assetAMD))}, "not a gzip archive"},
		"binary not at root": {map[string][]byte{assetAMD: noBinary, checksumsFile: []byte(sumLine(noBinary, assetAMD))}, "no burro binary"},
		"binary is symlink":  {map[string][]byte{assetAMD: symlink, checksumsFile: []byte(sumLine(symlink, assetAMD))}, "no burro binary"},
		"archive too large":  {map[string][]byte{assetAMD: huge, checksumsFile: []byte(sumLine(huge, assetAMD))}, "larger than"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s, _ := serve(t, tt.assets)
			got, err := s.ForMachine(context.Background(), machineAMD)
			wantServerError(t, err)
			if got != nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %q, %v; want an error with %q", got, err, tt.want)
			}
		})
	}
}

func TestDownloadUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	_, err := New(fstest.MapFS{}, version, srv.URL).ForMachine(context.Background(), machineAMD)
	wantServerError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _ := serve(t, nil)
	if _, err := s.ForMachine(ctx, machineAMD); err == nil {
		t.Fatal("a canceled download passed")
	}
	bad := New(fstest.MapFS{}, version, "http://bad host")
	if _, err := bad.ForMachine(context.Background(), machineAMD); err == nil {
		t.Fatal("a broken URL passed")
	}
}

func wantServerError(t *testing.T, err error) {
	t.Helper()
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != output.ExitServer || coded.Fix() == "" {
		t.Fatalf("got %v, want exit 2 with a fix line", err)
	}
}
