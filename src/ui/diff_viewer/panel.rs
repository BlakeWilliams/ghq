use unicode_width::{UnicodeWidthStr, UnicodeWidthChar};
use unicode_segmentation::UnicodeSegmentation;
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use std::time::{SystemTime, UNIX_EPOCH};

use crate::ui::scroll::{ScrollState, Scrollable};
use crate::ui::styles::DiffColors;
use crate::ui::copilot_state::{ContentBlock, ToolGroup, ToolStatus};

// Our ANSI-palette-derived stylesheet for tui-markdown.
#[derive(Clone)]
struct CommentStyleSheet;

impl tui_markdown::StyleSheet for CommentStyleSheet {
    fn heading(&self, level: u8) -> Style {
        match level {
            1 => Style::new().fg(Color::Cyan).add_modifier(Modifier::BOLD | Modifier::UNDERLINED),
            2 => Style::new().fg(Color::Cyan).add_modifier(Modifier::BOLD),
            3 => Style::new().fg(Color::Cyan).add_modifier(Modifier::BOLD | Modifier::ITALIC),
            _ => Style::new().fg(Color::DarkGray).add_modifier(Modifier::ITALIC),
        }
    }

    fn code(&self) -> Style {
        Style::new().fg(Color::Yellow)
    }

    fn link(&self) -> Style {
        Style::new().fg(Color::Blue).add_modifier(Modifier::UNDERLINED)
    }

    fn blockquote(&self) -> Style {
        Style::new().fg(Color::DarkGray)
    }

    fn heading_meta(&self) -> Style {
        Style::new().dim()
    }

    fn metadata_block(&self) -> Style {
        Style::new().fg(Color::DarkGray)
    }
}

fn md_options() -> tui_markdown::Options<CommentStyleSheet> {
    tui_markdown::Options::new(CommentStyleSheet)
}

