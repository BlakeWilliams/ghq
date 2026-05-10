pub mod panel;
pub mod render_list;
pub mod search;

use ratatui::layout::{Constraint, Direction, Layout, Rect};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::Paragraph;
use ratatui::Frame;
use unicode_width::UnicodeWidthStr;

use super::components::file_tree::{self, FileTreeEntry};
use super::highlight::Highlighter;
use super::styles::{self, DiffColors};
use crate::git::diff::DiffMode;
use crate::github::types::PullRequestFile;

pub use render_list::{DiffLineData, LineType, RenderItem, RenderList};

pub const TREE_WIDTH: u16 = 35;
const CHROME_ROWS: u16 = 4; // header + separator + footer separator + footer

pub struct LayoutInfo {
    pub mode: DiffMode,
    pub branch_name: String,
    pub file_count: usize,
    pub current_file_idx: usize,
    pub current_filename: String,
    pub additions: i32,
    pub deletions: i32,
    pub help_line: Vec<(String, String)>,
}

pub struct DiffViewer {
    pub viewport_offset: usize,
    pub diff_cursor: usize,
    pub render_list: RenderList,
    pub width: u16,
    pub height: u16,
    pub mode: DiffMode,
    pub search: search::SearchState,
    pub selection_start: Option<usize>,
    pub tree_entries: Vec<FileTreeEntry>,
    pub tree_cursor: usize,
    pub tree_focused: bool,
    pub panel_focused: bool,
    pub files: Vec<PullRequestFile>,
    pub current_file_idx: usize,
    pub(crate) highlighter: Highlighter,
    pub panel: panel::CommentPanel,
    pub dots_frame: usize,
    pub waiting_g: bool,
}

fn spans_width(spans: &[Span]) -> usize {
    spans
        .iter()
        .map(|s| UnicodeWidthStr::width(s.content.as_ref()))
        .sum()
}

fn expand_tabs_hl(spans: &[(Style, String)]) -> Vec<(Style, String)> {
    spans
        .iter()
        .map(|(s, t)| (*s, t.replace('\t', "        ")))
        .collect()
}

fn truncate_str(s: &str, max_width: usize) -> String {
    use unicode_width::UnicodeWidthChar;
    let mut w = 0;
    let mut result = String::new();
    for c in s.chars() {
        let cw = UnicodeWidthChar::width(c).unwrap_or(0);
        if w + cw > max_width {
            break;
        }
        result.push(c);
        w += cw;
    }
    result
}

/// Powerline rounded caps for badge pills.
const BADGE_CAP_LEFT: &str = "\u{e0b6}";
const BADGE_CAP_RIGHT: &str = "\u{e0b4}";
const BADGE_ICON: &str = "󰆈";

