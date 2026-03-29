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

// CommentsLoadedMsg is sent when PR comments are loaded.
type CommentsLoadedMsg struct {
	Comments []IssueComment
	Number   int
}

// GetIssueComments returns a tea.Cmd that fetches PR comments with caching.
func (c *CachedClient) GetIssueComments(number int) tea.Cmd {
	key := fmt.Sprintf("comments:%d", number)
	fetchFn := func() ([]IssueComment, error) {
		return c.client.GetIssueComments(context.Background(), number)
	}

	data, found, _, refetchFn := cache.Query(c.cache, key, fetchFn)

	var cmds []tea.Cmd

	if found {
		comments := data
		cmds = append(cmds, func() tea.Msg {
			return CommentsLoadedMsg{Comments: comments, Number: number}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return CommentsLoadedMsg{Comments: result, Number: number}
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

// FileContentLoadedMsg is sent when a file's content is loaded.
type FileContentLoadedMsg struct {
	Filename string
	Content  string
	Ref      string
}

// GetFileContent returns a tea.Cmd that fetches a file's content with caching.
func (c *CachedClient) GetFileContent(filename, ref string) tea.Cmd {
	key := fmt.Sprintf("file-content:%s:%s", ref, filename)
	fetchFn := func() (string, error) {
		return c.client.GetFileContent(context.Background(), filename, ref)
	}

	data, found, _, refetchFn := cache.Query(c.cache, key, fetchFn)

	var cmds []tea.Cmd

	if found {
		content := data
		cmds = append(cmds, func() tea.Msg {
			return FileContentLoadedMsg{Filename: filename, Content: content, Ref: ref}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return FileContentLoadedMsg{Filename: filename, Content: result, Ref: ref}
		})
	}

	return tea.Batch(cmds...)
}

// FetchFileContent synchronously fetches file content, using the cache.
// Intended for use inside a tea.Cmd goroutine.
func (c *CachedClient) FetchFileContent(filename, ref string) (string, error) {
	key := fmt.Sprintf("file-content:%s:%s", ref, filename)
	fetchFn := func() (string, error) {
		return c.client.GetFileContent(context.Background(), filename, ref)
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
