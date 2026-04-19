package inbox

import (
	"testing"

	"github.com/blakewilliams/gg/internal/github"
)

func inboxPR(number int, action github.ActionReason) github.InboxPR {
	return github.InboxPR{
		Number: number,
		Repo:   github.RepoRef{Owner: "owner", Name: "repo"},
		Title:  "Test PR",
		Action: action,
	}
}

func TestDetectChanges_NilSnapshot(t *testing.T) {
	// First load — no notifications.
	changes := DetectChanges(nil, []github.InboxPR{
		inboxPR(1, github.ActionReviewRequested),
	})
	if len(changes) != 0 {
		t.Errorf("first load should produce no changes, got %d", len(changes))
	}
}

func TestDetectChanges_NewReviewRequested(t *testing.T) {
	old := Snapshot{}
	newPRs := []github.InboxPR{
		inboxPR(1, github.ActionReviewRequested),
	}
	changes := DetectChanges(old, newPRs)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(changes))
	}
	if !changes[0].IsNew {
		t.Error("should be marked as new")
	}
	if changes[0].NewAction != github.ActionReviewRequested {
		t.Errorf("got action %q, want %q", changes[0].NewAction, github.ActionReviewRequested)
	}
}

func TestDetectChanges_NewWaitingIgnored(t *testing.T) {
	// A new "waiting_for_review" PR shouldn't notify — not urgent.
	old := Snapshot{}
	newPRs := []github.InboxPR{
		inboxPR(1, github.ActionWaitingForReview),
	}
	changes := DetectChanges(old, newPRs)
	if len(changes) != 0 {
		t.Errorf("got %d changes, want 0 (waiting is not noteworthy)", len(changes))
	}
}

func TestDetectChanges_ActionChanged(t *testing.T) {
	old := TakeSnapshot([]github.InboxPR{
		inboxPR(1, github.ActionWaitingForReview),
	})
	newPRs := []github.InboxPR{
		inboxPR(1, github.ActionApproved),
	}
	changes := DetectChanges(old, newPRs)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(changes))
	}
	c := changes[0]
	if c.IsNew {
		t.Error("should not be marked as new")
	}
	if c.OldAction != github.ActionWaitingForReview {
		t.Errorf("old action: got %q, want %q", c.OldAction, github.ActionWaitingForReview)
	}
	if c.NewAction != github.ActionApproved {
		t.Errorf("new action: got %q, want %q", c.NewAction, github.ActionApproved)
	}
}

func TestDetectChanges_SameActionNoChange(t *testing.T) {
	old := TakeSnapshot([]github.InboxPR{
		inboxPR(1, github.ActionReviewRequested),
	})
	newPRs := []github.InboxPR{
		inboxPR(1, github.ActionReviewRequested),
	}
	changes := DetectChanges(old, newPRs)
	if len(changes) != 0 {
		t.Errorf("same action should produce no changes, got %d", len(changes))
	}
}

func TestDetectChanges_TransitionToCIPending_Ignored(t *testing.T) {
	// ci_pending is not noteworthy as a transition.
	old := TakeSnapshot([]github.InboxPR{
		inboxPR(1, github.ActionWaitingForReview),
	})
	newPRs := []github.InboxPR{
		inboxPR(1, github.ActionCIPending),
	}
	changes := DetectChanges(old, newPRs)
	if len(changes) != 0 {
		t.Errorf("ci_pending transition should be ignored, got %d", len(changes))
	}
}

func TestDetectChanges_TransitionToCIFailed(t *testing.T) {
	old := TakeSnapshot([]github.InboxPR{
		inboxPR(1, github.ActionCIPending),
	})
	newPRs := []github.InboxPR{
		inboxPR(1, github.ActionCIFailed),
	}
	changes := DetectChanges(old, newPRs)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(changes))
	}
	if changes[0].NewAction != github.ActionCIFailed {
		t.Errorf("got %q, want %q", changes[0].NewAction, github.ActionCIFailed)
	}
}

func TestDetectChanges_TransitionToMerged(t *testing.T) {
	old := TakeSnapshot([]github.InboxPR{
		inboxPR(1, github.ActionReadyToMerge),
	})
	newPRs := []github.InboxPR{
		inboxPR(1, github.ActionMerged),
	}
	changes := DetectChanges(old, newPRs)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(changes))
	}
}

func TestDetectChanges_MultipleChanges(t *testing.T) {
	old := TakeSnapshot([]github.InboxPR{
		inboxPR(1, github.ActionWaitingForReview),
		inboxPR(2, github.ActionCIPending),
	})
	newPRs := []github.InboxPR{
		inboxPR(1, github.ActionApproved),
		inboxPR(2, github.ActionCIFailed),
		inboxPR(3, github.ActionReviewRequested), // new
	}
	changes := DetectChanges(old, newPRs)
	if len(changes) != 3 {
		t.Errorf("got %d changes, want 3", len(changes))
	}
}

func TestNotificationText_New(t *testing.T) {
	c := Change{
		PR:        inboxPR(42, github.ActionReviewRequested),
		NewAction: github.ActionReviewRequested,
		IsNew:     true,
	}
	title, body := c.NotificationText()
	if title != "Review requested" {
		t.Errorf("title: got %q", title)
	}
	if body != "owner/repo #42 Test PR" {
		t.Errorf("body: got %q", body)
	}
}

func TestNotificationText_Transition(t *testing.T) {
	c := Change{
		PR:        inboxPR(10, github.ActionApproved),
		OldAction: github.ActionWaitingForReview,
		NewAction: github.ActionApproved,
	}
	title, body := c.NotificationText()
	if title != "Waiting for review → Approved" {
		t.Errorf("title: got %q", title)
	}
	if body != "owner/repo #10 Test PR" {
		t.Errorf("body: got %q", body)
	}
}
