use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};

use crate::ui::components::file_tree::{self, FileTreeEntry};
use crate::ui::scroll::ScrollState;
use crate::ui::styles::DiffColors;
use crate::github::types::PullRequestFile;

/// Marker returned by `set_files` indicating the diff cursor should be reset.
pub struct ResetCursor;

pub enum UpdateResult {
    /// The current file was retained — caller should preserve diff cursor/offset.
    Preserved,
    /// The current file was removed — caller should reset diff cursor/offset.
    Reset,
}

fn spans_width(spans: &[Span]) -> usize {
    use unicode_width::UnicodeWidthStr;
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
        comment_counts: &std::collections::HashMap<String, usize>,
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

        let badge = if !entry.is_dir {
            if let Some(f) = self.files.get(entry.file_index as usize) {
                comment_counts.get(&f.filename).copied().filter(|&n| n > 0)
            } else {
                None
            }
        } else {
            None
        };
        let badge_text = badge.map(|n| format!("●{n}"));
        let badge_w = badge_text.as_ref().map_or(0, |t| {
            use unicode_width::UnicodeWidthStr;
            UnicodeWidthStr::width(t.as_str())
        });

        let text_w = spans_width(&spans);
        let total_content = text_w + badge_w;
        let pad = inner_w.saturating_sub(total_content);
        if pad > 0 {
            spans.push(Span::raw(" ".repeat(pad)));
        }
        if let Some(text) = badge_text {
            spans.push(Span::styled(text, Style::default().fg(Color::Cyan)));
        }
        spans.push(sep);

        Line::from(spans)
    }
}
