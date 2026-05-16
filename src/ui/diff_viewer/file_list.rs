use std::collections::{HashMap, HashSet};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use unicode_width::UnicodeWidthStr;

use crate::ui::components::file_tree::{self, FileTreeEntry};
use crate::ui::scroll::ScrollState;
use crate::ui::styles::DiffColors;
use crate::github::types::PullRequestFile;

const ICON_COMMENT: &str = "\u{f0188}"; // 󰆈 nf-md-comment_text_outline

/// Marker returned by `set_files` indicating the diff cursor should be reset.
pub struct ResetCursor;

pub enum UpdateResult {
    /// The current file was retained — caller should preserve diff cursor/offset.
    Preserved,
    /// The current file was removed — caller should reset diff cursor/offset.
    Reset,
}

fn spans_width(spans: &[Span]) -> usize {
    spans
        .iter()
        .map(|s| UnicodeWidthStr::width(s.content.as_ref()))
        .sum()
}

pub struct FileList {
    pub entries: Vec<FileTreeEntry>,
    pub scroll: ScrollState,
    pub focused: bool,
    pub files: Vec<PullRequestFile>,
    pub current_file_idx: usize,
    pub loaded: bool,
}

impl FileList {
    pub fn new() -> Self {
        Self {
            entries: Vec::new(),
            scroll: ScrollState::new(),
            focused: true,
            files: Vec::new(),
            current_file_idx: 0,
            loaded: false,
        }
    }

    pub fn cursor(&self) -> usize {
        self.scroll.cursor
    }

    pub fn set_cursor(&mut self, pos: usize) {
        self.scroll.cursor = pos;
        self.scroll.set_total(self.entries.len());
        self.scroll.ensure_visible();
    }

    pub fn set_files(&mut self, files: Vec<PullRequestFile>) -> ResetCursor {
        self.entries = file_tree::build_file_tree(&files);
        self.files = files;
        self.current_file_idx = 0;
        let first = self
            .entries
            .iter()
            .position(|e| !e.is_dir)
            .unwrap_or(0);
        self.scroll.cursor = first;
        self.scroll.offset = 0;
        self.scroll.set_total(self.entries.len());
        ResetCursor
    }

    /// Update the file list while preserving the current file selection.
    /// Returns `Some(saved_cursor, saved_offset)` if the file was retained,
    /// `None` if the current file was removed (cursor reset needed).
    pub fn update_files(&mut self, files: Vec<PullRequestFile>) -> UpdateResult {
        let prev_filename = self
            .files
            .get(self.current_file_idx)
            .map(|f| f.filename.clone());

        self.entries = file_tree::build_file_tree(&files);
        self.files = files;
        self.scroll.set_total(self.entries.len());

        if let Some(ref name) = prev_filename {
            if let Some(idx) = self.files.iter().position(|f| &f.filename == name) {
                self.current_file_idx = idx;
                return UpdateResult::Preserved;
            }
        }

        self.current_file_idx = 0;
        let first = self
            .entries
            .iter()
            .position(|e| !e.is_dir)
            .unwrap_or(0);
        self.scroll.cursor = first;
        self.scroll.offset = 0;
        UpdateResult::Reset
    }

    pub fn current_filename(&self) -> String {
        self.files
            .get(self.current_file_idx)
            .map(|f| f.filename.clone())
            .unwrap_or_default()
    }

    pub fn next_file(&mut self) {
        if !self.files.is_empty() {
            self.current_file_idx = (self.current_file_idx + 1) % self.files.len();
        }
    }

    pub fn prev_file(&mut self) {
        if !self.files.is_empty() {
            if self.current_file_idx == 0 {
                self.current_file_idx = self.files.len() - 1;
            } else {
                self.current_file_idx -= 1;
            }
        }
    }

    /// Sync tree cursor to match current_file_idx.
    pub fn sync_cursor(&mut self) {
        for (i, entry) in self.entries.iter().enumerate() {
            if !entry.is_dir && entry.file_index as usize == self.current_file_idx {
                self.scroll.cursor = i;
                self.scroll.set_total(self.entries.len());
                break;
            }
        }
    }

