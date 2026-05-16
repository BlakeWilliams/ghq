pub struct Palette {
    pub colors: [Option<(u8, u8, u8)>; 16],
    ready_count: usize,
}

impl Palette {
    pub fn new() -> Self {
        Self {
            colors: [None; 16],
            ready_count: 0,
        }
    }

    pub fn set(&mut self, index: usize, r: u8, g: u8, b: u8) {
        if index < 16 {
            self.colors[index] = Some((r, g, b));
            self.ready_count = self.colors.iter().filter(|c| c.is_some()).count();
        }
    }

    pub fn complete(&self) -> bool {
        self.ready_count == 16
    }

    pub fn parse_osc4_response(&mut self, data: &str) {
        // Parse OSC 4 responses: "4;N;rgb:RRRR/GGGG/BBBB" or "4;N;rgb:RR/GG/BB"
        let parts: Vec<&str> = data.splitn(3, ';').collect();
        if parts.len() < 3 {
            return;
        }
        if parts[0] != "4" {
            return;
        }
        let index: usize = match parts[1].parse() {
            Ok(i) => i,
            Err(_) => return,
        };
        if index >= 16 {
            return;
        }

        let color_spec = parts[2];

        // Handle rgb:RRRR/GGGG/BBBB or rgb:RR/GG/BB format
        if let Some(rgb) = color_spec.strip_prefix("rgb:") {
            let components: Vec<&str> = rgb.split('/').collect();
            if components.len() != 3 {
                return;
            }
            let parse_component = |s: &str| -> Option<u8> {
                match s.len() {
                    // 1-char: "R" → scale to 8-bit
                    1 => u8::from_str_radix(s, 16).ok().map(|v| v * 17),
                    // 2-char: "RR" → direct 8-bit
                    2 => u8::from_str_radix(s, 16).ok(),
                    // 3-char: "RRR" → take first 2
                    3 => u8::from_str_radix(&s[..2], 16).ok(),
                    // 4-char: "RRRR" → take first 2 (high byte of 16-bit)
                    4 => u8::from_str_radix(&s[..2], 16).ok(),
                    _ => u8::from_str_radix(&s[..s.len().min(2)], 16).ok(),
                }
            };
            if let (Some(r), Some(g), Some(b)) = (
                parse_component(components[0]),
                parse_component(components[1]),
                parse_component(components[2]),
            ) {
                self.set(index, r, g, b);
            }
        }
    }
}

impl Default for Palette {
    fn default() -> Self {
        Self::new()
    }
}

pub fn query_terminal_colors() -> Palette {
    // Try loading from disk cache first — avoids the slow OSC query
    if let Some(cached) = load_palette_cache() {
        return cached;
    }

    let mut palette = Palette::new();

    #[cfg(unix)]
    query_terminal_colors_unix(&mut palette);

    palette
}

/// Non-blocking drain of any pending bytes on stdin.
/// Call after palette queries to discard late-arriving OSC responses
/// that would otherwise leak into the event loop as garbage key input.
#[cfg(unix)]
pub fn drain_stdin() {
    use std::os::unix::io::AsRawFd;
    let fd = std::io::stdin().as_raw_fd();
    let mut tmp = [0u8; 1024];

    // Keep reading while data is immediately available (poll with 0ms timeout)
    loop {
        let mut fds = [libc::pollfd {
            fd,
            events: libc::POLLIN,
            revents: 0,
        }];
        let ret = unsafe { libc::poll(fds.as_mut_ptr(), 1, 0) };
        if ret <= 0 {
            break;
        }
        let n = unsafe { libc::read(fd, tmp.as_mut_ptr() as *mut libc::c_void, tmp.len()) };
        if n <= 0 {
            break;
        }
    }
}

#[cfg(not(unix))]
pub fn drain_stdin() {}

