use std::io;
use std::sync::Arc;
use std::time::Duration;

use anyhow::Result;
use crossterm::event::{self, Event, EventStream, KeyCode, KeyModifiers, MouseEventKind};
use crossterm::terminal::{
    EnterAlternateScreen, LeaveAlternateScreen, disable_raw_mode, enable_raw_mode,
};
use crossterm::{execute, cursor};
use futures::StreamExt;
use ratatui::Terminal;
use ratatui::backend::CrosstermBackend;
use ratatui::layout::Rect;
use tokio::sync::{Notify, mpsc};
use tokio::time::{Instant, MissedTickBehavior, interval, sleep};

use crate::agent::AgentRunner;
use crate::agent::copilot::CopilotAgent;
use crate::agent::types::AgentEvent;
use crate::config::Config;
use crate::git::diff::DiffMode;
use crate::github::CachedClient;

use super::local_diff::{LocalDiff, Pane};
use super::styles::DiffColors;

pub enum ActiveView {
    LocalDiff,
}

pub struct App {
    pub repo_root: String,
    pub owner: String,
    pub repo: String,
    pub branch: String,
    pub config: Config,
    pub github: CachedClient,
    pub username: Option<String>,
    pub active_view: ActiveView,
    pub local_diff: LocalDiff,
    pub diff_colors: DiffColors,
    pub should_quit: bool,
    pub size: Rect,
    pub agent: Arc<dyn AgentRunner>,
    ctrl_c_count: u8,
}

impl App {
    pub fn new(
        repo_root: String,
        owner: String,
        repo: String,
        branch: String,
        config: Config,
        github: CachedClient,
    ) -> Self {
        let diff_colors = DiffColors::default();
        let mut local_diff = LocalDiff::new(
            repo_root.clone(),
            DiffMode::Working,
        );
        local_diff.base_branch = branch.clone();
        let agent: Arc<dyn AgentRunner> = Arc::new(CopilotAgent::new(repo_root.clone()));

        Self {
            repo_root,
            owner,
            repo,
            branch,
            config,
            github,
            username: None,
            active_view: ActiveView::LocalDiff,
            local_diff,
            diff_colors,
            should_quit: false,
            size: Rect::default(),
            agent,
            ctrl_c_count: 0,
        }
    }

    pub async fn run(&mut self) -> Result<()> {
        // Query terminal palette BEFORE entering alternate screen / raw mode.
        // The OSC 4 query needs the terminal in cooked mode to read responses.
        let palette = crate::terminal::palette::query_terminal_colors();

        let resolved = palette.colors.iter().filter(|c| c.is_some()).count();
        tracing::info!("Palette: resolved {resolved}/16 colors");
        for (i, c) in palette.colors.iter().enumerate() {
            if let Some((r, g, b)) = c {
                tracing::debug!("  palette[{i}] = ({r}, {g}, {b})");
            }
        }

        // Always use palette colors, even if incomplete — Go handles each color
        // individually with nil checks. from_palette uses unwrap_or fallbacks.
        if resolved > 0 {
            let theme = crate::ui::highlight::build_theme_from_palette(&palette);
            self.local_diff.viewer.highlighter.set_theme(theme);
            self.diff_colors = DiffColors::from_palette(&palette);
            tracing::info!("Using palette-derived colors and theme");
        } else {
            tracing::info!("No palette colors resolved, using defaults");
        }

        enable_raw_mode()?;
        let mut stdout = io::stdout();
        execute!(
            stdout,
            EnterAlternateScreen,
            crossterm::event::EnableMouseCapture,
            crossterm::terminal::Clear(crossterm::terminal::ClearType::All),
            cursor::Hide
        )?;

        let backend = CrosstermBackend::new(stdout);
        let mut terminal = Terminal::new(backend)?;
        terminal.clear()?;

        // Take ownership of the agent event receiver before starting the loop
        // so we can `select!` on it directly. Subsequent calls return None.
        let agent_events = self
            .agent
            .take_events()
            .expect("agent events receiver already taken");

        // Start Copilot agent in background
        let agent = self.agent.clone();
        tokio::spawn(async move {
            match agent.start().await {
                Ok(()) => tracing::info!("Copilot agent started successfully"),
                Err(e) => tracing::warn!("Failed to start copilot agent: {e}"),
            }
        });

        // Load initial diff
        self.local_diff.load_diff().await;

        // Fetch authenticated user in background
        if let Ok(user) = self.github.authenticated_user().await {
            self.username = Some(user.login.clone());
            self.local_diff.username = Some(user.login);
        }

        let result = self.event_loop(&mut terminal, agent_events).await;

        // Stop the agent
        self.agent.stop();

        disable_raw_mode()?;
        execute!(
            terminal.backend_mut(),
            crossterm::event::DisableMouseCapture,
            LeaveAlternateScreen,
            cursor::Show
        )?;

        result
    }

