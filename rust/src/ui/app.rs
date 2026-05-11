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

use super::local_diff::keybinds;
use tokio::time::{Instant, MissedTickBehavior, interval, sleep};

use crate::agent::AgentRunner;
use crate::agent::copilot::CopilotAgent;
use crate::agent::types::AgentEvent;
use crate::config::Config;
use crate::git::diff::DiffMode;
use crate::git::watcher::{RepoWatcher, WatchEvent};
use crate::github::CachedClient;

use super::local_diff::{DiffLoaded, LocalDiff, Pane};
use super::scroll::Scrollable;
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
    diff_rx: mpsc::UnboundedReceiver<DiffLoaded>,
    /// Suppress key events until this instant to discard stale OSC responses
    /// that crossterm parsed as garbage key events before drain_stdin ran.
    suppress_keys_until: Option<std::time::Instant>,
}

impl App {
    pub async fn new(
        repo_root: String,
        owner: String,
        repo: String,
        branch: String,
        config: Config,
        github: CachedClient,
    ) -> Self {
        let diff_colors = DiffColors::default();
        let (diff_tx, diff_rx) = mpsc::unbounded_channel();
        let mut local_diff = LocalDiff::new(
            repo_root.clone(),
            DiffMode::Working,
            diff_tx,
        );
        local_diff.base_branch = branch.clone();
        local_diff.default_branch = crate::git::default_branch_short(&repo_root)
            .await
            .unwrap_or_else(|_| "main".to_string());
        local_diff.github = Some(github.clone());
        local_diff.owner = owner.clone();
        local_diff.repo_name = repo.clone();
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
            diff_rx,
            suppress_keys_until: None,
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
            self.local_diff.viewer.highlights.highlighter.set_theme(theme);
            self.local_diff.viewer.clear_highlight_cache();
            self.diff_colors = DiffColors::from_palette(&palette);
            self.local_diff.colors = self.diff_colors.clone();
            tracing::info!("Using palette-derived colors and theme");
        } else {
            tracing::info!("No palette colors resolved, using defaults");
        }

        enable_raw_mode()?;

        // Drain any stale OSC palette responses that arrived after the
        // initial query. Some terminals defer delivery until focus returns.
        crate::terminal::palette::drain_stdin();

        let mut stdout = io::stdout();
        execute!(
            stdout,
            EnterAlternateScreen,
            crossterm::event::EnableMouseCapture,
            crossterm::event::EnableFocusChange,
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

        // Load initial diff (non-blocking — result arrives via diff_rx)
        self.local_diff.load_diff();

        let (watch_tx, watch_rx) = mpsc::unbounded_channel();
        let _watcher = match RepoWatcher::new(&self.repo_root, watch_tx) {
            Ok(w) => {
                tracing::info!("File watcher started");
                Some(w)
            }
            Err(e) => {
                tracing::warn!("Failed to start file watcher: {e}");
                None
            }
        };

        // Fetch authenticated user in background
        if let Ok(user) = self.github.authenticated_user().await {
            self.username = Some(user.login.clone());
            self.local_diff.username = Some(user.login);
        }

        let result = self.event_loop(&mut terminal, agent_events, watch_rx).await;

        // Stop the agent
        self.agent.stop();

        disable_raw_mode()?;
        execute!(
            terminal.backend_mut(),
            crossterm::event::DisableMouseCapture,
            crossterm::event::DisableFocusChange,
            LeaveAlternateScreen,
            cursor::Show
        )?;

        result
    }

    async fn event_loop(
        &mut self,
        terminal: &mut Terminal<CrosstermBackend<io::Stdout>>,
        mut agent_events: mpsc::Receiver<AgentEvent>,
        mut watch_events: mpsc::UnboundedReceiver<WatchEvent>,
    ) -> Result<()> {
        // Event-driven loop: only redraws when something requests it. A `Notify`
        // coalesces multiple redraw requests within a frame into a single draw.
        // Inspired by codex's FrameRequester actor pattern.
        let redraw = Notify::new();

        let mut term_events = EventStream::new();

        // Animation tick for the copilot spinner. Only consumed while pending.
        let mut anim = interval(Duration::from_millis(240));
        anim.set_missed_tick_behavior(MissedTickBehavior::Delay);

        // Rate-limit redraws to ~120 fps so streaming bursts don't thrash the
        // terminal. Bursty notifies coalesce naturally because Notify holds at
        // most one stored permit.
        const MIN_FRAME_INTERVAL: Duration = Duration::from_millis(8);
        let mut last_draw: Option<Instant> = None;

        let mut agent_open = true;

        // Periodic review comment refresh (every 60s when a PR is detected)
        let mut comment_refresh = interval(Duration::from_secs(60));
        comment_refresh.set_missed_tick_behavior(MissedTickBehavior::Delay);

        // Schedule the initial draw.
        redraw.notify_one();

        loop {
            let needs_animation = self.local_diff.copilot_state.has_pending()
                || !self.local_diff.flash.is_empty();

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

                _ = comment_refresh.tick(), if self.local_diff.pr.is_some() => {
                    self.local_diff.fetch_review_comments(&self.github, &self.owner, &self.repo);
                }

                watch_ev = watch_events.recv() => {
                    if let Some(ev) = watch_ev {
                        match ev {
                            WatchEvent::FilesChanged => {
                                tracing::debug!("File watcher: files changed, reloading diff");
                                self.local_diff.reload_diff();
                            }
                            WatchEvent::BranchChanged(branch) => {
                                tracing::info!("File watcher: branch changed to {branch}");
                                self.branch = branch.clone();
                                self.local_diff.base_branch = branch;
                                self.local_diff.reload_diff();
                            }
                        }
                    }
                }

                diff_result = self.diff_rx.recv() => {
                    if let Some(result) = diff_result {
                        let is_first_load = !self.local_diff.pr_loaded && self.local_diff.pr.is_none();
                        self.local_diff.apply_diff_loaded(result).await;
                        if is_first_load {
                            self.local_diff.fetch_pr(&self.github, &self.owner, &self.repo);
                        }
                        redraw.notify_one();
                    }
                }

                github_event = self.local_diff.github_rx.recv() => {
                    if let Some(event) = github_event {
                        let github = self.github.clone();
                        let owner = self.owner.clone();
                        let repo = self.repo.clone();
                        self.local_diff.handle_github_event(event, &github, &owner, &repo).await;
                        redraw.notify_one();
                    }
                }

                hl_result = self.local_diff.highlight_rx.recv() => {
                    if let Some(result) = hl_result {
                        self.local_diff.apply_highlight_result(result);
                        redraw.notify_one();
                    }
                }
            }
        }

