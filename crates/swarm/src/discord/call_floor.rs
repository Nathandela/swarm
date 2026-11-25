//! Bounded isolated utterances and personal interruption on raw 20 ms media frames.
use std::time::{Duration, Instant};

pub(super) const FRAME_BYTES: usize = 960;
const TEAM_QUIET: Duration = Duration::from_millis(420);
const PERSONAL_QUIET: Duration = Duration::from_millis(850);
const MAX_UTTERANCE: usize = 1000 * FRAME_BYTES;

pub(super) struct Floor {
    team: bool,
    turn: Option<Utterance>,
    occupied: usize,
    invalid: bool,
    interrupted: bool,
}

pub(super) struct Utterance {
    pub(super) speaker: String,
    pub(super) last_voice: Instant,
    pub(super) pcm: Vec<u8>,
    voiced: usize,
}

pub(super) struct Ingress {
    pub(super) complete: Option<Utterance>,
    pub(super) interrupt: bool,
    pub(super) voiced: bool,
}

impl Floor {
    pub(super) fn new(team: bool) -> Self {
        Self {
            team,
            turn: None,
            occupied: 0,
            invalid: false,
            interrupted: false,
        }
    }

    pub(super) fn quiet(&self, now: Instant) -> bool {
        let delay = if self.team {
            TEAM_QUIET
        } else {
            PERSONAL_QUIET
        };
        self.turn
            .as_ref()
            .is_none_or(|turn| now >= turn.last_voice + delay)
    }

    pub(super) fn settle(&mut self, now: Instant) -> Option<Utterance> {
        if !self.quiet(now) {
            return None;
        }
        let invalid = std::mem::take(&mut self.invalid);
        self.occupied = 0;
        self.interrupted = false;
        self.turn.take().filter(|turn| !invalid && turn.voiced >= 5)
    }

    pub(super) fn push(&mut self, speaker: &str, pcm: &[u8; FRAME_BYTES], now: Instant) -> Ingress {
        let complete = self.settle(now);
        let signal = signal_samples(pcm);
        let voiced = signal > 0;
        if voiced && self.turn.is_none() {
            self.turn = Some(Utterance {
                speaker: speaker.into(),
                last_voice: now,
                pcm: Vec::new(),
                voiced: 0,
            });
        }
        let mut interrupt = false;
        if let Some(turn) = &mut self.turn {
            if voiced {
                turn.last_voice = now;
                turn.voiced += 1;
                self.invalid |= turn.speaker != speaker;
                self.occupied = self.occupied.saturating_add(signal);
                if !self.team && self.occupied > FRAME_BYTES / 2 && !self.interrupted {
                    self.interrupted = true;
                    interrupt = true;
                }
            } else {
                self.occupied = 0;
            }
            if turn.speaker == speaker && !self.invalid {
                if turn.pcm.len() + FRAME_BYTES > MAX_UTTERANCE {
                    self.invalid = true;
                    turn.pcm.clear();
                } else {
                    turn.pcm.extend_from_slice(pcm);
                }
            }
        }
        Ingress {
            complete,
            interrupt,
            voiced,
        }
    }
}

// One millisecond bins bound the contribution of a sparse pulse independently of frame alignment.
fn signal_samples(pcm: &[u8; FRAME_BYTES]) -> usize {
    let (mut hits, mut occupied, mut previous) = (0, 0, None);
    for (index, sample) in pcm.as_chunks::<2>().0.iter().enumerate() {
        if i16::from_le_bytes([sample[0], sample[1]]).unsigned_abs() > 420 {
            hits += 1;
            occupied += previous
                .filter(|prev| index / 24 == prev / 24)
                .map_or(1, |prev| index - prev);
            previous = Some(index);
        }
    }
    if hits < 24 { 0 } else { occupied }
}

pub(super) fn authorized_words(text: &str, team: bool) -> Option<&str> {
    let text = text.trim();
    let text = if team { addressed_words(text)? } else { text };
    (!text.is_empty() && text.len() <= 4096 && !text.contains('\0')).then_some(text)
}

fn addressed_words(text: &str) -> Option<&str> {
    const PUNCTUATION: &str = ",.:!?;—–";
    let delimiter = |ch: char| ch.is_whitespace() || PUNCTUATION.contains(ch);
    let mut words = text;
    let mut greeted = false;
    if text
        .get(..3)
        .is_some_and(|prefix| prefix.eq_ignore_ascii_case("hey"))
        && text[3..].starts_with(delimiter)
    {
        words = text[3..].trim_start_matches(delimiter);
        greeted = true;
    }
    let address = "dispatcher";
    if !words
        .get(..address.len())
        .is_some_and(|prefix| prefix.eq_ignore_ascii_case(address))
    {
        return None;
    }
    let tail = &words[address.len()..];
    if !tail.starts_with(delimiter)
        || !greeted && !tail.trim_start().starts_with(|ch| PUNCTUATION.contains(ch))
    {
        return None;
    }
    Some(tail.trim_start_matches(delimiter))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pulse(start: usize, end: usize) -> [u8; FRAME_BYTES] {
        let mut pcm = [0; FRAME_BYTES];
        for sample in pcm
            .as_chunks_mut::<2>()
            .0
            .iter_mut()
            .skip(start)
            .take(end - start)
        {
            sample.copy_from_slice(&1000_i16.to_le_bytes());
        }
        pcm
    }

    #[test]
    fn clicks_do_not_interrupt_for_any_subframe_alignment() {
        let now = Instant::now();
        let period = Duration::from_millis(20);
        for offset in 0..480 {
            let mut floor = Floor::new(false);
            assert!(!floor.push("owner", &pulse(offset, 480), now).interrupt);
            assert!(
                !floor
                    .push("owner", &pulse(0, offset), now + period)
                    .interrupt
            );
            assert!(floor.settle(now + PERSONAL_QUIET + period).is_none());
        }
        let mut floor = Floor::new(false);
        assert!(floor.push("owner", &pulse(0, 480), now).voiced);
        assert!(floor.push("owner", &pulse(0, 480), now + period).interrupt);
        assert!(
            !floor
                .push("owner", &pulse(0, 480), now + period * 2)
                .interrupt
        );
    }

    #[test]
    fn overlapping_turns_are_discarded_and_address_is_explicit() {
        let now = Instant::now();
        let mut floor = Floor::new(true);
        let mut send = |speaker, start: u64| {
            for n in 0..5 {
                let at = now + Duration::from_millis(start + n * 20);
                let progress = floor.push(speaker, &pulse(0, 480), at);
                assert!(progress.voiced && !progress.interrupt && progress.complete.is_none());
            }
        };
        send("first", 0);
        send("second", 100);
        assert!(floor.settle(now + Duration::from_secs(1)).is_none());
        for n in 0..5 {
            floor.push(
                "second",
                &pulse(0, 480),
                now + Duration::from_millis(1100 + n * 20),
            );
        }
        let turn = floor.settle(now + Duration::from_millis(1600)).unwrap();
        assert_eq!(turn.speaker, "second");
        assert_eq!(turn.pcm.len(), FRAME_BYTES * 5);
        for text in [
            "Dispatcher is useful",
            "they said hey dispatcher, help",
            "Dispatcherish, check",
        ] {
            assert_eq!(authorized_words(text, true), None);
        }
        assert_eq!(
            authorized_words(" Hey dispatcher — check the build. ", true),
            Some("check the build.")
        );
        assert_eq!(
            authorized_words("Dispatcher: check the build", true),
            Some("check the build")
        );
        assert_eq!(
            authorized_words("check the build", false),
            Some("check the build")
        );
    }
}
