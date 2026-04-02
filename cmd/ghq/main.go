package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/blakewilliams/ghq/internal/cache"
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

	p := tea.NewProgram(ui.NewApp(cachedClient))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
