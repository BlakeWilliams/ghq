use std::sync::Arc;
use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};

use crate::agent::AgentRunner;
use crate::git::diff::DiffMode;
use crate::ui::commit::{self, CommitAction, CommitKeyResult, CommitOverlay, COMMIT_GEN_PREFIX};
use crate::ui::picker::{Picker, PickerItem};
use crate::ui::scroll::Scrollable;
use super::LocalDiff;

pub async fn handle_key(
    local_diff: &mut LocalDiff,
    key: KeyEvent,
    repo_root: &str,
    agent: &Arc<dyn AgentRunner>,
) -> Option<String> {
    // Composing mode: route all keys to the input buffer
    if local_diff.composing.is_active() {
        handle_composing_key(local_diff, key, agent).await;
        return None;
    }

    // Commit overlay: route all keys to the commit modal
    if local_diff.commit_overlay.is_some() {
        return handle_commit_key(local_diff, key, repo_root, agent).await;
    }

    // Search input mode: route keys to search query
    if local_diff.viewer.search.active {
        handle_search_key(local_diff, key);
        return None;
    }

    // Picker mode: route keys to picker
    if local_diff.picker.is_some() {
        return handle_picker_key(local_diff, key, repo_root, agent).await;
    }

    let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
    let shift = key.modifiers.contains(KeyModifiers::SHIFT);

    // Reset waiting_g on any key that isn't 'g'
    if key.code != KeyCode::Char('g') || ctrl {
        local_diff.viewer.waiting_g = false;
    }

    // Handle `]c` / `[c` chord for next/prev comment thread
    if let Some(bracket) = local_diff.viewer.pending_bracket.take() {
        if key.code == KeyCode::Char('c')
            && !ctrl
            && !local_diff.viewer.file_list.focused
        {
            let cursor = local_diff.viewer.scroll.cursor;
            let target = if bracket == ']' {
                local_diff.viewer.render_list.next_badge_idx(cursor)
            } else {
                local_diff.viewer.render_list.prev_badge_idx(cursor)
            };
            if let Some(idx) = target {
                local_diff.viewer.selection_start = None;
                local_diff.viewer.scroll.cursor = idx;
                local_diff.viewer.scroll.sync_viewport_center();
                if let Some(badge) = local_diff.viewer.render_list.badge_at(idx) {
                    let thread_key = badge.thread_key.clone();
                    local_diff.open_panel_for_thread(&thread_key);
                    local_diff.viewer.panel_focused = true;
                    local_diff.viewer.file_list.focused = false;
                }
            }
        }
        return None;
    }

    match key.code {
        // Pane focus: h → left, l → right (tree ↔ diff ↔ panel)
        KeyCode::Char('h') | KeyCode::Left if !ctrl => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel_focused = false;
                local_diff.viewer.file_list.focused = false;
            } else if !local_diff.viewer.file_list.focused {
                local_diff.viewer.file_list.focused = true;
            }
        }
        KeyCode::Char('l') | KeyCode::Right if !ctrl => {
            if local_diff.viewer.file_list.focused {
                local_diff.viewer.file_list.focused = false;
                local_diff.viewer.panel_focused = false;
            } else if local_diff.viewer.panel.visible && !local_diff.viewer.panel_focused {
                local_diff.viewer.panel_focused = true;
            }
        }
        KeyCode::Char('f') if !ctrl => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel_focused = false;
                local_diff.viewer.file_list.focused = true;
            } else if local_diff.viewer.file_list.focused {
                local_diff.viewer.file_list.focused = false;
            } else if local_diff.viewer.panel.visible {
                local_diff.viewer.panel_focused = true;
            } else {
                local_diff.viewer.file_list.focused = true;
            }
        }

        // Navigation within focused pane
        KeyCode::Char('j') | KeyCode::Char('J') | KeyCode::Down if !ctrl => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel.scroll_viewport(1);
            } else if local_diff.viewer.file_list.focused {
                move_tree_cursor(local_diff, 1);
            } else {
                let extending = shift || key.code == KeyCode::Char('J');
                if extending {
                    if local_diff.viewer.selection_start.is_none() {
                        local_diff.viewer.selection_start = Some(local_diff.viewer.scroll.cursor);
                    }
                } else {
                    local_diff.viewer.selection_start = None;
                }
                local_diff.viewer.scroll_down(1);
            }
        }
        KeyCode::Char('k') | KeyCode::Char('K') | KeyCode::Up if !ctrl => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel.scroll_viewport(-1);
            } else if local_diff.viewer.file_list.focused {
                move_tree_cursor(local_diff, -1);
            } else {
                let extending = shift || key.code == KeyCode::Char('K');
                if extending {
                    if local_diff.viewer.selection_start.is_none() {
                        local_diff.viewer.selection_start = Some(local_diff.viewer.scroll.cursor);
                    }
                } else {
                    local_diff.viewer.selection_start = None;
                }
                local_diff.viewer.scroll_up(1);
            }
        }

        // Enter: select file in tree, toggle panel on thread, start comment on diff line
        KeyCode::Enter => {
            if local_diff.viewer.file_list.focused {
                let cursor = local_diff.viewer.file_list.cursor();
                let is_dir = cursor < local_diff.viewer.file_list.entries.len()
                    && local_diff.viewer.file_list.entries[cursor].is_dir;
                if !is_dir {
                    select_tree_entry(local_diff).await;
                    local_diff.viewer.file_list.focused = false;
                }
            } else if local_diff.viewer.panel.visible {
                // Panel is open — close it
                local_diff.viewer.panel.close();
                local_diff.viewer.panel_focused = false;
            } else if !local_diff.viewer.render_list.is_empty() {
                let cursor = local_diff.viewer.scroll.cursor;
                // Check if this diff line has a badge — open thread panel
                if let Some(badge) = local_diff.viewer.render_list.badge_at(cursor) {
                    let thread_key = badge.thread_key.clone();
                    local_diff.open_panel_for_thread(&thread_key);
                    local_diff.viewer.panel_focused = true;
                    local_diff.viewer.file_list.focused = false;
                } else if let Some(crate::ui::diff_viewer::RenderItem::DiffLine(dl)) =
                    local_diff.viewer.render_list.get(cursor)
                {
                    let filename = local_diff.current_filename();
                    let line = dl.new_line_no.or(dl.old_line_no).unwrap_or(0);
                    let side = if dl.new_line_no.is_some() { "RIGHT" } else { "LEFT" };
                    local_diff.composing.start_new(filename, line, side.to_string());
                }
            }
        }

        // Tree navigation via ctrl+j/k — skip directories
        KeyCode::Char('j') if ctrl => {
            move_tree_cursor_skip_dirs(local_diff, 1);
            select_tree_entry(local_diff).await;
        }
        KeyCode::Char('k') if ctrl => {
            move_tree_cursor_skip_dirs(local_diff, -1);
            select_tree_entry(local_diff).await;
        }

        // Half-page scroll — respects focused pane
        KeyCode::Char('d') if ctrl => {
            if local_diff.viewer.panel_focused {
                let half = local_diff.viewer.viewport_height() as i32 / 2;
                local_diff.viewer.panel.scroll_viewport(half);
            } else if local_diff.viewer.file_list.focused {
                let half = local_diff.viewer.viewport_height() as usize / 2;
                move_tree_cursor_n(local_diff, half as i32);
            } else {
                local_diff.viewer.scroll_half_page_down();
            }
        }
        KeyCode::Char('u') if ctrl => {
            if local_diff.viewer.panel_focused {
                let half = local_diff.viewer.viewport_height() as i32 / 2;
                local_diff.viewer.panel.scroll_viewport(-half);
            } else if local_diff.viewer.file_list.focused {
                let half = local_diff.viewer.viewport_height() as usize / 2;
                move_tree_cursor_n(local_diff, -(half as i32));
            } else {
                local_diff.viewer.scroll_half_page_up();
            }
        }

        // Full-page scroll
        KeyCode::Char('f') if ctrl => {
            if local_diff.viewer.panel_focused {
                let page = local_diff.viewer.viewport_height() as i32;
                local_diff.viewer.panel.scroll_viewport(page);
            } else if local_diff.viewer.file_list.focused {
                let page = local_diff.viewer.viewport_height() as i32;
                move_tree_cursor_n(local_diff, page);
            } else {
                local_diff.viewer.scroll_full_page_down();
            }
        }
        KeyCode::Char('b') if ctrl => {
            if local_diff.viewer.panel_focused {
                let page = local_diff.viewer.viewport_height() as i32;
                local_diff.viewer.panel.scroll_viewport(-page);
            } else if local_diff.viewer.file_list.focused {
                let page = local_diff.viewer.viewport_height() as i32;
                move_tree_cursor_n(local_diff, -page);
            } else {
                local_diff.viewer.scroll_full_page_up();
            }
        }

        // Top (gg — vim-style double tap)
        KeyCode::Char('g') if !ctrl => {
            if local_diff.viewer.waiting_g {
                local_diff.viewer.waiting_g = false;
                if local_diff.viewer.panel_focused {
                    local_diff.viewer.panel.goto_top();
                } else if local_diff.viewer.file_list.focused {
                    let first_file = local_diff.viewer.file_list.entries
                        .iter()
                        .position(|e| !e.is_dir)
                        .unwrap_or(0);
                    local_diff.viewer.file_list.set_cursor(first_file);
                } else {
                    local_diff.viewer.goto_top();
                }
            } else {
                local_diff.viewer.waiting_g = true;
            }
        }
        KeyCode::Char('G') => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel.goto_bottom();
            } else if local_diff.viewer.file_list.focused {
                // Jump to last non-directory entry
                let last_file = local_diff.viewer.file_list.entries
                    .iter()
                    .rposition(|e| !e.is_dir)
                    .unwrap_or(local_diff.viewer.file_list.entries.len().saturating_sub(1));
                local_diff.viewer.file_list.set_cursor(last_file);
            } else {
                local_diff.viewer.goto_bottom();
            }
        }

        // Next/prev comment thread chord: `]c` / `[c`
        KeyCode::Char(']') if !ctrl => {
            local_diff.viewer.pending_bracket = Some(']');
        }
        KeyCode::Char('[') if !ctrl => {
            local_diff.viewer.pending_bracket = Some('[');
        }

        // Mode cycling
        KeyCode::Char('m') => {
            let is_default = local_diff.base_branch == local_diff.default_branch;
            local_diff.cycle_mode(is_default);
            local_diff.reload_diff();
        }

        // Staging
        KeyCode::Char('s') if !shift && !ctrl => {
            stage_current_line(local_diff, repo_root, false).await;
        }
        KeyCode::Char('S') => {
            stage_current_hunk(local_diff, repo_root, false).await;
        }
        KeyCode::Char('u') if !ctrl && !shift => {
            stage_current_line(local_diff, repo_root, true).await;
        }
        KeyCode::Char('U') => {
            stage_current_hunk(local_diff, repo_root, true).await;
        }

        // Search
        KeyCode::Char('/') => {
            let cursor = local_diff.viewer.scroll.cursor;
            let offset = local_diff.viewer.scroll.offset;
            local_diff.viewer.search.start(cursor, offset);
        }
        KeyCode::Char('n') if !ctrl => {
            let cursor = local_diff.viewer.scroll.cursor;
            if let Some(line) = local_diff.viewer.search.next_match(cursor) {
                local_diff.viewer.scroll.cursor = line;
                local_diff.viewer.scroll_down(0);
            }
        }
        KeyCode::Char('N') => {
            let cursor = local_diff.viewer.scroll.cursor;
            if let Some(line) = local_diff.viewer.search.prev_match(cursor) {
                local_diff.viewer.scroll.cursor = line;
                local_diff.viewer.scroll_up(0);
            }
        }

        // Ask Copilot about the current hunk
        KeyCode::Char('c') if !ctrl => {
            if !local_diff.viewer.file_list.focused && !local_diff.viewer.render_list.is_empty() {
                let cursor = local_diff.viewer.scroll.cursor;
                if let Some(dl) = local_diff.viewer.render_list.get_diff_line(cursor) {
                    let filename = local_diff.current_filename();
                    let line = dl.new_line_no.or(dl.old_line_no).unwrap_or(0);
                    let side = if dl.new_line_no.is_some() { "RIGHT" } else { "LEFT" };
                    local_diff.composing.start_new(filename, line, side.to_string());
                }
            }
        }

        // Reply to existing comment thread
        KeyCode::Char('r') if !ctrl => {
            if local_diff.viewer.panel_focused && local_diff.viewer.panel.visible {
                // Reply from the panel: use the panel's thread
                if let Some(thread_key) = local_diff.viewer.panel.thread_key.clone() {
                    let file_path = local_diff.viewer.panel.file_path.clone();
                    let line = local_diff.viewer.panel.panel_line;
                    let side = "RIGHT".to_string();
                    let mode = if local_diff.comment_store.thread_has_copilot_comment(&thread_key) {
                        crate::ui::diff_viewer::panel::ReplyMode::Copilot
                    } else {
                        crate::ui::diff_viewer::panel::ReplyMode::GitHub
                    };
                    local_diff.composing.start_reply_with_mode(thread_key, file_path, line, side, mode);
                }
            } else if !local_diff.viewer.file_list.focused && !local_diff.viewer.render_list.is_empty() {
                let cursor = local_diff.viewer.scroll.cursor;
                if let Some(badge) = local_diff.viewer.render_list.badge_at(cursor) {
                    let thread_key = badge.thread_key.clone();
                    let filename = local_diff.current_filename();
                    let line = badge.line;
                    let side = badge.side.clone();
                    let mode = if local_diff.comment_store.thread_has_copilot_comment(&thread_key) {
                        crate::ui::diff_viewer::panel::ReplyMode::Copilot
                    } else {
                        crate::ui::diff_viewer::panel::ReplyMode::GitHub
                    };
                    local_diff.composing.start_reply_with_mode(thread_key.clone(), filename, line, side, mode);
                    local_diff.open_panel_for_thread(&thread_key);
                }
            }
        }

        KeyCode::Esc => {
            if local_diff.viewer.panel.visible {
                local_diff.viewer.panel.close();
                local_diff.viewer.panel_focused = false;
            } else {
                local_diff.viewer.search.clear();
                local_diff.viewer.selection_start = None;
            }
        }

        // Close panel with q (app.rs prevents quit when panel is visible)
        KeyCode::Char('q') if local_diff.viewer.panel.visible => {
            local_diff.viewer.panel.close();
            local_diff.viewer.panel_focused = false;
        }

        // Resolve/unresolve thread (panel open or cursor on badge)
        KeyCode::Char('x') if !ctrl => {
            let thread_key = if local_diff.viewer.panel.visible {
                local_diff.viewer.panel.thread_key.clone()
            } else {
                let cursor = local_diff.viewer.scroll.cursor;
                local_diff.viewer.render_list.badge_at(cursor)
                    .map(|b| b.thread_key.clone())
            };
            if let Some(root_id) = thread_key {
                let currently_resolved = local_diff.comment_store.comments.iter()
                    .find(|c| c.id == root_id)
                    .map(|c| c.resolved)
                    .unwrap_or(false);
                let _ = local_diff.comment_store.resolve(&root_id, !currently_resolved);
                if local_diff.viewer.panel.visible {
                    local_diff.viewer.panel.close();
                    local_diff.viewer.panel_focused = false;
                }
                let filename = local_diff.current_filename();
                local_diff.place_file_comments(&filename);
            }
        }

        // File picker (Ctrl+P)
        KeyCode::Char('p') if ctrl => {
            let items: Vec<PickerItem> = local_diff.viewer.file_list.files.iter()
                .map(|f| PickerItem {
                    label: f.filename.clone(),
                    description: format!("+{} -{}", f.additions, f.deletions),
                    value: f.filename.clone(),
                })
                .collect();
            local_diff.picker = Some(Picker::new("Files", items));
            local_diff.picker_kind = "file".to_string();
        }

        // Help (?)
        KeyCode::Char('?') if !ctrl => {
            open_help_palette(local_diff);
        }

        // Commit picker (C)
        KeyCode::Char('C') if !ctrl => {
            open_commit_picker(local_diff, repo_root).await;
        }

        _ => {}
    }
    None
}

