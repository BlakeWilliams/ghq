use serde::Deserialize;

#[derive(Debug, Clone, Deserialize)]
pub struct PullRequest {
    pub number: u64,
    pub head: Branch,
    pub base: Branch,
    pub html_url: Option<String>,
}

impl PullRequest {
    pub fn repo_owner(&self) -> &str {
        &self.base.repo.owner.login
    }

    pub fn repo_name(&self) -> &str {
        &self.base.repo.name
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct User {
    pub login: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Branch {
    #[serde(rename = "ref")]
    pub ref_name: String,
    pub sha: String,
    pub repo: BranchRepo,
}

#[derive(Debug, Clone, Deserialize)]
pub struct BranchRepo {
    pub name: String,
    pub owner: User,
}

#[derive(Debug, Clone, Deserialize)]
pub struct ReviewComment {
    pub id: u64,
    pub user: User,
    pub body: String,
    pub path: String,
    pub line: Option<i32>,
    pub original_line: Option<i32>,
    pub side: Option<String>,
    pub in_reply_to_id: Option<u64>,
    pub created_at: String,
    pub updated_at: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct PullRequestFile {
    pub filename: String,
    pub status: String,
    #[serde(default)]
    pub patch: String,
    #[serde(default)]
    pub additions: i32,
    #[serde(default)]
    pub deletions: i32,
}
