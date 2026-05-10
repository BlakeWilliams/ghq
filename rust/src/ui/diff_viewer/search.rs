use regex::Regex;

pub struct SearchState {
    pub active: bool,
    pub query: String,
    pub compiled: Option<Regex>,
    pub matches: Vec<SearchMatch>,
    pub current_match: usize,
    pub case_sensitive: bool,
}

#[derive(Debug, Clone)]
pub struct SearchMatch {
    pub line_idx: usize,
    pub byte_start: usize,
    pub byte_end: usize,
}

impl SearchState {
    pub fn new() -> Self {
        Self {
            active: false,
            query: String::new(),
            compiled: None,
            matches: Vec::new(),
            current_match: 0,
            case_sensitive: false,
        }
    }

    pub fn set_query(&mut self, query: &str) {
        self.query = query.to_string();
        // Smart case: case-insensitive unless query has uppercase
        self.case_sensitive = query.chars().any(|c| c.is_uppercase());
        self.compiled = if self.case_sensitive {
            Regex::new(query).ok()
        } else {
            Regex::new(&format!("(?i){query}")).ok()
        };
    }

    pub fn search_lines(&mut self, lines: &[String]) {
        self.matches.clear();
        self.current_match = 0;

        let re = match &self.compiled {
            Some(r) => r,
            None => return,
        };

        for (idx, line) in lines.iter().enumerate() {
            for m in re.find_iter(line) {
                self.matches.push(SearchMatch {
                    line_idx: idx,
                    byte_start: m.start(),
                    byte_end: m.end(),
                });
            }
        }
    }

    pub fn next_match(&mut self) -> Option<usize> {
        if self.matches.is_empty() {
            return None;
        }
        self.current_match = (self.current_match + 1) % self.matches.len();
        Some(self.matches[self.current_match].line_idx)
    }

    pub fn prev_match(&mut self) -> Option<usize> {
        if self.matches.is_empty() {
            return None;
        }
        if self.current_match == 0 {
            self.current_match = self.matches.len() - 1;
        } else {
            self.current_match -= 1;
        }
        Some(self.matches[self.current_match].line_idx)
    }

    pub fn clear(&mut self) {
        self.active = false;
        self.query.clear();
        self.compiled = None;
        self.matches.clear();
        self.current_match = 0;
    }

    pub fn match_count(&self) -> usize {
        self.matches.len()
    }

    pub fn current_match_index(&self) -> usize {
        self.current_match
    }
}

impl Default for SearchState {
    fn default() -> Self {
        Self::new()
    }
}
