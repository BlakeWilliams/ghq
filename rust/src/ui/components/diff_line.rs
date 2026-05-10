use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};

use crate::ui::styles::DiffColors;
use crate::ui::diff_viewer::render_list::LineType;

pub fn render_diff_line(
    content: &str,
    kind: LineType,
    old_line_no: Option<i32>,
    new_line_no: Option<i32>,
    _width: u16,
    colors: &DiffColors,
    is_cursor: bool,
    is_selected: bool,
) -> Line<'static> {
    let old = old_line_no
        .map(|n| format!("{:>4}", n))
        .unwrap_or_else(|| "    ".to_string());
    let new = new_line_no
        .map(|n| format!("{:>4}", n))
        .unwrap_or_else(|| "    ".to_string());

    let (prefix, fg, line_bg) = match kind {
        LineType::Add => ("+", colors.add_fg, colors.add_bg),
        LineType::Delete => ("-", colors.del_fg, colors.del_bg),
        LineType::Context => (" ", colors.context_fg, ratatui::style::Color::Reset),
        LineType::HunkHeader => ("@", colors.hunk_fg, colors.hunk_bg),
    };

    let bg = if is_cursor {
        colors.cursor_bg
    } else if is_selected {
        colors.selection_bg
    } else {
        line_bg
    };

    let gutter_style = Style::default()
        .fg(colors.line_number_fg)
        .bg(bg);
    let marker_style = Style::default().fg(fg).bg(bg).add_modifier(Modifier::BOLD);
    let content_style = Style::default().bg(bg);

    let expanded = content.replace('\t', "        ");

    Line::from(vec![
        Span::styled(old, gutter_style),
        Span::styled(" ", gutter_style),
        Span::styled(new, gutter_style),
        Span::styled(format!(" {prefix}"), marker_style),
        Span::styled(expanded, content_style),
    ])
}
