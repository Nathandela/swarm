pub(crate) mod native;
pub(crate) mod transfer;

use crate::ipc::field;
use serde_json::{Value, json};
use std::io;

pub(crate) const OPTIONS: &[&str] = &[
    "profile",
    "provider",
    "model",
    "reasoning",
    "toolsets",
    "skills",
    "yolo",
];

fn release(value: &str) -> Option<[u32; 3]> {
    let mut parts = value.split('.');
    let mut numbers = [0; 3];
    for number in &mut numbers {
        let part = parts.next()?;
        if part.len() > 9 || !part.bytes().all(|c| c.is_ascii_digit()) {
            return None;
        }
        *number = part.parse().ok()?;
    }
    parts.next().is_none().then_some(numbers)
}

pub(crate) fn version(output: &str) -> io::Result<&str> {
    let version = output.lines().find_map(|line| {
        let token = line
            .trim()
            .strip_prefix("Hermes Agent ")?
            .split_whitespace()
            .next()?;
        let token = token.trim_matches(|ch| "()[]{}<>,;:".contains(ch));
        let version = token.strip_prefix('v').unwrap_or(token);
        release(version).map(|parts| (version, parts))
    });
    match version {
        Some((version, parts)) if ([0, 20, 6]..=[9999, 0, 0]).contains(&parts) => Ok(version),
        _ => Err(io::Error::other(
            "Hermes Agent requires a stable supported version >= 0.20.6",
        )),
    }
}

pub(crate) fn native_version(version: &str) -> bool {
    release(version).is_some_and(|parts| parts >= [0, 22, 5])
}

fn lower_hex(input: &str) -> bool {
    input
        .bytes()
        .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
}

pub(crate) fn native_id(id: &str) -> bool {
    id.strip_prefix("local-")
        .is_some_and(|hex| hex.len() == 64 && lower_hex(hex))
}

pub(crate) fn classic_id(id: &str) -> bool {
    if id.len() != 22 || !id.is_ascii() || &id[15..16] != "_" || !lower_hex(&id[16..]) {
        return false;
    }
    let Ok(format) =
        time::format_description::parse_borrowed::<2>("[year][month][day]_[hour][minute][second]")
    else {
        return false;
    };
    time::PrimitiveDateTime::parse(&id[..15], &format).is_ok()
}

pub(crate) fn prefix(options: &Value) -> Vec<String> {
    let profile = field(options, "profile");
    if profile.is_empty() {
        vec![]
    } else {
        vec!["--profile".into(), profile.into()]
    }
}

pub(crate) fn argv(request: &Value) -> io::Result<Vec<String>> {
    let options = &request["options"];
    let mut argv = prefix(options);
    argv.extend(["chat".into(), "--cli".into()]);
    let id = field(request, "resume_conversation_id");
    let prompt = field(request, "initial_prompt");
    if !id.is_empty() {
        if !classic_id(id) || !prompt.is_empty() {
            return Err(io::Error::other(
                "invalid classic Hermes resume identity or initial prompt",
            ));
        }
        argv.extend(["--resume".into(), id.into(), "--no-restore-cwd".into()]);
    }
    for key in ["provider", "model", "reasoning", "toolsets", "skills"] {
        let value = field(options, key);
        if !value.is_empty() {
            argv.extend([format!("--{key}"), value.into()]);
        }
    }
    if options["yolo"] == "true" {
        argv.push("--yolo".into());
    }
    if !prompt.is_empty() {
        argv.extend(["-q".into(), prompt.into()]);
    }
    Ok(argv)
}

fn id_prefix(text: &str) -> Option<&str> {
    let id = text.get(..22)?;
    let next = *text.as_bytes().get(22)?;
    (classic_id(id) && !next.is_ascii_alphanumeric() && next != b'_').then_some(id)
}

pub(crate) fn classic_observation(tail: &[u8], grid: &[String]) -> Option<String> {
    let raw = String::from_utf8_lossy(tail);
    let lines: Vec<_> = raw.lines().collect();
    let exit = exit_identity(&lines);
    if exit.is_some() {
        return exit.map(str::to_owned);
    }
    let raw_rows: Vec<_> = raw.split_inclusive('\n').map(str::to_owned).collect();
    match banner(&raw_rows) {
        Ok(Some(id)) => Some(id.into()),
        Ok(None) => banner(grid).ok().flatten().map(str::to_owned),
        Err(()) => None,
    }
}

