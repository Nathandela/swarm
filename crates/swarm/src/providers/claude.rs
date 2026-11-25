use serde_json::{Map, Value, json};
use sha2::{Digest, Sha256};
use std::io::{self, Read};

pub(crate) const EVENTS: &[&str] = &[
    "Notification",
    "PermissionRequest",
    "PostToolUse",
    "PreToolUse",
    "Stop",
    "SubagentStart",
    "SubagentStop",
    "UserPromptSubmit",
];

pub(crate) fn version(output: &str) -> io::Result<&str> {
    let version = output
        .split_whitespace()
        .find_map(|word| {
            let word = word.strip_prefix('v').unwrap_or(word);
            if word.contains('.')
                && word
                    .split('.')
                    .all(|part| !part.is_empty() && part.bytes().all(|c| c.is_ascii_digit()))
            {
                Some(word)
            } else {
                None
            }
        })
        .ok_or_else(|| io::Error::other("unrecognized Claude Code version"))?;
    let release: Vec<u32> = version
        .split('.')
        .map(str::parse)
        .collect::<Result<_, _>>()
        .map_err(|_| io::Error::other("invalid Claude Code release version"))?;
    let bound = (
        release[0],
        release.get(1).copied().unwrap_or(0),
        release.get(2).copied().unwrap_or(0),
    );
    if !((2, 0, 0)..=(9999, 0, 0)).contains(&bound) {
        return Err(io::Error::other(format!(
            "Claude Code {version} is outside supported versions 2.0.0–9999.0.0"
        )));
    }
    Ok(version)
}

pub(crate) fn argv(label: &str, request: &Value) -> io::Result<Vec<String>> {
    let current = std::env::current_exe()?;
    let current = current
        .to_str()
        .ok_or_else(|| io::Error::other("Swarm path is not representable as UTF-8"))?;
    let hook = format!("'{}' hook", current.replace('\'', "'\\''"));
    let hooks: Map<_, _> = EVENTS
        .iter()
        .map(|event| {
            (
                (*event).to_owned(),
                json!([{"hooks":[{"type":"command","command":format!("{hook} {event}")}]}]),
            )
        })
        .collect();
    let settings = serde_json::to_string(&json!({"hooks": hooks}))?;
    let mut argv = vec!["--settings".to_owned(), settings];
    if !label.is_empty() {
        argv.extend(["--name".into(), label.to_owned()]);
    }
    let options = &request["options"];
    if let Some(model) = options["model"]
        .as_str()
        .filter(|m| !m.is_empty() && !m.starts_with('-'))
    {
        argv.extend(["--model".into(), model.to_owned()]);
    }
    if options["dangerously-skip-permissions"] == "true" {
        argv.push("--dangerously-skip-permissions".into());
    }
    if let Some(prompt) = request["initial_prompt"].as_str().filter(|p| !p.is_empty()) {
        if prompt.starts_with('-') {
            argv.push("--".into());
        }
        argv.push(prompt.to_owned());
    }
    Ok(argv)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn floor_version_and_option_arguments() {
        assert!(version("1.9.9 (Claude Code)").is_err());
        assert!(version("3.0.0-preview (Claude Code)").is_err());
        assert!(version("garbage").is_err());
        assert_eq!(version("v2.0.0 (Claude Code)").unwrap(), "2.0.0");
        let args = argv(
            "test name",
            &json!({
                "options": {"model":"--remote-control"},
                "initial_prompt":"--not-an-option\n東京",
            }),
        )
        .unwrap();
        assert_eq!(
            &args[2..],
            ["--name", "test name", "--", "--not-an-option\n東京"]
        );
        let settings: Value = serde_json::from_str(&args[1]).unwrap();
        assert_eq!(settings["hooks"].as_object().unwrap().len(), EVENTS.len());
        assert!(
            settings["hooks"]["PermissionRequest"][0]["hooks"][0]["command"]
                .as_str()
                .unwrap()
                .ends_with(" hook PermissionRequest")
        );
    }
}

