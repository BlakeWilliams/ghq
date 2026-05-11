use anyhow::{Result, bail};
use tokio::process::Command;

pub async fn stage_lines(
    dir: &str,
    filename: &str,
    file_status: &str,
    full_patch: &str,
    new_line_nos: &[i32],
    old_line_nos: &[i32],
    unstage: bool,
) -> Result<()> {
    if file_status == "added" && !unstage {
        ensure_tracked(dir, filename).await;
    }
    let patch = build_partial_patch(filename, file_status, full_patch, new_line_nos, old_line_nos);
    if patch.is_empty() {
        return Ok(());
    }
    apply_patch(dir, &patch, unstage).await
}

pub async fn stage_hunk(
    dir: &str,
    filename: &str,
    file_status: &str,
    full_patch: &str,
    line_no: i32,
    side: &str,
    unstage: bool,
) -> Result<()> {
    if file_status == "added" && !unstage {
        ensure_tracked(dir, filename).await;
    }
    let patch = build_hunk_patch(filename, file_status, full_patch, line_no, side);
    if patch.is_empty() {
        return Ok(());
    }
    apply_patch(dir, &patch, unstage).await
}

pub async fn stage_all(dir: &str) -> Result<()> {
    let output = Command::new("git")
        .args(["-C", dir, "add", "-A"])
        .output()
        .await?;
    if !output.status.success() {
        bail!(
            "git add -A: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(())
}

async fn ensure_tracked(dir: &str, filename: &str) {
    let already = Command::new("git")
        .args(["-C", dir, "ls-files", "--error-unmatch", filename])
        .output()
        .await
        .map(|o| o.status.success())
        .unwrap_or(false);
    if already {
        return;
    }
    let _ = Command::new("git")
        .args(["-C", dir, "add", "--intent-to-add", filename])
        .output()
        .await;
}

async fn apply_patch(dir: &str, patch: &str, unstage: bool) -> Result<()> {
    let mut args = vec!["-C", dir, "apply", "--cached", "--allow-empty"];
    if unstage {
        args.push("--reverse");
    }
    args.push("-");

    let mut cmd = tokio::process::Command::new("git");
    cmd.args(&args);
    cmd.stdin(std::process::Stdio::piped());
    cmd.stdout(std::process::Stdio::piped());
    cmd.stderr(std::process::Stdio::piped());

    let mut child = cmd.spawn()?;

    if let Some(mut stdin) = child.stdin.take() {
        use tokio::io::AsyncWriteExt;
        stdin.write_all(patch.as_bytes()).await?;
        drop(stdin);
    }

    let output = child.wait_with_output().await?;
    if !output.status.success() {
        bail!(
            "git apply: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(())
}

fn build_partial_patch(
    filename: &str,
    file_status: &str,
    full_patch: &str,
    new_line_nos: &[i32],
    old_line_nos: &[i32],
) -> String {
    use std::collections::HashSet;

    let new_set: HashSet<i32> = new_line_nos.iter().copied().collect();
    let old_set: HashSet<i32> = old_line_nos.iter().copied().collect();

    let lines: Vec<&str> = full_patch.split('\n').collect();
    let mut hunks: Vec<String> = Vec::new();
    let mut current_hunk: Vec<String> = Vec::new();
    let mut old_start = 0i32;
    let mut new_start = 0i32;
    let mut old_count = 0i32;
    let mut new_count = 0i32;
    let mut old_num = 0i32;
    let mut new_num = 0i32;
    let mut in_hunk = false;

    let mut flush_hunk = |hunks: &mut Vec<String>, current_hunk: &mut Vec<String>,
                          old_start: i32, old_count: i32, new_start: i32, new_count: i32| {
        if current_hunk.is_empty() {
            return;
        }
        let has_change = current_hunk
            .iter()
            .any(|l| l.starts_with('+') || l.starts_with('-'));
        if !has_change {
            current_hunk.clear();
            return;
        }
        hunks.push(format!(
            "@@ -{old_start},{old_count} +{new_start},{new_count} @@"
        ));
        hunks.append(current_hunk);
    };

    for line in &lines {
        if line.is_empty() {
            continue;
        }

        if line.starts_with("@@") {
            flush_hunk(
                &mut hunks,
                &mut current_hunk,
                old_start,
                old_count,
                new_start,
                new_count,
            );
            let (o, n) = parse_hunk_nums(line);
            old_num = o;
            new_num = n;
            old_start = o;
            new_start = n;
            old_count = 0;
            new_count = 0;
            in_hunk = true;
            continue;
        }

        if !in_hunk {
            continue;
        }

        if let Some(rest) = line.strip_prefix('+') {
            if new_set.contains(&new_num) {
                current_hunk.push(line.to_string());
                new_count += 1;
            }
            // else: skip this addition entirely
            let _ = rest;
            new_num += 1;
        } else if let Some(rest) = line.strip_prefix('-') {
            if old_set.contains(&old_num) {
                current_hunk.push(line.to_string());
                old_count += 1;
            } else {
                current_hunk.push(format!(" {rest}"));
                old_count += 1;
                new_count += 1;
            }
            old_num += 1;
        } else {
            let content = if line.starts_with(' ') {
                line.to_string()
            } else {
                format!(" {line}")
            };
            current_hunk.push(content);
            old_count += 1;
            new_count += 1;
            old_num += 1;
            new_num += 1;
        }
    }

    flush_hunk(
        &mut hunks,
        &mut current_hunk,
        old_start,
        old_count,
        new_start,
        new_count,
    );

    if hunks.is_empty() {
        return String::new();
    }

    let mut b = String::new();
    b.push_str(&format!("diff --git a/{filename} b/{filename}\n"));
    if file_status == "added" {
        b.push_str("new file mode 100644\n");
        b.push_str("--- /dev/null\n");
    } else {
        b.push_str(&format!("--- a/{filename}\n"));
    }
    b.push_str(&format!("+++ b/{filename}\n"));
    for h in &hunks {
        b.push_str(h);
        b.push('\n');
    }
    b
}

fn build_hunk_patch(
    filename: &str,
    file_status: &str,
    full_patch: &str,
    line_no: i32,
    side: &str,
) -> String {
    let lines: Vec<&str> = full_patch.split('\n').collect();
    let mut hunk_header = String::new();
    let mut hunk_lines: Vec<String> = Vec::new();
    let mut found = false;
    let mut old_num = 0i32;
    let mut new_num = 0i32;

    for line in &lines {
        if line.is_empty() {
            continue;
        }
        if line.starts_with("@@") {
            if found {
                break;
            }
            hunk_header = line.to_string();
            hunk_lines.clear();
            let (o, n) = parse_hunk_nums(line);
            old_num = o;
            new_num = n;
            continue;
        }
        if hunk_header.is_empty() {
            continue;
        }

        hunk_lines.push(line.to_string());

        if line.starts_with('+') {
            if side == "RIGHT" && new_num == line_no {
                found = true;
            }
            new_num += 1;
        } else if line.starts_with('-') {
            if side == "LEFT" && old_num == line_no {
                found = true;
            }
            old_num += 1;
        } else {
            if side == "RIGHT" && new_num == line_no {
                found = true;
            }
            if side == "LEFT" && old_num == line_no {
                found = true;
            }
            old_num += 1;
            new_num += 1;
        }
    }

    if !found || hunk_header.is_empty() {
        return String::new();
    }

    let mut b = String::new();
    b.push_str(&format!("diff --git a/{filename} b/{filename}\n"));
    if file_status == "added" {
        b.push_str("new file mode 100644\n");
        b.push_str("--- /dev/null\n");
    } else {
        b.push_str(&format!("--- a/{filename}\n"));
    }
    b.push_str(&format!("+++ b/{filename}\n"));
    b.push_str(&hunk_header);
    b.push('\n');
    for hl in &hunk_lines {
        b.push_str(hl);
        b.push('\n');
    }
    b
}

fn parse_hunk_nums(header: &str) -> (i32, i32) {
    let parts: Vec<&str> = header.split_whitespace().collect();
    if parts.len() < 3 {
        return (0, 0);
    }
    let old = parts[1].trim_start_matches('-');
    let old = old.split(',').next().unwrap_or("0");
    let new = parts[2].trim_start_matches('+');
    let new = new.split(',').next().unwrap_or("0");
    (
        old.parse().unwrap_or(0),
        new.parse().unwrap_or(0),
    )
}