pub fn open_command_palette(local_diff: &mut LocalDiff) {
    let cmd = |name: &str, desc: &str| PickerItem {
        label: name.into(),
        description: desc.into(),
        value: format!("cmd:{name}"),
    };

    let items = vec![
        cmd("refresh", "Reload diff from disk"),
        cmd("commit", "Commit, push, or open PR"),
        cmd("view-on-github", "Open PR in browser"),
        cmd("set-merge-base", "Choose merge base branch"),
        cmd("quit", "Quit the application"),
    ];
    local_diff.picker = Some(Picker::new("Commands", items));
    local_diff.picker_kind = "command".to_string();
}

fn open_help_palette(local_diff: &mut LocalDiff) {
    let cmd = |name: &str, desc: &str| PickerItem {
        label: name.into(),
        description: desc.into(),
        value: format!("cmd:{name}"),
    };
    let key = |keys: &str, desc: &str| PickerItem {
        label: keys.into(),
        description: desc.into(),
        value: String::new(),
    };

    let items = vec![
        cmd("refresh", "Reload diff from disk"),
        cmd("commit", "Commit, push, or open PR"),
        cmd("view-on-github", "Open PR in browser"),
        cmd("set-merge-base", "Choose merge base branch"),
        cmd("quit", "Quit the application"),
        key("j/k", "Move cursor up/down"),
        key("h/l", "Focus tree/diff/panel"),
        key("Ctrl+d/u", "Half-page down/up"),
        key("Ctrl+f/b", "Full-page down/up"),
        key("gg/G", "Go to top/bottom"),
        key("]c/[c", "Next/prev comment thread"),
        key("/", "Search in file"),
        key("n/N", "Next/prev search match"),
        key("Ctrl+p", "File picker"),
        key(":", "Command palette"),
        key("s/u", "Stage/unstage line"),
        key("S/U", "Stage/unstage hunk"),
        key("C", "Commit/push/PR"),
        key("c", "Ask Copilot about hunk"),
        key("r", "Reply to comment"),
        key("x", "Resolve/unresolve thread"),
        key("Enter", "Open comment thread / start comment"),
        key("m", "Cycle diff mode"),
    ];
    local_diff.picker = Some(Picker::new("Help", items));
    local_diff.picker_kind = "command".to_string();
}

