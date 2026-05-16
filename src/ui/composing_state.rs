use crate::ui::diff_viewer::panel::ReplyMode;

pub struct ComposingState {
    active: bool,
    pub input: String,
    /// Byte offset cursor position within `input`.
    pub cursor: usize,
    pub path: String,
    pub line: i32,
    pub side: String,
    pub reply_to: Option<String>,
    pub reply_mode: ReplyMode,
}

impl ComposingState {
    pub fn new() -> Self {
        Self {
            active: false,
            input: String::new(),
            cursor: 0,
            path: String::new(),
            line: 0,
            side: String::new(),
            reply_to: None,
            reply_mode: ReplyMode::Copilot,
        }
    }

    pub fn is_active(&self) -> bool {
        self.active
    }

    pub fn start_new(&mut self, path: String, line: i32, side: String) {
        self.active = true;
        self.input.clear();
        self.cursor = 0;
        self.path = path;
        self.line = line;
        self.side = side;
        self.reply_to = None;
        self.reply_mode = ReplyMode::Copilot;
    }

    pub fn start_reply(&mut self, reply_to: String, path: String, line: i32, side: String) {
        self.active = true;
        self.input.clear();
        self.cursor = 0;
        self.path = path;
        self.line = line;
        self.side = side;
        self.reply_to = Some(reply_to);
        self.reply_mode = ReplyMode::Copilot;
    }

    pub fn start_reply_with_mode(&mut self, reply_to: String, path: String, line: i32, side: String, mode: ReplyMode) {
        self.active = true;
        self.input.clear();
        self.cursor = 0;
        self.path = path;
        self.line = line;
        self.side = side;
        self.reply_to = Some(reply_to);
        self.reply_mode = mode;
    }

    pub fn toggle_reply_mode(&mut self) {
        self.reply_mode = match self.reply_mode {
            ReplyMode::Copilot => ReplyMode::GitHub,
            ReplyMode::GitHub => ReplyMode::Copilot,
        };
    }

    pub fn cancel(&mut self) {
        self.active = false;
        self.input.clear();
        self.cursor = 0;
        self.reply_to = None;
        self.reply_mode = ReplyMode::Copilot;
    }

    pub fn take_input(&mut self) -> String {
        self.active = false;
        self.cursor = 0;
        self.reply_to = None;
        self.reply_mode = ReplyMode::Copilot;
        std::mem::take(&mut self.input)
    }

    // Cursor navigation helpers

    pub fn move_left(&mut self) {
        if self.cursor > 0 {
            self.cursor = prev_char_boundary(&self.input, self.cursor);
        }
    }

    pub fn move_right(&mut self) {
        if self.cursor < self.input.len() {
            self.cursor = next_char_boundary(&self.input, self.cursor);
        }
    }

    pub fn move_up(&mut self) {
        let (row, col) = cursor_row_col(&self.input, self.cursor);
        if row > 0 {
            self.cursor = offset_from_row_col(&self.input, row - 1, col);
        }
    }

    pub fn move_down(&mut self) {
        let (row, col) = cursor_row_col(&self.input, self.cursor);
        let line_count = self.input.split('\n').count();
        if row + 1 < line_count {
            self.cursor = offset_from_row_col(&self.input, row + 1, col);
        }
    }

    pub fn move_home(&mut self) {
        let line_start = self.input[..self.cursor].rfind('\n').map_or(0, |i| i + 1);
        self.cursor = line_start;
    }

    pub fn move_end(&mut self) {
        let line_end = self.input[self.cursor..]
            .find('\n')
            .map_or(self.input.len(), |i| self.cursor + i);
        self.cursor = line_end;
    }

    pub fn insert_char(&mut self, c: char) {
        self.input.insert(self.cursor, c);
        self.cursor += c.len_utf8();
    }

    pub fn insert_newline(&mut self) {
        self.input.insert(self.cursor, '\n');
        self.cursor += 1;
    }

