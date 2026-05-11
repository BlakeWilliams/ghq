use ratatui::layout::Rect;
use ratatui::style::Style;
use ratatui::text::{Line, Span};
use ratatui::widgets::Paragraph;
use ratatui::Frame;

use crate::review::comments::{CommentAuthor, LocalComment};
use crate::ui::styles::DiffColors;
use super::author::colored_author;

pub fn render_comment_panel(
    frame: &mut Frame,
    area: Rect,
    comments: &[&LocalComment],
    colors: &DiffColors,
) {
    let mut lines: Vec<Line> = Vec::new();

    for comment in comments {
        let author_name = match &comment.author {
            CommentAuthor::You => "you",
            CommentAuthor::Copilot => "copilot",
            CommentAuthor::GitHub(name) => name,
        };
        let author_style = colored_author(author_name);

        lines.push(Line::from(vec![
            Span::styled(author_name, author_style),
            Span::raw("  "),
            Span::styled(&comment.created_at, Style::default().fg(colors.line_number_fg)),
        ]));

        for body_line in comment.body.lines() {
            lines.push(Line::from(Span::raw(body_line.to_string())));
        }

        lines.push(Line::from(""));
    }

    let panel = Paragraph::new(lines);
    frame.render_widget(panel, area);
}