fn handle_search_key(local_diff: &mut LocalDiff, key: KeyEvent) {
    match key.code {
        KeyCode::Enter => {
            local_diff.viewer.search.confirm();
        }
        KeyCode::Esc => {
            // Restore cursor/offset to where `/` was pressed
            let origin_cursor = local_diff.viewer.search.origin_cursor;
            let origin_offset = local_diff.viewer.search.origin_offset;
            local_diff.viewer.search.cancel();
            local_diff.viewer.scroll.cursor = origin_cursor;
            local_diff.viewer.scroll.offset = origin_offset;
        }
        KeyCode::Backspace => {
            let mut q = local_diff.viewer.search.query.clone();
            q.pop();
            local_diff.viewer.search.set_query(&q, &local_diff.viewer.render_list);
            // Incsearch: re-search from origin
            incsearch_jump(local_diff);
        }
        KeyCode::Char(c) => {
            let mut q = local_diff.viewer.search.query.clone();
            q.push(c);
            local_diff.viewer.search.set_query(&q, &local_diff.viewer.render_list);
            // Incsearch: search from origin
            incsearch_jump(local_diff);
        }
        _ => {}
    }
}

/// Jump to the first match at or after the search origin cursor.
fn incsearch_jump(local_diff: &mut LocalDiff) {
    use crate::ui::scroll::Scrollable;
    let origin = local_diff.viewer.search.origin_cursor;
    if let Some(line) = local_diff.viewer.search.next_match_inclusive(origin) {
        local_diff.viewer.scroll.cursor = line;
        local_diff.viewer.scroll_down(0);
    }
}

