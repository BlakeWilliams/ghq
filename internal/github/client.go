package github

import (
	"context"
	"encoding/base64"
	"fmt"

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

func NewClient() (*Client, error) {
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, fmt.Errorf("creating REST client: %w", err)
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

func (c *Client) GetIssueComments(ctx context.Context, number int) ([]IssueComment, error) {
	var comments []IssueComment
	path := fmt.Sprintf("repos/%s/%s/issues/%d/comments?per_page=100", c.owner, c.repo, number)
	err := c.rest.Get(path, &comments)
	if err != nil {
		return nil, fmt.Errorf("getting comments for PR #%d: %w", number, err)
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
