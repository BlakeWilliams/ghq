use std::collections::HashMap;
use std::collections::HashSet;
use std::str::FromStr;
use std::sync::Arc;

use ratatui::style::{Color, Modifier, Style};
use syntect::easy::HighlightLines;
use syntect::highlighting::{
    Color as SyntectColor, FontStyle, ScopeSelectors, StyleModifier, Theme, ThemeItem,
    ThemeSettings,
};
use syntect::parsing::SyntaxSet;
use syntect::util::LinesWithEndings;

use crate::terminal::palette::Palette;

/// Rust keywords that two-face's grammar doesn't scope.
fn rust_extra_keywords() -> HashSet<&'static str> {
    [
        "async", "await", "dyn", "move", "try", "union", "yield",
        "abstract", "become", "box", "do", "final", "override",
        "priv", "typeof", "unsized", "virtual",
    ]
    .into_iter()
    .collect()
}

#[derive(Clone)]
pub struct Highlighter {
    syntax_set: Arc<SyntaxSet>,
    theme: Theme,
    /// When set, maps syntect RGB output → ANSI color names.
    ansi_map: Option<HashMap<(u8, u8, u8), Color>>,
}

/// Semantic color roles for syntax highlighting, matching Go's chroma style.
struct ThemeColors {
    text: (u8, u8, u8),
    keyword: (u8, u8, u8),
    string: (u8, u8, u8),
    number: (u8, u8, u8),
    comment: (u8, u8, u8),
    function: (u8, u8, u8),
    typ: (u8, u8, u8),
    operator: (u8, u8, u8),
    constant: (u8, u8, u8),
    deleted: (u8, u8, u8),
    inserted: (u8, u8, u8),
}

fn sc(r: u8, g: u8, b: u8) -> SyntectColor {
    SyntectColor { r, g, b, a: 255 }
}

fn scope_item(scope: &str, fg: (u8, u8, u8), bold: bool, italic: bool) -> ThemeItem {
    let mut font_style = FontStyle::empty();
    if bold {
        font_style |= FontStyle::BOLD;
    }
    if italic {
        font_style |= FontStyle::ITALIC;
    }
    ThemeItem {
        scope: ScopeSelectors::from_str(scope).unwrap(),
        style: StyleModifier {
            foreground: Some(sc(fg.0, fg.1, fg.2)),
            background: None,
            font_style: if font_style.is_empty() {
                None
            } else {
                Some(font_style)
            },
        },
    }
}