/// Word-wrap a styled Line to fit within `max_width` visible columns.
/// Preserves styling across wrap boundaries. Returns one or more wrapped lines,
/// each prefixed with a single space for panel padding.
fn wrap_styled_line(line: ratatui::text::Line<'static>, max_width: usize) -> Vec<Line<'static>> {
    if max_width == 0 {
        return vec![line];
    }

    // Flatten all spans into (char, style) pairs for easier wrapping
    let mut chars: Vec<(char, Style)> = Vec::new();
    for span in &line.spans {
        for c in span.content.chars() {
            chars.push((c, span.style));
        }
    }

    let mut result: Vec<Line<'static>> = Vec::new();
    let mut pos = 0;
    let pad = 1_usize; // leading space

    while pos < chars.len() {
        let avail = max_width.saturating_sub(pad);
        let mut line_end = pos;
        let mut w = 0_usize;

        // Measure how many chars fit
        while line_end < chars.len() && chars[line_end].0 != '\n' {
            let cw = UnicodeWidthChar::width(chars[line_end].0).unwrap_or(0);
            if w + cw > avail {
                break;
            }
            w += cw;
            line_end += 1;
        }

        // If we stopped mid-line, try to break at a word boundary
        if line_end < chars.len() && chars[line_end].0 != '\n' && line_end > pos {
            let mut break_at = line_end;
            while break_at > pos && !chars[break_at - 1].0.is_whitespace() {
                break_at -= 1;
            }
            if break_at > pos {
                line_end = break_at;
            }
        }

        // Build spans for this wrapped line
        let mut spans: Vec<Span<'static>> = vec![Span::raw(" ")];
        let mut cur_style = None;
        let mut cur_text = String::new();
        for &(c, style) in &chars[pos..line_end] {
            if cur_style == Some(style) {
                cur_text.push(c);
            } else {
                if !cur_text.is_empty() {
                    spans.push(Span::styled(cur_text, cur_style.unwrap_or_default()));
                    cur_text = String::new();
                }
                cur_style = Some(style);
                cur_text.push(c);
            }
        }
        if !cur_text.is_empty() {
            spans.push(Span::styled(cur_text, cur_style.unwrap_or_default()));
        }
        result.push(Line::from(spans));

        pos = line_end;
        // Skip newline char
        if pos < chars.len() && chars[pos].0 == '\n' {
            pos += 1;
        }
        // Skip leading whitespace on continuation lines
        if pos < chars.len() && pos > 0 {
            while pos < chars.len() && chars[pos].0 == ' ' {
                pos += 1;
            }
        }
    }

    if result.is_empty() {
        result.push(Line::from(Span::raw(" ")));
    }
    result
}

/// Render a markdown string into panel lines. Each returned line has inline
/// styling from tui-markdown, wrapped to fit within max_width.
/// If a Highlighter is provided, fenced code blocks are syntax-highlighted.
fn render_markdown(body: &str, max_width: usize, highlighter: Option<&crate::ui::highlight::Highlighter>) -> Vec<Line<'static>> {
    let owned_body = body.to_string();
    let text = tui_markdown::from_str_with_options(&owned_body, &md_options());

    // Own all spans so lines are 'static
    let mut raw_lines: Vec<Line<'static>> = text
        .lines
        .into_iter()
        .map(|line| {
            Line::from(
                line.spans
                    .into_iter()
                    .map(|s| Span::styled(s.content.to_string(), s.style))
                    .collect::<Vec<_>>(),
            )
        })
        .collect();

    // tui-markdown emits list markers ("1. ", "- ") on their own line with
    // the item text on the next line. Merge them.
    let mut merged: Vec<Line<'static>> = Vec::with_capacity(raw_lines.len());
    let mut i = 0;
    while i < raw_lines.len() {
        if i + 1 < raw_lines.len() && is_list_marker_line(&raw_lines[i]) {
            let mut spans = std::mem::take(&mut raw_lines[i].spans);
            spans.extend(std::mem::take(&mut raw_lines[i + 1].spans));
            merged.push(Line::from(spans));
            i += 2;
        } else {
            merged.push(std::mem::take(&mut raw_lines[i]));
            i += 1;
        }
    }

    // Syntax-highlight fenced code blocks using our Highlighter.
    // tui-markdown emits ```lang and ``` as literal span content.
    if let Some(hl) = highlighter {
        let mut highlighted: Vec<Line<'static>> = Vec::with_capacity(merged.len());
        let mut in_code = false;
        let mut code_lang = String::new();
        let mut code_buf = String::new();
        let mut fence_line: Option<Line<'static>> = None;

        for line in merged {
            let text: String = line.spans.iter().map(|s| s.content.as_ref()).collect();
            let trimmed = text.trim();

            if !in_code && trimmed.starts_with("```") {
                in_code = true;
                code_lang = trimmed.trim_start_matches('`').to_string();
                code_buf.clear();
                fence_line = Some(line);
            } else if in_code && trimmed == "```" {
                // Highlight collected code block
                if !code_lang.is_empty() && code_lang != "text" && code_lang != "plain" {
                    let hl_lines = hl.highlight_code_block(&code_buf, &code_lang);
                    // Emit opening fence
                    if let Some(fl) = fence_line.take() {
                        highlighted.push(fl);
                    }
                    for spans in hl_lines {
                        let rspans: Vec<Span<'static>> = spans
                            .into_iter()
                            .map(|(style, text)| Span::styled(text, style))
                            .collect();
                        highlighted.push(Line::from(rspans));
                    }
                } else {
                    // No language — emit as-is with code style
                    if let Some(fl) = fence_line.take() {
                        highlighted.push(fl);
                    }
                    for code_line in code_buf.lines() {
                        highlighted.push(Line::from(Span::styled(
                            code_line.to_string(),
                            Style::default().fg(Color::Yellow),
                        )));
                    }
                }
                highlighted.push(line); // closing fence
                in_code = false;
            } else if in_code {
                code_buf.push_str(&text);
                code_buf.push('\n');
            } else {
                highlighted.push(line);
            }
        }

        // If we ended mid-fence (streaming), highlight what we have so far
        if in_code {
            if let Some(fl) = fence_line.take() {
                highlighted.push(fl);
            }
            if !code_lang.is_empty() && code_lang != "text" && code_lang != "plain" {
                let hl_lines = hl.highlight_code_block(&code_buf, &code_lang);
                for spans in hl_lines {
                    let rspans: Vec<Span<'static>> = spans
                        .into_iter()
                        .map(|(style, text)| Span::styled(text, style))
                        .collect();
                    highlighted.push(Line::from(rspans));
                }
            } else {
                for code_line in code_buf.lines() {
                    highlighted.push(Line::from(Span::styled(
                        code_line.to_string(),
                        Style::default().fg(Color::Yellow),
                    )));
                }
            }
        }

        merged = highlighted;
    }

    let mut result = Vec::new();
    for line in merged {
        result.extend(wrap_styled_line(line, max_width));
    }
    result
}

