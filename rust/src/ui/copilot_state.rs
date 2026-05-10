use std::collections::HashMap;

use crate::agent::types::{AgentEvent, EventKind, EventPayload};

#[derive(Debug, Clone)]
pub struct ToolCall {
    pub name: String,
    pub args_summary: String,
    pub status: ToolStatus,
}

#[derive(Debug, Clone, PartialEq)]
pub enum ToolStatus {
    Running,
    Done,
    Failed,
}

#[derive(Debug, Clone)]
pub struct ToolGroup {
    pub label: String,
    pub tools: Vec<ToolCall>,
}

impl ToolGroup {
    pub fn overall_status(&self) -> &ToolStatus {
        if self.tools.iter().any(|t| t.status == ToolStatus::Failed) {
            return &ToolStatus::Failed;
        }
        if self.tools.iter().any(|t| t.status == ToolStatus::Running) {
            return &ToolStatus::Running;
        }
        &ToolStatus::Done
    }
}

pub struct PendingReply {
    pub reply_buf: String,
    pub tool_groups: Vec<ToolGroup>,
    pub intent: String,
    pub path: String,
    pub line: i32,
    pub side: String,
}

pub struct CompletedReply {
    pub comment_id: String,
    pub body: String,
    pub tool_groups: Vec<ToolGroup>,
    pub path: String,
    pub line: i32,
    pub side: String,
    pub is_error: bool,
}

pub struct CopilotState {
    pending: HashMap<String, PendingReply>,
    dots: usize,
}

impl CopilotState {
    pub fn new() -> Self {
        Self {
            pending: HashMap::new(),
            dots: 0,
        }
    }

    pub fn set_pending(&mut self, comment_id: String, path: String, line: i32, side: String) {
        self.pending.insert(
            comment_id,
            PendingReply {
                reply_buf: String::new(),
                tool_groups: Vec::new(),
                intent: String::new(),
                path,
                line,
                side,
            },
        );
    }

    pub fn has_pending(&self) -> bool {
        !self.pending.is_empty()
    }

    pub fn is_pending(&self, comment_id: &str) -> bool {
        self.pending.contains_key(comment_id)
    }

    pub fn reply_buf(&self, comment_id: &str) -> Option<&str> {
        self.pending.get(comment_id).map(|p| p.reply_buf.as_str())
    }

    pub fn tool_groups(&self, comment_id: &str) -> Option<&[ToolGroup]> {
        self.pending.get(comment_id).map(|p| p.tool_groups.as_slice())
    }

    pub fn intent(&self, comment_id: &str) -> Option<&str> {
        self.pending.get(comment_id).map(|p| p.intent.as_str())
    }

    pub fn advance_dots(&mut self) {
        self.dots = (self.dots + 1) % 4;
    }

    pub fn dots_str(&self) -> String {
        ".".repeat(self.dots + 1)
    }

    pub fn pending_display_text(&self, comment_id: &str) -> Option<String> {
        let pending = self.pending.get(comment_id)?;
        let dots = self.dots_str();
        if !pending.reply_buf.is_empty() {
            Some(pending.reply_buf.clone())
        } else if !pending.intent.is_empty() {
            Some(format!("{}{dots}", pending.intent))
        } else {
            Some(format!("Thinking{dots}"))
        }
    }

    pub fn pending_for_file(&self, path: &str) -> Vec<(&str, &PendingReply)> {
        self.pending
            .iter()
            .filter(|(_, p)| p.path == path)
            .map(|(id, p)| (id.as_str(), p))
            .collect()
    }