fn exit_identity<'a>(lines: &[&'a str]) -> Option<&'a str> {
    lines.windows(2).enumerate().rev().find_map(|(i, block)| {
        if block[0].trim() != "Resume this session with:" {
            return None;
        }
        let command = block[1].trim_start().strip_prefix("hermes --resume ")?;
        let id = if i + 2 < lines.len() && classic_id(command) {
            command
        } else {
            id_prefix(command)?
        };
        let summary = &lines[i + 2..lines.len().min(i + 10)];
        let start = summary.iter().position(|row| row.starts_with("Session:"))?;
        if summary[start].strip_prefix("Session:")?.trim() != id {
            return None;
        }
        let mut row = start + 1;
        if summary
            .get(row)
            .is_some_and(|line| line.starts_with("Title: "))
        {
            row += 1;
        }
        let duration = summary.get(row)?.strip_prefix("Duration: ")?.trim();
        if !valid_duration(duration) {
            return None;
        }
        let text = summary.get(row + 1)?.strip_prefix("Messages: ")?.trim();
        let (total, counts) = text.split_once(" (")?;
        let (user, tools) = counts.strip_suffix(" tool calls)")?.split_once(" user, ")?;
        [total, user, tools]
            .iter()
            .all(|count| decimal(count))
            .then_some(id)
    })
}

fn decimal(number: &str) -> bool {
    !number.is_empty()
        && (number.len() == 1 || !number.starts_with('0'))
        && number.bytes().all(|ch| ch.is_ascii_digit())
}

fn valid_duration(duration: &str) -> bool {
    let parts: Vec<_> = duration.split_whitespace().collect();
    let unit = |part: &str, suffix: char, positive: bool| {
        part.strip_suffix(suffix).is_some_and(|value| {
            decimal(value)
                && (!positive || value != "0")
                && (suffix == 'h' || value.parse::<u8>().is_ok_and(|n| n < 60))
        })
    };
    match parts.as_slice() {
        [s] => unit(s, 's', false),
        [m, s] => unit(m, 'm', true) && unit(s, 's', false),
        [h, m, s] => unit(h, 'h', true) && unit(m, 'm', false) && unit(s, 's', false),
        _ => false,
    }
}

fn banner(rows: &[String]) -> Result<Option<&str>, ()> {
    let mut found = None;
    for (index, row) in rows.iter().enumerate() {
        let Some(candidate) = row
            .trim_start()
            .strip_prefix('│')
            .and_then(|row| row.trim_start().strip_prefix("Session:"))
            .and_then(|row| id_prefix(row.trim_start()))
        else {
            continue;
        };
        let branded = rows[..index]
            .iter()
            .rev()
            .take(4)
            .map_while(|row| row.trim_start().strip_prefix('│'))
            .any(|row| row.contains("Nous Research") || row.contains("no model configured"));
        if branded {
            if found.is_some_and(|prior| prior != candidate) {
                return Err(());
            }
            found = Some(candidate);
        }
    }
    Ok(found)
}

fn single(token: &str, alphabet: &str) -> bool {
    token.chars().count() == 1 && alphabet.contains(token)
}

fn profile(token: &str) -> bool {
    !token.is_empty()
        && token.len() <= 64
        && token.bytes().enumerate().all(|(i, c)| {
            c.is_ascii_lowercase() || c.is_ascii_digit() || i > 0 && matches!(c, b'_' | b'-')
        })
}

fn border(row: &str) -> bool {
    row.chars().count() >= 6 && row.chars().all(|c| "─━═╌╍".contains(c))
}

fn nav(row: &str, expected: &str) -> bool {
    row == expected
        || row.strip_prefix(expected).is_some_and(|tail| {
            tail.trim()
                .strip_prefix('(')
                .and_then(|tail| tail.strip_suffix("s)"))
                .is_some_and(|n| !n.is_empty() && n.bytes().all(|c| c.is_ascii_digit()))
        })
}

