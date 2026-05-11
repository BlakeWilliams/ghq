package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Client is a stateless GitHub API client. Every repo-scoped call
// takes owner and repo as explicit arguments.
type Client struct {
	rest *api.RESTClient
	gql  *api.GraphQLClient
}

// NewClient creates a GitHub API client.
func NewClient() (*Client, error) {
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, fmt.Errorf("creating REST client: %w", err)
	}

	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("creating GraphQL client: %w", err)
	}

	return &Client{rest: rest, gql: gql}, nil
}

// GetAuthenticatedUser returns the currently authenticated GitHub user.
func (c *Client) GetAuthenticatedUser(ctx context.Context) (User, error) {
	var user User
	err := c.rest.Get("user", &user)
	if err != nil {
		return user, fmt.Errorf("getting authenticated user: %w", err)
	}
	return user, nil
}

func (c *Client) ListPullRequests(ctx context.Context, owner, repo string) ([]PullRequest, error) {
	var prs []PullRequest
	path := fmt.Sprintf("repos/%s/%s/pulls?state=open&per_page=30", owner, repo)
	err := c.rest.Get(path, &prs)
	if err != nil {
		return nil, fmt.Errorf("listing pull requests: %w", err)
	}
	return prs, nil
}

func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	var pr PullRequest
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number)
	err := c.rest.Get(path, &pr)
	if err != nil {
		return pr, fmt.Errorf("getting pull request #%d: %w", number, err)
	}
	return pr, nil
}

func (c *Client) GetPullRequestByBranch(ctx context.Context, owner, repo, branch string) (PullRequest, error) {
	var prs []PullRequest
	path := fmt.Sprintf("repos/%s/%s/pulls?state=open&head=%s:%s&per_page=1", owner, repo, owner, branch)
	err := c.rest.Get(path, &prs)
	if err != nil {
		return PullRequest{}, fmt.Errorf("finding PR for branch %s: %w", branch, err)
	}
	if len(prs) == 0 {
		return PullRequest{}, fmt.Errorf("no open PR found for branch %s", branch)
	}
	return prs[0], nil
}

func (c *Client) GetReviews(ctx context.Context, owner, repo string, number int) ([]Review, error) {
	var reviews []Review
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews?per_page=100", owner, repo, number)
	err := c.rest.Get(path, &reviews)
	if err != nil {
		return nil, fmt.Errorf("getting reviews: %w", err)
	}
	return reviews, nil
}

func (c *Client) GetIssueComments(ctx context.Context, owner, repo string, number int) ([]IssueComment, error) {
	var comments []IssueComment
	path := fmt.Sprintf("repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, number)
	err := c.rest.Get(path, &comments)
	if err != nil {
		return nil, fmt.Errorf("getting issue comments: %w", err)
	}
	return comments, nil
}

func (c *Client) GetReviewComments(ctx context.Context, owner, repo string, number int) ([]ReviewComment, error) {
	var comments []ReviewComment
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/comments?per_page=100", owner, repo, number)
	err := c.rest.Get(path, &comments)
	if err != nil {
		return nil, fmt.Errorf("getting review comments: %w", err)
	}
	return comments, nil
}

func (c *Client) GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]PullRequestFile, error) {
	var files []PullRequestFile
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, number)
	err := c.rest.Get(path, &files)
	if err != nil {
		return nil, fmt.Errorf("getting pull request files: %w", err)
	}
	return files, nil
}

func (c *Client) GetCheckRuns(ctx context.Context, owner, repo, ref string) ([]CheckRun, error) {
	var resp CheckRunsResponse
	path := fmt.Sprintf("repos/%s/%s/commits/%s/check-runs?per_page=100", owner, repo, ref)
	err := c.rest.Get(path, &resp)
	if err != nil {
		return nil, fmt.Errorf("getting check runs: %w", err)
	}
	return resp.CheckRuns, nil
}

func (c *Client) CreateReviewComment(ctx context.Context, owner, repo string, number int, body, commitID, path string, line int, side string, startLine int, startSide string) (ReviewComment, error) {
	reqBody := map[string]interface{}{
		"body":      body,
		"commit_id": commitID,
		"path":      path,
		"line":      line,
		"side":      side,
	}
	if startLine > 0 {
		reqBody["start_line"] = startLine
		reqBody["start_side"] = startSide
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return ReviewComment{}, err
	}

	apiPath := fmt.Sprintf("repos/%s/%s/pulls/%d/comments", owner, repo, number)
	var comment ReviewComment
	err = c.rest.Post(apiPath, bytes.NewReader(jsonBody), &comment)
	if err != nil {
		return ReviewComment{}, fmt.Errorf("creating review comment: %w", err)
	}
	return comment, nil
}

func (c *Client) ReplyToReviewComment(ctx context.Context, owner, repo string, number int, commentID int, body string) (ReviewComment, error) {
	reqBody := map[string]interface{}{
		"body": body,
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return ReviewComment{}, err
	}

	apiPath := fmt.Sprintf("repos/%s/%s/pulls/comments/%d/replies", owner, repo, commentID)
	var comment ReviewComment
	err = c.rest.Post(apiPath, bytes.NewReader(jsonBody), &comment)
	if err != nil {
		return ReviewComment{}, fmt.Errorf("replying to review comment: %w", err)
	}
	return comment, nil
}

func (c *Client) GetFileContent(ctx context.Context, owner, repo, filepath, ref string) (string, error) {
	var resp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	path := fmt.Sprintf("repos/%s/%s/contents/%s?ref=%s", owner, repo, filepath, ref)
	err := c.rest.Get(path, &resp)
	if err != nil {
		return "", fmt.Errorf("getting file content: %w", err)
	}
	if resp.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(resp.Content)
		if err != nil {
			return "", fmt.Errorf("decoding base64 content: %w", err)
		}
		return string(decoded), nil
	}
	return resp.Content, nil
}

// SearchIssues searches GitHub issues/PRs with the given query string.
func (c *Client) SearchIssues(ctx context.Context, query string) ([]SearchIssueResult, error) {
	var resp SearchResponse
	path := fmt.Sprintf("search/issues?q=%s&per_page=100&sort=updated&order=desc", query)
	err := c.rest.Get(path, &resp)
	if err != nil {
		return nil, fmt.Errorf("searching issues: %w", err)
	}
	return resp.Items, nil
}