    async fn event_loop(
        &mut self,
        terminal: &mut Terminal<CrosstermBackend<io::Stdout>>,
        mut agent_events: mpsc::Receiver<AgentEvent>,
    ) -> Result<()> {
        // Event-driven loop: only redraws when something requests it. A `Notify`
        // coalesces multiple redraw requests within a frame into a single draw.
        // Inspired by codex's FrameRequester actor pattern.
        let redraw = Notify::new();

        let mut term_events = EventStream::new();

        // Animation tick for the copilot spinner. Only consumed while pending.
        let mut anim = interval(Duration::from_millis(120));
        anim.set_missed_tick_behavior(MissedTickBehavior::Delay);

        // Rate-limit redraws to ~120 fps so streaming bursts don't thrash the
        // terminal. Bursty notifies coalesce naturally because Notify holds at
        // most one stored permit.
        const MIN_FRAME_INTERVAL: Duration = Duration::from_millis(8);
        let mut last_draw: Option<Instant> = None;

        let mut agent_open = true;

        // Schedule the initial draw.
        redraw.notify_one();

        loop {
            let needs_animation = self.local_diff.copilot_state.has_pending();

            tokio::select! {
                biased;

                _ = redraw.notified() => {
                    if let Some(prev) = last_draw {
                        let elapsed = prev.elapsed();
                        if elapsed < MIN_FRAME_INTERVAL {
                            sleep(MIN_FRAME_INTERVAL - elapsed).await;
                        }
                    }
                    terminal.draw(|frame| {
                        self.size = frame.area();
                        self.render(frame);
                    })?;
                    last_draw = Some(Instant::now());
                    if self.should_quit {
                        break;
                    }
                }

                term_event = term_events.next() => {
                    match term_event {
                        Some(Ok(ev)) => {
                            self.handle_event(ev).await;
                            redraw.notify_one();
                        }
                        Some(Err(e)) => {
                            tracing::warn!("terminal event stream error: {e}");
                            break;
                        }
                        None => break,
                    }
                }

                agent_ev = agent_events.recv(), if agent_open => {
                    match agent_ev {
                        Some(event) => {
                            self.local_diff.handle_agent_event(event);
                            redraw.notify_one();
                        }
                        None => {
                            agent_open = false;
                        }
                    }
                }

                _ = anim.tick(), if needs_animation => {
                    self.local_diff.tick();
                    redraw.notify_one();
                }
            }
        }

        Ok(())
    }

    async fn handle_event(&mut self, ev: Event) {
        match ev {
            Event::Key(key) => self.handle_key(key).await,
            Event::Mouse(mouse) => self.handle_mouse(mouse),
            Event::Resize(w, h) => {
                self.size = Rect::new(0, 0, w, h);
                self.local_diff.resize(w, h);
            }
            _ => {}
        }
    }

    async fn handle_key(&mut self, key: event::KeyEvent) {
        // ctrl+c quit logic
        if key.code == KeyCode::Char('c') && key.modifiers.contains(KeyModifiers::CONTROL) {
            self.ctrl_c_count += 1;
            if self.ctrl_c_count >= 2 {
                self.should_quit = true;
            }
            return;
        }
        self.ctrl_c_count = 0;

        // Quit — but not when composing or panel is focused
        if key.code == KeyCode::Char('q')
            && !self.local_diff.composing.is_active()
            && !self.local_diff.viewer.panel.visible
        {
            self.should_quit = true;
            return;
        }

        // Route to active view
        match self.active_view {
            ActiveView::LocalDiff => {
                self.local_diff.handle_key(key, &self.repo_root, &self.agent).await;
            }
        }
    }

    fn handle_mouse(&mut self, mouse: event::MouseEvent) {
        let scroll_lines: i32 = 3;
        match mouse.kind {
            MouseEventKind::ScrollUp => {
                let pane = self.local_diff.pane_at_column(mouse.column);
                match pane {
                    Pane::Tree => self.local_diff.viewer.scroll_viewport(-scroll_lines),
                    Pane::Panel => self.local_diff.viewer.panel.scroll_up(scroll_lines as usize),
                    Pane::Diff => self.local_diff.viewer.scroll_viewport(-scroll_lines),
                }
            }
            MouseEventKind::ScrollDown => {
                let pane = self.local_diff.pane_at_column(mouse.column);
                match pane {
                    Pane::Tree => self.local_diff.viewer.scroll_viewport(scroll_lines),
                    Pane::Panel => self.local_diff.viewer.panel.scroll_down(scroll_lines as usize),
                    Pane::Diff => self.local_diff.viewer.scroll_viewport(scroll_lines),
                }
            }
            _ => {}
        }
    }

    fn render(&mut self, frame: &mut ratatui::Frame) {
        match self.active_view {
            ActiveView::LocalDiff => {
                self.local_diff.render(frame, frame.area(), &self.diff_colors);
            }
        }
    }
}
