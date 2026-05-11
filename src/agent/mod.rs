pub mod types;

pub mod copilot;

use async_trait::async_trait;
use tokio::sync::mpsc;

use self::types::AgentEvent;

#[async_trait]
pub trait AgentRunner: Send + Sync {
    /// Take the receiver for agent events. Returns `Some` on the first call,
    /// `None` thereafter — the app event loop owns the receiver and selects on it.
    fn take_events(&self) -> Option<mpsc::Receiver<AgentEvent>>;
    async fn send(&self, comment_id: &str, prompt: &str) -> anyhow::Result<()>;
    async fn start(&self) -> anyhow::Result<()>;
    fn stop(&self);
}
