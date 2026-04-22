package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initTestRepo creates a temporary git repo with an initial commit on "main".
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "test")

	// Create initial commit.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hello\n"), 0644))
	run("add", ".")
	run("commit", "-m", "initial")

	return dir
}

func TestLocalBranches(t *testing.T) {
	dir := initTestRepo(t)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
	}

	// Initially only "main".
	branches, err := LocalBranches(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, branches)

	// Create additional branches.
	run("branch", "feature-a")
	run("branch", "feature-b")

	branches, err = LocalBranches(dir)
	require.NoError(t, err)
	sort.Strings(branches)
	assert.Equal(t, []string{"feature-a", "feature-b", "main"}, branches)
}

func TestResolveMergeBase(t *testing.T) {
	dir := initTestRepo(t)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
	}

	// Get the initial commit SHA (this will be the merge base).
	initialSHA, err := MergeBase(dir, "main")
	require.NoError(t, err)

	// Create a feature branch with a new commit.
	run("checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package feature\n"), 0644))
	run("add", ".")
	run("commit", "-m", "feature commit")

	// ResolveMergeBase with explicit branch should return the initial commit.
	mb, err := ResolveMergeBase(dir, "main")
	require.NoError(t, err)
	assert.Equal(t, initialSHA, mb)

	// ResolveMergeBase with empty string should fall back to DefaultBranch.
	// Since there's no origin, DefaultBranch falls back to "main" (local).
	mb2, err := ResolveMergeBase(dir, "")
	require.NoError(t, err)
	assert.Equal(t, initialSHA, mb2)
}

func TestDiff_WithBaseBranch(t *testing.T) {
	dir := initTestRepo(t)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
	}

	// Create two branches from main with different content.
	run("checkout", "-b", "base-a")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644))
	run("add", ".")
	run("commit", "-m", "base-a commit")

	run("checkout", "main")
	run("checkout", "-b", "base-b")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0644))
	run("add", ".")
	run("commit", "-m", "base-b commit")

	// Create a feature branch off base-a.
	run("checkout", "base-a")
	run("checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package feature\n"), 0644))
	run("add", ".")
	run("commit", "-m", "feature commit")

	// Diff against base-a: should only show feature.go (not a.go, since a.go is in base-a).
	diffA, err := Diff(dir, DiffBranch, "base-a")
	require.NoError(t, err)
	filesA := ParseDiffToFiles(diffA)
	assert.Len(t, filesA, 1)
	assert.Equal(t, "feature.go", filesA[0].Filename)

	// Diff against main: should show both a.go and feature.go (since main doesn't have either).
	diffMain, err := Diff(dir, DiffBranch, "main")
	require.NoError(t, err)
	filesMain := ParseDiffToFiles(diffMain)
	assert.Len(t, filesMain, 2)
	filenames := []string{filesMain[0].Filename, filesMain[1].Filename}
	sort.Strings(filenames)
	assert.Equal(t, []string{"a.go", "feature.go"}, filenames)

	// Diff in working mode ignores baseBranch.
	diffWorking, err := Diff(dir, DiffWorking, "base-a")
	require.NoError(t, err)
	assert.Empty(t, diffWorking) // no unstaged changes
}

func TestFetchRef_InvalidRemote(t *testing.T) {
	dir := initTestRepo(t)

	// Fetching from a nonexistent remote should return an error.
	err := FetchRef(dir, "nonexistent", "main")
	assert.Error(t, err)
}
