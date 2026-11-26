use crate::ipc::field;
use serde_json::{Value, json};
use std::fs::OpenOptions;
use std::io::{self, Read};
use std::os::unix::fs::OpenOptionsExt;
use std::path::Path;

pub(crate) const EVENT: &str = "AgyStatusLine";

fn components(value: &str) -> Option<(u64, u64, u64)> {
    let mut parts = value.split('.').map(|part| {
        if part.is_empty() || !part.bytes().all(|b| b.is_ascii_digit()) {
            return None;
        }
        part.parse::<u64>().ok()
    });
    let version = (
        parts.next()??,
        parts.next()??,
        parts.next().unwrap_or(Some(0))?,
    );
    if parts.any(|part| part != Some(0)) {
        return None;
    }
    Some(version)
}

pub(crate) fn version(output: &str) -> io::Result<&str> {
    let (name, components) = output
        .split_whitespace()
        .map(|token| token.strip_prefix('v').unwrap_or(token))
        .find_map(|name| Some((name, components(name)?)))
        .ok_or_else(|| io::Error::other("unrecognized Antigravity CLI version"))?;
    if !((1, 1, 0)..=(9999, 0, 0)).contains(&components) {
        return Err(io::Error::other(
            "Antigravity CLI version outside supported range 1.1.0–9999.0.0",
        ));
    }
    Ok(name)
}

pub(crate) fn status_line(version: &str) -> bool {
    components(version).is_some_and(|(major, minor, _)| (major, minor) >= (1, 4))
}

pub(crate) fn supports_acp(version: &str) -> bool {
    version.bytes().filter(|&ch| ch == b'.').count() == 2
        && components(version).is_some_and(|version| version >= (1, 5, 12))
}

pub(crate) fn advertises_settings(help: &str) -> bool {
    help.lines()
        .any(|line| line.split_whitespace().next() == Some("--settings"))
}

pub(crate) fn argv(request: &Value) -> io::Result<Vec<String>> {
    let opts = &request["options"];
    let model = field(opts, "model");
    let mode = field(opts, "mode");
    let mut args = Vec::new();
    if !model.is_empty() && !model.starts_with('-') {
        args.extend(["--model".into(), model.into()]);
    }
    if !mode.is_empty() {
        if !matches!(mode, "accept-edits" | "auto" | "plan") {
            return Err(io::Error::other("invalid Antigravity execution mode"));
        }
        args.extend(["--mode".into(), mode.into()]);
    }
    if field(opts, "dangerously-skip-permissions") == "true" {
        args.push("--dangerously-skip-permissions".into());
    }
    let prompt = field(request, "initial_prompt");
    let resume = field(opts, "resume_conversation_id");
    if !resume.is_empty() {
        if !super::codex_receipt::canonical_thread(resume) || !prompt.is_empty() {
            return Err(io::Error::other(
                "invalid Antigravity resume identity or prompt",
            ));
        }
        args.extend(["--conversation".into(), resume.into()]);
    } else if !prompt.is_empty() {
        args.extend(["--prompt-interactive".into(), prompt.into()]);
    }
    Ok(args)
}

fn settings(home: &Path) -> Option<Value> {
    const LIMIT: u64 = 1024 * 1024;
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NONBLOCK)
        .open(home.join(".gemini/antigravity-cli/settings.json"))
        .ok()?;
    let meta = file.metadata().ok()?;
    if !meta.is_file() || meta.len() > LIMIT {
        return None;
    }
    let mut bytes = Vec::new();
    file.take(LIMIT + 1).read_to_end(&mut bytes).ok()?;
    if bytes.len() as u64 > LIMIT {
        return None;
    }
    serde_json::from_slice(bytes.strip_prefix(b"\xef\xbb\xbf").unwrap_or(&bytes)).ok()
}

pub(crate) fn status_line_args(home: Option<&Path>, binary: &Path) -> Vec<String> {
    let quote = |text: &str| format!("'{}'", text.replace('\'', "'\\''"));
    let Some(binary) = binary.to_str().filter(|_| binary.is_absolute()) else {
        return Vec::new();
    };
    let mut line = home
        .and_then(settings)
        .map(|value| value["statusLine"].clone())
        .unwrap_or(Value::Null);
    if line["enabled"] == false {
        return Vec::new();
    }
    let prefix = format!(
        "{} hook {EVENT}",
        quote(binary.trim_end_matches(" (deleted)"))
    );
    let original = field(&line, "command");
    if line["type"] == "command"
        && !original.is_empty()
        && !original.contains(" hook AgyStatusLine ")
    {
        line["command"] = json!(format!("{prefix} {}", quote(original)));
    } else {
        line = json!({
            "type": "command", "command": format!("{prefix} ''"),
            "enabled": true, "stack_with_default": true,
        });
    }
    vec!["--settings".into(), json!({"statusLine": line}).to_string()]
}

