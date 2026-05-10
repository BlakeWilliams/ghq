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

        scored.sort_by(|a, b| b.score.cmp(&a.score));
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
