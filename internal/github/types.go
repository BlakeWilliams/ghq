package github

import "time"

type PullRequest struct {
	Number             int        `json:"number"`
	HTMLURL            string     `json:"html_url"`
	Title              string     `json:"title"`
	Body               string     `json:"body"`
	State              string     `json:"state"`
	Draft              bool       `json:"draft"`
	Merged             bool       `json:"merged"`
	MergedAt           *time.Time `json:"merged_at"`
	MergedBy           *User      `json:"merged_by"`
	ClosedAt           *time.Time `json:"closed_at"`
	User               User       `json:"user"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	Head               Branch     `json:"head"`
	Base               Branch     `json:"base"`
	Additions          int        `json:"additions"`
	Deletions          int        `json:"deletions"`
	ChangedFiles       int        `json:"changed_files"`
	Labels             []Label    `json:"labels"`
	RequestedReviewers []User     `json:"requested_reviewers"`
}

// RepoOwner returns the repository owner for this PR.
func (pr PullRequest) RepoOwner() string {
	if pr.Base.Repo != nil {
		return pr.Base.Repo.Owner.Login
	}
	return ""
}

// RepoName returns the repository name for this PR.
func (pr PullRequest) RepoName() string {
	if pr.Base.Repo != nil {
		return pr.Base.Repo.Name
	}
	return ""
}

type User struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	Type      string `json:"type"`
}

type Branch struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *BranchRepo `json:"repo,omitempty"`
}

type BranchRepo struct {
	Owner User   `json:"owner"`
	Name  string `json:"name"`
}

type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type Review struct {
	ID          int       `json:"id"`
	User        User      `json:"user"`
	Body        string    `json:"body"`
	State       string    `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	SubmittedAt time.Time `json:"submitted_at"`
}

type IssueComment struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ReviewComment struct {
	ID                  int       `json:"id"`
	Body                string    `json:"body"`
	Path                string    `json:"path"`
	Line                *int      `json:"line"`
	OriginalLine        *int      `json:"original_line"`
	Side                string    `json:"side"`
	StartLine           *int      `json:"start_line"`
	OriginalStartLine   *int      `json:"original_start_line"`
	InReplyToID         *int      `json:"in_reply_to_id"`
	User                User      `json:"user"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type CheckRun struct {
	ID          int        `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`      // queued, in_progress, completed
	Conclusion  *string    `json:"conclusion"`   // success, failure, neutral, cancelled, skipped, timed_out, action_required
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	HTMLURL     string     `json:"html_url"`
}

type CheckRunsResponse struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []CheckRun `json:"check_runs"`
}

type PullRequestFile struct {
	Filename         string `json:"filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
	PreviousFilename string `json:"previous_filename"`
}
