pub mod keybinds;

use std::sync::Arc;

use crossterm::event::KeyEvent;
use ratatui::layout::Rect;
use ratatui::Frame;
use tokio::sync::mpsc;

use crate::agent::AgentRunner;
use crate::agent::types::{AgentEvent, EventKind, EventPayload};
use crate::git::diff::{self, DiffMode};
use crate::github::CachedClient;
use crate::github::types::{PullRequest, PullRequestFile, ReviewComment};
use crate::review::comments::{CommentAuthor, CommentStore, ContentBlock, LocalComment, ToolEntry};
use super::commit::{CommitEvent, CommitOverlay, COMMIT_GEN_PREFIX};
use super::composing_state::ComposingState;
use super::copilot_state::{CopilotState, CompletedReply, ToolCall, ToolGroup, ToolStatus};
use super::diff_viewer::{DiffLineData, DiffViewer, LayoutInfo, LineType, TREE_WIDTH};
use super::diff_viewer::panel::{self, PanelComment};
use super::picker::Picker;
use super::scroll::Scrollable;
use super::styles::DiffColors;

pub enum Pane {
    Tree,
    Diff,
    Panel,
}

/// Result of a background diff load. Delivered via channel to avoid blocking
/// the event loop while git subprocesses run.
pub enum DiffLoaded {
    /// Initial load or mode switch — resets cursor/scroll.
    Full {
        files: Vec<PullRequestFile>,
    },
    /// File-watcher reload — preserves cursor/scroll position.
    Reload {
        files: Vec<PullRequestFile>,
        branch: Option<String>,
    },
}

/// Events arriving from background GitHub API calls.
pub enum GithubEvent {
    PrDetected(PullRequest),
    ReviewComments(Vec<ReviewComment>),
    Error(String),
}

pub struct HighlightResult {
    pub filename: String,
    pub hl_new: Vec<Vec<(ratatui::style::Style, String)>>,
    pub hl_old: Vec<Vec<(ratatui::style::Style, String)>>,
}

pub struct LocalDiff {
    pub viewer: DiffViewer,
    pub repo_root: String,
    pub mode: DiffMode,
    pub base_branch: String,
    pub merge_base_ref: String,
    pub default_branch: String,
    pub composing: ComposingState,
    pub comment_store: CommentStore,
    pub copilot_state: CopilotState,
    pub username: Option<String>,
    pub diff_tx: mpsc::UnboundedSender<DiffLoaded>,
    pub highlight_tx: mpsc::UnboundedSender<HighlightResult>,
    pub highlight_rx: mpsc::UnboundedReceiver<HighlightResult>,
    pub github_tx: mpsc::UnboundedSender<GithubEvent>,
    pub github_rx: mpsc::UnboundedReceiver<GithubEvent>,
    pub github: Option<CachedClient>,
    pub owner: String,
    pub repo_name: String,
    pub colors: DiffColors,
    pub picker: Option<Picker>,
    pub picker_kind: String,
    pub pr: Option<PullRequest>,
    pub pr_loaded: bool,
    pub flash: super::flash::FlashState,
    pub commit_overlay: Option<CommitOverlay>,
    pub commit_tx: mpsc::UnboundedSender<CommitEvent>,
    pub commit_rx: mpsc::UnboundedReceiver<CommitEvent>,
}

impl LocalDiff {
    pub fn new(repo_root: String, mode: DiffMode, diff_tx: mpsc::UnboundedSender<DiffLoaded>) -> Self {
        let comment_store = CommentStore::new(&repo_root, "");
        let (highlight_tx, highlight_rx) = mpsc::unbounded_channel();
        let (github_tx, github_rx) = mpsc::unbounded_channel();
        let (commit_tx, commit_rx) = mpsc::unbounded_channel();
        Self {
            viewer: DiffViewer::new(mode),
            repo_root,
            mode,
            base_branch: String::new(),
            merge_base_ref: String::new(),
            default_branch: String::new(),
            composing: ComposingState::new(),
            comment_store,
            copilot_state: CopilotState::new(),
            username: None,
            diff_tx,
            highlight_tx,
            highlight_rx,
            github_tx,
            github_rx,
            github: None,
            owner: String::new(),
            repo_name: String::new(),
            colors: DiffColors::default(),
            picker: None,
            picker_kind: String::new(),
            pr: None,
            pr_loaded: false,
            flash: super::flash::FlashState::new(),
            commit_overlay: None,
            commit_tx,
            commit_rx,
        }
    }

    /// Spawn a background task to load the diff. Results arrive via `diff_tx`.
    pub fn load_diff(&self) {
        let repo_root = self.repo_root.clone();
        let mode = self.mode;
        let merge_base_ref = self.merge_base_ref.clone();
        let tx = self.diff_tx.clone();
        tokio::spawn(async move {
            let raw = match diff::diff(&repo_root, mode, &merge_base_ref).await {
                Ok(d) => d,
                Err(e) => {
                    tracing::error!("Failed to load diff: {e}");
                    return;
                }
            };
            let files = diff::parse_diff_to_files(&raw);
            let _ = tx.send(DiffLoaded::Full { files });
        });
    }

