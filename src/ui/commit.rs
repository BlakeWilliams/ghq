use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};
use ratatui::layout::Rect;
use ratatui::style::{Color, Style};
use ratatui::text::{Line, Span};
use ratatui::Frame;
use unicode_width::UnicodeWidthStr;

use super::popup::{Popup, PopupPosition};

pub const COMMIT_GEN_PREFIX: &str = "commit-gen";

#[derive(Clone, Copy, PartialEq)]
pub enum CommitAction {
    Commit,
    CommitAndPush,
    Push,
    OpenPR,
    CommitAll,
    CommitAllAndPush,
}

impl CommitAction {
    pub fn label(&self) -> &'static str {
        match self {
            Self::Commit => "Commit",
            Self::CommitAndPush => "Commit & Push",
            Self::Push => "Push",
            Self::OpenPR => "Open PR",
            Self::CommitAll => "Commit All",
            Self::CommitAllAndPush => "Commit All & Push",
        }
    }

    pub fn needs_message(&self) -> bool {
        !matches!(self, Self::Push)
    }
}

#[derive(Clone, PartialEq)]
pub enum CommitPhase {
    Generating,
    Editing,
    Executing,
}

pub enum CommitKeyResult {
    Continue,
    Execute,
    Cancel,
    /// Open $EDITOR; the String is the temp file path.
    OpenEditor(String),
}

pub struct CommitOverlay {
    pub action: CommitAction,
    pub phase: CommitPhase,
    pub input: String,
    /// Byte offset cursor position within `input`.
    pub cursor: usize,
    pub generating_message: String,
    pub spinner_frame: usize,
}

impl CommitOverlay {
    pub fn new(action: CommitAction) -> Self {
        let phase = if action.needs_message() {
            CommitPhase::Generating
        } else {
            CommitPhase::Executing
        };
        Self {
            action,
            phase,
            input: String::new(),
            cursor: 0,
            generating_message: String::new(),
            spinner_frame: 0,
        }
    }

    pub fn handle_key(&mut self, key: KeyEvent) -> CommitKeyResult {
        let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
        match self.phase {
            CommitPhase::Generating => {
                if key.code == KeyCode::Esc {
                    return CommitKeyResult::Cancel;
                }
                CommitKeyResult::Continue
            }
            CommitPhase::Editing => match key.code {
                KeyCode::Esc => CommitKeyResult::Cancel,
                KeyCode::Char('g') if ctrl => {
                    let tmp = std::env::temp_dir().join(format!(
                        "ghq-commit-{}.txt",
                        std::process::id()
                    ));
                    let _ = std::fs::write(&tmp, &self.input);
                    CommitKeyResult::OpenEditor(tmp.to_string_lossy().to_string())
                }
                KeyCode::Char('a') if ctrl => {
                    // Home — move to start of current line
                    let line_start = self.input[..self.cursor].rfind('\n').map_or(0, |i| i + 1);
                    self.cursor = line_start;
                    CommitKeyResult::Continue
                }
                KeyCode::Home => {
                    let line_start = self.input[..self.cursor].rfind('\n').map_or(0, |i| i + 1);
                    self.cursor = line_start;
                    CommitKeyResult::Continue
                }
                KeyCode::End => {
                    let line_end = self.input[self.cursor..]
                        .find('\n')
                        .map_or(self.input.len(), |i| self.cursor + i);
                    self.cursor = line_end;
                    CommitKeyResult::Continue
                }
                KeyCode::Enter => {
                    if key.modifiers.contains(KeyModifiers::SHIFT)
                        || key.modifiers.contains(KeyModifiers::ALT)
                    {
                        self.input.insert(self.cursor, '\n');
                        self.cursor += 1;
                        CommitKeyResult::Continue
                    } else if !self.input.trim().is_empty() {
                        self.phase = CommitPhase::Executing;
                        CommitKeyResult::Execute
                    } else {
                        CommitKeyResult::Continue
                    }
                }
                KeyCode::Backspace => {
                    if self.cursor > 0 {
                        // Find the previous char boundary
                        let prev = prev_char_boundary(&self.input, self.cursor);
                        self.input.drain(prev..self.cursor);
                        self.cursor = prev;
                    }
                    CommitKeyResult::Continue
                }
                KeyCode::Delete => {
                    if self.cursor < self.input.len() {
                        let next = next_char_boundary(&self.input, self.cursor);
                        self.input.drain(self.cursor..next);
                    }
                    CommitKeyResult::Continue
                }
                KeyCode::Left => {
                    if self.cursor > 0 {
                        self.cursor = prev_char_boundary(&self.input, self.cursor);
                    }
                    CommitKeyResult::Continue
                }
                KeyCode::Right => {
                    if self.cursor < self.input.len() {
                        self.cursor = next_char_boundary(&self.input, self.cursor);
                    }
                    CommitKeyResult::Continue
                }
                KeyCode::Up => {
                    // Move cursor up one line, preserving column offset
                    let (row, col) = cursor_row_col(&self.input, self.cursor);
                    if row > 0 {
                        self.cursor = offset_from_row_col(&self.input, row - 1, col);
                    }
                    CommitKeyResult::Continue
                }
                KeyCode::Down => {
                    let (row, col) = cursor_row_col(&self.input, self.cursor);
                    let line_count = self.input.split('\n').count();
                    if row + 1 < line_count {
                        self.cursor = offset_from_row_col(&self.input, row + 1, col);
                    }
                    CommitKeyResult::Continue
                }
                KeyCode::Char(c) => {
                    self.input.insert(self.cursor, c);
                    self.cursor += c.len_utf8();
                    CommitKeyResult::Continue
                }
                _ => CommitKeyResult::Continue,
            },
            CommitPhase::Executing => CommitKeyResult::Continue,
        }
    }

