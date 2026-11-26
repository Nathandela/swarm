//! Ownership of the attach footer and its input. Worker output is observed before
//! writing so a conflicting mouse mode cannot reach the terminal while we own it.
use crate::terminal_mouse::{ENCODING, Modes, TRACKING, Tracker, decimal};
use serde_json::Value;
use std::io;

pub(super) const LABEL: &str = "[swarm (ctrl+q)]";
const RESET: &str = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l";

#[derive(Default)]
pub(super) struct Touch {
    tracker: Tracker,
    owned: bool,
    blocked: bool,
    discard: bool,
    tracking: u16,
    row: u16,
    native_buttons: u8,
    gesture: Option<(u16, bool)>,
    repaint: bool,
}

impl Touch {
    pub(super) fn from_snapshot(snapshot: &[u8]) -> Self {
        let value: Value = serde_json::from_slice(snapshot).unwrap_or_default();
        let modes = Modes::from_snapshot(&value);
        let blocked = !modes.known || modes.ambiguous() || modes.encoding & 8 != 0;
        Self {
            tracker: Tracker::new(modes),
            blocked,
            ..Self::default()
        }
    }

    pub(super) fn paint(&mut self, geometry: (u16, u16), output: &mut String) -> bool {
        use std::fmt::Write;
        let mode = if self.blocked {
            None
        } else {
            self.tracker.modes.supported()
        };
        let enabled = geometry.0 as usize >= LABEL.len() && geometry.1 >= 3 && mode.is_some();
        let next_tracking = mode.unwrap_or_default();
        if self.row != geometry.1 || self.tracking != next_tracking || self.owned != enabled {
            self.cancel_press();
        }
        if enabled && (!self.owned || self.repaint) {
            let outer = if next_tracking == 1003 { 1003 } else { 1002 };
            let _ = write!(output, "{RESET}\x1b[?{outer}h\x1b[?1006h");
        } else if !enabled && self.owned {
            restore_native(&self.tracker.modes, output);
        }
        self.discard = !enabled
            && (self.owned || self.discard)
            && (self.tracker.modes.tracking == 0 || self.tracker.modes.encoding & 2 == 0);
        self.tracking = next_tracking;
        self.owned = enabled;
        self.row = geometry.1;
        self.repaint = false;
        enabled
    }

    pub(super) fn cancel_press(&mut self) {
        if let Some((_, pressed)) = &mut self.gesture {
            *pressed = false;
        }
    }

    pub(super) fn retain_input(&self) -> bool {
        self.owned || self.discard || self.gesture.is_some()
    }

    pub(super) fn mouse(&mut self, frame: &[u8]) -> (bool, bool) {
        let Some((button, x, y, release)) = decode(frame) else {
            self.cancel_press();
            return (self.discard || (self.owned && self.tracking == 0), false);
        };
        if self.discard {
            return (true, false);
        }
        let target = self.owned && x <= LABEL.len() as u16 && y == self.row;
        let code = button & 3;
        let stationary = code < 3 && button & 96 == 0;
        if self.native_buttons != 0 {
            if stationary {
                if release {
                    self.native_buttons &= !(1 << code);
                } else {
                    self.native_buttons |= 1 << code;
                }
            }
            return (self.tracking == 1000 && button & 32 != 0, false);
        }
        if let Some((owned, pressed)) = self.gesture {
            let matching = release && code == owned;
            self.gesture = (!matching).then_some((owned, false));
            return (true, matching && button == 0 && target && pressed);
        }
        if !self.owned {
            return (false, false);
        }
        if !release && stationary {
            if target {
                self.gesture = Some((code, button == 0));
                return (true, false);
            }
            if self.tracking != 0 {
                self.native_buttons = 1 << code;
            }
        }
        (
            target || self.tracking == 0 || self.tracking == 1000 && button & 32 != 0,
            false,
        )
    }

    pub(super) fn ground(&self) -> bool {
        self.tracker.ground()
    }

    pub(super) fn needs_paint(&self) -> bool {
        self.repaint
    }

