pub mod file_list;
pub mod highlight_cache;
pub mod panel;
pub mod render_list;
pub mod search;

use ratatui::layout::{Constraint, Direction, Layout, Rect};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::Paragraph;
use ratatui::Frame;
use regex::Regex;
use unicode_width::UnicodeWidthStr;

use self::file_list::FileList;
use self::highlight_cache::HighlightManager;
use super::scroll::{ScrollState, Scrollable};
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
    pub current_filename: String,
    pub additions: i32,
    pub deletions: i32,
    pub help_line: Vec<(String, String)>,
    pub comment_counts: std::collections::HashMap<String, usize>,
    pub copilot_working_files: std::collections::HashSet<String>,
    pub pr_number: Option<u64>,
    pub pr_owner: String,
    pub pr_repo: String,
}

pub struct DiffViewer {
    pub scroll: ScrollState,
    pub render_list: RenderList,
    pub width: u16,
    pub height: u16,
    pub mode: DiffMode,
    pub search: search::SearchState,
    pub selection_start: Option<usize>,
    pub file_list: FileList,
    pub panel_focused: bool,
    pub(crate) highlights: HighlightManager,
    pub panel: panel::CommentPanel,
    pub dots_frame: usize,
    pub waiting_g: bool,
    pub pending_bracket: Option<char>,
}

fn spans_width(spans: &[Span]) -> usize {
    spans
        .iter()
        .map(|s| UnicodeWidthStr::width(s.content.as_ref()))
        .sum()
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
            scroll: ScrollState::new(),
            render_list: RenderList::new(),
            width: 0,
            height: 0,
            mode,
            search: search::SearchState::new(),
            selection_start: None,
            file_list: FileList::new(),
            panel_focused: false,
            highlights: HighlightManager::new(),
            panel: panel::CommentPanel::new(),
            dots_frame: 0,
            waiting_g: false,
            pending_bracket: None,
        }
    }

    pub fn viewport_height(&self) -> u16 {
        self.height.saturating_sub(CHROME_ROWS)
    }

    pub fn is_selected(&self, idx: usize) -> bool {
        if let Some(anchor) = self.selection_start {
            let lo = anchor.min(self.scroll.cursor);
            let hi = anchor.max(self.scroll.cursor);
            idx >= lo && idx <= hi
        } else {
            false
        }
    }

    pub fn resize(&mut self, width: u16, height: u16) {
        self.width = width;
        self.height = height;
        self.scroll.set_viewport_height(self.viewport_height() as usize);
    }

    pub fn total_lines(&self) -> usize {
        self.render_list.total_lines()
    }
}

impl Scrollable for DiffViewer {
    fn scroll_state_mut(&mut self) -> &mut ScrollState {
        &mut self.scroll
    }
    fn sync_scroll_total(&mut self) {
        self.scroll.set_total(self.total_lines());
    }
}

impl DiffViewer {
    pub fn set_diff_lines(
        &mut self,
        lines: Vec<DiffLineData>,
        filename: &str,
    ) {
        self.render_list = RenderList::from_diff_lines(lines);
        let total = self.total_lines();
        self.scroll.set_total(total);

        // Apply cached highlights immediately if available
        self.highlights.apply_cached(&mut self.render_list, filename);

        // Re-run search against the new file's content
        self.search.run_search(&self.render_list);
    }

    pub fn apply_highlights(
        &mut self,
        hl_new: Vec<Vec<(Style, String)>>,
        hl_old: Vec<Vec<(Style, String)>>,
        filename: &str,
    ) {
        self.highlights.apply(&mut self.render_list, hl_new, hl_old, filename);
    }

    pub fn clear_highlight_cache(&mut self) {
        self.highlights.clear();
    }

    pub fn set_files(&mut self, files: Vec<PullRequestFile>) {
        let _ = self.file_list.set_files(files);
        self.scroll.goto_top();
    }

