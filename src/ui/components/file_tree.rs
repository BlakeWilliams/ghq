use std::collections::BTreeMap;

use crate::github::types::PullRequestFile;

#[derive(Debug, Clone)]
pub struct FileTreeEntry {
    pub file_index: i32, // -1 for directories
    pub display: String,
    pub depth: usize,
    pub is_dir: bool,
    /// Aggregated status: "added" if all children added, "removed" if all removed, else "modified"
    pub status: String,
}

pub fn build_file_tree(files: &[PullRequestFile]) -> Vec<FileTreeEntry> {
    struct TreeNode {
        children: BTreeMap<String, TreeNode>,
        order: Vec<String>,
        files: Vec<(String, usize)>,
    }

    impl TreeNode {
        fn new() -> Self {
            Self {
                children: BTreeMap::new(),
                order: Vec::new(),
                files: Vec::new(),
            }
        }
    }

    let mut root = TreeNode::new();

    for (i, f) in files.iter().enumerate() {
        let path = &f.filename;
        let slash_pos = path.rfind('/');
        let (dir_part, file_name) = match slash_pos {
            Some(pos) => (&path[..pos], &path[pos + 1..]),
            None => ("", path.as_str()),
        };

        let mut node = &mut root;
        if !dir_part.is_empty() {
            for part in dir_part.split('/') {
                if !node.children.contains_key(part) {
                    node.order.push(part.to_string());
                    node.children.insert(part.to_string(), TreeNode::new());
                }
                node = node.children.get_mut(part).unwrap();
            }
        }
        node.files.push((file_name.to_string(), i));
    }

    // Collapse single-child directory chains
    fn collapse(node: &mut TreeNode) {
        let keys: Vec<String> = node.order.clone();
        for key in keys {
            if let Some(child) = node.children.get_mut(&key) {
                collapse(child);
            }
        }

        while node.order.len() == 1 && node.files.is_empty() {
            let child_key = node.order[0].clone();
            let child = node.children.remove(&child_key).unwrap();

            if child.order.len() == 1 && child.files.is_empty() {
                let grand_key = child.order[0].clone();
                let new_key = format!("{child_key}/{grand_key}");
                let grandchild = child.children.into_iter().next().unwrap().1;
                node.children.insert(new_key.clone(), grandchild);
                node.order = vec![new_key];
            } else {
                node.children.insert(child_key.clone(), child);
                node.order = vec![child_key];
                break;
            }
        }
    }
    collapse(&mut root);

    // Compute aggregate status for a subtree: "added" if all files added,
    // "removed" if all removed, otherwise "modified".
    fn aggregate_status(node: &TreeNode, files: &[PullRequestFile]) -> String {
        let mut all_added = true;
        let mut all_removed = true;

        for (_, idx) in &node.files {
            let s = files[*idx].status.as_str();
            if s != "added" { all_added = false; }
            if s != "removed" { all_removed = false; }
        }

        for key in &node.order {
            if let Some(child) = node.children.get(key) {
                let child_status = aggregate_status(child, files);
                if child_status != "added" { all_added = false; }
                if child_status != "removed" { all_removed = false; }
            }
        }

        // Empty nodes (shouldn't happen) default to modified
        if !all_added && !all_removed { return "modified".to_string(); }
        if all_added { "added".to_string() } else { "removed".to_string() }
    }

    // Flatten into entries
    let mut entries = Vec::new();
    fn walk(node: &TreeNode, depth: usize, entries: &mut Vec<FileTreeEntry>, files: &[PullRequestFile]) {
        let mut dirs: Vec<&str> = node.order.iter().map(|s| s.as_str()).collect();
        dirs.sort();
        for key in dirs {
            if let Some(child) = node.children.get(key) {
                let status = aggregate_status(child, files);
                entries.push(FileTreeEntry {
                    file_index: -1,
                    display: format!("{key}/"),
                    depth,
                    is_dir: true,
                    status,
                });
                walk(child, depth + 1, entries, files);
            }
        }
        for (name, file_idx) in &node.files {
            entries.push(FileTreeEntry {
                file_index: *file_idx as i32,
                display: name.clone(),
                depth,
                is_dir: false,
                status: files[*file_idx].status.clone(),
            });
        }
    }
    walk(&root, 0, &mut entries, files);

    entries
}
