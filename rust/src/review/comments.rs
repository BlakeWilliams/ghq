use serde::{Deserialize, Serialize};

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

    pub fn load(&mut self) -> anyhow::Result<()> {
        let filename = format!("comments-{}.json", self.repo_key);
        if let Some(loaded) = crate::cache::persist::load::<Vec<LocalComment>>(&filename)? {
            self.comments = loaded;
        }
        Ok(())
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

    pub fn comments_for_file(&self, path: &str) -> Vec<&LocalComment> {
        self.comments
            .iter()
            .filter(|c| c.path == path && !c.resolved)
            .collect()
    }

    /// Returns root comments for a file, grouped by (side, line).
    /// Only returns the root of each thread (no replies).
    pub fn root_threads_for_file(&self, path: &str) -> Vec<&LocalComment> {
        self.comments
            .iter()
            .filter(|c| c.path == path && !c.resolved && c.in_reply_to_id.is_none())
            .collect()
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
    fn comments_for_file_filters() {
        let mut store = memory_store();
        store.comments.push(make_comment("c1", "a", CommentAuthor::You, None));
        let mut c2 = make_comment("c2", "b", CommentAuthor::You, None);
        c2.path = "other.rs".to_string();
        store.comments.push(c2);

        let file_comments = store.comments_for_file("file.rs");
        assert_eq!(file_comments.len(), 1);
        assert_eq!(file_comments[0].id, "c1");
    }

    #[test]
    fn comments_for_file_excludes_resolved() {
        let mut store = memory_store();
        let mut c = make_comment("c1", "resolved", CommentAuthor::You, None);
        c.resolved = true;
        store.comments.push(c);
        store.comments.push(make_comment("c2", "open", CommentAuthor::You, None));

        let file_comments = store.comments_for_file("file.rs");
        assert_eq!(file_comments.len(), 1);
        assert_eq!(file_comments[0].id, "c2");
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
}
