/// Trait for components that support scrollable navigation.
/// Implement this to get uniform j/k, Ctrl+D/U, gg/G behavior
/// without per-component branching in keybinds.
pub trait Scrollable {
    fn scroll_state(&self) -> &ScrollState;
    fn scroll_state_mut(&mut self) -> &mut ScrollState;

    /// Called before scroll operations to sync total line count.
    /// Override if total is derived dynamically (e.g. from a render list).
    fn sync_scroll_total(&mut self) {}

    fn scroll_down(&mut self, n: usize) {
        self.sync_scroll_total();
        self.scroll_state_mut().scroll_down(n);
    }
    fn scroll_up(&mut self, n: usize) {
        self.scroll_state_mut().scroll_up(n);
    }
    fn scroll_half_page_down(&mut self) {
        self.sync_scroll_total();
        self.scroll_state_mut().half_page_down();
    }
    fn scroll_half_page_up(&mut self) {
        self.scroll_state_mut().half_page_up();
    }
    fn scroll_full_page_down(&mut self) {
        self.sync_scroll_total();
        self.scroll_state_mut().full_page_down();
    }
    fn scroll_full_page_up(&mut self) {
        self.scroll_state_mut().full_page_up();
    }
    fn scroll_viewport(&mut self, delta: i32) {
        self.sync_scroll_total();
        self.scroll_state_mut().scroll_viewport(delta);
    }
    fn goto_top(&mut self) {
        self.scroll_state_mut().goto_top();
    }
    fn goto_bottom(&mut self) {
        self.sync_scroll_total();
        self.scroll_state_mut().scroll_to_bottom();
    }
}

/// Reusable scroll state for any scrollable region.
///
/// Tracks a cursor position, viewport offset, and total content length.
/// Provides scroll/navigation methods and scrollbar thumb computation.
pub struct ScrollState {
    pub cursor: usize,
    pub offset: usize,
    viewport_height: usize,
    total: usize,
    /// Lines of padding between cursor and viewport edge.
    scroll_margin: usize,
    /// When true, the next `clamp()` or `set_total()` will snap to the bottom.
    pub pending_bottom: bool,
}

impl ScrollState {
    pub fn new() -> Self {
        Self {
            cursor: 0,
            offset: 0,
            viewport_height: 0,
            total: 0,
            scroll_margin: 0,
            pending_bottom: false,
        }
    }

    pub fn set_viewport_height(&mut self, h: usize) {
        self.viewport_height = h;
    }

    pub fn set_scroll_margin(&mut self, margin: usize) {
        self.scroll_margin = margin;
    }

    pub fn viewport_height(&self) -> usize {
        self.viewport_height
    }

    pub fn set_total(&mut self, total: usize) {
        self.total = total;
        self.clamp();
    }

    pub fn total(&self) -> usize {
        self.total
    }

    /// Move cursor down by `n`, keeping it in bounds and syncing viewport.
    pub fn scroll_down(&mut self, n: usize) {
        let max = self.total.saturating_sub(1);
        self.cursor = (self.cursor + n).min(max);
        self.sync_viewport();
    }

    /// Move cursor up by `n`, keeping it in bounds and syncing viewport.
    pub fn scroll_up(&mut self, n: usize) {
        self.cursor = self.cursor.saturating_sub(n);
        self.sync_viewport();
    }

    /// Vim-style half-page down: move both cursor and viewport together.
    pub fn half_page_down(&mut self) {
        let half = (self.viewport_height / 2).max(1);
        let max_offset = self.total.saturating_sub(self.viewport_height);
        let max_cursor = self.total.saturating_sub(1);
        self.cursor = (self.cursor + half).min(max_cursor);
        self.offset = (self.offset + half).min(max_offset);
    }

    /// Vim-style half-page up: move both cursor and viewport together.
    pub fn half_page_up(&mut self) {
        let half = (self.viewport_height / 2).max(1);
        self.cursor = self.cursor.saturating_sub(half);
        self.offset = self.offset.saturating_sub(half);
    }

