//! Tracks DEC mouse modes across worker terminal control boundaries.
use serde_json::{Value, json};
use std::io;

pub(crate) const TRACKING: [u16; 5] = [9, 1000, 1001, 1002, 1003];
pub(crate) const ENCODING: [u16; 4] = [1005, 1006, 1015, 1016];

#[derive(Default)]
pub(crate) struct Modes {
    pub(crate) known: bool,
    pub(crate) tracking: u16,
    pub(crate) encoding: u16,
}

#[derive(Default)]
enum Output {
    #[default]
    Ground,
    Escape,
    EscapeIntermediate,
    Csi,
    String {
        osc: bool,
        escaped: bool,
        utf8: u8,
    },
    Utf8(u8),
}

#[derive(Default)]
pub(crate) struct Change {
    pub(crate) modes: bool,
    pub(crate) encoding: bool,
    pub(crate) damage: bool,
    pub(crate) query: bool,
}

#[derive(Default)]
pub(crate) struct Tracker {
    pub(crate) modes: Modes,
    state: Output,
    pending: Vec<u8>,
    control_start: usize,
}

impl Tracker {
    pub(crate) fn new(modes: Modes) -> Self {
        Self {
            modes,
            ..Self::default()
        }
    }

    pub(crate) fn snapshot(&self) -> Value {
        json!({"known": self.modes.known && self.ground(),
               "tracking": self.modes.tracking, "encoding": self.modes.encoding})
    }

    pub(crate) fn ground(&self) -> bool {
        matches!(self.state, Output::Ground)
    }

    pub(crate) fn feed(
        &mut self,
        bytes: &[u8],
        mut out: Option<&mut Vec<u8>>,
        mut on_control: impl FnMut(&Modes, Change, Option<&mut Vec<u8>>) -> io::Result<()>,
    ) -> io::Result<()> {
        for &b in bytes {
            if self.ground() && !matches!(b, 0x1b | 0x80..=0x9f | 0xc2..=0xf4) {
                if let Some(out) = &mut out {
                    out.push(b);
                }
                continue;
            }
            if matches!(b, 0x1b | 0x9b)
                && !matches!(self.state, Output::String { .. })
                && !(b == 0x9b && matches!(self.state, Output::Utf8(_)))
            {
                self.control_start = self.pending.len();
            }
            self.pending.push(b);
            if let Output::String { escaped, utf8, .. } = self.state
                && (escaped && b != b'\\' || utf8 > 0 && b == 0x9c)
            {
                self.modes.known = false;
                on_control(
                    &self.modes,
                    Change {
                        modes: true,
                        ..Change::default()
                    },
                    out.as_deref_mut(),
                )?;
            }
            self.advance(b);
            if self.pending.len() > 64 * 1024 {
                self.modes.known = false;
                self.control_start = 0;
                on_control(
                    &self.modes,
                    Change {
                        modes: true,
                        ..Change::default()
                    },
                    out.as_deref_mut(),
                )?;
                if let Some(out) = &mut out {
                    out.append(&mut self.pending);
                } else {
                    self.pending.clear();
                }
            }
            if self.ground() {
                let change = self.observe_control();
                if change.modes || change.damage || change.query {
                    on_control(&self.modes, change, out.as_deref_mut())?;
                }
                if let Some(out) = &mut out {
                    out.append(&mut self.pending);
                } else {
                    self.pending.clear();
                }
                self.control_start = 0;
            }
        }
        Ok(())
    }