/// Build the full scope list used by both default and palette-derived themes.
fn build_scopes(c: &ThemeColors) -> Vec<ThemeItem> {
    vec![
        // ── Keywords ──
        scope_item("keyword", c.keyword, true, false),
        scope_item("keyword.control", c.keyword, true, false),
        scope_item("keyword.control.import", c.keyword, false, false),
        scope_item("keyword.control.flow", c.keyword, true, false),
        scope_item("keyword.control.conditional", c.keyword, true, false),
        scope_item("keyword.control.loop", c.keyword, true, false),
        scope_item("keyword.declaration", c.keyword, true, false),
        scope_item("keyword.operator", c.operator, false, false),
        scope_item("keyword.operator.assignment", c.operator, false, false),
        scope_item("keyword.operator.arithmetic", c.operator, false, false),
        scope_item("keyword.operator.logical", c.operator, false, false),
        scope_item("keyword.operator.comparison", c.operator, false, false),
        scope_item("keyword.operator.new", c.keyword, true, false),
        scope_item("keyword.other", c.keyword, false, false),

        // ── Storage ──
        scope_item("storage", c.keyword, true, false),
        scope_item("storage.type", c.typ, false, false),
        scope_item("storage.type.function", c.keyword, true, false),
        scope_item("storage.type.class", c.keyword, true, false),
        scope_item("storage.type.interface", c.keyword, true, false),
        scope_item("storage.type.struct", c.keyword, true, false),
        scope_item("storage.type.enum", c.keyword, true, false),
        scope_item("storage.modifier", c.keyword, true, false),

        // ── Entity names ──
        scope_item("entity.name", c.function, false, false),
        scope_item("entity.name.function", c.function, false, false),
        scope_item("entity.name.function.constructor", c.function, false, false),
        scope_item("entity.name.function.decorator", c.operator, false, false),
        scope_item("entity.name.class", c.function, true, false),
        scope_item("entity.name.struct", c.function, true, false),
        scope_item("entity.name.enum", c.function, true, false),
        scope_item("entity.name.interface", c.function, true, false),
        scope_item("entity.name.trait", c.function, true, false),
        scope_item("entity.name.type", c.typ, false, false),
        scope_item("entity.name.tag", c.keyword, false, false),
        scope_item("entity.name.section", c.function, true, false),
        scope_item("entity.name.namespace", c.keyword, false, false),
        scope_item("entity.name.impl", c.typ, false, false),
        scope_item("entity.other.attribute-name", c.function, false, false),
        scope_item("entity.other.inherited-class", c.typ, false, false),

        // ── Variables ──
        scope_item("variable", c.text, false, false),
        scope_item("variable.other", c.text, false, false),
        scope_item("variable.other.constant", c.constant, false, false),
        scope_item("variable.other.member", c.text, false, false),
        scope_item("variable.other.property", c.text, false, false),
        scope_item("variable.parameter", c.text, false, false),
        scope_item("variable.language", c.keyword, false, true),
        scope_item("variable.function", c.function, false, false),
        scope_item("variable.annotation", c.operator, false, false),

        // ── Constants ──
        scope_item("constant", c.constant, false, false),
        scope_item("constant.numeric", c.number, false, false),
        scope_item("constant.numeric.integer", c.number, false, false),
        scope_item("constant.numeric.float", c.number, false, false),
        scope_item("constant.numeric.hex", c.number, false, false),
        scope_item("constant.language", c.constant, false, false),
        scope_item("constant.character", c.string, false, false),
        scope_item("constant.character.escape", c.operator, false, false),
        scope_item("constant.other", c.constant, false, false),

        // ── Strings ──
        scope_item("string", c.string, false, false),
        scope_item("string.quoted", c.string, false, false),
        scope_item("string.quoted.single", c.string, false, false),
        scope_item("string.quoted.double", c.string, false, false),
        scope_item("string.quoted.triple", c.string, false, false),
        scope_item("string.quoted.other", c.string, false, false),
        scope_item("string.template", c.string, false, false),
        scope_item("string.interpolated", c.string, false, false),
        scope_item("string.regexp", c.operator, false, false),
        scope_item("string.other", c.string, false, false),

        // ── Comments ──
        scope_item("comment", c.comment, false, true),
        scope_item("comment.line", c.comment, false, true),
        scope_item("comment.block", c.comment, false, true),
        scope_item("comment.block.documentation", c.comment, false, true),

        // ── Support (built-in / framework) ──
        scope_item("support", c.function, false, false),
        scope_item("support.function", c.function, false, false),
        scope_item("support.function.builtin", c.function, false, false),
        scope_item("support.macro", c.function, false, true),
        scope_item("support.class", c.function, true, false),
        scope_item("support.type", c.typ, false, false),
        scope_item("support.type.builtin", c.typ, false, false),
        scope_item("support.constant", c.constant, false, false),
        scope_item("support.variable", c.text, false, false),
        scope_item("support.other", c.function, false, false),
        scope_item("support.module", c.keyword, false, false),

        // ── Meta ──
        scope_item("meta.preprocessor", c.operator, false, false),
        scope_item("meta.decorator", c.operator, false, false),
        scope_item("meta.annotation", c.operator, false, false),
        scope_item("meta.function-call", c.text, false, false),
        scope_item("meta.attribute", c.operator, false, false),

        // ── Punctuation ──
        scope_item("punctuation", c.text, false, false),
        scope_item("punctuation.definition.string", c.string, false, false),
        scope_item("punctuation.definition.comment", c.comment, false, true),
        scope_item("punctuation.definition.tag", c.keyword, false, false),
        scope_item("punctuation.definition.annotation", c.operator, false, false),
        scope_item("punctuation.separator", c.text, false, false),
        scope_item("punctuation.section", c.text, false, false),
        scope_item("punctuation.accessor", c.text, false, false),

        // ── Source-specific ──
        scope_item("source.go keyword.function", c.keyword, true, false),
        scope_item("source.go keyword.var", c.keyword, true, false),
        scope_item("source.go keyword.const", c.keyword, true, false),
        scope_item("source.go keyword.type", c.keyword, true, false),
        scope_item("source.go keyword.interface", c.keyword, true, false),
        scope_item("source.go keyword.struct", c.keyword, true, false),
        scope_item("source.go keyword.package", c.keyword, true, false),
        scope_item("source.go keyword.import", c.keyword, false, false),
        scope_item("source.rust keyword.other", c.keyword, true, false),
        scope_item("source.rust storage.type.impl", c.keyword, true, false),
        scope_item("source.rust entity.name.lifetime", c.operator, false, false),
        scope_item("source.rust storage.type.lifetime", c.operator, false, false),
        scope_item("source.rust punctuation.definition.attribute", c.operator, false, false),
        scope_item("source.rust meta.attribute", c.operator, false, false),
        scope_item("source.python meta.function-call.generic", c.function, false, false),
        scope_item("source.python meta.qualified-name", c.text, false, false),
        scope_item("source.ts entity.name.type", c.typ, false, false),
        scope_item("source.js entity.name.type", c.typ, false, false),

        // ── Markup ──
        scope_item("markup.deleted", c.deleted, false, false),
        scope_item("markup.inserted", c.inserted, false, false),
        scope_item("markup.changed", c.operator, false, false),
        scope_item("markup.italic", c.text, false, true),
        scope_item("markup.bold", c.text, true, false),
        scope_item("markup.heading", c.function, true, false),
        scope_item("markup.list", c.keyword, false, false),
        scope_item("markup.quote", c.comment, false, true),
        scope_item("markup.raw", c.string, false, false),
        scope_item("markup.underline.link", c.function, false, false),
    ]
}

