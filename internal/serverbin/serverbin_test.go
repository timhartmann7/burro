package serverbin

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/timhartmann7/burro/internal/output"
)

func TestForMachine(t *testing.T) {
	s := &Set{files: fstest.MapFS{
		"bin/burro-linux-amd64": {Data: []byte("amd64 binary")},
		"bin/burro-linux-arm64": {Data: []byte("arm64 binary")},
	}}
	for machine, want := range map[string]string{
		"x86_64": "amd64 binary", "amd64": "amd64 binary", "aarch64": "arm64 binary", "arm64": "arm64 binary",
	} {
		got, err := s.ForMachine(machine)
		if err != nil || string(got) != want {
			t.Errorf("ForMachine(%q) = %q, %v", machine, got, err)
		}
	}
	for _, machine := range []string{"", "armv7l", "riscv64", "x86_64\n"} {
		_, err := s.ForMachine(machine)
		wantServerError(t, err)
	}
}

func TestMissingBinary(t *testing.T) {
	// The embedded folder holds only the README unless make build ran.
	s := &Set{files: fstest.MapFS{"bin/README": {Data: []byte("x")}}}
	_, err := s.ForMachine("x86_64")
	wantServerError(t, err)
	if _, err := New().ForMachine("mips"); err == nil {
		t.Fatal("an unknown machine passed")
	}
	broken := &Set{files: fstest.MapFS{"bin/burro-linux-amd64": {Mode: 0o20000000000}}}
	if _, err := broken.ForMachine("x86_64"); err == nil {
		t.Fatal("an unreadable binary passed")
	}
}

func wantServerError(t *testing.T, err error) {
	t.Helper()
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != output.ExitServer || coded.Fix() == "" {
		t.Fatalf("got %v, want exit 2 with a fix line", err)
	}
}
