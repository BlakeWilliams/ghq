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

/// A segment of a comment body. A comment is a sequence of content blocks —
/// typically text interspersed with tool-call groups, matching Go's ContentBlock.
#[derive(Debug, Clone)]
pub enum ContentBlock {
    Text(String),
    ToolGroup(ToolGroup),
}

/// Extract a plain-text body from blocks by joining all Text segments.
pub fn body_from_blocks(blocks: &[ContentBlock]) -> String {
    let parts: Vec<&str> = blocks
        .iter()
        .filter_map(|b| match b {
            ContentBlock::Text(t) => Some(t.as_str()),
            _ => None,
        })
        .collect();
    parts.join("\n").trim().to_string()
}

/// Normalize blocks: if non-empty return as-is, otherwise synthesize from body.
pub fn normalized_blocks(blocks: Vec<ContentBlock>, body: &str) -> Vec<ContentBlock> {
    if !blocks.is_empty() {
        return blocks;
    }
    if !body.is_empty() {
        return vec![ContentBlock::Text(body.to_string())];
    }
    Vec::new()
}

pub struct PendingReply {
    pub blocks: Vec<ContentBlock>,
    pub intent: String,
    pub path: String,
    pub line: i32,
    pub side: String,
}

pub struct CompletedReply {
    pub comment_id: String,
    pub body: String,
    pub blocks: Vec<ContentBlock>,
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
                blocks: Vec::new(),
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

    pub fn pending_ids(&self) -> impl Iterator<Item = &str> {
        self.pending.keys().map(|s| s.as_str())
    }

