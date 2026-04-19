package inbox

import (
	"testing"
	"time"

	"github.com/blakewilliams/gg/internal/github"
)

func boolPtr(b bool) *bool     { return &b }
func timePtr(t time.Time) *time.Time { return &t }

var now = time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)

func pr(opts ...func(*github.InboxPR)) github.InboxPR {
	p := github.InboxPR{
		Number:    1,
		State:     "open",
		UpdatedAt: now.Add(-1 * time.Hour),
		Sources:   []github.PRSource{github.SourceAuthored},
	}
	for _, o := range opts {
		o(&p)
	}
	return p
}

func withSources(s ...github.PRSource) func(*github.InboxPR) {
	return func(p *github.InboxPR) { p.Sources = s }
}

func withState(s string) func(*github.InboxPR) {
	return func(p *github.InboxPR) { p.State = s }
}

func withReviewDecision(d string) func(*github.InboxPR) {
	return func(p *github.InboxPR) { p.ReviewDecision = d }
}

func withCI(s string) func(*github.InboxPR) {
	return func(p *github.InboxPR) { p.CIStatus = s }
}

func withMergeable(b bool) func(*github.InboxPR) {
	return func(p *github.InboxPR) { p.Mergeable = boolPtr(b) }
}

func withReviewRequested(b bool) func(*github.InboxPR) {
	return func(p *github.InboxPR) { p.ReviewRequested = b }
}

func withCommitAndReview(commit, review time.Time) func(*github.InboxPR) {
	return func(p *github.InboxPR) {
		p.LatestCommitAt = timePtr(commit)
		p.LatestReviewAt = timePtr(review)
	}
}

// --- ComputeAction tests: authored PRs ---

func TestAuthoredAction_Closed(t *testing.T) {
	got := ComputeAction(pr(withState("closed")), "me")
	if got != github.ActionClosed {
		t.Errorf("closed PR: got %q, want %q", got, github.ActionClosed)
	}
}

func TestAuthoredAction_Merged(t *testing.T) {
	got := ComputeAction(pr(withState("merged")), "me")
	if got != github.ActionMerged {
		t.Errorf("merged PR: got %q, want %q", got, github.ActionMerged)
	}
}

func TestAuthoredAction_MergeConflicts(t *testing.T) {
	got := ComputeAction(pr(withMergeable(false)), "me")
	if got != github.ActionMergeConflicts {
		t.Errorf("got %q, want %q", got, github.ActionMergeConflicts)
	}
}

func TestAuthoredAction_CIFailed(t *testing.T) {
	for _, ci := range []string{"failure", "error"} {
		got := ComputeAction(pr(withCI(ci)), "me")
		if got != github.ActionCIFailed {
			t.Errorf("CI %q: got %q, want %q", ci, got, github.ActionCIFailed)
		}
	}
}

func TestAuthoredAction_ChangesRequested(t *testing.T) {
	got := ComputeAction(pr(withReviewDecision("CHANGES_REQUESTED")), "me")
	if got != github.ActionChangesRequested {
		t.Errorf("got %q, want %q", got, github.ActionChangesRequested)
	}
}

func TestAuthoredAction_ReadyToMerge(t *testing.T) {
	got := ComputeAction(pr(
		withReviewDecision("APPROVED"),
		withCI("success"),
		withMergeable(true),
	), "me")
	if got != github.ActionReadyToMerge {
		t.Errorf("got %q, want %q", got, github.ActionReadyToMerge)
	}
}

func TestAuthoredAction_ReadyToMerge_NoReviewDecision(t *testing.T) {
	// Empty review decision + CI success + mergeable = ready.
	got := ComputeAction(pr(
		withReviewDecision(""),
		withCI("success"),
		withMergeable(true),
	), "me")
	if got != github.ActionReadyToMerge {
		t.Errorf("got %q, want %q", got, github.ActionReadyToMerge)
	}
}

func TestAuthoredAction_Approved_CIPending(t *testing.T) {
	got := ComputeAction(pr(
		withReviewDecision("APPROVED"),
		withCI("pending"),
	), "me")
	if got != github.ActionCIPending {
		t.Errorf("got %q, want %q", got, github.ActionCIPending)
	}
}

func TestAuthoredAction_Approved_NoCI(t *testing.T) {
	got := ComputeAction(pr(withReviewDecision("APPROVED")), "me")
	if got != github.ActionApproved {
		t.Errorf("got %q, want %q", got, github.ActionApproved)
	}
}

func TestAuthoredAction_CIPending(t *testing.T) {
	got := ComputeAction(pr(withCI("pending")), "me")
	if got != github.ActionCIPending {
		t.Errorf("got %q, want %q", got, github.ActionCIPending)
	}
}