    /// Spawn a background task to reload the diff (preserves position).
    pub fn reload_diff(&self) {
        self.reload_diff_with_branch(None);
    }

    /// Spawn a background reload, optionally updating the base branch.
    pub fn reload_diff_with_branch(&self, new_branch: Option<String>) {
        let repo_root = self.repo_root.clone();
        let mode = self.mode;
        let merge_base_ref = self.merge_base_ref.clone();
        let tx = self.diff_tx.clone();
        let branch = new_branch;
        tokio::spawn(async move {
            let raw = match diff::diff(&repo_root, mode, &merge_base_ref).await {
                Ok(d) => d,
                Err(e) => {
                    tracing::error!("Failed to reload diff: {e}");
                    return;
                }
            };
            let files = diff::parse_diff_to_files(&raw);
            let _ = tx.send(DiffLoaded::Reload { files, branch });
        });
    }

    /// Fetch the PR associated with the current branch in the background.
    pub fn fetch_pr(&self, github: &CachedClient, owner: &str, repo: &str) {
        let github = github.clone();
        let owner = owner.to_string();
        let repo = repo.to_string();
        let branch = self.base_branch.clone();
        let tx = self.github_tx.clone();
        tokio::spawn(async move {
            match github.pull_request_by_branch(&owner, &repo, &branch).await {
                Ok(Some(pr)) => {
                    tracing::info!("PR #{} detected for branch {branch}", pr.number);
                    let _ = tx.send(GithubEvent::PrDetected(pr));
                }
                Ok(None) => {
                    tracing::debug!("No PR found for branch {branch}");
                }
                Err(e) => {
                    tracing::warn!("Failed to fetch PR for branch {branch}: {e}");
                }
            }
        });
    }

    /// Fetch review comments for the detected PR in the background.
    pub fn fetch_review_comments(&self, github: &CachedClient, owner: &str, repo: &str) {
        let Some(pr) = &self.pr else { return };
        let number = pr.number;
        let github = github.clone();
        let owner = owner.to_string();
        let repo = repo.to_string();
        let tx = self.github_tx.clone();
        tokio::spawn(async move {
            match github.review_comments(&owner, &repo, number).await {
                Ok(comments) => {
                    tracing::info!("Fetched {} review comments for PR #{number}", comments.len());
                    let _ = tx.send(GithubEvent::ReviewComments(comments));
                }
                Err(e) => {
                    tracing::warn!("Failed to fetch review comments for PR #{number}: {e}");
                }
            }
        });
    }

    /// Handle a GitHub event arriving from a background fetch.
    pub async fn handle_github_event(&mut self, event: GithubEvent, github: &CachedClient, _owner: &str, _repo: &str) {
        match event {
            GithubEvent::PrDetected(pr) => {
                self.merge_base_ref = format!("origin/{}", pr.base.ref_name);
                self.owner = pr.repo_owner().to_string();
                self.repo_name = pr.repo_name().to_string();
                self.pr = Some(pr);
                self.pr_loaded = true;
                let o = self.owner.clone();
                let r = self.repo_name.clone();
                self.fetch_review_comments(github, &o, &r);
            }
            GithubEvent::ReviewComments(comments) => {
                self.comment_store.import_gh(&comments);
                if let Some(f) = self.viewer.file_list.files.get(self.viewer.file_list.current_file_idx) {
                    let filename = f.filename.clone();
                    self.place_file_comments(&filename);
                }
            }
            GithubEvent::Error(msg) => {
                self.flash.error(msg);
            }
        }
    }

    /// Apply the result of a background diff load. Called from the event loop.
    pub async fn apply_diff_loaded(&mut self, result: DiffLoaded) {
        let is_reload = matches!(result, DiffLoaded::Reload { .. });
        match result {
            DiffLoaded::Full { files } => {
                self.viewer.set_files(files);
                self.viewer.file_list.sync_cursor();
            }
            DiffLoaded::Reload { files, branch } => {
                if let Some(b) = branch {
                    self.base_branch = b;
                }
                self.viewer.update_files(files);
                self.viewer.file_list.sync_cursor();
            }
        }
        self.viewer.file_list.loaded = true;
        self.refresh_current_file(is_reload).await;
    }

