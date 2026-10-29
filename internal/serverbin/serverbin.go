// Package serverbin holds the Linux binaries the client installs on servers.
package serverbin

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/timhartmann7/burro/internal/output"
)

//go:embed bin
var embedded embed.FS

// Set is the server binaries built into this client.
type Set struct {
	files fs.FS
}

// New returns the binaries embedded at build time.
func New() *Set {
	return &Set{files: embedded}
}

// ForMachine returns the binary for a server whose `uname -m` printed machine.
func (s *Set) ForMachine(machine string) ([]byte, error) {
	arch, err := goarch(machine)
	if err != nil {
		return nil, err
	}
	data, err := fs.ReadFile(s.files, "bin/burro-linux-"+arch)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, output.NewError(output.ExitServer,
			"this build of burro has no server binary for linux/"+arch,
			"install a release build of burro, or build it with make build")
	}
	if err != nil {
		return nil, fmt.Errorf("read server binary: %w", err)
	}
	return data, nil
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