const BADGE_SPIN_FRAMES: &[&str] = &["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];

/// Overlay a comment badge pill on the right edge of a diff line's spans.
/// Truncates the line content to make room, then appends the pill.
fn overlay_badge(
    spans: &mut Vec<Span<'static>>,
    badge: &render_list::CommentBadge,
    line_bg: Color,
    width: usize,
    dots_frame: &usize,
) {
    let pill_color = if badge.has_pending {
        Color::Magenta
    } else if badge.resolved {
        Color::Cyan
    } else {
        Color::Yellow
    };

    let content_text = if badge.has_pending {
        let frame = BADGE_SPIN_FRAMES[*dots_frame % BADGE_SPIN_FRAMES.len()];
        format!("{BADGE_ICON} {frame}")
    } else {
        format!("{BADGE_ICON} {}", badge.count)
    };

    // Badge pill: capL + content + capR
    // Visible width: 1 (capL) + content_w + 1 (capR) + 1 (space before)
    let content_w = UnicodeWidthStr::width(content_text.as_str());
    let pill_total_w = 1 + content_w + 1 + 1; // space + capL + content + capR

    if pill_total_w >= width {
        return;
    }

    // Truncate spans to make room for the badge
    let target_w = width - pill_total_w;
    truncate_spans(spans, target_w, line_bg);

    // Append badge: space + capL(pill_color fg, line bg) + content(pill_color bg) + capR(pill_color fg, line bg)
    spans.push(Span::styled(" ", Style::default().bg(line_bg)));
    spans.push(Span::styled(
        BADGE_CAP_LEFT.to_string(),
        Style::default().fg(pill_color).bg(line_bg),
    ));
    spans.push(Span::styled(
        content_text,
        Style::default().fg(Color::Black).bg(pill_color),
    ));
    spans.push(Span::styled(
        BADGE_CAP_RIGHT.to_string(),
        Style::default().fg(pill_color).bg(line_bg),
    ));
}

/// Truncate a vec of spans to fit within `max_width` visible columns,
/// padding with `bg` to reach exactly `max_width`.
fn truncate_spans(spans: &mut Vec<Span<'static>>, max_width: usize, bg: Color) {
    use unicode_width::UnicodeWidthChar;

    let mut total_w = 0;
    let mut truncate_at: Option<(usize, usize)> = None; // (span_idx, char_offset)

    'outer: for (si, span) in spans.iter().enumerate() {
        for (ci, ch) in span.content.chars().enumerate() {
            let cw = UnicodeWidthChar::width(ch).unwrap_or(0);
            if total_w + cw > max_width {
                truncate_at = Some((si, ci));
                break 'outer;
            }
            total_w += cw;
        }
    }

    if let Some((si, ci)) = truncate_at {
        // Truncate span at si to ci chars
        let truncated_content: String = spans[si].content.chars().take(ci).collect();
        let style = spans[si].style;
        spans.truncate(si + 1);
        spans[si] = Span::styled(truncated_content, style);
        total_w = spans_width(spans);
    }

    // Pad to exact width
    let pad = max_width.saturating_sub(total_w);
    if pad > 0 {
        spans.push(Span::styled(" ".repeat(pad), Style::default().bg(bg)));
    }
}

impl DiffViewer {
    pub fn new(mode: DiffMode) -> Self {
        Self {
            viewport_offset: 0,
            diff_cursor: 0,
            render_list: RenderList::new(),
            width: 0,
            height: 0,
            mode,
            search: search::SearchState::new(),
            selection_start: None,
            tree_entries: Vec::new(),
            tree_cursor: 0,
            tree_focused: true,
            panel_focused: false,
            files: Vec::new(),
            current_file_idx: 0,
            highlighter: Highlighter::new(),
            panel: panel::CommentPanel::new(),
            dots_frame: 0,
            waiting_g: false,
        }
    }

    pub fn viewport_height(&self) -> u16 {
        self.height.saturating_sub(CHROME_ROWS)
    }

    pub fn resize(&mut self, width: u16, height: u16) {
        self.width = width;
        self.height = height;
    }

    pub fn total_lines(&self) -> usize {
        self.render_list.total_lines()
    }

    pub fn scroll_down(&mut self, n: usize) {
        let max = self.total_lines().saturating_sub(1);
        self.diff_cursor = (self.diff_cursor + n).min(max);
        self.sync_viewport();
    }

    pub fn scroll_up(&mut self, n: usize) {
        self.diff_cursor = self.diff_cursor.saturating_sub(n);
        self.sync_viewport();
    }

    pub fn scroll_half_page_down(&mut self) {
        let half = (self.viewport_height() / 2) as usize;
        self.scroll_down(half.max(1));
    }

    pub fn scroll_half_page_up(&mut self) {
        let half = (self.viewport_height() / 2) as usize;
        self.scroll_up(half.max(1));
    }

    /// Scroll the viewport directly (mouse wheel). Cursor follows only to stay visible.
    pub fn scroll_viewport(&mut self, delta: i32) {
        let vp_h = self.viewport_height() as usize;
        let total = self.total_lines();
        if total <= vp_h {
            return;
        }
        let max_offset = total.saturating_sub(vp_h);
        if delta > 0 {
            self.viewport_offset = (self.viewport_offset + delta as usize).min(max_offset);
        } else {
            self.viewport_offset = self.viewport_offset.saturating_sub((-delta) as usize);
        }
        // Clamp cursor to stay within viewport
        if self.diff_cursor < self.viewport_offset {
            self.diff_cursor = self.viewport_offset;
        } else if self.diff_cursor >= self.viewport_offset + vp_h {
            self.diff_cursor = self.viewport_offset + vp_h - 1;
        }
    }

