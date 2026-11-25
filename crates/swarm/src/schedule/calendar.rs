use super::{Calendar, Result};
use jiff::{
    Timestamp,
    civil::{Date, Time},
    tz::{AmbiguousOffset, TimeZone},
};
use sha2::{Digest, Sha256};
use std::{fs, path::Path};

const DAYS: [&str; 7] = [
    "Sunday",
    "Monday",
    "Tuesday",
    "Wednesday",
    "Thursday",
    "Friday",
    "Saturday",
];
const MONTHS: [&str; 12] = [
    "jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec",
];
const LOW: [u8; 5] = [0, 0, 1, 1, 0];
const HIGH: [u8; 5] = [59, 23, 31, 12, 7];

#[derive(Clone)]
pub(super) struct Zone {
    pub id: Option<String>,
    pub tz: TimeZone,
}

impl Zone {
    pub fn host(path: &Path) -> Result<Self> {
        let target = fs::canonicalize(path).map_err(|e| format!("resolve host time zone: {e}"))?;
        let data = fs::read(&target)?;
        let name = target
            .to_str()
            .and_then(|p| p.split_once("/zoneinfo/").map(|(_, name)| name));
        Ok(Self {
            tz: TimeZone::tzif(name.unwrap_or("Host local time"), &data)?,
            id: Some(name.map_or_else(|| format!("{:x}", Sha256::digest(&data)), str::to_owned)),
        })
    }
}

pub(super) struct Rule {
    pub zone: Zone,
    once: Option<Date>,
    fields: [u64; 5],
    day_or: bool,
}

impl Rule {
    pub fn compile(cal: &Calendar, host: &Path) -> Result<Self> {
        let zone = match cal.time_zone.as_str() {
            "host" => Zone::host(host)?,
            "" | "Local" => {
                return Err("select the host time zone or a named IANA time zone".into());
            }
            name => Zone {
                id: None,
                tz: TimeZone::get(name)?,
            },
        };
        let mut rule = Self {
            zone,
            once: None,
            fields: std::array::from_fn(|i| mask(LOW[i], HIGH[i])),
            day_or: false,
        };
        rule.fields[4] &= 0x7f;
        if cal.kind != "cron" {
            if !digits(&cal.time, b':', &[2, 2]) {
                return Err("time must use HH:MM".into());
            }
            let time: Time = cal.time.parse().map_err(|_| "time must use HH:MM")?;
            rule.fields[0] = 1 << time.minute();
            rule.fields[1] = 1 << time.hour();
        }
        match cal.kind.as_str() {
            "once" => {
                if !digits(&cal.date, b'-', &[4, 2, 2]) {
                    return Err("date must use YYYY-MM-DD".into());
                }
                rule.once = Some(cal.date.parse().map_err(|_| "date must use YYYY-MM-DD")?);
            }
            "daily" => {}
            "weekdays" => rule.fields[4] = 0b0111110,
            "weekly" => {
                let i = DAYS
                    .iter()
                    .position(|day| {
                        day.eq_ignore_ascii_case(&cal.weekday)
                            || day[..3].eq_ignore_ascii_case(&cal.weekday)
                    })
                    .ok_or("select a weekday")?;
                rule.fields[4] = 1 << i;
            }
            "cron" => {
                let fields: Vec<_> = cal.cron.split_whitespace().collect();
                if fields.len() != 5 {
                    return Err("cron must contain five fields".into());
                }
                for (i, field) in fields.iter().enumerate() {
                    rule.fields[i] =
                        cron_field(field, i).map_err(|e| format!("cron field {}: {e}", i + 1))?;
                }
                rule.day_or = !fields[2].starts_with('*') && !fields[4].starts_with('*');
                rule.fields[4] = (rule.fields[4] | (rule.fields[4] >> 7)) & 0x7f;
            }
            other => return Err(format!("unknown schedule kind {other:?}").into()),
        }
        Ok(rule)
    }

    pub fn find(&self, bound: Timestamp, previous: bool) -> Option<Timestamp> {
        if let Some(date) = self.once {
            return self.on_day(date, bound, previous);
        }
        let mut date = bound.to_zoned(self.zone.tz.clone()).date();
        for _ in 0..=8 * 366 {
            if self.matches_day(date)
                && let Some(at) = self.on_day(date, bound, previous)
            {
                return Some(at);
            }
            date = if previous {
                date.yesterday()
            } else {
                date.tomorrow()
            }
            .ok()?;
        }
        None
    }

    fn matches_day(&self, date: Date) -> bool {
        let dom = self.fields[2] & (1 << date.day()) != 0;
        let dow = self.fields[4] & (1 << date.weekday().to_sunday_zero_offset()) != 0;
        let month = self.fields[3] & (1 << date.month()) != 0;
        month && if self.day_or { dom || dow } else { dom && dow }
    }

