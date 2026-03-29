package github

import (
	"context"
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

func (c *Client) GetPullRequestFiles(ctx context.Context, number int) ([]PullRequestFile, error) {
	var files []PullRequestFile
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100", c.owner, c.repo, number)
	err := c.rest.Get(path, &files)
	if err != nil {
		return nil, fmt.Errorf("getting files for PR #%d: %w", number, err)
	}
	return files, nil
}
