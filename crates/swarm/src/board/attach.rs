use super::term;
use crate::ipc::{Client, field, wire};
use serde_json::{Value, json};
use std::io::{self, Write};
use std::os::fd::AsRawFd;
use std::path::Path;
use std::time::{Duration, Instant};

const LIVE_OUTPUT: u8 = 2;
const INPUT: u8 = 3;
const MAX_SNAPSHOT: u64 = 200 * 1024 * 1024;
const POLL: Duration = Duration::from_millis(40);
const PASTE_START: &[u8] = b"\x1b[200~";
const PASTE_END: &[u8] = b"\x1b[201~";
const KITTY_DETACH: &[u8] = b"\x1b[113;5u";
const KITTY_DETACH_PRESS: &[u8] = b"\x1b[113;5:1u";

pub(super) fn run(path: &Path, id: &str, name: &str, output: &mut impl Write) -> io::Result<()> {
    let mut client = Client::dial(path, &["attach"])?;
    let lease = client.request("attach", Some(id), "lease")?;
    if field(&lease, "endpoint_id") != client.endpoint() || field(&lease, "session_id") != id {
        return Err(io::Error::other(
            "attach lease does not match the selected session",
        ));
    }
    let generation = lease["generation"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(|| io::Error::other("attach lease has no valid generation"))?;
    let length = lease["snapshot_len"].as_u64().unwrap_or_default();
    if length > MAX_SNAPSHOT {
        return Err(io::Error::other("attach snapshot exceeds protocol limit"));
    }
    let snapshot = client.take_snapshot(length as usize)?;
    let (mut stream, endpoint) = client.into_attach_parts();
    let send_control =
        |op, dimensions: Option<(u16, u16)>, stream: &mut std::os::unix::net::UnixStream| {
            let mut message = json!({
                "op": op, "endpoint_id": endpoint, "session_id": id, "generation": generation,
            });
            if let Some((cols, rows)) = dimensions {
                message["cols"] = json!(cols);
                message["rows"] = json!(rows);
            }
            wire::write_frame(stream, wire::CONTROL, &serde_json::to_vec(&message)?)
        };
    let mut geometry = term::size();
    let mut paint = String::from(term::RESTORE);
    if !snapshot.is_empty() {
        render_snapshot(&snapshot, geometry, &mut paint)?;
    }
    hint(&mut paint, name, geometry);
    output.write_all(paint.as_bytes())?;
    output.flush()?;
    send_control("resize", Some(worker_size(geometry)), &mut stream)?;

    let mut filter = AttachKeys::default();
    let mut input = [0; 4096];
    let mut last_input = Instant::now();
    while !term::stopped() {
        let next = term::size();
        if next != geometry {
            geometry = next;
            send_control("resize", Some(worker_size(geometry)), &mut stream)?;
            paint.clear();
            hint(&mut paint, name, geometry);
            output.write_all(paint.as_bytes())?;
            output.flush()?;
        }
        let (input_ready, socket_ready) = term::ready(Some(stream.as_raw_fd()), POLL)?;
        if input_ready {
            let count = term::input(&mut input)?;
            if count == 0 {
                break;
            }
            last_input = Instant::now();
            let (bytes, detached) = filter.feed(&input[..count]);
            if !bytes.is_empty() {
                wire::write_frame(&mut stream, INPUT, &bytes)?;
            }
            if detached {
                let _ = send_control("detach", None, &mut stream);
                break;
            }
        } else if !filter.pending.is_empty() && last_input.elapsed() >= POLL {
            wire::write_frame(&mut stream, INPUT, &std::mem::take(&mut filter.pending))?;
        }
        if socket_ready {
            let (tag, payload) = match wire::read_frame(&mut stream) {
                Ok(frame) => frame,
                Err(error) if error.kind() == io::ErrorKind::UnexpectedEof => break,
                Err(error) => return Err(error),
            };
            match tag {
                LIVE_OUTPUT => {
                    output.write_all(&payload)?;
                    output.flush()?;
                }
                wire::CONTROL => {
                    let message: Value = serde_json::from_slice(&payload)?;
                    match field(&message, "op") {
                        "detached" | "detach" | "lease_revoked" => break,
                        "error" => {
                            return Err(io::Error::other(field(&message, "error").to_owned()));
                        }
                        _ => {}
                    }
                }
                _ => return Err(io::Error::other("unexpected frame after attach snapshot")),
            }
        }
    }
    Ok(())
}

fn worker_size((cols, rows): (u16, u16)) -> (u16, u16) {
    (cols, if rows >= 3 { rows - 1 } else { rows })
}

fn hint(output: &mut String, name: &str, (cols, rows): (u16, u16)) {
    use std::fmt::Write;
    if rows >= 3 {
        let text = super::clip(&format!("{name}  ctrl+q returns to swarm"), cols);
        let _ = write!(
            output,
            "\x1b7\x1b[?6l\x1b[1;{}r\x1b[{rows};1H\x1b[2m{text}\x1b[0m\x1b[K\x1b8",
            rows - 1
        );
    } else {
        output.push_str("\x1b[r");
    }
}

fn render_snapshot(bytes: &[u8], geometry: (u16, u16), output: &mut String) -> io::Result<()> {
    use std::fmt::Write;
    let snap: Value = serde_json::from_slice(bytes)?;
    if snap["version"].as_u64() != Some(1) {
        return Err(io::Error::other("unsupported attach snapshot version"));
    }
    let (cols, rows) = worker_size(geometry);
    if flag(&snap, "altScreen") {
        output.push_str("\x1b[?1049h");
    }
    output.push_str("\x1b[0m\x1b[2J\x1b[H");
    let lines = snap["lines"]
        .as_array()
        .ok_or_else(|| io::Error::other("snapshot has no lines"))?;
    for (row, line) in lines.iter().take(usize::from(rows)).enumerate() {
        let _ = write!(output, "\x1b[{};1H", row + 1);
        let mut remaining = cols;
        if let Some(runs) = line["runs"].as_array() {
            for run in runs {
                let width = run["width"].as_u64().unwrap_or_default();
                if remaining == 0 || width == 0 || width > 1000 {
                    break;
                }
                style(run, output);
                let width = remaining.min(width as u16);
                output.push_str(&super::clip(field(run, "text"), width));
                remaining -= width;
            }
        }
    }
    let cursor = |key, end: u16| {
        snap[key]
            .as_u64()
            .unwrap_or_default()
            .min(u64::from(end - 1))
            + 1
    };
    let _ = write!(
        output,
        "\x1b[0m\x1b[{};{}H\x1b[?25{}",
        cursor("cursorY", rows),
        cursor("cursorX", cols),
        if flag(&snap, "cursorVisible") {
            'h'
        } else {
            'l'
        }
    );
    restore_input_modes(&snap, output);
    Ok(())
}

fn restore_input_modes(snap: &Value, output: &mut String) {
    use std::fmt::Write;
    let modes = &snap["inputModes"];
    let mouse = &snap["mouse"];
    const TRACKING: [u64; 5] = [9, 1000, 1001, 1002, 1003];
    const ENCODING: [u64; 4] = [1005, 1006, 1015, 1016];
    let mut enable = |mode| {
        let _ = write!(output, "\x1b[?{mode}h");
    };
    if flag(modes, "known") {
        for (key, valid) in [
            ("tracking", TRACKING.as_slice()),
            ("encoding", ENCODING.as_slice()),
        ] {
            if let Some(mode) = modes[key].as_u64().filter(|n| valid.contains(n)) {
                enable(mode);
            }
        }
        for (key, mode) in [
            ("cursorKeys", 1),
            ("focus", 1004),
            ("altScroll", 1007),
            ("paste", 2004),
        ] {
            if flag(modes, key) {
                enable(mode);
            }
        }
    } else if modes.is_null() && flag(mouse, "known") {
        for (key, mode_bits) in [
            ("tracking", TRACKING.as_slice()),
            ("encoding", ENCODING.as_slice()),
        ] {
            let mask = mouse[key].as_u64().unwrap_or_default();
            if mask.is_power_of_two()
                && let Some(mode) = mode_bits.get(mask.trailing_zeros() as usize)
            {
                enable(*mode);
            }
        }
    }
}

fn flag(row: &Value, field: &str) -> bool {
    row[field].as_bool().unwrap_or(false)
}

fn style(run: &Value, output: &mut String) {
    use std::fmt::Write;
    output.push_str("\x1b[0");
    for (key, code) in [
        ("bold", 1),
        ("faint", 2),
        ("italic", 3),
        ("underline", 4),
        ("reverse", 7),
    ] {
        if flag(run, key) {
            let _ = write!(output, ";{code}");
        }
    }
    for (key, code) in [("fg", 38), ("bg", 48)] {
        let color = field(run, key);
        if let Some(rgb) = color
            .strip_prefix('#')
            .filter(|hex| hex.len() == 6)
            .and_then(|hex| u32::from_str_radix(hex, 16).ok())
        {
            let _ = write!(
                output,
                ";{code};2;{};{};{}",
                rgb >> 16,
                (rgb >> 8) & 255,
                rgb & 255
            );
        }
    }
    output.push('m');
}

#[derive(Default)]
struct AttachKeys {
    pending: Vec<u8>,
    paste: bool,
}

impl AttachKeys {
    fn feed(&mut self, bytes: &[u8]) -> (Vec<u8>, bool) {
        let mut out = Vec::with_capacity(bytes.len() + self.pending.len());
        for &byte in bytes {
            self.pending.push(byte);
            loop {
                let pending = self.pending.as_slice();
                if !self.paste && matches!(pending, b"\x11" | KITTY_DETACH | KITTY_DETACH_PRESS) {
                    self.pending.clear();
                    return (out, true);
                }
                if pending == PASTE_START || pending == PASTE_END {
                    self.paste = pending == PASTE_START;
                    out.append(&mut self.pending);
                    break;
                }
                if [PASTE_START, PASTE_END, KITTY_DETACH, KITTY_DETACH_PRESS]
                    .iter()
                    .any(|sequence| sequence.starts_with(pending))
                {
                    break;
                }
                out.push(self.pending.remove(0));
            }
        }
        (out, false)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fragmented_bracketed_paste_and_detach_keep_earlier_bytes() {
        let mut filter = AttachKeys::default();
        assert_eq!(filter.feed(b"before\x1b[20"), (b"before".to_vec(), false));
        assert_eq!(
            filter.feed(b"0~\x11\x1b[201~\x1b[113;"),
            (b"\x1b[200~\x11\x1b[201~".to_vec(), false)
        );
        assert_eq!(filter.feed(b"5uignored"), (Vec::new(), true));
    }
}
