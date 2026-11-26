//! Replies from the worker's virtual terminal. An attached terminal may see the
//! same queries, so consume its matching DECRPM once the emulator has accepted
//! that query. Other terminal input, including pasted text, stays transparent.
use std::collections::VecDeque;
use std::time::{Duration, Instant};

use crate::terminal_mouse::decimal;

const OUTSTANDING: usize = 256;
const RESPONSE_WINDOW: Duration = Duration::from_secs(2);
const PREFIX_WAIT: Duration = Duration::from_millis(60);
const START_PASTE: &[u8] = b"\x1b[200~";
const END_PASTE: &[u8] = b"\x1b[201~";

#[derive(Default)]
pub(super) struct Debt(VecDeque<(u16, Instant)>);

impl Debt {
    pub(super) fn register(&mut self, mode: u16, now: Instant) {
        self.expire(now);
        if self.0.len() == OUTSTANDING {
            self.0.pop_front();
        }
        self.0.push_back((mode, now + RESPONSE_WINDOW));
    }

    fn expire(&mut self, now: Instant) {
        while self.0.front().is_some_and(|(_, until)| now >= *until) {
            self.0.pop_front();
        }
    }

    fn pay(&mut self, mode: u16) -> bool {
        self.0
            .iter()
            .position(|(expected, _)| *expected == mode)
            .and_then(|index| self.0.remove(index))
            .is_some()
    }
}

#[derive(Default)]
pub(super) struct Filter {
    pending: Vec<u8>,
    began: Option<Instant>,
    paste: bool,
    paste_prefix: usize,
    utf8_left: u8,
}

impl Filter {
    pub(super) fn delay(&self, now: Instant) -> Option<Duration> {
        self.began
            .map(|began| PREFIX_WAIT.saturating_sub(now.duration_since(began)))
    }

    pub(super) fn flush(&mut self) -> Vec<u8> {
        self.began = None;
        std::mem::take(&mut self.pending)
    }

    pub(super) fn feed(&mut self, debt: &mut Debt, input: &[u8], now: Instant) -> Vec<u8> {
        let mut output = if self.delay(now).is_some_and(|delay| delay.is_zero()) {
            self.flush()
        } else {
            Vec::with_capacity(input.len())
        };
        debt.expire(now);
        for &byte in input {
            let pasted = self.paste;
            self.observe_paste(byte);
            let continuation = self.utf8_left > 0 && (0x80..=0xbf).contains(&byte);
            self.utf8_left = if continuation {
                self.utf8_left - 1
            } else {
                crate::terminal_mouse::utf8_continuations(byte)
            };
            if pasted || debt.0.is_empty() {
                output.extend(self.flush());
                output.push(byte);
                continue;
            }
            if !continuation && matches!(byte, 0x1b | 0x9b) {
                output.extend(self.flush());
                self.began = Some(now);
                self.pending.push(byte);
                continue;
            }
            if self.pending.is_empty() {
                output.push(byte);
                continue;
            }
            self.pending.push(byte);
            match classify(&self.pending) {
                Report::Prefix => {}
                Report::Reply(mode) if debt.pay(mode) => {
                    self.pending.clear();
                    self.began = None;
                }
                _ => output.extend(self.flush()),
            }
        }
        output
    }

    fn observe_paste(&mut self, byte: u8) {
        let fence = if self.paste { END_PASTE } else { START_PASTE };
        if byte == fence[self.paste_prefix] {
            self.paste_prefix += 1;
            if self.paste_prefix == fence.len() {
                self.paste = !self.paste;
                self.paste_prefix = 0;
            }
        } else {
            self.paste_prefix = usize::from(byte == fence[0]);
        }
    }
}

enum Report {
    Plain,
    Prefix,
    Reply(u16),
}

fn classify(frame: &[u8]) -> Report {
    let params = match frame {
        b"\x1b" | b"\x1b[" | b"\x9b" => return Report::Prefix,
        _ => frame
            .strip_prefix(b"\x1b[?")
            .or_else(|| frame.strip_prefix(b"\x9b?")),
    };
    let Some(params) = params else {
        return Report::Plain;
    };
    let Some(split) = params.iter().position(|b| *b == b';') else {
        return if params.len() <= 4 && params.iter().all(u8::is_ascii_digit) {
            Report::Prefix
        } else {
            Report::Plain
        };
    };
    let Some(mode) = decimal(&params[..split]) else {
        return Report::Plain;
    };
    match &params[split + 1..] {
        [] | [b'0'..=b'4'] | [b'0'..=b'4', b'$'] => Report::Prefix,
        [b'0'..=b'4', b'$', b'y'] => Report::Reply(mode),
        _ => Report::Plain,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filters_one_fragmented_reply_and_passes_paste_unsolicited_and_expired_reply() {
        let mut filter = Filter::default();
        let mut debt = Debt::default();
        let now = Instant::now();
        debt.register(1000, now);
        assert_eq!(filter.feed(&mut debt, b"one\x1b[?1000;", now), b"one");
        assert_eq!(filter.feed(&mut debt, b"1$ytwo", now), b"two");
        assert_eq!(
            filter.feed(&mut debt, b"\x1b[?1000;1$y", now),
            b"\x1b[?1000;1$y"
        );
        debt.register(1000, now);
        let paste = b"\x1b[200~\x1b[?1000;1$y\x1b[201~";
        assert_eq!(filter.feed(&mut debt, paste, now), paste);
        assert_eq!(
            filter.feed(&mut debt, b"\x1b[?1000;1$y", now + RESPONSE_WINDOW),
            b"\x1b[?1000;1$y"
        );
    }

    #[test]
    fn flushes_unfinished_input_and_preserves_utf8_continuations() {
        let mut filter = Filter::default();
        let mut debt = Debt::default();
        let now = Instant::now();
        debt.register(1000, now);
        assert_eq!(
            filter.feed(&mut debt, b"\xe2\x9b\x84", now),
            b"\xe2\x9b\x84"
        );
        assert_eq!(filter.feed(&mut debt, b"\x1b[?1000", now), b"");
        assert_eq!(filter.delay(now + PREFIX_WAIT), Some(Duration::ZERO));
        assert_eq!(
            filter.feed(&mut debt, b"tail", now + PREFIX_WAIT),
            b"\x1b[?1000tail"
        );
    }
}
