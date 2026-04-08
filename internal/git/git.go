package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/blakewilliams/ghq/internal/github"
)

// DiffMode controls what git diff compares.
type DiffMode int

const (
	DiffWorking DiffMode = iota // unstaged + staged vs HEAD
	DiffStaged                  // staged only (--cached)
	DiffBranch                  // current branch vs default branch
)

func (m DiffMode) String() string {
	switch m {
	case DiffWorking:
		return "Working Tree"
	case DiffStaged:
		return "Staged"
	case DiffBranch:
		return "Branch"
	}
	return "Unknown"
}

// IsGitRepo returns true if dir is inside a git repository.
func IsGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// RepoRoot returns the root directory of the git repository containing dir.
func RepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentBranch returns the current branch name.
func CurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git current branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DefaultBranch tries to detect the default branch (main, master, etc.).
func DefaultBranch(dir string) (string, error) {
	// Try symbolic ref of origin/HEAD first.
	cmd := exec.Command("git", "-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	out, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(out))
		// refs/remotes/origin/main -> main
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1], nil
		}
	}

	// Fallback: check if main or master exists.
	for _, branch := range []string{"main", "master"} {
		cmd = exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/heads/"+branch)
		if err := cmd.Run(); err == nil {
			return branch, nil
		}
	}

	return "main", nil // default fallback
}

// Diff runs git diff and returns the raw unified diff output.
func Diff(dir string, mode DiffMode) (string, error) {
	var args []string

	switch mode {
	case DiffWorking:
		args = []string{"-C", dir, "diff", "HEAD", "--no-color"}
	case DiffStaged:
		args = []string{"-C", dir, "diff", "--cached", "--no-color"}
	case DiffBranch:
		defaultBranch, err := DefaultBranch(dir)
		if err != nil {
			return "", err
		}
		// Find merge base to get clean branch diff.
		mbCmd := exec.Command("git", "-C", dir, "merge-base", defaultBranch, "HEAD")
		mbOut, err := mbCmd.Output()
		if err != nil {
			return "", fmt.Errorf("git merge-base: %w", err)
		}
		mergeBase := strings.TrimSpace(string(mbOut))
		args = []string{"-C", dir, "diff", mergeBase + "..HEAD", "--no-color"}
	}

	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		// git diff returns exit 1 when there are diffs in some cases
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return string(out), nil
		}
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(out), nil
}

// FileContent reads a file from the working tree.
func FileContent(dir string, path string) (string, error) {
	fullPath := filepath.Join(dir, path)
	cmd := exec.Command("cat", fullPath)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

var diffHeaderRegex = regexp.MustCompile(`^diff --git a/(.*) b/(.*)`)

// ParseDiffToFiles splits raw git diff output into per-file PullRequestFile structs.
// The Patch field contains only the hunk content (starting from @@), suitable for ParsePatchLines.
func ParseDiffToFiles(rawDiff string) []github.PullRequestFile {
	if rawDiff == "" {
		return nil
	}

	lines := strings.Split(rawDiff, "\n")
	var files []github.PullRequestFile
	var currentFile *github.PullRequestFile
	var patchLines []string
	inHeader := false

	flushFile := func() {
		if currentFile != nil {
			currentFile.Patch = strings.Join(patchLines, "\n")
			// Count additions/deletions from patch.
			for _, l := range patchLines {
				if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
					currentFile.Additions++
				} else if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
					currentFile.Deletions++
				}
			}
			files = append(files, *currentFile)
			currentFile = nil
			patchLines = nil
		}
	}

	for _, line := range lines {
		if matches := diffHeaderRegex.FindStringSubmatch(line); matches != nil {
			flushFile()
			currentFile = &github.PullRequestFile{
				Filename: matches[2],
				Status:   "modified",
			}
			if matches[1] != matches[2] {
				currentFile.PreviousFilename = matches[1]
				currentFile.Status = "renamed"
			}
			inHeader = true
			continue
		}

		if currentFile == nil {
			continue
		}

		// Skip diff header lines (---, +++, index, mode lines) until we hit a hunk.
		if inHeader {
			if strings.HasPrefix(line, "@@") {
				inHeader = false
				patchLines = append(patchLines, line)
			} else if strings.HasPrefix(line, "new file") {
				currentFile.Status = "added"
			} else if strings.HasPrefix(line, "deleted file") {
				currentFile.Status = "removed"
			} else if strings.HasPrefix(line, "Binary files") {
				currentFile.Patch = ""
			}
			continue
		}

		// Inside hunk content. A new hunk header also belongs to patch.
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "+") ||
			strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") ||
			line == "\\ No newline at end of file" {
			patchLines = append(patchLines, line)
		}
	}

	flushFile()
	return files
}

// DiffStat returns a short stat summary (e.g., "3 files changed, 10 insertions(+), 5 deletions(-)").
func DiffStat(dir string, mode DiffMode) (string, error) {
	var args []string
	switch mode {
	case DiffWorking:
		args = []string{"-C", dir, "diff", "HEAD", "--stat", "--no-color"}
	case DiffStaged:
		args = []string{"-C", dir, "diff", "--cached", "--stat", "--no-color"}
	case DiffBranch:
		defaultBranch, err := DefaultBranch(dir)
		if err != nil {
			return "", err
		}
		mbCmd := exec.Command("git", "-C", dir, "merge-base", defaultBranch, "HEAD")
		mbOut, err := mbCmd.Output()
		if err != nil {
			return "", fmt.Errorf("git merge-base: %w", err)
		}
		mergeBase := strings.TrimSpace(string(mbOut))
		args = []string{"-C", dir, "diff", mergeBase + "..HEAD", "--stat", "--no-color"}
	}

	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return string(out), nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// FilesAddedDeletedStats returns total additions and deletions counts.
func FilesAddedDeletedStats(files []github.PullRequestFile) (adds, dels int) {
	for _, f := range files {
		adds += f.Additions
		dels += f.Deletions
	}
	return
}

// NumStat returns per-file add/delete counts using git diff --numstat.
func NumStat(dir string, mode DiffMode) (map[string][2]int, error) {
	var args []string
	switch mode {
	case DiffWorking:
		args = []string{"-C", dir, "diff", "HEAD", "--numstat", "--no-color"}
	case DiffStaged:
		args = []string{"-C", dir, "diff", "--cached", "--numstat", "--no-color"}
	case DiffBranch:
		defaultBranch, err := DefaultBranch(dir)
		if err != nil {
			return nil, err
		}
		mbCmd := exec.Command("git", "-C", dir, "merge-base", defaultBranch, "HEAD")
		mbOut, err := mbCmd.Output()
		if err != nil {
			return nil, err
		}
		mergeBase := strings.TrimSpace(string(mbOut))
		args = []string{"-C", dir, "diff", mergeBase + "..HEAD", "--numstat", "--no-color"}
	}

	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	result := make(map[string][2]int)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		adds, _ := strconv.Atoi(parts[0])
		dels, _ := strconv.Atoi(parts[1])
		result[parts[2]] = [2]int{adds, dels}
	}
	return result, nil
}