    pub fn render_row(
        &self,
        row: usize,
        _tree_h: usize,
        tree_w: usize,
        colors: &DiffColors,
        comment_counts: &HashMap<String, usize>,
        copilot_working: &HashSet<String>,
        dots_frame: &str,
    ) -> Line<'static> {
        let inner_w = tree_w.saturating_sub(1);
        let sep = Span::styled("│", Style::default().fg(colors.chrome_fg));

        let total = self.entries.len();
        if total == 0 {
            return Line::from(vec![Span::raw(" ".repeat(inner_w)), sep]);
        }

        let idx = self.scroll.offset + row;

        if idx >= total {
            return Line::from(vec![Span::raw(" ".repeat(inner_w)), sep]);
        }

        let entry = &self.entries[idx];
        let is_cursor = idx == self.scroll.cursor;
        let is_current = !entry.is_dir && entry.file_index as usize == self.current_file_idx;

        let depth_pad = "  ".repeat(entry.depth);
        let mut spans: Vec<Span> = Vec::new();

        if entry.is_dir {
            let text = format!("  {depth_pad}{}", entry.display);
            spans.push(Span::styled(
                text,
                Style::default()
                    .fg(Color::Blue)
                    .add_modifier(Modifier::BOLD),
            ));
        } else {
            let name = &entry.display;
            let prefix = if is_cursor { "▸ " } else { "  " };
            let name_style = if is_cursor || is_current {
                Style::default()
                    .fg(Color::Magenta)
                    .add_modifier(Modifier::BOLD)
            } else {
                Style::default()
            };
            spans.push(Span::styled(
                format!("{depth_pad}{prefix}{name}"),
                name_style,
            ));

            if let Some(f) = self.files.get(entry.file_index as usize) {
                match f.status.as_str() {
                    "added" => {
                        spans.push(Span::styled(
                            " +",
                            Style::default().fg(Color::Green),
                        ));
                    }
                    "removed" => {
                        spans.push(Span::styled(
                            " -",
                            Style::default().fg(Color::Red),
                        ));
                    }
                    _ => {
                        if f.additions > 0 {
                            spans.push(Span::styled(
                                " +",
                                Style::default().fg(Color::Green),
                            ));
                        }
                        if f.deletions > 0 {
                            spans.push(Span::styled(
                                "-",
                                Style::default().fg(Color::Red),
                            ));
                        }
                    }
                }
            }
        }

        // Right-aligned badge: comment count or copilot spinner
        let (badge_text, badge_color) = if !entry.is_dir {
            if let Some(f) = self.files.get(entry.file_index as usize) {
                let is_working = copilot_working.contains(&f.filename);
                let count = comment_counts.get(&f.filename).copied().unwrap_or(0);

                if is_working {
                    (Some(dots_frame.to_string()), Color::Magenta)
                } else if count > 0 {
                    (Some(format!("{ICON_COMMENT} {count}")), Color::Yellow)
                } else {
                    (None, Color::Reset)
                }
            } else {
                (None, Color::Reset)
            }
        } else {
            (None, Color::Reset)
        };

        let badge_w = badge_text.as_ref().map_or(0, |t| UnicodeWidthStr::width(t.as_str()));
        let text_w = spans_width(&spans);
        let total_content = text_w + badge_w;
        let pad = inner_w.saturating_sub(total_content);
        if pad > 0 {
            spans.push(Span::raw(" ".repeat(pad)));
        }
        if let Some(text) = badge_text {
            spans.push(Span::styled(text, Style::default().fg(badge_color)));
        }
        spans.push(sep);

        Line::from(spans)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ui::styles::DiffColors;

    fn make_file(name: &str, status: &str, add: i32, del: i32) -> PullRequestFile {
        PullRequestFile {
            filename: name.to_string(),
            status: status.to_string(),
            additions: add,
            deletions: del,
            patch: String::new(),
            previous_filename: String::new(),
        }
    }

