#[derive(Debug, PartialEq, Eq)]
pub(super) enum Key {
    Escape,
    Enter,
    AltEnter,
    Backspace,
    CtrlC,
    CtrlD,
    CtrlE,
    CtrlS,
    CtrlT,
    CtrlU,
    CtrlX,
    Tab,
    BackTab,
    Up,
    Down,
    Left,
    Right,
    Home,
    End,
    EditHome,
    EditEnd,
    PageUp,
    PageDown,
    Mouse(Mouse),
    Text(char),
    Paste(String),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) struct Mouse {
    pub(super) x: u16,
    pub(super) y: u16,
    pub(super) action: MouseAction,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum MouseAction {
    Press,
    Release,
    Drag,
    WheelUp,
    WheelDown,
}

fn mouse(sequence: &[u8]) -> Option<Mouse> {
    let body = std::str::from_utf8(sequence.strip_prefix(b"<")?).ok()?;
    let (body, released) = if let Some(body) = body.strip_suffix('m') {
        (body, true)
    } else {
        (body.strip_suffix('M')?, false)
    };
    let mut values = body.split(';').map(str::parse::<u16>);
    let action = match values.next()?.ok()? {
        0 if released => MouseAction::Release,
        0 => MouseAction::Press,
        32 | 35 => MouseAction::Drag,
        64 => MouseAction::WheelUp,
        65 => MouseAction::WheelDown,
        _ => return None,
    };
    let x = values.next()?.ok()?;
    let y = values.next()?.ok()?;
    (x > 0 && y > 0 && values.next().is_none()).then_some(Mouse { x, y, action })
}

const PASTE_END: &[u8] = b"\x1b[201~";
const PASTE_LIMIT: usize = 16384;

#[derive(Default)]
pub(super) struct Keys {
    queued: Vec<u8>,
    paste: Option<Vec<u8>>,
}

impl Keys {
    pub(super) fn read(&mut self, bytes: &[u8], flush: bool) -> Vec<Key> {
        let mut keys = Vec::new();
        self.queued.extend_from_slice(bytes);
        let mut start = 0;
        while let Some(&first) = self.queued.get(start) {
            let pending = &self.queued[start..];
            if let Some(paste) = &mut self.paste {
                if pending.starts_with(PASTE_END) {
                    keys.push(Key::Paste(String::from_utf8_lossy(paste).into_owned()));
                    self.paste = None;
                    start += PASTE_END.len();
                    continue;
                }
                if PASTE_END.starts_with(pending) {
                    break;
                }
                if paste.len() < PASTE_LIMIT {
                    paste.push(first);
                }
                start += 1;
                continue;
            }
            if first == 27 {
                if let Some(sequence) = pending.strip_prefix(b"\x1b[") {
                    let end = sequence
                        .iter()
                        .position(|byte| (0x40..=0x7e).contains(byte));
                    if let Some(end) = end {
                        start += end + 3;
                        if &sequence[..=end] == b"200~" {
                            self.paste = Some(Vec::new());
                            continue;
                        }
                        match &sequence[..=end] {
                            b"A" => keys.push(Key::Up),
                            b"B" => keys.push(Key::Down),
                            b"C" => keys.push(Key::Right),
                            b"D" => keys.push(Key::Left),
                            b"Z" | b"9;2u" => keys.push(Key::BackTab),
                            b"27u" => keys.push(Key::Escape),
                            b"13;3u" => keys.push(Key::AltEnter),
                            b"100;5u" => keys.push(Key::CtrlD),
                            b"101;5u" => keys.push(Key::CtrlE),
                            b"115;5u" => keys.push(Key::CtrlS),
                            b"116;5u" => keys.push(Key::CtrlT),
                            b"1;9D" | b"57350;9u" | b"57350;33u" | b"97;5u" => {
                                keys.push(Key::EditHome)
                            }
                            b"1;9C" | b"57351;9u" | b"57351;33u" => keys.push(Key::EditEnd),
                            b"127;9u" | b"127;33u" | b"8;9u" | b"8;33u" | b"27;9;127~"
                            | b"27;9;8~" | b"117;5u" => keys.push(Key::CtrlU),
                            b"H" | b"1~" | b"7~" => keys.push(Key::Home),
                            b"F" | b"4~" | b"8~" => keys.push(Key::End),
                            b"5~" => keys.push(Key::PageUp),
                            b"6~" => keys.push(Key::PageDown),
                            _ => {
                                if let Some(event) = mouse(&sequence[..=end]) {
                                    keys.push(Key::Mouse(event));
                                }
                            }
                        }
                        continue;
                    }
                    if sequence.len() <= 64 && !flush {
                        break;
                    }
                    start = self.queued.len();
                    continue;
                }
                if pending.len() == 1 && !flush {
                    break;
                }
                if matches!(pending.get(1), Some(b'\r' | b'\n')) {
                    keys.push(Key::AltEnter);
                    start += 2;
                    continue;
                }
                if matches!(pending.get(1), Some(8 | 127)) {
                    // A legacy ESC prefix encodes Option, not Command, for Backspace.
                    start += 2;
                    continue;
                }
                keys.push(Key::Escape);
                start += 1;
                continue;
            }

            let key = match first {
                b'\r' | b'\n' => Some(Key::Enter),
                8 | 127 => Some(Key::Backspace),
                1 => Some(Key::EditHome),
                3 => Some(Key::CtrlC),
                4 => Some(Key::CtrlD),
                5 => Some(Key::CtrlE),
                19 => Some(Key::CtrlS),
                20 => Some(Key::CtrlT),
                21 => Some(Key::CtrlU),
                24 => Some(Key::CtrlX),
                b'\t' => Some(Key::Tab),
                32..=126 => Some(Key::Text(char::from(first))),
                128..=255 => {
                    let width = match first {
                        0xc2..=0xdf => 2,
                        0xe0..=0xef => 3,
                        0xf0..=0xf4 => 4,
                        _ => 1,
                    };
                    if pending.len() < width && !flush {
                        break;
                    }
                    if let Some(ch) = pending
                        .get(..width)
                        .and_then(|raw| std::str::from_utf8(raw).ok())
                        .and_then(|s| s.chars().next())
                    {
                        keys.push(Key::Text(ch));
                        start += width;
                        continue;
                    }
                    None
                }
                _ => None,
            };
            if let Some(key) = key {
                keys.push(key);
            }
            start += 1;
        }
        self.queued.drain(..start);
        keys
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn split_csi_unicode_and_paste_never_dispatch_commands() {
        let mut keys = Keys::default();
        assert_eq!(keys.read(b"\x1b[", false), []);
        assert_eq!(keys.read(b"A\xe5", false), [Key::Up]);
        assert_eq!(
            keys.read(b"\xb9\xb4\x1b[200~n\r\x18\x1b[20", false),
            [Key::Text('年')]
        );
        assert_eq!(
            keys.read(b"1~\x1b[27u", false),
            [Key::Paste("n\r\x18".into()), Key::Escape]
        );
    }
}
