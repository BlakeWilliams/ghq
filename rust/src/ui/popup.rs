use ratatui::layout::Rect;
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Clear, Paragraph};
use ratatui::Frame;
use unicode_width::UnicodeWidthStr;

/// Positioning bias for the popup within the container area.
#[derive(Clone, Copy)]
pub enum PopupPosition {
    /// Biased toward top-third (1/3 from top), like Go's picker/search.
    TopThird,
    /// Vertically centered.
    Center,
}

/// A rendered popup ready to be overlaid on the screen.
pub struct Popup<'a> {
    pub title: &'a str,
    pub lines: Vec<Line<'a>>,
    pub position: PopupPosition,
    pub border_color: Color,
    /// Min width for the modal (content area, excluding borders).
    pub min_width: u16,
}

impl<'a> Popup<'a> {
    pub fn new(title: &'a str) -> Self {
        Self {
            title,
            lines: Vec::new(),
            position: PopupPosition::TopThird,
            border_color: Color::DarkGray,
            min_width: 36,
        }
    }

    pub fn lines(mut self, lines: Vec<Line<'a>>) -> Self {
        self.lines = lines;
        self
    }

    pub fn position(mut self, position: PopupPosition) -> Self {
        self.position = position;
        self
    }

    pub fn border_color(mut self, color: Color) -> Self {
        self.border_color = color;
        self
    }

    pub fn min_width(mut self, w: u16) -> Self {
        self.min_width = w;
        self
    }

    /// Render the popup as a centered overlay within `area`.
    pub fn render(self, frame: &mut Frame, area: Rect) {
        let area_w = area.width as usize;
        let area_h = area.height as usize;

        // Modal width: half the area, clamped to [min_width+4, area-4].
        // +4 accounts for border chars + padding on each side.
        let half = area_w / 2;
        let min_total = (self.min_width as usize) + 4;
        let max_total = area_w.saturating_sub(4);
        let modal_w = half.max(min_total).min(max_total).max(6);
        let inner_w = modal_w.saturating_sub(4); // border + 1 space padding each side

        // Modal height: borders + content lines.
        let content_h = self.lines.len();
        let modal_h = content_h + 2; // top border + bottom border

        // Vertical position.
        let pad_y = match self.position {
            PopupPosition::TopThird => {
                let p = area_h.saturating_sub(modal_h) / 3;
                p.max(1)
            }
            PopupPosition::Center => {
                area_h.saturating_sub(modal_h) / 2
            }
        };

        let pad_x = area_w.saturating_sub(modal_w) / 2;
        let bc = Style::default().fg(self.border_color);

        // Top border: ╭─ Title ─────╮
        let title_str = if self.title.is_empty() {
            String::new()
        } else {
            format!(" {} ", self.title)
        };
        let title_w = UnicodeWidthStr::width(title_str.as_str());
        let fill_w = modal_w.saturating_sub(3 + title_w); // "╭─" = 2 cols, "╮" = 1 col
        let mut top_spans = vec![
            Span::styled("╭─", bc),
            Span::styled(title_str, Style::default().add_modifier(Modifier::BOLD)),
            Span::styled("─".repeat(fill_w), bc),
            Span::styled("╮", bc),
        ];
        pad_line(&mut top_spans, modal_w);

        // Bottom border: ╰──────────╯
        let bottom_fill = modal_w.saturating_sub(2);
        let mut bottom_spans = vec![
            Span::styled("╰", bc),
            Span::styled("─".repeat(bottom_fill), bc),
            Span::styled("╯", bc),
        ];
        pad_line(&mut bottom_spans, modal_w);

        // Render: clear + top border
        let modal_rect = Rect::new(
            area.x + pad_x as u16,
            area.y + pad_y as u16,
            modal_w as u16,
            modal_h as u16,
        );
        frame.render_widget(Clear, modal_rect);

        let mut row = 0u16;
        // Top border
        frame.render_widget(
            Paragraph::new(Line::from(top_spans)),
            Rect::new(modal_rect.x, modal_rect.y + row, modal_rect.width, 1),
        );
        row += 1;

        // Content lines
        for line in &self.lines {
            let content_w: usize = line.spans.iter()
                .map(|s| UnicodeWidthStr::width(s.content.as_ref()))
                .sum();
            let pad = inner_w.saturating_sub(content_w);

            let mut spans = vec![Span::styled("│ ", bc)];
            for s in &line.spans {
                spans.push(s.clone());
            }
            if pad > 0 {
                spans.push(Span::raw(" ".repeat(pad)));
            }
            spans.push(Span::styled(" │", bc));

            frame.render_widget(
                Paragraph::new(Line::from(spans)),
                Rect::new(modal_rect.x, modal_rect.y + row, modal_rect.width, 1),
            );
            row += 1;
        }

        // Bottom border
        frame.render_widget(
            Paragraph::new(Line::from(bottom_spans)),
            Rect::new(modal_rect.x, modal_rect.y + row, modal_rect.width, 1),
        );
    }
}

