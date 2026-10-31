package output

import (
	"fmt"
	"slices"
	"strconv"
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

// Kinds of a failure: absent from the last finished run of the same
// pipeline, or failing there too.
const (
	KindNew  = "NEW"
	KindSeen = "SEEN"
)

// Unreadable is the line a step shows when its JUnit report can't be
// parsed.
const Unreadable = "report unreadable"

// Started is a run the runner just took over.
type Started struct {
	Run      string   `json:"run"`
	Pipeline string   `json:"pipeline"`
	Tree     string   `json:"tree"`
	Steps    []string `json:"steps"`
	Args     []string `json:"args,omitempty"`
	Synced   int      `json:"synced"`
	Seconds  float64  `json:"seconds"`
	// Near are the steps whose last passing run took most of their
	// timeout, so this run may hit it.
	Near []NearTimeout `json:"near_timeout,omitempty"`
}

// NearTimeout is a step whose last passing run, Run, took Seconds of its
// Timeout seconds.
type NearTimeout struct {
	Step    string `json:"step"`
	Run     string `json:"run"`
	Seconds int    `json:"seconds"`
	Timeout int    `json:"timeout"`
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
	text := fmt.Sprintf("%s %s · tree %s · %s\nsynced %d %s in %s\nsteps: %s\n",
		s.Run, s.Pipeline, s.Tree, StateRunning, s.Synced, files, Duration(took), strings.Join(s.Steps, " "))
	if len(s.Args) > 0 {
		text += "args: " + strings.Join(s.Args, " ") + "\n"
	}
	for _, n := range s.Near {
		text += fmt.Sprintf("%s took %s in %s, its timeout is %s\n", n.Step,
			Duration(time.Duration(n.Seconds)*time.Second), n.Run, Duration(time.Duration(n.Timeout)*time.Second))
	}
	return p.write(text)
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
	Failures []Failure     `json:"failures"`
	// Hidden counts the failures left out: those the server didn't send and
	// those past --max-lines.
	Hidden int `json:"hidden"`
	// More is the command that shows more of the first failure.
	More string `json:"more,omitempty"`
	// limit is the --max-lines the summary was cut to.
	limit int
}

// SummaryStep is one step of a summary. A failed step without failures
// from a report carries the tail of its clean log and the command that
// shows more.
type SummaryStep struct {
	Name       string   `json:"name"`
	State      string   `json:"state"`
	Seconds    int      `json:"seconds"`
	Tests      *Tests   `json:"tests,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	Unreadable bool     `json:"report_unreadable,omitempty"`
	Tail       []string `json:"tail,omitempty"`
	More       string   `json:"more,omitempty"`
}

// Tests counts the tests in a step's JUnit reports.
type Tests struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Failure is one failed test, merged over the runs of it that fail the same
// way, such as one test in three Playwright projects.
type Failure struct {
	Step    string `json:"step"`
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Message string `json:"message"`
	Frame   string `json:"frame"`
}

// lines is how many lines the failure takes in a summary.
func (f *Failure) lines() int {
	n := 1
	if f.Message != "" {
		n++
	}
	if f.Frame != "" {
		n++
	}
	return n
}

// moreFactor is how much more --max-lines the cut line asks for.
const moreFactor = 10

// Summary prints the run line, the local tree line when the trees differ,
// one line per step and under each failed step its failures or log tail.
// With maxLines above 0 everything fits in that many lines; the last line
// then says how many failures stayed out and which command shows them.
func (p *Printer) Summary(s *Summary, maxLines int) error {
	s = s.fit(maxLines)
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
	for i := range s.Steps {
		b.WriteString(p.stepLine(&s.Steps[i], nameWidth, stateWidth) + "\n")
		p.block(&b, s, &s.Steps[i])
	}
	if s.More != "" {
		b.WriteString("more: " + s.More + "\n")
	}
	if s.Hidden > 0 && s.limit > 0 {
		fmt.Fprintf(&b, "+%d more: burro failures %s --max-lines %d\n", s.Hidden, s.Run, moreFactor*s.limit)
	}
	return p.write(b.String())
}

// block prints what goes under a step: its failures, or the unreadable
// line, the log tail and the more line.
func (p *Printer) block(b *strings.Builder, s *Summary, st *SummaryStep) {
	for i := range s.Failures {
		if s.Failures[i].Step == st.Name {
			p.failure(b, &s.Failures[i])
		}
	}
	if st.Unreadable {
		b.WriteString("  " + Unreadable + "\n")
	}
	for _, line := range st.Tail {
		b.WriteString("  " + line + "\n")
	}
	if st.More != "" {
		b.WriteString("more: " + st.More + "\n")
	}
}

// stepLine is name, state, test counts, duration and detail of one step.
// Padding goes outside the color codes, so columns line up in both modes.
func (p *Printer) stepLine(st *SummaryStep, nameWidth, stateWidth int) string {
	state := p.state(st.State) + strings.Repeat(" ", stateWidth-len(st.State))
	cols := []string{fmt.Sprintf("%-*s", nameWidth, st.Name), state}
	if st.Tests != nil {
		cols = append(cols, fmt.Sprintf("%d/%d", st.Tests.Passed, st.Tests.Total))
	}
	if st.State == StateOK || st.State == StateFail || st.State == StateRunning {
		cols = append(cols, Duration(time.Duration(st.Seconds)*time.Second))
	}
	if st.Detail != "" {
		cols = append(cols, st.Detail)
	}
	return strings.TrimRight(strings.Join(cols, "  "), " ")
}

// failure prints one failure: kind, place, name and count, then the first
// message line and the frame under the place.
func (p *Printer) failure(b *strings.Builder, f *Failure) {
	kind := p.palette.Fail(f.Kind)
	if f.Kind == KindSeen {
		kind = p.palette.Skip(f.Kind)
	}
	kind += strings.Repeat(" ", len(KindSeen)-len(f.Kind))
	place := f.File
	if f.Line > 0 {
		place += ":" + strconv.Itoa(f.Line)
	}
	line := "  " + kind + "  " + place + "  " + f.Name
	if f.Count > 1 {
		line += "  x" + strconv.Itoa(f.Count)
	}
	b.WriteString(line + "\n")
	const indent = "        "
	if f.Message != "" {
		b.WriteString(indent + f.Message + "\n")
	}
	if f.Frame != "" {
		b.WriteString(indent + "at " + f.Frame + "\n")
	}
}

// fit returns s cut to maxLines lines. The run line, the step lines and the
// more lines always stay; failures and log tails fill the rest in order.
// Once one doesn't fit, the ones after it stay out too, so what shows is
// never out of order. A log tail that doesn't fit loses its first lines.
func (s *Summary) fit(maxLines int) *Summary {
	if maxLines <= 0 {
		if s.Failures != nil {
			return s
		}
		out := *s
		out.Failures = []Failure{}
		return &out
	}
	fixed := 1 + len(s.Steps)
	if s.Local != "" {
		fixed++
	}
	if s.More != "" {
		fixed++
	}
	cut := s.cut(maxLines - fixed)
	if cut.Hidden > s.Hidden {
		// The line that says so takes a line too.
		cut = s.cut(maxLines - fixed - 1)
	}
	cut.limit = maxLines
	return cut
}

func (s *Summary) cut(room int) *Summary {
	out := *s
	out.Steps = slices.Clone(s.Steps)
	out.Failures = []Failure{}
	full := false
	for i := range out.Steps {
		st := &out.Steps[i]
		room, full = out.fitFailures(s.Failures, st.Name, room, full)
		if len(st.Tail) == 0 && !st.Unreadable {
			continue
		}
		used, shown, trimmed := st.fitTail(room, full)
		if !shown {
			out.Hidden++
		}
		full = full || !shown || trimmed
		room -= used
	}
	return &out
}

// fitFailures adds the failures of step that fit in room lines and counts
// the others as hidden. It returns the room left and whether one didn't
// fit.
func (s *Summary) fitFailures(all []Failure, step string, room int, full bool) (int, bool) {
	for _, f := range all {
		if f.Step != step {
			continue
		}
		if full || f.lines() > room {
			full = true
			s.Hidden++
			continue
		}
		s.Failures = append(s.Failures, f)
		room -= f.lines()
	}
	return room, full
}

// lines is how many lines the block under a step without failures takes:
// the unreadable line, the tail and the more line.
func (st *SummaryStep) lines() int {
	n := len(st.Tail)
	if st.Unreadable {
		n++
	}
	if st.More != "" {
		n++
	}
	return n
}

// fitTail cuts the block under a step to room lines, keeping the end of the
// tail, and returns the lines it uses and whether it shows and was cut. A
// block that can't keep one tail line, or comes after one that didn't fit,
// goes whole.
func (st *SummaryStep) fitTail(room int, full bool) (used int, shown, trimmed bool) {
	head := st.lines() - len(st.Tail)
	if full || head+min(1, len(st.Tail)) > room {
		st.Tail, st.More, st.Unreadable = nil, "", false
		return 0, false, false
	}
	if keep := room - head; keep < len(st.Tail) {
		st.Tail = st.Tail[len(st.Tail)-keep:]
		trimmed = true
	}
	return st.lines(), true, trimmed
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

// Cleaned is what clean removed for one run.
type Cleaned struct {
	Run        string `json:"run"`
	Containers int    `json:"containers"`
	Networks   int    `json:"networks"`
	Volumes    int    `json:"volumes"`
	Secrets    bool   `json:"secrets"`
}

// Cleaned prints one line: the run and what was removed.
func (p *Printer) Cleaned(c *Cleaned) error {
	if p.json {
		return p.writeJSON(c)
	}
	var parts []string
	for _, n := range []struct {
		count int
		what  string
	}{{c.Containers, "container"}, {c.Networks, "network"}, {c.Volumes, "volume"}} {
		if n.count == 1 {
			parts = append(parts, "1 "+n.what)
		} else if n.count > 1 {
			parts = append(parts, fmt.Sprintf("%d %ss", n.count, n.what))
		}
	}
	if c.Secrets {
		parts = append(parts, "the secrets folder")
	}
	if len(parts) == 0 {
		return p.write(c.Run + " · nothing to remove\n")
	}
	list := strings.Join(parts, ", ")
	if len(parts) > 1 {
		list = strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
	return p.write(c.Run + " · removed " + list + "\n")
}

// What init did to a file.
const (
	Created   = "created"
	Updated   = "updated"
	Unchanged = "unchanged"
)

// InitFile is one file init looked at: what it did and what changed.
type InitFile struct {
	Name   string `json:"file"`
	Change string `json:"change"`
	Detail string `json:"detail,omitempty"`
}

// Init prints one line per file: what init did, the file, what changed.
func (p *Printer) Init(files []InitFile) error {
	if p.json {
		return p.writeJSON(struct {
			Files []InitFile `json:"files"`
		}{files})
	}
	width := 0
	for _, f := range files {
		width = max(width, len(f.Name))
	}
	var b strings.Builder
	for _, f := range files {
		line := fmt.Sprintf("%-*s  %-*s   %s", len(Unchanged), f.Change, width, f.Name, f.Detail)
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	return p.write(b.String())
}

// Rules prints the agent rules block as it is, or as one JSON object.
func (p *Printer) Rules(block string) error {
	if p.json {
		return p.writeJSON(struct {
			Block string `json:"block"`
		}{block})
	}
	return p.write(block)
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
