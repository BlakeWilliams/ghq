package comments

import (
	"crypto/sha256"
	"fmt"

	"github.com/blakewilliams/ghq/internal/git"
	"github.com/blakewilliams/ghq/internal/cache/persist"
)

// ViewState persists the user's position in the local diff view.
// Each file is scoped to repo+branch+mode so there's no cross-contamination.
type ViewState struct {
	Filename string `json:"filename"`          // selected file (empty = overview)
	LineNo   int    `json:"line_no"`           // source line number at cursor (not diff index)
	Side     string `json:"side"`              // "LEFT" or "RIGHT" — which side the line is on
}

func stateFile(repoPath, branch string, mode git.DiffMode) string {
	key := fmt.Sprintf("%s:%s:%d", repoPath, branch, mode)
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("local-view-%x.json", h[:8])
}

// LoadViewState loads persisted position for this repo+branch+mode.
func LoadViewState(repoPath, branch string, mode git.DiffMode) ViewState {
	var s ViewState
	persist.Load(stateFile(repoPath, branch, mode), &s)
	return s
}

// SaveViewState persists position.
func SaveViewState(repoPath, branch string, mode git.DiffMode, s ViewState) {
	persist.Save(stateFile(repoPath, branch, mode), s)
}

// ActiveMode persists which mode was last used on this repo+branch.
type ActiveState struct {
	Mode git.DiffMode `json:"mode"`
}

func activeFile(repoPath, branch string) string {
	key := fmt.Sprintf("%s:%s:active", repoPath, branch)
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("local-active-%x.json", h[:8])
}

// LoadActiveState loads which mode was last active.
func LoadActiveState(repoPath, branch string) ActiveState {
	var s ActiveState
	persist.Load(activeFile(repoPath, branch), &s)
	return s
}

// SaveActiveState persists the active mode.
func SaveActiveState(repoPath, branch string, s ActiveState) {
	persist.Save(activeFile(repoPath, branch), s)
}