    pub(crate) async fn refresh_current_file(&mut self, preserve_panel: bool) {
        if self.viewer.file_list.files.is_empty() {
            self.viewer.set_diff_lines(Vec::new(), "");
            return;
        }

        let idx = self.viewer.file_list.current_file_idx.min(self.viewer.file_list.files.len() - 1);
        let filename = self.viewer.file_list.files[idx].filename.clone();

        // Close the comment panel when switching files, but keep it open
        // on reloads if it's showing a thread for the current file
        if self.viewer.panel.visible && !preserve_panel
            && self.viewer.panel.file_path != filename {
                self.viewer.panel.close();
                self.viewer.panel_focused = false;
            }

        let patch = self.viewer.file_list.files[idx].patch.clone();
        let diff_lines = parse_patch_to_diff_lines(&patch);

        // On reload, try to apply cached highlights immediately to avoid
        // a flash of unstyled content
        self.viewer.set_diff_lines(diff_lines, &filename);
        self.viewer.highlights.apply_cached(&mut self.viewer.render_list, &filename);

        self.place_file_comments(&filename);

        // Highlight current file; prefetch chain continues in apply_highlight_result
        self.spawn_highlight(&filename);
    }

    fn spawn_highlight(&self, filename: &str) {
        let repo_root = self.repo_root.clone();
        let fname = filename.to_string();
        let highlighter = self.viewer.highlights.highlighter.clone();
        let tx = self.highlight_tx.clone();

        tokio::spawn(async move {
            let new_content = crate::git::file_content(&repo_root, &fname)
                .await
                .unwrap_or_default();
            let old_content = crate::git::file_content_at_ref(&repo_root, &fname, "HEAD")
                .await
                .unwrap_or_default();

            let hl_new = if !new_content.is_empty() {
                highlighter.highlight_file(&new_content, &fname)
            } else {
                Vec::new()
            };
            let hl_old = if !old_content.is_empty() {
                highlighter.highlight_file(&old_content, &fname)
            } else {
                Vec::new()
            };

            let _ = tx.send(HighlightResult {
                filename: fname,
                hl_new,
                hl_old,
            });
        });
    }

    /// Apply background highlight results. Always cache; apply to render list only if file matches.
    /// Then continue prefetching the next uncached file.
    pub fn apply_highlight_result(&mut self, result: HighlightResult) {
        let current = self.current_filename();
        if current == result.filename {
            self.viewer.apply_highlights(result.hl_new, result.hl_old, &result.filename);
        } else {
            self.viewer.highlights.cache_only(
                result.hl_new,
                result.hl_old,
                &result.filename,
            );
        }

        // Continue prefetching: find next uncached file fanning out from cursor
        self.prefetch_next_uncached();
    }

    /// Spawn a highlight task for the nearest uncached file, fanning out from current position.
    fn prefetch_next_uncached(&self) {
        let total = self.viewer.file_list.files.len();
        if total == 0 {
            return;
        }
        let center = self.viewer.file_list.current_file_idx.min(total - 1);

        for offset in 1..total {
            for candidate in [center.wrapping_add(offset), center.wrapping_sub(offset)] {
                if candidate < total {
                    let fname = &self.viewer.file_list.files[candidate].filename;
                    if !self.viewer.highlights.is_cached(fname) {
                        self.spawn_highlight(fname);
                        return;
                    }
                }
            }
        }
    }

    /// Insert comment thread markers into the render list for the given file.
    pub(crate) fn place_file_comments(&mut self, filename: &str) {
        use crate::ui::diff_viewer::render_list::CommentPosition;

        let roots: Vec<CommentPosition> = self
            .comment_store
            .root_threads_for_file(filename)
            .into_iter()
            .map(|c| {
                let count = self.comment_store.thread_comments(&c.id).len();
                CommentPosition {
                    comment_id: c.id.clone(),
                    side: c.side.clone(),
                    line: c.line,
                    count,
                }
            })
            .collect();

        let pending: Vec<CommentPosition> = self
            .copilot_state
            .pending_for_file(filename)
            .into_iter()
            .map(|(id, p)| CommentPosition {
                comment_id: id.to_string(),
                side: p.side.clone(),
                line: p.line,
                count: 1,
            })
            .collect();

        self.viewer.render_list.place_comments(&roots, &pending);
    }

    pub fn resize(&mut self, width: u16, height: u16) {
        self.viewer.resize(width, height);
    }

    pub fn pane_at_column(&self, col: u16) -> Pane {
        let tree_w = TREE_WIDTH.min(self.viewer.width / 3);
        if col < tree_w {
            return Pane::Tree;
        }
        if self.viewer.panel.visible {
            let panel_start = self.viewer.width.saturating_sub(self.viewer.panel.width);
            if col >= panel_start {
                return Pane::Panel;
            }
        }
        Pane::Diff
    }

    pub fn move_tree_cursor_up(&mut self) {
        keybinds::move_tree_cursor(self, -1);
    }

    pub fn move_tree_cursor_down(&mut self) {
        keybinds::move_tree_cursor(self, 1);
    }

    pub async fn handle_key(&mut self, key: KeyEvent, repo_root: &str, agent: &Arc<dyn AgentRunner>) -> Option<String> {
        keybinds::handle_key(self, key, repo_root, agent).await
    }