func TestAuthoredAction_Draft(t *testing.T) {
	got := ComputeAction(pr(withState("draft")), "me")
	if got != github.ActionDraft {
		t.Errorf("got %q, want %q", got, github.ActionDraft)
	}
}

func TestAuthoredAction_WaitingForReview(t *testing.T) {
	got := ComputeAction(pr(), "me")
	if got != github.ActionWaitingForReview {
		t.Errorf("got %q, want %q", got, github.ActionWaitingForReview)
	}
}

// --- ComputeAction tests: reviewer PRs ---

func TestReviewerAction_ReReviewRequested(t *testing.T) {
	commit := now.Add(-1 * time.Hour)
	review := now.Add(-2 * time.Hour)
	got := ComputeAction(pr(
		withSources(github.SourceReviewRequested),
		withCommitAndReview(commit, review),
	), "me")
	if got != github.ActionReReviewRequested {
		t.Errorf("got %q, want %q", got, github.ActionReReviewRequested)
	}
}

func TestReviewerAction_ReviewRequested(t *testing.T) {
	got := ComputeAction(pr(
		withSources(github.SourceReviewRequested),
		withReviewRequested(true),
	), "me")
	if got != github.ActionReviewRequested {
		t.Errorf("got %q, want %q", got, github.ActionReviewRequested)
	}
}

func TestReviewerAction_Assigned(t *testing.T) {
	got := ComputeAction(pr(withSources(github.SourceAssigned)), "me")
	if got != github.ActionReviewRequested {
		t.Errorf("got %q, want %q", got, github.ActionReviewRequested)
	}
}

func TestReviewerAction_Mentioned(t *testing.T) {
	got := ComputeAction(pr(withSources(github.SourceMentioned)), "me")
	if got != github.ActionMentioned {
		t.Errorf("got %q, want %q", got, github.ActionMentioned)
	}
}

func TestReviewerAction_None(t *testing.T) {
	// No source that makes it actionable.
	p := github.InboxPR{State: "open", Sources: []github.PRSource{}}
	got := ComputeAction(p, "me")
	if got != github.ActionNone {
		t.Errorf("got %q, want %q", got, github.ActionNone)
	}
}

// --- ComputeAction priority order ---
// Authored: merge_conflicts > ci_failed > changes_requested > ready_to_merge

func TestAuthoredAction_ConflictsTrumpsCIFailed(t *testing.T) {
	got := ComputeAction(pr(withMergeable(false), withCI("failure")), "me")
	if got != github.ActionMergeConflicts {
		t.Errorf("got %q, want %q", got, github.ActionMergeConflicts)
	}
}

func TestAuthoredAction_CIFailedTrumpsChangesRequested(t *testing.T) {
	got := ComputeAction(pr(withCI("failure"), withReviewDecision("CHANGES_REQUESTED")), "me")
	if got != github.ActionCIFailed {
		t.Errorf("got %q, want %q", got, github.ActionCIFailed)
	}
}

// --- ComputeScore tests ---

func TestComputeScore_AtUpdateTime(t *testing.T) {
	// Score at age=0 should equal the raw weight.
	score := ComputeScore(github.ActionMergeConflicts, now, now)
	expected := 100.0 // priority 0 → 100 * 0.8^0 = 100
	if diff := score - expected; diff > 0.01 || diff < -0.01 {
		t.Errorf("got %.2f, want %.2f", score, expected)
	}
}

func TestComputeScore_AtHalfLife(t *testing.T) {
	// Score at exactly one half-life should be half the weight.
	halfLife := configs[github.ActionReviewRequested].halfLife // 48h
	updatedAt := now.Add(-halfLife)
	score := ComputeScore(github.ActionReviewRequested, updatedAt, now)
	weight := 100.0 * 0.8 * 0.8 * 0.8 * 0.8 * 0.8 * 0.8 // priority 6
	expected := weight / 2
	if diff := score - expected; diff > 0.1 || diff < -0.1 {
		t.Errorf("got %.2f, want %.2f", score, expected)
	}
}

func TestComputeScore_UnknownAction(t *testing.T) {
	score := ComputeScore(github.ActionNone, now, now)
	if score != 0 {
		t.Errorf("got %.2f, want 0", score)
	}
}

func TestComputeScore_HigherPriorityScoresHigher(t *testing.T) {
	conflicts := ComputeScore(github.ActionMergeConflicts, now, now)
	review := ComputeScore(github.ActionReviewRequested, now, now)
	mentioned := ComputeScore(github.ActionMentioned, now, now)

	if conflicts <= review {
		t.Errorf("merge_conflicts (%.2f) should score higher than review_requested (%.2f)", conflicts, review)
	}
	if review <= mentioned {
		t.Errorf("review_requested (%.2f) should score higher than mentioned (%.2f)", review, mentioned)
	}
}

