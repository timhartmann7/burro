package output

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// hintTail caps the lines the "full output" hint asks for, so following the
// hint can't flood the agent either.
const hintTail = 200

// Palette colors status words. internal/mode implements it, so color codes
// come from one place and agent mode can't print them.
type Palette interface {
	OK(s string) string
	Fail(s string) string
	Skip(s string) string
}

// Printer writes one command result to stdout as text or as one JSON object.
type Printer struct {
	w       io.Writer
	json    bool
	palette Palette
}

// NewPrinter returns a printer that writes to w, as JSON when asJSON is set.
func NewPrinter(w io.Writer, asJSON bool, palette Palette) *Printer {
	return &Printer{w: w, json: asJSON, palette: palette}
}

// Check is one line of doctor.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Doctor prints one line per check.
func (p *Printer) Doctor(checks []Check) error {
	if p.json {
		return p.writeJSON(struct {
			Checks []Check `json:"checks"`
		}{checks})
	}
	var b strings.Builder
	for _, c := range checks {
		state, pad := p.palette.OK("ok"), "    "
		if !c.OK {
			state, pad = p.palette.Fail("FAIL"), "  "
		}
		line := fmt.Sprintf("%-8s%s%s%s", c.Name, state, pad, c.Detail)
		if c.Hint != "" {
			line += " · " + c.Hint
		}
		b.WriteString(line + "\n")
	}
	return p.write(b.String())
}

// Host is one line of host ls.
type Host struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Version string `json:"version"`
}

// Hosts prints one host per line in aligned columns.
func (p *Printer) Hosts(hosts []Host) error {
	if p.json {
		if hosts == nil {
			hosts = []Host{}
		}
		return p.writeJSON(struct {
			Hosts []Host `json:"hosts"`
		}{hosts})
	}
	if len(hosts) == 0 {
		return p.write("no hosts\n")
	}
	nameWidth, addrWidth := 0, 0
	for _, h := range hosts {
		nameWidth = max(nameWidth, len(h.Name))
		addrWidth = max(addrWidth, len(h.Address))
	}
	var b strings.Builder
	for _, h := range hosts {
		fmt.Fprintf(&b, "%-*s   %-*s   %s\n", nameWidth, h.Name, addrWidth, h.Address, h.Version)
	}
	return p.write(b.String())
}

// Exec is the result of one remote command.
type Exec struct {
	Output string `json:"output"`
	Hidden int    `json:"hidden"`
	Log    string `json:"log"`
	Exit   int    `json:"exit"`
}

// Exec prints the command's output, the cut line when lines are hidden and
// the original exit code when burro has to map it to 1.
func (p *Printer) Exec(e Exec) error {
	if p.json {
		return p.writeJSON(e)
	}
	var b strings.Builder
	b.WriteString(e.Output)
	if e.Output != "" && !strings.HasSuffix(e.Output, "\n") {
		b.WriteString("\n")
	}
	if e.Hidden > 0 {
		cmd := fmt.Sprintf("burro exec -- tail -n %d %s", min(e.Hidden, hintTail), e.Log)
		fmt.Fprintf(&b, "+%d lines hidden · full output: %s\n", e.Hidden, cmd)
	}
	if e.Exit != 0 && e.Exit != 1 {
		fmt.Fprintf(&b, "exit %d\n", e.Exit)
	}
	return p.write(b.String())
}

// Sync is the result of one sync.
type Sync struct {
	Tree    string
	Changed int
	Took    time.Duration
}

// Sync prints the short tree hash, the changed file count and the time.
func (p *Printer) Sync(s Sync) error {
	if p.json {
		return p.writeJSON(struct {
			Tree    string  `json:"tree"`
			Changed int     `json:"changed"`
			Seconds float64 `json:"seconds"`
		}{s.Tree, s.Changed, s.Took.Round(100 * time.Millisecond).Seconds()})
	}
	files := "files"
	if s.Changed == 1 {
		files = "file"
	}
	return p.write(fmt.Sprintf("tree %s · %d %s changed · %s\n", s.Tree, s.Changed, files, Duration(s.Took)))
}

// Duration formats d the way every summary line does: 0.4s, 12s, 3m10s, 22m,
// 1h05m.
func Duration(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m, s := int(d.Minutes()), int(d.Seconds())%60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// Head reads r to the end and returns its first maxLines lines, cut short
// when they would pass maxBytes, and the number of lines it left out. A last
// line without a newline still counts as a line.
func Head(r io.Reader, maxLines, maxBytes int) (head string, hidden int, err error) {
	br := bufio.NewReader(r)
	var b strings.Builder
	shown := 0
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			// After the first hidden line every later one is hidden too, so
			// the shown part stays one unbroken head.
			if hidden == 0 && shown < maxLines && b.Len()+len(line) <= maxBytes {
				b.WriteString(line)
				shown++
			} else {
				hidden++
			}
		}
		if errors.Is(err, io.EOF) {
			return b.String(), hidden, nil
		}
		if err != nil {
			return "", 0, fmt.Errorf("read output: %w", err)
		}
	}
}

func (p *Printer) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return p.write(string(data) + "\n")
}

func (p *Printer) write(s string) error {
	if _, err := io.WriteString(p.w, s); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
