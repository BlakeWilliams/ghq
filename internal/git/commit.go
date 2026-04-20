package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// Commit creates a git commit with the given message.
func Commit(dir string, message string) error {
	cmd := exec.Command("git", "-C", dir, "commit", "-m", message)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Push pushes the current branch to origin.
func Push(dir string) error {
	cmd := exec.Command("git", "-C", dir, "push", "-u", "origin", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// CreatePR opens a pull request using the gh CLI.
func CreatePR(dir string, title string, body string) error {
	args := []string{"pr", "create", "--title", title, "--body", body}
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr create: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// StagedDiff returns the staged diff (--cached).
func StagedDiff(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "diff", "--cached")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff --cached: %w", err)
	}
	return string(out), nil
}

// HasStagedChanges returns true if there are staged changes.
func HasStagedChanges(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "diff", "--cached", "--quiet")
	return cmd.Run() != nil // exit 1 means there are changes
}

// HasUnpushedCommits returns true if the current branch has commits not yet pushed.
func HasUnpushedCommits(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "log", "--oneline", "@{u}..HEAD")
	out, err := cmd.Output()
	if err != nil {
		// No upstream set — treat as having unpushed commits.
		return true
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// HasOpenPR returns true if the current branch already has an open PR.
func HasOpenPR(dir string) bool {
	cmd := exec.Command("gh", "pr", "view", "--json", "state", "-q", ".state")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "OPEN"
}

// BranchDiff returns the diff of the current branch vs the default branch.
func BranchDiff(dir string) (string, error) {
	// Find the merge base with the default branch.
	base := exec.Command("gh", "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	base.Dir = dir
	baseOut, err := base.Output()
	defaultBranch := strings.TrimSpace(string(baseOut))
	if err != nil || defaultBranch == "" {
		defaultBranch = "main"
	}

	cmd := exec.Command("git", "-C", dir, "diff", defaultBranch+"...HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff branch: %w", err)
	}
	return string(out), nil
}

// BranchLog returns the commit log of the current branch vs the default branch.
func BranchLog(dir string) (string, error) {
	base := exec.Command("gh", "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	base.Dir = dir
	baseOut, err := base.Output()
	defaultBranch := strings.TrimSpace(string(baseOut))
	if err != nil || defaultBranch == "" {
		defaultBranch = "main"
	}

	cmd := exec.Command("git", "-C", dir, "log", "--oneline", defaultBranch+"..HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git log branch: %w", err)
	}
	return string(out), nil
}
