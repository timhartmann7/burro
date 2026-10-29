package report_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/report"
	"github.com/timhartmann7/burro/internal/testutil"
)

const hunter = "hunter22"

// masked writes chunks through a masker with secrets and returns the result.
func masked(t *testing.T, secrets []string, chunks ...string) string {
	t.Helper()
	var out bytes.Buffer
	values := make([][]byte, 0, len(secrets))
	for _, s := range secrets {
		values = append(values, []byte(s))
	}
	m := report.NewMasker(&out, values)
	for _, c := range chunks {
		if n, err := m.Write([]byte(c)); err != nil || n != len(c) {
			t.Fatalf("write %q: %d, %v", c, n, err)
		}
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestMask(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
		chunks  []string
		want    string
	}{
		{name: "plain", secrets: []string{hunter}, chunks: []string{"token=hunter22 ok\n"}, want: "token=*** ok\n"},
		{name: "every time", secrets: []string{hunter}, chunks: []string{"hunter22hunter22 hunter22"}, want: "****** ***"},
		{name: "split over writes", secrets: []string{hunter}, chunks: []string{"a hun", "t", "er22 b"}, want: "a *** b"},
		{name: "one byte at a time", secrets: []string{hunter}, chunks: strings.Split("x hunter22 y", ""), want: "x *** y"},
		{name: "start that never ends", secrets: []string{hunter}, chunks: []string{"hunt", "ed down"}, want: "hunted down"},
		{name: "file newline is not part of it", secrets: []string{"npm-token\n"}, chunks: []string{"npm-token"}, want: "***"},
		{name: "short values stay", secrets: []string{"abc", " ab \n"}, chunks: []string{"abc ab"}, want: "abc ab"},
		{name: "longest wins", secrets: []string{"pass", "password"}, chunks: []string{"password pass"}, want: "*** ***"},
		{name: "multi-line secret", secrets: []string{"-----BEGIN KEY-----\nabc\n-----END KEY-----\n"}, chunks: []string{"-----BEGIN KEY-----\na", "bc\n-----END KEY-----\n"}, want: "***\n"},
		// Masking "zzzz" would leave "a***", another secret.
		{name: "mask makes a secret", secrets: []string{"a***", "zzzz"}, chunks: []string{"aa", "zzz", "z"}, want: "***"},
		{name: "no secrets", chunks: []string{"a", "b"}, want: "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := masked(t, tt.secrets, tt.chunks...)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// A text where every byte could start the secret is held back only up to a
// bound, so a masker never holds a whole log.
func TestMaskHoldsBounded(t *testing.T) {
	var out bytes.Buffer
	m := report.NewMasker(&out, [][]byte{[]byte("aaaaX")})
	if _, err := m.Write(bytes.Repeat([]byte("a"), 200<<10)); err != nil {
		t.Fatal(err)
	}
	if out.Len() < 100<<10 {
		t.Fatalf("held %d bytes back", 200<<10-out.Len())
	}
	if _, err := m.Write([]byte("aaaaX")); err != nil {
		t.Fatal(err)
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "aaaaX") || !strings.HasSuffix(got, "***") {
		t.Fatalf("ends %q", got[len(got)-10:])
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestMaskWriteError(t *testing.T) {
	m := report.NewMasker(brokenWriter{}, [][]byte{[]byte("secret")})
	if _, err := m.Write([]byte("hello ")); err == nil {
		t.Fatal("write error lost")
	}
	m = report.NewMasker(brokenWriter{}, [][]byte{[]byte("secret")})
	if _, err := m.Write([]byte("sec")); err != nil {
		t.Fatal("nothing should be written yet")
	}
	if err := m.Flush(); err == nil {
		t.Fatal("flush error lost")
	}
}

func TestReadSecrets(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for path, data := range map[string]string{filepath.Join(a, "npmrc"): "one", filepath.Join(b, "token"): "two"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(a, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := report.ReadSecrets(a, b, filepath.Join(a, "gone"))
	if len(got) != 2 || string(got[0]) != "one" || string(got[1]) != "two" {
		t.Fatalf("got %q", got)
	}
}

// acceptance: S3-09
func FuzzMask(f *testing.F) {
	for _, seed := range testutil.FuzzSeeds(f, "FuzzMask") {
		f.Add(seed, hunter, "a***", uint8(3))
	}
	f.Fuzz(func(t *testing.T, text []byte, one, two string, chunk uint8) {
		var out bytes.Buffer
		m := report.NewMasker(&out, [][]byte{[]byte(one), []byte(two)})
		size := int(chunk) + 1
		for i := 0; i < len(text); i += size {
			if _, err := m.Write(text[i:min(len(text), i+size)]); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Flush(); err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{one, two} {
			if v := strings.TrimSpace(s); len(v) >= 4 && strings.Contains(out.String(), v) {
				t.Fatalf("secret %q in %q, masked from %q", v, out.String(), text)
			}
		}
	})
}
