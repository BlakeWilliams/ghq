use std::collections::HashSet;
use regex::Regex;

use super::render_list::{LineType, RenderItem, RenderList};

pub struct SearchState {
    /// True while the search input popup is open.
    pub active: bool,
    /// Current typed query (while popup is open or after confirmation).
    pub query: String,
    /// Compiled regex from the query.
    pub pattern: Option<Regex>,
    /// Render-list indices of diff lines that match (ordered).
    pub matches: Vec<usize>,
    /// Fast lookup set for match checking.
    match_set: HashSet<usize>,
    /// Index into `matches` for the current highlighted match.
    pub match_idx: Option<usize>,
    /// Cursor position when `/` was pressed (for incsearch origin + cancel restore).
    pub origin_cursor: usize,
    /// Scroll offset when `/` was pressed (for cancel restore).
    pub origin_offset: usize,
}

impl SearchState {
    pub fn new() -> Self {
        Self {
            active: false,
            query: String::new(),
            pattern: None,
            matches: Vec::new(),
            match_set: HashSet::new(),
            match_idx: None,
            origin_cursor: 0,
            origin_offset: 0,
        }
    }

    pub fn start(&mut self, cursor: usize, offset: usize) {
        self.active = true;
        self.query.clear();
        self.origin_cursor = cursor;
        self.origin_offset = offset;
    }

    /// Update query and immediately re-run search. Smart-case: case-insensitive
    /// unless the query contains an uppercase character.
    pub fn set_query(&mut self, query: &str, render_list: &RenderList) {
        self.query = query.to_string();
        if query.is_empty() {
            self.pattern = None;
            self.matches.clear();
            self.match_set.clear();
            self.match_idx = None;
            return;
        }
        let has_upper = query.chars().any(|c| c.is_uppercase());
        let escaped = regex::escape(query);
        self.pattern = if has_upper {
            Regex::new(&escaped).ok()
        } else {
            Regex::new(&format!("(?i){escaped}")).ok()
        };
        self.run_search(render_list);
    }

    /// Scan the render list for matching diff lines (skips hunk headers).
    pub fn run_search(&mut self, render_list: &RenderList) {
        self.matches.clear();
        self.match_set.clear();
        self.match_idx = None;

        let re = match &self.pattern {
            Some(r) => r,
            None => return,
        };

        for (idx, item) in render_list.items().iter().enumerate() {
            if let RenderItem::DiffLine(dl) = item {
                if dl.line_type == LineType::HunkHeader {
                    continue;
                }
                if re.is_match(&dl.content) {
                    self.matches.push(idx);
                    self.match_set.insert(idx);
                }
            }
        }
    }

    /// Confirm search (Enter). Keep the pattern active but close the popup.
    pub fn confirm(&mut self) {
        self.active = false;
    }

    /// Cancel search (Esc while popup is open). Clears everything.
    pub fn cancel(&mut self) {
        self.active = false;
        self.query.clear();
        self.pattern = None;
        self.matches.clear();
        self.match_set.clear();
        self.match_idx = None;
    }

    /// Clear search results (Esc when popup is not open but pattern exists).
    pub fn clear(&mut self) {
        self.query.clear();
        self.pattern = None;
        self.matches.clear();
        self.match_set.clear();
        self.match_idx = None;
    }

    /// Jump to the next match after `cursor` (render-list index). Wraps around.
    pub fn next_match(&mut self, cursor: usize) -> Option<usize> {
        if self.matches.is_empty() {
            return None;
        }
        let pos = self.matches.iter().position(|&m| m > cursor);
        let idx = pos.unwrap_or(0);
        self.match_idx = Some(idx);
        Some(self.matches[idx])
    }

    /// Jump to the next match at or after `cursor`. Wraps around.
    pub fn next_match_inclusive(&mut self, cursor: usize) -> Option<usize> {
        if self.matches.is_empty() {
            return None;
        }
        let pos = self.matches.iter().position(|&m| m >= cursor);
        let idx = pos.unwrap_or(0);
        self.match_idx = Some(idx);
        Some(self.matches[idx])
    }

