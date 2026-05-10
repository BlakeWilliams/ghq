use std::collections::HashMap;

use ratatui::style::Style;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LineType {
    Context,
    Add,
    Delete,
    HunkHeader,
}

#[derive(Debug, Clone)]
pub struct CommentBadge {
    pub thread_key: String,
    pub side: String,
    pub line: i32,
    pub count: usize,
    pub has_pending: bool,
    pub resolved: bool,
}

#[derive(Debug, Clone)]
pub struct DiffLineData {
    pub line_type: LineType,
    pub content: String,
    pub old_line_no: Option<i32>,
    pub new_line_no: Option<i32>,
    pub highlighted: Vec<(Style, String)>,
    pub badge: Option<CommentBadge>,
}

#[derive(Debug, Clone)]
pub struct CommentThreadData {
    pub thread_key: String,
    pub diff_idx: usize,
    pub side: String,
    pub line: i32,
    pub comment_count: usize,
    pub has_pending: bool,
    pub resolved: bool,
}

#[derive(Debug, Clone)]
pub enum RenderItem {
    DiffLine(DiffLineData),
    CommentThread(CommentThreadData),
}

impl RenderItem {
    pub fn line_count(&self) -> usize {
        match self {
            RenderItem::DiffLine(_) => 1,
            RenderItem::CommentThread(_) => 1,
        }
    }

    pub fn diff_idx(&self) -> Option<usize> {
        match self {
            RenderItem::DiffLine(_) => None,
            RenderItem::CommentThread(t) => Some(t.diff_idx),
        }
    }

    pub fn is_diff_line(&self) -> bool {
        matches!(self, RenderItem::DiffLine(_))
    }

    pub fn thread_key(&self) -> Option<&str> {
        match self {
            RenderItem::DiffLine(_) => None,
            RenderItem::CommentThread(t) => Some(&t.thread_key),
        }
    }

    pub fn as_diff_line(&self) -> Option<&DiffLineData> {
        match self {
            RenderItem::DiffLine(d) => Some(d),
            _ => None,
        }
    }

    pub fn as_thread(&self) -> Option<&CommentThreadData> {
        match self {
            RenderItem::CommentThread(t) => Some(t),
            _ => None,
        }
    }
}

pub struct RenderList {
    items: Vec<RenderItem>,
    diff_line_map: HashMap<usize, usize>,
}

impl RenderList {
    pub fn new() -> Self {
        Self {
            items: Vec::new(),
            diff_line_map: HashMap::new(),
        }
    }

    pub fn from_diff_lines(lines: Vec<DiffLineData>) -> Self {
        let mut list = Self::new();
        for line in lines {
            list.items.push(RenderItem::DiffLine(line));
        }
        list.rebuild_diff_map();
        list
    }

    pub fn items(&self) -> &[RenderItem] {
        &self.items
    }

    pub fn len(&self) -> usize {
        self.items.len()
    }

    pub fn is_empty(&self) -> bool {
        self.items.is_empty()
    }

    pub fn total_lines(&self) -> usize {
        self.items.iter().map(|i| i.line_count()).sum()
    }

    pub fn diff_line_count(&self) -> usize {
        self.items.iter().filter(|i| i.is_diff_line()).count()
    }

    pub fn get(&self, index: usize) -> Option<&RenderItem> {
        self.items.get(index)
    }

    pub fn get_diff_line(&self, index: usize) -> Option<&DiffLineData> {
        self.items.get(index).and_then(|i| i.as_diff_line())
    }

    pub fn diff_line_offset(&self, diff_idx: usize) -> Option<usize> {
        self.diff_line_map.get(&diff_idx).copied()
    }

    pub fn item_at_offset(&self, offset: usize) -> Option<&RenderItem> {
        let mut pos = 0;
        for item in &self.items {
            let lc = item.line_count();
            if offset < pos + lc {
                return Some(item);
            }
            pos += lc;
        }
        None
    }

    pub fn insert_after_diff_idx(&mut self, diff_idx: usize, item: RenderItem) {
        if let Some(&item_pos) = self.diff_line_map.get(&diff_idx) {
            self.items.insert(item_pos + 1, item);
        } else {
            self.items.push(item);
        }
        self.rebuild_diff_map();
    }

    pub fn remove_thread(&mut self, key: &str) {
        self.items
            .retain(|i| i.thread_key() != Some(key));
        self.rebuild_diff_map();
    }

    pub fn replace_thread(&mut self, key: &str, new_item: RenderItem) {
        if let Some(pos) = self
            .items
            .iter()
            .position(|i| i.thread_key() == Some(key))
        {
            self.items[pos] = new_item;
            self.rebuild_diff_map();
        }
    }