    pub fn goto_top(&mut self) {
        self.diff_cursor = 0;
        self.viewport_offset = 0;
    }

    pub fn goto_bottom(&mut self) {
        self.diff_cursor = self.total_lines().saturating_sub(1);
        self.sync_viewport();
    }

    fn sync_viewport(&mut self) {
        let vp_height = self.viewport_height() as usize;
        if vp_height == 0 {
            return;
        }
        if self.diff_cursor < self.viewport_offset {
            self.viewport_offset = self.diff_cursor;
        } else if self.diff_cursor >= self.viewport_offset + vp_height {
            self.viewport_offset = self.diff_cursor - vp_height + 1;
        }
    }

    pub fn set_diff_lines(
        &mut self,
        mut lines: Vec<DiffLineData>,
        filename: &str,
        new_content: &str,
        old_content: &str,
    ) {
        let hl_new = if !new_content.is_empty() {
            self.highlighter.highlight_file(new_content, filename)
        } else {
            Vec::new()
        };
        let hl_old = if !old_content.is_empty() {
            self.highlighter.highlight_file(old_content, filename)
        } else {
            Vec::new()
        };

        for dl in &mut lines {
            match dl.line_type {
                LineType::Add | LineType::Context => {
                    if let Some(ln) = dl.new_line_no {
                        let idx = (ln - 1) as usize;
                        if idx < hl_new.len() {
                            dl.highlighted = expand_tabs_hl(&hl_new[idx]);
                            continue;
                        }
                    }
                    let expanded = dl.content.replace('\t', "        ");
                    dl.highlighted = self.highlighter.highlight_line(&expanded, filename);
                }
                LineType::Delete => {
                    if let Some(ln) = dl.old_line_no {
                        let idx = (ln - 1) as usize;
                        if idx < hl_old.len() {
                            dl.highlighted = expand_tabs_hl(&hl_old[idx]);
                            continue;
                        }
                    }
                    let expanded = dl.content.replace('\t', "        ");
                    dl.highlighted = self.highlighter.highlight_line(&expanded, filename);
                }
                LineType::HunkHeader => {}
            }
        }

        self.render_list = RenderList::from_diff_lines(lines);
        let total = self.total_lines();
        if self.diff_cursor >= total && total > 0 {
            self.diff_cursor = total - 1;
        }
    }

    pub fn set_files(&mut self, files: Vec<PullRequestFile>) {
        self.tree_entries = file_tree::build_file_tree(&files);
        self.files = files;
        self.current_file_idx = 0;
        self.diff_cursor = 0;
        self.viewport_offset = 0;
        // Set tree cursor to first non-directory entry
        self.tree_cursor = self
            .tree_entries
            .iter()
            .position(|e| !e.is_dir)
            .unwrap_or(0);
    }

    /// Update the file list while preserving the current file, cursor, and
    /// scroll position. Used by file-watcher reloads so the view doesn't jump.
    pub fn update_files(&mut self, files: Vec<PullRequestFile>) {
        // Remember current file by name (survives reordering).
        let prev_filename = self
            .files
            .get(self.current_file_idx)
            .map(|f| f.filename.clone());
        let saved_cursor = self.diff_cursor;
        let saved_offset = self.viewport_offset;

        self.tree_entries = file_tree::build_file_tree(&files);
        self.files = files;

        // Re-resolve file index by name.
        if let Some(ref name) = prev_filename {
            if let Some(idx) = self.files.iter().position(|f| &f.filename == name) {
                self.current_file_idx = idx;
            } else {
                // File removed from diff — clamp to valid index.
                self.current_file_idx = 0;
                self.diff_cursor = 0;
                self.viewport_offset = 0;
                self.tree_cursor = self
                    .tree_entries
                    .iter()
                    .position(|e| !e.is_dir)
                    .unwrap_or(0);
                return;
            }
        }

        // Restore cursor/scroll — will be clamped after diff lines are set.
        self.diff_cursor = saved_cursor;
        self.viewport_offset = saved_offset;
    }

    fn gutter_width(&self) -> usize {
        self.render_list.gutter_width()
    }

