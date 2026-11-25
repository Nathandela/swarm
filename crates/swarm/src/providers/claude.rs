use serde_json::{Map, Value, json};
use std::io;

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
    let id = payload["session_id"].as_str()?;
    let canonical = id.len() == 36
        && id.bytes().enumerate().all(|(i, c)| {
            if matches!(i, 8 | 13 | 18 | 23) {
                c == b'-'
            } else {
                c.is_ascii_digit() || (b'a'..=b'f').contains(&c)
            }
        });
    canonical.then_some(id)
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