    pub fn find_thread(&self, key: &str) -> Option<&CommentThreadData> {
        self.items.iter().find_map(|i| match i {
            RenderItem::CommentThread(t) if t.thread_key == key => Some(t),
            _ => None,
        })
    }

    pub fn clear(&mut self) {
        self.items.clear();
        self.diff_line_map.clear();
    }

    /// Find the badge (if any) on the diff line at the given render index.
    pub fn badge_at(&self, index: usize) -> Option<&CommentBadge> {
        self.items.get(index).and_then(|item| {
            if let RenderItem::DiffLine(dl) = item {
                dl.badge.as_ref()
            } else {
                None
            }
        })
    }

    fn rebuild_diff_map(&mut self) {
        self.diff_line_map.clear();
        let mut diff_idx = 0;
        for (i, item) in self.items.iter().enumerate() {
            if item.is_diff_line() {
                self.diff_line_map.insert(diff_idx, i);
                diff_idx += 1;
            }
        }
    }

    pub fn gutter_width(&self) -> usize {
        let max_line = self
            .items
            .iter()
            .filter_map(|i| i.as_diff_line())
            .map(|dl| {
                let a = dl.old_line_no.unwrap_or(0) as usize;
                let b = dl.new_line_no.unwrap_or(0) as usize;
                a.max(b)
            })
            .max()
            .unwrap_or(0);
        let digits = if max_line == 0 {
            1
        } else {
            (max_line as f64).log10() as usize + 1
        };
        digits.max(3)
    }

    /// Attach comment badges to diff lines based on comment positions.
    /// Each badge is overlaid on the right edge of the matching diff line.
    pub fn place_comments(
        &mut self,
        roots: &[CommentPosition],
        pending: &[CommentPosition],
    ) {
        // Clear existing badges
        for item in &mut self.items {
            if let RenderItem::DiffLine(dl) = item {
                dl.badge = None;
            }
        }
        // Also remove any stale CommentThread items from prior versions
        self.items.retain(|i| i.is_diff_line());

        // Collect all positions, deduplicating by (side, line)
        let mut badge_map: std::collections::HashMap<(String, i32), CommentBadge> =
            std::collections::HashMap::new();

        for root in roots {
            let key = (root.side.clone(), root.line);
            badge_map
                .entry(key)
                .and_modify(|b| {
                    b.count = root.count;
                })
                .or_insert_with(|| CommentBadge {
                    thread_key: root.comment_id.clone(),
                    side: root.side.clone(),
                    line: root.line,
                    count: root.count,
                    has_pending: false,
                    resolved: false,
                });
        }

        for p in pending {
            let key = (p.side.clone(), p.line);
            badge_map
                .entry(key)
                .and_modify(|b| {
                    b.has_pending = true;
                })
                .or_insert_with(|| CommentBadge {
                    thread_key: p.comment_id.clone(),
                    side: p.side.clone(),
                    line: p.line,
                    count: p.count,
                    has_pending: true,
                    resolved: false,
                });
        }

        // Attach badges to matching diff lines
        for item in &mut self.items {
            if let RenderItem::DiffLine(dl) = item {
                match dl.line_type {
                    LineType::Delete => {
                        if let Some(ln) = dl.old_line_no {
                            if let Some(badge) = badge_map.remove(&("LEFT".to_string(), ln)) {
                                dl.badge = Some(badge);
                            }
                        }
                    }
                    LineType::Context => {
                        // Context lines can have RIGHT or LEFT badges
                        if let Some(ln) = dl.new_line_no {
                            if let Some(badge) = badge_map.remove(&("RIGHT".to_string(), ln)) {
                                dl.badge = Some(badge);
                                continue;
                            }
                        }
                        if let Some(ln) = dl.old_line_no {
                            if let Some(badge) = badge_map.remove(&("LEFT".to_string(), ln)) {
                                dl.badge = Some(badge);
                            }
                        }
                    }
                    LineType::Add => {
                        if let Some(ln) = dl.new_line_no {
                            if let Some(badge) = badge_map.remove(&("RIGHT".to_string(), ln)) {
                                dl.badge = Some(badge);
                            }
                        }
                    }
                    LineType::HunkHeader => {}
                }
            }
        }

        self.rebuild_diff_map();
    }
}

/// Describes a comment position for placement in the render list.
pub struct CommentPosition {
    pub comment_id: String,
    pub side: String,
    pub line: i32,
    pub count: usize,
}