    pub async fn next_file(&mut self) {
        if !self.viewer.file_list.files.is_empty() {
            self.viewer.file_list.current_file_idx = (self.viewer.file_list.current_file_idx + 1) % self.viewer.file_list.files.len();
            self.viewer.scroll.cursor = 0;
            self.viewer.scroll.offset = 0;
            self.viewer.file_list.sync_cursor();
            self.refresh_current_file(false).await;
        }
    }

    pub async fn prev_file(&mut self) {
        if !self.viewer.file_list.files.is_empty() {
            if self.viewer.file_list.current_file_idx == 0 {
                self.viewer.file_list.current_file_idx = self.viewer.file_list.files.len() - 1;
            } else {
                self.viewer.file_list.current_file_idx -= 1;
            }
            self.viewer.scroll.cursor = 0;
            self.viewer.scroll.offset = 0;
            self.viewer.file_list.sync_cursor();
            self.refresh_current_file(false).await;
        }
    }

    pub fn cycle_mode(&mut self, is_default_branch: bool) {
        self.mode = match self.mode {
            DiffMode::Working => DiffMode::Staged,
            DiffMode::Staged => {
                if is_default_branch {
                    DiffMode::Working
                } else {
                    DiffMode::Branch
                }
            }
            DiffMode::Branch => DiffMode::Working,
        };
        self.viewer.mode = self.mode;
    }

    pub fn handle_agent_event(&mut self, event: AgentEvent) {
        // Route commit-gen events to the commit overlay
        if event.comment_id.starts_with(COMMIT_GEN_PREFIX) {
            self.handle_commit_agent_event(event);
            return;
        }

        tracing::info!("Agent event: {:?} for comment {}", event.kind, event.comment_id);
        let comment_id = event.comment_id.clone();
        if let Some(reply) = self.copilot_state.handle_event(event) {
            tracing::info!("Copilot reply complete for {}", comment_id);
            self.handle_completed_reply(reply);
            self.refresh_panel_for_thread(&comment_id);
            // Re-place comments to update thread counts and remove pending markers
            let filename = self.current_filename();
            self.place_file_comments(&filename);
        } else {
            self.refresh_panel_streaming(&comment_id);
        }
    }

    fn handle_commit_agent_event(&mut self, event: AgentEvent) {
        let overlay = match &mut self.commit_overlay {
            Some(o) => o,
            None => return,
        };

        match event.kind {
            EventKind::Delta => {
                if let EventPayload::Delta(d) = &event.payload {
                    overlay.append_token(&d.text);
                }
            }
            EventKind::Done => {
                overlay.finish_generation();
            }
            EventKind::Error => {
                if let EventPayload::Error(e) = &event.payload {
                    self.flash.error(format!("AI generation failed: {}", e.message));
                }
                overlay.generation_error();
            }
            _ => {}
        }
    }

    pub fn handle_commit_result(&mut self, event: CommitEvent) {
        self.commit_overlay = None;
        match event {
            CommitEvent::Success(msg) => {
                self.flash.success(msg);
                self.reload_diff();
            }
            CommitEvent::Error(msg) => {
                self.flash.error(msg);
            }
        }
    }

    fn handle_completed_reply(&mut self, reply: CompletedReply) {
        use crate::ui::copilot_state::ContentBlock as CopilotBlock;

        // Convert copilot_state blocks to stored comment blocks
        let blocks: Vec<ContentBlock> = reply
            .blocks
            .iter()
            .map(|b| match b {
                CopilotBlock::Text(text) => ContentBlock::Text {
                    content: text.clone(),
                },
                CopilotBlock::ToolGroup(g) => ContentBlock::ToolGroup {
                    label: g.label.clone(),
                    tools: g
                        .tools
                        .iter()
                        .map(|t| ToolEntry {
                            name: t.name.clone(),
                            args: t.args_summary.clone(),
                            result: None,
                            done: t.status != crate::ui::copilot_state::ToolStatus::Running,
                        })
                        .collect(),
                },
            })
            .collect();

        let comment = LocalComment {
            id: format!("copilot-{}", uuid_v4()),
            body: reply.body,
            author: CommentAuthor::Copilot,
            in_reply_to_id: Some(reply.comment_id),
            path: reply.path,
            line: reply.line,
            side: reply.side,
            resolved: false,
            created_at: chrono_now(),
            blocks,
        };
        let _ = self.comment_store.add(comment);
    }

    fn refresh_panel_streaming(&mut self, comment_id: &str) {
        // Don't reopen the panel if user closed it
        if !self.viewer.panel.visible {
            return;
        }

        if let Some(display_blocks) = self.copilot_state.pending_display_blocks(comment_id) {
            let root_id = self
                .comment_store
                .find_thread_root(comment_id)
                .map(|c| c.id.clone())
                .unwrap_or_else(|| comment_id.to_string());

            // Only refresh if showing this thread
            if self.viewer.panel.thread_key.as_deref() != Some(&root_id)
                && self.viewer.panel.thread_key.as_deref() != Some(comment_id)
            {
                return;
            }

            let mut comments = self.build_panel_comments(&root_id);

            comments.push(PanelComment {
                author: "Copilot".to_string(),
                is_copilot: true,
                blocks: display_blocks,
                is_pending: true,
                created_at: chrono_now(),
            });

            let file_path = self.current_filename();
            let ctx = self.viewer.panel.diff_context.clone();
            self.viewer.panel.open_thread(root_id, comments, file_path, ctx);

            // Auto-scroll to bottom so streaming content is visible
            self.viewer.panel.goto_bottom();
        }
    }

