pub fn render_markdown(body: &str, width: u16) -> Vec<String> {
    // Simple markdown rendering. For Phase 1, we do basic text processing.
    // Full pulldown-cmark + syntect rendering will be added in a follow-up.
    let mut lines = Vec::new();
    let max_width = width as usize;

    for line in body.lines() {
        if line.starts_with("```") {
            // Code fence — just pass through for now
            lines.push(line.to_string());
            continue;
        }

        // Basic heading rendering
        if let Some(heading) = line.strip_prefix("### ") {
            lines.push(format!("  {heading}"));
            continue;
        }
        if let Some(heading) = line.strip_prefix("## ") {
            lines.push(format!(" {heading}"));
            continue;
        }
        if let Some(heading) = line.strip_prefix("# ") {
            lines.push(heading.to_string());
            continue;
        }

        // Word wrap
        if line.len() <= max_width || max_width == 0 {
            lines.push(line.to_string());
        } else {
            let mut remaining = line;
            while remaining.len() > max_width {
                let split_at = remaining[..max_width]
                    .rfind(' ')
                    .unwrap_or(max_width);
                lines.push(remaining[..split_at].to_string());
                remaining = remaining[split_at..].trim_start();
            }
            if !remaining.is_empty() {
                lines.push(remaining.to_string());
            }
        }
    }

    lines
}
