use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use notify::{Event, EventKind, RecursiveMode, Watcher as NotifyWatcher};
use tokio::sync::{Notify, mpsc};

const DEBOUNCE: Duration = Duration::from_millis(500);

static SKIP_DIRS: &[&str] = &[
    ".git", "node_modules", "vendor", ".next", "dist", "build", "__pycache__", "target",
];

/// Watches a git repository for file changes and HEAD/branch changes.
/// Sends debounced signals over channels.
pub struct RepoWatcher {
    _watcher: Box<dyn NotifyWatcher + Send>,
    shutdown: Arc<Notify>,
}

/// Events produced by the watcher.
#[derive(Debug, Clone)]
pub enum WatchEvent {
    FilesChanged,
    BranchChanged(String),
}

impl RepoWatcher {
    pub fn new(repo_root: &str, tx: mpsc::UnboundedSender<WatchEvent>) -> anyhow::Result<Self> {
        let repo_root = PathBuf::from(repo_root).canonicalize()?;
        let shutdown = Arc::new(Notify::new());

        let git_dir = repo_root.join(".git");
        let _head_file = git_dir.join("HEAD");
        let _refs_heads = git_dir.join("refs").join("heads");
        let repo_for_filter = repo_root.clone();
        let shutdown_clone = shutdown.clone();

        // Channel from sync notify callback → async debounce task
        let (raw_tx, mut raw_rx) = mpsc::unbounded_channel::<RawEvent>();

        let mut watcher = notify::recommended_watcher(move |res: Result<Event, notify::Error>| {
            if let Ok(event) = res {
                tracing::info!("notify raw event: {:?} {:?}", event.kind, event.paths);
                let _ = raw_tx.send(RawEvent {
                    paths: event.paths,
                    kind: event.kind,
                });
            }
        })?;

        // Watch repo root recursively
        watcher.watch(&repo_root, RecursiveMode::Recursive)?;

        // Spawn debounce task
        let tx_clone = tx;
        tokio::spawn(async move {
            let mut file_pending = false;
            let mut head_pending = false;
            let git_dir = repo_for_filter.join(".git");
            let head_file_path = git_dir.join("HEAD");
            let refs_heads_path = git_dir.join("refs").join("heads");

            loop {
                let timeout = if file_pending || head_pending {
                    DEBOUNCE
                } else {
                    Duration::from_secs(3600) // effectively infinite
                };

                tokio::select! {
                    _ = shutdown_clone.notified() => break,
                    raw = raw_rx.recv() => {
                        let Some(raw) = raw else { break };

                        if !matches!(raw.kind,
                            EventKind::Create(_) | EventKind::Modify(_) | EventKind::Remove(_)
                        ) {
                            continue;
                        }

                        for path in &raw.paths {
                            // HEAD change → branch switch
                            if path == &head_file_path {
                                head_pending = true;
                                continue;
                            }

                            // Ref change in refs/heads → treat as file change (commit)
                            if path.starts_with(&refs_heads_path) {
                                file_pending = true;
                                continue;
                            }

                            // Skip .git internals
                            if path.starts_with(&git_dir) {
                                continue;
                            }

                            // Skip hidden/temp files
                            if let Some(name) = path.file_name().and_then(|n| n.to_str()) {
                                if name.starts_with('.')
                                    || name.ends_with('~')
                                    || name.ends_with(".swp")
                                    || name.ends_with(".swo")
                                {
                                    continue;
                                }
                            }

                            // Skip known non-source dirs
                            let should_skip = path.ancestors().any(|a| {
                                a.file_name()
                                    .and_then(|n| n.to_str())
                                    .is_some_and(|n| SKIP_DIRS.contains(&n))
                            });
                            if should_skip {
                                continue;
                            }

                            file_pending = true;
                        }
                    }
                    _ = tokio::time::sleep(timeout), if file_pending || head_pending => {
                        if head_pending {
                            head_pending = false;
                            if let Some(branch) = read_branch(&repo_for_filter) {
                                tracing::info!("watcher: branch changed to {branch}");
                                let _ = tx_clone.send(WatchEvent::BranchChanged(branch));
                            }
                        }
                        if file_pending {
                            file_pending = false;
                            tracing::info!("watcher: sending FilesChanged");
                            let _ = tx_clone.send(WatchEvent::FilesChanged);
                        }
                    }
                }
            }
        });

        Ok(Self {
            _watcher: Box::new(watcher),
            shutdown,
        })
    }

}

impl Drop for RepoWatcher {
    fn drop(&mut self) {
        self.shutdown.notify_one();
    }
}

struct RawEvent {
    paths: Vec<PathBuf>,
    kind: EventKind,
}

fn read_branch(repo_root: &Path) -> Option<String> {
    let data = std::fs::read_to_string(repo_root.join(".git").join("HEAD")).ok()?;
    let trimmed = data.trim();
    trimmed.strip_prefix("ref: refs/heads/").map(|s| s.to_string())
}
