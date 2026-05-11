use itertools::Itertools;
use ratatui::buffer::Buffer;
use ratatui::layout::Rect;
use ratatui::text::Text;
use ratatui::widgets::Widget;

/// Renders text as a clickable OSC 8 hyperlink.
///
/// Uses the 2-char-chunk workaround from ratatui's official hyperlink
/// example to avoid buffer width miscalculation (ratatui#902).
pub struct Hyperlink<'a> {
    text: Text<'a>,
    url: String,
}

impl<'a> Hyperlink<'a> {
    pub fn new(text: impl Into<Text<'a>>, url: impl Into<String>) -> Self {
        Self {
            text: text.into(),
            url: url.into(),
        }
    }
}

impl Widget for &Hyperlink<'_> {
    fn render(self, area: Rect, buf: &mut Buffer) {
        (&self.text).render(area, buf);

        for (i, two_chars) in self
            .text
            .to_string()
            .chars()
            .chunks(2)
            .into_iter()
            .enumerate()
        {
            let chunk: String = two_chars.collect();
            let osc = format!("\x1b]8;;{}\x07{chunk}\x1b]8;;\x07", self.url);
            let x = area.x + i as u16 * 2;
            if x < area.x + area.width && area.y < area.y + area.height {
                buf[(x, area.y)].set_symbol(&osc);
            }
        }
    }
}

/// Inject an OSC 8 hyperlink into an already-rendered buffer region.
///
/// Call this after rendering text normally (so styles are preserved).
/// It overwrites cell symbols in-place with OSC 8 wrapped 2-char chunks.
pub fn inject_hyperlink(buf: &mut Buffer, x: u16, y: u16, width: u16, text: &str, url: &str) {
    for (i, two_chars) in text.chars().chunks(2).into_iter().enumerate() {
        let chunk: String = two_chars.collect();
        let osc = format!("\x1b]8;;{url}\x07{chunk}\x1b]8;;\x07");
        let cell_x = x + i as u16 * 2;
        if cell_x < x + width {
            buf[(cell_x, y)].set_symbol(&osc);
        }
    }
}
