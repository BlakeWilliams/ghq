use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};

use tokio::sync::RwLock;

use super::Client;
use super::types::*;

const GC_INTERVAL: Duration = Duration::from_secs(60);
const STALE_TIME: Duration = Duration::from_secs(30);
const GC_TIME: Duration = Duration::from_secs(300);

struct CacheEntry<T> {
    data: T,
    fetched_at: Instant,
    last_accessed: Instant,
}

pub struct CachedClient {
    client: Client,
    pr_list: Arc<RwLock<HashMap<String, CacheEntry<Vec<PullRequest>>>>>,
    reviews: Arc<RwLock<HashMap<String, CacheEntry<Vec<Review>>>>>,
    review_comments: Arc<RwLock<HashMap<String, CacheEntry<Vec<ReviewComment>>>>>,
    issue_comments: Arc<RwLock<HashMap<String, CacheEntry<Vec<IssueComment>>>>>,
    pr_files: Arc<RwLock<HashMap<String, CacheEntry<Vec<PullRequestFile>>>>>,
    check_runs: Arc<RwLock<HashMap<String, CacheEntry<CheckRunsResponse>>>>,
}

impl CachedClient {
    pub fn new(client: Client) -> Self {
        Self {
            client,
            pr_list: Arc::new(RwLock::new(HashMap::new())),
            reviews: Arc::new(RwLock::new(HashMap::new())),
            review_comments: Arc::new(RwLock::new(HashMap::new())),
            issue_comments: Arc::new(RwLock::new(HashMap::new())),
            pr_files: Arc::new(RwLock::new(HashMap::new())),
            check_runs: Arc::new(RwLock::new(HashMap::new())),
        }
    }

    pub fn gc_interval(&self) -> Duration {
        GC_INTERVAL
    }

    pub async fn gc(&self) {
        let now = Instant::now();
        Self::gc_map(&self.pr_list, now, GC_TIME).await;
        Self::gc_map(&self.reviews, now, GC_TIME).await;
        Self::gc_map(&self.review_comments, now, GC_TIME).await;
        Self::gc_map(&self.issue_comments, now, GC_TIME).await;
        Self::gc_map(&self.pr_files, now, GC_TIME).await;
        Self::gc_map(&self.check_runs, now, GC_TIME).await;
    }

    async fn gc_map<T>(map: &Arc<RwLock<HashMap<String, CacheEntry<T>>>>, now: Instant, max_age: Duration) {
        let mut m = map.write().await;
        m.retain(|_, v| now.duration_since(v.last_accessed) < max_age);
    }

    pub async fn invalidate_all(&self) {
        self.pr_list.write().await.clear();
        self.reviews.write().await.clear();
        self.review_comments.write().await.clear();
        self.issue_comments.write().await.clear();
        self.pr_files.write().await.clear();
        self.check_runs.write().await.clear();
    }

    pub async fn authenticated_user(&self) -> anyhow::Result<User> {
        self.client.authenticated_user().await
    }

    pub async fn pull_requests(
        &self,
        owner: &str,
        repo: &str,
    ) -> anyhow::Result<Vec<PullRequest>> {
        let key = format!("{owner}/{repo}/pulls");
        if let Some(cached) = self.get_cached(&self.pr_list, &key).await {
            return Ok(cached);
        }
        let data = self.client.pull_requests(owner, repo).await?;
        self.set_cached(&self.pr_list, key, data.clone()).await;
        Ok(data)
    }

    pub async fn reviews(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
    ) -> anyhow::Result<Vec<Review>> {
        let key = format!("{owner}/{repo}/pulls/{number}/reviews");
        if let Some(cached) = self.get_cached(&self.reviews, &key).await {
            return Ok(cached);
        }
        let data = self.client.reviews(owner, repo, number).await?;
        self.set_cached(&self.reviews, key, data.clone()).await;
        Ok(data)
    }

    pub async fn review_comments(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
    ) -> anyhow::Result<Vec<ReviewComment>> {
        let key = format!("{owner}/{repo}/pulls/{number}/comments");
        if let Some(cached) = self.get_cached(&self.review_comments, &key).await {
            return Ok(cached);
        }
        let data = self.client.review_comments(owner, repo, number).await?;
        self.set_cached(&self.review_comments, key, data.clone()).await;
        Ok(data)
    }

    pub async fn issue_comments(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
    ) -> anyhow::Result<Vec<IssueComment>> {
        let key = format!("{owner}/{repo}/issues/{number}/comments");
        if let Some(cached) = self.get_cached(&self.issue_comments, &key).await {
            return Ok(cached);
        }
        let data = self.client.issue_comments(owner, repo, number).await?;
        self.set_cached(&self.issue_comments, key, data.clone()).await;
        Ok(data)
    }

    pub async fn pr_files(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
    ) -> anyhow::Result<Vec<PullRequestFile>> {
        let key = format!("{owner}/{repo}/pulls/{number}/files");
        if let Some(cached) = self.get_cached(&self.pr_files, &key).await {
            return Ok(cached);
        }
        let data = self.client.pr_files(owner, repo, number).await?;
        self.set_cached(&self.pr_files, key, data.clone()).await;
        Ok(data)
    }

    pub async fn check_runs(
        &self,
        owner: &str,
        repo: &str,
        git_ref: &str,
    ) -> anyhow::Result<CheckRunsResponse> {
        let key = format!("{owner}/{repo}/commits/{git_ref}/check-runs");
        if let Some(cached) = self.get_cached(&self.check_runs, &key).await {
            return Ok(cached);
        }
        let data = self.client.check_runs(owner, repo, git_ref).await?;
        self.set_cached(&self.check_runs, key, data.clone()).await;
        Ok(data)
    }

    pub async fn pull_request(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
    ) -> anyhow::Result<PullRequest> {
        self.client.pull_request(owner, repo, number).await
    }

    pub async fn pull_request_by_branch(
        &self,
        owner: &str,
        repo: &str,
        branch: &str,
    ) -> anyhow::Result<Option<PullRequest>> {
        self.client.pull_request_by_branch(owner, repo, branch).await
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
    ) -> anyhow::Result<ReviewComment> {
        let comment = self
            .client
            .create_review_comment(owner, repo, number, body, commit_id, path, line, side)
            .await?;
        // Invalidate review comments cache
        let key = format!("{owner}/{repo}/pulls/{number}/comments");
        self.review_comments.write().await.remove(&key);
        Ok(comment)
    }

    pub async fn reply_to_comment(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
        comment_id: u64,
        body: &str,
    ) -> anyhow::Result<ReviewComment> {
        let comment = self
            .client
            .reply_to_comment(owner, repo, number, comment_id, body)
            .await?;
        let key = format!("{owner}/{repo}/pulls/{number}/comments");
        self.review_comments.write().await.remove(&key);
        Ok(comment)
    }

    async fn get_cached<T: Clone>(
        &self,
        map: &Arc<RwLock<HashMap<String, CacheEntry<T>>>>,
        key: &str,
    ) -> Option<T> {
        let mut m = map.write().await;
        if let Some(entry) = m.get_mut(key) {
            if entry.fetched_at.elapsed() < STALE_TIME {
                entry.last_accessed = Instant::now();
                return Some(entry.data.clone());
            }
        }
        None
    }

    async fn set_cached<T>(
        &self,
        map: &Arc<RwLock<HashMap<String, CacheEntry<T>>>>,
        key: String,
        data: T,
    ) {
        let now = Instant::now();
        map.write().await.insert(
            key,
            CacheEntry {
                data,
                fetched_at: now,
                last_accessed: now,
            },
        );
    }
}