    pub fn blocks(&self, comment_id: &str) -> Option<&[ContentBlock]> {
        self.pending.get(comment_id).map(|p| p.blocks.as_slice())
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

    /// Build display blocks for a pending reply, appending dots to the last
    /// text block (or adding a "Thinking..." placeholder if no text yet).
    pub fn pending_display_blocks(&self, comment_id: &str) -> Option<Vec<ContentBlock>> {
        let pending = self.pending.get(comment_id)?;
        let dots = self.dots_str();

        let placeholder = if !pending.intent.is_empty() {
            pending.intent.clone()
        } else {
            "Thinking".to_string()
        };

        if pending.blocks.is_empty() {
            return Some(vec![ContentBlock::Text(format!("{placeholder}{dots}"))]);
        }

        let mut render_blocks = pending.blocks.clone();
        let n = render_blocks.len();
        if let Some(ContentBlock::Text(t)) = render_blocks.last_mut() {
            t.push_str(&dots);
        } else {
            render_blocks.push(ContentBlock::Text(format!("{placeholder}{dots}")));
        }
        let _ = n; // suppress unused warning
        Some(render_blocks)
    }

    pub fn pending_for_file(&self, path: &str) -> Vec<(&str, &PendingReply)> {
        self.pending
            .iter()
            .filter(|(_, p)| p.path == path)
            .map(|(id, p)| (id.as_str(), p))
            .collect()
    }

    pub fn pending_file_set(&self) -> std::collections::HashSet<String> {
        self.pending.values().map(|p| p.path.clone()).collect()
    }

    pub fn handle_event(&mut self, event: AgentEvent) -> Option<CompletedReply> {
        match event.kind {
            EventKind::Delta => {
                if let EventPayload::Delta(d) = &event.payload {
                    if let Some(pending) = self.pending.get_mut(&event.comment_id) {
                        // Append to last TextBlock, or create new one
                        if let Some(ContentBlock::Text(t)) = pending.blocks.last_mut() {
                            t.push_str(&d.text);
                        } else {
                            pending.blocks.push(ContentBlock::Text(d.text.clone()));
                        }
                    }
                }
                None
            }
            EventKind::Message => {
                if let EventPayload::Delta(d) = &event.payload {
                    if let Some(pending) = self.pending.get_mut(&event.comment_id) {
                        // Full message replace — set as sole text block
                        if let Some(ContentBlock::Text(t)) = pending.blocks.last_mut() {
                            *t = d.text.clone();
                        } else {
                            pending.blocks.push(ContentBlock::Text(d.text.clone()));
                        }
                    }
                }
                None
            }
            EventKind::ToolStart => {
                if let EventPayload::Tool(t) = &event.payload {
                    if let Some(pending) = self.pending.get_mut(&event.comment_id) {
                        if t.tool_name == "report_intent" {
                            pending.intent = t.args_summary.clone();
                            // Update label on current tool group block
                            if let Some(ContentBlock::ToolGroup(g)) = pending.blocks.last_mut() {
                                if !t.args_summary.is_empty() {
                                    g.label = t.args_summary.clone();
                                }
                            }
                        } else {
                            let tc = ToolCall {
                                name: t.tool_name.clone(),
                                args_summary: t.args_summary.clone(),
                                status: ToolStatus::Running,
                            };
                            // Append to last ToolGroup block, or create new one
                            if let Some(ContentBlock::ToolGroup(g)) = pending.blocks.last_mut() {
                                g.tools.push(tc);
                            } else {
                                pending.blocks.push(ContentBlock::ToolGroup(ToolGroup {
                                    label: pending.intent.clone(),
                                    tools: vec![tc],
                                }));
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
                        // Walk blocks in reverse to find the first running tool
                        for block in pending.blocks.iter_mut().rev() {
                            if let ContentBlock::ToolGroup(g) = block {
                                for tool in g.tools.iter_mut() {
                                    if tool.status == ToolStatus::Running {
                                        tool.status = ToolStatus::Done;
                                        return None;
                                    }
                                }
                            }
                        }
                    }
                }
                None
            }
            EventKind::Done => {
                let pending = self.pending.remove(&event.comment_id)?;
                let body = body_from_blocks(&pending.blocks);
                Some(CompletedReply {
                    comment_id: event.comment_id,
                    body,
                    blocks: pending.blocks,
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
                    blocks: Vec::new(),
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

        let blocks = state.blocks("c1").unwrap();
        assert_eq!(blocks.len(), 1);
        match &blocks[0] {
            ContentBlock::Text(t) => assert_eq!(t, "Hello world"),
            _ => panic!("expected TextBlock"),
        }
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
        assert!(state.blocks("unknown").is_none());
    }

    #[test]
    fn interleaved_tools_and_text() {
        let mut state = CopilotState::new();
        state.set_pending("c1".into(), "file.rs".into(), 1, "RIGHT".into());

        // Tool start
        state.handle_event(AgentEvent {
            comment_id: "c1".into(),
            kind: EventKind::ToolStart,
            payload: EventPayload::Tool(ToolPayload {
                tool_name: "read_file".into(),
                args_summary: "main.rs".into(),
                result: None,
            }),
        });
        // Tool complete
        state.handle_event(AgentEvent {
            comment_id: "c1".into(),
            kind: EventKind::ToolComplete,
            payload: EventPayload::Tool(ToolPayload {
                tool_name: "read_file".into(),
                args_summary: String::new(),
                result: None,
            }),
        });
        // Text delta
        state.handle_event(AgentEvent {
            comment_id: "c1".into(),
            kind: EventKind::Delta,
            payload: EventPayload::Delta(DeltaPayload {
                text: "Here is the fix.".into(),
            }),
        });
        // Another tool start
        state.handle_event(AgentEvent {
            comment_id: "c1".into(),
            kind: EventKind::ToolStart,
            payload: EventPayload::Tool(ToolPayload {
                tool_name: "apply_edit".into(),
                args_summary: "main.rs".into(),
                result: None,
            }),
        });

        let blocks = state.blocks("c1").unwrap();
        assert_eq!(blocks.len(), 3); // ToolGroup, Text, ToolGroup
        assert!(matches!(&blocks[0], ContentBlock::ToolGroup(_)));
        assert!(matches!(&blocks[1], ContentBlock::Text(t) if t == "Here is the fix."));
        assert!(matches!(&blocks[2], ContentBlock::ToolGroup(_)));
    }
}
