package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"github.com/timhartmann7/burro/internal/output"
)

const (
	// maxPull caps what one pull copies, so a pattern that matches a whole
	// build folder fails before anything moves.
	maxPull = 256 << 20
	// maxPullFiles caps how many files one pull lists in its reply.
	maxPullFiles = 5000
)

// errPullFull ends the walk once a pull is over its limits.
var errPullFull = errors.New("pull is over its limits")

// files lists the regular files of the project folder that the patterns
// match, with everything inside a matched folder, for the client to copy.
// The patterns are fs.Glob patterns opened through os.Root, so nothing
// outside the project folder ever matches.
func (e *Executor) files(args []string) (*output.Pulled, error) {
	f := newFlags("files")
	if err := f.set.Parse(args); err != nil || f.path == "" || f.set.NArg() == 0 {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("agent files: bad arguments %q", args),
			"rerun with the same burro version")
	}
	dir, err := e.projectDir(f.path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open project folder: %w", err)
	}
	defer func() { _ = root.Close() }()
	list := &pullList{fsys: root.FS(), seen: map[string]bool{}, pulled: &output.Pulled{Files: []string{}}}
	for _, pattern := range f.set.Args() {
		if err := list.add(pattern); err != nil {
			return nil, err
		}
	}
	slices.Sort(list.pulled.Files)
	return list.pulled, nil
}

// pullList gathers the files of one pull.
type pullList struct {
	fsys   fs.FS
	seen   map[string]bool
	pulled *output.Pulled
}

func (l *pullList) add(pattern string) error {
	matches, err := fs.Glob(l.fsys, pattern)
	if err != nil {
		return output.NewError(output.ExitUsage, fmt.Sprintf("pattern %q is not valid", pattern),
			"use a path or a glob inside the project folder, for example 'test-results/*/*.png'")
	}
	if len(matches) == 0 {
		return output.NewError(output.ExitUsage, fmt.Sprintf("nothing in the project folder on the server matches %q", pattern),
			"pass a path relative to the project folder; burro exec -- ls shows what is there")
	}
	for _, m := range matches {
		err := fs.WalkDir(l.fsys, m, l.visit)
		if errors.Is(err, errPullFull) {
			return output.NewError(output.ExitUsage,
				fmt.Sprintf("%q matches over %d files or %d MiB", pattern, maxPullFiles, maxPull>>20),
				"narrow the pattern to the files you need, for example the failed test's folder")
		}
		if err != nil {
			return fmt.Errorf("list %s: %w", m, err)
		}
	}
	return nil
}

// visit takes regular files only: links and devices stay on the server.
func (l *pullList) visit(path string, d fs.DirEntry, err error) error {
	if err != nil || !d.Type().IsRegular() || l.seen[path] {
		return err
	}
	info, err := d.Info()
	if err != nil {
		return err
	}
	l.seen[path] = true
	l.pulled.Files = append(l.pulled.Files, path)
	l.pulled.Bytes += info.Size()
	if l.pulled.Bytes > maxPull || len(l.pulled.Files) > maxPullFiles {
		return errPullFull
	}
	return nil
}
