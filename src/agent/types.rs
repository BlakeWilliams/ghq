#[derive(Debug, Clone)]
pub struct AgentEvent {
    pub comment_id: String,
    pub kind: EventKind,
    pub payload: EventPayload,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum EventKind {
    Delta,
    Message,
    ToolStart,
    ToolComplete,
    Done,
    Error,
}

#[derive(Debug, Clone)]
pub enum EventPayload {
    Delta(DeltaPayload),
    Tool(ToolPayload),
    Error(ErrorPayload),
    None,
}

#[derive(Debug, Clone)]
pub struct DeltaPayload {
    pub text: String,
}

#[derive(Debug, Clone)]
pub struct ToolPayload {
    pub tool_name: String,
    pub args_summary: String,
    pub result: Option<String>,
}

#[derive(Debug, Clone)]
pub struct ErrorPayload {
    pub message: String,
}
