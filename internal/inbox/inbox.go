package inbox

import (
	"math"
	"sort"
	"time"

	"github.com/blakewilliams/gg/internal/github"
)

const (
	halfDay   = 12 * time.Hour
	oneDay    = 24 * time.Hour
	twoDays   = 48 * time.Hour
	threeDays = 72 * time.Hour
	fourDays  = 96 * time.Hour
	fiveDays  = 120 * time.Hour
	oneWeek   = 168 * time.Hour
)

// actionConfig holds the weight and half-life for a given action reason.
type actionConfig struct {
	priority int
	halfLife time.Duration
}

// configs maps each action reason to its priority and half-life.
//
// Priority determines the base weight: weight = 100 * 0.8^priority.
// Lower priority number = higher weight = more urgent.
//
// Half-life controls how fast the score decays over time.
// At age = half-life, the score is halved. Shorter half-lives
// cause items to drop off the list faster.
//
// Priority order (most → least urgent):
var configs = map[github.ActionReason]actionConfig{
	github.ActionMergeConflicts:    {priority: 0, halfLife: oneWeek},
	github.ActionCIFailed:          {priority: 1, halfLife: oneWeek},
	github.ActionReadyToMerge:      {priority: 2, halfLife: twoDays},
	github.ActionChangesRequested:  {priority: 3, halfLife: threeDays},
	github.ActionApproved:          {priority: 4, halfLife: threeDays},
	github.ActionReReviewRequested: {priority: 5, halfLife: twoDays},
	github.ActionReviewRequested:   {priority: 6, halfLife: twoDays},
	github.ActionCIPending:         {priority: 7, halfLife: oneDay},
	github.ActionWaitingForReview:  {priority: 8, halfLife: fourDays},
	github.ActionMentioned:         {priority: 9, halfLife: oneDay},
	github.ActionDraft:             {priority: 10, halfLife: oneWeek},
	github.ActionMerged:            {priority: 11, halfLife: halfDay},
	github.ActionClosed:            {priority: 12, halfLife: halfDay},
}

// ComputeAction determines the actionability reason for a PR.
func ComputeAction(pr github.InboxPR, username string) github.ActionReason {
	isAuthor := pr.HasSource(github.SourceAuthored)

	if isAuthor {
		return computeAuthoredAction(pr)
	}
	return computeReviewerAction(pr)
}

func computeAuthoredAction(pr github.InboxPR) github.ActionReason {
	if pr.State == "closed" {
		return github.ActionClosed
	}
	if pr.State == "merged" {
		return github.ActionMerged
	}
	if pr.Mergeable != nil && !*pr.Mergeable {
		return github.ActionMergeConflicts
	}
	if pr.CIStatus == "failure" || pr.CIStatus == "error" {
		return github.ActionCIFailed
	}
	if pr.ReviewDecision == "CHANGES_REQUESTED" {
		return github.ActionChangesRequested
	}
	mergeable := pr.Mergeable != nil && *pr.Mergeable
	if (pr.ReviewDecision == "APPROVED" || pr.ReviewDecision == "") &&
		pr.CIStatus == "success" && mergeable {
		return github.ActionReadyToMerge
	}
	if pr.ReviewDecision == "APPROVED" {
		if pr.CIStatus == "pending" {
			return github.ActionCIPending
		}
		return github.ActionApproved
	}
	if pr.CIStatus == "pending" {
		return github.ActionCIPending
	}
	if pr.State == "draft" {
		return github.ActionDraft
	}
	return github.ActionWaitingForReview
}

func computeReviewerAction(pr github.InboxPR) github.ActionReason {
	// New commits since last review → re-review needed.
	if pr.LatestCommitAt != nil && pr.LatestReviewAt != nil {
		if pr.LatestCommitAt.After(*pr.LatestReviewAt) {
			return github.ActionReReviewRequested
		}
	}
	if pr.ReviewRequested {
		return github.ActionReviewRequested
	}
	if pr.HasSource(github.SourceAssigned) {
		return github.ActionReviewRequested
	}
	if pr.HasSource(github.SourceMentioned) {
		return github.ActionMentioned
	}
	return github.ActionNone
}

// stateChangedAt returns the timestamp most representative of when the PR
// entered its current action state. Falls back to UpdatedAt when no
// better signal is available.
func stateChangedAt(pr github.InboxPR, action github.ActionReason) time.Time {
	pick := func(candidates ...*time.Time) time.Time {
		for _, t := range candidates {
			if t != nil {
				return *t
			}
		}
		return pr.UpdatedAt
	}

	switch action {
	case github.ActionCIFailed, github.ActionCIPending, github.ActionMergeConflicts:
		return pick(pr.LatestCommitAt)
	case github.ActionChangesRequested, github.ActionApproved:
		return pick(pr.LatestReviewAt)
	case github.ActionReadyToMerge:
		// Ready to merge once both CI and review pass — use the later of the two.
		if pr.LatestReviewAt != nil && pr.LatestCommitAt != nil {
			if pr.LatestReviewAt.After(*pr.LatestCommitAt) {
				return *pr.LatestReviewAt
			}
			return *pr.LatestCommitAt
		}
		return pick(pr.LatestReviewAt, pr.LatestCommitAt)
	case github.ActionReReviewRequested:
		return pick(pr.LatestCommitAt)
	case github.ActionDraft, github.ActionWaitingForReview:
		return pr.CreatedAt
	default:
		return pr.UpdatedAt
	}
}

// ComputeScore calculates the priority score with time decay.
// Higher score = higher priority. Pass the current time for testability.
func ComputeScore(action github.ActionReason, stateChanged time.Time, now time.Time) float64 {
	cfg, ok := configs[action]
	if !ok {
		return 0
	}

	weight := 100.0 * math.Pow(0.8, float64(cfg.priority))
	age := now.Sub(stateChanged)
	ageHours := age.Hours()
	halfLifeHours := cfg.halfLife.Hours()

	return weight * math.Pow(2, -ageHours/halfLifeHours)
}

// ProcessInbox computes actionability and scoring for all PRs,
// filters out non-actionable ones, and sorts by score descending.
func ProcessInbox(prs []github.InboxPR, username string) []github.InboxPR {
	return ProcessInboxAt(prs, username, time.Now())
}

// ProcessInboxAt is like ProcessInbox but accepts a fixed time for testability.
func ProcessInboxAt(prs []github.InboxPR, username string, now time.Time) []github.InboxPR {
	var actionable []github.InboxPR

	for i := range prs {
		action := ComputeAction(prs[i], username)
		if action == github.ActionNone {
			continue
		}
		prs[i].Action = action
		prs[i].GHQStateChangedAt = stateChangedAt(prs[i], action)
		prs[i].Score = ComputeScore(action, prs[i].GHQStateChangedAt, now)
		actionable = append(actionable, prs[i])
	}

	sort.SliceStable(actionable, func(i, j int) bool {
		diff := actionable[i].Score - actionable[j].Score
		if diff > 0.01 {
			return true
		}
		if diff < -0.01 {
			return false
		}
		if !actionable[i].GHQStateChangedAt.Equal(actionable[j].GHQStateChangedAt) {
			return actionable[i].GHQStateChangedAt.After(actionable[j].GHQStateChangedAt)
		}
		return actionable[i].Number > actionable[j].Number
	})

	return actionable
}
