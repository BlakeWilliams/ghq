use std::path::PathBuf;

use anyhow::Result;
use serde::Serialize;

pub fn dir() -> Result<PathBuf> {
    let base = dirs::cache_dir()
        .or_else(dirs::home_dir)
        .unwrap_or_else(|| PathBuf::from("."));
    let dir = base.join("ghq");
    std::fs::create_dir_all(&dir)?;
    Ok(dir)
}

pub fn save<T: Serialize>(filename: &str, data: &T) -> Result<()> {
    let path = dir()?.join(filename);
    let json = serde_json::to_string_pretty(data)?;
    std::fs::write(path, json)?;
    Ok(())
}
