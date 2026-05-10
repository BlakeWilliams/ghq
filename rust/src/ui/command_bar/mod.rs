pub struct CommandBar {
    pub visible: bool,
    pub input: String,
    pub commands: Vec<CommandDef>,
}

pub struct CommandDef {
    pub name: &'static str,
    pub description: &'static str,
}

pub enum CommandResult {
    Execute(String, Vec<String>),
    Cancel,
}

impl CommandBar {
    pub fn new() -> Self {
        Self {
            visible: false,
            input: String::new(),
            commands: vec![
                CommandDef { name: "quit", description: "Quit the application" },
                CommandDef { name: "refresh", description: "Refresh the current view" },
                CommandDef { name: "reset-expansions", description: "Reset all expanded threads" },
                CommandDef { name: "back", description: "Go back to previous view" },
                CommandDef { name: "inbox", description: "Open inbox" },
                CommandDef { name: "local", description: "Switch to local diff view" },
                CommandDef { name: "pr", description: "Open PR view" },
            ],
        }
    }

    pub fn open(&mut self) {
        self.visible = true;
        self.input.clear();
    }

    pub fn close(&mut self) {
        self.visible = false;
        self.input.clear();
    }

    pub fn ghost_completion(&self) -> Option<&str> {
        if self.input.is_empty() {
            return None;
        }
        self.commands
            .iter()
            .find(|c| c.name.starts_with(&self.input) && c.name != self.input)
            .map(|c| &c.name[self.input.len()..])
    }

    pub fn submit(&mut self) -> CommandResult {
        let input = self.input.trim().to_string();
        self.close();
        if input.is_empty() {
            return CommandResult::Cancel;
        }
        let parts: Vec<String> = input.split_whitespace().map(|s| s.to_string()).collect();
        let cmd = parts[0].clone();
        let args = parts[1..].to_vec();
        CommandResult::Execute(cmd, args)
    }

    pub fn accept_completion(&mut self) {
        if let Some(ghost) = self.ghost_completion().map(|s| s.to_string()) {
            self.input.push_str(&ghost);
        }
    }
}

impl Default for CommandBar {
    fn default() -> Self {
        Self::new()
    }
}
