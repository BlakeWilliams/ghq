package comments

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/blakewilliams/gg/internal/cache/persist"
	"github.com/blakewilliams/gg/internal/github"
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
// For "gh-<n>" IDs (imported GitHub comments), it returns n directly so that
// the int round-trips to the original GitHub API ID.
func IDToInt(id string) int {
	if n, ok := GHIDFromString(id); ok {
		return n
	}
	h := sha256.Sum256([]byte(id))
	// Use first 4 bytes as a positive int.
	return int(h[0])<<24 | int(h[1])<<16 | int(h[2])<<8 | int(h[3])
}

// GHIDToString converts a GitHub int comment ID to the canonical "gh-<n>" string form.
func GHIDToString(id int) string {
	return fmt.Sprintf("gh-%d", id)
}

// GHIDFromString parses a "gh-<n>" string back to the original GitHub int ID.
func GHIDFromString(id string) (int, bool) {
	if strings.HasPrefix(id, "gh-") {
		n, err := strconv.Atoi(id[3:])
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// IsGHID returns true if the ID represents an imported GitHub comment.
func IsGHID(id string) bool {
	return strings.HasPrefix(id, "gh-")
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

// Save persists the comment store to disk, excluding imported GitHub comments
// (which are ephemeral and refreshed from the API).
func (s *CommentStore) Save() error {
	persistent := &CommentStore{RepoPath: s.RepoPath}
	for _, c := range s.Comments {
		if !IsGHID(c.ID) {
			persistent.Comments = append(persistent.Comments, c)
		}
	}
	return persist.Save(cacheFilename(s.RepoPath), persistent)
}

// ImportGH replaces the in-memory set of imported GitHub review comments.
// Previous "gh-" entries are removed and the new set is added. The store is
// NOT persisted — these comments come from the API and are refreshed periodically.
func (s *CommentStore) ImportGH(ghComments []github.ReviewComment) {
	// Keep only local (non-GH) comments.
	kept := s.Comments[:0]
	for _, c := range s.Comments {
		if !IsGHID(c.ID) {
			kept = append(kept, c)
		}
	}
	s.Comments = kept

	for _, gc := range ghComments {
		s.Comments = append(s.Comments, ghReviewToLocal(gc))
	}
}

// ghReviewToLocal converts a GitHub ReviewComment to a LocalComment with
// a "gh-<id>" string ID so it participates in the normal CommentStore flow.
func ghReviewToLocal(c github.ReviewComment) LocalComment {
	lc := LocalComment{
		ID:        GHIDToString(c.ID),
		Body:      c.Body,
		Path:      c.Path,
		Side:      c.Side,
		Author:    c.User.Login,
		CreatedAt: c.CreatedAt,
	}
	if !c.UpdatedAt.IsZero() {
		lc.CreatedAt = c.UpdatedAt
	}
	if c.Line != nil {
		lc.Line = *c.Line
	} else if c.OriginalLine != nil {
		lc.Line = *c.OriginalLine
	}
	if c.StartLine != nil {
		lc.StartLine = *c.StartLine
	} else if c.OriginalStartLine != nil {
		lc.StartLine = *c.OriginalStartLine
	}
	if c.InReplyToID != nil {
		lc.InReplyToID = GHIDToString(*c.InReplyToID)
	}
	if lc.Side == "" {
		lc.Side = "RIGHT"
	}
	return lc
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

// ThreadHasCopilotComment returns true if the thread rooted at rootID
// contains any comment authored by copilot.
func (s *CommentStore) ThreadHasCopilotComment(rootID string) bool {
	for _, c := range s.Comments {
		if (c.ID == rootID || c.InReplyToID == rootID) && c.Author == "copilot" {
			return true
		}
	}
	return false
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
