// Package serverbin holds the Linux binaries the client installs on servers.
// A build without them, such as one made by go install, downloads them from
// the GitHub release of its own version.
package serverbin

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/timhartmann7/burro/internal/output"
)

// ReleaseBase is where GitHub serves the assets of burro's releases, one
// folder per tag.
const ReleaseBase = "https://github.com/timhartmann7/burro/releases/download"

const (
	// maxArchive caps a downloaded archive and the binary inside it.
	maxArchive = 50 << 20
	// maxChecksums caps checksums.txt, which holds a few lines.
	maxChecksums = 1 << 20
	// downloadTimeout bounds both downloads over a slow link.
	downloadTimeout = 5 * time.Minute
	checksumsFile   = "checksums.txt"
)

// releaseVersion matches the versions that have a release to download from.
var releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

//go:embed bin
var embedded embed.FS

// Set is the server binaries of this client: built in, or downloaded.
type Set struct {
	files   fs.FS
	version string
	base    string
	client  *http.Client
}

// Embedded is the folder of binaries built into this client. It holds only
// a README unless make build ran before the build.
func Embedded() fs.FS {
	return embedded
}

// New returns the binaries in files. A binary missing there comes from the
// release of version under base, checked against its checksums.txt.
func New(files fs.FS, version, base string) *Set {
	return &Set{files: files, version: version, base: base, client: &http.Client{Timeout: downloadTimeout}}
}

// ForMachine returns the binary for a server whose `uname -m` printed machine.
func (s *Set) ForMachine(ctx context.Context, machine string) ([]byte, error) {
	arch, err := goarch(machine)
	if err != nil {
		return nil, err
	}
	data, err := fs.ReadFile(s.files, "bin/burro-linux-"+arch)
	if errors.Is(err, fs.ErrNotExist) {
		return s.download(ctx, arch)
	}
	if err != nil {
		return nil, fmt.Errorf("read server binary: %w", err)
	}
	return data, nil
}

// download fetches the archive for arch and checksums.txt from one release
// and returns the binary only when the archive matches its checksum.
func (s *Set) download(ctx context.Context, arch string) ([]byte, error) {
	if !releaseVersion.MatchString(s.version) {
		return nil, output.NewError(output.ExitServer,
			fmt.Sprintf("this build of burro (%s) has no server binary for linux/%s and no release to download it from", s.version, arch),
			"install a release build of burro, or build it with make build")
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	name := "burro_linux_" + arch + ".tar.gz"
	sums, err := s.fetch(ctx, checksumsFile, maxChecksums)
	if err != nil {
		return nil, err
	}
	want, err := s.checksum(sums, name)
	if err != nil {
		return nil, err
	}
	archive, err := s.fetch(ctx, name, maxArchive)
	if err != nil {
		return nil, err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return nil, s.broken(name + " does not match its checksum in " + checksumsFile)
	}
	bin, err := binary(archive)
	if err != nil {
		return nil, s.broken(name + ": " + err.Error())
	}
	return bin, nil
}

func (s *Set) url(name string) string {
	return s.base + "/v" + s.version + "/" + name
}

// fetch downloads one asset of the release, at most limit bytes.
func (s *Set) fetch(ctx context.Context, name string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(name), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, output.NewError(output.ExitServer, fmt.Sprintf("download %s: %v", s.url(name), errors.Unwrap(err)),
			"check that this machine reaches github.com, then rerun")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, output.NewError(output.ExitServer, fmt.Sprintf("download %s: %s", s.url(name), resp.Status),
			fmt.Sprintf("check that release v%s exists at https://github.com/timhartmann7/burro/releases", s.version))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, output.NewError(output.ExitServer, fmt.Sprintf("download %s: %v", s.url(name), err),
			"check that this machine reaches github.com, then rerun")
	}
	if int64(len(data)) > limit {
		return nil, s.broken(fmt.Sprintf("%s is larger than %d bytes", name, limit))
	}
	return data, nil
}

// checksum finds the sha256 of name in a checksums.txt of "<hex>  <name>"
// lines.
func (s *Set) checksum(sums []byte, name string) (string, error) {
	lines := bufio.NewScanner(bytes.NewReader(sums))
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) == 2 && fields[1] == name && len(fields[0]) == sha256.Size*2 {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", s.broken(checksumsFile + " has no sha256 for " + name)
}

// broken reports a release asset that can't be trusted. Nothing reached the
// server.
func (s *Set) broken(msg string) error {
	return output.NewError(output.ExitServer,
		fmt.Sprintf("server binary from release v%s: %s, nothing was installed", s.version, msg),
		"rerun later; if it repeats, report it at https://github.com/timhartmann7/burro/issues")
}

// binary returns the burro file at the root of a tar.gz archive.
func binary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	r := tar.NewReader(gz)
	for {
		h, err := r.Next()
		if err != nil {
			return nil, fmt.Errorf("no burro binary in the archive: %w", err)
		}
		if h.Name != "burro" || h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(r, maxArchive+1))
		if err != nil {
			return nil, fmt.Errorf("read the burro binary: %w", err)
		}
		if len(data) > maxArchive {
			return nil, fmt.Errorf("the burro binary is larger than %d bytes", maxArchive)
		}
		return data, nil
	}
}

func goarch(machine string) (string, error) {
	switch machine {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", output.NewError(output.ExitServer,
			fmt.Sprintf("server architecture %q is not supported", machine),
			"use a Linux server on amd64 or arm64")
	}
}
