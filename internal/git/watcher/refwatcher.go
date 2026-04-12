package watcher

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"
)

// RefChangedMsg signals that a git ref (branch pointer) changed, likely due to a push.
type RefChangedMsg struct{}

// RefWatcher watches .git/refs/heads/<branch> for changes to detect pushes.
type RefWatcher struct {
	fsw      *fsnotify.Watcher
	events   chan struct{}
	done     chan struct{}
	once     sync.Once
}

// NewRefWatcher creates a watcher on the git ref for the given branch.
func NewRefWatcher(repoRoot, branch string) (*RefWatcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &RefWatcher{
		fsw:    fsw,
		events: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}

	// Watch the refs/heads directory for the branch file.
	refsDir := filepath.Join(repoRoot, ".git", "refs", "heads")
	if _, err := os.Stat(refsDir); err == nil {
		fsw.Add(refsDir)
	}

	// Also watch packed-refs which updates on some push operations.
	packedRefs := filepath.Join(repoRoot, ".git", "packed-refs")
	if _, err := os.Stat(packedRefs); err == nil {
		fsw.Add(filepath.Dir(packedRefs))
	}

	go w.loop(branch)

	return w, nil
}

func (w *RefWatcher) loop(branch string) {
	const debounce = 500 * time.Millisecond
	var timer *time.Timer
	branchFile := branch // just the filename, not full path

	for {
		select {
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}
			// Only trigger on the specific branch file or packed-refs.
			base := filepath.Base(event.Name)
			if base != branchFile && base != "packed-refs" {
				continue
			}
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

// WaitCmd returns a tea.Cmd that blocks until a ref change is detected.
func (w *RefWatcher) WaitCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-w.events:
			return RefChangedMsg{}
		case <-w.done:
			return nil
		}
	}
}

// Close stops the watcher.
func (w *RefWatcher) Close() {
	w.once.Do(func() {
		close(w.done)
		w.fsw.Close()
	})
}