async fn handle_picker_key(local_diff: &mut LocalDiff, key: KeyEvent, repo_root: &str, agent: &Arc<dyn AgentRunner>) -> Option<String> {
    let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
    match key.code {
        KeyCode::Esc => {
            local_diff.picker = None;
        }
        KeyCode::Enter => {
            let selected = local_diff.picker.as_ref().and_then(|p| p.selected().cloned());
            let kind = local_diff.picker_kind.clone();
            local_diff.picker = None;

            if let Some(item) = selected {
                match kind.as_str() {
                    "file" => {
                        // Jump to the selected file
                        if let Some(idx) = local_diff.viewer.file_list.files.iter()
                            .position(|f| f.filename == item.value)
                        {
                            local_diff.viewer.file_list.current_file_idx = idx;
                            local_diff.viewer.scroll.cursor = 0;
                            local_diff.viewer.scroll.offset = 0;
                            local_diff.refresh_current_file(false).await;
                            // Sync file list cursor to match
                            if let Some(entry_idx) = local_diff.viewer.file_list.entries.iter()
                                .position(|e| !e.is_dir && e.file_index as usize == idx)
                            {
                                local_diff.viewer.file_list.set_cursor(entry_idx);
                            }
                        }
                    }
                    "merge-base" => {
                        local_diff.merge_base_ref = item.value.clone();
                        local_diff.mode = DiffMode::Branch;
                        local_diff.load_diff();
                    }
                    "commit" => {
                        start_commit_flow(local_diff, &item.value, repo_root, agent).await;
                    }
                    "command" => {
                        if let Some(cmd) = item.value.strip_prefix("cmd:") {
                            return Some(cmd.to_string());
                        }
                    }
                    _ => {}
                }
            }
        }
        KeyCode::Up | KeyCode::Char('k') if ctrl || key.code == KeyCode::Up => {
            if let Some(p) = &mut local_diff.picker {
                p.move_up();
            }
        }
        KeyCode::Down | KeyCode::Char('j') if ctrl || key.code == KeyCode::Down => {
            if let Some(p) = &mut local_diff.picker {
                p.move_down();
            }
        }
        KeyCode::Backspace => {
            if let Some(p) = &mut local_diff.picker {
                p.pop_char();
            }
        }
        KeyCode::Char(c) => {
            if let Some(p) = &mut local_diff.picker {
                p.push_char(c);
            }
        }
        _ => {}
    }
    None
}