    /// Jump to the previous match before `cursor`. Wraps around.
    pub fn prev_match(&mut self, cursor: usize) -> Option<usize> {
        if self.matches.is_empty() {
            return None;
        }
        let pos = self.matches.iter().rposition(|&m| m < cursor);
        let idx = pos.unwrap_or(self.matches.len() - 1);
        self.match_idx = Some(idx);
        Some(self.matches[idx])
    }

    /// Returns true if the given render-list index is a search match.
    pub fn is_match(&self, render_idx: usize) -> bool {
        self.pattern.is_some() && self.match_set.contains(&render_idx)
    }

    /// Returns true if the given render-list index is the current highlighted match.
    pub fn is_current_match(&self, render_idx: usize) -> bool {
        if let Some(idx) = self.match_idx {
            idx < self.matches.len() && self.matches[idx] == render_idx
        } else {
            false
        }
    }

    pub fn match_count(&self) -> usize {
        self.matches.len()
    }

    pub fn has_pattern(&self) -> bool {
        self.pattern.is_some()
    }
}

impl Default for SearchState {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ui::diff_viewer::render_list::DiffLineData;

    fn make_render_list(lines: &[(&str, LineType)]) -> RenderList {
        let diff_lines: Vec<DiffLineData> = lines
            .iter()
            .enumerate()
            .map(|(i, (content, lt))| DiffLineData {
                line_type: *lt,
                content: content.to_string(),
                old_line_no: Some(i as i32 + 1),
                new_line_no: Some(i as i32 + 1),
                highlighted: Vec::new(),
                badge: None,
            })
            .collect();
        RenderList::from_diff_lines(diff_lines)
    }

    #[test]
    fn smart_case_insensitive() {
        let rl = make_render_list(&[
            ("Hello world", LineType::Add),
            ("hello WORLD", LineType::Add),
            ("goodbye", LineType::Context),
        ]);
        let mut s = SearchState::new();
        s.set_query("hello", &rl);
        assert_eq!(s.matches.len(), 2);
    }

    #[test]
    fn smart_case_sensitive() {
        let rl = make_render_list(&[
            ("Hello world", LineType::Add),
            ("hello WORLD", LineType::Add),
        ]);
        let mut s = SearchState::new();
        s.set_query("Hello", &rl);
        assert_eq!(s.matches.len(), 1);
        assert_eq!(s.matches[0], 0);
    }

    #[test]
    fn skips_hunk_headers() {
        let rl = make_render_list(&[
            ("@@ -1,3 +1,3 @@ fn main", LineType::HunkHeader),
            ("let x = hello;", LineType::Add),
        ]);
        let mut s = SearchState::new();
        s.set_query("hello", &rl);
        assert_eq!(s.matches.len(), 1);
        assert_eq!(s.matches[0], 1);
    }

    #[test]
    fn next_wraps_around() {
        let rl = make_render_list(&[
            ("aaa", LineType::Add),
            ("bbb", LineType::Add),
            ("aaa", LineType::Add),
        ]);
        let mut s = SearchState::new();
        s.set_query("aaa", &rl);
        assert_eq!(s.matches, vec![0, 2]);

        assert_eq!(s.next_match(0), Some(2));
        assert_eq!(s.next_match(2), Some(0));
    }

    #[test]
    fn prev_wraps_around() {
        let rl = make_render_list(&[
            ("aaa", LineType::Add),
            ("bbb", LineType::Add),
            ("aaa", LineType::Add),
        ]);
        let mut s = SearchState::new();
        s.set_query("aaa", &rl);

        assert_eq!(s.prev_match(0), Some(2));
        assert_eq!(s.prev_match(2), Some(0));
    }