    fn refresh_panel_for_thread(&mut self, comment_id: &str) {
        // Only refresh if the panel is already visible and showing this thread
        if !self.viewer.panel.visible {
            return;
        }

        let root_id = self
            .comment_store
            .find_thread_root(comment_id)
            .map(|c| c.id.clone())
            .unwrap_or_else(|| comment_id.to_string());

        // Only refresh if the open thread matches
        if self.viewer.panel.thread_key.as_deref() != Some(&root_id) {
            return;
        }

        let comments = self.build_panel_comments(&root_id);
        let file_path = self.current_filename();
        let ctx = self.viewer.panel.diff_context.clone();
        self.viewer.panel.open_thread(root_id, comments, file_path, ctx);
    }

    /// Open a thread in the panel (unconditionally — used for explicit user actions).
    pub fn open_panel_for_thread(&mut self, comment_id: &str) {
        let root_id = self
            .comment_store
            .find_thread_root(comment_id)
            .map(|c| c.id.clone())
            .unwrap_or_else(|| comment_id.to_string());

        let resolved = self
            .comment_store
            .find_thread_root(comment_id)
            .map(|c| c.resolved)
            .unwrap_or(false);

        let comments = self.build_panel_comments(&root_id);
        let file_path = self.current_filename();
        let ctx = self.viewer.panel_diff_context(&self.colors);
        self.viewer.panel.open_thread(root_id, comments, file_path, ctx);
        self.viewer.panel.resolved = resolved;
    }

    fn build_panel_comments(&self, root_id: &str) -> Vec<PanelComment> {
        use crate::ui::copilot_state::ContentBlock as PanelBlock;

        let you_name = self.username.as_deref().unwrap_or("You");
        self.comment_store
            .thread_comments(root_id)
            .into_iter()
            .map(|c| {
                // Convert stored ContentBlocks to panel ContentBlocks
                let blocks: Vec<PanelBlock> = if c.blocks.is_empty() {
                    if !c.body.is_empty() {
                        vec![PanelBlock::Text(c.body.clone())]
                    } else {
                        Vec::new()
                    }
                } else {
                    c.blocks
                        .iter()
                        .map(|b| match b {
                            ContentBlock::Text { content } => PanelBlock::Text(content.clone()),
                            ContentBlock::ToolGroup { label, tools } => {
                                PanelBlock::ToolGroup(ToolGroup {
                                    label: label.clone(),
                                    tools: tools
                                        .iter()
                                        .map(|t| ToolCall {
                                            name: t.name.clone(),
                                            args_summary: t.args.clone(),
                                            status: if t.done {
                                                ToolStatus::Done
                                            } else {
                                                ToolStatus::Running
                                            },
                                        })
                                        .collect(),
                                })
                            }
                        })
                        .collect()
                };

                PanelComment {
                    author: match &c.author {
                        CommentAuthor::You => you_name.to_string(),
                        CommentAuthor::Copilot => "Copilot".to_string(),
                        CommentAuthor::GitHub(name) => name.clone(),
                    },
                    is_copilot: matches!(c.author, CommentAuthor::Copilot),
                    blocks,
                    is_pending: false,
                    created_at: c.created_at.clone(),
                }
            })
            .collect()
    }

    pub(crate) fn current_filename(&self) -> String {
        self.viewer
            .file_list
            .files
            .get(self.viewer.file_list.current_file_idx)
            .map(|f| f.filename.clone())
            .unwrap_or_default()
    }

    pub fn tick(&mut self) {
        if let Some(overlay) = &mut self.commit_overlay {
            overlay.tick();
        }
        if self.copilot_state.has_pending() {
            self.copilot_state.advance_dots();
            self.viewer.dots_frame = self.viewer.dots_frame.wrapping_add(1);

            // Refresh panel if it's showing a thread with any pending copilot reply
            if self.viewer.panel.visible {
                if let Some(thread_key) = self.viewer.panel.thread_key.clone() {
                    // Find any pending comment that belongs to this thread
                    if let Some(pending_id) = self.find_pending_for_thread(&thread_key) {
                        self.refresh_panel_streaming(&pending_id);
                    }
                }
            }
        }
    }

    /// Find a pending copilot comment ID whose thread root matches `thread_key`.
    fn find_pending_for_thread(&self, thread_key: &str) -> Option<String> {
        for pending_id in self.copilot_state.pending_ids() {
            // Check if this pending ID itself IS the thread key
            if pending_id == thread_key {
                return Some(pending_id.to_string());
            }
            // Check if the thread root of this pending comment matches
            if let Some(root) = self.comment_store.find_thread_root(pending_id) {
                if root.id == thread_key {
                    return Some(pending_id.to_string());
                }
            }
        }
        None
    }