    fn make_list(files: Vec<PullRequestFile>) -> FileList {
        let mut list = FileList::new();
        list.set_files(files.clone());
        list
    }

    fn extract_text(line: &Line) -> String {
        line.spans.iter().map(|s| s.content.as_ref()).collect()
    }

    #[test]
    fn badge_shows_comment_count() {
        let files = vec![make_file("main.rs", "modified", 5, 2)];
        let list = make_list(files);
        let colors = DiffColors::default();
        let mut counts = HashMap::new();
        counts.insert("main.rs".to_string(), 3);
        let working = HashSet::new();

        let line = list.render_row(0, 20, 35, &colors, &counts, &working, "⠋");
        let text = extract_text(&line);
        assert!(text.contains('\u{f0188}'), "should have comment icon, got: {text}");
        assert!(text.contains('3'), "should show count 3");
    }

    #[test]
    fn badge_shows_spinner_when_copilot_working() {
        let files = vec![make_file("lib.rs", "modified", 1, 0)];
        let list = make_list(files);
        let colors = DiffColors::default();
        let counts = HashMap::new();
        let mut working = HashSet::new();
        working.insert("lib.rs".to_string());

        let line = list.render_row(0, 20, 35, &colors, &counts, &working, "⠹");
        let text = extract_text(&line);
        assert!(text.contains('⠹'), "should show spinner frame, got: {text}");
    }

    #[test]
    fn no_badge_when_no_comments_or_copilot() {
        let files = vec![make_file("README.md", "modified", 1, 0)];
        let list = make_list(files);
        let colors = DiffColors::default();
        let counts = HashMap::new();
        let working = HashSet::new();

        let line = list.render_row(0, 20, 35, &colors, &counts, &working, "⠋");
        let text = extract_text(&line);
        assert!(!text.contains('\u{f0188}'), "should not have comment icon");
    }

    #[test]
    fn copilot_working_overrides_comment_count() {
        let files = vec![make_file("app.rs", "modified", 1, 0)];
        let list = make_list(files);
        let colors = DiffColors::default();
        let mut counts = HashMap::new();
        counts.insert("app.rs".to_string(), 5);
        let mut working = HashSet::new();
        working.insert("app.rs".to_string());

        let line = list.render_row(0, 20, 35, &colors, &counts, &working, "⠸");
        let text = extract_text(&line);
        assert!(text.contains('⠸'), "spinner should show when copilot working");
        assert!(!text.contains('\u{f0188}'), "comment icon hidden when working");
    }

    #[test]
    fn badge_color_is_yellow_for_comments() {
        let files = vec![make_file("test.rs", "added", 10, 0)];
        let list = make_list(files);
        let colors = DiffColors::default();
        let mut counts = HashMap::new();
        counts.insert("test.rs".to_string(), 2);
        let working = HashSet::new();

        let line = list.render_row(0, 20, 35, &colors, &counts, &working, "⠋");
        let badge_span = line.spans.iter().find(|s| s.content.contains('\u{f0188}'));
        assert!(badge_span.is_some(), "should have badge span");
        let style = badge_span.unwrap().style;
        assert_eq!(style.fg, Some(Color::Yellow));
    }

    #[test]
    fn badge_color_is_magenta_for_copilot() {
        let files = vec![make_file("test.rs", "added", 10, 0)];
        let list = make_list(files);
        let colors = DiffColors::default();
        let counts = HashMap::new();
        let mut working = HashSet::new();
        working.insert("test.rs".to_string());

        let line = list.render_row(0, 20, 35, &colors, &counts, &working, "⠙");
        let badge_span = line.spans.iter().find(|s| s.content.contains('⠙'));
        assert!(badge_span.is_some(), "should have spinner span");
        let style = badge_span.unwrap().style;
        assert_eq!(style.fg, Some(Color::Magenta));
    }
}
