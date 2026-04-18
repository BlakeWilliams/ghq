package git

import (
	"fmt"
	"os"
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
		return "Unstaged"
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
// On a repo with no commits yet, falls back to reading .git/HEAD directly.
func CurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err == nil {
		branch := strings.TrimSpace(string(out))
		if branch != "HEAD" {
			return branch, nil
		}
	}
	// Fallback: read .git/HEAD for orphan branches (no commits yet).
	headFile := filepath.Join(dir, ".git", "HEAD")
	data, err := os.ReadFile(headFile)
	if err != nil {
		return "main", nil // ultimate fallback
	}
	ref := strings.TrimSpace(string(data))
	if strings.HasPrefix(ref, "ref: refs/heads/") {
		return strings.TrimPrefix(ref, "ref: refs/heads/"), nil
	}
	return "main", nil
}

// HasCommits returns true if the repo has at least one commit.
func HasCommits(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	return cmd.Run() == nil
}

// DefaultBranch tries to detect the default branch, preferring the remote
// tracking ref (e.g. "origin/main") when origin is available.
func DefaultBranch(dir string) (string, error) {
	// Try symbolic ref of origin/HEAD first.
	cmd := exec.Command("git", "-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	out, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(out))
		// refs/remotes/origin/main -> origin/main
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			name := parts[len(parts)-1]
			return "origin/" + name, nil
		}
	}

	// Fallback: check if origin/main or origin/master exists, then local.
	for _, branch := range []string{"main", "master"} {
		cmd = exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/remotes/origin/"+branch)
		if err := cmd.Run(); err == nil {
			return "origin/" + branch, nil
		}
	}
	for _, branch := range []string{"main", "master"} {
		cmd = exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/heads/"+branch)
		if err := cmd.Run(); err == nil {
			return branch, nil
		}
	}

	return "main", nil // default fallback
}

// DefaultBranchShort returns just the branch name without the remote prefix.
func DefaultBranchShort(dir string) (string, error) {
	branch, err := DefaultBranch(dir)
	if err != nil {
		return branch, err
	}
	if after, ok := strings.CutPrefix(branch, "origin/"); ok {
		return after, nil
	}
	return branch, nil
}

// MergeBase returns the merge-base commit between the given branch and HEAD.
func MergeBase(dir, branch string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "merge-base", branch, "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Diff runs git diff and returns the raw unified diff output.
func Diff(dir string, mode DiffMode) (string, error) {
	var args []string

	hasCommits := HasCommits(dir)

	switch mode {
	case DiffWorking:
		if hasCommits {
			// Show only unstaged changes (not yet git add'd).
			args = []string{"-C", dir, "diff", "--no-color"}
		} else {
			// No commits yet — show staged files (everything that's been git add'd).
			args = []string{"-C", dir, "diff", "--cached", "--no-color"}
		}
	case DiffStaged:
		args = []string{"-C", dir, "diff", "--cached", "--no-color"}
	case DiffBranch:
		if !hasCommits {
			return "", nil // can't diff branches with no commits
		}
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
	result := string(out)

	// Append untracked files for working tree mode so they appear in the diff.
	if mode == DiffWorking {
		untracked, err := UntrackedFiles(dir)
		if err == nil && len(untracked) > 0 {
			result += untrackedDiff(dir, untracked)
		}
	}

	return result, nil
}

// UntrackedFiles returns the list of untracked, non-ignored files in the repo.
func UntrackedFiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// untrackedDiff synthesizes unified-diff output for untracked files so they
// appear alongside tracked changes in the working tree view.
func untrackedDiff(dir string, files []string) string {
	var sb strings.Builder
	for _, f := range files {
		content, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		// Drop trailing empty line from final newline split.
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		sb.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", f, f))
		sb.WriteString("new file mode 100644\n")
		sb.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n", f))
		sb.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines)))
		for _, l := range lines {
			sb.WriteString("+" + l + "\n")
		}
	}
	return sb.String()
}

// FileContent reads a file from the working tree.
func FileContent(dir string, path string) (string, error) {
	fullPath := filepath.Join(dir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// FileContentAtRef reads a file at a specific git ref (e.g., "HEAD", "main", commit SHA).
// Returns empty string if the file doesn't exist at that ref.
func FileContentAtRef(dir, path, ref string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "show", ref+":"+path)
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
		args = []string{"-C", dir, "diff", "--stat", "--no-color"}
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
		args = []string{"-C", dir, "diff", "--numstat", "--no-color"}
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
