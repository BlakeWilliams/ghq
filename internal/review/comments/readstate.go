package comments

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"github.com/blakewilliams/gg/internal/cache/persist"
)

// ReadState tracks which comment threads have been read.
// Keys are thread root IDs. The stored time is when the user last viewed the thread.
// A thread is "unread" if any comment in it is newer than the stored time.
type ReadState struct {
	repoPath string
	Seen     map[string]time.Time `json:"seen"`
}

func readStateFile(repoPath string) string {
	h := sha256.Sum256([]byte(repoPath))
	return fmt.Sprintf("comment-readstate-%x.json", h[:8])
}

// LoadReadState loads persisted read state for the given repo.
func LoadReadState(repoPath string) *ReadState {
	rs := &ReadState{repoPath: repoPath, Seen: map[string]time.Time{}}
	persist.Load(readStateFile(repoPath), rs)
	rs.repoPath = repoPath
	if rs.Seen == nil {
		rs.Seen = map[string]time.Time{}
	}
	return rs
}

// Save persists the read state to disk.
func (rs *ReadState) Save() error {
	return persist.Save(readStateFile(rs.repoPath), rs)
}

// MarkRead records a thread as read now (keyed by root comment ID).
func (rs *ReadState) MarkRead(threadRootID string) {
	rs.Seen[threadRootID] = time.Now()
}

// MarkReadInt records a thread with an int root ID as read.
func (rs *ReadState) MarkReadInt(threadRootID int) {
	rs.MarkRead(strconv.Itoa(threadRootID))
}

// LastReadTime returns the last time the thread was marked read.
// Returns zero time if never read.
func (rs *ReadState) LastReadTime(threadRootID string) time.Time {
	return rs.Seen[threadRootID]
}

// IsThreadUnread returns true if the thread has activity newer than the last
// time it was marked read. If the thread has never been read, it's unread.
func (rs *ReadState) IsThreadUnread(threadRootID string, newestComment time.Time) bool {
	lastRead, ok := rs.Seen[threadRootID]
	if !ok {
		return true
	}
	return newestComment.After(lastRead)
}

// IsThreadUnreadInt checks unread status for an int root ID.
func (rs *ReadState) IsThreadUnreadInt(threadRootID int, newestComment time.Time) bool {
	return rs.IsThreadUnread(strconv.Itoa(threadRootID), newestComment)
}
