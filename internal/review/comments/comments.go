package comments

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/blakewilliams/ghq/internal/cache/persist"
	"github.com/blakewilliams/ghq/internal/github"
)

// LocalComment represents a comment on a local diff line.
type LocalComment struct {
	ID          string         `json:"id"`
	Body        string         `json:"body"`
	Path        string         `json:"path"`
	Line        int            `json:"line"`
	Side        string         `json:"side"` // "LEFT" or "RIGHT"
	StartLine   int            `json:"start_line,omitempty"`
	StartSide   string         `json:"start_side,omitempty"`
	InReplyToID string         `json:"in_reply_to_id,omitempty"`
	Author      string         `json:"author"` // "you" or "copilot"
	Resolved    bool           `json:"resolved"`
	CreatedAt   time.Time      `json:"created_at"`
	Blocks      []ContentBlock `json:"-"` // custom marshal via RawBlocks
	RawBlocks   json.RawMessage `json:"blocks,omitempty"`
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
	for i := range store.Comments {
		store.Comments[i].hydrateBlocks()
	}
	return store
}

// Save persists the comment store to disk.
func (s *CommentStore) Save() error {
	return persist.Save(cacheFilename(s.RepoPath), s)
}

// Add adds a comment and persists. If the comment has Blocks, they are
// serialized to RawBlocks and Body is derived for backward compatibility.
func (s *CommentStore) Add(c LocalComment) {
	c.prepareForSave()
	s.Comments = append(s.Comments, c)
	s.Save()
}

// resolvedIDs returns the set of all comment IDs that belong to resolved threads.
// Walks the full reply chain from resolved roots so nested replies are included.
func (s *CommentStore) resolvedIDs() map[string]bool {
	resolved := map[string]bool{}
	for _, c := range s.Comments {
		if c.Resolved && c.InReplyToID == "" {
			resolved[c.ID] = true
		}
	}
	// Propagate to all descendants.
	changed := true
	for changed {
		changed = false
		for _, c := range s.Comments {
			if !resolved[c.ID] && resolved[c.InReplyToID] {
				resolved[c.ID] = true
				changed = true
			}
		}
	}
	return resolved
}

// ForFile returns non-resolved comments for a given file path as ReviewComments.
// Note: blocks are lost in this conversion — use ForFileLocal for block-aware rendering.
func (s *CommentStore) ForFile(path string) []github.ReviewComment {
	resolved := s.resolvedIDs()

	var result []github.ReviewComment
	for _, c := range s.Comments {
		if c.Path != path {
			continue
		}
		if resolved[c.ID] {
			continue
		}
		result = append(result, c.ToReviewComment())
	}
	return result
}

// ForFileLocal returns non-resolved LocalComments for a given file path,
// preserving block data for rendering.
func (s *CommentStore) ForFileLocal(path string) []LocalComment {
	resolved := s.resolvedIDs()

	var result []LocalComment
	for _, c := range s.Comments {
		if c.Path != path {
			continue
		}
		if resolved[c.ID] {
			continue
		}
		result = append(result, c)
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

// Resolve toggles the resolved state of all comments in a thread,
// walking the full reply chain (not just direct children of root).
func (s *CommentStore) Resolve(rootID string, resolved bool) {
	// Collect all IDs in the thread via breadth-first walk.
	threadIDs := map[string]bool{rootID: true}
	changed := true
	for changed {
		changed = false
		for _, c := range s.Comments {
			if !threadIDs[c.ID] && threadIDs[c.InReplyToID] {
				threadIDs[c.ID] = true
				changed = true
			}
		}
	}
	for i := range s.Comments {
		if threadIDs[s.Comments[i].ID] {
			s.Comments[i].Resolved = resolved
		}
	}
	s.Save()
}

// prepareForSave serializes Blocks to RawBlocks and populates Body from
// text blocks for backward compatibility.
func (c *LocalComment) prepareForSave() {
	if len(c.Blocks) > 0 {
		raw, err := MarshalBlocksJSON(c.Blocks)
		if err == nil {
			c.RawBlocks = raw
		}
		c.Body = BodyFromBlocks(c.Blocks)
	}
}

// hydrateBlocks deserializes RawBlocks into Blocks after loading from disk.
func (c *LocalComment) hydrateBlocks() {
	if len(c.RawBlocks) > 0 {
		blocks, err := UnmarshalBlocksJSON(c.RawBlocks)
		if err == nil {
			c.Blocks = blocks
		}
	}
}
