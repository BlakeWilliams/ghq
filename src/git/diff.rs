use std::fmt;
use std::path::Path;

use anyhow::{Result, bail};
use regex::Regex;
use tokio::process::Command;

use crate::github::types::PullRequestFile;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DiffMode {
    Working,
    Staged,
    Branch,
}

impl fmt::Display for DiffMode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            DiffMode::Working => write!(f, "Unstaged"),
            DiffMode::Staged => write!(f, "Staged"),
            DiffMode::Branch => write!(f, "Branch"),
        }
    }
}

pub async fn diff(dir: &str, mode: DiffMode, base_branch: &str) -> Result<String> {
    let has_commits = super::has_commits(dir).await;

    let mut args: Vec<String> = vec!["-C".into(), dir.into()];

    match mode {
        DiffMode::Working => {
            if has_commits {
                args.extend(["diff".into(), "--no-color".into()]);
            } else {
                args.extend(["diff".into(), "--cached".into(), "--no-color".into()]);
            }
        }
        DiffMode::Staged => {
            args.extend(["diff".into(), "--cached".into(), "--no-color".into()]);
        }
        DiffMode::Branch => {
            if !has_commits {
                return Ok(String::new());
            }
            let mb = super::resolve_merge_base(dir, base_branch).await?;
            args.extend([
                "diff".into(),
                format!("{mb}..HEAD"),
                "--no-color".into(),
            ]);
        }
    }

    let output = Command::new("git").args(&args).output().await?;

    let mut result = if output.status.success() {
        String::from_utf8_lossy(&output.stdout).to_string()
    } else {
        // git diff returns exit 1 when there are diffs in some cases
        if output.status.code() == Some(1) {
            String::from_utf8_lossy(&output.stdout).to_string()
        } else {
            bail!(
                "git diff: {}",
                String::from_utf8_lossy(&output.stderr)
            );
        }
    };

    // Append untracked files for working tree mode.
    if mode == DiffMode::Working {
        if let Ok(untracked) = super::untracked_files(dir).await {
            if !untracked.is_empty() {
                result.push_str(&untracked_diff(dir, &untracked).await);
            }
        }
    }

    Ok(result)
}

async fn untracked_diff(dir: &str, files: &[String]) -> String {
    let mut sb = String::new();
    for f in files {
        let full_path = Path::new(dir).join(f);
        let content = match tokio::fs::read_to_string(&full_path).await {
            Ok(c) => c,
            Err(_) => continue,
        };
        let mut lines: Vec<&str> = content.split('\n').collect();
        if lines.last() == Some(&"") {
            lines.pop();
        }
        sb.push_str(&format!("diff --git a/{f} b/{f}\n"));
        sb.push_str("new file mode 100644\n");
        sb.push_str(&format!("--- /dev/null\n+++ b/{f}\n"));
        sb.push_str(&format!("@@ -0,0 +1,{} @@\n", lines.len()));
        for l in &lines {
            sb.push('+');
            sb.push_str(l);
            sb.push('\n');
        }
    }
    sb
}

pub fn parse_diff_to_files(raw_diff: &str) -> Vec<PullRequestFile> {
    if raw_diff.is_empty() {
        return vec![];
    }

    let diff_header_re = Regex::new(r"^diff --git a/(.*) b/(.*)").unwrap();

    let lines: Vec<&str> = raw_diff.split('\n').collect();
    let mut files: Vec<PullRequestFile> = Vec::new();
    let mut current_file: Option<PullRequestFile> = None;
    let mut patch_lines: Vec<String> = Vec::new();
    let mut in_header = false;

    let flush_file = |file: &mut Option<PullRequestFile>,
                      patches: &mut Vec<String>,
                      files: &mut Vec<PullRequestFile>| {
        if let Some(mut f) = file.take() {
            for l in patches.iter() {
                if l.starts_with('+') && !l.starts_with("+++") {
                    f.additions += 1;
                } else if l.starts_with('-') && !l.starts_with("---") {
                    f.deletions += 1;
                }
            }
            f.patch = patches.join("\n");
            files.push(f);
            patches.clear();
        }
    };

    for line in &lines {
        if let Some(caps) = diff_header_re.captures(line) {
            flush_file(&mut current_file, &mut patch_lines, &mut files);
            let a_path = caps.get(1).unwrap().as_str().to_string();
            let b_path = caps.get(2).unwrap().as_str().to_string();
            let (status, prev) = if a_path != b_path {
                ("renamed".to_string(), a_path)
            } else {
                ("modified".to_string(), String::new())
            };
            current_file = Some(PullRequestFile {
                filename: b_path,
                status,
                previous_filename: prev,
                patch: String::new(),
                additions: 0,
                deletions: 0,
            });
            in_header = true;
            continue;
        }

        if current_file.is_none() {
            continue;
        }

        if in_header {
            if line.starts_with("@@") {
                in_header = false;
                patch_lines.push(line.to_string());
            } else if line.starts_with("new file") {
                if let Some(ref mut f) = current_file {
                    f.status = "added".to_string();
                }
            } else if line.starts_with("deleted file") {
                if let Some(ref mut f) = current_file {
                    f.status = "removed".to_string();
                }
            } else if line.starts_with("Binary files") {
                if let Some(ref mut f) = current_file {
                    f.patch = String::new();
                }
            }
            continue;
        }

        if line.starts_with("@@")
            || line.starts_with('+')
            || line.starts_with('-')
            || line.starts_with(' ')
            || *line == "\\ No newline at end of file"
        {
            patch_lines.push(line.to_string());
        }
    }

    flush_file(&mut current_file, &mut patch_lines, &mut files);
    files
}

pub async fn diff_stat(dir: &str, mode: DiffMode, base_branch: &str) -> Result<String> {
    let mut args: Vec<String> = vec!["-C".into(), dir.into()];

    match mode {
        DiffMode::Working => {
            args.extend(["diff".into(), "--stat".into(), "--no-color".into()]);
        }
        DiffMode::Staged => {
            args.extend([
                "diff".into(),
                "--cached".into(),
                "--stat".into(),
                "--no-color".into(),
            ]);
        }
        DiffMode::Branch => {
            let mb = super::resolve_merge_base(dir, base_branch).await?;
            args.extend([
                "diff".into(),
                format!("{mb}..HEAD"),
                "--stat".into(),
                "--no-color".into(),
            ]);
        }
    }

    let output = Command::new("git").args(&args).output().await?;
    if output.status.success() || output.status.code() == Some(1) {
        Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
    } else {
        bail!(
            "git diff --stat: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
}

pub fn files_added_deleted_stats(files: &[PullRequestFile]) -> (i32, i32) {
    files
        .iter()
        .fold((0, 0), |(a, d), f| (a + f.additions, d + f.deletions))
}
