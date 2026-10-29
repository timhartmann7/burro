package report

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode"
	"unicode/utf8"
)

const (
	// maxLine caps one raw log line; a binary blob without newlines must not
	// fill the memory of the server.
	maxLine = 1 << 20
	esc     = 0x1b
	bel     = 0x07
)

// Cleaner turns a raw step log into clean lines: no ANSI sequences, \r
// redraws collapsed to their last state, no bytes that aren't printable
// UTF-8, no lines that match filters.drop.
type Cleaner struct {
	drop []*regexp.Regexp
}

// NewCleaner returns a cleaner that also drops lines matching any of drop.
func NewCleaner(drop []*regexp.Regexp) *Cleaner {
	return &Cleaner{drop: drop}
}

// Lines reads r to the end and calls fn with each clean line.
func (c *Cleaner) Lines(r io.Reader, fn func(line string)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var raw []byte
	for {
		chunk, err := br.ReadSlice('\n')
		raw = append(raw, chunk[:min(len(chunk), maxLine-len(raw))]...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(raw) > 0 {
			c.emit(raw, fn)
		}
		raw = raw[:0]
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read log: %w", err)
		}
	}
}

func (c *Cleaner) emit(raw []byte, fn func(string)) {
	line := cleanLine(raw)
	for _, re := range c.drop {
		if re.MatchString(line) {
			return
		}
	}
	fn(line)
}

// cleanLine cleans one raw line. A \r starts the line over the way a
// terminal does; the last part with text is what the terminal ended up
// showing.
func cleanLine(raw []byte) string {
	var seg, last []byte
	for i := 0; i < len(raw); {
		switch raw[i] {
		case esc:
			i = skipEscape(raw, i+1)
			continue
		case '\r':
			if len(seg) > 0 {
				last, seg = append(last[:0], seg...), seg[:0]
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(raw[i:])
		if (r != utf8.RuneError || size > 1) && (r == '\t' || !unicode.IsControl(r)) {
			seg = append(seg, raw[i:i+size]...)
		}
		i += size
	}
	if len(seg) == 0 {
		seg = last
	}
	return string(seg)
}

// skipEscape returns the index after the escape sequence whose ESC came
// right before b[i]: CSI up to its final byte, OSC and other strings up to
// BEL or ST, anything else up to one final byte.
func skipEscape(b []byte, i int) int {
	if i >= len(b) {
		return i
	}
	switch b[i] {
	case '[':
		return skipCSI(b, i+1)
	case ']', 'P', 'X', '^', '_':
		return skipString(b, i+1)
	default:
		for i < len(b) && b[i] >= 0x20 && b[i] <= 0x2f {
			i++
		}
		return min(i+1, len(b))
	}
}

// skipCSI skips parameters and intermediates up to the final byte.
func skipCSI(b []byte, i int) int {
	for ; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			return i + 1
		}
	}
	return i
}

// skipString skips an OSC or other string up to BEL or ESC \.
func skipString(b []byte, i int) int {
	for ; i < len(b); i++ {
		if b[i] == bel {
			return i + 1
		}
		if b[i] == esc && i+1 < len(b) && b[i+1] == '\\' {
			return i + 2
		}
	}
	return i
}

// Tail returns the last n clean lines of r that fit in maxBytes, and how
// many lines came before them.
func (c *Cleaner) Tail(r io.Reader, n, maxBytes int) (lines []string, hidden int, err error) {
	size := 0
	err = c.Lines(r, func(line string) {
		lines = append(lines, line)
		size += len(line)
		for len(lines) > 0 && (len(lines) > n || size > maxBytes) {
			size -= len(lines[0])
			lines = lines[1:]
			hidden++
		}
	})
	return lines, hidden, err
}

// Grep returns the clean lines of r that match re, with context lines
// around each match. Windows that overlap or touch merge; "--" separates
// the others. Output stops at n lines or maxBytes; matches past that count
// as hidden.
func (c *Cleaner) Grep(r io.Reader, re *regexp.Regexp, context, n, maxBytes int) (lines []string, matches, hidden int, err error) {
	g := &grep{re: re, context: context, n: n, maxBytes: maxBytes}
	err = c.Lines(r, g.add)
	return g.out, g.matches, g.matches - g.shown, err
}

type grep struct {
	re                   *regexp.Regexp
	context, n, maxBytes int
	// before holds the last lines that no window printed yet.
	before []string
	// after counts the context lines still owed to the last match.
	after       int
	line        int
	lastPrinted int
	out         []string
	size        int
	full        bool
	matches     int
	shown       int
}

func (g *grep) add(s string) {
	g.line++
	switch {
	case g.re.MatchString(s):
		g.matches++
		if g.lastPrinted > 0 && g.line-len(g.before) > g.lastPrinted+1 {
			g.print("--")
		}
		for _, b := range g.before {
			g.print(b)
		}
		g.before = g.before[:0]
		if g.print(s) {
			g.shown++
		}
		g.after, g.lastPrinted = g.context, g.line
	case g.after > 0:
		g.print(s)
		g.after--
		g.lastPrinted = g.line
	case g.context > 0:
		if len(g.before) == g.context {
			g.before = g.before[1:]
		}
		g.before = append(g.before, s)
	}
}

// print adds s unless the output is full. Once full it stays full, so the
// output never skips a line in the middle.
func (g *grep) print(s string) bool {
	if g.full || len(g.out) >= g.n || g.size+len(s) > g.maxBytes {
		g.full = true
		return false
	}
	g.out = append(g.out, s)
	g.size += len(s)
	return true
}
