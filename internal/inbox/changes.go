package inbox

import (
	"github.com/blakewilliams/gg/internal/github"
)

// Change describes a state transition for a PR.
type Change struct {
	PR        github.InboxPR
	OldAction github.ActionReason // empty if new PR
	NewAction github.ActionReason
	IsNew     bool // PR wasn't in the previous snapshot
}

// NotificationText returns a human-readable summary of the change.
func (c Change) NotificationText() (title, body string) {
	repo := c.PR.Repo.FullName()

	if c.IsNew {
		title = actionLabel(c.NewAction)
		body = repo + " #" + itoa(c.PR.Number) + " " + c.PR.Title
		return
	}

	title = actionLabel(c.OldAction) + " → " + actionLabel(c.NewAction)
	body = repo + " #" + itoa(c.PR.Number) + " " + c.PR.Title
	return
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func actionLabel(a github.ActionReason) string {
	switch a {
	case github.ActionMergeConflicts:
		return "Merge conflicts"
	case github.ActionCIFailed:
		return "CI failed"
	case github.ActionChangesRequested:
		return "Changes requested"
	case github.ActionReadyToMerge:
		return "Ready to merge"
	case github.ActionApproved:
		return "Approved"
	case github.ActionReReviewRequested:
		return "Re-review requested"
	case github.ActionReviewRequested:
		return "Review requested"
	case github.ActionCIPending:
		return "CI pending"
	case github.ActionWaitingForReview:
		return "Waiting for review"
	case github.ActionMentioned:
		return "Mentioned"
	case github.ActionDraft:
		return "Draft"
	case github.ActionMerged:
		return "Merged"
	case github.ActionClosed:
		return "Closed"
	default:
		return string(a)
	}
}

// Snapshot is a map of PR key → action reason for change detection.
type Snapshot map[string]github.ActionReason

// TakeSnapshot creates a snapshot from the current inbox.
func TakeSnapshot(prs []github.InboxPR) Snapshot {
	s := make(Snapshot, len(prs))
	for _, pr := range prs {
		key := pr.Repo.FullName() + "#" + itoa(pr.Number)
		s[key] = pr.Action
	}
	return s
}

// DetectChanges compares old and new inbox states and returns notable changes.
// Only returns changes worth notifying about — not minor transitions.
func DetectChanges(old Snapshot, newPRs []github.InboxPR) []Change {
	if old == nil {
		return nil // first load, no notifications
	}

	var changes []Change
	for _, pr := range newPRs {
		key := pr.Repo.FullName() + "#" + itoa(pr.Number)
		oldAction, existed := old[key]

		if !existed {
			// New PR appeared.
			if isNoteworthyNew(pr.Action) {
				changes = append(changes, Change{
					PR:        pr,
					NewAction: pr.Action,
					IsNew:     true,
				})
			}
			continue
		}

		if oldAction != pr.Action {
			// Action changed — only notify for important transitions.
			if isNoteworthyTransition(oldAction, pr.Action) {
				changes = append(changes, Change{
					PR:        pr,
					OldAction: oldAction,
					NewAction: pr.Action,
				})
			}
		}
	}

	return changes
}

// isNoteworthyNew returns true if a new PR appearing is worth a notification.
func isNoteworthyNew(action github.ActionReason) bool {
	switch action {
	case github.ActionReviewRequested,
		github.ActionReReviewRequested,
		github.ActionMentioned:
		return true
	default:
		return false
	}
}

// isNoteworthyTransition returns true if a state change is worth notifying.
func isNoteworthyTransition(old, new github.ActionReason) bool {
	switch new {
	case github.ActionApproved,
		github.ActionReadyToMerge,
		github.ActionCIFailed,
		github.ActionChangesRequested,
		github.ActionMergeConflicts,
		github.ActionMerged,
		github.ActionReviewRequested,
		github.ActionReReviewRequested:
		return true
	default:
		return false
	}
}
