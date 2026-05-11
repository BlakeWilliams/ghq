pub mod anchor;
pub mod comments;

use std::collections::HashMap;

use crate::github::types::PullRequestFile;

pub struct Session {
    pub files: HashMap<String, File>,
    pub file_order: Vec<String>,
    pub current_file: usize,
    pub diff_cursor: usize,
}

pub struct File {
    pub filename: String,
    pub status: String,
    pub patch: String,
    pub additions: i32,
    pub deletions: i32,
    pub previous_filename: String,
    pub local_comments: Vec<comments::LocalComment>,
}

impl Session {
    pub fn new() -> Self {
        Self {
            files: HashMap::new(),
            file_order: Vec::new(),
            current_file: 0,
            diff_cursor: 0,
        }
    }

    pub fn set_files(&mut self, pr_files: Vec<PullRequestFile>) {
        self.file_order.clear();
        let mut new_files = HashMap::new();

        for pf in pr_files {
            let filename = pf.filename.clone();
            self.file_order.push(filename.clone());

            // Preserve local comments if the file already exists.
            let local_comments = self
                .files
                .get(&filename)
                .map(|f| f.local_comments.clone())
                .unwrap_or_default();

            new_files.insert(
                filename.clone(),
                File {
                    filename,
                    status: pf.status,
                    patch: pf.patch,
                    additions: pf.additions,
                    deletions: pf.deletions,
                    previous_filename: pf.previous_filename,
                    local_comments,
                },
            );
        }

        self.files = new_files;
    }

    pub fn current_filename(&self) -> Option<&str> {
        self.file_order
            .get(self.current_file)
            .map(|s| s.as_str())
    }

    pub fn current_file(&self) -> Option<&File> {
        self.current_filename()
            .and_then(|name| self.files.get(name))
    }

    pub fn file_count(&self) -> usize {
        self.file_order.len()
    }
}

impl Default for Session {
    fn default() -> Self {
        Self::new()
    }
}
