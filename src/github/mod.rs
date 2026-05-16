pub use cached::CachedClient;
pub mod cached;
pub mod types;

use anyhow::{Context, Result};

fn format_octocrab_error(url: &str, err: octocrab::Error) -> anyhow::Error {
    match err {
        octocrab::Error::GitHub { source, .. } => {
            anyhow::anyhow!("{} {}: {}", source.status_code.as_u16(), url, source.message)
        }
        other => anyhow::anyhow!("POST {url}: {other}"),
    }
}

pub struct Client {
    octocrab: octocrab::Octocrab,
}

impl Client {
    pub fn new() -> Result<Self> {
        let token = Self::resolve_token().context("no GitHub token found")?;
        let octocrab = octocrab::Octocrab::builder()
            .personal_token(token)
            .build()
            .context("failed to build octocrab client")?;
        Ok(Self { octocrab })
    }

    fn resolve_token() -> Option<String> {
        if let Ok(token) = std::env::var("GITHUB_TOKEN") {
            return Some(token);
        }
        if let Ok(token) = std::env::var("GH_TOKEN") {
            return Some(token);
        }
        // Try gh auth token
        std::process::Command::new("gh")
            .args(["auth", "token"])
            .output()
            .ok()
            .and_then(|o| {
                if o.status.success() {
                    Some(String::from_utf8_lossy(&o.stdout).trim().to_string())
                } else {
                    None
                }
            })
    }

    pub async fn authenticated_user(&self) -> Result<types::User> {
        let user: types::User = self
            .octocrab
            .get("/user", None::<&()>)
            .await
            .context("failed to fetch authenticated user")?;
        Ok(user)
    }

    pub async fn pull_request_by_branch(
        &self,
        owner: &str,
        repo: &str,
        branch: &str,
    ) -> Result<Option<types::PullRequest>> {
        let prs: Vec<types::PullRequest> = self
            .octocrab
            .get(
                format!("/repos/{owner}/{repo}/pulls"),
                Some(&[("head", &format!("{owner}:{branch}")), ("state", &"open".to_string())]),
            )
            .await
            .context("failed to search PRs by branch")?;
        Ok(prs.into_iter().next())
    }

    pub async fn review_comments(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
    ) -> Result<Vec<types::ReviewComment>> {
        let comments: Vec<types::ReviewComment> = self
            .octocrab
            .get(
                format!("/repos/{owner}/{repo}/pulls/{number}/comments"),
                Some(&[("per_page", "100")]),
            )
            .await
            .context("failed to fetch review comments")?;
        Ok(comments)
    }

    pub async fn create_review_comment(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
        body: &str,
        commit_id: &str,
        path: &str,
        line: i32,
        side: &str,
    ) -> Result<types::ReviewComment> {
        let url = format!("/repos/{owner}/{repo}/pulls/{number}/comments");
        let comment: types::ReviewComment = self
            .octocrab
            .post(
                &url,
                Some(&serde_json::json!({
                    "body": body,
                    "commit_id": commit_id,
                    "path": path,
                    "line": line,
                    "side": side,
                })),
            )
            .await
            .map_err(|e| anyhow::anyhow!("POST {url}: {e}"))?;
        Ok(comment)
    }

    pub async fn reply_to_comment(
        &self,
        owner: &str,
        repo: &str,
        _number: u64,
        comment_id: u64,
        body: &str,
    ) -> Result<types::ReviewComment> {
        let url = format!("/repos/{owner}/{repo}/pulls/comments/{comment_id}/replies");
        let comment: types::ReviewComment = self
            .octocrab
            .post(&url, Some(&serde_json::json!({ "body": body })))
            .await
            .map_err(|e| format_octocrab_error(&url, e))?;
        Ok(comment)
    }
}