    pub async fn submit_comment(&mut self, agent: &Arc<dyn AgentRunner>) {
        let body = self.composing.input.trim().to_string();
        if body.is_empty() {
            self.composing.cancel();
            return;
        }

        let path = self.composing.path.clone();
        let line = self.composing.line;
        let side = self.composing.side.clone();
        let reply_to = self.composing.reply_to.clone();
        let reply_mode = self.composing.reply_mode;
        self.composing.take_input();

        // If replying to a GitHub thread in GitHub mode, post via API
        let gh_comment_id = if reply_mode == panel::ReplyMode::GitHub {
            if let Some(ref rt) = reply_to {
                if let Some(root) = self.comment_store.find_thread_root(rt) {
                    crate::review::comments::parse_gh_id(&root.id)
                } else {
                    None
                }
            } else {
                None
            }
        } else {
            None
        };

        if let (Some(gh_root_id), Some(pr), Some(github)) = (gh_comment_id, &self.pr, &self.github) {
            let github = github.clone();
            let owner = self.owner.clone();
            let repo = self.repo_name.clone();
            let pr_number = pr.number;
            let body_clone = body.clone();
            let tx = self.github_tx.clone();
            tokio::spawn(async move {
                match github.reply_to_comment(&owner, &repo, pr_number, gh_root_id, &body_clone).await {
                    Ok(_reply) => {
                        tracing::info!("Posted reply to GitHub comment gh-{gh_root_id}");
                        match github.review_comments(&owner, &repo, pr_number).await {
                            Ok(comments) => {
                                let _ = tx.send(GithubEvent::ReviewComments(comments));
                            }
                            Err(e) => tracing::warn!("Failed to refresh comments after reply: {e}"),
                        }
                    }
                    Err(e) => {
                        tracing::error!("Failed to post reply to GitHub: {e}");
                        let _ = tx.send(GithubEvent::Error(format!("Reply failed: {e}")));
                    }
                }
            });
        } else if reply_to.is_none() {
            // New top-level comment: if a PR is open, post as a review comment
            if let (Some(pr), Some(github)) = (&self.pr, &self.github) {
                let github = github.clone();
                let owner = self.owner.clone();
                let repo = self.repo_name.clone();
                let pr_number = pr.number;
                let commit_id = pr.head.sha.clone();
                let body_clone = body.clone();
                let path_clone = path.clone();
                let side_clone = side.clone();
                let tx = self.github_tx.clone();
                tokio::spawn(async move {
                    match github.create_review_comment(
                        &owner, &repo, pr_number, &body_clone, &commit_id,
                        &path_clone, line, &side_clone,
                    ).await {
                        Ok(_comment) => {
                            tracing::info!("Posted new review comment on {path_clone}:{line}");
                            match github.review_comments(&owner, &repo, pr_number).await {
                                Ok(comments) => {
                                    let _ = tx.send(GithubEvent::ReviewComments(comments));
                                }
                                Err(e) => tracing::warn!("Failed to refresh comments after create: {e}"),
                            }
                        }
                        Err(e) => {
                            tracing::error!("Failed to create review comment on GitHub: {e}");
                            let _ = tx.send(GithubEvent::Error(format!("Comment failed: {e}")));
                        }
                    }
                });
            }
        }

        // For GitHub-mode replies to GH threads or new GH comments, we don't
        // create a local comment. The success path refreshes from the API.
        // Only create local comments for Copilot mode or non-GH contexts.
        let posted_to_gh = gh_comment_id.is_some()
            || (reply_to.is_none() && reply_mode == panel::ReplyMode::GitHub && self.pr.is_some() && self.github.is_some());

        if posted_to_gh {
            // Nothing to do locally — the spawned task will refresh on success
            // or show a flash error on failure.
            return;
        }

        let comment_id = format!("local-{}", uuid_v4());
        tracing::info!("Submitting comment {comment_id} on {path}:{line} ({side})");

        let comment = LocalComment {
            id: comment_id.clone(),
            body: body.clone(),
            author: CommentAuthor::You,
            in_reply_to_id: reply_to,
            path: path.clone(),
            line,
            side: side.clone(),
            resolved: false,
            created_at: chrono_now(),
            blocks: vec![ContentBlock::Text {
                content: body.clone(),
            }],
        };
        let _ = self.comment_store.add(comment);

        // Re-place comment threads in the render list
        self.place_file_comments(&path);

        // Resolve the root thread for panel display
        let root_id = self
            .comment_store
            .find_thread_root(&comment_id)
            .map(|c| c.id.clone())
            .unwrap_or_else(|| comment_id.clone());

        if reply_mode == panel::ReplyMode::Copilot {
            let diff_hunk = self.build_diff_context(self.viewer.scroll.cursor);
            let prompt = format!(
                "The user left a comment on `{}` line {} ({}):\n\n{}\n\nDiff context:\n```\n{}\n```\n\nPlease provide a helpful response.",
                path, line, side, body, diff_hunk
            );

            self.copilot_state
                .set_pending(comment_id.clone(), path.clone(), line, side);

            // Show thread with pending indicator immediately
            let mut comments = self.build_panel_comments(&root_id);
            comments.push(PanelComment {
                author: "Copilot".to_string(),
                is_copilot: true,
                blocks: vec![crate::ui::copilot_state::ContentBlock::Text("Thinking...".to_string())],
                is_pending: true,
                created_at: chrono_now(),
            });
            let file_path = self.current_filename();
            let ctx = self.viewer.panel_diff_context(&self.colors);
            self.viewer.panel.open_thread(root_id.clone(), comments, file_path, ctx);
            self.viewer.panel_focused = true;
            self.viewer.file_list.focused = false;

            tracing::info!("Sending prompt to copilot agent for {comment_id}");
            if let Err(e) = agent.send(&comment_id, &prompt).await {
                tracing::error!("Failed to send to copilot: {e}");
                let mut comments = self.build_panel_comments(&root_id);
                comments.push(PanelComment {
                    author: "Copilot".to_string(),
                    is_copilot: true,
                    blocks: vec![crate::ui::copilot_state::ContentBlock::Text(format!("⚠ {e}"))],
                    is_pending: false,
                    created_at: chrono_now(),
                });
                let file_path = self.current_filename();
                let ctx = self.viewer.panel.diff_context.clone();
                self.viewer.panel.open_thread(root_id, comments, file_path, ctx);
            }
        } else {
            // GitHub reply mode: just show the updated thread without Copilot
            let comments = self.build_panel_comments(&root_id);
            let file_path = self.current_filename();
            let ctx = self.viewer.panel_diff_context(&self.colors);
            self.viewer.panel.open_thread(root_id, comments, file_path, ctx);
            self.viewer.panel_focused = true;
            self.viewer.file_list.focused = false;
        }
    }

