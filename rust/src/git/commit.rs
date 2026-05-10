use anyhow::{Result, bail};
use tokio::process::Command;

pub async fn commit(dir: &str, message: &str) -> Result<()> {
    let output = Command::new("git")
        .args(["-C", dir, "commit", "-m", message])
        .output()
        .await?;
    if !output.status.success() {
        bail!(
            "git commit: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(())
}

pub async fn push(dir: &str) -> Result<()> {
    let output = Command::new("git")
        .args(["-C", dir, "push", "-u", "origin", "HEAD"])
        .output()
        .await?;
    if !output.status.success() {
        bail!(
            "git push: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(())
}

pub async fn create_pr(dir: &str, title: &str, body: &str) -> Result<()> {
    let output = Command::new("gh")
        .args(["pr", "create", "--title", title, "--body", body])
        .current_dir(dir)
        .output()
        .await?;
    if !output.status.success() {
        bail!(
            "gh pr create: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(())
}

pub async fn has_staged_changes(dir: &str) -> bool {
    Command::new("git")
        .args(["-C", dir, "diff", "--cached", "--quiet"])
        .output()
        .await
        .map(|o| !o.status.success()) // exit 1 = changes exist
        .unwrap_or(false)
}

pub async fn has_unstaged_changes(dir: &str) -> bool {
    Command::new("git")
        .args(["-C", dir, "status", "--porcelain"])
        .output()
        .await
        .map(|o| !String::from_utf8_lossy(&o.stdout).trim().is_empty())
        .unwrap_or(false)
}

pub async fn has_unpushed_commits(dir: &str) -> bool {
    Command::new("git")
        .args(["-C", dir, "log", "--oneline", "@{u}..HEAD"])
        .output()
        .await
        .map(|o| {
            if !o.status.success() {
                true // no upstream = treat as unpushed
            } else {
                !String::from_utf8_lossy(&o.stdout).trim().is_empty()
            }
        })
        .unwrap_or(true)
}

pub async fn has_open_pr(dir: &str) -> bool {
    Command::new("gh")
        .args(["pr", "view", "--json", "state", "-q", ".state"])
        .current_dir(dir)
        .output()
        .await
        .map(|o| String::from_utf8_lossy(&o.stdout).trim() == "OPEN")
        .unwrap_or(false)
}

pub async fn staged_diff(dir: &str) -> Result<String> {
    let output = Command::new("git")
        .args(["-C", dir, "diff", "--cached"])
        .output()
        .await?;
    Ok(String::from_utf8_lossy(&output.stdout).to_string())
}

pub async fn branch_diff(dir: &str) -> Result<String> {
    let default_branch = get_default_branch_via_gh(dir).await;
    let output = Command::new("git")
        .args(["-C", dir, "diff", &format!("{default_branch}...HEAD")])
        .output()
        .await?;
    if !output.status.success() {
        bail!(
            "git diff branch: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(String::from_utf8_lossy(&output.stdout).to_string())
}

pub async fn branch_log(dir: &str) -> Result<String> {
    let default_branch = get_default_branch_via_gh(dir).await;
    let output = Command::new("git")
        .args(["-C", dir, "log", "--oneline", &format!("{default_branch}..HEAD")])
        .output()
        .await?;
    if !output.status.success() {
        bail!(
            "git log branch: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(String::from_utf8_lossy(&output.stdout).to_string())
}

async fn get_default_branch_via_gh(dir: &str) -> String {
    Command::new("gh")
        .args([
            "repo",
            "view",
            "--json",
            "defaultBranchRef",
            "-q",
            ".defaultBranchRef.name",
        ])
        .current_dir(dir)
        .output()
        .await
        .map(|o| {
            let s = String::from_utf8_lossy(&o.stdout).trim().to_string();
            if s.is_empty() { "main".to_string() } else { s }
        })
        .unwrap_or_else(|_| "main".to_string())
}