/// Returns true if a line contains only a list marker (e.g. "  1. " or " - ").
fn is_list_marker_line(line: &Line) -> bool {
    let text: String = line.spans.iter().map(|s| s.content.as_ref()).collect();
    let trimmed = text.trim();
    if trimmed.is_empty() {
        return false;
    }
    // Ordered: "1." or "12."
    if let Some(rest) = trimmed.strip_suffix('.') {
        return rest.chars().all(|c| c.is_ascii_digit());
    }
    // Unordered: just "-"
    trimmed == "-"
}

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

/// Truncate a string to fit within `max_width` visible columns,
/// appending "…" if truncated.
fn truncate_to_width(s: &str, max_width: usize) -> String {
    use unicode_width::UnicodeWidthChar;
    let total_w: usize = s.chars().map(|c| UnicodeWidthChar::width(c).unwrap_or(0)).sum();
    if total_w <= max_width {
        return s.to_string();
    }
    let target = max_width.saturating_sub(1); // room for "…"
    let mut w = 0;
    let mut result = String::new();
    for c in s.chars() {
        let cw = UnicodeWidthChar::width(c).unwrap_or(0);
        if w + cw > target {
            break;
        }
        result.push(c);
        w += cw;
    }
    result.push('…');
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

/// Convert an ISO 8601 or epoch timestamp to a relative time string.
fn relative_time(created_at: &str) -> String {
    let now_secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();

    // Try parsing as epoch seconds first, then ISO 8601
    let created_secs = if let Ok(s) = created_at.parse::<u64>() {
        s
    } else if let Some(s) = parse_iso8601(created_at) {
        s
    } else {
        return String::new();
    };

    let elapsed = now_secs.saturating_sub(created_secs);
    let minutes = elapsed / 60;
    let hours = elapsed / 3600;
    let days = elapsed / 86400;
    let months = days / 30;

    match () {
        _ if elapsed < 60 => "just now".to_string(),
        _ if minutes == 1 => "1m ago".to_string(),
        _ if hours == 0 => format!("{minutes}m ago"),
        _ if hours == 1 => "1h ago".to_string(),
        _ if days == 0 => format!("{hours}h ago"),
        _ if days == 1 => "1d ago".to_string(),
        _ if months == 0 => format!("{days}d ago"),
        _ if months == 1 => "1mo ago".to_string(),
        _ => format!("{months}mo ago"),
    }
}

/// Minimal ISO 8601 parser → epoch seconds.
fn parse_iso8601(s: &str) -> Option<u64> {
    // Format: "2024-01-15T10:30:00Z" or "2024-01-15T10:30:00+00:00"
    let s = s.trim();
    if s.len() < 19 {
        return None;
    }
    let year: u64 = s[0..4].parse().ok()?;
    let month: u64 = s[5..7].parse().ok()?;
    let day: u64 = s[8..10].parse().ok()?;
    let hour: u64 = s[11..13].parse().ok()?;
    let min: u64 = s[14..16].parse().ok()?;
    let sec: u64 = s[17..19].parse().ok()?;

    // Rough days-from-epoch calculation (good enough for relative time)
    let mut total_days: u64 = 0;
    for y in 1970..year {
        total_days += if y % 4 == 0 && (y % 100 != 0 || y % 400 == 0) { 366 } else { 365 };
    }
    let month_days = [0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334];
    total_days += month_days.get(month.saturating_sub(1) as usize).copied().unwrap_or(0);
    if month > 2 && year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) {
        total_days += 1;
    }
    total_days += day.saturating_sub(1);

    Some(total_days * 86400 + hour * 3600 + min * 60 + sec)
}