    fn advance(&mut self, b: u8) {
        use Output::*;
        self.state = match self.state {
            Utf8(left) if (0x80..=0xbf).contains(&b) => {
                if left == 1 {
                    Ground
                } else {
                    Utf8(left - 1)
                }
            }
            Utf8(_) | Ground => match b {
                0x1b => Escape,
                0x9b => Csi,
                0x90 | 0x98 | 0x9d..=0x9f => String {
                    osc: b == 0x9d,
                    escaped: false,
                    utf8: 0,
                },
                0xc2..=0xdf => Utf8(1),
                0xe0..=0xef => Utf8(2),
                0xf0..=0xf4 => Utf8(3),
                _ => Ground,
            },
            _ if matches!(b, 0x18 | 0x1a) => Ground,
            String { osc, escaped, utf8 } => {
                if utf8 > 0 && (0x80..=0xbf).contains(&b) {
                    String {
                        osc,
                        escaped: false,
                        utf8: utf8 - 1,
                    }
                } else if b == 0x9c || osc && b == 7 || escaped && b == b'\\' {
                    Ground
                } else {
                    String {
                        osc,
                        escaped: b == 0x1b,
                        utf8: utf8_continuations(b),
                    }
                }
            }
            _ if b == 0x1b => Escape,
            _ if b == 0x9b => Csi,
            Escape => match b {
                b'[' => Csi,
                b']' | b'P' | b'X' | b'^' | b'_' => String {
                    osc: b == b']',
                    escaped: false,
                    utf8: 0,
                },
                0x20..=0x2f => EscapeIntermediate,
                _ => Ground,
            },
            EscapeIntermediate if b >= 0x30 => Ground,
            Csi if (0x40..=0x7e).contains(&b) => Ground,
            _ => std::mem::take(&mut self.state),
        };
    }

    fn observe_control(&mut self) -> Change {
        let mut change = Change::default();
        // An ESC cancelling an earlier partial CSI starts a fresh control.
        let bytes = &self.pending[self.control_start..];
        if bytes == b"\x1bc" {
            self.modes = Modes {
                known: true,
                ..Modes::default()
            };
            change = Change {
                modes: true,
                encoding: true,
                damage: true,
                query: false,
            };
        } else if let Some(csi) = bytes
            .strip_prefix(b"\x1b[")
            .or_else(|| bytes.strip_prefix(b"\x9b"))
        {
            let Some((&final_byte, body)) = csi.split_last() else {
                return change;
            };
            if final_byte == b'p' && body == b"!" {
                self.modes.known = false;
                change.modes = true;
                return change;
            }
            if matches!(final_byte, b'J' | b'r') {
                change.damage = true;
            }
            if let Some(params) = body.strip_prefix(b"?") {
                if params
                    .split(|byte| *byte == b';')
                    .any(|p| matches!(p, b"47" | b"1047" | b"1049"))
                {
                    change.damage = true;
                }
                let query = final_byte == b's' || (final_byte == b'p' && params.ends_with(b"$"));
                let mutates = matches!(final_byte, b'h' | b'l' | b'r');
                let params = if final_byte == b'p' {
                    params.strip_suffix(b"$").unwrap_or(params)
                } else {
                    params
                };
                if mutates
                    && (params.len() > 256
                        || params.split(|byte| *byte == b';').count() > 32
                        || params
                            .split(|byte| *byte == b';')
                            .any(|p| p.len() > 5 || !p.is_empty() && decimal(p).is_none())
                        || final_byte == b'r' && params.is_empty())
                {
                    self.modes.known = false;
                    change.modes = true;
                    return change;
                }
                let old_encoding = self.modes.encoding;
                let mut changed = false;
                for parameter in params.split(|byte| *byte == b';') {
                    let Some(mode) = decimal(parameter) else {
                        continue;
                    };
                    let mask = mode_bit_from_number(mode, TRACKING);
                    let (bits, bit) = if mask > 0 {
                        (&mut self.modes.tracking, mask)
                    } else {
                        (
                            &mut self.modes.encoding,
                            mode_bit_from_number(mode, ENCODING),
                        )
                    };
                    if bit == 0 {
                        continue;
                    }
                    change.query |= query;
                    match final_byte {
                        b'h' => *bits |= bit,
                        b'l' => *bits &= !bit,
                        b'r' => self.modes.known = false,
                        _ => continue,
                    }
                    changed = true;
                    self.modes.known &= !self.modes.ambiguous();
                }
                change.modes = changed;
                change.encoding = old_encoding != self.modes.encoding;
            }
        }
        change
    }
}
impl Modes {
    pub(crate) fn from_snapshot(value: &Value) -> Self {
        let mouse = &value["mouse"];
        let modes = &value["inputModes"];
        if !mouse.is_null() {
            Self {
                known: mouse["known"] == true,
                tracking: mask(&mouse["tracking"], TRACKING.len()),
                encoding: mask(&mouse["encoding"], ENCODING.len()),
            }
        } else {
            Self {
                known: modes["known"] == true,
                tracking: mode_bit(&modes["tracking"], TRACKING),
                encoding: mode_bit(&modes["encoding"], ENCODING),
            }
        }
    }