    fn build_diff_context(&self, cursor: usize) -> String {
        let total = self.viewer.render_list.len();
        let start = cursor.saturating_sub(5);
        let end = (cursor + 6).min(total);
        let mut lines = Vec::new();
        for i in start..end {
            if let Some(dl) = self.viewer.render_list.get_diff_line(i) {
                let prefix = match dl.line_type {
                    LineType::Add => "+",
                    LineType::Delete => "-",
                    LineType::Context => " ",
                    LineType::HunkHeader => "@@",
                };
                let marker = if i == cursor { " ← comment here" } else { "" };
                lines.push(format!("{prefix}{}{marker}", dl.content));
            }
        }
        lines.join("\n")
    }

    pub fn render(&mut self, frame: &mut Frame, area: Rect, colors: &DiffColors) {
        let file = self.viewer.file_list.files.get(self.viewer.file_list.current_file_idx);
        let copilot_working_files = self.copilot_state.pending_file_set();
        let info = LayoutInfo {
            mode: self.mode,
            branch_name: self.base_branch.clone(),
            file_count: self.viewer.file_list.files.len(),
            current_file_idx: self.viewer.file_list.current_file_idx,
            current_filename: file.map(|f| f.filename.clone()).unwrap_or_default(),
            additions: file.map(|f| f.additions).unwrap_or(0),
            deletions: file.map(|f| f.deletions).unwrap_or(0),
            help_line: self.build_help_line(),
            comment_counts: self.comment_store.thread_counts_by_file(),
            copilot_working_files,
            pr_number: self.pr.as_ref().map(|p| p.number),
            pr_owner: self.owner.clone(),
            pr_repo: self.repo_name.clone(),
        };

        // Show composing input in the panel reply area
        if self.composing.is_active() {
            if !self.viewer.panel.visible {
                let file_path = self.current_filename();
                let ctx = self.viewer.panel_diff_context(colors);
                self.viewer.panel.open_thread(
                    "composing".to_string(),
                    Vec::new(),
                    file_path,
                    ctx,
                );
            }
            self.viewer.panel.set_reply_view(
                self.composing.input.clone(),
                self.composing.reply_mode,
            );
        } else {
            self.viewer.panel.clear_reply_view();
        }

        self.viewer.render_layout(frame, area, colors, &info);

        // Picker popup overlay (rendered on top of everything)
        if let Some(picker) = &self.picker {
            use super::popup::{Popup, PopupPosition, picker_popup_lines};
            let max_visible = 10usize;
            let start = if picker.cursor >= max_visible {
                picker.cursor - max_visible + 1
            } else {
                0
            };
            let end = (start + max_visible).min(picker.filtered.len());

            let items: Vec<(String, String, Vec<usize>, bool)> = (start..end)
                .map(|i| {
                    let fi = &picker.filtered[i];
                    let item = &picker.items[fi.index];
                    (
                        item.label.clone(),
                        item.description.clone(),
                        fi.match_positions.clone(),
                        i == picker.cursor,
                    )
                })
                .collect();

            let lines = picker_popup_lines(
                &picker.query,
                &items,
                picker.items.len(),
                picker.filtered.len(),
            );

            Popup::new(&picker.title)
                .lines(lines)
                .position(PopupPosition::TopThird)
                .border_color(colors.border_fg)
                .render(frame, area);
        }

        // Commit overlay (rendered on top of picker and diff)
        if let Some(overlay) = &self.commit_overlay {
            overlay.render(frame, area, colors.border_fg);
        }

        // Flash notifications (top-right, rendered last so they're on top)
        self.flash.gc();
        self.flash.render(frame, area);
    }