    pub fn full_page_down(&mut self) {
        let page = self.viewport_height.max(1);
        let max_offset = self.total.saturating_sub(self.viewport_height);
        let max_cursor = self.total.saturating_sub(1);
        self.cursor = (self.cursor + page).min(max_cursor);
        self.offset = (self.offset + page).min(max_offset);
    }

    pub fn full_page_up(&mut self) {
        let page = self.viewport_height.max(1);
        self.cursor = self.cursor.saturating_sub(page);
        self.offset = self.offset.saturating_sub(page);
    }

    /// Scroll the viewport directly (mouse wheel). Cursor follows to stay visible.
    pub fn scroll_viewport(&mut self, delta: i32) {
        if self.total <= self.viewport_height {
            return;
        }
        let max_offset = self.total.saturating_sub(self.viewport_height);
        if delta > 0 {
            self.offset = (self.offset + delta as usize).min(max_offset);
        } else {
            self.offset = self.offset.saturating_sub((-delta) as usize);
        }
        if self.cursor < self.offset {
            self.cursor = self.offset;
        } else if self.cursor >= self.offset + self.viewport_height {
            self.cursor = self.offset + self.viewport_height - 1;
        }
    }

    pub fn goto_top(&mut self) {
        self.cursor = 0;
        self.offset = 0;
    }

    pub fn goto_bottom(&mut self) {
        self.cursor = self.total.saturating_sub(1);
        self.sync_viewport();
    }

    /// Scroll to the very end — for content-only scrolling (no cursor concept).
    /// If total is known, snaps immediately. Otherwise sets `pending_bottom`
    /// which is resolved on the next `set_total()` or `clamp()` call.
    pub fn scroll_to_bottom(&mut self) {
        if self.total > 0 {
            self.offset = self.total.saturating_sub(self.viewport_height);
            self.cursor = self.total.saturating_sub(1);
        } else {
            self.pending_bottom = true;
        }
    }

    /// Clamp offset so you can't scroll past the last screenful.
    pub fn clamp(&mut self) {
        if self.pending_bottom && self.total > 0 {
            self.pending_bottom = false;
            self.offset = self.total.saturating_sub(self.viewport_height);
            self.cursor = self.total.saturating_sub(1);
            return;
        }
        let max_offset = self.total.saturating_sub(self.viewport_height);
        if self.offset > max_offset {
            self.offset = max_offset;
        }
        if self.total > 0 && self.cursor >= self.total {
            self.cursor = self.total - 1;
        }
    }

    /// Compute scrollbar thumb position and length.
    /// Returns `(-1, 0)` if no scrollbar needed (content fits in viewport).
    pub fn scrollbar(&self) -> (i32, i32) {
        if self.total <= self.viewport_height || self.total == 0 {
            return (-1, 0);
        }
        let mut thumb_len = (self.viewport_height * self.viewport_height / self.total) as i32;
        if thumb_len < 1 {
            thumb_len = 1;
        }
        let scrollable = self.total - self.viewport_height;
        let offset = self.offset.min(scrollable);
        let thumb_start =
            (offset * (self.viewport_height - thumb_len as usize) / scrollable) as i32;
        (thumb_start, thumb_len)
    }

    /// Adjust viewport offset so the cursor is visible.
    pub fn ensure_visible(&mut self) {
        self.sync_viewport();
    }

    fn sync_viewport(&mut self) {
        if self.viewport_height == 0 {
            return;
        }
        // Effective margin: can't exceed half the viewport (otherwise cursor
        // could never satisfy both top and bottom margin simultaneously).
        let margin = self.scroll_margin.min(self.viewport_height / 2);
        let max_offset = self.total.saturating_sub(self.viewport_height);

        if self.cursor < self.offset + margin {
            self.offset = self.cursor.saturating_sub(margin);
        } else if self.cursor + margin >= self.offset + self.viewport_height {
            self.offset = (self.cursor + margin + 1).saturating_sub(self.viewport_height);
        }
        if self.offset > max_offset {
            self.offset = max_offset;
        }
    }

