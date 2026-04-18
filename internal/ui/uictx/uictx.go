package uictx

import (
	"fmt"
	"image/color"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

// QueryErrMsg is sent when a GitHub API query fails.
type QueryErrMsg struct {
	Err error
}

// PRLoadedMsg is sent when a single PR is loaded (shared by navigation flows).
type PRLoadedMsg struct {
	PR github.PullRequest
}

// FetchPR returns a tea.Cmd that fetches a single PR by owner/repo/number.
func FetchPR(c *github.CachedClient, owner, repo string, number int) tea.Cmd {
	return func() tea.Msg {
		pr, err := c.FetchPR(owner, repo, number)
		if err != nil {
			return QueryErrMsg{Err: err}
		}
		return PRLoadedMsg{PR: pr}
	}
}

// FetchPRByBranch returns a tea.Cmd that finds an open PR for the given branch.
func FetchPRByBranch(c *github.CachedClient, owner, repo, branch string) tea.Cmd {
	return func() tea.Msg {
		pr, err := c.FetchPRByBranch(owner, repo, branch)
		if err != nil {
			return QueryErrMsg{Err: err}
		}
		return PRLoadedMsg{PR: pr}
	}
}

// CachedCmd turns a cache query result into a tea.Cmd. If cached data is
// available it is returned immediately; if a refetch is needed it runs in the
// background. Errors produce QueryErrMsg.
func CachedCmd[T any](data T, found bool, refetch func() (T, error), wrap func(T) tea.Msg) tea.Cmd {
	var cmds []tea.Cmd

	if found {
		msg := wrap(data)
		cmds = append(cmds, func() tea.Msg { return msg })
	}

	if refetch != nil {
		fn := refetch
		cmds = append(cmds, func() tea.Msg {
			result, err := fn()
			if err != nil {
				return QueryErrMsg{Err: err}
			}
			return wrap(result)
		})
	}

	return tea.Batch(cmds...)
}

// BrightnessModify applies lualine's brightness_modifier formula to an
// existing color. Positive pct lightens, negative darkens. Returns a new
// lipgloss.Color hex string.
func BrightnessModify(c color.Color, pct float64) color.Color {
	if c == nil {
		return lipgloss.BrightBlack
	}
	r, g, b, _ := c.RGBA()
	rr := clampByte(int(float64(r>>8) + float64(r>>8)*pct/100))
	gg := clampByte(int(float64(g>>8) + float64(g>>8)*pct/100))
	bb := clampByte(int(float64(b>>8) + float64(b>>8)*pct/100))
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", rr, gg, bb))
}

func clampByte(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

type Context struct {
	Client     *github.CachedClient
	DiffColors styles.DiffColors
	ChromeColor color.Color // separator/header color derived from terminal bg
	Username   string
	Owner      string // repo owner (from flag or detected)
	Repo       string // repo name
}

// KeyBinding describes a key and what it does, for help display.
type KeyBinding struct {
	Key         string // e.g. "j / k", "ctrl+d"
	Description string // e.g. "Move cursor down / up"
	Keywords    []string // extra search terms for fuzzy matching
}

type View interface {
	Init() tea.Cmd
	Update(tea.Msg) (View, tea.Cmd)
	HandleKey(tea.KeyPressMsg) (View, tea.Cmd, bool)
	View() string
	// KeyBindings returns the keybindings for this view, for the help picker.
	KeyBindings() []KeyBinding
}