    pub fn append_token(&mut self, token: &str) {
        self.generating_message.push_str(token);
    }

    pub fn finish_generation(&mut self) {
        self.input = self.generating_message.trim().to_string();
        self.cursor = self.input.len();
        self.phase = CommitPhase::Editing;
    }

    pub fn generation_error(&mut self) {
        self.input.clear();
        self.phase = CommitPhase::Editing;
    }

    pub fn tick(&mut self) {
        self.spinner_frame = self.spinner_frame.wrapping_add(1);
    }

    pub fn render(&self, frame: &mut Frame, area: Rect, border_color: Color) {
        // Compute inner width to match popup layout: modal_w = max(area/2, min_width+4)
        let area_w = area.width as usize;
        let half = area_w / 2;
        let min_total = 64; // min_width(60) + 4 for borders
        let max_total = area_w.saturating_sub(4);
        let modal_w = half.max(min_total).min(max_total).max(6);
        let inner_w = modal_w.saturating_sub(4);

        let title = self.action.label();
        let lines = self.build_lines(inner_w);
        Popup::new(title)
            .lines(lines)
            .position(PopupPosition::Center)
            .border_color(border_color)
            .min_width(60)
            .render(frame, area);
    }

    fn build_lines(&self, max_width: usize) -> Vec<Line<'_>> {
        let spinner = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
        let frame = self.spinner_frame % spinner.len();
        let hint = Style::default().fg(Color::DarkGray);
        let accent = Style::default().fg(Color::Yellow);

