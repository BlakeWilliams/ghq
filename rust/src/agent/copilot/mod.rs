use std::path::PathBuf;
use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use github_copilot_sdk::generated::session_events::{
    AssistantMessageData, AssistantMessageDeltaData, SessionEventType, ToolExecutionCompleteData,
    ToolExecutionStartData,
};
use github_copilot_sdk::handler::{PermissionResult, SessionHandler};
use github_copilot_sdk::types::{
    PermissionRequestData, RequestId, SessionConfig, SessionEvent, SessionId,
};
use tokio::sync::mpsc;

use super::AgentRunner;
use super::types::*;

struct GgHandler {
    events_tx: mpsc::Sender<AgentEvent>,
    comment_id: Arc<Mutex<String>>,
    repo_root: PathBuf,
}

#[async_trait]
impl SessionHandler for GgHandler {
    async fn on_permission_request(
        &self,
        _session_id: SessionId,
        _request_id: RequestId,
        _data: PermissionRequestData,
    ) -> PermissionResult {
        PermissionResult::Approved
    }

    async fn on_session_event(&self, _session_id: SessionId, event: SessionEvent) {
        let comment_id = self.comment_id.lock().unwrap().clone();
        let parsed = event.parsed_type();

        tracing::debug!("SDK event: type={} comment={}", event.event_type, comment_id);

        match parsed {
            SessionEventType::AssistantMessageDelta => {
                if let Some(data) = event.typed_data::<AssistantMessageDeltaData>() {
                    let _ = self
                        .events_tx
                        .send(AgentEvent {
                            comment_id: comment_id.clone(),
                            kind: EventKind::Delta,
                            payload: EventPayload::Delta(DeltaPayload {
                                text: data.delta_content,
                            }),
                        })
                        .await;
                }
            }
            SessionEventType::AssistantMessage => {
                if let Some(data) = event.typed_data::<AssistantMessageData>() {
                    let _ = self
                        .events_tx
                        .send(AgentEvent {
                            comment_id: comment_id.clone(),
                            kind: EventKind::Message,
                            payload: EventPayload::Delta(DeltaPayload {
                                text: data.content,
                            }),
                        })
                        .await;
                }
            }
            SessionEventType::ToolExecutionStart => {
                if let Some(data) = event.typed_data::<ToolExecutionStartData>() {
                    let args_summary = format_tool_args(
                        &data.tool_name,
                        data.arguments.as_ref(),
                        &self.repo_root,
                    );
                    let _ = self
                        .events_tx
                        .send(AgentEvent {
                            comment_id: comment_id.clone(),
                            kind: EventKind::ToolStart,
                            payload: EventPayload::Tool(ToolPayload {
                                tool_name: data.tool_name,
                                args_summary,
                                result: None,
                            }),
                        })
                        .await;
                }
            }
            SessionEventType::ToolExecutionComplete => {
                if let Some(data) = event.typed_data::<ToolExecutionCompleteData>() {
                    let result_text = data
                        .result
                        .as_ref()
                        .map(|r| r.content.clone());
                    let _ = self
                        .events_tx
                        .send(AgentEvent {
                            comment_id: comment_id.clone(),
                            kind: EventKind::ToolComplete,
                            payload: EventPayload::Tool(ToolPayload {
                                tool_name: String::new(),
                                args_summary: String::new(),
                                result: result_text,
                            }),
                        })
                        .await;
                }
            }
            SessionEventType::SessionIdle => {
                let _ = self
                    .events_tx
                    .send(AgentEvent {
                        comment_id: comment_id.clone(),
                        kind: EventKind::Done,
                        payload: EventPayload::None,
                    })
                    .await;
            }
            SessionEventType::SessionError => {
                let msg = event
                    .data
                    .get("message")
                    .and_then(|v| v.as_str())
                    .unwrap_or("unknown error")
                    .to_string();
                let _ = self
                    .events_tx
                    .send(AgentEvent {
                        comment_id: comment_id.clone(),
                        kind: EventKind::Error,
                        payload: EventPayload::Error(ErrorPayload { message: msg }),
                    })
                    .await;
            }
            _ => {
                tracing::debug!("Unhandled SDK event type: {}", event.event_type);
            }
        }
    }
}

pub struct CopilotAgent {
    events_rx: Mutex<Option<mpsc::Receiver<AgentEvent>>>,
    events_tx: mpsc::Sender<AgentEvent>,
    repo_root: PathBuf,
    client: Arc<tokio::sync::Mutex<Option<github_copilot_sdk::Client>>>,
}

impl CopilotAgent {
    pub fn new(repo_root: String) -> Self {
        let (tx, rx) = mpsc::channel(256);
        Self {
            events_rx: Mutex::new(Some(rx)),
            events_tx: tx,
            repo_root: PathBuf::from(repo_root),
            client: Arc::new(tokio::sync::Mutex::new(None)),
        }
    }
}

