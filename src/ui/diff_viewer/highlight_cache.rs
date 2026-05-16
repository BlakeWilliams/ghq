use std::collections::HashMap;

use ratatui::style::Style;

use super::render_list::{RenderItem, RenderList};
use crate::ui::highlight::Highlighter;

pub struct HighlightCache {
    pub hl_new: Vec<Vec<(Style, String)>>,
    pub hl_old: Vec<Vec<(Style, String)>>,
}

/// Manages syntax highlighting state: the highlighter engine plus
/// per-file caching. Applies highlights to a RenderList.
pub struct HighlightManager {
    pub highlighter: Highlighter,
    cache: HashMap<String, HighlightCache>,
}

fn expand_tabs_hl(spans: &[(Style, String)]) -> Vec<(Style, String)> {
    spans
        .iter()
        .map(|(s, t)| (*s, t.replace('\t', "        ")))
        .collect()
}

impl HighlightManager {
    pub fn new() -> Self {
        Self {
            highlighter: Highlighter::new(),
            cache: HashMap::new(),
        }
    }

    /// Apply highlights from cache if available. Returns true if cache hit.
    pub fn apply_cached(&mut self, render_list: &mut RenderList, filename: &str) -> bool {
        if let Some(cached) = self.cache.remove(filename) {
            self.apply_inner(render_list, &cached.hl_new, &cached.hl_old, filename);
            self.cache.insert(filename.to_string(), cached);
            true
        } else {
            false
        }
    }

    /// Apply pre-computed highlights to the render list and cache them.
    pub fn apply(
        &mut self,
        render_list: &mut RenderList,
        hl_new: Vec<Vec<(Style, String)>>,
        hl_old: Vec<Vec<(Style, String)>>,
        filename: &str,
    ) {
        self.apply_inner(render_list, &hl_new, &hl_old, filename);
        self.cache.insert(filename.to_string(), HighlightCache { hl_new, hl_old });
    }

    /// Cache highlights without applying to any render list.
    pub fn cache_only(
        &mut self,
        hl_new: Vec<Vec<(Style, String)>>,
        hl_old: Vec<Vec<(Style, String)>>,
        filename: &str,
    ) {
        self.cache.insert(filename.to_string(), HighlightCache { hl_new, hl_old });
    }

    fn apply_inner(
        &self,
        render_list: &mut RenderList,
        hl_new: &[Vec<(Style, String)>],
        hl_old: &[Vec<(Style, String)>],
        _filename: &str,
    ) {
        use super::render_list::LineType;

        for item in render_list.items_mut() {
            let RenderItem::DiffLine(dl) = item; {
                match dl.line_type {
                    LineType::Add | LineType::Context => {
                        if let Some(ln) = dl.new_line_no {
                            let idx = (ln - 1) as usize;
                            if idx < hl_new.len() {
                                dl.highlighted = expand_tabs_hl(&hl_new[idx]);
                            }
                        }
                    }
                    LineType::Delete => {
                        if let Some(ln) = dl.old_line_no {
                            let idx = (ln - 1) as usize;
                            if idx < hl_old.len() {
                                dl.highlighted = expand_tabs_hl(&hl_old[idx]);
                            }
                        }
                    }
                    LineType::HunkHeader => {}
                }
            }
        }
    }

    /// Clear all cached highlights.
    pub fn clear(&mut self) {
        self.cache.clear();
    }

    /// Check if a file has cached highlights.
    pub fn is_cached(&self, filename: &str) -> bool {
        self.cache.contains_key(filename)
    }
}

impl Default for HighlightManager {
    fn default() -> Self {
        Self::new()
    }
}
