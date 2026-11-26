use super::{
    term,
    touch::{LABEL, Touch},
};
use crate::ipc::{Client, field, wire};
use crate::terminal_mouse::utf8_continuations;
use serde_json::{Value, json};
use std::io::{self, Write};
use std::os::fd::AsRawFd;
use std::path::Path;
use std::time::{Duration, Instant};

pub(super) const CAPABILITIES: &[&str] = &["attach", "agy-permission-v1"];
const LIVE_OUTPUT: u8 = 2;
const INPUT: u8 = 3;
const MAX_SNAPSHOT: u64 = 200 * 1024 * 1024;
const POLL: Duration = Duration::from_millis(40);
const PASTE_START: &[u8] = b"\x1b[200~";
const PASTE_END: &[u8] = b"\x1b[201~";
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Exit {
    Board,
    Next,
    Previous,
}

pub(super) fn run(
    path: &Path,
    id: &str,
    name: &str,
    touch_back: bool,
    output: &mut impl Write,
) -> io::Result<Exit> {
    run_client(
        Client::dial(path, CAPABILITIES)?,
        id,
        name,
        touch_back,
        true,
        output,
    )
}

pub(super) fn run_client(
    mut client: Client,
    id: &str,
    name: &str,
    touch_back: bool,
    cycle: bool,
    output: &mut impl Write,
) -> io::Result<Exit> {
    if !client.supports("attach") {
        return Err(io::Error::other("Terminal attach capability was refused"));
    }
    let agy_permission = client.supports("agy-permission-v1");
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
    let mut touch = touch_back.then(|| Touch::from_snapshot(&snapshot));
    repaint(&mut paint, name, geometry, cycle, touch.as_mut());
    output.write_all(paint.as_bytes())?;
    output.flush()?;
    send_control("resize", Some(worker_size(geometry)), &mut stream)?;

    let mut permission = None;
    let permission_control = |stream: &mut std::os::unix::net::UnixStream,
                              op: &str,
                              request: &str,
                              display: &str,
                              choice: Option<u64>| {
        runtime_control(
            stream,
            &json!({
                "op":op,"endpoint_id":endpoint,"session_id":id,"generation":generation,
                "request":request,"display":display,"choice":choice,
            }),
        )
    };
    let mut filter = AttachKeys {
        no_cycle: !cycle,
        ..AttachKeys::default()
    };
    let mut repaint_pending = false;
    let mut seen = Vec::new();
    let mut input = [0; 4096];
    let mut last_input = Instant::now();
    while !term::stopped() {
        let next = term::size();
        if next != geometry {
            geometry = next;
            send_control("resize", Some(worker_size(geometry)), &mut stream)?;
            if let Some(touch) = &mut touch {
                touch.cancel_press();
            }
            repaint_pending = true;
        }
        if repaint_pending && touch.as_ref().is_none_or(Touch::ground) {
            paint.clear();
            repaint(&mut paint, name, geometry, cycle, touch.as_mut());
            output.write_all(paint.as_bytes())?;
            output.flush()?;
            repaint_pending = false;
        }
        let (input_ready, socket_ready) = term::ready(Some(stream.as_raw_fd()), POLL)?;
        if socket_ready {
            let (tag, payload) = match wire::read_frame(&mut stream) {
                Ok(frame) => frame,
                Err(error) if error.kind() == io::ErrorKind::UnexpectedEof => break,
                Err(error) => return Err(error),
            };
            match tag {
                LIVE_OUTPUT => {
                    if let Some(touch) = &mut touch {
                        seen.clear();
                        if let Err(error) = touch.observe(&payload, &mut seen) {
                            let _ = send_control("detach", None, &mut stream);
                            return Err(error);
                        }
                        output.write_all(&seen)?;
                        repaint_pending |= touch.needs_paint();
                        if repaint_pending && touch.ground() {
                            paint.clear();
                            repaint(&mut paint, name, geometry, cycle, Some(touch));
                            output.write_all(paint.as_bytes())?;
                            repaint_pending = false;
                        }
                    } else {
                        output.write_all(&payload)?;
                    }
                    output.flush()?;
                }
                wire::CONTROL => {
                    let message: Value = serde_json::from_slice(&payload)?;
                    match field(&message, "op") {
                        "agy_permission" if agy_permission => {
                            if message["endpoint_id"] != endpoint
                                || message["session_id"] != id
                                || message["generation"] != generation
                            {
                                return Err(io::Error::other(
                                    "invalid Antigravity permission display scope",
                                ));
                            }
                            permission = None;
                            filter = AttachKeys::default();
                            if let Some(menu) = PermissionInput::offer(&message) {
                                output.flush()?;
                                if unsafe { libc::tcflush(libc::STDIN_FILENO, libc::TCIFLUSH) } == 0
                                {
                                    permission_control(
                                        &mut stream,
                                        "agy_permission_displayed",
                                        &menu.request,
                                        &menu.display,
                                        None,
                                    )?;
                                    permission = Some(menu);
                                }
                            }
                            // The readiness bit may refer to input flushed above.
                            continue;
                        }
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
        if input_ready {
            let count = term::input(&mut input)?;
            if count == 0 {
                break;
            }
            last_input = Instant::now();
            let was_paste = filter.paste;
            let (bytes, command) = filter.feed_touch(&input[..count], touch.as_mut());
            if let Some(menu) = &mut permission {
                if was_paste
                    || filter.paste
                    || bytes
                        .windows(PASTE_START.len())
                        .any(|bytes| bytes == PASTE_START)
                {
                    menu.digits.clear();
                    menu.invalid = false;
                } else if let Some(choice) = menu.input(&bytes, output)? {
                    permission_control(
                        &mut stream,
                        "agy_permission_reply",
                        &menu.request,
                        &menu.display,
                        Some(choice),
                    )?;
                    permission = None;
                }
            } else if !bytes.is_empty() {
                wire::write_frame(&mut stream, INPUT, &bytes)?;
            }
            if let Some(exit) = command {
                send_control("detach", None, &mut stream)?;
                return Ok(exit);
            }
        } else if !filter.paste
            && !filter.pending.is_empty()
            && last_input.elapsed() >= Duration::from_millis(50)
        {
            let mut pending = Vec::new();
            filter.flush_pending(&mut pending, touch.as_mut());
            if let Some(menu) = &mut permission {
                if let Some(choice) = menu.input(&pending, output)? {
                    permission_control(
                        &mut stream,
                        "agy_permission_reply",
                        &menu.request,
                        &menu.display,
                        Some(choice),
                    )?;
                    permission = None;
                }
            } else if !pending.is_empty() {
                wire::write_frame(&mut stream, INPUT, &pending)?;
            }
        }
    }
    Ok(Exit::Board)
}

fn runtime_control(stream: &mut std::os::unix::net::UnixStream, message: &Value) -> io::Result<()> {
    wire::write_frame(stream, wire::CONTROL, &serde_json::to_vec(message)?)
}

struct PermissionInput {
    request: String,
    display: String,
    choices: u64,
    digits: String,
    invalid: bool,
}

impl PermissionInput {
    fn offer(message: &Value) -> Option<Self> {
        let request = field(message, "request");
        let choices = message["choices"]
            .as_u64()
            .filter(|choices| (1..=12).contains(choices))?;
        let display = field(message, "display");
        (crate::supervisor::runtime::valid_id(request)
            && crate::supervisor::runtime::valid_id(display))
        .then_some(Self {
            request: request.into(),
            display: display.into(),
            choices,
            digits: String::new(),
            invalid: false,
        })
    }

    fn input(&mut self, bytes: &[u8], output: &mut impl Write) -> io::Result<Option<u64>> {
        for &byte in bytes {
            match byte {
                b'0'..=b'9' if !self.invalid && self.digits.len() < 2 => {
                    self.digits.push(char::from(byte));
                    output.write_all(&[byte])?;
                }
                8 | 127 if !self.invalid => {
                    if self.digits.pop().is_some() {
                        output.write_all(b"\x08 \x08")?;
                    }
                }
                b'\r' | b'\n' => {
                    let value = (!self.invalid)
                        .then(|| self.digits.parse::<u64>().ok())
                        .flatten()
                        .filter(|&choice| choice <= self.choices);
                    self.digits.clear();
                    self.invalid = false;
                    output.write_all(b"\r\n")?;
                    output.flush()?;
                    if value.is_some() {
                        return Ok(value);
                    }
                }
                _ => self.invalid = true,
            }
        }
        output.flush()?;
        Ok(None)
    }
}

fn worker_size((cols, rows): (u16, u16)) -> (u16, u16) {
    (cols, if rows >= 3 { rows - 1 } else { rows })
}

fn repaint(
    output: &mut String,
    name: &str,
    (cols, rows): (u16, u16),
    cycle: bool,
    touch: Option<&mut Touch>,
) {
    use std::fmt::Write;
    let active = touch.is_some_and(|touch| touch.paint((cols, rows), output));
    if rows >= 3 {
        let nav = if cycle { "  ctrl+p/ctrl+n switch" } else { "" };
        let label = if active {
            format!("{LABEL}  {name}{nav}")
        } else {
            format!("{name}  ctrl+q returns to swarm{nav}")
        };
        let text = super::clip(&label, cols);
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
    no_cycle: bool,
    pending: Vec<u8>,
    paste: bool,
    string_payload: bool,
    escape: bool,
    utf8_left: u8,
}

impl AttachKeys {
    #[cfg(test)]
    fn feed(&mut self, bytes: &[u8]) -> (Vec<u8>, Option<Exit>) {
        self.feed_touch(bytes, None)
    }

    fn feed_touch(
        &mut self,
        bytes: &[u8],
        mut touch: Option<&mut Touch>,
    ) -> (Vec<u8>, Option<Exit>) {
        let mut out = Vec::with_capacity(bytes.len() + self.pending.len());
        for &byte in bytes {
            if self.paste {
                self.pending.push(byte);
                while !PASTE_END.starts_with(&self.pending) {
                    out.push(self.pending.remove(0));
                }
                if self.pending == PASTE_END {
                    out.append(&mut self.pending);
                    self.paste = false;
                }
                continue;
            }
            if self.string_payload {
                out.push(byte);
                if byte == 7 || (self.escape && byte == b'\\') {
                    self.string_payload = false;
                }
                self.escape = byte == 27;
                continue;
            }
            if self.utf8_left > 0 && (0x80..=0xbf).contains(&byte) {
                self.utf8_left -= 1;
                out.push(byte);
                continue;
            }
            self.utf8_left = utf8_continuations(byte);
            if self.escape && matches!(byte, b']' | b'P' | b'_' | b'^' | b'X') {
                self.string_payload = true;
                out.append(&mut self.pending);
                out.push(byte);
                self.escape = false;
                continue;
            }
            self.escape = byte == 27;
            if let Some(exit) = match byte {
                0x11 => Some(Exit::Board),
                0x0e if !self.no_cycle => Some(Exit::Next),
                0x10 if !self.no_cycle => Some(Exit::Previous),
                _ => None,
            } {
                self.flush_pending(&mut out, touch.as_deref_mut());
                return (out, Some(exit));
            }
            if byte == 27
                || (byte == 0x9b && touch.as_ref().is_some_and(|touch| touch.retain_input()))
            {
                self.flush_pending(&mut out, touch.as_deref_mut());
                self.pending.push(byte);
                continue;
            }
            if self.pending.is_empty() {
                if let Some(touch) = &mut touch {
                    touch.cancel_press();
                }
                out.push(byte);
                continue;
            }
            self.pending.push(byte);
            if self.pending == b"\x1b[" {
                continue;
            }
            if (self.pending.starts_with(b"\x1b[") || self.pending.starts_with(b"\x9b"))
                && !(0x40..=0x7e).contains(&byte)
                && self.pending.len() < 64
            {
                continue;
            }
            if let Some(touch) = &mut touch {
                if touch.retain_input() && mouse_prefix(&self.pending) {
                    let (consume, clicked) = touch.mouse(&self.pending);
                    if clicked {
                        self.pending.clear();
                        return (out, Some(Exit::Board));
                    }
                    if consume {
                        self.pending.clear();
                        continue;
                    }
                } else {
                    touch.cancel_press();
                }
            }
            if self.pending == PASTE_START {
                self.paste = true;
            } else if let Some(exit) = kitty_command(&self.pending)
                && (!self.no_cycle || exit == Exit::Board)
            {
                self.pending.clear();
                return (out, Some(exit));
            }
            out.append(&mut self.pending);
        }
        (out, None)
    }

    fn flush_pending(&mut self, output: &mut Vec<u8>, touch: Option<&mut Touch>) {
        let consumed = touch.is_some_and(|touch| {
            touch.retain_input() && mouse_prefix(&self.pending) && touch.mouse(&self.pending).0
        });
        if consumed {
            self.pending.clear();
        } else {
            output.append(&mut self.pending);
        }
    }
}

fn mouse_prefix(frame: &[u8]) -> bool {
    frame.starts_with(b"\x1b[<") || frame.starts_with(b"\x9b<")
}

fn kitty_command(frame: &[u8]) -> Option<Exit> {
    let text = std::str::from_utf8(frame.strip_prefix(b"\x1b[")?.strip_suffix(b"u")?).ok()?;
    let mut fields = text.split(';');
    let key = fields.next()?.split(':').next()?;
    let mut modifiers = fields.next()?.split(':');
    let mask: u16 = modifiers.next()?.parse().ok()?;
    // Caps and Num Lock do not change the shortcut. Modified chords and releases belong to the PTY.
    if mask.checked_sub(1)? & !0xc0 != 4
        || !matches!(modifiers.next(), None | Some("1" | "2"))
        || modifiers.next().is_some()
    {
        return None;
    }
    match key {
        "113" => Some(Exit::Board),
        "110" => Some(Exit::Next),
        "112" => Some(Exit::Previous),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn linked_attach_forwards_cycle_keys_to_its_worker() {
        let mut keys = AttachKeys {
            no_cycle: true,
            ..AttachKeys::default()
        };
        for input in [b"\x0e".as_slice(), b"\x10", b"\x1b[110;5u", b"\x1b[112;5u"] {
            assert_eq!(keys.feed(input), (input.to_vec(), None));
        }
        assert_eq!(keys.feed(b"\x11"), (vec![], Some(Exit::Board)));
    }

    #[test]
    fn attachment_controls_survive_chunking_and_preserve_worker_input() {
        for (key, exit) in [
            (b'n', Exit::Next),
            (b'p', Exit::Previous),
            (b'q', Exit::Board),
        ] {
            let mut filter = AttachKeys::default();
            assert_eq!(filter.feed(b"before\x1b[20"), (b"before".to_vec(), None));
            let pasted = [b"0~".as_slice(), &[key & 31], b"\x1b[110;5u\x1b[20"].concat();
            let (out, command) = filter.feed(&pasted);
            assert_eq!(command, None);
            assert_eq!(out, [PASTE_START, &[key & 31], b"\x1b[110;5u"].concat());
            assert_eq!(filter.feed(b"1~"), (PASTE_END.to_vec(), None));
            for sequence in [
                b"\x1b[112;6u".as_slice(),
                b"\x1b[110;5:3u",
                b"\x1b]title\x0e\x1b[113;5u\x07",
                b"\x1bPdata\x10\x1b\\",
            ] {
                assert_eq!(filter.feed(sequence), (sequence.to_vec(), None));
            }
            for byte in format!("\x1b[{};5:1", key).bytes() {
                assert_eq!(filter.feed(&[byte]), (Vec::new(), None));
            }
            assert_eq!(filter.feed(b"uignored"), (Vec::new(), Some(exit)));
            assert_eq!(
                filter.feed(&[b'a', key & 31, b'b']),
                (vec![b'a'], Some(exit))
            );
        }
    }
}
