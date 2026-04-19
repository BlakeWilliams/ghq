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