pub(crate) fn status_conversation(payload: &Value) -> io::Result<Option<&str>> {
    payload
        .get("conversation_id")
        .or_else(|| payload.get("session_id"))
        .map(|id| {
            id.as_str()
                .map(str::trim)
                .filter(|id| super::codex_receipt::canonical_thread(id))
                .ok_or_else(|| io::Error::other("invalid Antigravity status-line conversation"))
        })
        .transpose()
}

pub(crate) fn hook_status(state: &str) -> Option<Value> {
    let (turn, interaction) = match state {
        "working" | "tool_use" | "initializing" => ("active", "none"),
        "idle" => ("idle", "none"),
        "reviewing" => ("idle", "permission"),
        "authenticating" => ("idle", "prompt"),
        "error" => ("idle", "error"),
        _ => return None,
    };
    Some(json!({"Process": "running", "Turn": turn, "Interaction": interaction}))
}

pub(crate) fn terminal_status(screen: &vt100::Screen) -> Option<Value> {
    let (_, cols) = screen.size();
    let rows: Vec<_> = screen.rows(0, cols.get()).collect();
    let recent: Vec<_> = rows
        .iter()
        .enumerate()
        .rev()
        .filter(|(_, row)| !row.trim_end_matches(' ').is_empty())
        .take(6)
        .collect();
    let braille = |ch| ('\u{2800}'..='\u{28ff}').contains(&ch);
    if recent
        .iter()
        .any(|(_, row)| row.contains("esc to cancel") || row.contains("Generating..."))
    {
        return hook_status("working");
    }
    if !recent.iter().any(|(_, row)| row.chars().any(braille))
        && let Some(index) = recent.iter().take(3).position(|(_, row)| row.trim() == ">")
        && index > 0
    {
        let border = recent[index - 1].1.trim_end_matches(' ');
        let drawn = border
            .chars()
            .filter(|&ch| "─━═╌╍-_▀▄╹╺╸╻│┃┌┐└┘├┤".contains(ch))
            .count();
        let content = border.chars().filter(|&ch| ch != ' ').count();
        if border.chars().count() >= 3 && content != 0 && 5 * drawn >= 4 * content {
            return hook_status("idle");
        }
    }
    let (index, line) = recent.first()?;
    let text = line.trim();
    let first = text.chars().next()?;
    let last = text.chars().next_back()?;
    if braille(first)
        || braille(last)
        || ("|/-\\".contains(first)
            && (text.len() == 1 || {
                let rest = &text[1..];
                rest.starts_with(' ') && !rest.contains(first)
            }))
    {
        return hook_status("working");
    }
    if !screen.hide_cursor()
        && usize::from(screen.cursor_position().0) == *index
        && ">$#%❯»›".contains(last)
    {
        return hook_status("idle");
    }
    None
}

pub(crate) fn exit_conversation(bytes: &[u8]) -> Option<&str> {
    let marker = b"agy --conversation=";
    let start = bytes
        .windows(marker.len())
        .rposition(|window| window == marker)?
        + marker.len();
    let token = &bytes[start..];
    let end = token.iter().position(|&b| b <= 0x20 || b >= 0x7f)?;
    let id = std::str::from_utf8(&token[..end]).ok()?;
    super::codex_receipt::canonical_thread(id).then_some(id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn version_and_conversation_fences() {
        assert_eq!(version("Antigravity v1.4.7\n").unwrap(), "1.4.7");
        assert!(version("1.0.99\n1.4.7").is_err());
        assert!(!status_line("1.3.99"));
        assert!(status_line("1.4.0"));
        assert!(advertises_settings(
            "  --model    Model\n  --settings      Settings JSON"
        ));
        assert!(!advertises_settings(
            "  --settings-file    Other CLI setting"
        ));
        assert!(!advertises_settings("Use --settings to refer to the flag"));
        assert!(version("1.4.").is_err());
        let id = "12345678-abcd-1234-abcd-123456789abc";
        assert_eq!(
            exit_conversation(format!("agy --conversation={id}\x1b[0m").as_bytes()),
            Some(id)
        );
        assert_eq!(
            exit_conversation(format!("agy --conversation={id}").as_bytes()),
            None
        );
        assert_eq!(
            exit_conversation(format!("--conversation={id}\n").as_bytes()),
            None
        );
        assert_eq!(
            exit_conversation(
                format!("agy --conversation={id}\nagy --conversation=bad\n").as_bytes()
            ),
            None
        );
    }

    #[test]
    fn argv_uses_native_flags_in_go_order() {
        let request = json!({"initial_prompt":"hello","options":{
            "model":"Gemini", "mode":"plan", "dangerously-skip-permissions":"true"
        }});
        assert_eq!(
            argv(&request).unwrap(),
            [
                "--model",
                "Gemini",
                "--mode",
                "plan",
                "--dangerously-skip-permissions",
                "--prompt-interactive",
                "hello"
            ]
        );
        assert!(argv(&json!({"options":{"mode":"--acp"}})).is_err());
        assert!(
            argv(&json!({"options":{"model":"--acp"}}))
                .unwrap()
                .is_empty()
        );
    }
}

pub(crate) mod handoff;
pub(crate) use handoff::verify as verify_handoff;
