package github

import (
	"context"
	"fmt"
	"time"

	"github.com/blakewilliams/ghq/internal/cache"
	tea "charm.land/bubbletea/v2"
)

// PRsLoadedMsg is sent when pull requests are loaded (from cache or network).
type PRsLoadedMsg struct {
	PRs []PullRequest
}

// PRFilesLoadedMsg is sent when PR files are loaded (from cache or network).
type PRFilesLoadedMsg struct {
	Files  []PullRequestFile
	Number int
}

// QueryErrMsg is sent when a cached query fails.
type QueryErrMsg struct {
	Err error
}

// GCTickMsg triggers garbage collection of stale cache entries.
type GCTickMsg struct{}

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

// RepoFullName returns the owner/repo string.
func (c *CachedClient) RepoFullName() string {
	return c.client.RepoFullName()
}

// ListPullRequests returns a tea.Cmd that fetches PRs with caching.
// If cached data exists, it's returned immediately. If stale, a background
// refetch is also triggered.
func (c *CachedClient) ListPullRequests() tea.Cmd {
	fetchFn := func() ([]PullRequest, error) {
		return c.client.ListPullRequests(context.Background())
	}

	data, found, _, refetchFn := cache.Query(c.cache, "pulls", fetchFn)

	var cmds []tea.Cmd

	if found {
		prs := data
		cmds = append(cmds, func() tea.Msg {
			return PRsLoadedMsg{PRs: prs}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return PRsLoadedMsg{PRs: result}
		})
	}

	return tea.Batch(cmds...)
}

// GetPullRequestFiles returns a tea.Cmd that fetches PR files with caching.
func (c *CachedClient) GetPullRequestFiles(number int) tea.Cmd {
	key := fmt.Sprintf("pull-files:%d", number)
	fetchFn := func() ([]PullRequestFile, error) {
		return c.client.GetPullRequestFiles(context.Background(), number)
	}

	data, found, _, refetchFn := cache.Query(c.cache, key, fetchFn)

	var cmds []tea.Cmd

	if found {
		files := data
		cmds = append(cmds, func() tea.Msg {
			return PRFilesLoadedMsg{Files: files, Number: number}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return PRFilesLoadedMsg{Files: result, Number: number}
		})
	}

	return tea.Batch(cmds...)
}

// InvalidatePR removes cached data for a specific PR.
func (c *CachedClient) InvalidatePR(number int) {
	c.cache.Invalidate(fmt.Sprintf("pull:%d", number))
	c.cache.Invalidate(fmt.Sprintf("pull-files:%d", number))
}

// InvalidateAll clears the entire cache.
func (c *CachedClient) InvalidateAll() {
	c.cache.InvalidatePrefix("")
}

// GCTickCmd returns a tea.Cmd that periodically triggers cache GC.
func (c *CachedClient) GCTickCmd() tea.Cmd {
	return tea.Tick(c.cache.GCInterval(), func(t time.Time) tea.Msg {
		return GCTickMsg{}
	})
}

// GC runs garbage collection on the cache.
func (c *CachedClient) GC() {
	c.cache.GC()
}
