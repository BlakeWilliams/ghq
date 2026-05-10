pub struct Chat {
    pub visible: bool,
    pub messages: Vec<ChatMessage>,
    pub input: String,
    pub scroll_offset: usize,
    pub streaming: bool,
}

pub struct ChatMessage {
    pub author: ChatAuthor,
    pub body: String,
}

pub enum ChatAuthor {
    User,
    Copilot,
}

impl Chat {
    pub fn new() -> Self {
        Self {
            visible: false,
            messages: vec![ChatMessage {
                author: ChatAuthor::Copilot,
                body: "Hi! I'm Copilot. Ask me anything about your code.".to_string(),
            }],
            input: String::new(),
            scroll_offset: 0,
            streaming: false,
        }
    }

    pub fn toggle(&mut self) {
        self.visible = !self.visible;
    }

    pub fn send_message(&mut self) -> Option<String> {
        let text = self.input.trim().to_string();
        if text.is_empty() {
            return None;
        }
        self.messages.push(ChatMessage {
            author: ChatAuthor::User,
            body: text.clone(),
        });
        self.input.clear();
        self.streaming = true;
        Some(text)
    }

    pub fn append_delta(&mut self, text: &str) {
        if let Some(last) = self.messages.last_mut() {
            if matches!(last.author, ChatAuthor::Copilot) && self.streaming {
                last.body.push_str(text);
                return;
            }
        }
        self.messages.push(ChatMessage {
            author: ChatAuthor::Copilot,
            body: text.to_string(),
        });
    }

    pub fn finish_streaming(&mut self) {
        self.streaming = false;
    }

    pub fn scroll_down(&mut self, n: usize) {
        self.scroll_offset += n;
    }

    pub fn scroll_up(&mut self, n: usize) {
        self.scroll_offset = self.scroll_offset.saturating_sub(n);
    }
}

impl Default for Chat {
    fn default() -> Self {
        Self::new()
    }
}