    #[test]
    fn cancel_clears_state() {
        let rl = make_render_list(&[("hello", LineType::Add)]);
        let mut s = SearchState::new();
        s.start(0, 0);
        s.set_query("hello", &rl);
        assert!(s.has_pattern());
        s.cancel();
        assert!(!s.has_pattern());
        assert!(s.matches.is_empty());
    }

    #[test]
    fn empty_query_clears() {
        let rl = make_render_list(&[("hello", LineType::Add)]);
        let mut s = SearchState::new();
        s.set_query("hello", &rl);
        assert_eq!(s.matches.len(), 1);
        s.set_query("", &rl);
        assert!(s.matches.is_empty());
        assert!(!s.has_pattern());
    }

    #[test]
    fn start_saves_origin() {
        let mut s = SearchState::new();
        s.start(42, 10);
        assert!(s.active);
        assert_eq!(s.origin_cursor, 42);
        assert_eq!(s.origin_offset, 10);
        assert!(s.query.is_empty());
    }

    #[test]
    fn confirm_keeps_pattern_clears_active() {
        let rl = make_render_list(&[("hello", LineType::Add)]);
        let mut s = SearchState::new();
        s.start(0, 0);
        s.set_query("hello", &rl);
        assert!(s.active);
        assert!(s.has_pattern());

        s.confirm();
        assert!(!s.active);
        assert!(s.has_pattern());
        assert_eq!(s.matches.len(), 1);
    }

    #[test]
    fn is_match_and_current_match() {
        let rl = make_render_list(&[
            ("aaa", LineType::Add),
            ("bbb", LineType::Context),
            ("aaa", LineType::Add),
        ]);
        let mut s = SearchState::new();
        s.set_query("aaa", &rl);

        assert!(s.is_match(0));
        assert!(s.is_match(2));
        assert!(!s.is_match(1));

        // No current match yet
        assert!(!s.is_current_match(0));
        assert!(!s.is_current_match(2));

        // Navigate to first match
        s.next_match(0);
        assert!(s.is_current_match(2));
        assert!(!s.is_current_match(0));
    }

    #[test]
    fn incsearch_from_origin_finds_at_origin() {
        let rl = make_render_list(&[
            ("aaa", LineType::Add),     // 0
            ("bbb", LineType::Context), // 1
            ("aaa", LineType::Add),     // 2
        ]);
        let mut s = SearchState::new();
        s.start(0, 0);
        s.set_query("aaa", &rl);

        // Inclusive search from origin should find match at origin line
        let result = s.next_match_inclusive(s.origin_cursor);
        assert_eq!(result, Some(0));
    }

    #[test]
    fn incsearch_from_mid_file() {
        let rl = make_render_list(&[
            ("aaa", LineType::Add),     // 0
            ("bbb", LineType::Context), // 1
            ("aaa", LineType::Add),     // 2
            ("ccc", LineType::Context), // 3
            ("aaa", LineType::Add),     // 4
        ]);
        let mut s = SearchState::new();
        s.start(3, 0); // cursor at line 3
        s.set_query("aaa", &rl);

        // Should find first match at/after origin (3), which is line 4
        let result = s.next_match_inclusive(s.origin_cursor);
        assert_eq!(result, Some(4));
    }

    #[test]
    fn no_matches_returns_none() {
        let rl = make_render_list(&[("hello", LineType::Add)]);
        let mut s = SearchState::new();
        s.set_query("xyz", &rl);
        assert!(s.matches.is_empty());
        assert_eq!(s.next_match(0), None);
        assert_eq!(s.prev_match(0), None);
    }

    #[test]
    fn clear_vs_cancel() {
        let rl = make_render_list(&[("hello", LineType::Add)]);
        let mut s = SearchState::new();
        s.start(5, 2);
        s.set_query("hello", &rl);

        // clear keeps origin (it's not "undoing" the search start)
        s.clear();
        assert!(!s.has_pattern());
        assert!(s.matches.is_empty());

        // cancel also clears pattern
        s.set_query("hello", &rl);
        s.cancel();
        assert!(!s.active);
        assert!(!s.has_pattern());
    }
}