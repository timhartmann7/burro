package report

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"os"
	"slices"
)

const (
	// Mask replaces every secret value.
	Mask = "***"
	// minSecret is the shortest value that gets masked; shorter ones would
	// mask half of every log.
	minSecret = 4
	// maxHeld bounds what a masker holds back while the text so far could
	// still turn into a secret.
	maxHeld = 64 << 10
)

// Masker writes through to w with every secret value replaced by Mask. A
// secret may arrive split over several writes, so the masker holds back
// text that could still become one; Flush writes the rest.
type Masker struct {
	w       io.Writer
	secrets [][]byte
	longest int
	held    []byte
}

// NewMasker masks values in what goes to w. Surrounding whitespace, such
// as a file's last newline, is not part of a value.
func NewMasker(w io.Writer, values [][]byte) *Masker {
	m := &Masker{w: w}
	for _, v := range values {
		v = bytes.TrimSpace(v)
		if len(v) >= minSecret && !slices.ContainsFunc(m.secrets, func(s []byte) bool { return bytes.Equal(s, v) }) {
			m.secrets = append(m.secrets, v)
			m.longest = max(m.longest, len(v))
		}
	}
	// At one position the longest value wins, so a secret that starts with
	// another one is masked whole.
	slices.SortFunc(m.secrets, func(a, b []byte) int { return cmp.Compare(len(b), len(a)) })
	return m
}

// Write masks p together with the text held back so far and writes what
// can no longer change.
func (m *Masker) Write(p []byte) (int, error) {
	m.held = m.mask(append(m.held, p...))
	cut := m.safe()
	if err := m.emit(cut); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes the text held back.
func (m *Masker) Flush() error {
	return m.emit(len(m.held))
}

func (m *Masker) emit(n int) error {
	if n == 0 {
		return nil
	}
	if _, err := m.w.Write(m.held[:n]); err != nil {
		return fmt.Errorf("write masked output: %w", err)
	}
	m.held = append(m.held[:0], m.held[n:]...)
	return nil
}

// mask replaces secrets until none is left. One pass can join a mask and
// the text around it into another secret, and each pass makes b shorter, so
// the loop ends.
func (m *Masker) mask(b []byte) []byte {
	for {
		out, changed := m.replace(b)
		if !changed {
			return out
		}
		b = out
	}
}

// replace masks the leftmost secret, then the next one after it, and so on.
func (m *Masker) replace(b []byte) ([]byte, bool) {
	next := make([]int, len(m.secrets))
	for j, s := range m.secrets {
		next[j] = bytes.Index(b, s)
	}
	var out []byte
	i := 0
	for {
		at, which := m.leftmost(b, i, next)
		if at < 0 {
			if out == nil {
				return b, false
			}
			return append(out, b[i:]...), true
		}
		out = append(append(out, b[i:at]...), Mask...)
		i = at + len(m.secrets[which])
	}
}

// leftmost returns where the first secret at or after i starts in b, and
// which one it is; on a tie the longer one. next holds where each secret
// was found last and is kept up to date.
func (m *Masker) leftmost(b []byte, i int, next []int) (at, which int) {
	at, which = -1, -1
	for j, s := range m.secrets {
		if next[j] >= 0 && next[j] < i {
			// That match overlapped one masked already.
			next[j] = -1
			if k := bytes.Index(b[i:], s); k >= 0 {
				next[j] = i + k
			}
		}
		if next[j] >= 0 && (at < 0 || next[j] < at) {
			at, which = next[j], j
		}
	}
	return at, which
}

// safe is how much of the held text can go out: none of it may end with
// the start of a secret, or the next write could finish that secret, or
// join the text with a mask into one. Past maxHeld the masker keeps only
// what a secret split over writes needs.
func (m *Masker) safe() int {
	if len(m.secrets) == 0 {
		return len(m.held)
	}
	for p := len(m.held); p > 0; p-- {
		if len(m.held)-p > maxHeld {
			return len(m.held) - (m.longest - 1)
		}
		if !m.endsInSecret(m.held[:p]) {
			return p
		}
	}
	return 0
}

// endsInSecret tells whether b ends with the start of a secret.
func (m *Masker) endsInSecret(b []byte) bool {
	for _, s := range m.secrets {
		tail := b[max(0, len(b)-(len(s)-1)):]
		for i := 0; i < len(tail); i++ {
			k := bytes.IndexByte(tail[i:], s[0])
			if k < 0 {
				break
			}
			i += k
			if bytes.HasPrefix(s, tail[i:]) {
				return true
			}
		}
	}
	return false
}

// ReadSecrets reads the values of every secret in the folders of dirs.
// A folder that is gone by now holds nothing to mask.
func ReadSecrets(dirs ...string) [][]byte {
	var values [][]byte
	for _, dir := range dirs {
		root, err := os.OpenRoot(dir)
		if err != nil {
			continue
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if data, err := root.ReadFile(e.Name()); err == nil && e.Type().IsRegular() {
				values = append(values, data)
			}
		}
		_ = root.Close()
	}
	return values
}
