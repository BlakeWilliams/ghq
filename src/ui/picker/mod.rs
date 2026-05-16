use std::cmp::Reverse;

use fuzzy_matcher::FuzzyMatcher;
use fuzzy_matcher::skim::SkimMatcherV2;

pub struct Picker {
    pub visible: bool,
    pub query: String,
    pub items: Vec<PickerItem>,
    pub filtered: Vec<FilteredItem>,
    pub cursor: usize,
    pub title: String,
    matcher: SkimMatcherV2,
}

#[derive(Clone)]
pub struct PickerItem {
    pub label: String,
    pub description: String,
    pub value: String,
}

pub struct FilteredItem {
    pub index: usize,
    pub score: i64,
    pub match_positions: Vec<usize>,
}

impl Picker {
    pub fn new(title: &str, items: Vec<PickerItem>) -> Self {
        let mut picker = Self {
            visible: true,
            query: String::new(),
            items,
            filtered: Vec::new(),
            cursor: 0,
            title: title.to_string(),
            matcher: SkimMatcherV2::default(),
        };
        picker.filter();
        picker
    }

    pub fn filter(&mut self) {
        self.filtered.clear();
        self.cursor = 0;

        if self.query.is_empty() {
            self.filtered = self
                .items
                .iter()
                .enumerate()
                .map(|(i, _)| FilteredItem {
                    index: i,
                    score: 0,
                    match_positions: Vec::new(),
                })
                .collect();
            return;
        }

        let mut scored: Vec<FilteredItem> = self
            .items
            .iter()
            .enumerate()
            .filter_map(|(i, item)| {
                self.matcher
                    .fuzzy_indices(&item.label, &self.query)
                    .map(|(score, indices)| FilteredItem {
                        index: i,
                        score,
                        match_positions: indices,
                    })
            })
            .collect();

        scored.sort_by_key(|a| Reverse(a.score));
        self.filtered = scored;
    }

    pub fn selected(&self) -> Option<&PickerItem> {
        self.filtered
            .get(self.cursor)
            .map(|f| &self.items[f.index])
    }

    pub fn move_up(&mut self) {
        if self.cursor > 0 {
            self.cursor -= 1;
        }
    }

    pub fn move_down(&mut self) {
        if self.cursor + 1 < self.filtered.len() {
            self.cursor += 1;
        }
    }

    pub fn push_char(&mut self, c: char) {
        self.query.push(c);
        self.filter();
    }

    pub fn pop_char(&mut self) {
        self.query.pop();
        self.filter();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn items() -> Vec<PickerItem> {
        vec![
            PickerItem { label: "src/main.rs".into(), description: "+10 -5".into(), value: "src/main.rs".into() },
            PickerItem { label: "src/lib.rs".into(), description: "+3 -1".into(), value: "src/lib.rs".into() },
            PickerItem { label: "Cargo.toml".into(), description: "+1 -0".into(), value: "Cargo.toml".into() },
            PickerItem { label: "README.md".into(), description: "".into(), value: "README.md".into() },
        ]
    }

    #[test]
    fn empty_query_shows_all() {
        let p = Picker::new("Files", items());
        assert_eq!(p.filtered.len(), 4);
        assert_eq!(p.cursor, 0);
    }

    #[test]
    fn fuzzy_filter_narrows() {
        let mut p = Picker::new("Files", items());
        p.push_char('m');
        p.push_char('a');
        p.push_char('i');
        // "mai" should match "src/main.rs" and "README.md" (fuzzy)
        assert!(p.filtered.len() <= 4);
        assert!(p.filtered.len() >= 1);
        // Best match should be main.rs
        let best = &p.items[p.filtered[0].index];
        assert!(best.label.contains("main"));
    }

    #[test]
    fn cursor_movement_clamps() {
        let mut p = Picker::new("Test", items());
        p.move_up();
        assert_eq!(p.cursor, 0); // can't go above 0

        p.move_down();
        p.move_down();
        p.move_down();
        assert_eq!(p.cursor, 3);
        p.move_down();
        assert_eq!(p.cursor, 3); // can't exceed len-1
    }

    #[test]
    fn selected_returns_correct_item() {
        let mut p = Picker::new("Test", items());
        let first = p.selected().unwrap();
        assert_eq!(first.value, "src/main.rs");

        p.move_down();
        let second = p.selected().unwrap();
        assert_eq!(second.value, "src/lib.rs");
    }

    #[test]
    fn backspace_widens_results() {
        let mut p = Picker::new("Test", items());
        p.push_char('x');
        p.push_char('y');
        p.push_char('z');
        let narrow = p.filtered.len();
        p.pop_char();
        p.pop_char();
        p.pop_char();
        assert_eq!(p.filtered.len(), 4); // back to showing all
        assert!(p.filtered.len() >= narrow);
    }

    #[test]
    fn filter_resets_cursor() {
        let mut p = Picker::new("Test", items());
        p.move_down();
        p.move_down();
        assert_eq!(p.cursor, 2);
        p.push_char('m');
        assert_eq!(p.cursor, 0); // reset on filter
    }
}
