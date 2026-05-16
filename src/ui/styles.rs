use ratatui::style::{Color, Modifier, Style};

use crate::git::diff::DiffMode;
use crate::terminal::palette::Palette;

struct ColorConstants {
    white: (u8, u8, u8),
    black: (u8, u8, u8),
    fallback_bg: (u8, u8, u8),
    fallback_border: (u8, u8, u8),
}

const COLORS: ColorConstants = ColorConstants {
    white: (255, 255, 255),
    black: (0, 0, 0),
    fallback_bg: (30, 30, 30),
    fallback_border: (128, 128, 128),
};

struct BlendFactors {
    selection: f64,
    dim: f64,
    add_bg: f64,
    add_selected: f64,
    del_bg: f64,
    del_selected: f64,
    hunk_bg: f64,
    hunk_selected: f64,
    contrast_boost: f64,
    search_current: f64,
}

const BLEND: BlendFactors = BlendFactors {
    selection: 0.12,
    dim: 0.35,
    add_bg: 0.08,
    add_selected: 0.25,
    del_bg: 0.08,
    del_selected: 0.25,
    hunk_bg: 0.10,
    hunk_selected: 0.25,
    contrast_boost: 0.3,
    search_current: 0.7,
};

#[derive(Debug, Clone)]
pub struct DiffColors {
    pub add_fg: Color,
    pub add_bg: Color,
    pub del_fg: Color,
    pub del_bg: Color,
    pub hunk_fg: Color,
    pub hunk_bg: Color,
    pub context_fg: Color,
    pub line_number_fg: Color,
    pub cursor_bg: Color,
    pub selected_add_bg: Color,
    pub selected_del_bg: Color,
    pub selected_ctx_bg: Color,
    pub selected_hunk_bg: Color,
    pub search_match_bg: Color,
    pub search_current_bg: Color,
    pub search_match_fg: Color,
    pub selection_bg: Color,
    pub border_fg: Color,
    pub chrome_fg: Color,
    pub palette_dim: Color,
}

/// Alpha blend: result = fg*alpha + bg*(1-alpha)
fn blend(fg: (u8, u8, u8), bg: (u8, u8, u8), alpha: f64) -> (u8, u8, u8) {
    (
        (fg.0 as f64 * alpha + bg.0 as f64 * (1.0 - alpha)) as u8,
        (fg.1 as f64 * alpha + bg.1 as f64 * (1.0 - alpha)) as u8,
        (fg.2 as f64 * alpha + bg.2 as f64 * (1.0 - alpha)) as u8,
    )
}

fn rgb(t: (u8, u8, u8)) -> Color {
    Color::Rgb(t.0, t.1, t.2)
}

fn relative_luminance(c: (u8, u8, u8)) -> f64 {
    0.2126 * (c.0 as f64 / 255.0)
        + 0.7152 * (c.1 as f64 / 255.0)
        + 0.0722 * (c.2 as f64 / 255.0)
}

/// Brightens fg if it doesn't have enough contrast against bg.
/// Matches Go's ensureContrast: if luminance diff <= 0.15, blend 30% white into fg.
fn ensure_contrast(fg: (u8, u8, u8), bg: (u8, u8, u8)) -> (u8, u8, u8) {
    if relative_luminance(fg) - relative_luminance(bg) > 0.15 {
        return fg;
    }
    blend(COLORS.white, fg, BLEND.contrast_boost)
}

/// Lualine's brightness_modifier: channel = clamp(channel + channel * pct / 100).
fn brightness_modify(r: u8, g: u8, b: u8, pct: f64) -> (u8, u8, u8) {
    let modify = |v: u8| -> u8 { (v as f64 + v as f64 * pct / 100.0).round().clamp(0.0, 255.0) as u8 };
    (modify(r), modify(g), modify(b))
}

fn or_default(c: Option<(u8, u8, u8)>, fallback: (u8, u8, u8)) -> (u8, u8, u8) {
    c.unwrap_or(fallback)
}