#[derive(Default)]
pub(crate) struct Observation {
    parent_active: bool,
    idle_known: bool,
    interaction: &'static str,
    children: std::collections::BTreeSet<String>,
}

impl Observation {
    pub(crate) fn apply(&mut self, event: &str, payload: &Value) {
        let child = crate::ipc::field(payload, "agent_id");
        match event {
            "UserPromptSubmit" | "PreToolUse" | "PostToolUse" => {
                self.parent_active = true;
                self.interaction = "none";
            }
            "SubagentStart" if !child.is_empty() && self.children.len() < 256 => {
                self.children.insert(child.to_owned());
            }
            "SubagentStop" if !child.is_empty() => {
                self.children.remove(child);
            }
            "Stop" => {
                self.parent_active = false;
                self.idle_known = true;
                self.interaction = "none";
            }
            "PermissionRequest" => {
                self.parent_active = false;
                self.idle_known = true;
                self.interaction = "permission";
            }
            "Notification" => {
                let subtype = crate::ipc::field(payload, "notification_type");
                let interaction = match subtype {
                    "permission" | "permission_prompt" => "permission",
                    "idle" | "idle_prompt" => "none",
                    "prompt" => "prompt",
                    _ => return,
                };
                self.parent_active = false;
                self.idle_known = true;
                self.interaction = interaction;
            }
            _ => {}
        }
    }

    pub(crate) fn status(&self) -> Value {
        let turn = if self.parent_active || !self.children.is_empty() {
            "active"
        } else if self.idle_known {
            "idle"
        } else {
            "unknown"
        };
        let interaction = if self.interaction.is_empty() {
            "none"
        } else {
            self.interaction
        };
        let mut status = json!({"Process": "running", "Turn": turn, "Interaction": interaction});
        if matches!(interaction, "permission" | "prompt") && turn == "active" {
            status["Attention"] = json!(true);
        }
        status
    }
}

pub(crate) fn conversation_id(payload: &Value) -> Option<&str> {
    payload["session_id"].as_str().filter(|id| canonical_id(id))
}

pub(crate) fn canonical_id(id: &str) -> bool {
    id.len() == 36
        && id.bytes().enumerate().all(|(i, c)| {
            if matches!(i, 8 | 13 | 18 | 23) {
                c == b'-'
            } else {
                c.is_ascii_digit() || (b'a'..=b'f').contains(&c)
            }
        })
}

#[cfg(test)]
mod observation_tests {
    use super::*;

    #[test]
    fn late_child_exit_does_not_finish_a_new_parent_turn() {
        let mut observation = Observation::default();
        let child = json!({"agent_id":"child","turn":"idle"});
        observation.apply("SubagentStart", &child);
        observation.apply("PermissionRequest", &json!({}));
        assert_eq!(observation.status()["Attention"], true);
        observation.apply("Notification", &json!({"notification_type":"auth_success"}));
        assert_eq!(observation.status()["Interaction"], "permission");
        observation.apply("UserPromptSubmit", &json!({}));
        observation.apply("SubagentStop", &child);
        assert_eq!(observation.status()["Turn"], "active");
        observation.apply("Stop", &json!({}));
        assert_eq!(observation.status()["Turn"], "idle");
        assert!(conversation_id(&json!({"session_id":"not-a-real-id"})).is_none());
    }
}

pub(crate) fn scheduled(version: &str) -> bool {
    version == "2.1.329"
}

pub(crate) fn new_conversation() -> io::Result<String> {
    let mut bytes = [0; 16];
    std::fs::File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    let hex: String = bytes.iter().map(|byte| format!("{byte:02x}")).collect();
    Ok(format!(
        "{}-{}-{}-{}-{}",
        &hex[..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..]
    ))
}

pub(crate) fn scheduled_text(text: &str) -> bool {
    !text.is_empty() && !text.chars().any(|c| c.is_control() && c != '\n')
}

