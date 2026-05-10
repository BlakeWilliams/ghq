use unicode_width::UnicodeWidthStr;
use unicode_segmentation::UnicodeSegmentation;
use ratatui::style::Color;

use super::super::styles::DiffColors;
use crate::ui::copilot_state::{ToolGroup, ToolStatus};

/// Word-wrap text to fit within `max_width` visible columns.
/// Returns a Vec of wrapped lines. Preserves explicit newlines.
fn wrap_text(text: &str, max_width: usize) -> Vec<String> {
    if max_width == 0 {
        return vec![text.to_string()];
    }
    let mut result = Vec::new();
    for line in text.split('\n') {
        if UnicodeWidthStr::width(line) <= max_width {
            result.push(line.to_string());
            continue;
        }
        let mut current = String::new();
        let mut current_w: usize = 0;
        for word in line.split_word_bounds() {
            let word_w = UnicodeWidthStr::width(word);
            if current_w + word_w > max_width && current_w > 0 {
                result.push(current);
                current = String::new();
                current_w = 0;
                let trimmed = word.trim_start();
                if !trimmed.is_empty() {
                    current.push_str(trimmed);
                    current_w = UnicodeWidthStr::width(trimmed);
                }
            } else {
                current.push_str(word);
                current_w += word_w;
            }
        }
        if !current.is_empty() || result.is_empty() {
            result.push(current);
        }
    }
    if result.is_empty() {
        result.push(String::new());
    }
    result
}

/// DJB2-style hash for weechat-like username coloring.
fn nick_color(name: &str) -> Color {
    const NICK_COLORS: &[Color] = &[
        Color::Red,
        Color::Green,
        Color::Yellow,
        Color::Blue,
        Color::Magenta,
        Color::Cyan,
        Color::LightRed,
        Color::LightGreen,
        Color::LightYellow,
        Color::LightBlue,
        Color::LightMagenta,
        Color::LightCyan,
        Color::Indexed(208), // orange
        Color::Indexed(172), // dark orange
        Color::Indexed(141), // purple
        Color::Indexed(167), // salmon
        Color::Indexed(109), // steel blue
        Color::Indexed(150), // sage
    ];
    let mut hash: u32 = 5381;
    for c in name.chars() {
        hash = hash.wrapping_mul(33).wrapping_add(c as u32);
    }
    NICK_COLORS[(hash as usize) % NICK_COLORS.len()]
}

#[derive(Clone)]
pub struct PanelComment {
    pub author: String,
    pub is_copilot: bool,
    pub body: String,
    pub is_pending: bool,
    pub tool_groups: Vec<ToolGroup>,
}

pub enum ReplyMode {
    GitHub,
    Copilot,
}

pub struct CommentPanel {
    pub visible: bool,
    pub scroll_offset: usize,
    pub width: u16,
    pub thread_key: Option<String>,
    pub comments: Vec<PanelComment>,
    pub reply_view: Option<String>,
    pub reply_mode: ReplyMode,
    pub file_path: String,
}

const MIN_PANEL_WIDTH: u16 = 45;
const MAX_PANEL_WIDTH: u16 = 100;

impl CommentPanel {
    pub fn new() -> Self {
        Self {
            visible: false,
            scroll_offset: 0,
            width: MIN_PANEL_WIDTH,
            thread_key: None,
            comments: Vec::new(),
            reply_view: None,
            reply_mode: ReplyMode::Copilot,
            file_path: String::new(),
        }
    }

    pub fn open_thread(&mut self, thread_key: String, comments: Vec<PanelComment>, file_path: String) {
        let changed_thread = self.thread_key.as_deref() != Some(&thread_key);
        self.visible = true;
        self.thread_key = Some(thread_key);
        self.comments = comments;
        self.file_path = file_path;
        if changed_thread {
            self.scroll_offset = 0;
        }
    }

    pub fn set_reply_view(&mut self, text: String, mode: ReplyMode) {
        self.reply_view = Some(text);
        self.reply_mode = mode;
    }

    pub fn clear_reply_view(&mut self) {
        self.reply_view = None;
    }

    pub fn close(&mut self) {
        self.visible = false;
        self.thread_key = None;
        self.comments.clear();
        self.reply_view = None;
        self.file_path.clear();
        self.scroll_offset = 0;
    }

    pub fn scroll_down(&mut self, n: usize) {
        let max = self.content_line_count().saturating_sub(1);
        self.scroll_offset = (self.scroll_offset + n).min(max);
    }

    pub fn scroll_up(&mut self, n: usize) {
        self.scroll_offset = self.scroll_offset.saturating_sub(n);
    }

    /// Compute effective panel width clamped to available space.
    pub fn effective_width(available: u16) -> u16 {
        let w = available.min(MAX_PANEL_WIDTH).max(MIN_PANEL_WIDTH);
        w.min(available)
    }

