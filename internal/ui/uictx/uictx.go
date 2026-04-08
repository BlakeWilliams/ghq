package uictx

import (
	tea "charm.land/bubbletea/v2"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

type Context struct {
	Client     *github.CachedClient
	DiffColors styles.DiffColors
	Username   string
	NWO        string // optional repo filter (owner/repo)
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
