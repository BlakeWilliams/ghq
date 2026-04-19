package github

import (
	"context"
	"fmt"
	"time"

	"github.com/blakewilliams/gg/internal/cache"
)

// CachedClient wraps a Client with TanStack Query-style caching.
type CachedClient struct {
	client *Client
	cache  *cache.Cache
}

// NewCachedClient creates a CachedClient wrapping the given Client.
func NewCachedClient(client *Client, opts cache.Options) *CachedClient {
	return &CachedClient{
		client: client,
		cache:  cache.New(opts),
	}
}

// GCInterval returns the configured GC interval for tick commands.
func (c *CachedClient) GCInterval() time.Duration {
	return c.cache.GCInterval()
}

// GC runs garbage collection on the cache.
func (c *CachedClient) GC() {
	c.cache.GC()
}

// InvalidatePR removes cached data for a specific PR.
func (c *CachedClient) InvalidatePR(owner, repo string, number int) {
	key := fmt.Sprintf("%s/%s:pull:%d", owner, repo, number)
	c.cache.Invalidate(key)
	c.cache.Invalidate(fmt.Sprintf("%s/%s:pull-files:%d", owner, repo, number))
}

// InvalidateAll clears the entire cache.
func (c *CachedClient) InvalidateAll() {
	c.cache.InvalidatePrefix("")
}

// --- Cached query methods ---

func (c *CachedClient) ListPullRequests(owner, repo string) (data []PullRequest, found bool, refetch func() ([]PullRequest, error)) {
	key := fmt.Sprintf("%s/%s:pulls", owner, repo)
	fetchFn := func() ([]PullRequest, error) {
		return c.client.ListPullRequests(context.Background(), owner, repo)
	}
	data, found, _, refetch = cache.Query(c.cache, key, fetchFn)
	return
}

func (c *CachedClient) GetReviews(owner, repo string, number int) (data []Review, found bool, refetch func() ([]Review, error)) {
	key := fmt.Sprintf("%s/%s:reviews:%d", owner, repo, number)
	fetchFn := func() ([]Review, error) {
		return c.client.GetReviews(context.Background(), owner, repo, number)
	}
	data, found, _, refetch = cache.Query(c.cache, key, fetchFn)
	return
}

func (c *CachedClient) GetIssueComments(owner, repo string, number int) (data []IssueComment, found bool, refetch func() ([]IssueComment, error)) {
	key := fmt.Sprintf("%s/%s:comments:%d", owner, repo, number)
	fetchFn := func() ([]IssueComment, error) {
		return c.client.GetIssueComments(context.Background(), owner, repo, number)
	}
	data, found, _, refetch = cache.Query(c.cache, key, fetchFn)
	return
}

func (c *CachedClient) GetReviewComments(owner, repo string, number int) (data []ReviewComment, found bool, refetch func() ([]ReviewComment, error)) {
	key := fmt.Sprintf("%s/%s:review-comments:%d", owner, repo, number)
	fetchFn := func() ([]ReviewComment, error) {
		return c.client.GetReviewComments(context.Background(), owner, repo, number)
	}
	data, found, _, refetch = cache.Query(c.cache, key, fetchFn)
	return
}

func (c *CachedClient) GetCheckRuns(owner, repo, ref string) (data []CheckRun, found bool, refetch func() ([]CheckRun, error)) {
	key := fmt.Sprintf("%s/%s:check-runs:%s", owner, repo, ref)
	fetchFn := func() ([]CheckRun, error) {
		return c.client.GetCheckRuns(context.Background(), owner, repo, ref)
	}
	data, found, _, refetch = cache.Query(c.cache, key, fetchFn)
	return
}

func (c *CachedClient) GetPullRequestFiles(owner, repo string, number int) (data []PullRequestFile, found bool, refetch func() ([]PullRequestFile, error)) {
	key := fmt.Sprintf("%s/%s:pull-files:%d", owner, repo, number)
	fetchFn := func() ([]PullRequestFile, error) {
		return c.client.GetPullRequestFiles(context.Background(), owner, repo, number)
	}
	data, found, _, refetch = cache.Query(c.cache, key, fetchFn)
	return
}

func (c *CachedClient) GetFileContent(owner, repo, filename, ref string) (data string, found bool, refetch func() (string, error)) {
	key := fmt.Sprintf("%s/%s:file-content:%s:%s", owner, repo, ref, filename)
	fetchFn := func() (string, error) {
		return c.client.GetFileContent(context.Background(), owner, repo, filename, ref)
	}
	data, found, _, refetch = cache.Query(c.cache, key, fetchFn)
	return
}

// FetchFileContent synchronously fetches file content, using the cache.
func (c *CachedClient) FetchFileContent(owner, repo, filename, ref string) (string, error) {
	key := fmt.Sprintf("%s/%s:file-content:%s:%s", owner, repo, ref, filename)
	fetchFn := func() (string, error) {
		return c.client.GetFileContent(context.Background(), owner, repo, filename, ref)
	}

	data, found, _, refetchFn := cache.Query(c.cache, key, fetchFn)
	if found {
		return data, nil
	}
	if refetchFn != nil {
		return refetchFn()
	}
	return "", fmt.Errorf("failed to fetch %s", filename)
}

// --- Direct fetch methods (no caching) ---

func (c *CachedClient) FetchAuthenticatedUser() (User, error) {
	return c.client.GetAuthenticatedUser(context.Background())
}

func (c *CachedClient) FetchInbox(username, nwo string) ([]InboxPR, error) {
	prs, err := c.client.FetchInboxPRs(context.Background(), username, nwo)
	if err != nil {
		return nil, err
	}
	prs, _ = c.client.EnrichPRs(context.Background(), prs, username)
	return prs, nil
}

func (c *CachedClient) FetchPR(owner, repo string, number int) (PullRequest, error) {
	return c.client.GetPullRequest(context.Background(), owner, repo, number)
}

func (c *CachedClient) FetchPRByBranch(owner, repo, branch string) (PullRequest, error) {
	return c.client.GetPullRequestByBranch(context.Background(), owner, repo, branch)
}

func (c *CachedClient) CreateReviewComment(owner, repo string, number int, body, commitID, path string, line int, side string, startLine int, startSide string) (ReviewComment, error) {
	comment, err := c.client.CreateReviewComment(context.Background(), owner, repo, number, body, commitID, path, line, side, startLine, startSide)
	if err != nil {
		return ReviewComment{}, err
	}
	c.cache.Invalidate(fmt.Sprintf("%s/%s:review-comments:%d", owner, repo, number))
	return comment, nil
}

func (c *CachedClient) ReplyToReviewComment(owner, repo string, number int, commentID int, body string) (ReviewComment, error) {
	comment, err := c.client.ReplyToReviewComment(context.Background(), owner, repo, number, commentID, body)
	if err != nil {
		return ReviewComment{}, err
	}
	c.cache.Invalidate(fmt.Sprintf("%s/%s:review-comments:%d", owner, repo, number))
	return comment, nil
}
