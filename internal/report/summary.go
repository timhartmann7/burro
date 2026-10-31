package report

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/timhartmann7/burro/internal/output"
)

const (
	// tailLines is how much of a failed step's clean log a summary shows.
	tailLines = 30
	// junitFolder holds the report copies of each step in a run folder.
	junitFolder = "junit"
)

// word finds the words of a test name; the longest one makes a grep that
// finds the test in the log.
var word = regexp.MustCompile(`\w+`)

// Summary turns s into what status, wait, failures and cancel print. With
// details, every step with JUnit reports gets its test counts and every
// failed step its failures, marked NEW or SEEN against the last finished
// run of the same pipeline; a failed step whose reports hold no failure
// gets the tail of its clean log instead. Everything fits in maxBytes. now
// gives a running run its duration so far.
func (p *Project) Summary(s *Status, c *Cleaner, details bool, maxBytes int, now time.Time) (*output.Summary, error) {
	end := now
	if s.Finished != nil {
		end = *s.Finished
	}
	sum := &output.Summary{
		Run: s.Run, Pipeline: s.Pipeline, Tree: s.Tree, State: s.State, Exit: s.Exit,
		Seconds: int(end.Sub(s.Started).Seconds()), Reason: s.Reason,
		Steps: make([]output.SummaryStep, 0, len(s.Steps)), Failures: []output.Failure{},
	}
	for _, st := range s.Steps {
		sum.Steps = append(sum.Steps, output.SummaryStep{Name: st.Name, State: st.State, Seconds: st.Seconds, Detail: st.Detail})
	}
	if !details {
		return sum, nil
	}
	b := &builder{p: p, sum: sum, seen: p.previous(s), room: maxBytes / 2}
	var tails []int
	for i, st := range s.Steps {
		hasFailures, err := b.step(i, st.State == output.StateFail)
		if err != nil {
			return nil, err
		}
		if st.State == output.StateFail && !hasFailures {
			tails = append(tails, i)
		}
	}
	for _, i := range tails {
		step := &sum.Steps[i]
		lines, err := p.tail(s.Run, step.Name, c, maxBytes/2/len(tails))
		if err != nil {
			return nil, err
		}
		step.Tail = lines
		step.More = fmt.Sprintf("burro log %s --step %s", s.Run, step.Name)
	}
	if len(sum.Failures) > 0 {
		sum.More = more(s.Run, &sum.Failures[0])
	}
	return sum, nil
}

// builder fills a summary from the reports of its steps.
type builder struct {
	p    *Project
	sum  *output.Summary
	seen map[string]bool
	// room is what the failures may still take of the reply.
	room int
}

// step reads the reports of step i: their counts always, their failures
// when the step failed. It tells whether there were failures to show.
func (b *builder) step(i int, failed bool) (bool, error) {
	step := &b.sum.Steps[i]
	rep, unreadable, err := b.p.reports(b.sum.Run, step.Name)
	// Only a failed step says so; a passing one has nothing to explain.
	step.Unreadable = failed && unreadable
	if err != nil || rep == nil {
		return false, err
	}
	step.Tests = &output.Tests{Total: rep.total, Passed: rep.total - rep.failed - rep.skipped, Failed: rep.failed, Skipped: rep.skipped}
	if !failed || len(rep.cases) == 0 {
		return false, nil
	}
	merged := map[string]int{}
	for _, c := range rep.cases {
		if at, ok := merged[c.key()]; ok {
			b.sum.Failures[at].Count++
			continue
		}
		size := len(c.File) + len(c.Name) + len(c.Message) + len(c.Frame)
		if size > b.room {
			b.sum.Hidden++
			continue
		}
		b.room -= size
		kind := output.KindNew
		if b.seen[c.key()] {
			kind = output.KindSeen
		}
		merged[c.key()] = len(b.sum.Failures)
		b.sum.Failures = append(b.sum.Failures, output.Failure{
			Step: step.Name, Kind: kind, File: c.File, Line: c.Line, Name: c.Name, Count: 1, Message: c.Message, Frame: c.Frame,
		})
	}
	return true, nil
}