#[async_trait]
impl AgentRunner for CopilotAgent {
    fn take_events(&self) -> Option<mpsc::Receiver<AgentEvent>> {
        self.events_rx.lock().ok().and_then(|mut g| g.take())
    }

    async fn start(&self) -> anyhow::Result<()> {
        let mut options = github_copilot_sdk::ClientOptions::default();
        options.cwd = self.repo_root.clone();
        let client = github_copilot_sdk::Client::start(options)
            .await
            .map_err(|e| anyhow::anyhow!("failed to start copilot SDK: {e}"))?;
        *self.client.lock().await = Some(client);
        tracing::info!("Copilot SDK client started");
        Ok(())
    }

    async fn send(&self, comment_id: &str, prompt: &str) -> anyhow::Result<()> {
        let client_guard = self.client.lock().await;
        let client = client_guard
            .as_ref()
            .ok_or_else(|| anyhow::anyhow!("copilot client not started"))?;

        let comment_id_shared = Arc::new(Mutex::new(comment_id.to_string()));

        let handler = GgHandler {
            events_tx: self.events_tx.clone(),
            comment_id: comment_id_shared,
            repo_root: self.repo_root.clone(),
        };

        let mut config = SessionConfig::default();
        config.model = Some("claude-sonnet-4.5".to_string());
        config.streaming = Some(true);

        let session = client
            .create_session(config.with_handler(Arc::new(handler)))
            .await
            .map_err(|e| anyhow::anyhow!("failed to create copilot session: {e}"))?;

        session
            .send(prompt)
            .await
            .map_err(|e| anyhow::anyhow!("failed to send to copilot: {e}"))?;

        // The session event loop runs in the background. Events flow through
        // the GgHandler's on_session_event callback into our events_tx channel.
        // The session will be cleaned up when it goes idle or errors out.
        // We don't disconnect immediately — we let the agent finish.

        // Spawn a task to wait for idle and then disconnect.
        let session = Arc::new(session);
        let session_clone = session.clone();
        let events_tx = self.events_tx.clone();
        let cid = comment_id.to_string();
        tokio::spawn(async move {
            let mut sub = session_clone.subscribe();
            let timeout = tokio::time::Duration::from_secs(120);
            let deadline = tokio::time::Instant::now() + timeout;

            loop {
                match tokio::time::timeout_at(deadline, sub.recv()).await {
                    Ok(Ok(event)) => {
                        let parsed = event.parsed_type();
                        if parsed == SessionEventType::SessionIdle
                            || (parsed == SessionEventType::SessionError
                                && !event.is_transient_error())
                        {
                            break;
                        }
                    }
                    Ok(Err(_)) => break, // channel closed
                    Err(_) => {
                        // Timeout
                        let _ = events_tx
                            .send(AgentEvent {
                                comment_id: cid.clone(),
                                kind: EventKind::Error,
                                payload: EventPayload::Error(ErrorPayload {
                                    message: "copilot session timed out".to_string(),
                                }),
                            })
                            .await;
                        break;
                    }
                }
            }

            if let Err(e) = session_clone.disconnect().await {
                tracing::warn!("failed to disconnect copilot session: {e}");
            }
        });

        Ok(())
    }

    fn stop(&self) {
        // Client shutdown is handled on drop, but we can explicitly stop it
        // via a spawned task if needed.
        let client = self.client.clone();
        tokio::spawn(async move {
            if let Some(c) = client.lock().await.take() {
                if let Err(e) = c.stop().await {
                    tracing::warn!("failed to stop copilot client: {e:?}");
                }
            }
        });
    }
}

fn format_tool_args(
    tool_name: &str,
    args: Option<&serde_json::Value>,
    repo_root: &std::path::Path,
) -> String {
    let args = match args {
        Some(v) => v,
        None => return String::new(),
    };

    if tool_name == "report_intent" {
        return args
            .get("intent")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
    }

    // Prioritize specific keys for a clean summary
    let priority_keys = ["path", "pattern", "query", "url", "skill", "command"];
    for key in &priority_keys {
        if let Some(val) = args.get(*key).and_then(|v| v.as_str()) {
            let val = relativize_path(val, repo_root);
            return format!("{key}: {val}");
        }
    }

    // Fallback: first string field
    if let Some(obj) = args.as_object() {
        for (k, v) in obj {
            if let Some(s) = v.as_str() {
                let s = relativize_path(s, repo_root);
                return format!("{k}: {s}");
            }
        }
    }

    String::new()
}

fn relativize_path(path: &str, repo_root: &std::path::Path) -> String {
    let p = std::path::Path::new(path);
    p.strip_prefix(repo_root)
        .map(|rel| rel.display().to_string())
        .unwrap_or_else(|_| path.to_string())
}

