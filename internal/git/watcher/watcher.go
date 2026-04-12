package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"
)

// FileChangedMsg signals that files in the repo changed.
type FileChangedMsg struct{}

// Watcher watches a git repository for file changes and produces
// debounced FileChangedMsg events for the Bubble Tea message loop.
type Watcher struct {
	fsw      *fsnotify.Watcher
	repoRoot string
	events   chan struct{}
	done     chan struct{}
	once     sync.Once
}

// New creates a watcher for the given repo root. It watches:
// - The repo root directory for top-level file changes
// - Specific subdirectories that contain changed files
// Note: We intentionally do NOT watch .git/ to avoid feedback loops
// when git commands read/write internal state.
func New(repoRoot string, changedDirs []string) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		fsw:      fsw,
		repoRoot: repoRoot,
		events:   make(chan struct{}, 1),
		done:     make(chan struct{}),
	}

	// Watch the repo root for top-level file changes.
	fsw.Add(repoRoot)

	// Watch .git/refs/heads/ recursively to detect commits.
	addDirRecursive(fsw, filepath.Join(repoRoot, ".git", "refs", "heads"))

	// Watch all directories in the repo (excluding .git).
	// This ensures we detect edits in any subdirectory.
	addRepoDirectories(fsw, repoRoot)

	// Also watch any extra dirs specified by the caller.
	for _, dir := range changedDirs {
		absDir := dir
		if !filepath.IsAbs(dir) {
			absDir = filepath.Join(repoRoot, dir)
		}
		if info, err := os.Stat(absDir); err == nil && info.IsDir() {
			fsw.Add(absDir)
		}
	}

	go w.loop()

	return w, nil
}

func (w *Watcher) loop() {
	const debounce = 500 * time.Millisecond
	var timer *time.Timer

	for {
		select {
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			// Only care about actual writes/creates/removes.
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) &&
				!event.Has(fsnotify.Remove) && !event.Has(fsnotify.Rename) {
				continue
			}
			// Ignore anything inside .git/.
			if isGitInternal(w.repoRoot, event.Name) {
				continue
			}
			// Ignore hidden files and common non-source files.
			base := filepath.Base(event.Name)
			if strings.HasPrefix(base, ".") || strings.HasSuffix(base, "~") ||
				strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".swo") {
				continue
			}
			// Debounce: reset timer on each event.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounce, func() {
				select {
				case w.events <- struct{}{}:
				default:
				}
			})
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		case <-w.done:
			if timer != nil {
				timer.Stop()
			}
			return
		}
	}
}

// isGitInternal returns true for paths inside .git/, except for
// .git/refs/heads/ which we watch to detect commits.
func isGitInternal(repoRoot, path string) bool {
	gitDir := filepath.Join(repoRoot, ".git")
	if !strings.HasPrefix(path, gitDir+string(os.PathSeparator)) && path != gitDir {
		return false
	}
	// Allow refs/heads changes through (commit detection).
	refsHeads := filepath.Join(gitDir, "refs", "heads")
	if strings.HasPrefix(path, refsHeads) {
		return false
	}
	return true
}

// WaitCmd returns a tea.Cmd that blocks until a file change is detected.
func (w *Watcher) WaitCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-w.events:
			return FileChangedMsg{}
		case <-w.done:
			return nil
		}
	}
}

// UpdateDirs adds directories to the watch list. Call after a diff load
// to watch subdirectories that contain changed files.
func (w *Watcher) UpdateDirs(dirs []string) {
	for _, dir := range dirs {
		absDir := dir
		if !filepath.IsAbs(dir) {
			absDir = filepath.Join(w.repoRoot, dir)
		}
		if info, err := os.Stat(absDir); err == nil && info.IsDir() {
			w.fsw.Add(absDir)
		}
	}
}

// Close stops the watcher and releases resources.
func (w *Watcher) Close() {
	w.once.Do(func() {
		close(w.done)
		w.fsw.Close()
	})
}

// addRepoDirectories walks the repo and adds all directories to the watcher,
// skipping .git, node_modules, vendor, and other common non-source dirs.
func addRepoDirectories(fsw *fsnotify.Watcher, repoRoot string) {
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		".next": true, "dist": true, "build": true, "__pycache__": true,
	}
	filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if skipDirs[base] {
			return filepath.SkipDir
		}
		fsw.Add(path)
		return nil
	})
}

// addDirRecursive adds a directory and all subdirectories to the watcher.
func addDirRecursive(fsw *fsnotify.Watcher, dir string) {
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			fsw.Add(path)
		}
		return nil
	})
}

// DirsFromFiles extracts unique directory paths from a list of file paths.
func DirsFromFiles(filenames []string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, f := range filenames {
		dir := filepath.Dir(f)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}