pub fn move_tree_cursor(local_diff: &mut LocalDiff, delta: i32) {
    let entries = &local_diff.viewer.file_list.entries;
    if entries.is_empty() {
        return;
    }
    let max = entries.len() as i32 - 1;
    let step = if delta > 0 { 1 } else { -1 };
    let mut pos = local_diff.viewer.file_list.cursor() as i32 + step;
    while pos >= 0 && pos <= max {
        if !entries[pos as usize].is_dir {
            local_diff.viewer.file_list.set_cursor(pos as usize);
            return;
        }
        pos += step;
    }
}

/// Like move_tree_cursor but skips directory entries, landing on the next file.
fn move_tree_cursor_skip_dirs(local_diff: &mut LocalDiff, delta: i32) {
    move_tree_cursor(local_diff, delta);
}

/// Move tree cursor by N file entries (skipping directories).
fn move_tree_cursor_n(local_diff: &mut LocalDiff, n: i32) {
    let steps = n.unsigned_abs() as usize;
    let dir = if n > 0 { 1 } else { -1 };
    for _ in 0..steps {
        move_tree_cursor(local_diff, dir);
    }
}

async fn select_tree_entry(local_diff: &mut LocalDiff) {
    let cursor = local_diff.viewer.file_list.cursor();
    if cursor >= local_diff.viewer.file_list.entries.len() {
        return;
    }
    let entry = &local_diff.viewer.file_list.entries[cursor];
    if entry.is_dir {
        return;
    }
    let file_idx = entry.file_index as usize;
    if file_idx >= local_diff.viewer.file_list.files.len() {
        return;
    }
    local_diff.viewer.file_list.current_file_idx = file_idx;
    local_diff.viewer.scroll.cursor = 0;
    local_diff.viewer.scroll.offset = 0;
    local_diff.refresh_current_file(false).await;
}

