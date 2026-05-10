pub mod keybinds;

use std::sync::Arc;

use crossterm::event::KeyEvent;
use ratatui::layout::Rect;
use ratatui::Frame;

use crate::agent::AgentRunner;
use crate::agent::types::AgentEvent;
use crate::git::diff::{self, DiffMode};
use crate::review::comments::{CommentAuthor, CommentStore, ContentBlock, LocalComment};
use super::composing_state::ComposingState;
use super::copilot_state::{CopilotState, CompletedReply};
use super::diff_viewer::{DiffLineData, DiffViewer, LayoutInfo, LineType, TREE_WIDTH};
use super::diff_viewer::panel::{self, PanelComment};
use super::styles::DiffColors;

pub enum Pane {
    Tree,
    Diff,
    Panel,
}

pub struct LocalDiff {
    pub viewer: DiffViewer,
    pub repo_root: String,
    pub mode: DiffMode,
    pub base_branch: String,
    pub composing: ComposingState,
    pub comment_store: CommentStore,
    pub copilot_state: CopilotState,
    pub username: Option<String>,
}

impl LocalDiff {
    pub fn new(repo_root: String, mode: DiffMode) -> Self {
        let comment_store = CommentStore::new(&repo_root, "");
        Self {
            viewer: DiffViewer::new(mode),
            repo_root,
            mode,
            base_branch: String::new(),
            composing: ComposingState::new(),
            comment_store,
            copilot_state: CopilotState::new(),
            username: None,
        }
    }

    pub async fn load_diff(&mut self) {
        let raw = match diff::diff(&self.repo_root, self.mode, &self.base_branch).await {
            Ok(d) => d,
            Err(e) => {
                tracing::error!("Failed to load diff: {e}");
                return;
            }
        };

        let files = diff::parse_diff_to_files(&raw);
        self.viewer.set_files(files);
        self.sync_tree_cursor();
        self.refresh_current_file().await;
    }

