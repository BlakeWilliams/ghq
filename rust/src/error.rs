use std::io;
use std::result::Result as StdResult;

use thiserror::Error;

#[derive(Error, Debug)]
pub enum Error {
    #[error("git error: {0}")]
    Git(String),

    #[error("git command failed: {cmd}\n{stderr}")]
    GitCommand { cmd: String, stderr: String },

    #[error("github API error: {0}")]
    GitHub(String),

    #[error("config error: {0}")]
    Config(String),

    #[error("cache error: {0}")]
    Cache(String),

    #[error("agent error: {0}")]
    Agent(String),

    #[error("ui error: {0}")]
    Ui(String),

    #[error("parse error: {0}")]
    Parse(String),

    #[error(transparent)]
    Io(#[from] io::Error),

    #[error(transparent)]
    SerdeJson(#[from] serde_json::Error),

    #[error(transparent)]
    SerdeYaml(#[from] serde_yaml::Error),

    #[error(transparent)]
    Other(#[from] anyhow::Error),
}

pub type Result<T> = StdResult<T, Error>;