        match &self.phase {
            CommitPhase::Generating => {
                let mut lines = vec![Line::from(vec![
                    Span::styled(spinner[frame], accent),
                    Span::raw(" "),
                    Span::styled("Generating message...", hint),
                ])];

                if !self.generating_message.is_empty() {
                    lines.push(Line::from(""));
                    for wrapped in wrap_text(&self.generating_message, max_width) {
                        lines.push(Line::from(Span::raw(wrapped)));
                    }
                }

                lines
            }
            CommitPhase::Editing => {
                let mut lines = Vec::new();
                let before_cursor = &self.input[..self.cursor];

                // Which input line has the cursor, and at what byte col
                let pre_lines: Vec<&str> = before_cursor.split('\n').collect();
                let cursor_line_idx = pre_lines.len() - 1;
                let cursor_col_bytes = pre_lines.last().unwrap_or(&"").len();

                let input_lines: Vec<&str> = if self.input.is_empty() {
                    vec![""]
                } else {
                    self.input.split('\n').collect()
                };

                for (i, line) in input_lines.iter().enumerate() {
                    if i != cursor_line_idx {
                        for wl in wrap_text(line, max_width) {
                            lines.push(Line::from(wl));
                        }
                        continue;
                    }

                    // This line has the cursor — wrap then locate cursor
                    let wrapped = wrap_text(line, max_width);
                    // Map cursor_col_bytes to (visual_row, visual_col_bytes)
                    let mut bytes_remaining = cursor_col_bytes;
                    let mut vis_row = wrapped.len().saturating_sub(1);
                    let mut vis_col = 0;
                    for (wi, wl) in wrapped.iter().enumerate() {
                        let wl_len = wl.len();
                        if bytes_remaining <= wl_len {
                            vis_row = wi;
                            vis_col = bytes_remaining;
                            break;
                        }
                        // Account for the space that was removed at wrap boundary
                        // (wrap_text splits on whitespace, consuming it)
                        bytes_remaining -= wl_len;
                        // Skip whitespace between wrapped segments
                        let rest = &line[cursor_col_bytes - bytes_remaining..];
                        if rest.starts_with(' ') {
                            bytes_remaining = bytes_remaining.saturating_sub(1);
                        }
                    }

                    for (wi, wl) in wrapped.iter().enumerate() {
                        if wi != vis_row {
                            lines.push(Line::from(wl.clone()));
                            continue;
                        }
                        // Render this visual line with cursor
                        let before_part = &wl[..vis_col];
                        let after_part = &wl[vis_col..];
                        if after_part.is_empty() {
                            // Cursor at end of visual line
                            lines.push(Line::from(vec![
                                Span::raw(before_part.to_string()),
                                Span::styled("█", Style::default().fg(Color::White)),
                            ]));
                        } else {
                            let next = next_char_boundary(after_part, 0);
                            let cursor_char = &after_part[..next];
                            let rest = &after_part[next..];
                            lines.push(Line::from(vec![
                                Span::raw(before_part.to_string()),
                                Span::styled(
                                    cursor_char.to_string(),
                                    Style::default().fg(Color::Black).bg(Color::White),
                                ),
                                Span::raw(rest.to_string()),
                            ]));
                        }
                    }

                    // Edge case: empty line with cursor
                    if wrapped.is_empty() || (wrapped.len() == 1 && wrapped[0].is_empty()) {
                        // Already pushed by the loop above if wrapped has one empty entry,
                        // but if wrap_text returned [""], the cursor-at-end path handles it.
                        // If it returned empty vec, push cursor:
                        if wrapped.is_empty() {
                            lines.push(Line::from(vec![
                                Span::styled("█", Style::default().fg(Color::White)),
                            ]));
                        }
                    }
                }

                // Hint line: left hints ... padding ... enter submit
                lines.push(Line::from(""));
                let left_spans = vec![
                    Span::styled("esc", accent),
                    Span::styled(" cancel  ", hint),
                    Span::styled("ctrl+g", accent),
                    Span::styled(" editor  ", hint),
                    Span::styled("shift+↵", accent),
                    Span::styled(" newline", hint),
                ];
                let right_spans = vec![
                    Span::styled("↵", accent),
                    Span::styled(" submit", hint),
                ];
                let left_w: usize = left_spans
                    .iter()
                    .map(|s| UnicodeWidthStr::width(s.content.as_ref()))
                    .sum();
                let right_w: usize = right_spans
                    .iter()
                    .map(|s| UnicodeWidthStr::width(s.content.as_ref()))
                    .sum();
                let pad = max_width.saturating_sub(left_w + right_w);
                let mut hint_spans = left_spans;
                hint_spans.push(Span::raw(" ".repeat(pad)));
                hint_spans.extend(right_spans);
                lines.push(Line::from(hint_spans));

                lines
            }
            CommitPhase::Executing => {
                let desc = match self.action {
                    CommitAction::Commit | CommitAction::CommitAll => "Committing...",
                    CommitAction::Push => "Pushing...",
                    CommitAction::CommitAndPush | CommitAction::CommitAllAndPush => {
                        "Committing and pushing..."
                    }
                    CommitAction::OpenPR => "Creating PR...",
                };
                vec![Line::from(vec![
                    Span::styled(spinner[frame], accent),
                    Span::raw(" "),
                    Span::raw(desc),
                ])]
            }
        }
    }
}

/// Find the previous char boundary before `pos` in `s`.
fn prev_char_boundary(s: &str, pos: usize) -> usize {
    let mut p = pos.saturating_sub(1);
    while p > 0 && !s.is_char_boundary(p) {
        p -= 1;
    }
    p
}

/// Find the next char boundary after `pos` in `s`.
fn next_char_boundary(s: &str, pos: usize) -> usize {
    let mut p = pos + 1;
    while p < s.len() && !s.is_char_boundary(p) {
        p += 1;
    }
    p.min(s.len())
}

/// Get (row, col) of a byte offset cursor in a string (0-indexed, col in chars).
fn cursor_row_col(s: &str, cursor: usize) -> (usize, usize) {
    let before = &s[..cursor];
    let row = before.matches('\n').count();
    let line_start = before.rfind('\n').map_or(0, |i| i + 1);
    let col = before[line_start..].chars().count();
    (row, col)
}

