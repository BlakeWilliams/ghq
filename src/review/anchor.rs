use crate::github::types::ReviewComment;

pub fn anchor_comment(
    comment: &ReviewComment,
    diff_lines: &[(String, i32, i32, &str)], // (content, old_line, new_line, kind)
) -> Option<(i32, String)> {
    let line = comment.line?;
    let original_line = comment.original_line.unwrap_or(line);

    // Reject outdated comments
    if line != original_line {
        return None;
    }

    let side = comment.side.as_deref().unwrap_or("RIGHT");

    // Extract the commented line content from the diff hunk
    let target_content = comment.diff_hunk.as_ref().and_then(|hunk| {
        find_line_content(hunk, line, side)
    })?;

    // Find matching line in local diff
    let normalized_target = normalize_ws(&target_content);

    let mut best_match: Option<(i32, String, i32)> = None;

    for (content, old_line, new_line, kind) in diff_lines {
        if !side_compatible(side, kind) {
            continue;
        }

        let candidate_line = if side == "LEFT" { *old_line } else { *new_line };
        let normalized = normalize_ws(content);

        if normalized == normalized_target {
            let distance = (candidate_line - line).abs();
            if best_match.is_none() || distance < best_match.as_ref().unwrap().2 {
                best_match = Some((candidate_line, side.to_string(), distance));
            }
        }
    }

    best_match.map(|(l, s, _)| (l, s))
}

fn find_line_content(hunk: &str, target_line: i32, side: &str) -> Option<String> {
    let mut old_num = 0i32;
    let mut new_num = 0i32;

    for line in hunk.lines() {
        if line.starts_with("@@") {
            let (o, n) = parse_hunk_header(line);
            old_num = o;
            new_num = n;
            continue;
        }

        if let Some(content) = line.strip_prefix('+') {
            if side == "RIGHT" && new_num == target_line {
                return Some(content.to_string());
            }
            new_num += 1;
        } else if let Some(content) = line.strip_prefix('-') {
            if side == "LEFT" && old_num == target_line {
                return Some(content.to_string());
            }
            old_num += 1;
        } else {
            let content = line.strip_prefix(' ').unwrap_or(line);
            if side == "RIGHT" && new_num == target_line {
                return Some(content.to_string());
            }
            if side == "LEFT" && old_num == target_line {
                return Some(content.to_string());
            }
            old_num += 1;
            new_num += 1;
        }
    }
    None
}

fn parse_hunk_header(header: &str) -> (i32, i32) {
    let parts: Vec<&str> = header.split_whitespace().collect();
    if parts.len() < 3 {
        return (0, 0);
    }
    let old = parts[1]
        .trim_start_matches('-')
        .split(',')
        .next()
        .unwrap_or("0");
    let new = parts[2]
        .trim_start_matches('+')
        .split(',')
        .next()
        .unwrap_or("0");
    (
        old.parse().unwrap_or(0),
        new.parse().unwrap_or(0),
    )
}

fn normalize_ws(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn side_compatible(side: &str, kind: &str) -> bool {
    match (side, kind) {
        ("LEFT", "-") => true,
        ("RIGHT", "+") | ("RIGHT", " ") => true,
        _ => false,
    }
}