#[cfg(unix)]
fn query_terminal_colors_unix(palette: &mut Palette) {
    use std::io::Write;
    use std::os::unix::io::AsRawFd;
    use std::time::Instant;

    if crossterm::terminal::enable_raw_mode().is_err() {
        return;
    }

    let mut stdout = std::io::stdout();
    let in_tmux = std::env::var("TMUX").is_ok();

    // Send OSC 4 queries for all 16 ANSI colors.
    // In tmux, we must wrap each query in DCS passthrough:
    //   ESC P tmux; <doubled-ESC query> ESC \
    // Outside tmux, send raw OSC with BEL terminator.
    for i in 0..16u8 {
        if in_tmux {
            // DCS passthrough: inner ESC is doubled (\x1b\x1b)
            let _ = write!(stdout, "\x1bPtmux;\x1b\x1b]4;{i};?\x07\x1b\\");
        } else {
            let _ = write!(stdout, "\x1b]4;{i};?\x07");
        }
    }
    let _ = stdout.flush();

    let fd = std::io::stdin().as_raw_fd();
    let mut buf = Vec::with_capacity(8192);
    let mut tmp = [0u8; 1024];
    let timeout_ms: i32 = if in_tmux { 500 } else { 300 };
    let deadline = Instant::now() + std::time::Duration::from_millis(timeout_ms as u64);

    loop {
        let remaining_ms = deadline
            .saturating_duration_since(Instant::now())
            .as_millis() as i32;
        if remaining_ms <= 0 {
            break;
        }

        let mut fds = [libc::pollfd {
            fd,
            events: libc::POLLIN,
            revents: 0,
        }];

        let ret = unsafe { libc::poll(fds.as_mut_ptr(), 1, remaining_ms) };
        if ret <= 0 {
            break;
        }

        let n =
            unsafe { libc::read(fd, tmp.as_mut_ptr() as *mut libc::c_void, tmp.len()) };
        if n <= 0 {
            break;
        }
        buf.extend_from_slice(&tmp[..n as usize]);

        // Parse incrementally — break as soon as we have all 16 colors.
        let raw = String::from_utf8_lossy(&buf);
        let mut check = Palette::new();
        if in_tmux {
            let stripped = strip_dcs_wrappers(&raw);
            parse_osc_responses(&stripped, &mut check);
        } else {
            parse_osc_responses(&raw, &mut check);
        }
        if check.complete() {
            break;
        }
    }

    let _ = crossterm::terminal::disable_raw_mode();

    // Drain any late-arriving OSC responses from stdin so they don't leak
    // into crossterm's event stream later. Some terminals (especially macOS)
    // defer response delivery until the window regains focus.
    drain_stdin();

    let raw = String::from_utf8_lossy(&buf);
    if in_tmux {
        let stripped = strip_dcs_wrappers(&raw);
        parse_osc_responses(&stripped, palette);
    } else {
        parse_osc_responses(&raw, palette);
    }

    // Debug dump
    if let Ok(home) = std::env::var("HOME") {
        let path = format!("{home}/.cache/gg-palette-debug.log");
        if let Ok(mut f) = std::fs::File::create(&path) {
            let _ = writeln!(f, "in_tmux: {in_tmux}");
            let _ = writeln!(f, "raw bytes: {}", buf.len());
            let _ = writeln!(f, "raw hex: {:02x?}", &buf[..buf.len().min(512)]);
            let _ = writeln!(f, "raw str: {:?}", &raw[..raw.len().min(512)]);
            for (i, c) in palette.colors.iter().enumerate() {
                let _ = writeln!(f, "palette[{i}] = {c:?}");
            }
        }
    }

    // Cache successful results to disk for faster subsequent runs
    let resolved = palette.colors.iter().filter(|c| c.is_some()).count();
    if resolved > 0 {
        save_palette_cache(palette);
    }
}

/// Strip DCS passthrough wrappers that tmux adds to responses.
/// tmux wraps OSC responses in: \x1bP ... \x1b\\
/// We extract the inner content so the OSC parser can handle it.
fn strip_dcs_wrappers(data: &str) -> String {
    let mut result = String::with_capacity(data.len());
    let bytes = data.as_bytes();
    let mut i = 0;

    while i < bytes.len() {
        // Check for DCS start: ESC P
        if i + 1 < bytes.len() && bytes[i] == 0x1b && bytes[i + 1] == b'P' {
            i += 2;
            // Read until ST terminator (ESC \)
            let start = i;
            while i < bytes.len() {
                if i + 1 < bytes.len() && bytes[i] == 0x1b && bytes[i + 1] == b'\\' {
                    // Extract inner content (the OSC sequences inside the DCS)
                    result.push_str(&data[start..i]);
                    i += 2;
                    break;
                }
                i += 1;
            }
        } else {
            // Pass through non-DCS data as-is
            result.push(bytes[i] as char);
            i += 1;
        }
    }

    result
}

fn parse_osc_responses(data: &str, palette: &mut Palette) {
    let bytes = data.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        // Look for OSC start: ESC ] (two bytes) or 0x9d (single byte C1)
        let osc_start = if i + 1 < bytes.len() && bytes[i] == 0x1b && bytes[i + 1] == b']' {
            i += 2;
            true
        } else if bytes[i] == 0x9d {
            i += 1;
            true
        } else {
            false
        };

        if osc_start {
            let start = i;
            while i < bytes.len() {
                if bytes[i] == 0x07 {
                    // BEL terminator
                    palette.parse_osc4_response(&data[start..i]);
                    i += 1;
                    break;
                } else if bytes[i] == 0x9c {
                    // Single-byte ST
                    palette.parse_osc4_response(&data[start..i]);
                    i += 1;
                    break;
                } else if i + 1 < bytes.len() && bytes[i] == 0x1b && bytes[i + 1] == b'\\' {
                    // Two-byte ST
                    palette.parse_osc4_response(&data[start..i]);
                    i += 2;
                    break;
                }
                i += 1;
            }
        } else {
            i += 1;
        }
    }
}

