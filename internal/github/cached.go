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

// CurrentUserLoadedMsg is sent when the authenticated user is loaded.
type CurrentUserLoadedMsg struct {
	User User
}

// GetCurrentUser returns a tea.Cmd that fetches the authenticated user with caching.
func (c *CachedClient) GetCurrentUser() tea.Cmd {
	fetchFn := func() (User, error) {
		return c.client.GetCurrentUser(context.Background())
	}

	data, found, _, refetchFn := cache.Query(c.cache, "current-user", fetchFn)

	var cmds []tea.Cmd

	if found {
		user := data
		cmds = append(cmds, func() tea.Msg {
			return CurrentUserLoadedMsg{User: user}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return CurrentUserLoadedMsg{User: result}
		})
	}

	return tea.Batch(cmds...)
}

// ReviewsLoadedMsg is sent when PR reviews are loaded.
type ReviewsLoadedMsg struct {
	Reviews []Review
	Number  int
}

// GetReviews returns a tea.Cmd that fetches PR reviews with caching.
func (c *CachedClient) GetReviews(number int) tea.Cmd {
	key := fmt.Sprintf("reviews:%d", number)
	fetchFn := func() ([]Review, error) {
		return c.client.GetReviews(context.Background(), number)
	}

	data, found, _, refetchFn := cache.Query(c.cache, key, fetchFn)

	var cmds []tea.Cmd

	if found {
		reviews := data
		cmds = append(cmds, func() tea.Msg {
			return ReviewsLoadedMsg{Reviews: reviews, Number: number}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return ReviewsLoadedMsg{Reviews: result, Number: number}
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

// ReviewCommentsLoadedMsg is sent when PR review comments (inline diff comments) are loaded.
type ReviewCommentsLoadedMsg struct {
	Comments []ReviewComment
	Number   int
}

// GetReviewComments returns a tea.Cmd that fetches PR review comments with caching.
func (c *CachedClient) GetReviewComments(number int) tea.Cmd {
	key := fmt.Sprintf("review-comments:%d", number)
	fetchFn := func() ([]ReviewComment, error) {
		return c.client.GetReviewComments(context.Background(), number)
	}

	data, found, _, refetchFn := cache.Query(c.cache, key, fetchFn)

	var cmds []tea.Cmd

	if found {
		comments := data
		cmds = append(cmds, func() tea.Msg {
			return ReviewCommentsLoadedMsg{Comments: comments, Number: number}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return ReviewCommentsLoadedMsg{Comments: result, Number: number}
		})
	}

	return tea.Batch(cmds...)
}

// CheckRunsLoadedMsg is sent when check runs are loaded.
type CheckRunsLoadedMsg struct {
	CheckRuns []CheckRun
	Ref       string
}

// GetCheckRuns returns a tea.Cmd that fetches check runs with caching.
func (c *CachedClient) GetCheckRuns(ref string) tea.Cmd {
	key := fmt.Sprintf("check-runs:%s", ref)
	fetchFn := func() ([]CheckRun, error) {
		return c.client.GetCheckRuns(context.Background(), ref)
	}

	data, found, _, refetchFn := cache.Query(c.cache, key, fetchFn)

	var cmds []tea.Cmd

	if found {
		checks := data
		cmds = append(cmds, func() tea.Msg {
			return CheckRunsLoadedMsg{CheckRuns: checks, Ref: ref}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return CheckRunsLoadedMsg{CheckRuns: result, Ref: ref}
		})
	}

	return tea.Batch(cmds...)
}

// BranchProtectionLoadedMsg is sent when branch protection is loaded.
type BranchProtectionLoadedMsg struct {
	Protection *BranchProtection
	Branch     string
}

// GetBranchProtection returns a tea.Cmd that fetches branch protection with caching.
func (c *CachedClient) GetBranchProtection(branch string) tea.Cmd {
	key := fmt.Sprintf("branch-protection:%s", branch)
	fetchFn := func() (*BranchProtection, error) {
		return c.client.GetBranchProtection(context.Background(), branch)
	}

	data, found, _, refetchFn := cache.Query(c.cache, key, fetchFn)

	var cmds []tea.Cmd

	if found {
		prot := data
		cmds = append(cmds, func() tea.Msg {
			return BranchProtectionLoadedMsg{Protection: prot, Branch: branch}
		})
	}

	if refetchFn != nil {
		fn := refetchFn
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return BranchProtectionLoadedMsg{Protection: result, Branch: branch}
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

// CommentCreatedMsg is sent when a review comment is successfully created.
type CommentCreatedMsg struct {
	Comment ReviewComment
	Number  int
}

// CommentErrorMsg is sent when creating a comment fails.
type CommentErrorMsg struct {
	Err error
}

// CreateReviewComment returns a tea.Cmd that posts a new review comment.
// For multi-line comments, set startLine > 0 and startSide.
func (c *CachedClient) CreateReviewComment(number int, body, commitID, path string, line int, side string, startLine int, startSide string) tea.Cmd {
	return func() tea.Msg {
		comment, err := c.client.CreateReviewComment(context.Background(), number, body, commitID, path, line, side, startLine, startSide)
		if err != nil {
			return CommentErrorMsg{Err: err}
		}
		// Invalidate review comments cache so next fetch picks up the new comment.
		c.cache.Invalidate(fmt.Sprintf("review-comments:%d", number))
		return CommentCreatedMsg{Comment: comment, Number: number}
	}
}

// ReplyToReviewComment returns a tea.Cmd that replies to an existing comment thread.
func (c *CachedClient) ReplyToReviewComment(number int, commentID int, body string) tea.Cmd {
	return func() tea.Msg {
		comment, err := c.client.ReplyToReviewComment(context.Background(), number, commentID, body)
		if err != nil {
			return CommentErrorMsg{Err: err}
		}
		c.cache.Invalidate(fmt.Sprintf("review-comments:%d", number))
		return CommentCreatedMsg{Comment: comment, Number: number}
	}
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
