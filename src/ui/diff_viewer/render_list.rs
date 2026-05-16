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
pub enum RenderItem {
    DiffLine(DiffLineData),
}

impl RenderItem {
    pub fn line_count(&self) -> usize {
        match self {
            RenderItem::DiffLine(_) => 1,
        }
    }

    pub fn is_diff_line(&self) -> bool {
        matches!(self, RenderItem::DiffLine(_))
    }

    pub fn as_diff_line(&self) -> Option<&DiffLineData> {
        match self {
            RenderItem::DiffLine(d) => Some(d),
        }
    }
}

pub struct RenderList {
    items: Vec<RenderItem>,
    diff_line_map: HashMap<usize, usize>,
    cached_total_lines: usize,
    cached_gutter_width: usize,
}

impl RenderList {
    pub fn new() -> Self {
        Self {
            items: Vec::new(),
            diff_line_map: HashMap::new(),
            cached_total_lines: 0,
            cached_gutter_width: 3,
        }
    }

    pub fn from_diff_lines(lines: Vec<DiffLineData>) -> Self {
        let mut list = Self::new();
        for line in lines {
            list.items.push(RenderItem::DiffLine(line));
        }
        list.rebuild_caches();
        list
    }

    pub fn items(&self) -> &[RenderItem] {
        &self.items
    }

    pub fn items_mut(&mut self) -> &mut [RenderItem] {
        &mut self.items
    }

    pub fn len(&self) -> usize {
        self.items.len()
    }

    pub fn is_empty(&self) -> bool {
        self.items.is_empty()
    }

    pub fn total_lines(&self) -> usize {
        self.cached_total_lines
    }

    pub fn get(&self, index: usize) -> Option<&RenderItem> {
        self.items.get(index)
    }

    pub fn get_diff_line(&self, index: usize) -> Option<&DiffLineData> {
        self.items.get(index).and_then(|i| i.as_diff_line())
    }

    /// Find the badge (if any) on the diff line at the given render index.
    pub fn badge_at(&self, index: usize) -> Option<&CommentBadge> {
        self.items.get(index).and_then(|item| {
            let RenderItem::DiffLine(dl) = item;
            dl.badge.as_ref()
        })
    }

    /// Find the render index of the next badge after `from`, wrapping around.
    pub fn next_badge_idx(&self, from: usize) -> Option<usize> {
        let len = self.items.len();
        if len == 0 {
            return None;
        }
        let mut first: Option<usize> = None;
        for (i, item) in self.items.iter().enumerate() {
            let RenderItem::DiffLine(dl) = item; {
                if dl.badge.is_some() {
                    if first.is_none() {
                        first = Some(i);
                    }
                    if i > from {
                        return Some(i);
                    }
                }
            }
        }
        first // wrap around
    }

    /// Find the render index of the previous badge before `from`, wrapping around.
    pub fn prev_badge_idx(&self, from: usize) -> Option<usize> {
        let mut last: Option<usize> = None;
        let mut candidate: Option<usize> = None;
        for (i, item) in self.items.iter().enumerate() {
            let RenderItem::DiffLine(dl) = item; {
                if dl.badge.is_some() {
                    last = Some(i);
                    if i < from {
                        candidate = Some(i);
                    }
                }
            }
        }
        candidate.or(last) // wrap around
    }

    pub fn gutter_width(&self) -> usize {
        self.cached_gutter_width
    }

    fn rebuild_caches(&mut self) {
        // Rebuild diff_line_map
        self.diff_line_map.clear();
        let mut max_line: usize = 0;
        let mut total_lines: usize = 0;
        for (i, item) in self.items.iter().enumerate() {
            total_lines += item.line_count();
            let RenderItem::DiffLine(dl) = item;
            self.diff_line_map.insert(i, i);
            let a = dl.old_line_no.unwrap_or(0) as usize;
            let b = dl.new_line_no.unwrap_or(0) as usize;
            max_line = max_line.max(a).max(b);
        }
        self.cached_total_lines = total_lines;
        let digits = if max_line == 0 {
            1
        } else {
            (max_line as f64).log10() as usize + 1
        };
        self.cached_gutter_width = digits.max(3);
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
            let RenderItem::DiffLine(dl) = item; {
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
            let RenderItem::DiffLine(dl) = item; {
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

        self.rebuild_caches();
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

        assert!(list.get(0).unwrap().is_diff_line());
        assert_eq!(
            list.get_diff_line(0).unwrap().line_type,
            LineType::HunkHeader
        );
        assert_eq!(list.get_diff_line(2).unwrap().line_type, LineType::Add);
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
    fn empty_list() {
        let list = RenderList::new();
        assert_eq!(list.len(), 0);
        assert_eq!(list.total_lines(), 0);
        assert!(list.is_empty());
        assert_eq!(list.gutter_width(), 3);
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