    pub fn update_files(&mut self, files: Vec<PullRequestFile>) {
        let saved_cursor = self.scroll.cursor;
        let saved_offset = self.scroll.offset;
        match self.file_list.update_files(files) {
            file_list::UpdateResult::Preserved => {
                self.scroll.cursor = saved_cursor;
                self.scroll.offset = saved_offset;
            }
            file_list::UpdateResult::Reset => {
                self.scroll.goto_top();
            }
        }
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
        self.file_list.scroll.set_viewport_height(vp_h);
        self.file_list.scroll.set_total(self.file_list.entries.len());
        let (thumb_start, thumb_len) = {
            self.scroll.set_total(self.total_lines());
            self.scroll.set_viewport_height(vp_h);
            self.scroll.clamp();
            self.scroll.scrollbar()
        };

        // Pre-build panel lines if visible (side panel or fallback)
        // Reserve 1 col for scrollbar on the right edge of the panel
        let panel_total_lines;
        let panel_lines = if self.panel.visible {
            if let Some(pa) = panel_area {
                // side panel: │ + content + scrollbar
                let inner_w = (pa.width as usize).saturating_sub(2);
                if inner_w > 0 {
                    let lines = self.panel.build_lines(colors, vp_h, inner_w, Some(&self.highlights.highlighter));
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
                let lines = self.panel.build_lines(colors, vp_h, inner_w, Some(&self.highlights.highlighter));
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
            self.panel.scroll.set_viewport_height(vp_h);
            self.panel.scroll.set_total(panel_total_lines);
            // Panel is offset-only (no cursor). Keep cursor pinned to offset
            // so clamp() doesn't fight with cursor-based logic.
            self.panel.scroll.cursor = self.panel.scroll.offset;
            self.panel.scroll.scrollbar()
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
            let spinner_frame = BADGE_SPIN_FRAMES[self.dots_frame % BADGE_SPIN_FRAMES.len()];
            let tree_line = self.file_list.render_row(row, vp_h, tree_area.width as usize, colors, &info.comment_counts, &info.copilot_working_files, spinner_frame);
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
                let panel_line_idx = self.panel.scroll.offset + row;
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
                // Normal mode: render diff content or empty state
                let diff_line = if self.file_list.files.is_empty() && self.render_list.is_empty() {
                    let mid = vp_h / 2;
                    if row == mid {
                        let msg = if self.file_list.loaded {
                            "No changes"
                        } else {
                            "Loading…"
                        };
                        let msg_w = UnicodeWidthStr::width(msg);
                        let left_pad = diff_inner_w.saturating_sub(msg_w) / 2;
                        let right_pad = diff_inner_w.saturating_sub(left_pad + msg_w);
                        Line::from(vec![
                            Span::raw(" ".repeat(left_pad)),
                            Span::styled(msg, Style::default().fg(Color::DarkGray)),
                            Span::raw(" ".repeat(right_pad)),
                        ])
                    } else {
                        Line::from(Span::raw(" ".repeat(diff_inner_w)))
                    }
                } else {
                    self.render_diff_row(row, diff_inner_w, colors)
                };
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
                    let panel_line_idx = self.panel.scroll.offset + row;

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

        // Search popup overlay (only while typing)
        if self.search.active {
            use super::popup::{Popup, PopupPosition, search_popup_lines};
            let popup_lines = search_popup_lines(
                &self.search.query,
                self.search.active,
                self.search.match_count(),
                self.search.match_idx,
            );
            Popup::new("Search")
                .lines(popup_lines)
                .position(PopupPosition::TopThird)
                .border_color(colors.border_fg)
                .render(frame, area);
        }
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
        let tree_title_style = if self.file_list.focused { bright } else { dim };
        let diff_title_style = if !self.file_list.focused { bright } else { dim };

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

        // Build PR badge text (right-aligned in the tree pane)
        let (pr_text, pr_w) = if let Some(pr_num) = info.pr_number {
            let text = format!("PR#{pr_num}");
            let w = UnicodeWidthStr::width(text.as_str()) + 1; // +1 trailing space
            (Some(text), w)
        } else {
            (None, 0)
        };

        // Truncate branch name to fit alongside PR badge
        let branch = &info.branch_name;
        let min_gap = if pr_w > 0 { 1 } else { 0 };
        let max_w = tree_inner
            .saturating_sub(pr_w)
            .saturating_sub(min_gap)
            .saturating_sub(1) // leading space in branch text
            .max(4);

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

        let branch_text = format!(" {name}");
        let branch_w = UnicodeWidthStr::width(branch_text.as_str());

        let gap = tree_inner.saturating_sub(branch_w + pr_w);

        let mut spans = vec![
            Span::styled(branch_text, Style::default().fg(Color::DarkGray)),
            Span::raw(" ".repeat(gap)),
        ];

        if let Some(ref text) = pr_text {
            let style = Style::default().fg(Color::Cyan);
            spans.push(Span::styled(text.clone(), style));
            spans.push(Span::raw(" "));
        }

        spans.push(Span::styled("│", Style::default().fg(colors.chrome_fg)));

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

        if let (Some(pr_num), Some(text)) = (info.pr_number, &pr_text) {
            let url = format!(
                "https://github.com/{}/{}/pull/{}",
                info.pr_owner, info.pr_repo, pr_num
            );
            let pr_col = area.x + branch_w as u16 + gap as u16;
            super::hyperlink::inject_hyperlink(
                frame.buffer_mut(),
                pr_col,
                area.y,
                area.width.saturating_sub(pr_col - area.x),
                text,
                &url,
            );
        }
    }

    fn render_diff_row(
        &self,
        row: usize,
        width: usize,
        colors: &DiffColors,
    ) -> Line<'static> {
        let idx = self.scroll.offset + row;
        let item = match self.render_list.get(idx) {
            Some(item) => item,
            None => return Line::from(Span::raw(" ".repeat(width))),
        };

        match item {
            RenderItem::DiffLine(dl) => self.render_diff_line_row(dl, idx, width, colors),
        }
    }

    fn render_diff_line_row(
        &self,
        dl: &DiffLineData,
        idx: usize,
        width: usize,
        colors: &DiffColors,
    ) -> Line<'static> {
        let is_cursor = idx == self.scroll.cursor;
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
                if dl.highlighted.is_empty() {
                    let expanded = dl.content.replace('\t', "        ");
                    spans.push(Span::styled(expanded, bg_style));
                } else {
                    for (hl_style, text) in &dl.highlighted {
                        spans.push(Span::styled(
                            text.clone(),
                            hl_style.bg(bg),
                        ));
                    }
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
                if dl.highlighted.is_empty() {
                    let expanded = dl.content.replace('\t', "        ");
                    spans.push(Span::styled(expanded, bg_style));
                } else {
                    for (hl_style, text) in &dl.highlighted {
                        spans.push(Span::styled(
                            text.clone(),
                            hl_style.bg(bg),
                        ));
                    }
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
                if dl.highlighted.is_empty() {
                    let expanded = dl.content.replace('\t', "        ");
                    spans.push(Span::styled(expanded, bg_style));
                } else {
                    for (hl_style, text) in &dl.highlighted {
                        spans.push(Span::styled(
                            text.clone(),
                            hl_style.bg(bg),
                        ));
                    }
                }
            }
        }

        // Pad to exact width so background fills entire row
        let used = spans_width(&spans);
        let pad = width.saturating_sub(used);
        if pad > 0 {
            spans.push(Span::styled(" ".repeat(pad), bg_style));
        }

        // Apply search match highlighting
        if self.search.is_match(idx) {
            let is_current = self.search.is_current_match(idx);
            let search_bg = if is_current {
                colors.search_current_bg
            } else {
                colors.search_match_bg
            };
            if let Some(re) = &self.search.pattern {
                spans = highlight_search_in_spans(spans, &dl.content, re, search_bg, col_w);
            }
        }

        // Overlay comment badge pill on the right edge
        if let Some(badge) = &dl.badge {
            overlay_badge(&mut spans, badge, bg, width, &self.dots_frame);
        }

        Line::from(spans)
    }


    /// Build highlighted diff context lines for the comment panel.
    /// Renders the lines around the current cursor position (or selection range).
    pub fn panel_diff_context(&self, colors: &DiffColors) -> Vec<Line<'static>> {
        let total = self.render_list.len();
        if total == 0 {
            return Vec::new();
        }

        let (start_idx, end_idx) = if let Some(sel) = self.selection_start {
            let lo = sel.min(self.scroll.cursor);
            let hi = sel.max(self.scroll.cursor);
            (lo, hi)
        } else {
            (self.scroll.cursor, self.scroll.cursor)
        };

        let mut lines = Vec::new();
        let width = self.panel.width.saturating_sub(2) as usize; // inner panel width

        for idx in start_idx..=end_idx.min(total - 1) {
            if let Some(RenderItem::DiffLine(dl)) = self.render_list.get(idx) {
                lines.push(self.render_context_line(dl, width, colors));
            }
        }
        lines
    }