    pub fn delete_back(&mut self) {
        if self.cursor > 0 {
            let prev = prev_char_boundary(&self.input, self.cursor);
            self.input.drain(prev..self.cursor);
            self.cursor = prev;
        }
    }

    pub fn delete_forward(&mut self) {
        if self.cursor < self.input.len() {
            let next = next_char_boundary(&self.input, self.cursor);
            self.input.drain(self.cursor..next);
        }
    }

    /// Write input to a temp file for $EDITOR. Returns the temp path.
    pub fn write_to_temp(&self) -> String {
        let tmp = std::env::temp_dir().join(format!("ghq-comment-{}.txt", std::process::id()));
        let _ = std::fs::write(&tmp, &self.input);
        tmp.to_string_lossy().to_string()
    }

    /// Read edited content back from temp file.
    pub fn read_from_temp(&mut self, path: &str) {
        if let Ok(content) = std::fs::read_to_string(path) {
            self.input = content.trim_end().to_string();
            self.cursor = self.input.len();
        }
        let _ = std::fs::remove_file(path);
    }
}

/// Find the previous char boundary before `pos`.
fn prev_char_boundary(s: &str, pos: usize) -> usize {
    let mut p = pos.saturating_sub(1);
    while p > 0 && !s.is_char_boundary(p) {
        p -= 1;
    }
    p
}

/// Find the next char boundary after `pos`.
fn next_char_boundary(s: &str, pos: usize) -> usize {
    let mut p = pos + 1;
    while p < s.len() && !s.is_char_boundary(p) {
        p += 1;
    }
    p.min(s.len())
}

/// Get (row, col) of a byte offset cursor (0-indexed, col in chars).
fn cursor_row_col(s: &str, cursor: usize) -> (usize, usize) {
    let before = &s[..cursor];
    let row = before.matches('\n').count();
    let line_start = before.rfind('\n').map_or(0, |i| i + 1);
    let col = before[line_start..].chars().count();
    (row, col)
}

/// Get byte offset from (row, col), clamped to line length.
fn offset_from_row_col(s: &str, row: usize, col: usize) -> usize {
    let mut offset = 0;
    for (i, line) in s.split('\n').enumerate() {
        if i == row {
            let mut char_count = 0;
            for (byte_offset, _) in line.char_indices() {
                if char_count == col {
                    return offset + byte_offset;
                }
                char_count += 1;
            }
            return offset + line.len();
        }
        offset += line.len() + 1;
    }
    s.len()
}

impl Default for ComposingState {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn new_is_inactive() {
        let state = ComposingState::new();
        assert!(!state.is_active());
        assert!(state.input.is_empty());
    }

    #[test]
    fn start_new_activates() {
        let mut state = ComposingState::new();
        state.start_new("file.rs".into(), 10, "RIGHT".into());
        assert!(state.is_active());
        assert_eq!(state.path, "file.rs");
        assert_eq!(state.line, 10);
        assert_eq!(state.side, "RIGHT");
        assert!(state.reply_to.is_none());
    }

    #[test]
    fn start_reply_sets_reply_to() {
        let mut state = ComposingState::new();
        state.start_reply("parent-id".into(), "file.rs".into(), 5, "LEFT".into());
        assert!(state.is_active());
        assert_eq!(state.reply_to.as_deref(), Some("parent-id"));
    }

    #[test]
    fn cancel_deactivates() {
        let mut state = ComposingState::new();
        state.start_new("file.rs".into(), 10, "RIGHT".into());
        state.input.push_str("hello");
        state.cancel();
        assert!(!state.is_active());
        assert!(state.input.is_empty());
    }

    #[test]
    fn take_input_returns_and_deactivates() {
        let mut state = ComposingState::new();
        state.start_new("file.rs".into(), 10, "RIGHT".into());
        state.input.push_str("my comment");
        let text = state.take_input();
        assert_eq!(text, "my comment");
        assert!(!state.is_active());
        assert!(state.input.is_empty());
    }
}