        Ok(())
    }

    async fn handle_event(&mut self, ev: Event) {
        match ev {
            Event::Key(key) => {
                // Discard keys that arrived during the suppression window
                // (stale OSC palette responses parsed as garbage chars).
                if let Some(until) = self.suppress_keys_until {
                    if std::time::Instant::now() < until {
                        return;
                    }
                    self.suppress_keys_until = None;
                }
                self.handle_key(key).await;
            }
            Event::Mouse(mouse) => self.handle_mouse(mouse),
            Event::Resize(w, h) => {
                self.size = Rect::new(0, 0, w, h);
                self.local_diff.resize(w, h);
            }
            Event::FocusGained => {
                // Drain stale OSC palette responses that terminals may defer
                // until the window regains focus.
                crate::terminal::palette::drain_stdin();
                // Suppress key events for 100ms — crossterm may have already
                // parsed some OSC response bytes into its event queue as
                // garbage key events before drain_stdin could discard them.
                self.suppress_keys_until =
                    Some(std::time::Instant::now() + std::time::Duration::from_millis(50));
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

        // Quit — but not when composing, panel is focused, or picker is open
        if key.code == KeyCode::Char('q')
            && !self.local_diff.composing.is_active()
            && !self.local_diff.viewer.panel.visible
            && self.local_diff.picker.is_none()
        {
            self.should_quit = true;
            return;
        }

        // Open command palette with `:`
        if key.code == KeyCode::Char(':')
            && !self.local_diff.composing.is_active()
            && self.local_diff.picker.is_none()
            && !self.local_diff.viewer.search.active
        {
            keybinds::open_command_palette(&mut self.local_diff);
            return;
        }

        // Route to active view
        match self.active_view {
            ActiveView::LocalDiff => {
                if let Some(cmd) = self.local_diff.handle_key(key, &self.repo_root, &self.agent).await {
                    self.handle_command(&cmd, &[]).await;
                }
            }
        }
    }

    async fn handle_command(&mut self, name: &str, _args: &[String]) {
        match name {
            "quit" | "q" => {
                self.should_quit = true;
            }
            "refresh" => {
                self.local_diff.reload_diff();
            }
            "view-on-github" => {
                if let Some(ref pr) = self.local_diff.pr {
                    let url = if let Some(ref html_url) = pr.html_url {
                        html_url.to_string()
                    } else {
                        format!(
                            "https://github.com/{}/{}/pull/{}",
                            self.owner, self.repo, pr.number
                        )
                    };
                    let _ = open::that(&url);
                } else {
                    self.local_diff.flash.error("No PR found for current branch".to_string());
                }
            }
            "set-merge-base" => {
                let repo_root = self.repo_root.clone();
                match crate::git::local_branches(&repo_root).await {
                    Ok(branches) => {
                        let items: Vec<super::picker::PickerItem> = branches
                            .into_iter()
                            .map(|b| super::picker::PickerItem {
                                label: b.clone(),
                                description: String::new(),
                                value: b,
                            })
                            .collect();
                        self.local_diff.picker = Some(super::picker::Picker::new("Merge Base", items));
                        self.local_diff.picker_kind = "merge-base".to_string();
                    }
                    Err(e) => {
                        self.local_diff.flash.error(format!("Failed to list branches: {e}"));
                    }
                }
            }
            _ => {
                self.local_diff.flash.error("Unknown command: ".to_string() + name);
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
                    Pane::Panel => self.local_diff.viewer.panel.scroll_viewport(-(scroll_lines)),
                    Pane::Diff => self.local_diff.viewer.scroll_viewport(-scroll_lines),
                }
            }
            MouseEventKind::ScrollDown => {
                let pane = self.local_diff.pane_at_column(mouse.column);
                match pane {
                    Pane::Tree => self.local_diff.viewer.scroll_viewport(scroll_lines),
                    Pane::Panel => self.local_diff.viewer.panel.scroll_viewport(scroll_lines),
                    Pane::Diff => self.local_diff.viewer.scroll_viewport(scroll_lines),
                }
            }
            _ => {}
        }
    }

    fn render(&mut self, frame: &mut ratatui::Frame) {
        let area = frame.area();

        match self.active_view {
            ActiveView::LocalDiff => {
                self.local_diff.render(frame, area, &self.diff_colors);
            }
        }
    }
}
