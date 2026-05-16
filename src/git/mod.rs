pub mod commit;
pub mod diff;
pub mod stage;
pub mod watcher;

use std::path::Path;

use anyhow::{Context, Result, bail};
use tokio::process::Command;

pub async fn repo_root(dir: &str) -> Result<String> {
    let output = Command::new("git")
        .args(["-C", dir, "rev-parse", "--show-toplevel"])
        .output()
        .await
        .context("failed to run git rev-parse")?;
    if !output.status.success() {
        bail!("not inside a git repository");
    }
    Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
}

pub async fn current_branch(dir: &str) -> Result<String> {
    let output = Command::new("git")
        .args(["-C", dir, "rev-parse", "--abbrev-ref", "HEAD"])
        .output()
        .await?;
    if output.status.success() {
        let branch = String::from_utf8_lossy(&output.stdout).trim().to_string();
        if branch != "HEAD" {
            return Ok(branch);
        }
    }
    // Fallback: read .git/HEAD for orphan branches.
    let head_file = Path::new(dir).join(".git/HEAD");
    if let Ok(data) = tokio::fs::read_to_string(&head_file).await {
        let r = data.trim();
        if let Some(branch) = r.strip_prefix("ref: refs/heads/") {
            return Ok(branch.to_string());
        }
    }
    Ok("main".to_string())
}

pub async fn has_commits(dir: &str) -> bool {
    Command::new("git")
        .args(["-C", dir, "rev-parse", "HEAD"])
        .output()
        .await
        .map(|o| o.status.success())
        .unwrap_or(false)
}

pub async fn default_branch(dir: &str) -> Result<String> {
    // Try symbolic ref of origin/HEAD first.
    let output = Command::new("git")
        .args(["-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD"])
        .output()
        .await?;
    if output.status.success() {
        let r = String::from_utf8_lossy(&output.stdout).trim().to_string();
        if let Some(name) = r.rsplit('/').next() {
            return Ok(format!("origin/{name}"));
        }
    }
    // Fallback: check if origin/main or origin/master exists, then local.
    for branch in ["main", "master"] {
        let ok = Command::new("git")
            .args([
                "-C",
                dir,
                "rev-parse",
                "--verify",
                &format!("refs/remotes/origin/{branch}"),
            ])
            .output()
            .await
            .map(|o| o.status.success())
            .unwrap_or(false);
        if ok {
            return Ok(format!("origin/{branch}"));
        }
    }
    for branch in ["main", "master"] {
        let ok = Command::new("git")
            .args([
                "-C",
                dir,
                "rev-parse",
                "--verify",
                &format!("refs/heads/{branch}"),
            ])
            .output()
            .await
            .map(|o| o.status.success())
            .unwrap_or(false);
        if ok {
            return Ok(branch.to_string());
        }
    }
    Ok("main".to_string())
}

pub async fn default_branch_short(dir: &str) -> Result<String> {
    let branch = default_branch(dir).await?;
    Ok(branch
        .strip_prefix("origin/")
        .unwrap_or(&branch)
        .to_string())
}

pub async fn merge_base(dir: &str, branch: &str) -> Result<String> {
    let output = Command::new("git")
        .args(["-C", dir, "merge-base", branch, "HEAD"])
        .output()
        .await
        .context("git merge-base failed")?;
    if !output.status.success() {
        bail!(
            "git merge-base: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
}

pub async fn resolve_merge_base(dir: &str, base_branch: &str) -> Result<String> {
    let branch = if base_branch.is_empty() {
        default_branch(dir).await?
    } else {
        base_branch.to_string()
    };
    merge_base(dir, &branch).await
}

pub async fn local_branches(dir: &str) -> Result<Vec<String>> {
    let output = Command::new("git")
        .args([
            "-C",
            dir,
            "for-each-ref",
            "--format=%(refname:short)",
            "refs/heads/",
        ])
        .output()
        .await?;
    let raw = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if raw.is_empty() {
        return Ok(vec![]);
    }
    Ok(raw.lines().map(|s| s.to_string()).collect())
}

pub async fn file_content(dir: &str, path: &str) -> Result<String> {
    let full = Path::new(dir).join(path);
    Ok(tokio::fs::read_to_string(full).await?)
}

pub async fn file_content_at_ref(dir: &str, path: &str, git_ref: &str) -> Result<String> {
    let output = Command::new("git")
        .args(["-C", dir, "show", &format!("{git_ref}:{path}")])
        .output()
        .await?;
    if !output.status.success() {
        bail!("git show {git_ref}:{path} failed");
    }
    Ok(String::from_utf8_lossy(&output.stdout).to_string())
}

pub async fn repo_owner_and_name(dir: &str) -> Result<(String, String)> {
    let output = Command::new("git")
        .args(["-C", dir, "remote", "get-url", "origin"])
        .output()
        .await?;
    let url = String::from_utf8_lossy(&output.stdout).trim().to_string();
    parse_remote_url(&url)
}

fn parse_remote_url(url: &str) -> Result<(String, String)> {
    // Handle SSH: git@github.com:owner/repo.git
    if let Some(rest) = url.strip_prefix("git@github.com:") {
        let rest = rest.strip_suffix(".git").unwrap_or(rest);
        let mut parts = rest.splitn(2, '/');
        let owner = parts.next().context("missing owner")?;
        let repo = parts.next().context("missing repo")?;
        return Ok((owner.to_string(), repo.to_string()));
    }
    // Handle HTTPS: https://github.com/owner/repo.git
    if url.contains("github.com") {
        let url = url.strip_suffix(".git").unwrap_or(url);
        let parts: Vec<&str> = url.rsplitn(3, '/').collect();
        if parts.len() >= 2 {
            return Ok((parts[1].to_string(), parts[0].to_string()));
        }
    }
    bail!("could not parse remote URL: {url}");
}

pub async fn untracked_files(dir: &str) -> Result<Vec<String>> {
    let output = Command::new("git")
        .args(["-C", dir, "ls-files", "--others", "--exclude-standard"])
        .output()
        .await?;
    let raw = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if raw.is_empty() {
        return Ok(vec![]);
    }
    Ok(raw.lines().map(|s| s.to_string()).collect())
}