fn pad_line(spans: &mut Vec<Span<'_>>, target_w: usize) {
    let w: usize = spans.iter().map(|s| UnicodeWidthStr::width(s.content.as_ref())).sum();
    if w < target_w {
        spans.push(Span::raw(" ".repeat(target_w - w)));
    }
}

/// Build the content lines for the search popup.
pub fn search_popup_lines<'a>(
    query: &str,
    active: bool,
    match_count: usize,
    match_idx: Option<usize>,
) -> Vec<Line<'a>> {
    let prompt = Style::default().fg(Color::Magenta).add_modifier(Modifier::BOLD);

    let mut spans: Vec<Span> = vec![
        Span::styled("/", prompt),
        Span::raw(" "),
        Span::styled(query.to_string(), Style::default().fg(Color::White)),
        Span::raw(" "),
        Span::styled("/", prompt),
    ];

    if active {
        if match_count > 0 {
            spans.push(Span::styled(
                format!("  {match_count} matches"),
                Style::default().fg(Color::DarkGray),
            ));
        }
    } else if let Some(idx) = match_idx {
        spans.push(Span::styled(
            format!("  {}/{match_count}", idx + 1),
            Style::default().fg(Color::DarkGray),
        ));
    } else if match_count > 0 {
        spans.push(Span::styled(
            format!("  {match_count} matches"),
            Style::default().fg(Color::DarkGray),
        ));
    }

    vec![Line::from(spans)]
}

/// Build the content lines for the picker popup.
pub fn picker_popup_lines<'a>(
    query: &str,
    items: &[(String, String, Vec<usize>, bool)], // (label, description, match_positions, is_cursor)
    total: usize,
    filtered: usize,
) -> Vec<Line<'a>> {
    let prompt = Style::default().fg(Color::Magenta).add_modifier(Modifier::BOLD);
    let count_style = Style::default().fg(Color::DarkGray);
    let pointer_style = Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD);
    let selected_style = Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD);
    let normal_style = Style::default();
    let desc_style = Style::default().fg(Color::DarkGray);

    let mut lines = Vec::new();

    // Input line
    lines.push(Line::from(vec![
        Span::styled("> ", prompt),
        Span::styled(format!("{query}█"), normal_style),
    ]));

    // Count line
    lines.push(Line::from(vec![
        Span::styled(format!("  {filtered}/{total}"), count_style),
    ]));

    // Item lines
    for (label, description, match_positions, is_cursor) in items {
        let mut spans: Vec<Span> = Vec::new();

        if *is_cursor {
            spans.push(Span::styled("█ ", pointer_style));
        } else {
            spans.push(Span::raw("  "));
        }

        // Render label with match highlighting
        let label_style = if *is_cursor { selected_style } else { normal_style };
        let hl_style = if *is_cursor {
            Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD | Modifier::UNDERLINED)
        } else {
            Style::default().fg(Color::Yellow).add_modifier(Modifier::BOLD)
        };

        let chars: Vec<char> = label.chars().collect();
        let mut pos = 0;
        for &mp in match_positions {
            if mp > pos && mp <= chars.len() {
                let before: String = chars[pos..mp].iter().collect();
                spans.push(Span::styled(before, label_style));
            }
            if mp < chars.len() {
                spans.push(Span::styled(chars[mp].to_string(), hl_style));
                pos = mp + 1;
            }
        }
        if pos < chars.len() {
            let rest: String = chars[pos..].iter().collect();
            spans.push(Span::styled(rest, label_style));
        }

        if !description.is_empty() {
            spans.push(Span::styled(format!("  {description}"), desc_style));
        }

        lines.push(Line::from(spans));
    }

    lines
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn search_popup_active_shows_count() {
        let lines = search_popup_lines("hello", true, 5, None);
        assert_eq!(lines.len(), 1);
        let text: String = lines[0].spans.iter().map(|s| s.content.to_string()).collect();
        assert!(text.contains("/"));
        assert!(text.contains("hello"));
        assert!(text.contains("5 matches"));
    }

    #[test]
    fn search_popup_confirmed_shows_position() {
        let lines = search_popup_lines("hello", false, 5, Some(2));
        let text: String = lines[0].spans.iter().map(|s| s.content.to_string()).collect();
        assert!(text.contains("3/5"));
    }

    #[test]
    fn picker_popup_renders_items() {
        let items = vec![
            ("file1.rs".to_string(), "src/".to_string(), vec![0, 1], true),
            ("file2.rs".to_string(), "src/".to_string(), vec![], false),
        ];
        let lines = picker_popup_lines("fi", &items, 10, 2);
        // input + count + 2 items
        assert_eq!(lines.len(), 4);
    }
}