func TestComputeScore_DecaysOverTime(t *testing.T) {
	fresh := ComputeScore(github.ActionReviewRequested, now, now)
	old := ComputeScore(github.ActionReviewRequested, now.Add(-72*time.Hour), now)

	if old >= fresh {
		t.Errorf("old score (%.2f) should be less than fresh (%.2f)", old, fresh)
	}
}

// --- ProcessInbox tests ---

func TestProcessInbox_FiltersNonActionable(t *testing.T) {
	// A PR with no actionable source gets filtered out.
	prs := []github.InboxPR{
		{Number: 1, State: "open", Sources: []github.PRSource{}},
		pr(withState("open")),
	}
	result := ProcessInboxAt(prs, "me", now)
	if len(result) != 1 {
		t.Fatalf("got %d PRs, want 1", len(result))
	}
	if result[0].Action != github.ActionWaitingForReview {
		t.Errorf("got action %q, want %q", result[0].Action, github.ActionWaitingForReview)
	}
}

func TestProcessInbox_SortsByScore(t *testing.T) {
	prs := []github.InboxPR{
		{Number: 1, State: "open", Sources: []github.PRSource{github.SourceMentioned}, UpdatedAt: now.Add(-1 * time.Hour)},
		{Number: 2, State: "open", Sources: []github.PRSource{github.SourceAuthored}, UpdatedAt: now.Add(-1 * time.Hour), CIStatus: "failure"},
		{Number: 3, State: "open", Sources: []github.PRSource{github.SourceReviewRequested}, ReviewRequested: true, UpdatedAt: now.Add(-1 * time.Hour)},
	}
	result := ProcessInboxAt(prs, "me", now)
	if len(result) != 3 {
		t.Fatalf("got %d PRs, want 3", len(result))
	}
	// ci_failed (#2) > review_requested (#3) > mentioned (#1)
	if result[0].Number != 2 {
		t.Errorf("first should be #2 (ci_failed), got #%d (%s)", result[0].Number, result[0].Action)
	}
	if result[1].Number != 3 {
		t.Errorf("second should be #3 (review_requested), got #%d (%s)", result[1].Number, result[1].Action)
	}
	if result[2].Number != 1 {
		t.Errorf("third should be #1 (mentioned), got #%d (%s)", result[2].Number, result[2].Action)
	}
}

func TestProcessInbox_StableSort(t *testing.T) {
	// Two PRs with same action, same created time — should sort by number desc.
	prs := []github.InboxPR{
		{Number: 10, State: "open", Sources: []github.PRSource{github.SourceAuthored}, CreatedAt: now, UpdatedAt: now},
		{Number: 20, State: "open", Sources: []github.PRSource{github.SourceAuthored}, CreatedAt: now, UpdatedAt: now},
	}
	result := ProcessInboxAt(prs, "me", now)
	if len(result) != 2 {
		t.Fatalf("got %d PRs, want 2", len(result))
	}
	if result[0].Number != 20 {
		t.Errorf("first should be #20, got #%d", result[0].Number)
	}
	if result[1].Number != 10 {
		t.Errorf("second should be #10, got #%d", result[1].Number)
	}
}

func TestProcessInbox_TiebreakByStateChangeTime(t *testing.T) {
	// Same action, different created times — more recent first.
	// Both are WaitingForReview, so stateChangedAt uses CreatedAt.
	prs := []github.InboxPR{
		{Number: 1, State: "open", Sources: []github.PRSource{github.SourceAuthored}, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour)},
		{Number: 2, State: "open", Sources: []github.PRSource{github.SourceAuthored}, CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
	}
	result := ProcessInboxAt(prs, "me", now)
	if result[0].Number != 2 {
		t.Errorf("more recently updated should be first, got #%d", result[0].Number)
	}
}

func TestProcessInbox_Empty(t *testing.T) {
	result := ProcessInboxAt(nil, "me", now)
	if len(result) != 0 {
		t.Errorf("got %d PRs, want 0", len(result))
	}
}

func TestProcessInbox_ClosedAtBottom(t *testing.T) {
	prs := []github.InboxPR{
		{Number: 1, State: "closed", Sources: []github.PRSource{github.SourceAuthored}, CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour)},
		{Number: 2, State: "open", Sources: []github.PRSource{github.SourceAuthored}, CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour)},
	}
	result := ProcessInboxAt(prs, "me", now)
	if len(result) != 2 {
		t.Fatalf("got %d PRs, want 2", len(result))
	}
	// Open PR should be first, closed at the bottom.
	if result[0].Number != 2 {
		t.Errorf("first should be #2 (open), got #%d (%s)", result[0].Number, result[0].Action)
	}
	if result[1].Number != 1 {
		t.Errorf("second should be #1 (closed), got #%d (%s)", result[1].Number, result[1].Action)
	}
}
