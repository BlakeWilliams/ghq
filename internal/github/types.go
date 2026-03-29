package github

import "time"

type PullRequest struct {
	Number       int        `json:"number"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	State        string     `json:"state"`
	Draft        bool       `json:"draft"`
	Merged       bool       `json:"merged"`
	MergedAt     *time.Time `json:"merged_at"`
	MergedBy     *User      `json:"merged_by"`
	ClosedAt     *time.Time `json:"closed_at"`
	User         User       `json:"user"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Head         Branch     `json:"head"`
	Base         Branch     `json:"base"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	ChangedFiles int        `json:"changed_files"`
	Labels       []Label    `json:"labels"`
}

type User struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	Type      string `json:"type"`
}

type Branch struct {
	Ref string `json:"ref"`
}

type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type PullRequestFile struct {
	Filename         string `json:"filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
	PreviousFilename string `json:"previous_filename"`
}