/// Build a default theme using typical terminal-friendly colors.
fn default_ansi_theme() -> Theme {
    let colors = ThemeColors {
        text: (200, 200, 200),
        keyword: (255, 85, 255),
        string: (85, 255, 85),
        number: (85, 255, 255),
        comment: (128, 128, 128),
        function: (85, 85, 255),
        typ: (85, 255, 255),
        operator: (255, 255, 85),
        constant: (85, 255, 255),
        deleted: (255, 85, 85),
        inserted: (85, 255, 85),
    };

    Theme {
        name: Some("ghq-default".to_string()),
        author: None,
        settings: ThemeSettings {
            foreground: Some(sc(colors.text.0, colors.text.1, colors.text.2)),
            background: None,
            ..Default::default()
        },
        scopes: build_scopes(&colors),
    }
}

/// Build the ANSI color map: maps default theme RGB → ANSI color names.
fn build_ansi_map() -> HashMap<(u8, u8, u8), Color> {
    let mut m = HashMap::new();
    m.insert((200, 200, 200), Color::White);
    m.insert((255, 85, 255), Color::LightMagenta);
    m.insert((85, 255, 85), Color::LightGreen);
    m.insert((85, 255, 255), Color::LightCyan);
    m.insert((128, 128, 128), Color::DarkGray);
    m.insert((85, 85, 255), Color::LightBlue);
    m.insert((255, 255, 85), Color::LightYellow);
    m.insert((255, 85, 85), Color::LightRed);
    m
}

