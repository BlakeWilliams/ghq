use std::collections::HashMap;
use std::time::{Duration, Instant};

use tokio::sync::RwLock;

use super::Client;
use super::types::*;

const GC_INTERVAL: Duration = Duration::from_secs(30);
const STALE_TIME: Duration = Duration::from_secs(30);
const GC_TIME: Duration = Duration::from_secs(300);

struct CacheEntry<T> {
    data: T,
    fetched_at: Instant,
    last_accessed: Instant,
}

struct Cache<T: Clone> {
    inner: RwLock<HashMap<String, CacheEntry<T>>>,
}

impl<T: Clone> Cache<T> {
    fn new() -> Self {
        Self {
            inner: RwLock::new(HashMap::new()),
        }
    }

    async fn get(&self, key: &str) -> Option<T> {
        let mut m = self.inner.write().await;
        if let Some(entry) = m.get_mut(key) {
            if entry.fetched_at.elapsed() < STALE_TIME {
                entry.last_accessed = Instant::now();
                return Some(entry.data.clone());
            }
        }
        None
    }

    async fn set(&self, key: String, data: T) {
        let now = Instant::now();
        self.inner.write().await.insert(
            key,
            CacheEntry {
                data,
                fetched_at: now,
                last_accessed: now,
            },
        );
    }

    async fn invalidate(&self, key: &str) {
        self.inner.write().await.remove(key);
    }

    async fn gc(&self) {
        let now = Instant::now();
        let mut m = self.inner.write().await;
        m.retain(|_, v| now.duration_since(v.last_accessed) < GC_TIME);
    }

    #[allow(dead_code)]
    async fn clear(&self) {
        self.inner.write().await.clear();
    }
}

pub struct CachedClient {
    client: Client,
    review_comments: Cache<Vec<ReviewComment>>,
}

impl CachedClient {
    pub fn new(client: Client) -> Self {
        Self {
            client,
            review_comments: Cache::new(),
        }
    }

    pub fn gc_interval(&self) -> Duration {
        GC_INTERVAL
    }

    pub async fn gc(&self) {
        self.review_comments.gc().await;
    }

    #[allow(dead_code)]
    pub async fn invalidate_all(&self) {
        self.review_comments.clear().await;
    }

    pub async fn authenticated_user(&self) -> anyhow::Result<User> {
        self.client.authenticated_user().await
    }

    pub async fn review_comments(
        &self,
        owner: &str,
        repo: &str,
        number: u64,
    ) -> anyhow::Result<Vec<ReviewComment>> {
        let key = format!("{owner}/{repo}/pulls/{number}/comments");
        if let Some(cached) = self.review_comments.get(&key).await {
            return Ok(cached);
        }
        let data = self.client.review_comments(owner, repo, number).await?;
        self.review_comments.set(key, data.clone()).await;
        Ok(data)
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
        let key = format!("{owner}/{repo}/pulls/{number}/comments");
        self.review_comments.invalidate(&key).await;
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
        self.review_comments.invalidate(&key).await;
        Ok(comment)
    }
}