    fn on_day(&self, date: Date, bound: Timestamp, previous: bool) -> Option<Timestamp> {
        let mut hours = self.fields[1];
        while let Some(hour) = take_bit(&mut hours, previous) {
            let mut minutes = self.fields[0];
            while let Some(minute) = take_bit(&mut minutes, previous) {
                let wall = date.at(hour, minute, 0, 0);
                let ambiguous = self.zone.tz.to_ambiguous_timestamp(wall);
                if matches!(ambiguous.offset(), AmbiguousOffset::Gap { .. }) {
                    continue;
                }
                if let Ok(at) = ambiguous.earlier()
                    && (at <= bound) == previous
                {
                    return Some(at);
                }
            }
        }
        None
    }
}

fn digits(text: &str, separator: u8, lengths: &[usize]) -> bool {
    text.as_bytes()
        .split(|b| *b == separator)
        .map(|part| part.iter().all(u8::is_ascii_digit).then_some(part.len()))
        .eq(lengths.iter().copied().map(Some))
}

fn mask(low: u8, high: u8) -> u64 {
    ((1 << (high + 1)) - 1) ^ ((1 << low) - 1)
}

fn take_bit(mask: &mut u64, reverse: bool) -> Option<i8> {
    if *mask == 0 {
        return None;
    }
    let bit = if reverse {
        63 - mask.leading_zeros()
    } else {
        mask.trailing_zeros()
    };
    *mask &= !(1 << bit);
    Some(bit as i8)
}

fn cron_field(text: &str, index: usize) -> Result<u64> {
    let (low, high) = (LOW[index], HIGH[index]);
    let mut field = 0;
    for part in text.split(',') {
        let (values, step) = part
            .split_once('/')
            .map_or((part, None), |(v, s)| (v, Some(s)));
        let stride = match step {
            None => 1,
            Some(s) => s
                .parse::<usize>()
                .ok()
                .filter(|n| (1..=usize::from(high - low + 1)).contains(n))
                .ok_or_else(|| format!("invalid step {s:?}"))?,
        };
        let get = |v| cron_value(v, index).ok_or_else(|| format!("invalid range {values:?}"));
        let (start, end) = if values == "*" {
            (low, high)
        } else if let Some((a, b)) = values.split_once('-') {
            (get(a)?, get(b)?)
        } else {
            let start = get(values)?;
            (start, if step.is_some() { high } else { start })
        };
        if start < low || end > high || start > end {
            return Err(format!("invalid range {values:?}").into());
        }
        for v in (start..=end).step_by(stride) {
            field |= 1 << v;
        }
    }
    Ok(field)
}

fn cron_value(value: &str, index: usize) -> Option<u8> {
    value.parse().ok().or_else(|| match index {
        3 => MONTHS
            .iter()
            .position(|m| m.eq_ignore_ascii_case(value))
            .map(|n| n as u8 + 1),
        4 => DAYS
            .iter()
            .position(|d| d[..3].eq_ignore_ascii_case(value))
            .map(|n| n as u8),
        _ => None,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn next(zone: &str, pattern: &str, bound: &str) -> String {
        Rule::compile(
            &Calendar {
                kind: "cron".into(),
                time_zone: zone.into(),
                cron: pattern.into(),
                ..Default::default()
            },
            Path::new("/dev/null"),
        )
        .unwrap()
        .find(bound.parse().unwrap(), false)
        .unwrap()
        .to_string()
    }

    #[test]
    fn daylight_transitions_use_first_wall_instant_and_skip_gaps() {
        assert_eq!(
            next("America/New_York", "30 2 * * *", "2026-03-08T00:00:00Z"),
            "2026-03-09T06:30:00Z"
        );
        assert_eq!(
            next("America/New_York", "30 1 * * *", "2026-11-01T04:00:00Z"),
            "2026-11-01T05:30:00Z"
        );
        assert_eq!(
            next("America/New_York", "30 1 * * *", "2026-11-01T05:30:00Z"),
            "2026-11-02T06:30:00Z"
        );
        assert_eq!(
            next("Australia/Lord_Howe", "45 1 * * *", "2026-04-04T12:00:00Z"),
            "2026-04-04T14:45:00Z"
        );
    }

    #[test]
    fn cron_day_or_and_century_leap_gap() {
        assert_eq!(
            next("UTC", "0 1 11 * MON", "2026-03-08T00:00:00Z"),
            "2026-03-09T01:00:00Z"
        );
        assert_eq!(
            next("UTC", "0 0 29 FEB *", "2096-02-29T00:00:00Z"),
            "2104-02-29T00:00:00Z"
        );
        assert_eq!(
            next("UTC", "0 0 * NOV 7", "2026-11-01T00:00:00Z"),
            "2026-11-08T00:00:00Z"
        );
    }
}
