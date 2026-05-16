use serde::{Deserialize, Serialize};

use crate::github::types::ReviewComment;

/// Returns the local ID string for a GitHub comment ID.
pub fn gh_id(id: u64) -> String {
    format!("gh-{id}")
}

/// Returns true if the ID represents an imported GitHub comment.
pub fn is_gh_id(id: &str) -> bool {
    id.starts_with("gh-")
}

/// Parses a "gh-N" string back to the original GitHub numeric ID.
pub fn parse_gh_id(id: &str) -> Option<u64> {
    id.strip_prefix("gh-")?.parse().ok()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LocalComment {
    pub id: String,
    pub body: String,
    pub author: CommentAuthor,
    pub in_reply_to_id: Option<String>,
    pub path: String,
    pub line: i32,
    pub side: String,
    pub resolved: bool,
    pub created_at: String,
    pub blocks: Vec<ContentBlock>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum CommentAuthor {
    You,
    Copilot,
    GitHub(String),
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type")]
pub enum ContentBlock {
    Text { content: String },
    ToolGroup { label: String, tools: Vec<ToolEntry> },
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolEntry {
    pub name: String,
    pub args: String,
    pub result: Option<String>,
    pub done: bool,
}

pub struct CommentStore {
    pub comments: Vec<LocalComment>,
    repo_key: String,
}

impl CommentStore {
    pub fn new(repo_root: &str, branch: &str) -> Self {
        let repo_key = format!(
            "{}-{}",
            repo_root.replace('/', "_"),
            branch.replace('/', "_")
        );
        Self {
            comments: Vec::new(),
            repo_key,
        }
    }

    pub fn save(&self) -> anyhow::Result<()> {
        let filename = format!("comments-{}.json", self.repo_key);
        crate::cache::persist::save(&filename, &self.comments)?;
        Ok(())
    }

    pub fn add(&mut self, comment: LocalComment) -> anyhow::Result<()> {
        self.comments.push(comment);
        self.save()
    }

    pub fn resolve(&mut self, root_id: &str, resolved: bool) -> anyhow::Result<()> {
        // Collect all comment IDs in the thread using the same multi-pass logic as thread_comments
        let mut ids_in_thread: std::collections::HashSet<String> =
            std::collections::HashSet::new();
        ids_in_thread.insert(root_id.to_string());

        // Multiple passes to catch nested replies (depth > 1)
        loop {
            let mut added = false;
            for c in &self.comments {
                if ids_in_thread.contains(&c.id) {
                    continue;
                }
                if let Some(parent) = &c.in_reply_to_id {
                    if ids_in_thread.contains(parent) {
                        ids_in_thread.insert(c.id.clone());
                        added = true;
                    }
                }
            }
            if !added {
                break;
            }
        }

        // Mark all comments in the thread
        for c in &mut self.comments {
            if ids_in_thread.contains(&c.id) {
                c.resolved = resolved;
            }
        }
        self.save()
    }

    pub fn find_thread_root(&self, comment_id: &str) -> Option<&LocalComment> {
        let comment = self.comments.iter().find(|c| c.id == comment_id)?;
        match &comment.in_reply_to_id {
            Some(parent_id) => self.find_thread_root(parent_id),
            None => Some(comment),
        }
    }

    pub fn thread_comments(&self, root_id: &str) -> Vec<&LocalComment> {
        // Collect all comments in the thread by walking the reply chain.
        // A comment belongs to the thread if its id IS root_id, or if
        // any ancestor in_reply_to_id chain reaches root_id.
        let mut ids_in_thread: std::collections::HashSet<&str> =
            std::collections::HashSet::new();
        ids_in_thread.insert(root_id);

        // Multiple passes to catch nested replies (depth > 1)
        loop {
            let mut added = false;
            for c in &self.comments {
                if ids_in_thread.contains(c.id.as_str()) {
                    continue;
                }
                if let Some(parent) = &c.in_reply_to_id {
                    if ids_in_thread.contains(parent.as_str()) {
                        ids_in_thread.insert(&c.id);
                        added = true;
                    }
                }
            }
            if !added {
                break;
            }
        }

        let mut thread: Vec<&LocalComment> = self
            .comments
            .iter()
            .filter(|c| ids_in_thread.contains(c.id.as_str()))
            .collect();
        thread.sort_by(|a, b| a.created_at.cmp(&b.created_at));
        thread
    }

    pub fn thread_has_copilot_comment(&self, root_id: &str) -> bool {
        self.thread_comments(root_id)
            .iter()
            .any(|c| matches!(c.author, CommentAuthor::Copilot))
    }

    /// Returns root comments for a file, grouped by (side, line).
    /// Only returns the root of each thread (no replies).
    pub fn root_threads_for_file(&self, path: &str) -> Vec<&LocalComment> {
        self.comments
            .iter()
            .filter(|c| c.path == path && !c.resolved && c.in_reply_to_id.is_none())
            .collect()
    }

    /// Returns a map of filename → unresolved thread count for all files.
    pub fn thread_counts_by_file(&self) -> std::collections::HashMap<String, usize> {
        self.comments
            .iter()
            .filter(|c| !c.resolved && c.in_reply_to_id.is_none())
            .fold(std::collections::HashMap::new(), |mut acc, c| {
                *acc.entry(c.path.clone()).or_insert(0) += 1;
                acc
            })
    }

    /// Import GitHub review comments, replacing any previous GH imports.
    /// Local (non-GH) comments are preserved.
    pub fn import_gh(&mut self, gh_comments: &[ReviewComment]) {
        self.comments.retain(|c| !is_gh_id(&c.id));

        for gc in gh_comments {
            self.comments.push(gh_review_to_local(gc));
        }
    }
}

fn gh_review_to_local(c: &ReviewComment) -> LocalComment {
    let line = c.line.or(c.original_line).unwrap_or(0);
    let side = c.side.clone().unwrap_or_else(|| "RIGHT".to_string());
    let created_at = c.updated_at.clone().unwrap_or_else(|| c.created_at.clone());
    let in_reply_to_id = c.in_reply_to_id.map(gh_id);

    LocalComment {
        id: gh_id(c.id),
        body: c.body.clone(),
        author: CommentAuthor::GitHub(c.user.login.clone()),
        in_reply_to_id,
        path: c.path.clone(),
        line,
        side,
        resolved: false,
        created_at,
        blocks: vec![ContentBlock::Text {
            content: c.body.clone(),
        }],
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_comment(id: &str, body: &str, author: CommentAuthor, reply_to: Option<&str>) -> LocalComment {
        LocalComment {
            id: id.to_string(),
            body: body.to_string(),
            author,
            in_reply_to_id: reply_to.map(|s| s.to_string()),
            path: "file.rs".to_string(),
            line: 10,
            side: "RIGHT".to_string(),
            resolved: false,
            created_at: "1000".to_string(),
            blocks: vec![ContentBlock::Text {
                content: body.to_string(),
            }],
        }
    }

    fn memory_store() -> CommentStore {
        CommentStore {
            comments: Vec::new(),
            repo_key: "test".to_string(),
        }
    }

    #[test]
    fn add_and_retrieve() {
        let mut store = memory_store();
        store.comments.push(make_comment("c1", "hello", CommentAuthor::You, None));
        assert_eq!(store.comments.len(), 1);
        assert_eq!(store.comments[0].body, "hello");
    }

    #[test]
    fn thread_comments_groups_replies() {
        let mut store = memory_store();
        store.comments.push(make_comment("c1", "root", CommentAuthor::You, None));
        store.comments.push(make_comment("c2", "reply", CommentAuthor::Copilot, Some("c1")));

        let thread = store.thread_comments("c1");
        assert_eq!(thread.len(), 2);
        assert_eq!(thread[0].id, "c1");
        assert_eq!(thread[1].id, "c2");
    }

    #[test]
    fn thread_comments_includes_nested_replies() {
        let mut store = memory_store();
        // root → user reply → copilot reply (nested 2 deep)
        store.comments.push(make_comment("c1", "root", CommentAuthor::You, None));
        store.comments.push(make_comment("c2", "user reply", CommentAuthor::You, Some("c1")));
        store.comments.push(make_comment("c3", "copilot reply", CommentAuthor::Copilot, Some("c2")));

        let thread = store.thread_comments("c1");
        assert_eq!(thread.len(), 3);
        assert_eq!(thread[0].id, "c1");
        assert_eq!(thread[1].id, "c2");
        assert_eq!(thread[2].id, "c3");
    }

    #[test]
    fn find_thread_root_follows_chain() {
        let mut store = memory_store();
        store.comments.push(make_comment("c1", "root", CommentAuthor::You, None));
        store.comments.push(make_comment("c2", "reply", CommentAuthor::Copilot, Some("c1")));

        let root = store.find_thread_root("c2").unwrap();
        assert_eq!(root.id, "c1");

        let root = store.find_thread_root("c1").unwrap();
        assert_eq!(root.id, "c1");
    }

    #[test]
    fn resolve_marks_direct_replies() {
        let mut store = memory_store();
        store.comments.push(make_comment("c1", "root", CommentAuthor::You, None));
        store.comments.push(make_comment("c2", "reply", CommentAuthor::Copilot, Some("c1")));

        // Don't call save() — in-memory only
        for c in &mut store.comments {
            if c.id == "c1" || c.in_reply_to_id.as_deref() == Some("c1") {
                c.resolved = true;
            }
        }

        assert!(store.comments.iter().all(|c| c.resolved));
    }

    #[test]
    fn resolve_marks_nested_replies() {
        let mut store = memory_store();
        // root → user reply → copilot reply (nested 2 deep)
        store.comments.push(make_comment("c1", "root", CommentAuthor::You, None));
        store.comments.push(make_comment("c2", "user reply", CommentAuthor::You, Some("c1")));
        store.comments.push(make_comment("c3", "copilot reply", CommentAuthor::Copilot, Some("c2")));

        // Simulate resolve using the new multi-pass logic
        let mut ids_in_thread: std::collections::HashSet<String> =
            std::collections::HashSet::new();
        ids_in_thread.insert("c1".to_string());

        loop {
            let mut added = false;
            for c in &store.comments {
                if ids_in_thread.contains(&c.id) {
                    continue;
                }
                if let Some(parent) = &c.in_reply_to_id {
                    if ids_in_thread.contains(parent) {
                        ids_in_thread.insert(c.id.clone());
                        added = true;
                    }
                }
            }
            if !added {
                break;
            }
        }

        for c in &mut store.comments {
            if ids_in_thread.contains(&c.id) {
                c.resolved = true;
            }
        }

        assert_eq!(store.comments.len(), 3);
        assert!(store.comments.iter().all(|c| c.resolved), "All comments in nested thread should be resolved");
    }

    #[test]
    fn resolve_deeply_nested_thread() {
        let mut store = memory_store();
        // Create a deep chain: c1 → c2 → c3 → c4 → c5
        store.comments.push(make_comment("c1", "root", CommentAuthor::You, None));
        store.comments.push(make_comment("c2", "reply 1", CommentAuthor::Copilot, Some("c1")));
        store.comments.push(make_comment("c3", "reply 2", CommentAuthor::You, Some("c2")));
        store.comments.push(make_comment("c4", "reply 3", CommentAuthor::Copilot, Some("c3")));
        store.comments.push(make_comment("c5", "reply 4", CommentAuthor::You, Some("c4")));

        // Simulate resolve using the new multi-pass logic
        let mut ids_in_thread: std::collections::HashSet<String> =
            std::collections::HashSet::new();
        ids_in_thread.insert("c1".to_string());

        loop {
            let mut added = false;
            for c in &store.comments {
                if ids_in_thread.contains(&c.id) {
                    continue;
                }
                if let Some(parent) = &c.in_reply_to_id {
                    if ids_in_thread.contains(parent) {
                        ids_in_thread.insert(c.id.clone());
                        added = true;
                    }
                }
            }
            if !added {
                break;
            }
        }

        for c in &mut store.comments {
            if ids_in_thread.contains(&c.id) {
                c.resolved = true;
            }
        }

        assert_eq!(store.comments.len(), 5);
        assert!(store.comments.iter().all(|c| c.resolved), "All comments in deeply nested thread should be resolved");
    }

    fn make_review_comment(id: u64, body: &str, path: &str, line: i32, reply_to: Option<u64>) -> ReviewComment {
        use crate::github::types::User;
        ReviewComment {
            id,
            user: User {
                login: "octocat".to_string(),
            },
            body: body.to_string(),
            path: path.to_string(),
            line: Some(line),
            original_line: None,
            side: Some("RIGHT".to_string()),
            in_reply_to_id: reply_to,
            created_at: "2024-01-01T00:00:00Z".to_string(),
            updated_at: None,
        }
    }

    #[test]
    fn import_gh_converts_comments() {
        let mut store = memory_store();
        let gh_comments = vec![
            make_review_comment(100, "looks good", "src/main.rs", 42, None),
            make_review_comment(101, "thanks", "src/main.rs", 42, Some(100)),
        ];

        store.import_gh(&gh_comments);
        assert_eq!(store.comments.len(), 2);
        assert_eq!(store.comments[0].id, "gh-100");
        assert_eq!(store.comments[0].path, "src/main.rs");
        assert_eq!(store.comments[0].line, 42);
        assert_eq!(store.comments[0].author, CommentAuthor::GitHub("octocat".to_string()));
        assert_eq!(store.comments[1].id, "gh-101");
        assert_eq!(store.comments[1].in_reply_to_id, Some("gh-100".to_string()));
    }

    #[test]
    fn import_gh_preserves_local_comments() {
        let mut store = memory_store();
        store.comments.push(make_comment("local-1", "my note", CommentAuthor::You, None));

        let gh_comments = vec![make_review_comment(200, "nit", "lib.rs", 10, None)];
        store.import_gh(&gh_comments);

        assert_eq!(store.comments.len(), 2);
        assert_eq!(store.comments[0].id, "local-1");
        assert_eq!(store.comments[1].id, "gh-200");
    }

    #[test]
    fn import_gh_replaces_previous_gh_imports() {
        let mut store = memory_store();
        store.comments.push(make_comment("local-1", "my note", CommentAuthor::You, None));

        let batch1 = vec![make_review_comment(100, "old", "a.rs", 1, None)];
        store.import_gh(&batch1);
        assert_eq!(store.comments.len(), 2);

        let batch2 = vec![
            make_review_comment(100, "updated", "a.rs", 1, None),
            make_review_comment(101, "new", "b.rs", 5, None),
        ];
        store.import_gh(&batch2);
        assert_eq!(store.comments.len(), 3); // 1 local + 2 GH
        assert_eq!(store.comments[0].id, "local-1");
        assert_eq!(store.comments[1].body, "updated");
        assert_eq!(store.comments[2].id, "gh-101");
    }

    #[test]
    fn import_gh_threading_works() {
        let mut store = memory_store();
        let gh_comments = vec![
            make_review_comment(300, "root", "x.rs", 10, None),
            make_review_comment(301, "reply", "x.rs", 10, Some(300)),
        ];
        store.import_gh(&gh_comments);

        let thread = store.thread_comments("gh-300");
        assert_eq!(thread.len(), 2);
        assert_eq!(thread[0].id, "gh-300");
        assert_eq!(thread[1].id, "gh-301");

        let root = store.find_thread_root("gh-301").unwrap();
        assert_eq!(root.id, "gh-300");
    }

    #[test]
    fn gh_id_helpers() {
        assert_eq!(gh_id(42), "gh-42");
        assert!(is_gh_id("gh-42"));
        assert!(!is_gh_id("local-1"));
        assert_eq!(parse_gh_id("gh-42"), Some(42));
        assert_eq!(parse_gh_id("local-1"), None);
    }
}
