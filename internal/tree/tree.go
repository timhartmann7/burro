// Package tree finds the project root and hashes the working tree the way
// git would commit it, without touching the repository's index.
package tree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/timhartmann7/burro/internal/output"
)

// gitTimeout bounds one git call; hashing a large tree reads every changed
// file.
const gitTimeout = 2 * time.Minute

// ShortLen is how many characters of the hash output shows.
const ShortLen = 7

// Repo is the git repository a command runs in.
type Repo struct {
	root string
	env  []string
}

// Open finds the repository that holds dir. env is the environment of every
// git call.
func Open(ctx context.Context, dir string, env []string) (*Repo, error) {
	r := &Repo{root: dir, env: env}
	root, err := r.git(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("%s is not inside a git repository", dir),
			"run burro inside the project's git repository")
	}
	r.root = root
	return r, nil
}

// Root is the top folder of the working tree.
func (r *Repo) Root() string {
	return r.root
}

// Hash returns the tree hash of the working tree: tracked, modified and new
// files that .gitignore doesn't exclude. It stages into a copy of the index,
// so the repository's own index stays byte for byte the same.
func (r *Repo) Hash(ctx context.Context) (string, error) {
	tmp, err := os.MkdirTemp("", "burro-index-")
	if err != nil {
		return "", hashError(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	index, err := r.gitPath(ctx, "index")
	if err != nil {
		return "", hashError(err)
	}
	// Starting from the real index reuses its stat data, so unchanged files
	// are not read again.
	data, err := os.ReadFile(filepath.Clean(index))
	switch {
	case err == nil:
		err = writeIn(tmp, "index", data)
	case errors.Is(err, fs.ErrNotExist):
		err = nil
	}
	if err != nil {
		return "", hashError(err)
	}
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := r.git(ctx, env, "add", "--all"); err != nil {
		return "", hashError(err)
	}
	hash, err := r.git(ctx, env, "write-tree")
	if err != nil {
		return "", hashError(err)
	}
	return hash, nil
}

// writeIn writes name inside dir and nowhere else.
func writeIn(dir, name string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	return errors.Join(root.WriteFile(name, data, 0o600), root.Close())
}

// Ignored returns what git leaves out of the tree, so a sync sends exactly
// the files the hash covers: untracked paths that ignore rules match, a
// whole folder as one path with a trailing slash, and the ignored folders
// that still hold tracked files. rsync can't read .gitignore the way git
// does, so git decides.
func (r *Repo) Ignored(ctx context.Context) (paths, folders []string, err error) {
	out, err := r.run(ctx, nil, nil, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil, nil, ignoredError(err)
	}
	paths = names(out)
	out, err = r.run(ctx, nil, nil, "ls-files", "-z", "--cached", "--ignored", "--exclude-standard")
	if err != nil {
		return nil, nil, ignoredError(err)
	}
	parents := parentFolders(names(out))
	if len(parents) == 0 {
		return paths, nil, nil
	}
	out, err = r.run(ctx, nil, bytes.NewReader(parents), "check-ignore", "-z", "--stdin", "--no-index")
	var exit *exec.ExitError
	// Exit 1 means none of the folders is ignored.
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, nil, ignoredError(err)
	}
	return paths, names(out), nil
}

// parentFolders lists every folder above the given paths once, with a
// trailing slash so git matches folder rules, NUL-terminated for its stdin.
func parentFolders(paths []string) []byte {
	var list []byte
	seen := map[string]bool{}
	for _, path := range paths {
		for i, c := range path {
			if c == '/' && !seen[path[:i+1]] {
				seen[path[:i+1]] = true
				list = append(append(list, path[:i+1]...), 0)
			}
		}
	}
	return list
}

// names splits NUL-terminated git output.
func names(out []byte) []string {
	var list []string
	for name := range strings.SplitSeq(string(out), "\x00") {
		if name != "" {
			list = append(list, name)
		}
	}
	return list
}

// ExcludeFile returns the absolute path of .git/info/exclude, or empty when
// the repository has none.
func (r *Repo) ExcludeFile(ctx context.Context) (string, error) {
	path, err := r.gitPath(ctx, "info/exclude")
	if err != nil {
		return "", output.NewError(output.ExitServer, err.Error(), "check that git works in "+r.root)
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return path, nil
}

func (r *Repo) gitPath(ctx context.Context, name string) (string, error) {
	path, err := r.git(ctx, nil, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.root, path)
	}
	return path, nil
}

func (r *Repo) git(ctx context.Context, env []string, args ...string) (string, error) {
	out, err := r.run(ctx, env, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// run is git with stdin and its output untouched, so names that start or
// end with a space survive.
func (r *Repo) run(ctx context.Context, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	//nolint:gosec // G204: git with an argument slice, no shell in between.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.root
	cmd.Env = append(append([]string{}, r.env...), env...)
	cmd.Stdin = stdin
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out, &gitError{args: args, exit: exit}
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// gitError is git's own failure: the message is what git printed, and the
// exit status stays reachable through errors.As.
type gitError struct {
	args []string
	exit *exec.ExitError
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.args, " "), strings.TrimSpace(string(e.exit.Stderr)))
}

func (e *gitError) Unwrap() error {
	return e.exit
}

func ignoredError(err error) error {
	return output.NewError(output.ExitServer, "ignored files: "+err.Error(),
		"check that git status works in the project")
}

func hashError(err error) error {
	return output.NewError(output.ExitServer, "tree hash: "+err.Error(),
		"check that git status works in the project")
}
