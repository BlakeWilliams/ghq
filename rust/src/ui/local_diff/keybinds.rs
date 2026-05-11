use std::sync::Arc;
use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};

use crate::agent::AgentRunner;
use crate::ui::picker::{Picker, PickerItem};
use crate::ui::scroll::Scrollable;
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

    // Search input mode: route keys to search query
    if local_diff.viewer.search.active {
        handle_search_key(local_diff, key);
        return;
    }

    // Picker mode: route keys to picker
    if local_diff.picker.is_some() {
        handle_picker_key(local_diff, key).await;
        return;
    }

    let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
    let shift = key.modifiers.contains(KeyModifiers::SHIFT);

    // Reset waiting_g on any key that isn't 'g'
    if key.code != KeyCode::Char('g') || ctrl {
        local_diff.viewer.waiting_g = false;
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

        // Mode cycling
        KeyCode::Char('m') => {
            let is_default = local_diff.base_branch == local_diff.default_branch;
            local_diff.cycle_mode(is_default);
            local_diff.load_diff();
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

        // Help picker (?)
        KeyCode::Char('?') if !ctrl => {
            let items = vec![
                PickerItem { label: "j/k".into(), description: "Move cursor up/down".into(), value: String::new() },
                PickerItem { label: "h/l".into(), description: "Focus tree/diff/panel".into(), value: String::new() },
                PickerItem { label: "Ctrl+d/u".into(), description: "Half-page down/up".into(), value: String::new() },
                PickerItem { label: "gg/G".into(), description: "Go to top/bottom".into(), value: String::new() },
                PickerItem { label: "/".into(), description: "Search in file".into(), value: String::new() },
                PickerItem { label: "n/N".into(), description: "Next/prev search match".into(), value: String::new() },
                PickerItem { label: "Ctrl+p".into(), description: "File picker".into(), value: String::new() },
                PickerItem { label: "s/u".into(), description: "Stage/unstage line".into(), value: String::new() },
                PickerItem { label: "c".into(), description: "Ask Copilot about hunk".into(), value: String::new() },
                PickerItem { label: "r".into(), description: "Reply to comment".into(), value: String::new() },
                PickerItem { label: "x".into(), description: "Resolve/unresolve thread".into(), value: String::new() },
                PickerItem { label: "Enter".into(), description: "Open comment thread".into(), value: String::new() },
                PickerItem { label: "1-4".into(), description: "Switch diff mode".into(), value: String::new() },
                PickerItem { label: "?".into(), description: "Show this help".into(), value: String::new() },
                PickerItem { label: "q".into(), description: "Quit".into(), value: String::new() },
            ];
            local_diff.picker = Some(Picker::new("Help", items));
            local_diff.picker_kind = "help".to_string();
        }

        _ => {}
    }
}

fn handle_search_key(local_diff: &mut LocalDiff, key: KeyEvent) {
    use crate::ui::scroll::Scrollable;
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

async fn handle_picker_key(local_diff: &mut LocalDiff, key: KeyEvent) {
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