impl DiffColors {
    pub fn from_palette(palette: &Palette) -> Self {
        let bg = palette.colors[0].unwrap_or(COLORS.fallback_bg);
        let green = palette.colors[2];
        let red = palette.colors[1];
        let blue = palette.colors[4];
        let white = palette.colors[15].or(palette.colors[7]);
        let yellow = palette.colors[3];
        let bright_black = palette.colors[8];

        // Selection tint: white on dark bg, black on light bg
        let bg_lum = relative_luminance(bg);
        let select_tint = if bg_lum < 0.5 {
            COLORS.white
        } else {
            COLORS.black
        };
        let select_bg = blend(select_tint, bg, BLEND.selection);

        let border = or_default(bright_black, COLORS.fallback_border);

        // Chrome color: brightness-modified bg (+40% dark, -20% light)
        let chrome_pct = if bg_lum < 0.5 { 40.0 } else { -20.0 };
        let chrome = brightness_modify(bg.0, bg.1, bg.2, chrome_pct);

        // Dim color for dimmed text (safe on both light/dark)
        let dim = blend(select_tint, bg, BLEND.dim);

        // Add colors
        let (add_fg_c, add_bg_c, selected_add_bg_c) = if let Some(g) = green {
            let add_bg = blend(g, bg, BLEND.add_bg);
            let add_fg = ensure_contrast(g, add_bg);
            let selected_add = blend(g, bg, BLEND.add_selected);
            (rgb(add_fg), rgb(add_bg), rgb(selected_add))
        } else {
            (Color::Green, Color::Reset, Color::DarkGray)
        };

        // Del colors
        let (del_fg_c, del_bg_c, selected_del_bg_c) = if let Some(r) = red {
            let del_bg = blend(r, bg, BLEND.del_bg);
            let del_fg = ensure_contrast(r, del_bg);
            let selected_del = blend(r, bg, BLEND.del_selected);
            (rgb(del_fg), rgb(del_bg), rgb(selected_del))
        } else {
            (Color::Red, Color::Reset, Color::DarkGray)
        };

        // Hunk colors
        let (hunk_fg_c, hunk_bg_c, selected_hunk_bg_c) =
            if let (Some(b), Some(w)) = (blue, white) {
                let hunk_bg = blend(b, bg, BLEND.hunk_bg);
                let hunk_fg = ensure_contrast(w, hunk_bg);
                let selected_hunk = blend(b, bg, BLEND.hunk_selected);
                (rgb(hunk_fg), rgb(hunk_bg), rgb(selected_hunk))
            } else {
                (Color::Cyan, Color::Reset, Color::DarkGray)
            };

        // Search match: palette yellow bg, luminance-based fg
        let (search_bg, search_current, search_fg) = if let Some(y) = yellow {
            let fg = if relative_luminance(y) > 0.4 {
                COLORS.black
            } else {
                COLORS.white
            };
            let current = blend(y, COLORS.white, BLEND.search_current);
            (rgb(y), rgb(current), rgb(fg))
        } else {
            (Color::Yellow, Color::LightYellow, Color::Black)
        };

        Self {
            add_fg: add_fg_c,
            add_bg: add_bg_c,
            del_fg: del_fg_c,
            del_bg: del_bg_c,
            hunk_fg: hunk_fg_c,
            hunk_bg: hunk_bg_c,
            context_fg: Color::Reset,
            line_number_fg: rgb(border),
            cursor_bg: rgb(select_bg),
            selected_add_bg: selected_add_bg_c,
            selected_del_bg: selected_del_bg_c,
            selected_ctx_bg: rgb(select_bg),
            selected_hunk_bg: selected_hunk_bg_c,
            search_match_bg: search_bg,
            search_current_bg: search_current,
            search_match_fg: search_fg,
            selection_bg: Color::DarkGray,
            border_fg: rgb(border),
            chrome_fg: rgb(chrome),
            palette_dim: rgb(dim),
        }
    }
}

impl Default for DiffColors {
    fn default() -> Self {
        // Pure ANSI colors — no indexed 256 colors, so they adapt to any
        // terminal colorscheme. The palette-derived colors (from_palette)
        // produce richer blended variants when the OSC 4 query succeeds.
        Self {
            add_fg: Color::Green,
            add_bg: Color::Reset,
            del_fg: Color::Red,
            del_bg: Color::Reset,
            hunk_fg: Color::Cyan,
            hunk_bg: Color::Reset,
            context_fg: Color::Reset,
            line_number_fg: Color::DarkGray,
            cursor_bg: Color::DarkGray,
            selected_add_bg: Color::DarkGray,
            selected_del_bg: Color::DarkGray,
            selected_ctx_bg: Color::DarkGray,
            selected_hunk_bg: Color::DarkGray,
            search_match_bg: Color::Yellow,
            search_current_bg: Color::LightYellow,
            search_match_fg: Color::Black,
            selection_bg: Color::DarkGray,
            border_fg: Color::DarkGray,
            chrome_fg: Color::DarkGray,
            palette_dim: Color::DarkGray,
        }
    }
}

pub fn mode_color(mode: DiffMode) -> Color {
    match mode {
        DiffMode::Working => Color::Magenta,
        DiffMode::Staged => Color::Green,
        DiffMode::Branch => Color::Blue,
    }
}

pub fn mode_label(mode: DiffMode) -> &'static str {
    match mode {
        DiffMode::Working => "UNSTAGED",
        DiffMode::Staged => "STAGED",
        DiffMode::Branch => "BRANCH",
    }
}

pub fn mode_style(mode: DiffMode) -> Style {
    Style::default()
        .fg(mode_color(mode))
        .add_modifier(Modifier::BOLD)
}