#[derive(Clone)]
pub struct PanelComment {
    pub author: String,
    pub is_copilot: bool,
    pub blocks: Vec<ContentBlock>,
    pub is_pending: bool,
    pub created_at: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ReplyMode {
    GitHub,
    Copilot,
}

pub struct CommentPanel {
    pub visible: bool,
    pub scroll: ScrollState,
    pub width: u16,
    pub thread_key: Option<String>,
    pub comments: Vec<PanelComment>,
    pub reply_view: Option<String>,
    pub reply_cursor: usize,
    pub reply_mode: ReplyMode,
    pub file_path: String,
    pub diff_context: Vec<Line<'static>>,
    pub resolved: bool,
    pub panel_line: i32,
    md_cache: std::collections::HashMap<(String, usize), Vec<Line<'static>>>,
    cached_lines: Option<Vec<PanelLine>>,
    cached_lines_width: usize,
}

const MIN_PANEL_WIDTH: u16 = 55;
const MAX_PANEL_WIDTH: u16 = 100;
const DIFF_MIN_WIDTH: u16 = 90;

impl Scrollable for CommentPanel {
    fn scroll_state_mut(&mut self) -> &mut ScrollState {
        &mut self.scroll
    }
}

impl CommentPanel {
    pub fn new() -> Self {
        Self {
            visible: false,
            scroll: ScrollState::new(),
            width: MIN_PANEL_WIDTH,
            thread_key: None,
            comments: Vec::new(),
            reply_view: None,
            reply_cursor: 0,
            reply_mode: ReplyMode::Copilot,
            file_path: String::new(),
            diff_context: Vec::new(),
            resolved: false,
            panel_line: 0,
            md_cache: std::collections::HashMap::new(),
            cached_lines: None,
            cached_lines_width: 0,
        }
    }

    pub fn open_thread(&mut self, thread_key: String, comments: Vec<PanelComment>, file_path: String, diff_context: Vec<Line<'static>>) {
        let changed_thread = self.thread_key.as_deref() != Some(&thread_key);
        self.visible = true;
        self.thread_key = Some(thread_key);
        self.comments = comments;
        self.file_path = file_path;
        self.diff_context = diff_context;
        self.cached_lines = None;
        if changed_thread {
            self.scroll.scroll_to_bottom();
            self.md_cache.clear();
        }
    }

    /// Compute panel width for side-panel mode. Returns 0 if the terminal
    /// is too narrow and the fallback (full-width) mode should be used.
    pub fn panel_width(available_right: u16) -> u16 {
        // available_right = total width minus tree width
        // Layout: tree_sep(1) | diff | scrollbar(1) | panel_sep(1) | panel
        // So inner = available_right - 1 (tree sep)
        // Then: panel = inner - DIFF_MIN_WIDTH - 2 (scroll + panel_sep)
        let available = available_right.saturating_sub(DIFF_MIN_WIDTH).saturating_sub(3);
        if available < MIN_PANEL_WIDTH {
            return 0; // too narrow, use fallback
        }
        available.min(MAX_PANEL_WIDTH)
    }

    pub fn set_reply_view(&mut self, text: String, cursor: usize, mode: ReplyMode) {
        let was_none = self.reply_view.is_none();
        self.reply_view = Some(text);
        self.reply_cursor = cursor;
        self.reply_mode = mode;
        self.cached_lines = None;
        if was_none {
            self.scroll.pending_bottom = true;
        }
    }

    pub fn clear_reply_view(&mut self) {
        self.reply_view = None;
        self.cached_lines = None;
    }

    pub fn close(&mut self) {
        self.visible = false;
        self.thread_key = None;
        self.comments.clear();
        self.reply_view = None;
        self.file_path.clear();
        self.diff_context.clear();
        self.resolved = false;
        self.panel_line = 0;
        self.scroll = ScrollState::new();
        self.md_cache.clear();
        self.cached_lines = None;
    }

    pub fn build_lines(&mut self, colors: &DiffColors, viewport_height: usize, inner_width: usize, highlighter: Option<&crate::ui::highlight::Highlighter>) -> Vec<PanelLine> {
        if let Some(ref cached) = self.cached_lines {
            if self.cached_lines_width == inner_width {
                return cached.clone();
            }
        }
        let lines = self.build_lines_inner(colors, viewport_height, inner_width, highlighter);
        self.cached_lines = Some(lines.clone());
        self.cached_lines_width = inner_width;
        lines
    }