    /// Re-center the viewport around the cursor.
    pub fn sync_viewport_center(&mut self) {
        if self.viewport_height == 0 || self.total == 0 {
            return;
        }
        let half = self.viewport_height / 2;
        self.offset = self.cursor.saturating_sub(half);
        let max_offset = self.total.saturating_sub(self.viewport_height);
        if self.offset > max_offset {
            self.offset = max_offset;
        }
    }
}

impl Default for ScrollState {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scroll_down_clamps() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.set_total(20);
        s.scroll_down(5);
        assert_eq!(s.cursor, 5);
        assert_eq!(s.offset, 0);
        s.scroll_down(100);
        assert_eq!(s.cursor, 19);
    }

    #[test]
    fn scroll_up_clamps() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.set_total(20);
        s.cursor = 5;
        s.scroll_up(3);
        assert_eq!(s.cursor, 2);
        s.scroll_up(100);
        assert_eq!(s.cursor, 0);
    }

    #[test]
    fn half_page_moves_both() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.set_total(50);
        s.half_page_down();
        assert_eq!(s.cursor, 5);
        assert_eq!(s.offset, 5);
        s.half_page_up();
        assert_eq!(s.cursor, 0);
        assert_eq!(s.offset, 0);
    }

    #[test]
    fn scrollbar_hidden_when_fits() {
        let mut s = ScrollState::new();
        s.set_viewport_height(20);
        s.set_total(10);
        assert_eq!(s.scrollbar(), (-1, 0));
    }

    #[test]
    fn scrollbar_visible_when_overflows() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.set_total(30);
        let (start, len) = s.scrollbar();
        assert!(start >= 0);
        assert!(len >= 1);
    }

    #[test]
    fn clamp_prevents_overscroll() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.set_total(20);
        s.offset = 100;
        s.cursor = 100;
        s.clamp();
        assert_eq!(s.offset, 10);
        assert_eq!(s.cursor, 19);
    }

    #[test]
    fn scroll_to_bottom_works() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.set_total(30);
        s.scroll_to_bottom();
        assert_eq!(s.offset, 20);
        assert_eq!(s.cursor, 29);
    }

    #[test]
    fn scroll_viewport_clamps_cursor() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.set_total(30);
        s.scroll_viewport(15);
        assert!(s.cursor >= s.offset);
        assert!(s.cursor < s.offset + 10);
    }

    #[test]
    fn goto_top_and_bottom() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.set_total(50);
        s.goto_bottom();
        assert_eq!(s.cursor, 49);
        s.goto_top();
        assert_eq!(s.cursor, 0);
        assert_eq!(s.offset, 0);
    }

    #[test]
    fn pending_bottom_resolves_on_set_total() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        // total unknown — scroll_to_bottom sets pending flag
        s.scroll_to_bottom();
        assert!(s.pending_bottom);
        assert_eq!(s.offset, 0);

        // When total becomes known, pending_bottom snaps to end
        s.set_total(50);
        assert!(!s.pending_bottom);
        assert_eq!(s.offset, 40); // 50 - 10
        assert_eq!(s.cursor, 49);
    }

    #[test]
    fn pending_bottom_cleared_by_manual_scroll() {
        let mut s = ScrollState::new();
        s.set_viewport_height(10);
        s.scroll_to_bottom();
        assert!(s.pending_bottom);

        // User scrolls manually — cancel pending bottom
        s.set_total(50);
        assert!(!s.pending_bottom);
        // Now scroll up — should not re-snap
        s.scroll_up(5);
        assert_eq!(s.cursor, 44);
        s.set_total(50);
        assert_eq!(s.cursor, 44); // stays put
    }
}