    /// Render a single diff line for panel context (no cursor/selection highlight).
    fn render_context_line(&self, dl: &DiffLineData, width: usize, colors: &DiffColors) -> Line<'static> {
        let col_w = self.gutter_width();
        let bg = match dl.line_type {
            LineType::Add => colors.add_bg,
            LineType::Delete => colors.del_bg,
            LineType::HunkHeader => colors.hunk_bg,
            LineType::Context => Color::Reset,
        };

        let mut spans: Vec<Span> = Vec::new();

        match dl.line_type {
            LineType::HunkHeader => {
                let expanded = dl.content.replace('\t', "        ");
                spans.push(Span::styled(expanded, Style::default().fg(colors.hunk_fg).bg(bg)));
            }
            LineType::Add => {
                let gutter_style = Style::default().fg(colors.add_fg).bg(bg);
                let marker_style = gutter_style.add_modifier(Modifier::BOLD);
                let new_gutter = dl.new_line_no
                    .map(|n| format!("{n:>col_w$}"))
                    .unwrap_or_else(|| " ".repeat(col_w));
                spans.push(Span::styled(" ".repeat(col_w), gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled(new_gutter, gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled("+", marker_style));
                for (hl_style, text) in &dl.highlighted {
                    spans.push(Span::styled(text.clone(), hl_style.bg(bg)));
                }
            }
            LineType::Delete => {
                let gutter_style = Style::default().fg(colors.del_fg).bg(bg);
                let marker_style = gutter_style.add_modifier(Modifier::BOLD);
                let old_gutter = dl.old_line_no
                    .map(|n| format!("{n:>col_w$}"))
                    .unwrap_or_else(|| " ".repeat(col_w));
                spans.push(Span::styled(old_gutter, gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled(" ".repeat(col_w), gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled("-", marker_style));
                for (hl_style, text) in &dl.highlighted {
                    spans.push(Span::styled(text.clone(), hl_style.bg(bg)));
                }
            }
            LineType::Context => {
                let gutter_style = Style::default().fg(colors.line_number_fg).bg(bg);
                let old_gutter = dl.old_line_no
                    .map(|n| format!("{n:>col_w$}"))
                    .unwrap_or_else(|| " ".repeat(col_w));
                let new_gutter = dl.new_line_no
                    .map(|n| format!("{n:>col_w$}"))
                    .unwrap_or_else(|| " ".repeat(col_w));
                spans.push(Span::styled(old_gutter, gutter_style));
                spans.push(Span::styled(" ", gutter_style));
                spans.push(Span::styled(new_gutter, gutter_style));
                spans.push(Span::styled("  ", gutter_style));
                for (hl_style, text) in &dl.highlighted {
                    spans.push(Span::styled(text.clone(), hl_style.bg(bg)));
                }
            }
        }

        // Pad/truncate to width
        let used: usize = spans.iter().map(|s| UnicodeWidthStr::width(s.content.as_ref())).sum();
        if used < width {
            spans.push(Span::styled(" ".repeat(width - used), Style::default().bg(bg)));
        }

        Line::from(spans)
    }
}

/// Highlight search matches in the content portion of diff line spans.
/// `gutter_cols` is the number of leading spans to skip (gutter + marker).
fn highlight_search_in_spans(
    spans: Vec<Span<'static>>,
    content: &str,
    re: &Regex,
    hl_bg: Color,
    gutter_width: usize,
) -> Vec<Span<'static>> {
    // The gutter region = old_num + space + new_num + space + marker = gutter_width*2 + 3
    // But it's simpler to just find which spans contain content and split those.
    // Strategy: rebuild the span list, splitting any span whose text overlaps a match.

    // First, collect all match byte ranges in the content
    let matches: Vec<(usize, usize)> = re.find_iter(content).map(|m| (m.start(), m.end())).collect();
    if matches.is_empty() {
        return spans;
    }

    // Determine gutter span count: gutter_width*2 + 3 character columns before content
    let gutter_cols_total = gutter_width * 2 + 3;
    let mut result = Vec::with_capacity(spans.len() + matches.len() * 2);
    let mut char_offset: usize = 0; // character position in the full rendered line

    for span in spans {
        let span_w = UnicodeWidthStr::width(span.content.as_ref());
        let span_start = char_offset;
        let span_end = char_offset + span_w;
        char_offset = span_end;

        // If this span is entirely in the gutter region, pass through
        if span_end <= gutter_cols_total {
            result.push(span);
            continue;
        }

        // For content spans, compute offset into `content` string
        let content_start = span_start.saturating_sub(gutter_cols_total);
        let content_end = span_end.saturating_sub(gutter_cols_total);

        // Find matches that overlap this span's content range
        let overlapping: Vec<(usize, usize)> = matches
            .iter()
            .filter(|(ms, me)| *ms < content_end && *me > content_start)
            .map(|(ms, me)| {
                let s = ms.saturating_sub(content_start);
                let e = (*me - content_start).min(content_end - content_start);
                (s, e)
            })
            .collect();

        if overlapping.is_empty() {
            result.push(span);
            continue;
        }

        // Split the span text at match boundaries
        let text = span.content.to_string();
        let base_style = span.style;
        let hl_style = Style::default().fg(Color::Black).bg(hl_bg);
        let chars: Vec<char> = text.chars().collect();
        let mut pos = 0usize;

        for (ms, me) in &overlapping {
            if *ms > pos {
                let before: String = chars[pos..*ms].iter().collect();
                result.push(Span::styled(before, base_style));
            }
            let matched: String = chars[*ms..*me].iter().collect();
            result.push(Span::styled(matched, hl_style));
            pos = *me;
        }
        if pos < chars.len() {
            let after: String = chars[pos..].iter().collect();
            result.push(Span::styled(after, base_style));
        }
    }

    result
}