async fn stage_current_line(local_diff: &mut LocalDiff, repo_root: &str, unstage: bool) {
    if local_diff.viewer.file_list.files.is_empty() {
        return;
    }
    let file = &local_diff.viewer.file_list.files[local_diff.viewer.file_list.current_file_idx];
    let cursor = local_diff.viewer.scroll.cursor;

    let patch_lines: Vec<&str> = file.patch.lines().collect();
    if cursor >= patch_lines.len() {
        return;
    }

    let line = patch_lines[cursor];
    if !line.starts_with('+') && !line.starts_with('-') {
        return;
    }

    let mut old_num = 0i32;
    let mut new_num = 0i32;
    let mut target_old = Vec::new();
    let mut target_new = Vec::new();

    for (i, pl) in patch_lines.iter().enumerate() {
        if pl.starts_with("@@") {
            let parts: Vec<&str> = pl.split_whitespace().collect();
            if parts.len() >= 3 {
                old_num = parts[1]
                    .trim_start_matches('-')
                    .split(',')
                    .next()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0);
                new_num = parts[2]
                    .trim_start_matches('+')
                    .split(',')
                    .next()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0);
            }
            continue;
        }

        if i == cursor {
            if pl.starts_with('+') {
                target_new.push(new_num);
            } else if pl.starts_with('-') {
                target_old.push(old_num);
            }
        }

        if pl.starts_with('+') {
            new_num += 1;
        } else if pl.starts_with('-') {
            old_num += 1;
        } else {
            old_num += 1;
            new_num += 1;
        }
    }

    let result = crate::git::stage::stage_lines(
        repo_root,
        &file.filename,
        &file.status,
        &file.patch,
        &target_new,
        &target_old,
        unstage,
    )
    .await;

    if let Err(e) = result {
        tracing::error!("Stage failed: {e}");
        return;
    }

    local_diff.load_diff();
}