    pub(super) fn observe(&mut self, bytes: &[u8], out: &mut Vec<u8>) -> io::Result<()> {
        self.tracker.feed(bytes, Some(out), |modes, change, out| {
            if change.query {
                self.blocked = true;
                if self.owned {
                    let mut native = String::new();
                    restore_native(modes, &mut native);
                    out.expect("attached output buffer").extend(native.as_bytes());
                    self.owned = false;
                    self.discard = modes.tracking == 0 || modes.encoding & 2 == 0;
                    if let Some((_, pressed)) = &mut self.gesture {
                        *pressed = false;
                    }
                }
            }
            if change.modes {
                if self.owned && (modes.ambiguous() || !modes.known
                    || modes.encoding & 8 != 0
                    || self.gesture.is_some() && modes.supported().is_none())
                {
                    return Err(io::Error::other("CLI button stopped: terminal mouse state changed; turn off CLI button in Options to continue"));
                }
                self.blocked |= !modes.known || modes.encoding & 8 != 0;
                if change.encoding {
                    self.native_buttons = 0;
                }
                if let Some((_, pressed)) = &mut self.gesture {
                    *pressed = false;
                }
            }
            self.repaint = true;
            Ok(())
        })
    }
}

fn restore_native(modes: &Modes, output: &mut String) {
    use std::fmt::Write;
    output.push_str(RESET);
    for (bits, list) in [
        (modes.tracking, TRACKING.as_slice()),
        (modes.encoding, ENCODING.as_slice()),
    ] {
        for (index, mode) in list.iter().enumerate() {
            if bits & (1 << index) != 0 {
                let _ = write!(output, "\x1b[?{mode}h");
            }
        }
    }
}

fn decode(frame: &[u8]) -> Option<(u16, u16, u16, bool)> {
    let body = frame
        .strip_prefix(b"\x1b[<")
        .or_else(|| frame.strip_prefix(b"\x9b<"))?;
    let (&final_byte, body) = body.split_last()?;
    if !matches!(final_byte, b'M' | b'm') {
        return None;
    }
    let mut fields = body.split(|b| *b == b';');
    let (b, x, y) = (
        decimal(fields.next()?)?,
        decimal(fields.next()?)?,
        decimal(fields.next()?)?,
    );
    (x > 0 && y > 0 && fields.next().is_none()).then_some((b, x, y, final_byte == b'm'))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn preserves_native_release_and_invalidates_owned_click_on_resize() {
        let mut touch =
            Touch::from_snapshot(br#"{"mouse":{"known":true,"tracking":2,"encoding":2}}"#);
        let mut paint = String::new();
        assert!(touch.paint((80, 24), &mut paint));
        assert!(paint.ends_with("\x1b[?1002h\x1b[?1006h"));
        assert_eq!(touch.mouse(b"\x1b[<0;3;3M"), (false, false));
        assert_eq!(touch.mouse(b"\x1b[<0;3;24m"), (false, false));
        assert_eq!(touch.mouse(b"\x1b[<0;3;24M"), (true, false));
        assert!(!touch.paint((10, 24), &mut paint));
        assert!(paint.ends_with("\x1b[?1000h\x1b[?1006h"));
        assert_eq!(touch.mouse(b"\x1b[<0;3;24m"), (true, false));
        assert!(touch.paint((80, 24), &mut paint));
        assert_eq!(touch.mouse(b"\x9b<0;3;24M"), (true, false));
        assert_eq!(touch.mouse(b"\x9b<0;3;24m"), (true, true));
    }

    #[test]
    fn refuses_unknown_or_ambiguous_native_mouse_and_conflicts_while_attached() {
        for snapshot in [
            br#"{}"#.as_slice(),
            br#"{"mouse":{"known":true,"tracking":10,"encoding":2}}"#,
            br#"{"mouse":{"known":true,"tracking":0,"encoding":8}}"#,
        ] {
            let mut touch = Touch::from_snapshot(snapshot);
            assert!(!touch.paint((80, 24), &mut String::new()));
            assert!(!touch.retain_input());
        }
        let mut touch = Touch::from_snapshot(br#"{"inputModes":{"known":true}}"#);
        let mut out = Vec::new();
        assert!(touch.paint((80, 24), &mut String::new()));
        touch.observe(b"bytes\x1b[?10", &mut out).unwrap();
        assert!(!touch.ground());
        assert!(touch.observe(b"16h", &mut out).is_err());
        assert_eq!(out, b"bytes");
    }
}
