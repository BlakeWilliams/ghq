// Package config loads user configuration from $XDG_CONFIG_HOME/gg/config.yaml.
//
// The config file is optional; missing files yield defaults. Unknown fields
// are ignored so old binaries don't break on newer configs.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the user-facing configuration loaded from disk.
type Config struct {
	// HelpMode toggles inline UI help (shortcut hints next to mode badges,
	// contextual help row at the bottom of the file viewer, etc.).
	// Defaults to true.
	HelpMode bool `yaml:"help_mode"`

	// CommitPrompt is additional user instructions appended to the base
	// commit-message generation prompt (e.g. "use emoji prefixes",
	// "write in Spanish"). Leave empty for default behavior.
	CommitPrompt string `yaml:"commit_prompt"`

	// PRPrompt is additional user instructions appended to the base
	// PR description generation prompt. Leave empty for default behavior.
	PRPrompt string `yaml:"pull_request_prompt"`

	// CommentPanelMinWidth is the minimum width for the side comment panel.
	// The panel only appears when the terminal is wide enough to fit both
	// this and DiffMinWidth. Default: 55.
	CommentPanelMinWidth int `yaml:"comment_panel_min_width"`

	// DiffMinWidth is the minimum width reserved for the diff content when
	// the comment panel is shown beside it. Default: 90.
	DiffMinWidth int `yaml:"diff_min_width"`
}

// Default returns a Config with all defaults applied.
func Default() Config {
	return Config{
		HelpMode:             true,
		CommentPanelMinWidth: 55,
		DiffMinWidth:         90,
	}
}

// raw mirrors Config but uses pointers so we can detect "field not set"
// vs "field set to zero value" and apply defaults appropriately.
type raw struct {
	HelpMode             *bool   `yaml:"help_mode"`
	CommitPrompt         *string `yaml:"commit_prompt"`
	PRPrompt             *string `yaml:"pull_request_prompt"`
	CommentPanelMinWidth *int    `yaml:"comment_panel_min_width"`
	DiffMinWidth         *int    `yaml:"diff_min_width"`
}

// Load reads the user config from the standard XDG location, falling back
// to ~/.config/gg/config.yaml. Missing files return defaults without error.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}
	return LoadFrom(path)
}

// LoadFrom reads config from an explicit path. Missing files return defaults.
func LoadFrom(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}

	var r raw
	if err := yaml.Unmarshal(data, &r); err != nil {
		return cfg, err
	}
	if r.HelpMode != nil {
		cfg.HelpMode = *r.HelpMode
	}
	if r.CommitPrompt != nil {
		cfg.CommitPrompt = *r.CommitPrompt
	}
	if r.PRPrompt != nil {
		cfg.PRPrompt = *r.PRPrompt
	}
	if r.CommentPanelMinWidth != nil {
		cfg.CommentPanelMinWidth = *r.CommentPanelMinWidth
	}
	if r.DiffMinWidth != nil {
		cfg.DiffMinWidth = *r.DiffMinWidth
	}

	// Clamp to sane minimums.
	if cfg.CommentPanelMinWidth < 30 {
		cfg.CommentPanelMinWidth = 30
	}
	if cfg.DiffMinWidth < 40 {
		cfg.DiffMinWidth = 40
	}

	return cfg, nil
}

// Path returns the resolved config file path. It honors $XDG_CONFIG_HOME
// when set, otherwise falls back to ~/.config.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Dir returns the directory that holds gg's config files.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gg"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "gg"), nil
}