impl Highlighter {
    pub fn new() -> Self {
        let ss = two_face::syntax::extra_newlines();
        Self {
            syntax_set: Arc::new(ss),
            theme: default_ansi_theme(),
            ansi_map: Some(build_ansi_map()),
        }
    }

    /// Set a palette-derived theme. Clears the ANSI map so we use exact RGB.
    pub fn set_theme(&mut self, theme: Theme) {
        self.theme = theme;
        self.ansi_map = None;
    }

    /// Convert a syntect style to a ratatui Style, preserving bold/italic.
    fn to_style(&self, style: syntect::highlighting::Style) -> Style {
        let fg = if let Some(map) = &self.ansi_map {
            if let Some(&ansi) = map.get(&(style.foreground.r, style.foreground.g, style.foreground.b)) {
                ansi
            } else {
                Color::Rgb(style.foreground.r, style.foreground.g, style.foreground.b)
            }
        } else {
            Color::Rgb(style.foreground.r, style.foreground.g, style.foreground.b)
        };

        let mut s = Style::default().fg(fg);
        if style.font_style.contains(FontStyle::BOLD) {
            s = s.add_modifier(Modifier::BOLD);
        }
        if style.font_style.contains(FontStyle::ITALIC) {
            s = s.add_modifier(Modifier::ITALIC);
        }
        s
    }

    fn keyword_style(&self) -> Style {
        let kw = &self.theme.scopes.iter()
            .find(|s| {
                s.scope.selectors.iter().any(|sel| {
                    sel.extract_single_scope()
                        .is_some_and(|sc| sc.build_string() == "keyword")
                })
            });
        if let Some(item) = kw {
            self.to_style(syntect::highlighting::Style {
                foreground: item.style.foreground.unwrap_or(SyntectColor { r: 255, g: 85, b: 255, a: 255 }),
                background: SyntectColor { r: 0, g: 0, b: 0, a: 0 },
                font_style: item.style.font_style.unwrap_or(FontStyle::BOLD),
            })
        } else {
            Style::default().fg(Color::LightMagenta).add_modifier(Modifier::BOLD)
        }
    }

    /// Fix spans where the grammar missed Rust keywords like async/await.
    fn patch_rust_keywords(&self, spans: Vec<(Style, String)>, is_rust: bool) -> Vec<(Style, String)> {
        if !is_rust {
            return spans;
        }
        let keywords = rust_extra_keywords();
        let kw_style = self.keyword_style();

        let mut result = Vec::with_capacity(spans.len());
        for (style, text) in spans {
            let trimmed = text.trim();
            if keywords.contains(trimmed) {
                // Preserve any leading/trailing whitespace as separate spans
                let leading: String = text.chars().take_while(|c| c.is_whitespace()).collect();
                let trailing: String = text.chars().rev().take_while(|c| c.is_whitespace()).collect();
                if !leading.is_empty() {
                    result.push((style, leading));
                }
                result.push((kw_style, trimmed.to_string()));
                if !trailing.is_empty() {
                    result.push((style, trailing));
                }
            } else {
                result.push((style, text));
            }
        }
        result
    }

    pub fn highlight_file(&self, content: &str, filename: &str) -> Vec<Vec<(Style, String)>> {
        let syntax = self
            .syntax_set
            .find_syntax_for_file(filename)
            .ok()
            .flatten()
            .unwrap_or_else(|| self.syntax_set.find_syntax_plain_text());

        let is_rust = syntax.name == "Rust" || syntax.name == "Rust Enhanced";
        let mut h = HighlightLines::new(syntax, &self.theme);
        let mut result = Vec::new();

        for line in LinesWithEndings::from(content) {
            match h.highlight_line(line, &self.syntax_set) {
                Ok(regions) => {
                    let spans: Vec<(Style, String)> = regions
                        .into_iter()
                        .map(|(style, text)| {
                            let s = self.to_style(style);
                            let clean = text.trim_end_matches('\n').trim_end_matches('\r');
                            (s, clean.to_string())
                        })
                        .filter(|(_, t)| !t.is_empty())
                        .collect();
                    result.push(self.patch_rust_keywords(spans, is_rust));
                }
                Err(_) => {
                    result.push(vec![(Style::default(), line.trim_end().to_string())]);
                }
            }
        }

        result
    }

