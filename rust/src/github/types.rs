use serde::Deserialize;

#[derive(Debug, Clone, Deserialize)]
pub struct PullRequest {
    pub number: u64,
    pub title: String,
    pub body: Option<String>,
    pub state: String,
    pub draft: Option<bool>,
    pub merged: Option<bool>,
    pub user: User,
    pub head: Branch,
    pub base: Branch,
    pub labels: Option<Vec<Label>>,
    pub html_url: Option<String>,
    pub created_at: Option<String>,
    pub updated_at: Option<String>,
    pub mergeable: Option<bool>,
    pub mergeable_state: Option<String>,
    pub review_comments: Option<u64>,
    pub comments: Option<u64>,
    pub additions: Option<i32>,
    pub deletions: Option<i32>,
    pub changed_files: Option<i32>,
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
    pub id: Option<u64>,
    pub avatar_url: Option<String>,
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
    pub full_name: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Label {
    pub name: String,
    pub color: Option<String>,
    pub description: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Review {
    pub id: u64,
    pub user: User,
    pub body: Option<String>,
    pub state: String,
    pub submitted_at: Option<String>,
    pub html_url: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct IssueComment {
    pub id: u64,
    pub user: User,
    pub body: String,
    pub created_at: String,
    pub updated_at: Option<String>,
    pub html_url: Option<String>,
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
    pub diff_hunk: Option<String>,
    pub in_reply_to_id: Option<u64>,
    pub created_at: String,
    pub updated_at: Option<String>,
    pub html_url: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct CheckRun {
    pub id: u64,
    pub name: String,
    pub status: String,
    pub conclusion: Option<String>,
    pub html_url: Option<String>,
    pub started_at: Option<String>,
    pub completed_at: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct CheckRunsResponse {
    pub total_count: u64,
    pub check_runs: Vec<CheckRun>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct PullRequestFile {
    pub filename: String,
    pub status: String,
    #[serde(default)]
    pub previous_filename: String,
    #[serde(default)]
    pub patch: String,
    #[serde(default)]
    pub additions: i32,
    #[serde(default)]
    pub deletions: i32,
}
