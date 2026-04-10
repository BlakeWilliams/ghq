package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

type Client struct {
	rest  *api.RESTClient
	owner string
	repo  string
}

func (c *Client) RepoFullName() string {
	return c.owner + "/" + c.repo
}

// NewClient creates a GitHub API client. If nwo is empty, it detects the
// repo from the current directory. Otherwise nwo should be "owner/repo".
func NewClient(nwo string) (*Client, error) {
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, fmt.Errorf("creating REST client: %w", err)
	}

	if nwo != "" {
		parts := strings.SplitN(nwo, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid repo format %q, expected owner/repo", nwo)
		}
		return &Client{rest: rest, owner: parts[0], repo: parts[1]}, nil
	}

	repo, err := repository.Current()
	if err != nil {
		return nil, fmt.Errorf("detecting repository: %w", err)
	}

	return &Client{
		rest:  rest,
		owner: repo.Owner,
		repo:  repo.Name,
	}, nil
}

func (c *Client) ListPullRequests(ctx context.Context) ([]PullRequest, error) {
	var prs []PullRequest
	path := fmt.Sprintf("repos/%s/%s/pulls?state=open&per_page=30", c.owner, c.repo)
	err := c.rest.Get(path, &prs)
	if err != nil {
		return nil, fmt.Errorf("listing pull requests: %w", err)
	}
	return prs, nil
}

func (c *Client) GetPullRequest(ctx context.Context, number int) (PullRequest, error) {
	var pr PullRequest
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", c.owner, c.repo, number)
	err := c.rest.Get(path, &pr)
	if err != nil {
		return pr, fmt.Errorf("getting pull request #%d: %w", number, err)
	}
	return pr, nil
}

func (c *Client) GetReviews(ctx context.Context, number int) ([]Review, error) {
	var reviews []Review
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews?per_page=100", c.owner, c.repo, number)
	err := c.rest.Get(path, &reviews)
	if err != nil {
		return nil, fmt.Errorf("getting reviews for PR #%d: %w", number, err)
	}
	return reviews, nil
}

func (c *Client) GetIssueComments(ctx context.Context, number int) ([]IssueComment, error) {
	var comments []IssueComment
	path := fmt.Sprintf("repos/%s/%s/issues/%d/comments?per_page=100", c.owner, c.repo, number)
	err := c.rest.Get(path, &comments)
	if err != nil {
		return nil, fmt.Errorf("getting comments for PR #%d: %w", number, err)
	}
	return comments, nil
}

func (c *Client) GetReviewComments(ctx context.Context, number int) ([]ReviewComment, error) {
	var comments []ReviewComment
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/comments?per_page=100", c.owner, c.repo, number)
	err := c.rest.Get(path, &comments)
	if err != nil {
		return nil, fmt.Errorf("getting review comments for PR #%d: %w", number, err)
	}
	return comments, nil
}

func (c *Client) GetPullRequestFiles(ctx context.Context, number int) ([]PullRequestFile, error) {
	var files []PullRequestFile
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100", c.owner, c.repo, number)
	err := c.rest.Get(path, &files)
	if err != nil {
		return nil, fmt.Errorf("getting files for PR #%d: %w", number, err)
	}
	return files, nil
}

func (c *Client) GetCheckRuns(ctx context.Context, ref string) ([]CheckRun, error) {
	var result CheckRunsResponse
	path := fmt.Sprintf("repos/%s/%s/commits/%s/check-runs?per_page=100", c.owner, c.repo, ref)
	err := c.rest.Get(path, &result)
	if err != nil {
		return nil, fmt.Errorf("getting check runs for %s: %w", ref, err)
	}
	return result.CheckRuns, nil
}

func (c *Client) GetCurrentUser(ctx context.Context) (User, error) {
	var user User
	err := c.rest.Get("user", &user)
	if err != nil {
		return User{}, fmt.Errorf("getting current user: %w", err)
	}
	return user, nil
}

func (c *Client) GetBranchProtection(ctx context.Context, branch string) (*BranchProtection, error) {
	var result BranchProtection
	path := fmt.Sprintf("repos/%s/%s/branches/%s/protection", c.owner, c.repo, branch)
	err := c.rest.Get(path, &result)
	if err != nil {
		// Branch protection may not exist — return nil, not an error.
		return nil, nil
	}
	return &result, nil
}

// CreateReviewComment creates a new review comment on a pull request diff.
// For multi-line comments, set startLine > 0 and startSide to the side of the
// first line. When startLine is 0, a single-line comment is created.
func (c *Client) CreateReviewComment(ctx context.Context, number int, body, commitID, path string, line int, side string, startLine int, startSide string) (ReviewComment, error) {
	payload := struct {
		Body      string `json:"body"`
		CommitID  string `json:"commit_id"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Side      string `json:"side"`
		StartLine *int   `json:"start_line,omitempty"`
		StartSide string `json:"start_side,omitempty"`
	}{
		Body:     body,
		CommitID: commitID,
		Path:     path,
		Line:     line,
		Side:     side,
	}
	if startLine > 0 {
		payload.StartLine = &startLine
		payload.StartSide = startSide
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return ReviewComment{}, fmt.Errorf("marshaling comment: %w", err)
	}
	var result ReviewComment
	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%d/comments", c.owner, c.repo, number)
	err = c.rest.Post(apiPath, bytes.NewReader(jsonBody), &result)
	if err != nil {
		return ReviewComment{}, fmt.Errorf("creating review comment on PR #%d: %w", number, err)
	}
	return result, nil
}

// ReplyToReviewComment replies to an existing review comment thread.
func (c *Client) ReplyToReviewComment(ctx context.Context, number int, commentID int, body string) (ReviewComment, error) {
	payload := struct {
		Body string `json:"body"`
	}{Body: body}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return ReviewComment{}, fmt.Errorf("marshaling reply: %w", err)
	}
	var result ReviewComment
	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%d/comments/%d/replies", c.owner, c.repo, number, commentID)
	err = c.rest.Post(apiPath, bytes.NewReader(jsonBody), &result)
	if err != nil {
		return ReviewComment{}, fmt.Errorf("replying to comment %d on PR #%d: %w", commentID, number, err)
	}
	return result, nil
}

// GetFileContent fetches the content of a file at a specific git ref (branch, tag, or SHA).
func (c *Client) GetFileContent(ctx context.Context, filepath, ref string) (string, error) {
	var result struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	path := fmt.Sprintf("repos/%s/%s/contents/%s?ref=%s", c.owner, c.repo, filepath, ref)
	err := c.rest.Get(path, &result)
	if err != nil {
		return "", fmt.Errorf("getting file %s at %s: %w", filepath, ref, err)
	}
	if result.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(result.Content)
		if err != nil {
			return "", fmt.Errorf("decoding file %s: %w", filepath, err)
		}
		return string(decoded), nil
	}
	return result.Content, nil
}
