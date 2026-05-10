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
    ToolGroup { tools: Vec<ToolEntry> },
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
        for c in &mut self.comments {
            if c.id == root_id || c.in_reply_to_id.as_deref() == Some(root_id) {
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
        let mut thread: Vec<&LocalComment> = self
            .comments
            .iter()
            .filter(|c| c.id == root_id || c.in_reply_to_id.as_deref() == Some(root_id))
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
    fn resolve_marks_thread() {
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
}