    pub fn build_lines(&self, colors: &DiffColors, viewport_height: usize, inner_width: usize) -> Vec<PanelLine> {
        let mut lines = Vec::new();
        let w = inner_width;
        let body_wrap_w = w.saturating_sub(1);

        // File path header
        if !self.file_path.is_empty() {
            for wl in wrap_text(&self.file_path, w) {
                lines.push(PanelLine::styled(&wl, colors.context_fg, true));
            }
        }

        // Comments
        if !self.comments.is_empty() {
            lines.push(PanelLine::empty());

            for (i, comment) in self.comments.iter().enumerate() {
                // Author line: @username or ✦ @copilot
                let (author_display, author_color) = if comment.is_copilot {
                    (format!(" ✦ @{}", comment.author.to_lowercase()), Color::Cyan)
                } else {
                    (format!(" @{}", comment.author), nick_color(&comment.author))
                };
                lines.push(PanelLine::styled(&author_display, author_color, true));

                // Tool groups (before body text, matching Go order)
                for group in &comment.tool_groups {
                    self.build_tool_group_lines(&mut lines, group, w, colors);
                }

                // Body text
                if !comment.body.is_empty() {
                    let wrapped = wrap_text(&comment.body, body_wrap_w);
                    for wl in &wrapped {
                        lines.push(PanelLine::text(&format!(" {wl}")));
                    }
                } else if comment.is_pending && comment.tool_groups.is_empty() {
                    lines.push(PanelLine::text(" "));
                }

                // Separator between comments
                if i < self.comments.len() - 1 {
                    lines.push(PanelLine::empty());
                    let sep_w = w.saturating_sub(2);
                    lines.push(PanelLine::styled(
                        &format!(" {}", "─".repeat(sep_w)),
                        colors.chrome_fg,
                        false,
                    ));
                    lines.push(PanelLine::empty());
                }
            }

            lines.push(PanelLine::empty());
        }

        // Reply input area
        if let Some(reply) = &self.reply_view {
            let (border_color, label) = match self.reply_mode {
                ReplyMode::Copilot => (Color::Cyan, "Asking Copilot"),
                ReplyMode::GitHub => (Color::DarkGray, "Replying on GitHub"),
            };

            // Push reply area to bottom if content is short
            let reply_lines = self.count_reply_lines(reply, body_wrap_w);
            let content_lines = lines.len();
            let total = content_lines + reply_lines;
            if total < viewport_height {
                let padding = viewport_height - total;
                for _ in 0..padding {
                    lines.push(PanelLine::empty());
                }
            }

            let label_part = format!("── {label} ");
            let fill_w = w.saturating_sub(UnicodeWidthStr::width(label_part.as_str()));
            lines.push(PanelLine::styled(
                &format!("{label_part}{}", "─".repeat(fill_w)),
                border_color,
                false,
            ));

            let wrapped = wrap_text(reply, body_wrap_w);
            for wl in &wrapped {
                lines.push(PanelLine::text(&format!(" {wl}")));
            }
            if reply.is_empty() || reply.ends_with('\n') {
                lines.push(PanelLine::text(" ▍"));
            } else {
                let last = lines.last_mut().unwrap();
                last.text.push('▍');
            }

            lines.push(PanelLine::styled(
                &"─".repeat(w),
                border_color,
                false,
            ));
        }

        lines
    }

    fn count_reply_lines(&self, reply: &str, body_wrap_w: usize) -> usize {
        let wrapped = wrap_text(reply, body_wrap_w);
        // top border + wrapped lines (at least 1 for cursor) + bottom border
        2 + wrapped.len().max(1)
    }