    fn build_help_line(&self) -> Vec<(String, String)> {
        let mut hints = Vec::new();
        let h = |k: &str, d: &str| (k.to_string(), d.to_string());

        if self.composing.is_active() {
            hints.push(h("esc", "cancel"));
            hints.push(h("shift+tab", "switch mode"));
            hints.push(h("enter", "submit"));
            return hints;
        }

        if self.viewer.panel.visible {
            hints.push(h("esc", "close panel"));
            hints.push(h("r", "reply"));
            hints.push(h("x", "resolve"));
            hints.push(h("q", "close panel"));
            return hints;
        }

        if self.viewer.file_list.focused {
            hints.push(h("j/k", "navigate"));
            hints.push(h("l", "focus diff"));
            hints.push(h("^j/^k", "next/prev file"));
            match self.mode {
                DiffMode::Working => hints.push(h("s", "stage file")),
                DiffMode::Staged => hints.push(h("u", "unstage file")),
                _ => {}
            }
            hints.push(h("↵", "open file"));
        } else if !self.viewer.file_list.files.is_empty() {
            hints.push(h("j/k", "navigate"));
            hints.push(h("^j/^k", "next/prev file"));
            hints.push(h("f", "focus tree"));
            hints.push(h("↵", "comment"));
            hints.push(h("c", "ask copilot"));
            if self.viewer.render_list.badge_at(self.viewer.scroll.cursor).is_some() {
                hints.push(h("x", "resolve"));
            }
            match self.mode {
                DiffMode::Working => {
                    hints.push(h("s", "stage line"));
                    hints.push(h("S", "stage hunk"));
                }
                DiffMode::Staged => {
                    hints.push(h("u", "unstage line"));
                    hints.push(h("U", "unstage hunk"));
                }
                _ => {}
            }
            hints.push(h("m", "mode"));
            hints.push(h("C", "commit/push"));
        } else {
            hints.push(h("j/k", "navigate tree"));
            hints.push(h("↵", "open file"));
            hints.push(h("^j/^k", "next/prev file"));
        }

        hints
    }
}

fn parse_patch_to_diff_lines(patch: &str) -> Vec<DiffLineData> {
    let mut lines = Vec::new();
    let mut old_num: i32 = 0;
    let mut new_num: i32 = 0;

    for line in patch.lines() {
        if line.starts_with("@@") {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 3 {
                old_num = parts[1]
                    .trim_start_matches('-')
                    .split(',')
                    .next()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0);
                new_num = parts[2]
                    .trim_start_matches('+')
                    .split(',')
                    .next()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0);
            }
            lines.push(DiffLineData {
                line_type: LineType::HunkHeader,
                content: line.to_string(),
                old_line_no: None,
                new_line_no: None,
                highlighted: Vec::new(),
                badge: None,
            });
        } else if let Some(rest) = line.strip_prefix('+') {
            lines.push(DiffLineData {
                line_type: LineType::Add,
                content: rest.to_string(),
                old_line_no: None,
                new_line_no: Some(new_num),
                highlighted: Vec::new(),
                badge: None,
            });
            new_num += 1;
        } else if let Some(rest) = line.strip_prefix('-') {
            lines.push(DiffLineData {
                line_type: LineType::Delete,
                content: rest.to_string(),
                old_line_no: Some(old_num),
                new_line_no: None,
                highlighted: Vec::new(),
                badge: None,
            });
            old_num += 1;
        } else if let Some(rest) = line.strip_prefix(' ') {
            lines.push(DiffLineData {
                line_type: LineType::Context,
                content: rest.to_string(),
                old_line_no: Some(old_num),
                new_line_no: Some(new_num),
                highlighted: Vec::new(),
                badge: None,
            });
            old_num += 1;
            new_num += 1;
        } else if !line.starts_with('\\') {
            lines.push(DiffLineData {
                line_type: LineType::Context,
                content: line.to_string(),
                old_line_no: Some(old_num),
                new_line_no: Some(new_num),
                highlighted: Vec::new(),
                badge: None,
            });
            old_num += 1;
            new_num += 1;
        }
    }

    lines
}

fn uuid_v4() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let t = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    format!("{t:x}")
}

fn chrono_now() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
}