/// Get byte offset from (row, col) in a string, clamped to line length.
fn offset_from_row_col(s: &str, row: usize, col: usize) -> usize {
    let mut offset = 0;
    for (i, line) in s.split('\n').enumerate() {
        if i == row {
            for (char_count, (byte_offset, _)) in line.char_indices().enumerate() {
                if char_count == col {
                    return offset + byte_offset;
                }
            }
            // col is past end of line — clamp to end
            return offset + line.len();
        }
        offset += line.len() + 1; // +1 for '\n'
    }
    s.len()
}

/// Word-wrap text to fit within `max_width` columns.
fn wrap_text(text: &str, max_width: usize) -> Vec<String> {
    let max_width = max_width.max(10);
    let mut result = Vec::new();
    for line in text.split('\n') {
        if line.is_empty() {
            result.push(String::new());
            continue;
        }
        let mut current = String::new();
        let mut current_w: usize = 0;
        for word in line.split_whitespace() {
            let word_w = UnicodeWidthStr::width(word);
            if current.is_empty() {
                current = word.to_string();
                current_w = word_w;
            } else if current_w + 1 + word_w <= max_width {
                current.push(' ');
                current.push_str(word);
                current_w += 1 + word_w;
            } else {
                result.push(current);
                current = word.to_string();
                current_w = word_w;
            }
        }
        if !current.is_empty() {
            result.push(current);
        }
    }
    if result.is_empty() {
        result.push(String::new());
    }
    result
}

/// Event sent back from async git operations.
pub enum CommitEvent {
    Success(String),
    Error(String),
}

/// Execute the chosen commit action asynchronously.
pub async fn execute_commit_action(
    action: CommitAction,
    message: &str,
    repo_root: &str,
    tx: tokio::sync::mpsc::UnboundedSender<CommitEvent>,
) {
    let result = match action {
        CommitAction::Commit => crate::git::commit::commit(repo_root, message)
            .await
            .map(|_| "Committed successfully".to_string()),
        CommitAction::CommitAndPush => {
            match crate::git::commit::commit(repo_root, message).await {
                Ok(_) => crate::git::commit::push(repo_root)
                    .await
                    .map(|_| "Committed and pushed".to_string()),
                Err(e) => Err(e),
            }
        }
        CommitAction::Push => crate::git::commit::push(repo_root)
            .await
            .map(|_| "Pushed successfully".to_string()),
        CommitAction::OpenPR => {
            let mut lines = message.lines();
            let title = lines.next().unwrap_or("").trim();
            let body: String = lines.collect::<Vec<_>>().join("\n");
            let body = body.trim();
            crate::git::commit::create_pr(repo_root, title, body)
                .await
                .map(|_| "PR created successfully".to_string())
        }
        CommitAction::CommitAll => match crate::git::stage::stage_all(repo_root).await {
            Ok(_) => crate::git::commit::commit(repo_root, message)
                .await
                .map(|_| "Committed all changes".to_string()),
            Err(e) => Err(e),
        },
        CommitAction::CommitAllAndPush => match crate::git::stage::stage_all(repo_root).await {
            Ok(_) => match crate::git::commit::commit(repo_root, message).await {
                Ok(_) => crate::git::commit::push(repo_root)
                    .await
                    .map(|_| "Committed all and pushed".to_string()),
                Err(e) => Err(e),
            },
            Err(e) => Err(e),
        },
    };

    match result {
        Ok(msg) => {
            let _ = tx.send(CommitEvent::Success(msg));
        }
        Err(e) => {
            let _ = tx.send(CommitEvent::Error(format!("{e}")));
        }
    }
}

pub fn build_commit_prompt(diff: &str, branch: &str, extra: Option<&str>) -> String {
    let mut prompt = format!(
        "Here is a diff for a branch called `{branch}`.\n\n"
    );
    if let Some(extra) = extra {
        prompt.push_str(extra);
        prompt.push_str("\n\n");
    }
    prompt.push_str(
        "Write a clear, conventional commit message summarizing the changes. \
         Use a concise subject line (max 72 chars). If needed, include a brief body \
         after a blank line. Output only the commit message, no explanations.\n\n",
    );
    prompt.push_str(diff);
    prompt
}

pub fn build_pr_prompt(diff: &str, log: &str, branch: &str, extra: Option<&str>) -> String {
    let mut prompt = format!(
        "Here is a diff and commit log for a branch called `{branch}`.\n\n"
    );
    if let Some(extra) = extra {
        prompt.push_str(extra);
        prompt.push_str("\n\n");
    }
    prompt.push_str(
        "Write a clear pull request description. Start with a brief title on the first line, \
         then a blank line, then the body. Output only the title and description, no explanations.\n\n\
         Diff:\n",
    );
    prompt.push_str(diff);
    prompt.push_str("\n\nCommit log:\n");
    prompt.push_str(log);
    prompt
}
