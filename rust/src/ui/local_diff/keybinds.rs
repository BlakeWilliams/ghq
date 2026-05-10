use std::sync::Arc;
use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};

use crate::agent::AgentRunner;
use super::LocalDiff;

pub async fn handle_key(
    local_diff: &mut LocalDiff,
    key: KeyEvent,
    repo_root: &str,
    agent: &Arc<dyn AgentRunner>,
) {
    // Composing mode: route all keys to the input buffer
    if local_diff.composing.is_active() {
        handle_composing_key(local_diff, key, agent).await;
        return;
    }

    let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
    let shift = key.modifiers.contains(KeyModifiers::SHIFT);

    match key.code {
        // Pane focus: h → left, l → right (tree ↔ diff ↔ panel)
        KeyCode::Char('h') | KeyCode::Left if !ctrl => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel_focused = false;
                local_diff.viewer.tree_focused = false;
            } else if !local_diff.viewer.tree_focused {
                local_diff.viewer.tree_focused = true;
            }
        }
        KeyCode::Char('l') | KeyCode::Right if !ctrl => {
            if local_diff.viewer.tree_focused {
                local_diff.viewer.tree_focused = false;
                local_diff.viewer.panel_focused = false;
            } else if local_diff.viewer.panel.visible && !local_diff.viewer.panel_focused {
                local_diff.viewer.panel_focused = true;
            }
        }
        KeyCode::Char('f') if !ctrl => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel_focused = false;
                local_diff.viewer.tree_focused = true;
            } else if local_diff.viewer.tree_focused {
                local_diff.viewer.tree_focused = false;
            } else if local_diff.viewer.panel.visible {
                local_diff.viewer.panel_focused = true;
            } else {
                local_diff.viewer.tree_focused = true;
            }
        }

        // Navigation within focused pane
        KeyCode::Char('j') if !ctrl && !shift => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel.scroll_down(1);
            } else if local_diff.viewer.tree_focused {
                move_tree_cursor(local_diff, 1);
            } else {
                local_diff.viewer.scroll_down(1);
            }
        }
        KeyCode::Char('k') if !ctrl && !shift => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel.scroll_up(1);
            } else if local_diff.viewer.tree_focused {
                move_tree_cursor(local_diff, -1);
            } else {
                local_diff.viewer.scroll_up(1);
            }
        }
        KeyCode::Down => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel.scroll_down(1);
            } else if local_diff.viewer.tree_focused {
                move_tree_cursor(local_diff, 1);
            } else {
                local_diff.viewer.scroll_down(1);
            }
        }
        KeyCode::Up => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel.scroll_up(1);
            } else if local_diff.viewer.tree_focused {
                move_tree_cursor(local_diff, -1);
            } else {
                local_diff.viewer.scroll_up(1);
            }
        }

        // Enter: select file in tree, toggle panel on thread, start comment on diff line
        KeyCode::Enter => {
            if local_diff.viewer.tree_focused {
                let cursor = local_diff.viewer.tree_cursor;
                let is_dir = cursor < local_diff.viewer.tree_entries.len()
                    && local_diff.viewer.tree_entries[cursor].is_dir;
                if !is_dir {
                    select_tree_entry(local_diff).await;
                    local_diff.viewer.tree_focused = false;
                }
            } else if local_diff.viewer.panel.visible {
                // Panel is open — close it
                local_diff.viewer.panel.close();
                local_diff.viewer.panel_focused = false;
            } else if !local_diff.viewer.render_list.is_empty() {
                let cursor = local_diff.viewer.diff_cursor;
                let item = local_diff.viewer.render_list.get(cursor);
                match item {
                    Some(crate::ui::diff_viewer::RenderItem::CommentThread(t)) => {
                        // Open panel for this thread
                        let thread_key = t.thread_key.clone();
                        local_diff.refresh_panel_for_thread(&thread_key);
                        local_diff.viewer.panel_focused = true;
                        local_diff.viewer.tree_focused = false;
                    }
                    Some(crate::ui::diff_viewer::RenderItem::DiffLine(dl)) => {
                        let filename = local_diff.current_filename();
                        let line = dl.new_line_no.or(dl.old_line_no).unwrap_or(0);
                        let side = if dl.new_line_no.is_some() { "RIGHT" } else { "LEFT" };
                        local_diff.composing.start_new(filename, line, side.to_string());
                    }
                    None => {}
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
                let half = local_diff.viewer.viewport_height() as usize / 2;
                local_diff.viewer.panel.scroll_down(half);
            } else if local_diff.viewer.tree_focused {
                let half = local_diff.viewer.viewport_height() as usize / 2;
                move_tree_cursor_n(local_diff, half as i32);
            } else {
                local_diff.viewer.scroll_half_page_down();
            }
        }
        KeyCode::Char('u') if ctrl => {
            if local_diff.viewer.panel_focused {
                let half = local_diff.viewer.viewport_height() as usize / 2;
                local_diff.viewer.panel.scroll_up(half);
            } else if local_diff.viewer.tree_focused {
                let half = local_diff.viewer.viewport_height() as usize / 2;
                move_tree_cursor_n(local_diff, -(half as i32));
            } else {
                local_diff.viewer.scroll_half_page_up();
            }
        }

        // Top/bottom
        KeyCode::Char('g') if !ctrl => {
            if local_diff.viewer.panel_focused {
                local_diff.viewer.panel.scroll_offset = 0;
            } else if local_diff.viewer.tree_focused {
                // Jump to first non-directory entry
                let first_file = local_diff.viewer.tree_entries
                    .iter()
                    .position(|e| !e.is_dir)
                    .unwrap_or(0);
                local_diff.viewer.tree_cursor = first_file;
            } else {
                local_diff.viewer.goto_top();
            }
        }
        KeyCode::Char('G') => {
            if local_diff.viewer.panel_focused {
                let max = local_diff.viewer.panel.content_line_count().saturating_sub(1);
                local_diff.viewer.panel.scroll_offset = max;
            } else if local_diff.viewer.tree_focused {
                // Jump to last non-directory entry
                let last_file = local_diff.viewer.tree_entries
                    .iter()
                    .rposition(|e| !e.is_dir)
                    .unwrap_or(local_diff.viewer.tree_entries.len().saturating_sub(1));
                local_diff.viewer.tree_cursor = last_file;
            } else {
                local_diff.viewer.goto_bottom();
            }
        }

        // Mode cycling
        KeyCode::Char('m') => {
            local_diff.cycle_mode();
            local_diff.load_diff().await;
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
            local_diff.viewer.search.active = true;
        }
        KeyCode::Char('n') if !ctrl => {
            if let Some(line) = local_diff.viewer.search.next_match() {
                local_diff.viewer.diff_cursor = line;
                local_diff.viewer.scroll_down(0);
            }
        }
        KeyCode::Char('N') => {
            if let Some(line) = local_diff.viewer.search.prev_match() {
                local_diff.viewer.diff_cursor = line;
                local_diff.viewer.scroll_up(0);
            }
        }

        // Ask Copilot about the current hunk
        KeyCode::Char('c') if !ctrl => {
            if !local_diff.viewer.tree_focused && !local_diff.viewer.render_list.is_empty() {
                let cursor = local_diff.viewer.diff_cursor;
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
            if !local_diff.viewer.tree_focused && !local_diff.viewer.render_list.is_empty() {
                let cursor = local_diff.viewer.diff_cursor;
                if let Some(thread) = local_diff.viewer.render_list.get(cursor)
                    .and_then(|item| item.as_thread())
                {
                    let thread_key = thread.thread_key.clone();
                    let filename = local_diff.current_filename();
                    let line = thread.line;
                    let side = thread.side.clone();
                    local_diff.composing.start_reply(thread_key.clone(), filename, line, side);
                    local_diff.refresh_panel_for_thread(&thread_key);
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

        _ => {}
    }
}

pub fn move_tree_cursor(local_diff: &mut LocalDiff, delta: i32) {
    let entries = &local_diff.viewer.tree_entries;
    if entries.is_empty() {
        return;
    }
    let max = entries.len() as i32 - 1;
    let step = if delta > 0 { 1 } else { -1 };
    let mut pos = local_diff.viewer.tree_cursor as i32 + step;
    while pos >= 0 && pos <= max {
        if !entries[pos as usize].is_dir {
            local_diff.viewer.tree_cursor = pos as usize;
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
    let cursor = local_diff.viewer.tree_cursor;
    if cursor >= local_diff.viewer.tree_entries.len() {
        return;
    }
    let entry = &local_diff.viewer.tree_entries[cursor];
    if entry.is_dir {
        return;
    }
    let file_idx = entry.file_index as usize;
    if file_idx >= local_diff.viewer.files.len() {
        return;
    }
    local_diff.viewer.current_file_idx = file_idx;
    local_diff.viewer.diff_cursor = 0;
    local_diff.viewer.viewport_offset = 0;
    local_diff.refresh_current_file().await;
}

async fn stage_current_line(local_diff: &mut LocalDiff, repo_root: &str, unstage: bool) {
    if local_diff.viewer.files.is_empty() {
        return;
    }
    let file = &local_diff.viewer.files[local_diff.viewer.current_file_idx];
    let cursor = local_diff.viewer.diff_cursor;

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

    local_diff.load_diff().await;
}

async fn stage_current_hunk(local_diff: &mut LocalDiff, repo_root: &str, unstage: bool) {
    if local_diff.viewer.files.is_empty() {
        return;
    }
    let file = &local_diff.viewer.files[local_diff.viewer.current_file_idx];
    let cursor = local_diff.viewer.diff_cursor;

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

    local_diff.load_diff().await;
}

async fn handle_composing_key(
    local_diff: &mut LocalDiff,
    key: KeyEvent,
    agent: &Arc<dyn AgentRunner>,
) {
    match key.code {
        KeyCode::Esc => {
            local_diff.composing.cancel();
            local_diff.viewer.panel.close();
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