impl Default for RenderList {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_line(lt: LineType, content: &str, old: Option<i32>, new: Option<i32>) -> DiffLineData {
        DiffLineData {
            line_type: lt,
            content: content.to_string(),
            old_line_no: old,
            new_line_no: new,
            highlighted: Vec::new(),
            badge: None,
        }
    }

    #[test]
    fn from_diff_lines_builds_map() {
        let lines = vec![
            make_line(LineType::HunkHeader, "@@ -1,3 +1,4 @@", None, None),
            make_line(LineType::Context, "unchanged", Some(1), Some(1)),
            make_line(LineType::Add, "new line", None, Some(2)),
            make_line(LineType::Delete, "old line", Some(2), None),
        ];
        let list = RenderList::from_diff_lines(lines);

        assert_eq!(list.len(), 4);
        assert_eq!(list.total_lines(), 4);
        assert_eq!(list.diff_line_count(), 4);

        assert!(list.get(0).unwrap().is_diff_line());
        assert_eq!(
            list.get_diff_line(0).unwrap().line_type,
            LineType::HunkHeader
        );
        assert_eq!(list.get_diff_line(2).unwrap().line_type, LineType::Add);
    }

    #[test]
    fn diff_line_offset_maps_correctly() {
        let lines = vec![
            make_line(LineType::Context, "a", Some(1), Some(1)),
            make_line(LineType::Add, "b", None, Some(2)),
            make_line(LineType::Context, "c", Some(2), Some(3)),
        ];
        let list = RenderList::from_diff_lines(lines);

        assert_eq!(list.diff_line_offset(0), Some(0));
        assert_eq!(list.diff_line_offset(1), Some(1));
        assert_eq!(list.diff_line_offset(2), Some(2));
        assert_eq!(list.diff_line_offset(3), None);
    }

    #[test]
    fn insert_after_diff_idx() {
        let lines = vec![
            make_line(LineType::Context, "a", Some(1), Some(1)),
            make_line(LineType::Add, "b", None, Some(2)),
            make_line(LineType::Context, "c", Some(2), Some(3)),
        ];
        let mut list = RenderList::from_diff_lines(lines);

        let thread = RenderItem::CommentThread(CommentThreadData {
            thread_key: "thread-1".to_string(),
            diff_idx: 1,
            side: "RIGHT".to_string(),
            line: 2,
            comment_count: 1,
            has_pending: false,
            resolved: false,
        });
        list.insert_after_diff_idx(1, thread);

        assert_eq!(list.len(), 4);
        assert_eq!(list.total_lines(), 4);
        assert!(list.items[1].is_diff_line()); // diff line at idx 1
        assert!(!list.items[2].is_diff_line()); // thread inserted after it
        assert_eq!(list.items[2].thread_key(), Some("thread-1"));
        assert!(list.items[3].is_diff_line()); // original idx 2

        // diff map should be rebuilt
        assert_eq!(list.diff_line_offset(0), Some(0));
        assert_eq!(list.diff_line_offset(1), Some(1));
        assert_eq!(list.diff_line_offset(2), Some(3)); // shifted by thread
    }

    #[test]
    fn remove_thread() {
        let lines = vec![
            make_line(LineType::Context, "a", Some(1), Some(1)),
            make_line(LineType::Add, "b", None, Some(2)),
        ];
        let mut list = RenderList::from_diff_lines(lines);
        list.insert_after_diff_idx(
            0,
            RenderItem::CommentThread(CommentThreadData {
                thread_key: "t1".to_string(),
                diff_idx: 0,
                side: "RIGHT".to_string(),
                line: 1,
                comment_count: 2,
                has_pending: false,
                resolved: false,
            }),
        );
        assert_eq!(list.len(), 3);

        list.remove_thread("t1");
        assert_eq!(list.len(), 2);
        assert!(list.find_thread("t1").is_none());
        assert_eq!(list.diff_line_offset(0), Some(0));
        assert_eq!(list.diff_line_offset(1), Some(1));
    }

    #[test]
    fn replace_thread() {
        let lines = vec![make_line(LineType::Context, "a", Some(1), Some(1))];
        let mut list = RenderList::from_diff_lines(lines);
        list.insert_after_diff_idx(
            0,
            RenderItem::CommentThread(CommentThreadData {
                thread_key: "t1".to_string(),
                diff_idx: 0,
                side: "RIGHT".to_string(),
                line: 1,
                comment_count: 1,
                has_pending: false,
                resolved: false,
            }),
        );

        let replacement = RenderItem::CommentThread(CommentThreadData {
            thread_key: "t1".to_string(),
            diff_idx: 0,
            side: "RIGHT".to_string(),
            line: 1,
            comment_count: 3,
            has_pending: true,
            resolved: false,
        });
        list.replace_thread("t1", replacement);

        let thread = list.find_thread("t1").unwrap();
        assert_eq!(thread.comment_count, 3);
        assert!(thread.has_pending);
    }

