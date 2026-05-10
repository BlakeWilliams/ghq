use ratatui::style::{Color, Style};
use ratatui::text::{Line, Span};

const LEFT_CAP: &str = "\u{e0b6}"; // powerline rounded left
const RIGHT_CAP: &str = "\u{e0b4}"; // powerline rounded right

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum BadgeUrgency {
    Read,
    Resolved,
    Unread,
    Actionable,
}

impl BadgeUrgency {
    pub fn fg_color(&self) -> Color {
        match self {
            BadgeUrgency::Read => Color::DarkGray,
            BadgeUrgency::Resolved => Color::Cyan,
            BadgeUrgency::Unread => Color::Yellow,
            BadgeUrgency::Actionable => Color::Red,
        }
    }

    pub fn bg_color(&self) -> Color {
        match self {
            BadgeUrgency::Read => Color::DarkGray,
            BadgeUrgency::Resolved => Color::Cyan,
            BadgeUrgency::Unread => Color::Yellow,
            BadgeUrgency::Actionable => Color::Red,
        }
    }
}

pub fn render_badge_pill(text: &str, urgency: BadgeUrgency) -> Line<'static> {
    let badge_fg = Color::Black;
    let badge_bg = urgency.bg_color();

    Line::from(vec![
        Span::styled(LEFT_CAP, Style::default().fg(badge_bg)),
        Span::styled(
            format!(" {text} "),
            Style::default().fg(badge_fg).bg(badge_bg),
        ),
        Span::styled(RIGHT_CAP, Style::default().fg(badge_bg)),
    ])
}