    pub fn render_layout(
        &mut self,
        frame: &mut Frame,
        area: Rect,
        colors: &DiffColors,
        info: &LayoutInfo,
    ) {
        self.width = area.width;
        self.height = area.height;

        if area.height < CHROME_ROWS + 1 {
            return;
        }

        // Vertical layout: header | separator | content | footer_sep | footer
        let vert = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(1), // header
                Constraint::Length(1), // separator
                Constraint::Fill(1),   // content
                Constraint::Length(1), // footer separator
                Constraint::Length(1), // footer
            ])
            .split(area);
        let header_area = vert[0];
        let sep_area = vert[1];
        let content_area = vert[2];
        let fsep_area = vert[3];
        let footer_area = vert[4];

        let vp_h = content_area.height as usize;

        // Horizontal layout for content: tree | diff+scrollbar | panel(optional)
        let tree_w = TREE_WIDTH.min(area.width / 3);
        let available_right = area.width.saturating_sub(tree_w);
        let panel_w: u16 = if self.panel.visible {
            let pw = panel::CommentPanel::panel_width(available_right);
            if pw > 0 {
                self.panel.width = pw;
            }
            pw
        } else {
            0
        };
        let fallback_panel = self.panel.visible && panel_w == 0;

        let content_cols = if panel_w > 0 {
            Layout::default()
                .direction(Direction::Horizontal)
                .constraints([
                    Constraint::Length(tree_w),
                    Constraint::Fill(1),      // diff + scrollbar
                    Constraint::Length(panel_w),
                ])
                .split(content_area)
        } else {
            Layout::default()
                .direction(Direction::Horizontal)
                .constraints([
                    Constraint::Length(tree_w),
                    Constraint::Fill(1),
                ])
                .split(content_area)
        };

        let tree_area = content_cols[0];
        let diff_area = content_cols[1];
        let panel_area = if panel_w > 0 {
            Some(content_cols[2])
        } else {
            None
        };

        // The diff area includes the scrollbar (1 col on the right)
        let diff_inner_w = (diff_area.width as usize).saturating_sub(1);
        let (thumb_start, thumb_len) = self.compute_scrollbar(vp_h);

        // Pre-build panel lines if visible (side panel or fallback)
        // Reserve 1 col for scrollbar on the right edge of the panel
        let panel_total_lines;
        let panel_lines = if self.panel.visible {
            if let Some(pa) = panel_area {
                // side panel: │ + content + scrollbar
                let inner_w = (pa.width as usize).saturating_sub(2);
                if inner_w > 0 {
                    let lines = self.panel.build_lines(colors, vp_h, inner_w);
                    panel_total_lines = lines.len();
                    lines
                } else {
                    panel_total_lines = 0;
                    Vec::new()
                }
            } else if fallback_panel {
                // Fallback: panel takes over the diff area; content + scrollbar
                self.panel.width = diff_area.width;
                let inner_w = diff_inner_w.saturating_sub(1);
                let lines = self.panel.build_lines(colors, vp_h, inner_w);
                panel_total_lines = lines.len();
                lines
            } else {
                panel_total_lines = 0;
                Vec::new()
            }
        } else {
            panel_total_lines = 0;
            Vec::new()
        };

        let (panel_thumb_start, panel_thumb_len) = if self.panel.visible && panel_total_lines > 0 {
            self.panel.compute_scrollbar(vp_h, panel_total_lines)
        } else {
            (-1, 0)
        };

        // Render header
        self.render_header(frame, header_area, colors, info, tree_w as usize);

        // Render separators (full-width)
        self.render_separator(frame, sep_area, colors, tree_w as usize);
        self.render_separator(frame, fsep_area, colors, tree_w as usize);

        // Render content rows
        for row in 0..vp_h {
            // Tree
            let tree_line = self.render_tree_row(row, vp_h, tree_area.width as usize, colors);
            frame.render_widget(
                Paragraph::new(tree_line),
                Rect::new(tree_area.x, tree_area.y + row as u16, tree_area.width, 1),
            );

            // Panel scrollbar character for this row
            let panel_scroll_char = if panel_thumb_start < 0 {
                Span::raw(" ")
            } else if (row as i32) >= panel_thumb_start && (row as i32) < panel_thumb_start + panel_thumb_len {
                Span::styled("┃", Style::default().fg(colors.line_number_fg))
            } else {
                Span::styled("│", Style::default().fg(colors.chrome_fg))
            };

            if fallback_panel {
                // Fallback mode: render panel content in the diff area
                let panel_inner_w = diff_inner_w.saturating_sub(1);
                let panel_line_idx = self.panel.scroll_offset + row;
                let mut spans: Vec<Span> = Vec::new();
                if let Some(pl) = panel_lines.get(panel_line_idx) {
                    for s in &pl.line.spans {
                        spans.push(s.clone());
                    }
                    let content_w: usize = pl.line.spans.iter()
                        .map(|s| UnicodeWidthStr::width(s.content.as_ref()))
                        .sum();
                    let pad = panel_inner_w.saturating_sub(content_w);
                    if pad > 0 {
                        spans.push(Span::raw(" ".repeat(pad)));
                    }
                } else {
                    spans.push(Span::raw(" ".repeat(panel_inner_w)));
                }
                spans.push(panel_scroll_char);
                frame.render_widget(
                    Paragraph::new(Line::from(spans)),
                    Rect::new(diff_area.x, diff_area.y + row as u16, diff_area.width, 1),
                );
            } else {
                // Normal mode: render diff content
                let diff_line = self.render_diff_row(row, diff_inner_w, colors);
                let scroll_char = if thumb_start < 0 {
                    Span::raw(" ")
                } else if (row as i32) >= thumb_start && (row as i32) < thumb_start + thumb_len {
                    Span::styled("┃", Style::default().fg(colors.line_number_fg))
                } else {
                    Span::styled("│", Style::default().fg(colors.chrome_fg))
                };
                let mut diff_spans = diff_line.spans;
                diff_spans.push(scroll_char);
                frame.render_widget(
                    Paragraph::new(Line::from(diff_spans)),
                    Rect::new(diff_area.x, diff_area.y + row as u16, diff_area.width, 1),
                );

                // Side panel
                if let Some(pa) = panel_area {
                    let border_color = if self.panel_focused {
                        Color::Cyan
                    } else {
                        colors.chrome_fg
                    };
                    // inner_w = total panel width - border(1) - scrollbar(1)
                    let inner_w = (pa.width as usize).saturating_sub(2);
                    let panel_line_idx = self.panel.scroll_offset + row;

                    let mut spans: Vec<Span> = vec![
                        Span::styled("│", Style::default().fg(border_color)),
                    ];
                    if let Some(pl) = panel_lines.get(panel_line_idx) {
                        for s in &pl.line.spans {
                            spans.push(s.clone());
                        }
                        let content_w: usize = pl.line.spans.iter()
                            .map(|s| UnicodeWidthStr::width(s.content.as_ref()))
                            .sum();
                        let pad = inner_w.saturating_sub(content_w);
                        if pad > 0 {
                            spans.push(Span::raw(" ".repeat(pad)));
                        }
                    } else {
                        spans.push(Span::raw(" ".repeat(inner_w)));
                    }
                    spans.push(panel_scroll_char.clone());
                    frame.render_widget(
                        Paragraph::new(Line::from(spans)),
                        Rect::new(pa.x, pa.y + row as u16, pa.width, 1),
                    );
                }
            }
        }

        // Render footer
        self.render_footer(frame, footer_area, colors, info, tree_w as usize);
    }

    fn render_header(
        &self,
        frame: &mut Frame,
        area: Rect,
        colors: &DiffColors,
        info: &LayoutInfo,
        tree_w: usize,
    ) {
        let right_w = (area.width as usize).saturating_sub(tree_w);
        let dim = Style::default().fg(Color::DarkGray);
        let bright = Style::default()
            .fg(Color::White)
            .add_modifier(Modifier::BOLD);
        let tree_title_style = if self.tree_focused { bright } else { dim };
        let diff_title_style = if !self.tree_focused { bright } else { dim };

        let file_label = match info.file_count {
            0 => "Files".to_string(),
            1 => "1 File".to_string(),
            n => format!("{n} Files"),
        };

        let mode_label = styles::mode_label(info.mode);
        let mode_style = styles::mode_style(info.mode);

        let tree_inner = tree_w.saturating_sub(1);

        let file_span = Span::styled(format!(" {file_label}"), tree_title_style);
        let mode_span = Span::styled(mode_label.to_string(), mode_style);
        let file_vis_w = UnicodeWidthStr::width(file_span.content.as_ref());
        let mode_vis_w = UnicodeWidthStr::width(mode_span.content.as_ref());
        let tree_pad = tree_inner.saturating_sub(file_vis_w + mode_vis_w);

        let mut spans = vec![
            file_span,
            Span::raw(" ".repeat(tree_pad)),
            mode_span,
            Span::styled("│", Style::default().fg(colors.chrome_fg)),
        ];

        // Diff header: " dir/filename ... +N -M "
        let filename = &info.current_filename;
        let (dir, file) = match filename.rfind('/') {
            Some(pos) => (&filename[..=pos], &filename[pos + 1..]),
            None => ("", filename.as_str()),
        };

        let mut diff_spans: Vec<Span> = vec![Span::raw(" ")];
        if !dir.is_empty() {
            diff_spans.push(Span::styled(dir.to_string(), dim));
        }
        diff_spans.push(Span::styled(file.to_string(), diff_title_style));

        let mut stats_spans: Vec<Span> = Vec::new();
        if info.additions > 0 {
            stats_spans.push(Span::styled(
                format!("+{}", info.additions),
                Style::default().fg(Color::Green),
            ));
        }
        if info.deletions > 0 {
            if !stats_spans.is_empty() {
                stats_spans.push(Span::raw(" "));
            }
            stats_spans.push(Span::styled(
                format!("-{}", info.deletions),
                Style::default().fg(Color::Red),
            ));
        }
        if !stats_spans.is_empty() {
            stats_spans.push(Span::raw(" "));
        }

        let diff_content_w = spans_width(&diff_spans);
        let stats_w = spans_width(&stats_spans);
        let diff_pad = right_w.saturating_sub(diff_content_w + stats_w);
        diff_spans.push(Span::raw(" ".repeat(diff_pad)));
        diff_spans.extend(stats_spans);

        spans.extend(diff_spans);
        frame.render_widget(Paragraph::new(Line::from(spans)), area);
    }

    fn render_separator(
        &self,
        frame: &mut Frame,
        area: Rect,
        colors: &DiffColors,
        tree_w: usize,
    ) {
        let tree_fill = tree_w.saturating_sub(1);
        let right_fill = (area.width as usize).saturating_sub(tree_w);
        let sep = format!(
            "{}┼{}",
            "─".repeat(tree_fill),
            "─".repeat(right_fill)
        );
        frame.render_widget(
            Paragraph::new(Line::from(Span::styled(
                sep,
                Style::default().fg(colors.chrome_fg),
            ))),
            area,
        );
    }

    fn render_footer(
        &self,
        frame: &mut Frame,
        area: Rect,
        colors: &DiffColors,
        info: &LayoutInfo,
        tree_w: usize,
    ) {
        let right_w = (area.width as usize).saturating_sub(tree_w);
        let tree_inner = tree_w.saturating_sub(1);
        let branch = &info.branch_name;
        let max_w = tree_inner.saturating_sub(2);
        let name = if UnicodeWidthStr::width(branch.as_str()) > max_w {
            let mut n = String::new();
            let mut w = 0;
            for c in branch.chars() {
                let cw = UnicodeWidthStr::width(c.to_string().as_str());
                if w + cw >= max_w {
                    n.push('…');
                    break;
                }
                n.push(c);
                w += cw;
            }
            n
        } else {
            branch.clone()
        };

        let name_span = Span::styled(format!(" {name}"), Style::default().fg(Color::DarkGray));
        let name_w = UnicodeWidthStr::width(name_span.content.as_ref());
        let pad = tree_inner.saturating_sub(name_w);

        let mut spans = vec![
            name_span,
            Span::raw(" ".repeat(pad)),
            Span::styled("│", Style::default().fg(colors.chrome_fg)),
        ];

        // Help hints in the right pane
        if !info.help_line.is_empty() {
            spans.push(Span::raw(" "));
            let key_style = Style::default()
                .fg(Color::Magenta)
                .add_modifier(Modifier::BOLD);
            let hint_style = Style::default().fg(Color::DarkGray);
            let sep_style = Style::default().fg(Color::DarkGray);
            for (i, (key, desc)) in info.help_line.iter().enumerate() {
                if i > 0 {
                    spans.push(Span::styled("  ", sep_style));
                }
                spans.push(Span::styled(key.clone(), key_style));
                spans.push(Span::raw(" "));
                spans.push(Span::styled(desc.clone(), hint_style));
            }
            let used = spans_width(&spans);
            let rpad = (area.width as usize).saturating_sub(used);
            if rpad > 0 {
                spans.push(Span::raw(" ".repeat(rpad)));
            }
        } else {
            spans.push(Span::raw(" ".repeat(right_w)));
        }

        frame.render_widget(Paragraph::new(Line::from(spans)), area);
    }

    fn render_tree_row(
        &self,
        row: usize,
        tree_h: usize,
        tree_w: usize,
        colors: &DiffColors,
    ) -> Line<'static> {
        let inner_w = tree_w.saturating_sub(1);
        let sep = Span::styled("│", Style::default().fg(colors.chrome_fg));

        let total = self.tree_entries.len();
        if total == 0 {
            return Line::from(vec![Span::raw(" ".repeat(inner_w)), sep]);
        }

        let mut start = self.tree_cursor as isize - tree_h as isize / 2;
        if start < 0 {
            start = 0;
        }
        if (start as usize) + tree_h > total {
            start = (total as isize - tree_h as isize).max(0);
        }
        let idx = start as usize + row;

        if idx >= total {
            return Line::from(vec![Span::raw(" ".repeat(inner_w)), sep]);
        }

        let entry = &self.tree_entries[idx];
        let is_cursor = idx == self.tree_cursor;
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

        let text_w = spans_width(&spans);
        let pad = inner_w.saturating_sub(text_w);
        if pad > 0 {
            spans.push(Span::raw(" ".repeat(pad)));
        }
        spans.push(sep);

        Line::from(spans)
    }

    fn render_diff_row(
        &self,
        row: usize,
        width: usize,
        colors: &DiffColors,
    ) -> Line<'static> {
        let idx = self.viewport_offset + row;
        let item = match self.render_list.get(idx) {
            Some(item) => item,
            None => return Line::from(Span::raw(" ".repeat(width))),
        };

        match item {
            RenderItem::DiffLine(dl) => self.render_diff_line_row(dl, idx, width, colors),
            RenderItem::CommentThread(thread) => {
                self.render_thread_row(thread, idx, width, colors)
            }
        }
    }

    fn render_thread_row(
        &self,
        thread: &render_list::CommentThreadData,
        idx: usize,
        width: usize,
        colors: &DiffColors,
    ) -> Line<'static> {
        let is_cursor = idx == self.diff_cursor;
        let bg = if is_cursor {
            colors.cursor_bg
        } else {
            Color::Reset
        };

        let icon = if thread.has_pending {
            "⟳"
        } else if thread.resolved {
            "✓"
        } else {
            "●"
        };

        let label = format!(
            " {icon} {} comment{}",
            thread.comment_count,
            if thread.comment_count == 1 { "" } else { "s" }
        );
        let used = UnicodeWidthStr::width(label.as_str());
        let pad = width.saturating_sub(used);

        Line::from(vec![
            Span::styled(label, Style::default().fg(Color::Cyan).bg(bg)),
            Span::styled(" ".repeat(pad), Style::default().bg(bg)),
        ])
    }

    fn render_diff_line_row(
        &self,
        dl: &DiffLineData,
        idx: usize,
        width: usize,
        colors: &DiffColors,
    ) -> Line<'static> {
        let is_cursor = idx == self.diff_cursor;
        let is_selected = self.is_selected(idx);
        let col_w = self.gutter_width();

        let bg = if is_cursor {
            match dl.line_type {
                LineType::Add => colors.selected_add_bg,
                LineType::Delete => colors.selected_del_bg,
                LineType::HunkHeader => colors.selected_hunk_bg,
                LineType::Context => colors.cursor_bg,
            }
        } else if is_selected {
            match dl.line_type {
                LineType::Add => colors.selected_add_bg,
                LineType::Delete => colors.selected_del_bg,
                _ => colors.selected_ctx_bg,
            }
        } else {
            match dl.line_type {
                LineType::Add => colors.add_bg,
                LineType::Delete => colors.del_bg,
                LineType::HunkHeader => colors.hunk_bg,
                LineType::Context => Color::Reset,
            }
        };

        let bg_style = Style::default().bg(bg);
        let mut spans: Vec<Span> = Vec::new();

        match dl.line_type {
            LineType::HunkHeader => {
                let expanded = dl.content.replace('\t', "        ");
                spans.push(Span::styled(
                    expanded,
                    Style::default().fg(colors.hunk_fg).bg(bg),
                ));
            }
            LineType::Add => {
                let gutter_style = Style::default().fg(colors.add_fg).bg(bg);
                let marker_style = gutter_style.add_modifier(Modifier::BOLD);
                let old_gutter = " ".repeat(col_w);
                let new_gutter = dl
                    .new_line_no
                    .map(|n| format!("{n:>col_w$}"))
                    .unwrap_or_else(|| " ".repeat(col_w));
                spans.push(Span::styled(old_gutter, gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled(new_gutter, gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled("+", marker_style));
                for (hl_style, text) in &dl.highlighted {
                    spans.push(Span::styled(
                        text.clone(),
                        hl_style.bg(bg),
                    ));
                }
            }
            LineType::Delete => {
                let gutter_style = Style::default().fg(colors.del_fg).bg(bg);
                let marker_style = gutter_style.add_modifier(Modifier::BOLD);
                let old_gutter = dl
                    .old_line_no
                    .map(|n| format!("{n:>col_w$}"))
                    .unwrap_or_else(|| " ".repeat(col_w));
                let new_gutter = " ".repeat(col_w);
                spans.push(Span::styled(old_gutter, gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled(new_gutter, gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled("-", marker_style));
                for (hl_style, text) in &dl.highlighted {
                    spans.push(Span::styled(
                        text.clone(),
                        hl_style.bg(bg),
                    ));
                }
            }
            LineType::Context => {
                let gutter_style = Style::default().fg(colors.line_number_fg).bg(bg);
                let old_gutter = dl
                    .old_line_no
                    .map(|n| format!("{n:>col_w$}"))
                    .unwrap_or_else(|| " ".repeat(col_w));
                let new_gutter = dl
                    .new_line_no
                    .map(|n| format!("{n:>col_w$}"))
                    .unwrap_or_else(|| " ".repeat(col_w));
                spans.push(Span::styled(old_gutter, gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled(new_gutter, gutter_style));
                spans.push(Span::styled("  ", gutter_style));
                for (hl_style, text) in &dl.highlighted {
                    spans.push(Span::styled(
                        text.clone(),
                        hl_style.bg(bg),
                    ));
                }
            }
        }

        // Pad to exact width so background fills entire row
        let used = spans_width(&spans);
        let pad = width.saturating_sub(used);
        if pad > 0 {
            spans.push(Span::styled(" ".repeat(pad), bg_style));
        }

        // Overlay comment badge pill on the right edge
        if let Some(badge) = &dl.badge {
            overlay_badge(&mut spans, badge, bg, width, &self.dots_frame);
        }

        Line::from(spans)
    }

    fn compute_scrollbar(&self, vp_h: usize) -> (i32, i32) {
        let total = self.total_lines();
        if total <= vp_h || total == 0 {
            return (-1, 0);
        }
        let mut thumb_len = (vp_h * vp_h / total) as i32;
        if thumb_len < 1 {
            thumb_len = 1;
        }
        let scrollable = total - vp_h;
        let offset = self.viewport_offset.min(scrollable);
        let thumb_start =
            (offset * (vp_h as usize - thumb_len as usize) / scrollable) as i32;
        (thumb_start, thumb_len)
    }

    fn is_selected(&self, idx: usize) -> bool {
        if let Some(start) = self.selection_start {
            let (lo, hi) = if start <= self.diff_cursor {
                (start, self.diff_cursor)
            } else {
                (self.diff_cursor, start)
            };
            idx >= lo && idx <= hi
        } else {
            false
        }
    }
}
