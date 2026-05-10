pub struct ComposingState {
    active: bool,
    pub input: String,
    pub path: String,
    pub line: i32,
    pub side: String,
    pub reply_to: Option<String>,
}

impl ComposingState {
    pub fn new() -> Self {
        Self {
            active: false,
            input: String::new(),
            path: String::new(),
            line: 0,
            side: String::new(),
            reply_to: None,
        }
    }

    pub fn is_active(&self) -> bool {
        self.active
    }

    pub fn start_new(&mut self, path: String, line: i32, side: String) {
        self.active = true;
        self.input.clear();
        self.path = path;
        self.line = line;
        self.side = side;
        self.reply_to = None;
    }

    pub fn start_reply(&mut self, reply_to: String, path: String, line: i32, side: String) {
        self.active = true;
        self.input.clear();
        self.path = path;
        self.line = line;
        self.side = side;
        self.reply_to = Some(reply_to);
    }

    pub fn cancel(&mut self) {
        self.active = false;
        self.input.clear();
        self.reply_to = None;
    }

    pub fn take_input(&mut self) -> String {
        self.active = false;
        self.reply_to = None;
        std::mem::take(&mut self.input)
    }
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