fn palette_cache_path() -> Option<std::path::PathBuf> {
    dirs::cache_dir().map(|d| d.join("gg").join("palette.json"))
}

fn save_palette_cache(palette: &Palette) {
    let Some(path) = palette_cache_path() else {
        return;
    };
    if let Some(parent) = path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let colors: Vec<Option<[u8; 3]>> = palette
        .colors
        .iter()
        .map(|c| c.map(|(r, g, b)| [r, g, b]))
        .collect();
    if let Ok(json) = serde_json::to_string(&colors) {
        let _ = std::fs::write(&path, json);
    }
}

fn load_palette_cache() -> Option<Palette> {
    let path = palette_cache_path()?;
    let data = std::fs::read_to_string(&path).ok()?;

    // Invalidate cache older than 24 hours
    if let Ok(meta) = std::fs::metadata(&path) {
        if let Ok(modified) = meta.modified() {
            if modified.elapsed().unwrap_or_default().as_secs() > 86400 {
                let _ = std::fs::remove_file(&path);
                return None;
            }
        }
    }

    let colors: Vec<Option<[u8; 3]>> = serde_json::from_str(&data).ok()?;
    if colors.len() != 16 {
        return None;
    }

    let mut palette = Palette::new();
    for (i, c) in colors.iter().enumerate() {
        if let Some([r, g, b]) = c {
            palette.set(i, *r, *g, *b);
        }
    }

    // Only use cache if it has a reasonable number of colors
    if palette.colors.iter().filter(|c| c.is_some()).count() >= 8 {
        Some(palette)
    } else {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_osc4_basic() {
        let mut p = Palette::new();
        p.parse_osc4_response("4;0;rgb:1e1e/1e1e/1e1e");
        assert_eq!(p.colors[0], Some((0x1e, 0x1e, 0x1e)));
    }

    #[test]
    fn parse_osc4_short_hex() {
        let mut p = Palette::new();
        p.parse_osc4_response("4;1;rgb:ff/00/88");
        assert_eq!(p.colors[1], Some((0xff, 0x00, 0x88)));
    }

    #[test]
    fn parse_osc4_ignores_invalid() {
        let mut p = Palette::new();
        p.parse_osc4_response("4;16;rgb:ff/ff/ff"); // index too high
        p.parse_osc4_response("5;0;rgb:ff/ff/ff"); // wrong type
        p.parse_osc4_response("garbage");
        assert!(p.colors.iter().all(|c| c.is_none()));
    }

    #[test]
    fn parse_osc_responses_multiple() {
        let mut p = Palette::new();
        let data = "\x1b]4;0;rgb:1e/1e/1e\x07\x1b]4;1;rgb:ff/00/00\x07";
        parse_osc_responses(data, &mut p);
        assert_eq!(p.colors[0], Some((0x1e, 0x1e, 0x1e)));
        assert_eq!(p.colors[1], Some((0xff, 0x00, 0x00)));
    }

    #[test]
    fn parse_osc_responses_with_st_terminator() {
        let mut p = Palette::new();
        let data = "\x1b]4;2;rgb:00/ff/00\x1b\\";
        parse_osc_responses(data, &mut p);
        assert_eq!(p.colors[2], Some((0x00, 0xff, 0x00)));
    }

    #[test]
    fn strip_dcs_wrappers_extracts_inner() {
        let wrapped = "\x1bP\x1b]4;0;rgb:1e/1e/1e\x07\x1b\\";
        let stripped = strip_dcs_wrappers(wrapped);
        let mut p = Palette::new();
        parse_osc_responses(&stripped, &mut p);
        assert_eq!(p.colors[0], Some((0x1e, 0x1e, 0x1e)));
    }

    #[test]
    fn cache_round_trip() {
        let mut p = Palette::new();
        for i in 0..16 {
            p.set(i, (i as u8) * 16, (i as u8) * 8, (i as u8) * 4);
        }

        let colors: Vec<Option<[u8; 3]>> = p
            .colors
            .iter()
            .map(|c| c.map(|(r, g, b)| [r, g, b]))
            .collect();
        let json = serde_json::to_string(&colors).unwrap();
        let decoded: Vec<Option<[u8; 3]>> = serde_json::from_str(&json).unwrap();

        for (i, c) in decoded.iter().enumerate() {
            let [r, g, b] = c.unwrap();
            assert_eq!(p.colors[i], Some((r, g, b)));
        }
    }
}