pub(crate) fn grid(rows: &[String], cols: u16) -> Option<Value> {
    let last = rows.iter().rposition(|row| !row.trim().is_empty())?;
    let compact = cols < 64;
    let first = last.saturating_sub(11);
    let clip = |row: &str, full: &str| {
        row == full
            || full.starts_with(row) && row.chars().count() >= usize::from(cols.saturating_sub(1))
    };
    let prompt = |word| single(word, "❯>$#›»→");
    let spinner = |word| single(word, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏");
    let status =
        |turn, interaction| json!({"Process":"running","Turn":turn,"Interaction":interaction});
    for index in first..=last {
        let row = rows[index].trim();
        let mut words = row.split_whitespace();
        let Some(head) = words.next() else { continue };
        let next = words.next().unwrap_or_default();
        if !rows[..index]
            .iter()
            .rev()
            .map(|row| row.trim())
            .find(|row| !row.is_empty())
            .is_some_and(border)
            || (compact && index != last)
            || (!compact
                && (index == last
                    || !rows[index + 1..=last]
                        .iter()
                        .all(|row| row.trim().is_empty() || border(row.trim()))))
        {
            continue;
        }
        let above = &rows[first..index];
        let select = "↑/↓ to select, Enter to confirm";
        let dialogs = [
            (
                "⚠",
                "⚠ type 1/2/3, or use ↑/↓ then Enter",
                [select, "type 1/2/3, or ↑/↓ to select, Enter to confirm"],
                "permission",
            ),
            (
                "?",
                "?",
                [select, "↑/↓ to select, Enter to lock, Tab next question"],
                "prompt",
            ),
            (
                "✎",
                "✎ type your answer here and press Enter",
                ["type your answer and press Enter"; 2],
                "prompt",
            ),
        ];
        for (icon, full, navigation, interaction) in dialogs {
            if head == icon
                && (if compact {
                    clip(row, icon) || clip(row, full)
                } else {
                    prompt(next)
                })
                && above
                    .iter()
                    .any(|row| navigation.iter().any(|hint| nav(row.trim(), hint)))
            {
                return Some(status("idle", interaction));
            }
        }
        let busy = "msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel";
        if head == "⚕"
            && if compact {
                clip(row, "⚕") || clip(row, &format!("⚕ {busy}"))
            } else {
                prompt(next) && row == format!("⚕ {next} {busy}")
            }
        {
            return Some(status("active", "none"));
        }
        if spinner(head)
            && if compact {
                next.is_empty() || spinner(next)
            } else {
                prompt(next)
            }
            && above.iter().any(|row| {
                let Some((head, hint)) = row.trim().split_once(' ') else {
                    return false;
                };
                spinner(head)
                    && hint.starts_with("command in progress · ")
                    && [
                        "command in progress · input temporarily disabled",
                        "command in progress · input stays active; Enter queues",
                    ]
                    .iter()
                    .any(|full| hint == *full || (compact && full.starts_with(hint)))
            })
        {
            return Some(status("active", "none"));
        }
        if prompt(head) || profile(head) && prompt(next) {
            return Some(status("idle", "none"));
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classic_version_argv_and_calendar_identity() {
        assert!(version("Hermes Agent v0.20.5").is_err());
        assert!(version("Hermes Agent v0.22.5-dev").is_err());
        assert_eq!(
            version("Hermes Agent v0.22.5 (2026.8.27)").unwrap(),
            "0.22.5"
        );
        assert!(classic_id("20240229_000000_abcdef"));
        assert!(!classic_id("20230229_000000_abcdef"));
        assert!(!classic_id("20240229_240000_abcdef"));
        let req =
            json!({"options":{"profile":"chosen","yolo":"true"},"initial_prompt":"-q\nhello"});
        assert_eq!(
            argv(&req).unwrap(),
            [
                "--profile",
                "chosen",
                "chat",
                "--cli",
                "--yolo",
                "-q",
                "-q\nhello"
            ]
        );
        assert_eq!(
            argv(&json!({"resume_conversation_id":"20240229_000000_abcdef"})).unwrap(),
            [
                "chat",
                "--cli",
                "--resume",
                "20240229_000000_abcdef",
                "--no-restore-cwd"
            ]
        );
    }

    #[test]
    fn classic_needs_corroboration_and_terminal_geometry() {
        let rows = |lines: &[&str]| {
            lines
                .iter()
                .map(|line| line.to_string())
                .collect::<Vec<_>>()
        };
        let id = "20240229_000000_abcdef";
        assert_eq!(
            classic_observation(
                &[],
                &rows(&["│ Nous Research │", "│ Session: 20240229_000000_abcdef │"])
            )
            .as_deref(),
            Some(id)
        );
        assert!(
            classic_observation(
                &[],
                &rows(&["│ Nous Research │", "│ Session: 20240229_000000_abcdef"])
            )
            .is_none()
        );
        let exit = format!(
            "Resume this session with:\nhermes --resume {id}\nSession: {id}\nDuration: 12s\nMessages: 5 (1 user, 2 tool calls)\n"
        );
        assert_eq!(
            classic_observation(exit.as_bytes(), &[]).as_deref(),
            Some(id)
        );
        assert!(
            classic_observation(
                exit.replace("Duration: 12s", "Duration: arbitrary")
                    .as_bytes(),
                &[]
            )
            .is_none()
        );
        assert!(grid(&rows(&["──────", "❯", "──────", "more output"]), 80).is_none());
        assert!(grid(&rows(&["──────", "──❯"]), 63).is_none());
        assert_eq!(
            grid(
                &rows(&[
                    "↑/↓ to select, Enter to confirm (6s)",
                    "──────",
                    "⚠ ❯",
                    "──────"
                ]),
                80
            )
            .unwrap()["Interaction"],
            "permission"
        );
    }
}
