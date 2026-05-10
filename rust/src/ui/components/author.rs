use ratatui::style::{Color, Style};

const AUTHOR_COLORS: [Color; 8] = [
    Color::Cyan,
    Color::Green,
    Color::Yellow,
    Color::Blue,
    Color::Magenta,
    Color::Red,
    Color::LightCyan,
    Color::LightGreen,
];

pub fn colored_author(name: &str) -> Style {
    let hash = name
        .bytes()
        .fold(0u64, |acc, b| acc.wrapping_mul(31).wrapping_add(b as u64));
    let color = AUTHOR_COLORS[(hash % AUTHOR_COLORS.len() as u64) as usize];
    Style::default().fg(color)
}
