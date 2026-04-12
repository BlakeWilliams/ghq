package comments

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/cache/persist"
)

// LocalComment represents a comment on a local diff line.
type LocalComment struct {
	ID          string    `json:"id"`
	Body        string    `json:"body"`
	Path        string    `json:"path"`
	Line        int       `json:"line"`
	Side        string    `json:"side"` // "LEFT" or "RIGHT"
	StartLine   int       `json:"start_line,omitempty"`
	StartSide   string    `json:"start_side,omitempty"`
	InReplyToID string    `json:"in_reply_to_id,omitempty"`
	Author      string    `json:"author"` // "you" or "copilot"
	Resolved    bool      `json:"resolved"`
	CreatedAt   time.Time `json:"created_at"`
}

// ToReviewComment converts a LocalComment to a github.ReviewComment
// for use with the existing diff rendering pipeline.
func (c LocalComment) ToReviewComment() github.ReviewComment {
	rc := github.ReviewComment{
		ID:        IDToInt(c.ID),
		Body:      c.Body,
		Path:      c.Path,
		Side:      c.Side,
		User:      github.User{Login: c.Author},
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.CreatedAt,
	}
	if c.Line > 0 {
		line := c.Line
		rc.Line = &line
		rc.OriginalLine = &line
	}
	if c.StartLine > 0 {
		sl := c.StartLine
		rc.StartLine = &sl
		rc.OriginalStartLine = &sl
	}
	if c.InReplyToID != "" {
		replyID := IDToInt(c.InReplyToID)
		rc.InReplyToID = &replyID
	}
	return rc
}

// IDToInt produces a deterministic int from a string ID.
func IDToInt(id string) int {
	h := sha256.Sum256([]byte(id))
	// Use first 4 bytes as a positive int.
	return int(h[0])<<24 | int(h[1])<<16 | int(h[2])<<8 | int(h[3])
}

// CommentStore manages local diff comments with persistence.
type CommentStore struct {
	RepoPath string         `json:"repo_path"`
	Comments []LocalComment `json:"comments"`
}

func cacheFilename(repoPath string) string {
	h := sha256.Sum256([]byte(repoPath))
	return fmt.Sprintf("local-comments-%x.json", h[:8])
}

// LoadComments loads comments for the given repo from the cache.
func LoadComments(repoPath string) *CommentStore {
	store := &CommentStore{RepoPath: repoPath}
	persist.Load(cacheFilename(repoPath), store)
	store.RepoPath = repoPath
	return store
}

// Save persists the comment store to disk.
func (s *CommentStore) Save() error {
	return persist.Save(cacheFilename(s.RepoPath), s)
}

// Add adds a comment and persists.
func (s *CommentStore) Add(c LocalComment) {
	s.Comments = append(s.Comments, c)
	s.Save()
}

// ForFile returns non-resolved comments for a given file path.
func (s *CommentStore) ForFile(path string) []github.ReviewComment {
	// First pass: find resolved root IDs.
	resolved := map[string]bool{}
	for _, c := range s.Comments {
		if c.Resolved && c.InReplyToID == "" {
			resolved[c.ID] = true
		}
	}

	var result []github.ReviewComment
	for _, c := range s.Comments {
		if c.Path != path {
			continue
		}
		// Skip if this comment or its thread root is resolved.
		if c.Resolved || resolved[c.InReplyToID] {
			continue
		}
		result = append(result, c.ToReviewComment())
	}
	return result
}

// All returns all comments as ReviewComment values.
func (s *CommentStore) All() []github.ReviewComment {
	result := make([]github.ReviewComment, len(s.Comments))
	for i, c := range s.Comments {
		result[i] = c.ToReviewComment()
	}
	return result
}

// FindThreadRoot returns the ID of the non-resolved root comment on a given line/side/path.
// Returns empty string if no active thread exists.
func (s *CommentStore) FindThreadRoot(path string, line int, side string) string {
	for _, c := range s.Comments {
		if c.Path == path && c.Line == line && c.Side == side && c.InReplyToID == "" && !c.Resolved {
			return c.ID
		}
	}
	return ""
}

// Resolve toggles the resolved state of all comments in a thread.
func (s *CommentStore) Resolve(rootID string, resolved bool) {
	for i := range s.Comments {
		if s.Comments[i].ID == rootID || s.Comments[i].InReplyToID == rootID {
			s.Comments[i].Resolved = resolved
		}
	}
	s.Save()
}
