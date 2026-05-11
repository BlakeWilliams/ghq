use std::path::PathBuf;

use serde::Deserialize;

const DEFAULT_COMMENT_PANEL_MIN_WIDTH: u16 = 40;
const DEFAULT_DIFF_MIN_WIDTH: u16 = 80;

#[derive(Debug, Clone, Deserialize)]
#[serde(default)]
pub struct Config {
    pub help_mode: Option<String>,
    pub commit_prompt: Option<String>,
    pub pr_prompt: Option<String>,
    pub comment_panel_min_width: u16,
    pub diff_min_width: u16,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            help_mode: None,
            commit_prompt: None,
            pr_prompt: None,
            comment_panel_min_width: DEFAULT_COMMENT_PANEL_MIN_WIDTH,
            diff_min_width: DEFAULT_DIFF_MIN_WIDTH,
        }
    }
}

impl Config {
    pub fn load() -> Self {
        match Self::path() {
            Some(path) => Self::load_from(&path),
            None => Self::default(),
        }
    }

    pub fn load_from(path: &std::path::Path) -> Self {
        let contents = match std::fs::read_to_string(path) {
            Ok(c) => c,
            Err(_) => return Self::default(),
        };
        let mut config: Config = match serde_yaml::from_str(&contents) {
            Ok(c) => c,
            Err(_) => return Self::default(),
        };
        config.clamp_minimums();
        config
    }

    fn clamp_minimums(&mut self) {
        if self.comment_panel_min_width < DEFAULT_COMMENT_PANEL_MIN_WIDTH {
            self.comment_panel_min_width = DEFAULT_COMMENT_PANEL_MIN_WIDTH;
        }
        if self.diff_min_width < DEFAULT_DIFF_MIN_WIDTH {
            self.diff_min_width = DEFAULT_DIFF_MIN_WIDTH;
        }
    }

    pub fn dir() -> Option<PathBuf> {
        if let Ok(xdg) = std::env::var("XDG_CONFIG_HOME") {
            Some(PathBuf::from(xdg).join("gg"))
        } else {
            dirs::home_dir().map(|h| h.join(".config").join("gg"))
        }
    }

    pub fn path() -> Option<PathBuf> {
        Self::dir().map(|d| d.join("config.yaml"))
    }
}