    #[test]
    fn gutter_width_calculation() {
        let lines = vec![
            make_line(LineType::Context, "a", Some(1), Some(1)),
            make_line(LineType::Context, "b", Some(99), Some(100)),
        ];
        let list = RenderList::from_diff_lines(lines);
        assert_eq!(list.gutter_width(), 3);

        let lines = vec![make_line(LineType::Context, "a", Some(1000), Some(999))];
        let list = RenderList::from_diff_lines(lines);
        assert_eq!(list.gutter_width(), 4);
    }

    #[test]
    fn item_at_offset() {
        let lines = vec![
            make_line(LineType::Context, "a", Some(1), Some(1)),
            make_line(LineType::Add, "b", None, Some(2)),
        ];
        let list = RenderList::from_diff_lines(lines);
        assert!(list.item_at_offset(0).unwrap().is_diff_line());
        assert!(list.item_at_offset(1).unwrap().is_diff_line());
        assert!(list.item_at_offset(2).is_none());
    }

    #[test]
    fn empty_list() {
        let list = RenderList::new();
        assert_eq!(list.len(), 0);
        assert_eq!(list.total_lines(), 0);
        assert_eq!(list.diff_line_count(), 0);
        assert!(list.is_empty());
        assert_eq!(list.gutter_width(), 3);
        assert!(list.item_at_offset(0).is_none());
    }

    #[test]
    fn place_comments_attaches_badges() {
        let lines = vec![
            make_line(LineType::Context, "a", Some(1), Some(1)),
            make_line(LineType::Add, "b", None, Some(2)),
            make_line(LineType::Delete, "c", Some(3), None),
            make_line(LineType::Context, "d", Some(4), Some(3)),
        ];
        let mut list = RenderList::from_diff_lines(lines);
        assert_eq!(list.len(), 4);

        let roots = vec![
            CommentPosition {
                comment_id: "c1".to_string(),
                side: "RIGHT".to_string(),
                line: 2,
                count: 2,
            },
            CommentPosition {
                comment_id: "c2".to_string(),
                side: "LEFT".to_string(),
                line: 3,
                count: 1,
            },
        ];
        list.place_comments(&roots, &[]);

        // Badges attached inline — no extra items
        assert_eq!(list.len(), 4);

        // Badge on line 2 RIGHT (add line at index 1)
        let b1 = list.badge_at(1).expect("badge on add line");
        assert_eq!(b1.thread_key, "c1");
        assert_eq!(b1.count, 2);

        // Badge on line 3 LEFT (delete line at index 2)
        let b2 = list.badge_at(2).expect("badge on delete line");
        assert_eq!(b2.thread_key, "c2");
        assert_eq!(b2.count, 1);

        // No badge on other lines
        assert!(list.badge_at(0).is_none());
        assert!(list.badge_at(3).is_none());
    }

    #[test]
    fn place_comments_marks_pending() {
        let lines = vec![
            make_line(LineType::Add, "a", None, Some(5)),
        ];
        let mut list = RenderList::from_diff_lines(lines);

        let roots = vec![CommentPosition {
            comment_id: "c1".to_string(),
            side: "RIGHT".to_string(),
            line: 5,
            count: 1,
        }];
        let pending = vec![CommentPosition {
            comment_id: "c1".to_string(),
            side: "RIGHT".to_string(),
            line: 5,
            count: 1,
        }];
        list.place_comments(&roots, &pending);

        assert_eq!(list.len(), 1);
        let badge = list.badge_at(0).expect("badge on add line");
        assert!(badge.has_pending);
    }

    #[test]
    fn place_comments_replaces_existing() {
        let lines = vec![
            make_line(LineType::Add, "a", None, Some(1)),
        ];
        let mut list = RenderList::from_diff_lines(lines);

        let roots = vec![CommentPosition {
            comment_id: "c1".to_string(),
            side: "RIGHT".to_string(),
            line: 1,
            count: 1,
        }];
        list.place_comments(&roots, &[]);
        assert_eq!(list.len(), 1);
        assert_eq!(list.badge_at(0).unwrap().count, 1);

        // Place again with updated count — should replace, not duplicate
        let roots2 = vec![CommentPosition {
            comment_id: "c1".to_string(),
            side: "RIGHT".to_string(),
            line: 1,
            count: 3,
        }];
        list.place_comments(&roots2, &[]);
        assert_eq!(list.len(), 1);
        assert_eq!(list.badge_at(0).unwrap().count, 3);
    }
}
