package report_test

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/timhartmann7/burro/internal/report"
	"github.com/timhartmann7/burro/internal/testutil"
)

const unlimited = 1 << 30

func lines(t *testing.T, c *report.Cleaner, in string) []string {
	t.Helper()
	got := []string{}
	if err := c.Lines(strings.NewReader(in), func(l string) { got = append(got, l) }); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCleanLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "plain", in: "a\nb\n", want: []string{"a", "b"}},
		{name: "no final newline", in: "a\nb", want: []string{"a", "b"}},
		{name: "empty lines stay", in: "a\n\nb\n", want: []string{"a", "", "b"}},
		{name: "colors", in: "\x1b[31mFAIL\x1b[0m test\n", want: []string{"FAIL test"}},
		{name: "private and erase codes", in: "\x1b[?25l\x1b[2K\x1b[1;32mok\x1b[m\n", want: []string{"ok"}},
		{name: "osc title with bel", in: "\x1b]0;title\x07text\n", want: []string{"text"}},
		{name: "osc link with st", in: "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\\n", want: []string{"link"}},
		{name: "charset and keypad", in: "\x1b(Ba\x1b=b\x1b>c\n", want: []string{"abc"}},
		{name: "lone esc at the end", in: "a\x1b", want: []string{"a"}},
		{name: "unfinished csi", in: "a\x1b[12", want: []string{"a"}},
		{name: "progress redraws", in: "  0%\r 50%\r100%\n", want: []string{"100%"}},
		{name: "crlf", in: "line one\r\nline two\r\n", want: []string{"line one", "line two"}},
		{name: "redraw with erase code", in: "downloading 1/3\x1b[K\rdownloading 3/3\x1b[K\r\n", want: []string{"downloading 3/3"}},
		{name: "invalid utf-8", in: "a\xff\xfeb\xc3\n", want: []string{"ab"}},
		{name: "utf-8 stays", in: "é 世 \U0001F642 �\n", want: []string{"é 世 \U0001F642 �"}},
		{name: "binary and controls", in: "a\x00b\x07c\x08d\x7f\u0085e\tf\n", want: []string{"abcde\tf"}},
		{name: "only codes", in: "\x1b[2K\n", want: []string{""}},
	}
	c := report.NewCleaner(nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lines(t, c, tt.in); strings.Join(got, "|") != strings.Join(tt.want, "|") || len(got) != len(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCleanDrop(t *testing.T) {
	c := report.NewCleaner([]*regexp.Regexp{regexp.MustCompile(`^\[pg-boss\]`), regexp.MustCompile(`^\s*$`)})
	// Filters see the clean line, so color codes don't hide a match.
	got := lines(t, c, "keep 1\n\x1b[33m[pg-boss]\x1b[0m tick\n   \n\nkeep 2\n")
	if strings.Join(got, "|") != "keep 1|keep 2" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanLongLine(t *testing.T) {
	in := strings.Repeat("x", 3<<20) + "\nafter\n"
	got := lines(t, report.NewCleaner(nil), in)
	if len(got) != 2 || len(got[0]) != 1<<20 || got[1] != "after" {
		t.Fatalf("got %d lines, first %d bytes", len(got), len(got[0]))
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk gone") }

func TestCleanReadError(t *testing.T) {
	err := report.NewCleaner(nil).Lines(failingReader{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "disk gone") {
		t.Fatalf("got %v", err)
	}
	if _, _, err := report.NewCleaner(nil).Tail(failingReader{}, 1, 1); err == nil {
		t.Fatal("Tail passed a read error")
	}
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestTail(t *testing.T) {
	c := report.NewCleaner(nil)
	got, hidden, err := c.Tail(strings.NewReader(numbered(100)), 3, unlimited)
	if err != nil || strings.Join(got, ",") != "line 98,line 99,line 100" || hidden != 97 {
		t.Fatalf("got %q hidden %d err %v", got, hidden, err)
	}
	got, hidden, _ = c.Tail(strings.NewReader(numbered(2)), 30, unlimited)
	if len(got) != 2 || hidden != 0 {
		t.Fatalf("short log: %q %d", got, hidden)
	}
	// The byte cap keeps the newest lines that fit.
	got, hidden, _ = c.Tail(strings.NewReader("aaaa\nbbbb\ncccc\n"), 3, 9)
	if strings.Join(got, ",") != "bbbb,cccc" || hidden != 1 {
		t.Fatalf("byte cap: %q %d", got, hidden)
	}
	got, _, _ = c.Tail(strings.NewReader(""), 3, unlimited)
	if len(got) != 0 {
		t.Fatalf("empty log: %q", got)
	}
}

func TestGrep(t *testing.T) {
	c := report.NewCleaner(nil)
	log := numbered(40)
	tests := []struct {
		name            string
		re              string
		context, n, max int
		want            string
		matches, hidden int
	}{
		{name: "one match", re: `^line 20$`, context: 2, n: 60, want: "line 18,line 19,line 20,line 21,line 22", matches: 1},
		{name: "overlapping windows merge", re: `^line (10|13)$`, context: 2, n: 60, want: "line 8,line 9,line 10,line 11,line 12,line 13,line 14,line 15", matches: 2},
		{name: "touching windows merge", re: `^line (10|15)$`, context: 2, n: 60, want: "line 8,line 9,line 10,line 11,line 12,line 13,line 14,line 15,line 16,line 17", matches: 2},
		{name: "apart windows", re: `^line (10|20)$`, context: 1, n: 60, want: "line 9,line 10,line 11,--,line 19,line 20,line 21", matches: 2},
		{name: "no context", re: `^line [12]$`, n: 60, want: "line 1,line 2", matches: 2},
		{name: "no context apart", re: `^line (1|3)$`, n: 60, want: "line 1,--,line 3", matches: 2},
		{name: "start and end", re: `^line (1|40)$`, context: 3, n: 60, want: "line 1,line 2,line 3,line 4,--,line 37,line 38,line 39,line 40", matches: 2},
		{name: "cut", re: `^line (5|15|25|35)$`, context: 1, n: 7, want: "line 4,line 5,line 6,--,line 14,line 15,line 16", matches: 4, hidden: 2},
		{name: "cut in a window", re: `^line (5|6|7)$`, context: 0, n: 2, want: "line 5,line 6", matches: 3, hidden: 1},
		{name: "byte cap", re: `^line (5|6)$`, n: 60, max: 7, want: "line 5", matches: 2, hidden: 1},
		{name: "no match", re: `nothing`, context: 5, n: 60, want: "", matches: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxBytes := tt.max
			if maxBytes == 0 {
				maxBytes = unlimited
			}
			got, matches, hidden, err := c.Grep(strings.NewReader(log), regexp.MustCompile(tt.re), tt.context, tt.n, maxBytes)
			if err != nil || strings.Join(got, ",") != tt.want || matches != tt.matches || hidden != tt.hidden {
				t.Fatalf("got %q matches %d hidden %d err %v\nwant %q matches %d hidden %d", got, matches, hidden, err, tt.want, tt.matches, tt.hidden)
			}
			if len(got) > tt.n {
				t.Fatalf("%d lines, want at most %d", len(got), tt.n)
			}
		})
	}
}

// The timing of S2-12 is measured end to end in the integration test; the
// race detector makes this one too slow to time.
func TestGrepNoisyLog(t *testing.T) {
	log := testutil.NoisyLog(8<<20, 42)
	got, matches, hidden, err := report.NewCleaner(nil).Grep(bytes.NewReader(log), regexp.MustCompile(`cohorts`), 5, 60, unlimited)
	if err != nil || matches < 100 || len(got) != 60 || hidden != matches-5 {
		t.Fatalf("matches %d hidden %d lines %d err %v", matches, hidden, len(got), err)
	}
	for _, l := range got {
		if !utf8.ValidString(l) || strings.ContainsAny(l, "\x1b\r") {
			t.Fatalf("dirty line %q", l)
		}
	}
}

func FuzzCleanLog(f *testing.F) {
	for _, seed := range testutil.FuzzSeeds(f, "FuzzCleanLog") {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var out strings.Builder
		err := report.NewCleaner(nil).Lines(bytes.NewReader(data), func(l string) {
			out.WriteString(l + "\n")
		})
		if err != nil {
			t.Fatal(err)
		}
		if s := out.String(); !utf8.ValidString(s) || strings.ContainsAny(s, "\x1b\r") {
			t.Fatalf("Lines(%q) gave %q", data, s)
		}
	})
}