    pub fn handle_event(&mut self, event: AgentEvent) -> Option<CompletedReply> {
        match event.kind {
            EventKind::Delta => {
                if let EventPayload::Delta(d) = &event.payload {
                    if let Some(pending) = self.pending.get_mut(&event.comment_id) {
                        pending.reply_buf.push_str(&d.text);
                    }
                }
                None
            }
            EventKind::Message => {
                if let EventPayload::Delta(d) = &event.payload {
                    if let Some(pending) = self.pending.get_mut(&event.comment_id) {
                        pending.reply_buf = d.text.clone();
                    }
                }
                None
            }
            EventKind::ToolStart => {
                if let EventPayload::Tool(t) = &event.payload {
                    if let Some(pending) = self.pending.get_mut(&event.comment_id) {
                        if t.tool_name == "report_intent" {
                            pending.intent = t.args_summary.clone();
                            // Update label on current tool group
                            if let Some(group) = pending.tool_groups.last_mut() {
                                if !t.args_summary.is_empty() {
                                    group.label = t.args_summary.clone();
                                }
                            }
                        } else {
                            let tc = ToolCall {
                                name: t.tool_name.clone(),
                                args_summary: t.args_summary.clone(),
                                status: ToolStatus::Running,
                            };
                            if let Some(group) = pending.tool_groups.last_mut() {
                                group.tools.push(tc);
                            } else {
                                pending.tool_groups.push(ToolGroup {
                                    label: pending.intent.clone(),
                                    tools: vec![tc],
                                });
                            }
                        }
                    }
                }
                None
            }
            EventKind::ToolComplete => {
                if let EventPayload::Tool(t) = &event.payload {
                    if t.tool_name == "report_intent" {
                        return None;
                    }
                    if let Some(pending) = self.pending.get_mut(&event.comment_id) {
                        // Find last running tool with matching name and mark done
                        for group in pending.tool_groups.iter_mut().rev() {
                            for tool in group.tools.iter_mut() {
                                if tool.status == ToolStatus::Running {
                                    tool.status = ToolStatus::Done;
                                    return None;
                                }
                            }
                        }
                    }
                }
                None
            }
            EventKind::Done => {
                let pending = self.pending.remove(&event.comment_id)?;
                Some(CompletedReply {
                    comment_id: event.comment_id,
                    body: pending.reply_buf,
                    tool_groups: pending.tool_groups,
                    path: pending.path,
                    line: pending.line,
                    side: pending.side,
                    is_error: false,
                })
            }
            EventKind::Error => {
                let msg = if let EventPayload::Error(e) = &event.payload {
                    e.message.clone()
                } else {
                    "unknown error".to_string()
                };
                let pending = self.pending.remove(&event.comment_id);
                let (path, line, side) = pending
                    .map(|p| (p.path, p.line, p.side))
                    .unwrap_or_default();
                Some(CompletedReply {
                    comment_id: event.comment_id,
                    body: format!("⚠ {msg}"),
                    tool_groups: Vec::new(),
                    path,
                    line,
                    side,
                    is_error: true,
                })
            }
        }
    }
}

impl Default for CopilotState {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::agent::types::*;

    #[test]
    fn delta_accumulates() {
        let mut state = CopilotState::new();
        state.set_pending("c1".into(), "file.rs".into(), 10, "RIGHT".into());

        let result = state.handle_event(AgentEvent {
            comment_id: "c1".into(),
            kind: EventKind::Delta,
            payload: EventPayload::Delta(DeltaPayload {
                text: "Hello ".into(),
            }),
        });
        assert!(result.is_none());

        let result = state.handle_event(AgentEvent {
            comment_id: "c1".into(),
            kind: EventKind::Delta,
            payload: EventPayload::Delta(DeltaPayload {
                text: "world".into(),
            }),
        });
        assert!(result.is_none());

        assert_eq!(state.reply_buf("c1"), Some("Hello world"));
    }

    #[test]
    fn done_produces_completed_reply() {
        let mut state = CopilotState::new();
        state.set_pending("c1".into(), "file.rs".into(), 5, "LEFT".into());

        state.handle_event(AgentEvent {
            comment_id: "c1".into(),
            kind: EventKind::Delta,
            payload: EventPayload::Delta(DeltaPayload {
                text: "reply text".into(),
            }),
        });

        let reply = state
            .handle_event(AgentEvent {
                comment_id: "c1".into(),
                kind: EventKind::Done,
                payload: EventPayload::None,
            })
            .expect("should produce reply");

        assert_eq!(reply.comment_id, "c1");
        assert_eq!(reply.body, "reply text");
        assert_eq!(reply.path, "file.rs");
        assert_eq!(reply.line, 5);
        assert_eq!(reply.side, "LEFT");
        assert!(!reply.is_error);

        assert!(!state.is_pending("c1"));
    }

    #[test]
    fn error_produces_completed_reply() {
        let mut state = CopilotState::new();
        state.set_pending("c1".into(), "file.rs".into(), 1, "RIGHT".into());

        let reply = state
            .handle_event(AgentEvent {
                comment_id: "c1".into(),
                kind: EventKind::Error,
                payload: EventPayload::Error(ErrorPayload {
                    message: "timeout".into(),
                }),
            })
            .expect("should produce reply");

        assert_eq!(reply.body, "⚠ timeout");
        assert!(reply.is_error);
        assert!(!state.is_pending("c1"));
    }

    #[test]
    fn delta_for_unknown_id_ignored() {
        let mut state = CopilotState::new();
        let result = state.handle_event(AgentEvent {
            comment_id: "unknown".into(),
            kind: EventKind::Delta,
            payload: EventPayload::Delta(DeltaPayload {
                text: "data".into(),
            }),
        });
        assert!(result.is_none());
        assert!(state.reply_buf("unknown").is_none());
    }
}