async fn stage_current_hunk(local_diff: &mut LocalDiff, repo_root: &str, unstage: bool) {
    if local_diff.viewer.file_list.files.is_empty() {
        return;
    }
    let file = &local_diff.viewer.file_list.files[local_diff.viewer.file_list.current_file_idx];
    let cursor = local_diff.viewer.scroll.cursor;

    let patch_lines: Vec<&str> = file.patch.lines().collect();
    if cursor >= patch_lines.len() {
        return;
    }

    let mut new_num = 0i32;
    for (i, pl) in patch_lines.iter().enumerate() {
        if pl.starts_with("@@") {
            let parts: Vec<&str> = pl.split_whitespace().collect();
            if parts.len() >= 3 {
                new_num = parts[2]
                    .trim_start_matches('+')
                    .split(',')
                    .next()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0);
            }
            continue;
        }
        if i == cursor {
            break;
        }
        if pl.starts_with('+') || pl.starts_with(' ') || !pl.starts_with('-') {
            new_num += 1;
        }
    }

    let result = crate::git::stage::stage_hunk(
        repo_root,
        &file.filename,
        &file.status,
        &file.patch,
        new_num,
        "RIGHT",
        unstage,
    )
    .await;

    if let Err(e) = result {
        tracing::error!("Stage hunk failed: {e}");
        return;
    }

    local_diff.load_diff();
}

async fn handle_composing_key(
    local_diff: &mut LocalDiff,
    key: KeyEvent,
    agent: &Arc<dyn AgentRunner>,
) {
    match key.code {
        KeyCode::Esc => {
            local_diff.composing.cancel();
            // Keep the panel open — just cancel the reply input
        }
        KeyCode::BackTab => {
            local_diff.composing.toggle_reply_mode();
        }
        KeyCode::Enter => {
            if key.modifiers.contains(KeyModifiers::SHIFT)
                || key.modifiers.contains(KeyModifiers::ALT)
            {
                local_diff.composing.input.push('\n');
            } else {
                local_diff.submit_comment(agent).await;
            }
        }
        KeyCode::Backspace => {
            local_diff.composing.input.pop();
        }
        KeyCode::Char(c) => {
            local_diff.composing.input.push(c);
        }
        _ => {}
    }
}

async fn handle_commit_key(
    local_diff: &mut LocalDiff,
    key: KeyEvent,
    repo_root: &str,
    _agent: &Arc<dyn AgentRunner>,
) -> Option<String> {
    let overlay = match &mut local_diff.commit_overlay {
        Some(o) => o,
        None => return None,
    };

    match overlay.handle_key(key) {
        CommitKeyResult::Continue => {}
        CommitKeyResult::Cancel => {
            local_diff.commit_overlay = None;
        }
        CommitKeyResult::Execute => {
            let action = overlay.action;
            let message = overlay.input.clone();
            let repo_root = repo_root.to_string();
            let tx = local_diff.commit_tx.clone();
            tokio::spawn(async move {
                commit::execute_commit_action(action, &message, &repo_root, tx).await;
            });
        }
        CommitKeyResult::OpenEditor(path) => {
            return Some(format!("open-editor:{path}"));
        }
    }
    None
}