    pub(crate) fn ambiguous(&self) -> bool {
        self.tracking.count_ones() > 1 || self.encoding.count_ones() > 1
    }

    pub(crate) fn supported(&self) -> Option<u16> {
        if !self.known {
            return None;
        }
        match (self.tracking, self.encoding) {
            (0, 0 | 2) => Some(0),
            (2, 2) => Some(1000),
            (8, 2) => Some(1002),
            (16, 2) => Some(1003),
            _ => None,
        }
    }
}

fn mask(value: &Value, width: usize) -> u16 {
    match value.as_u64() {
        Some(bits) if bits < 1 << width => bits as u16,
        _ => u16::MAX,
    }
}

fn mode_bit<const N: usize>(value: &Value, modes: [u16; N]) -> u16 {
    match value.as_u64() {
        None if value.is_null() => 0,
        Some(0) => 0,
        Some(value) if value <= u16::MAX as u64 => {
            let bit = modes.iter().position(|mode| u64::from(*mode) == value);
            bit.map_or(u16::MAX, |i| 1 << i)
        }
        _ => u16::MAX,
    }
}

fn mode_bit_from_number<const N: usize>(value: u16, modes: [u16; N]) -> u16 {
    modes
        .iter()
        .position(|mode| *mode == value)
        .map_or(0, |i| 1 << i)
}

pub(crate) fn decimal(text: &[u8]) -> Option<u16> {
    if text.is_empty() || !text.iter().all(u8::is_ascii_digit) {
        return None;
    }
    std::str::from_utf8(text).ok()?.parse().ok()
}

pub(crate) fn utf8_continuations(b: u8) -> u8 {
    match b {
        0xc2..=0xdf => 1,
        0xe0..=0xef => 2,
        0xf0..=0xf4 => 3,
        _ => 0,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tracks_split_modes_without_treating_string_payloads_as_controls() {
        let mut tracker = Tracker::new(Modes {
            known: true,
            ..Modes::default()
        });
        for chunk in [
            b"plain\x1b[?100".as_slice(),
            b"0;1006h\x1b]2;utf\xe2\x98",
            b"\x83[?1016h\x07",
        ] {
            tracker.feed(chunk, None, |_, _, _| Ok(())).unwrap();
        }
        assert_eq!(tracker.modes.supported(), Some(1000));
        tracker
            .feed(b"\x1b[?1002h", None, |_, _, _| Ok(()))
            .unwrap();
        assert_eq!(tracker.modes.supported(), None);
        assert!(tracker.modes.ambiguous());
        tracker.feed(b"\x1b[?1016", None, |_, _, _| Ok(())).unwrap();
        assert_eq!(tracker.snapshot()["known"], false);
        tracker.feed(b"h", None, |_, _, _| Ok(())).unwrap();
        assert_eq!(tracker.snapshot()["encoding"], 10);
    }

    #[test]
    fn rejected_mode_is_never_emitted_even_after_cancelling_partial_control() {
        let mut tracker = Tracker::new(Modes {
            known: true,
            ..Modes::default()
        });
        let mut out = Vec::new();
        tracker
            .feed(b"worker\x1b[12", Some(&mut out), |_, _, _| Ok(()))
            .unwrap();
        assert_eq!(out, b"worker");
        assert!(
            tracker
                .feed(b"\x1b[?10\x0816h", Some(&mut out), |modes, _, _| {
                    assert!(!modes.known);
                    Err(io::Error::other("conflicting encoding"))
                })
                .is_err()
        );
        assert_eq!(out, b"worker");
    }
}
