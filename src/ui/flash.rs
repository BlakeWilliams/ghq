use std::time::{Duration, Instant};

use ratatui::layout::Rect;
use ratatui::style::{Color, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Clear, Paragraph};
use ratatui::Frame;
use unicode_width::UnicodeWidthStr;

const FLASH_DURATION: Duration = Duration::from_secs(4);
const MAX_WIDTH: u16 = 60;
const PADDING: u16 = 2;

#[derive(Clone)]
pub enum FlashLevel {
    Error,
}

impl FlashLevel {
    fn border_color(&self) -> Color {
        match self {
            FlashLevel::Error => Color::Red,
        }
    }

    fn icon(&self) -> &'static str {
        match self {
            FlashLevel::Error => "✗",
        }
    }
}

pub struct Flash {
    message: String,
    level: FlashLevel,
    created_at: Instant,
}

pub struct FlashState {
    flashes: Vec<Flash>,
}

impl FlashState {
    pub fn new() -> Self {
        Self {
            flashes: Vec::new(),
        }
    }

    pub fn error(&mut self, message: impl Into<String>) {
        self.flashes.push(Flash {
            message: message.into(),
            level: FlashLevel::Error,
            created_at: Instant::now(),
        });
    }

    pub fn gc(&mut self) {
        self.flashes
            .retain(|f| f.created_at.elapsed() < FLASH_DURATION);
    }

    pub fn is_empty(&self) -> bool {
        self.flashes.is_empty()
    }

    pub fn render(&self, frame: &mut Frame, area: Rect) {
        if self.flashes.is_empty() {
            return;
        }

        let mut y = area.y + 1;

        for flash in self.flashes.iter().rev().take(3) {
            let border_color = flash.level.border_color();
            let icon = flash.level.icon();

            let content = format!(" {icon} {}", flash.message);
            let visible_w = UnicodeWidthStr::width(content.as_str()) as u16;
            let box_w = (visible_w + PADDING).min(MAX_WIDTH).min(area.width.saturating_sub(4));
            let inner_w = (box_w as usize).saturating_sub(2);

            // Truncate if too wide
            let display = if UnicodeWidthStr::width(content.as_str()) > inner_w {
                let mut w = 0;
                let truncated: String = content
                    .chars()
                    .take_while(|c| {
                        w += UnicodeWidthStr::width(c.to_string().as_str());
                        w <= inner_w.saturating_sub(1)
                    })
                    .collect();
                format!("{truncated}…")
            } else {
                content
            };

            let box_h: u16 = 3; // top border + content + bottom border

            if y + box_h > area.y + area.height {
                break;
            }

            let x = area.x + area.width.saturating_sub(box_w + 2);
            let rect = Rect::new(x, y, box_w, box_h);
            frame.render_widget(Clear, rect);

            let top_fill = box_w.saturating_sub(2) as usize;
            let text_w = UnicodeWidthStr::width(display.as_str());
            let pad = inner_w.saturating_sub(text_w);

            let lines = vec![
                Line::from(Span::styled(
                    format!("╭{}╮", "─".repeat(top_fill)),
                    Style::default().fg(border_color),
                )),
                Line::from(vec![
                    Span::styled("│", Style::default().fg(border_color)),
                    Span::styled(display, Style::default().fg(Color::White)),
                    Span::raw(" ".repeat(pad)),
                    Span::styled("│", Style::default().fg(border_color)),
                ]),
                Line::from(Span::styled(
                    format!("╰{}╯", "─".repeat(top_fill)),
                    Style::default().fg(border_color),
                )),
            ];

            frame.render_widget(Paragraph::new(lines), rect);
            y += box_h + 1;
        }
    }
}

impl Default for FlashState {
    fn default() -> Self {
        Self::new()
    }
}