pub async fn open_commit_picker(local_diff: &mut LocalDiff, repo_root: &str) {
    use crate::git::commit as git_commit;

    let has_staged = git_commit::has_staged_changes(repo_root).await;
    let has_unstaged = git_commit::has_unstaged_changes(repo_root).await;
    let has_unpushed = git_commit::has_unpushed_commits(repo_root).await;
    let has_pr = git_commit::has_open_pr(repo_root).await;

    let mut items = Vec::new();

    if has_staged {
        items.push(PickerItem {
            label: "Commit".into(),
            description: "Commit staged changes".into(),
            value: "Commit".into(),
        });
        items.push(PickerItem {
            label: "Commit & Push".into(),
            description: "Commit and push to remote".into(),
            value: "CommitAndPush".into(),
        });
    }

    if has_unstaged {
        items.push(PickerItem {
            label: "Commit All".into(),
            description: "Stage all and commit".into(),
            value: "CommitAll".into(),
        });
        items.push(PickerItem {
            label: "Commit All & Push".into(),
            description: "Stage all, commit, and push".into(),
            value: "CommitAllAndPush".into(),
        });
    }

    if has_unpushed || has_staged {
        items.push(PickerItem {
            label: "Push".into(),
            description: "Push commits to remote".into(),
            value: "Push".into(),
        });
    }

    if !has_pr {
        items.push(PickerItem {
            label: "Open PR".into(),
            description: "Create a pull request".into(),
            value: "OpenPR".into(),
        });
    }

    if items.is_empty() {
        local_diff.flash.error("Nothing to commit, push, or PR".to_string());
        return;
    }

    local_diff.picker = Some(Picker::new("Git", items));
    local_diff.picker_kind = "commit".to_string();
}

async fn start_commit_flow(
    local_diff: &mut LocalDiff,
    action_str: &str,
    repo_root: &str,
    agent: &Arc<dyn AgentRunner>,
) {
    let action = match action_str {
        "Commit" => CommitAction::Commit,
        "CommitAndPush" => CommitAction::CommitAndPush,
        "Push" => CommitAction::Push,
        "OpenPR" => CommitAction::OpenPR,
        "CommitAll" => CommitAction::CommitAll,
        "CommitAllAndPush" => CommitAction::CommitAllAndPush,
        _ => return,
    };

    let overlay = CommitOverlay::new(action);

    // Push: execute immediately, no message needed
    if action == CommitAction::Push {
        let repo_root = repo_root.to_string();
        let tx = local_diff.commit_tx.clone();
        tokio::spawn(async move {
            commit::execute_commit_action(CommitAction::Push, "", &repo_root, tx).await;
        });
        local_diff.commit_overlay = Some(overlay);
        return;
    }

    // For actions needing a message, start AI generation
    let gen_id = format!("{COMMIT_GEN_PREFIX}-{}", uuid_v4_short());
    let repo_root_owned = repo_root.to_string();
    let branch = local_diff.base_branch.clone();
    let is_pr = action == CommitAction::OpenPR;
    let agent = agent.clone();

    tokio::spawn(async move {
        let prompt = if is_pr {
            let diff = crate::git::commit::branch_diff(&repo_root_owned)
                .await
                .unwrap_or_default();
            let log = crate::git::commit::branch_log(&repo_root_owned)
                .await
                .unwrap_or_default();
            commit::build_pr_prompt(&diff, &log, &branch, None)
        } else {
            let diff = crate::git::commit::staged_diff(&repo_root_owned)
                .await
                .unwrap_or_default();
            commit::build_commit_prompt(&diff, &branch, None)
        };

        if let Err(e) = agent.send(&gen_id, &prompt).await {
            tracing::error!("Failed to start AI generation: {e}");
        }
    });

    local_diff.commit_overlay = Some(overlay);
}

fn uuid_v4_short() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let t = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    format!("{t:x}")
}
