mod config;
mod error;

mod agent;
mod cache;
mod git;
mod github;
mod review;
mod terminal;
mod ui;

use anyhow::Context;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let log_dir = dirs::cache_dir()
        .unwrap_or_else(|| std::path::PathBuf::from("/tmp"))
        .join("gg");
    std::fs::create_dir_all(&log_dir).ok();
    let log_file = std::fs::File::create(log_dir.join("gg.log")).ok();

    if let Some(file) = log_file {
        tracing_subscriber::fmt()
            .with_env_filter(
                tracing_subscriber::EnvFilter::from_default_env()
                    .add_directive(tracing::Level::INFO.into()),
            )
            .with_writer(file)
            .with_ansi(false)
            .init();
    }

    let repo_root =
        git::repo_root(".").await.context("not a git repository")?;

    let config = config::Config::load();

    let (owner, repo) = git::repo_owner_and_name(&repo_root)
        .await
        .context("could not determine repository owner/name")?;

    let github_client =
        github::Client::new().context("failed to create GitHub client")?;
    let cached_client = github::CachedClient::new(github_client);

    let current_branch = git::current_branch(&repo_root).await?;

    let mut app = ui::App::new(
        repo_root,
        owner,
        repo,
        current_branch,
        config,
        cached_client,
    );

    app.run().await
}
