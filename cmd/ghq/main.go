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
	owner := flag.String("owner", "", "repository owner")
	repo := flag.String("repo", "", "repository name")
	flag.Parse()

	client, err := github.NewClient(*owner, *repo)
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

	p := tea.NewProgram(ui.NewApp(ui.AppConfig{
		Client:   cachedClient,
		Owner:    *owner,
		Repo:     *repo,
		RepoRoot: repoRoot,
	}))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