pub(crate) fn text_hash(text: &str) -> String {
    format!("{:x}", Sha256::digest(text.as_bytes()))
}

pub(crate) fn scheduled_paste(prompt: &str) -> Option<&str> {
    // Interactive 2.1.329 wraps a bracketed multiline paste in an identified native block.
    let (id, body) = prompt
        .strip_prefix("\n\n<pasted_content id=\"")?
        .split_once("\">\n")?;
    if id.len() != 4
        || !id
            .bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
    {
        return None;
    }
    body.strip_suffix(&format!("\n</pasted_content id=\"{id}\">\n"))
}

pub(crate) fn empty_composer(screen: &vt100::Screen) -> bool {
    composer(screen, "", "")
}

pub(crate) fn rendered_paste(screen: &vt100::Screen, text: &str) -> bool {
    let newlines = text.bytes().filter(|&byte| byte == b'\n').count();
    composer(
        screen,
        &format!("[Pasted text #1 +{newlines} lines]"),
        "paste again to expand",
    )
}

fn composer(screen: &vt100::Screen, draft: &str, footer: &str) -> bool {
    if !screen.bracketed_paste() || !screen.alternate_screen() {
        return false;
    }
    let (rows, cols) = screen.size();
    let (rows, cols) = (rows.get(), cols.get());
    let (row, col) = screen.cursor_position();
    if usize::from(col) != draft.len() + 2 || row < 1 || row.checked_add(3) != Some(rows) {
        return false;
    }
    let mut lines = screen.rows(0, cols).skip(usize::from(row - 1));
    let separator =
        |s: String| s.chars().count() == usize::from(cols) && s.chars().all(|c| c == '─');
    // The upper divider may include Claude's session name on the right.
    lines.next().is_some_and(|line| {
        line.ends_with('─') && line.chars().take_while(|c| *c == '─').count() >= 8
    }) && lines.next().is_some_and(|line| {
        line.strip_prefix('❯')
            .is_some_and(|body| body.trim() == draft)
    }) && lines.next().is_some_and(separator)
        && (footer.is_empty()
            || lines
                .next()
                .is_some_and(|line| line.trim_start().starts_with(footer)))
}

pub(crate) fn startup_attention(screen: &vt100::Screen) -> bool {
    let grid = screen.contents();
    [
        ("Yes, I trust this folder", "No, exit"),
        (
            "Do you want to use this API key?",
            "Detected a custom API key",
        ),
        ("Make auto mode your default permission mode?", "No, keep"),
    ]
    .iter()
    .any(|(heading, choice)| grid.contains(heading) && grid.contains(choice))
}

#[cfg(test)]
mod schedule_tests {
    use super::*;

    #[test]
    fn scheduled_composer_refuses_modal_or_owner_draft() {
        let mut parser = vt100::Parser::new(8.try_into().unwrap(), 20.try_into().unwrap(), 0);
        parser.process("\x1b[?1049h\x1b[?2004h\x1b[5;1H───────────── Test ─\x1b[6;1H❯  \x1b[7;1H────────────────────\x1b[6;3H".as_bytes());
        assert!(empty_composer(parser.screen()));
        parser.process(b"typed\x1b[6;3H");
        assert!(!empty_composer(parser.screen()));
        parser.process("\x1b[2J\x1b[6;1H❯ No, exit\x1b[6;3H".as_bytes());
        assert!(!empty_composer(parser.screen()));
        assert!(!scheduled_text("bad\x1b[201~"));
        assert!(scheduled_text("exact\nline"));
        assert_eq!(
            scheduled_paste(
                "\n\n<pasted_content id=\"abcd\">\nexact\nline\n</pasted_content id=\"abcd\">\n"
            ),
            Some("exact\nline")
        );
        assert_eq!(
            scheduled_paste(
                "\n\n<pasted_content id=\"abcd\">\nexact\nline\n</pasted_content id=\"acbd\">\n"
            ),
            None
        );
        assert!(canonical_id(&new_conversation().unwrap()));
    }
}