    fn build_tool_group_lines(&self, lines: &mut Vec<PanelLine>, group: &ToolGroup, w: usize, _colors: &DiffColors) {
        if group.tools.is_empty() {
            return;
        }

        let border_color = match group.overall_status() {
            ToolStatus::Running => Color::Yellow,
            ToolStatus::Failed => Color::Red,
            ToolStatus::Done => Color::Green,
        };

        let sub_w = w.saturating_sub(2); // indent
        let inner_w = sub_w.saturating_sub(4).max(6); // "│ " + " │"

        // Top border with optional label
        let top = if !group.label.is_empty() {
            let label = format!(" {} ", group.label);
            let label_w = UnicodeWidthStr::width(label.as_str());
            let fill = sub_w.saturating_sub(2).saturating_sub(label_w);
            format!(" ╭{label}{}", "─".repeat(fill) + "╮")
        } else {
            let fill = sub_w.saturating_sub(2);
            format!(" ╭{}╮", "─".repeat(fill))
        };
        lines.push(PanelLine::styled(&top, border_color, false));

        // Tool rows
        for tc in &group.tools {
            let icon = match tc.status {
                ToolStatus::Running => "●",
                ToolStatus::Done => "●",
                ToolStatus::Failed => "✕",
            };
            let icon_color = match tc.status {
                ToolStatus::Running => Color::Yellow,
                ToolStatus::Done => Color::Green,
                ToolStatus::Failed => Color::Red,
            };

            let name = &tc.name;
            let content = if !tc.args_summary.is_empty() {
                format!("{icon} {name} {}", tc.args_summary)
            } else {
                format!("{icon} {name}")
            };

            let content_w = UnicodeWidthStr::width(content.as_str());
            let pad = inner_w.saturating_sub(content_w);
            let row = format!(" │ {content}{} │", " ".repeat(pad));

            // We use the icon color for the whole row for simplicity;
            // the border chars get border_color from the PanelLine
            lines.push(PanelLine::styled(&row, icon_color, false));
        }

        // Bottom border
        let bot_fill = sub_w.saturating_sub(2);
        lines.push(PanelLine::styled(
            &format!(" ╰{}╯", "─".repeat(bot_fill)),
            border_color,
            false,
        ));
    }

    pub fn content_line_count(&self) -> usize {
        let body_wrap_w = (self.width as usize).saturating_sub(2);
        let mut count = 0;
        if !self.file_path.is_empty() {
            count += wrap_text(&self.file_path, self.width as usize).len();
        }
        if !self.comments.is_empty() {
            count += 1; // blank after file path
            for (i, c) in self.comments.iter().enumerate() {
                count += 1; // author
                // Tool groups
                for g in &c.tool_groups {
                    if !g.tools.is_empty() {
                        count += 2 + g.tools.len(); // top + rows + bottom
                    }
                }
                if !c.body.is_empty() {
                    count += wrap_text(&c.body, body_wrap_w).len();
                } else if c.is_pending && c.tool_groups.is_empty() {
                    count += 1;
                }
                if i < self.comments.len() - 1 {
                    count += 3; // blank + sep + blank
                }
            }
            count += 1; // trailing blank
        }
        if let Some(reply) = &self.reply_view {
            count += self.count_reply_lines(reply, body_wrap_w);
        }
        count
    }
}

impl Default for CommentPanel {
    fn default() -> Self {
        Self::new()
    }
}

pub struct PanelLine {
    pub text: String,
    pub color: Color,
    pub bold: bool,
}

impl PanelLine {
    pub fn text(s: &str) -> Self {
        Self {
            text: s.to_string(),
            color: Color::Reset,
            bold: false,
        }
    }

    pub fn styled(s: &str, color: Color, bold: bool) -> Self {
        Self {
            text: s.to_string(),
            color,
            bold,
        }
    }

    pub fn empty() -> Self {
        Self {
            text: String::new(),
            color: Color::Reset,
            bold: false,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wrap_short_text() {
        let result = wrap_text("hello world", 40);
        assert_eq!(result, vec!["hello world"]);
    }

    #[test]
    fn wrap_long_text() {
        let result = wrap_text("the quick brown fox jumps over the lazy dog", 20);
        assert_eq!(result, vec!["the quick brown fox ", "jumps over the lazy ", "dog"]);
    }

    #[test]
    fn wrap_preserves_newlines() {
        let result = wrap_text("line one\nline two", 40);
        assert_eq!(result, vec!["line one", "line two"]);
    }

    #[test]
    fn wrap_empty_text() {
        let result = wrap_text("", 40);
        assert_eq!(result, vec![""]);
    }

    #[test]
    fn wrap_exact_width() {
        let result = wrap_text("12345", 5);
        assert_eq!(result, vec!["12345"]);
    }

    #[test]
    fn nick_color_deterministic() {
        let c1 = nick_color("blake");
        let c2 = nick_color("blake");
        assert_eq!(c1, c2);
        // Different names should (likely) get different colors
        let c3 = nick_color("copilot");
        assert_ne!(c1, c3);
    }

    #[test]
    fn open_thread_resets_scroll_on_new_thread() {
        let mut panel = CommentPanel::new();
        panel.scroll_offset = 10;
        panel.open_thread("t1".to_string(), Vec::new(), "f.rs".to_string());
        assert_eq!(panel.scroll_offset, 0);

        // Same thread should preserve scroll
        panel.scroll_offset = 5;
        panel.open_thread("t1".to_string(), Vec::new(), "f.rs".to_string());
        assert_eq!(panel.scroll_offset, 5);

        // Different thread resets
        panel.open_thread("t2".to_string(), Vec::new(), "f.rs".to_string());
        assert_eq!(panel.scroll_offset, 0);
    }
}