    pub(crate) async fn refresh_current_file(&mut self) {
        if self.viewer.files.is_empty() {
            self.viewer.set_diff_lines(Vec::new(), "", "", "");
            return;
        }

        let idx = self.viewer.current_file_idx.min(self.viewer.files.len() - 1);
        let filename = self.viewer.files[idx].filename.clone();
        let patch = self.viewer.files[idx].patch.clone();
        let diff_lines = parse_patch_to_diff_lines(&patch);

        // Read full file content for proper full-file syntax highlighting
        let new_content = crate::git::file_content(&self.repo_root, &filename)
            .await
            .unwrap_or_default();

        let old_content = crate::git::file_content_at_ref(&self.repo_root, &filename, "HEAD")
            .await
            .unwrap_or_default();

        self.viewer
            .set_diff_lines(diff_lines, &filename, &new_content, &old_content);
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

    pub async fn handle_key(&mut self, key: KeyEvent, repo_root: &str, agent: &Arc<dyn AgentRunner>) {
        keybinds::handle_key(self, key, repo_root, agent).await;
    }

    pub async fn next_file(&mut self) {
        if !self.viewer.files.is_empty() {
            self.viewer.current_file_idx = (self.viewer.current_file_idx + 1) % self.viewer.files.len();
            self.viewer.diff_cursor = 0;
            self.viewer.viewport_offset = 0;
            self.sync_tree_cursor();
            self.refresh_current_file().await;
        }
    }

    pub async fn prev_file(&mut self) {
        if !self.viewer.files.is_empty() {
            if self.viewer.current_file_idx == 0 {
                self.viewer.current_file_idx = self.viewer.files.len() - 1;
            } else {
                self.viewer.current_file_idx -= 1;
            }
            self.viewer.diff_cursor = 0;
            self.viewer.viewport_offset = 0;
            self.sync_tree_cursor();
            self.refresh_current_file().await;
        }
    }

    fn sync_tree_cursor(&mut self) {
        for (i, entry) in self.viewer.tree_entries.iter().enumerate() {
            if !entry.is_dir && entry.file_index as usize == self.viewer.current_file_idx {
                self.viewer.tree_cursor = i;
                break;
            }
        }
    }

    pub fn cycle_mode(&mut self) {
        self.mode = match self.mode {
            DiffMode::Working => DiffMode::Staged,
            DiffMode::Staged => DiffMode::Branch,
            DiffMode::Branch => DiffMode::Working,
        };
        self.viewer.mode = self.mode;
    }

    pub fn handle_agent_event(&mut self, event: AgentEvent) {
        tracing::info!("Agent event: {:?} for comment {}", event.kind, event.comment_id);
        let comment_id = event.comment_id.clone();
        if let Some(reply) = self.copilot_state.handle_event(event) {
            tracing::info!("Copilot reply complete for {}", comment_id);
            self.handle_completed_reply(reply);
            self.refresh_panel_for_thread(&comment_id);
        } else {
            self.refresh_panel_streaming(&comment_id);
        }
    }

    fn handle_completed_reply(&mut self, reply: CompletedReply) {
        let comment = LocalComment {
            id: format!("copilot-{}", uuid_v4()),
            body: reply.body.clone(),
            author: CommentAuthor::Copilot,
            in_reply_to_id: Some(reply.comment_id),
            path: reply.path,
            line: reply.line,
            side: reply.side,
            resolved: false,
            created_at: chrono_now(),
            blocks: vec![ContentBlock::Text {
                content: reply.body,
            }],
        };
        let _ = self.comment_store.add(comment);
    }

    fn refresh_panel_streaming(&mut self, comment_id: &str) {
        if let Some(display_text) = self.copilot_state.pending_display_text(comment_id) {
            let root_id = self
                .comment_store
                .find_thread_root(comment_id)
                .map(|c| c.id.clone())
                .unwrap_or_else(|| comment_id.to_string());

            let mut comments = self.build_panel_comments(&root_id);

            let tool_groups = self
                .copilot_state
                .tool_groups(comment_id)
                .map(|g| g.to_vec())
                .unwrap_or_default();
            comments.push(PanelComment {
                author: "Copilot".to_string(),
                is_copilot: true,
                body: display_text,
                is_pending: true,
                tool_groups,
            });

            let file_path = self.current_filename();
            self.viewer.panel.open_thread(root_id, comments, file_path);

            // Auto-scroll to bottom so streaming content is visible
            let total = self.viewer.panel.content_line_count();
            let vp = self.viewer.viewport_height() as usize;
            if total > vp {
                self.viewer.panel.scroll_offset = total.saturating_sub(vp);
            }
        }
    }

    fn refresh_panel_for_thread(&mut self, comment_id: &str) {
        let root_id = self
            .comment_store
            .find_thread_root(comment_id)
            .map(|c| c.id.clone())
            .unwrap_or_else(|| comment_id.to_string());

        let comments = self.build_panel_comments(&root_id);
        let file_path = self.current_filename();
        self.viewer.panel.open_thread(root_id, comments, file_path);
    }

    fn build_panel_comments(&self, root_id: &str) -> Vec<PanelComment> {
        let you_name = self.username.as_deref().unwrap_or("You");
        self.comment_store
            .thread_comments(root_id)
            .into_iter()
            .map(|c| PanelComment {
                author: match &c.author {
                    CommentAuthor::You => you_name.to_string(),
                    CommentAuthor::Copilot => "Copilot".to_string(),
                    CommentAuthor::GitHub(name) => name.clone(),
                },
                is_copilot: matches!(c.author, CommentAuthor::Copilot),
                body: c.body.clone(),
                is_pending: false,
                tool_groups: Vec::new(),
            })
            .collect()
    }

    fn current_filename(&self) -> String {
        self.viewer
            .files
            .get(self.viewer.current_file_idx)
            .map(|f| f.filename.clone())
            .unwrap_or_default()
    }

    pub fn tick(&mut self) {
        if self.copilot_state.has_pending() {
            self.copilot_state.advance_dots();

            // Refresh panel if it's showing a thread with pending copilot replies
            if self.viewer.panel.visible {
                if let Some(thread_key) = self.viewer.panel.thread_key.clone() {
                    if self.copilot_state.is_pending(&thread_key) {
                        self.refresh_panel_streaming(&thread_key);
                    }
                }
            }
        }
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
        self.composing.take_input();

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

        let diff_hunk = self.build_diff_context(self.viewer.diff_cursor);
        let prompt = format!(
            "The user left a comment on `{}` line {} ({}):\n\n{}\n\nDiff context:\n```\n{}\n```\n\nPlease provide a helpful response.",
            path, line, side, body, diff_hunk
        );

        self.copilot_state
            .set_pending(comment_id.clone(), path, line, side);

        // Show thread with pending indicator immediately
        let mut comments = self.build_panel_comments(&comment_id);
        comments.push(PanelComment {
            author: "Copilot".to_string(),
            is_copilot: true,
            body: "Thinking...".to_string(),
            is_pending: true,
            tool_groups: Vec::new(),
        });
        let file_path = self.current_filename();
        self.viewer.panel.open_thread(comment_id.clone(), comments, file_path);
        self.viewer.panel_focused = true;
        self.viewer.tree_focused = false;

        tracing::info!("Sending prompt to copilot agent for {comment_id}");
        if let Err(e) = agent.send(&comment_id, &prompt).await {
            tracing::error!("Failed to send to copilot: {e}");
            // Show error in panel
            let mut comments = self.build_panel_comments(&comment_id);
            comments.push(PanelComment {
                author: "Copilot".to_string(),
                is_copilot: true,
                body: format!("⚠ {e}"),
                is_pending: false,
                tool_groups: Vec::new(),
            });
            let file_path = self.current_filename();
            self.viewer.panel.open_thread(comment_id, comments, file_path);
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
        let file = self.viewer.files.get(self.viewer.current_file_idx);
        let info = LayoutInfo {
            mode: self.mode,
            branch_name: self.base_branch.clone(),
            file_count: self.viewer.files.len(),
            current_file_idx: self.viewer.current_file_idx,
            current_filename: file.map(|f| f.filename.clone()).unwrap_or_default(),
            additions: file.map(|f| f.additions).unwrap_or(0),
            deletions: file.map(|f| f.deletions).unwrap_or(0),
            help_line: self.build_help_line(),
        };

        // Show composing input in the panel reply area
        if self.composing.is_active() {
            if !self.viewer.panel.visible {
                let file_path = self.current_filename();
                self.viewer.panel.open_thread(
                    "composing".to_string(),
                    Vec::new(),
                    file_path,
                );
            }
            self.viewer.panel.set_reply_view(
                self.composing.input.clone(),
                panel::ReplyMode::Copilot,
            );
        } else {
            self.viewer.panel.clear_reply_view();
        }

        self.viewer.render_layout(frame, area, colors, &info);
    }

    fn build_help_line(&self) -> Vec<(String, String)> {
        let mut hints = Vec::new();
        let h = |k: &str, d: &str| (k.to_string(), d.to_string());

        if self.composing.is_active() {
            hints.push(h("esc", "cancel"));
            hints.push(h("enter", "submit"));
            return hints;
        }

        if self.viewer.panel.visible {
            hints.push(h("esc", "close panel"));
            hints.push(h("r", "reply"));
            hints.push(h("q", "close panel"));
            return hints;
        }

        if self.viewer.tree_focused {
            hints.push(h("j/k", "navigate"));
            hints.push(h("l", "focus diff"));
            hints.push(h("^j/^k", "next/prev file"));
            match self.mode {
                DiffMode::Working => hints.push(h("s", "stage file")),
                DiffMode::Staged => hints.push(h("u", "unstage file")),
                _ => {}
            }
            hints.push(h("↵", "open file"));
        } else if !self.viewer.files.is_empty() {
            hints.push(h("j/k", "navigate"));
            hints.push(h("^j/^k", "next/prev file"));
            hints.push(h("f", "focus tree"));
            hints.push(h("↵", "comment"));
            hints.push(h("c", "ask copilot"));
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
            });
        } else if let Some(rest) = line.strip_prefix('+') {
            lines.push(DiffLineData {
                line_type: LineType::Add,
                content: rest.to_string(),
                old_line_no: None,
                new_line_no: Some(new_num),
                highlighted: Vec::new(),
            });
            new_num += 1;
        } else if let Some(rest) = line.strip_prefix('-') {
            lines.push(DiffLineData {
                line_type: LineType::Delete,
                content: rest.to_string(),
                old_line_no: Some(old_num),
                new_line_no: None,
                highlighted: Vec::new(),
            });
            old_num += 1;
        } else if let Some(rest) = line.strip_prefix(' ') {
            lines.push(DiffLineData {
                line_type: LineType::Context,
                content: rest.to_string(),
                old_line_no: Some(old_num),
                new_line_no: Some(new_num),
                highlighted: Vec::new(),
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
    use std::time::{SystemTime, UNIX_EPOCH};
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    format!("{secs}")
}