    /// Highlight a code block by language token (e.g. "rust", "go", "js").
    /// Returns one Vec<(Style, String)> per line.
    pub fn highlight_code_block(&self, code: &str, lang: &str) -> Vec<Vec<(Style, String)>> {
        let syntax = self
            .syntax_set
            .find_syntax_by_token(lang)
            .unwrap_or_else(|| self.syntax_set.find_syntax_plain_text());

        let is_rust = syntax.name == "Rust" || syntax.name == "Rust Enhanced";
        let mut h = HighlightLines::new(syntax, &self.theme);
        let mut result = Vec::new();

        for line in LinesWithEndings::from(code) {
            match h.highlight_line(line, &self.syntax_set) {
                Ok(regions) => {
                    let spans: Vec<(Style, String)> = regions
                        .into_iter()
                        .map(|(style, text)| {
                            let s = self.to_style(style);
                            let clean = text.trim_end_matches('\n').trim_end_matches('\r');
                            (s, clean.to_string())
                        })
                        .filter(|(_, t)| !t.is_empty())
                        .collect();
                    result.push(self.patch_rust_keywords(spans, is_rust));
                }
                Err(_) => {
                    result.push(vec![(Style::default(), line.trim_end().to_string())]);
                }
            }
        }

        result
    }
}

pub fn build_theme_from_palette(palette: &Palette) -> Theme {
    let get = |idx: usize| -> Option<(u8, u8, u8)> { palette.colors[idx] };

    let pick = |bright: usize, normal: usize| -> (u8, u8, u8) {
        get(bright).or_else(|| get(normal)).unwrap_or((200, 200, 200))
    };

    // ANSI indices: 0=Black 1=Red 2=Green 3=Yellow 4=Blue 5=Magenta 6=Cyan 7=White
    //               8=BrightBlack 9..15 = bright variants
    let colors = ThemeColors {
        text: pick(15, 7),
        keyword: pick(13, 5),
        string: pick(10, 2),
        number: pick(14, 6),
        comment: get(8).unwrap_or((128, 128, 128)),
        function: pick(12, 4),
        typ: pick(14, 6),
        operator: pick(11, 3),
        constant: pick(14, 6),
        deleted: pick(9, 1),
        inserted: pick(10, 2),
    };

    Theme {
        name: Some("ghq".to_string()),
        author: None,
        settings: ThemeSettings {
            foreground: Some(sc(colors.text.0, colors.text.1, colors.text.2)),
            background: None,
            ..Default::default()
        },
        scopes: build_scopes(&colors),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn async_await_highlighted_as_keywords() {
        let hl = Highlighter::new();
        let spans = hl.highlight_file("async fn main() {\n    foo().await;\n}\n", "test.rs");

        // First line should start with "async" in keyword style
        let first_line = &spans[0];
        let async_span = first_line.iter().find(|(_, t)| t.trim() == "async");
        assert!(async_span.is_some(), "async should be its own span");
        let (style, _) = async_span.unwrap();
        assert!(
            style.add_modifier.contains(Modifier::BOLD),
            "async should be bold (keyword style)"
        );

        // Second line should contain "await" in keyword style
        let second_line = &spans[1];
        let await_span = second_line.iter().find(|(_, t)| t.trim() == "await");
        assert!(await_span.is_some(), "await should be its own span");
        let (style, _) = await_span.unwrap();
        assert!(
            style.add_modifier.contains(Modifier::BOLD),
            "await should be bold (keyword style)"
        );
    }
}