    fn build_lines_inner(&mut self, colors: &DiffColors, viewport_height: usize, inner_width: usize, highlighter: Option<&crate::ui::highlight::Highlighter>) -> Vec<PanelLine> {
        let mut lines = Vec::new();
        let w = inner_width;
        let body_wrap_w = w.saturating_sub(1);

        // File path header (bold)
        if !self.file_path.is_empty() {
            let header = format!(" {}", self.file_path);
            for wl in wrap_text(&header, w) {
                lines.push(PanelLine::styled(&wl, colors.context_fg, true));
            }
        }

        // Diff context preview (highlighted)
        if !self.diff_context.is_empty() {
            for ctx_line in &self.diff_context {
                lines.push(PanelLine::rich(ctx_line.clone()));
            }
            lines.push(PanelLine::styled(
                &"─".repeat(w),
                colors.chrome_fg,
                false,
            ));
        }

        // Blank line before comments
        lines.push(PanelLine::empty());

        // Resolved indicator
        if self.resolved {
            lines.push(PanelLine::styled(" ✓ Resolved", Color::Cyan, false));
            lines.push(PanelLine::empty());
        }

        // Comments
        if !self.comments.is_empty() {
            for (i, comment) in self.comments.iter().enumerate() {
                // Author line: " @author · 2h ago" or " ✦ @copilot · 2h ago"
                let rel = relative_time(&comment.created_at);
                let author_color = if comment.is_copilot {
                    Color::Cyan
                } else {
                    nick_color(&comment.author)
                };

                let author_display = format!("@{}", comment.author);
                let author_style = if comment.is_copilot {
                    Style::default().fg(author_color).add_modifier(Modifier::BOLD)
                } else {
                    Style::default()
                        .fg(author_color)
                        .add_modifier(Modifier::BOLD | Modifier::UNDERLINED)
                };

                let mut header_spans: Vec<Span<'static>> = Vec::new();
                header_spans.push(Span::raw(" "));
                if comment.is_copilot {
                    header_spans.push(Span::styled("✦ ", Style::default().fg(Color::Cyan)));
                }
                header_spans.push(Span::styled(author_display, author_style));
                if !rel.is_empty() {
                    header_spans.push(Span::styled(
                        format!(" · {rel}"),
                        Style::default().fg(Color::DarkGray),
                    ));
                }
                lines.push(PanelLine::rich(Line::from(header_spans)));

                // Content blocks — interleaved text and tool groups
                if comment.blocks.is_empty() && comment.is_pending {
                    lines.push(PanelLine::text(" "));
                } else {
                    for block in &comment.blocks {
                        match block {
                            ContentBlock::Text(text) => {
                                if !text.is_empty() {
                                    let cache_key = (text.clone(), body_wrap_w);
                                    let md_lines = if comment.is_pending {
                                        render_markdown(text, body_wrap_w, highlighter)
                                    } else if let Some(cached) = self.md_cache.get(&cache_key) {
                                        cached.clone()
                                    } else {
                                        let rendered = render_markdown(text, body_wrap_w, highlighter);
                                        self.md_cache.insert(cache_key, rendered.clone());
                                        rendered
                                    };
                                    for ml in md_lines {
                                        lines.push(PanelLine::rich(ml));
                                    }
                                }
                            }
                            ContentBlock::ToolGroup(group) => {
                                self.build_tool_group_lines(&mut lines, group, w, colors);
                            }
                        }
                    }
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

            if reply.is_empty() {
                lines.push(PanelLine::rich(Line::from(vec![
                    Span::raw(" "),
                    Span::styled("▍", Style::default().fg(Color::White)),
                ])));
            } else {
                let cursor = self.reply_cursor.min(reply.len());
                let before_cursor = &reply[..cursor];
                let pre_lines: Vec<&str> = before_cursor.split('\n').collect();
                let cursor_input_line = pre_lines.len() - 1;
                let cursor_col_bytes = pre_lines.last().unwrap_or(&"").len();

                let input_lines: Vec<&str> = reply.split('\n').collect();
                let mut visual_line_idx = 0;
                // Track which visual line gets the cursor
                let mut cursor_visual_line = 0;
                let mut cursor_visual_col = 0;

                // First pass: figure out cursor visual position
                for (i, iline) in input_lines.iter().enumerate() {
                    let wrapped = wrap_text(iline, body_wrap_w);
                    if i == cursor_input_line {
                        let mut bytes_left = cursor_col_bytes;
                        for (wi, wl) in wrapped.iter().enumerate() {
                            if bytes_left <= wl.len() {
                                cursor_visual_line = visual_line_idx + wi;
                                cursor_visual_col = bytes_left;
                                break;
                            }
                            bytes_left -= wl.len();
                            // Skip whitespace consumed by wrapping
                            let rest = &iline[cursor_col_bytes - bytes_left..];
                            if rest.starts_with(' ') {
                                bytes_left = bytes_left.saturating_sub(1);
                            }
                        }
                    }
                    visual_line_idx += wrapped.len();
                }

                // Second pass: render with cursor
                visual_line_idx = 0;
                for iline in &input_lines {
                    let wrapped = wrap_text(iline, body_wrap_w);
                    for wl in &wrapped {
                        if visual_line_idx == cursor_visual_line {
                            let before_part = &wl[..cursor_visual_col];
                            let after_part = &wl[cursor_visual_col..];
                            if after_part.is_empty() {
                                lines.push(PanelLine::rich(Line::from(vec![
                                    Span::raw(format!(" {before_part}")),
                                    Span::styled("▍", Style::default().fg(Color::White)),
                                ])));
                            } else {
                                let mut p = 1;
                                while p < after_part.len() && !after_part.is_char_boundary(p) {
                                    p += 1;
                                }
                                let cursor_char = &after_part[..p];
                                let rest = &after_part[p..];
                                lines.push(PanelLine::rich(Line::from(vec![
                                    Span::raw(format!(" {before_part}")),
                                    Span::styled(
                                        cursor_char.to_string(),
                                        Style::default().fg(Color::Black).bg(Color::White),
                                    ),
                                    Span::raw(rest.to_string()),
                                ])));
                            }
                        } else {
                            lines.push(PanelLine::text(&format!(" {wl}")));
                        }
                        visual_line_idx += 1;
                    }
                }
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
        // top border + content line(s) + bottom border
        if reply.is_empty() {
            2 + 1
        } else {
            let input_lines: Vec<&str> = reply.split('\n').collect();
            let mut count = 0;
            for iline in &input_lines {
                count += wrap_text(iline, body_wrap_w).len();
            }
            2 + count
        }
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
            let max_label_w = sub_w.saturating_sub(4); // ╭ + space + space + ╮
            let label_raw = format!(" {} ", group.label);
            let label = truncate_to_width(&label_raw, max_label_w);
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
            let text = if !tc.args_summary.is_empty() {
                format!("{icon} {name} {}", tc.args_summary)
            } else {
                format!("{icon} {name}")
            };

            // Truncate to fit inside the box borders
            let content = truncate_to_width(&text, inner_w);
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
}

impl Default for CommentPanel {
    fn default() -> Self {
        Self::new()
    }
}

#[derive(Clone)]
pub struct PanelLine {
    pub line: Line<'static>,
}

impl PanelLine {
    pub fn text(s: &str) -> Self {
        Self {
            line: Line::from(Span::raw(s.to_string())),
        }
    }

    pub fn styled(s: &str, color: Color, bold: bool) -> Self {
        let mut style = Style::default();
        if color != Color::Reset {
            style = style.fg(color);
        }
        if bold {
            style = style.add_modifier(Modifier::BOLD);
        }
        Self {
            line: Line::from(Span::styled(s.to_string(), style)),
        }
    }

    pub fn rich(line: Line<'static>) -> Self {
        Self { line }
    }

    pub fn empty() -> Self {
        Self {
            line: Line::from(""),
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
    fn open_thread_scrolls_to_bottom_on_new_thread() {
        let mut panel = CommentPanel::new();
        panel.scroll.offset = 10;
        panel.open_thread("t1".to_string(), Vec::new(), "f.rs".to_string(), Vec::new());
        // New thread sets pending_bottom (resolved at render time when total is known)
        assert!(panel.scroll.pending_bottom);

        // Same thread should preserve scroll
        panel.scroll.pending_bottom = false;
        panel.scroll.offset = 5;
        panel.open_thread("t1".to_string(), Vec::new(), "f.rs".to_string(), Vec::new());
        assert_eq!(panel.scroll.offset, 5);
        assert!(!panel.scroll.pending_bottom);

        // Different thread scrolls to bottom again
        panel.open_thread("t2".to_string(), Vec::new(), "f.rs".to_string(), Vec::new());
        assert!(panel.scroll.pending_bottom);
    }
}
