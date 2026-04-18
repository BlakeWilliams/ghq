package main

import (
	"fmt"
	"os"
	"time"

	"github.com/blakewilliams/ghq/internal/cache"
	"github.com/blakewilliams/ghq/internal/config"
	"github.com/blakewilliams/ghq/internal/git"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui"
	"github.com/cli/go-gh/v2/pkg/repository"
	tea "charm.land/bubbletea/v2"
)

func main() {
	// Detect owner/repo from local git remote.
	var detectedOwner, detectedRepo string
	if r, err := repository.Current(); err == nil {
		detectedOwner = r.Owner
		detectedRepo = r.Name
	}

	client, err := github.NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	cachedClient := github.NewCachedClient(client, cache.Options{
		StaleTime:  30 * time.Second,
		GCTime:     5 * time.Minute,
		GCInterval: 1 * time.Minute,
	})

	repoRoot, _ := git.RepoRoot(".")
	if repoRoot == "" {
		fmt.Fprintf(os.Stderr, "Error: not in a git repository\n")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		// Non-fatal — fall back to defaults but warn.
		fmt.Fprintf(os.Stderr, "Warning: failed to load config: %v\n", err)
		cfg = config.Default()
	}

	p := tea.NewProgram(ui.NewApp(ui.AppConfig{
		Client:   cachedClient,
		Owner:    detectedOwner,
		Repo:     detectedRepo,
		RepoRoot: repoRoot,
		Config:   cfg,
	}))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
