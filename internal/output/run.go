package output

import (
	"fmt"
	"strings"
	"time"
)

// Run and step states, as status.json and every summary spell them.
const (
	StateRunning = "running"
	StateDone    = "done"
	// StateCanceled is also the reason of a canceled run and the detail of
	// the step the cancel stopped.
	StateCanceled = "cancelled" //nolint:misspell // status.json and docs/reference/cli.md spell the state this way.
	StatePending  = "pending"
	StateOK       = "ok"
	StateFail     = "fail"
	StateSkip     = "skip"
	StateNotRun   = "not run"
)

// Started is a run the runner just took over.
type Started struct {
	Run      string   `json:"run"`
	Pipeline string   `json:"pipeline"`
	Tree     string   `json:"tree"`
	Steps    []string `json:"steps"`
	Synced   int      `json:"synced"`
	Seconds  float64  `json:"seconds"`
}

// Started prints the run line, the sync line and the steps that will run.
func (p *Printer) Started(s *Started, took time.Duration) error {
	s.Seconds = took.Round(100 * time.Millisecond).Seconds()
	if p.json {
		return p.writeJSON(s)
	}
	files := "files"
	if s.Synced == 1 {
		files = "file"
	}
	return p.write(fmt.Sprintf("%s %s · tree %s · %s\nsynced %d %s in %s\nsteps: %s\n",
		s.Run, s.Pipeline, s.Tree, StateRunning, s.Synced, files, Duration(took), strings.Join(s.Steps, " ")))
}

// Summary is the state of one run: what status, wait and cancel print.
type Summary struct {
	Run      string        `json:"run"`
	Pipeline string        `json:"pipeline"`
	Tree     string        `json:"tree"`
	State    string        `json:"state"`
	Exit     *int          `json:"exit"`
	Seconds  int           `json:"seconds"`
	Reason   string        `json:"reason,omitempty"`
	Local    string        `json:"local_tree,omitempty"`
	Steps    []SummaryStep `json:"steps"`
}

// SummaryStep is one step of a summary. A failed step carries the tail of
// its clean log and the command that shows more.
type SummaryStep struct {
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Seconds int      `json:"seconds"`
	Detail  string   `json:"detail,omitempty"`
	Tail    []string `json:"tail,omitempty"`
	More    string   `json:"more,omitempty"`
}

// Summary prints the run line, the local tree line when the trees differ,
// one line per step and the log tail of each failed step.
func (p *Printer) Summary(s *Summary) error {
	if p.json {
		return p.writeJSON(s)
	}
	var b strings.Builder
	b.WriteString(s.Headline() + "\n")
	if s.Local != "" {
		fmt.Fprintf(&b, "local tree %s differs from run tree\n", s.Local)
	}
	nameWidth, stateWidth := 0, 0
	for _, st := range s.Steps {
		nameWidth = max(nameWidth, len(st.Name))
		stateWidth = max(stateWidth, len(st.State))
	}
	for _, st := range s.Steps {
		// Padding goes outside the color codes, so columns line up in both
		// modes.
		state := p.state(st.State) + strings.Repeat(" ", stateWidth-len(st.State))
		cols := []string{fmt.Sprintf("%-*s", nameWidth, st.Name), state}
		if st.State == StateOK || st.State == StateFail || st.State == StateRunning {
			cols = append(cols, Duration(time.Duration(st.Seconds)*time.Second))
		}
		if st.Detail != "" {
			cols = append(cols, st.Detail)
		}
		b.WriteString(strings.TrimRight(strings.Join(cols, "  "), " ") + "\n")
		for _, line := range st.Tail {
			b.WriteString("  " + line + "\n")
		}
		if st.More != "" {
			b.WriteString("more: " + st.More + "\n")
		}
	}
	return p.write(b.String())
}

// Headline is the first line of run, wait, status and cancel:
// `<id> <pipeline> · tree <hash> · <state>`.
func (s *Summary) Headline() string {
	state := StateRunning
	if s.State != StateRunning {
		state = fmt.Sprintf("%s · exit %d", Duration(time.Duration(s.Seconds)*time.Second), *s.Exit)
		if s.Reason != "" {
			state += " · " + s.Reason
		}
	}
	return fmt.Sprintf("%s %s · tree %s · %s", s.Run, s.Pipeline, s.Tree, state)
}

func (p *Printer) state(s string) string {
	switch s {
	case StateOK:
		return p.palette.OK(s)
	case StateFail:
		return p.palette.Fail("FAIL")
	case StateSkip:
		return p.palette.Skip(s)
	default:
		return s
	}
}

// Log is the clean log of one step, or the matches of --grep in it.
type Log struct {
	Run     string   `json:"run"`
	Step    string   `json:"step"`
	Grep    string   `json:"grep,omitempty"`
	Lines   []string `json:"lines"`
	Matches int      `json:"matches"`
	Hidden  int      `json:"hidden"`
	More    string   `json:"more,omitempty"`
}

// Log prints the lines, with a match count header for --grep, and a last
// line with the command that shows what was cut.
func (p *Printer) Log(l *Log) error {
	if p.json {
		return p.writeJSON(l)
	}
	var b strings.Builder
	if l.Grep != "" {
		matches := "matches"
		if l.Matches == 1 {
			matches = "match"
		}
		fmt.Fprintf(&b, "%s %s · %d %s\n", l.Run, l.Step, l.Matches, matches)
	}
	for _, line := range l.Lines {
		b.WriteString(line + "\n")
	}
	if l.Hidden > 0 {
		what := "earlier lines"
		switch {
		case l.Grep != "" && l.Hidden == 1:
			what = "more match"
		case l.Grep != "":
			what = "more matches"
		case l.Hidden == 1:
			what = "earlier line"
		}
		fmt.Fprintf(&b, "+%d %s: %s\n", l.Hidden, what, l.More)
	}
	return p.write(b.String())
}

// Progress is where a running run is, for the human progress line.
type Progress struct {
	Step    string `json:"step"`
	Index   int    `json:"index"`
	Total   int    `json:"total"`
	Seconds int    `json:"seconds"`
}

// Line is the progress line wait redraws on stderr in human mode.
func (g Progress) Line() string {
	return fmt.Sprintf("%s · step %d of %d · %s · Ctrl+C is safe, the run keeps going",
		g.Step, g.Index, g.Total, Duration(time.Duration(g.Seconds)*time.Second))
}
