package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/blakewilliams/ghq/internal/cache"
	"github.com/blakewilliams/ghq/internal/git"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui"
	tea "charm.land/bubbletea/v2"
)

func main() {
	nwo := flag.String("nwo", "", "repository in owner/repo format (defaults to current directory)")
	flag.Parse()

	client, err := github.NewClient(*nwo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	cachedClient := github.NewCachedClient(client, cache.Options{
		StaleTime:  30 * time.Second,
		GCTime:     5 * time.Minute,
		GCInterval: 1 * time.Minute,
	})

	// Detect git repo for local diff view.
	var repoRoot string
	if git.IsGitRepo(".") {
		repoRoot, _ = git.RepoRoot(".")
	}

	p := tea.NewProgram(ui.NewApp(cachedClient, *nwo, repoRoot))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