// more is the log command that finds failure f, by the longest word of its
// name.
func more(run string, f *output.Failure) string {
	cmd := fmt.Sprintf("burro log %s --step %s", run, f.Step)
	longest := ""
	for _, w := range word.FindAllString(f.Name, -1) {
		if len(w) > len(longest) {
			longest = w
		}
	}
	if longest != "" {
		cmd += " --grep " + longest
	}
	return cmd
}

// previous returns the failure keys of the last finished run before s of
// the same pipeline. A run with step arguments ran only some tests, so a
// failure it lacks may still be an old one; it is no baseline.
func (p *Project) previous(s *Status) map[string]bool {
	keys := map[string]bool{}
	m := runName.FindStringSubmatch(s.Run)
	if m == nil {
		return keys
	}
	n, _ := strconv.Atoi(m[1])
	for n--; n > 0; n-- {
		prev, err := p.Status("r" + strconv.Itoa(n))
		if err != nil || prev.Pipeline != s.Pipeline || prev.State == output.StateRunning || len(prev.Args) > 0 {
			continue
		}
		for _, st := range prev.Steps {
			// An unreadable report of an old run only costs its marks.
			if rep, _, _ := p.reports(prev.Run, st.Name); rep != nil {
				for _, c := range rep.cases {
					keys[c.key()] = true
				}
			}
		}
		return keys
	}
	return keys
}

// reports parses the report copies of one step. rep is nil when there is
// no readable copy; unreadable tells whether a copy was empty, broken or
// too big.
func (p *Project) reports(id, step string) (rep *junit, unreadable bool, err error) {
	dir := filepath.Join(p.RunDir(id), junitFolder, step)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("list reports of %s: %w", step, err)
	}
	for _, e := range entries {
		r, err := p.parseCopy(filepath.Join(dir, e.Name()))
		if errors.Is(err, errUnreadable) {
			unreadable = true
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if rep == nil {
			rep = &junit{}
		}
		rep.total += r.total
		rep.failed += r.failed
		rep.skipped += r.skipped
		rep.cases = append(rep.cases, r.cases...)
	}
	return rep, unreadable, nil
}

func (p *Project) parseCopy(path string) (*junit, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open report: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("read report: %w", err)
	}
	if info.Size() > maxReport {
		return nil, errUnreadable
	}
	return parseJUnit(f, p.dir)
}

// SaveReports copies the step's JUnit reports into the run folder: the
// files inside the project that match pattern and changed since the step
// started, with secrets masked. Older matches come from earlier runs. A
// report too big to parse is copied only as far as that shows.
func (p *Project) SaveReports(id, step, pattern string, since time.Time, secrets [][]byte) error {
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		return fmt.Errorf("open project folder: %w", err)
	}
	defer func() { _ = root.Close() }()
	matches, err := fs.Glob(root.FS(), pattern)
	if err != nil {
		return fmt.Errorf("find reports of %s: %w", step, err)
	}
	dir := filepath.Join(p.RunDir(id), junitFolder, step)
	saved := 0
	for _, m := range matches {
		// Stat goes through root, so a link out of the project fails here.
		info, err := root.Stat(m)
		if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(since) {
			continue
		}
		saved++
		if err := copyReport(root, m, filepath.Join(dir, strconv.Itoa(saved)+".xml"), secrets); err != nil {
			return err
		}
	}
	return nil
}

func copyReport(root *os.Root, name, dst string, secrets [][]byte) error {
	src, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("open report %s: %w", name, err)
	}
	defer func() { _ = src.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("create report folder: %w", err)
	}
	out, err := os.OpenFile(filepath.Clean(dst), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("copy report %s: %w", name, err)
	}
	m := NewMasker(out, secrets)
	_, err = io.Copy(m, io.LimitReader(src, maxReport+1))
	err = errors.Join(err, m.Flush(), out.Close())
	if err != nil {
		return fmt.Errorf("copy report %s: %w", name, err)
	}
	return nil
}

func (p *Project) tail(id, step string, c *Cleaner, maxBytes int) ([]string, error) {
	f, err := os.Open(p.LogFile(id, step))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open log of %s: %w", step, err)
	}
	defer func() { _ = f.Close() }()
	lines, _, err := c.Tail(f, tailLines, maxBytes)
	return lines, err
}
