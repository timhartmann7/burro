package tree_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
	"github.com/timhartmann7/burro/internal/tree"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acceptance: S1-11
func TestHashMatchesCleanCopy(t *testing.T) {
	ctx := context.Background()
	root := testutil.GitRepo(t, map[string]string{
		".gitignore":    "node_modules/\n*.log\n",
		"a.txt":         "a\n",
		"gone.txt":      "bye\n",
		"old name.txt":  "renamed\n",
		"src/main.go":   "package main\n",
		"src/staged.go": "package src\n",
	})
	// Unstaged, staged, new, deleted, renamed and ignored changes at once.
	write(t, root, "a.txt", "a changed\n")
	write(t, root, "src/staged.go", "package src // staged\n")
	testutil.Git(t, root, "add", "src/staged.go")
	write(t, root, "new é file.txt", "new\n")
	write(t, root, "node_modules/x/index.js", "ignored\n")
	write(t, root, "debug.log", "ignored\n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "old name.txt"), filepath.Join(root, "new name.txt")); err != nil {
		t.Fatal(err)
	}

	indexPath := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := tree.Open(ctx, filepath.Join(root, "src"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Root() != root {
		t.Fatalf("Root = %q, want %q", repo.Root(), root)
	}
	hash, err := repo.Hash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the repository's index changed")
	}

	// A clean copy: a fresh repository with the same files, staged by git.
	clean := testutil.GitRepo(t, map[string]string{
		".gitignore":     "node_modules/\n*.log\n",
		"a.txt":          "a changed\n",
		"new name.txt":   "renamed\n",
		"new é file.txt": "new\n",
		"src/main.go":    "package main\n",
		"src/staged.go":  "package src // staged\n",
	})
	if want := testutil.Git(t, clean, "write-tree"); hash != want {
		t.Fatalf("hash %s, clean copy %s", hash, want)
	}
	again, err := repo.Hash(ctx)
	if err != nil || again != hash {
		t.Fatalf("second hash %s, %v", again, err)
	}
}

func TestHashFreshRepository(t *testing.T) {
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, "init", "-q")
	write(t, dir, "only.txt", "x\n")
	repo, err := tree.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := repo.Hash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "index")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hashing created the repository's index")
	}
	testutil.Git(t, dir, "add", "-A")
	if want := testutil.Git(t, dir, "write-tree"); hash != want {
		t.Fatalf("hash %s, want %s", hash, want)
	}
}

func TestOpenOutsideGit(t *testing.T) {
	_, err := tree.Open(context.Background(), t.TempDir(), []string{"GIT_CEILING_DIRECTORIES=/"})
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != output.ExitUsage {
		t.Fatalf("got %v, want exit 4", err)
	}
}

func TestExcludeFile(t *testing.T) {
	ctx := context.Background()
	root := testutil.GitRepo(t, nil)
	repo, err := tree.Open(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".git", "info", "exclude")
	if got, err := repo.ExcludeFile(ctx); err != nil || got != want {
		t.Fatalf("ExcludeFile = %q, %v; want %q", got, err, want)
	}
	if err := os.Remove(want); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.ExcludeFile(ctx); err != nil || got != "" {
		t.Fatalf("without the file: %q, %v", got, err)
	}

	// A worktree has a .git file, and git knows where its exclude lives.
	wt := filepath.Join(t.TempDir(), "wt")
	testutil.Git(t, root, "worktree", "add", "-q", wt)
	wtRepo, err := tree.Open(ctx, wt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wtRepo.ExcludeFile(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHashBrokenIndex(t *testing.T) {
	ctx := context.Background()
	root := testutil.GitRepo(t, map[string]string{"a": "a"})
	repo, err := tree.Open(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = repo.Hash(ctx)
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != output.ExitServer {
		t.Fatalf("got %v, want exit 2", err)
	}
}
