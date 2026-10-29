package report

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/timhartmann7/burro/internal/output"
)

// tailLines is how much of a failed step's clean log a summary shows.
const tailLines = 30

// Summary turns s into what status and wait print. With tails, every failed
// step carries the last lines of its clean log, all of them together within
// maxBytes. now gives a running run its duration so far.
func (p *Project) Summary(s *Status, c *Cleaner, tails bool, maxBytes int, now time.Time) (*output.Summary, error) {
	end := now
	if s.Finished != nil {
		end = *s.Finished
	}
	sum := &output.Summary{
		Run: s.Run, Pipeline: s.Pipeline, Tree: s.Tree, State: s.State, Exit: s.Exit,
		Seconds: int(end.Sub(s.Started).Seconds()), Reason: s.Reason,
		Steps: make([]output.SummaryStep, 0, len(s.Steps)),
	}
	failed := 0
	for _, st := range s.Steps {
		if st.State == output.StateFail {
			failed++
		}
	}
	for _, st := range s.Steps {
		step := output.SummaryStep{Name: st.Name, State: st.State, Seconds: st.Seconds, Detail: st.Detail}
		if tails && st.State == output.StateFail {
			lines, err := p.tail(s.Run, st.Name, c, maxBytes/failed)
			if err != nil {
				return nil, err
			}
			step.Tail = lines
			step.More = fmt.Sprintf("burro log %s --step %s", s.Run, st.Name)
		}
		sum.Steps = append(sum.Steps, step)
	}
	return sum, nil
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
