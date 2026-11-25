//! Validate host-provided native coordinates independently of model tool arguments.
use super::config::invalid;
use serde_json::Value;
use std::io;

pub(crate) struct Coordinates<'a> {
    pub(crate) call: &'a str,
    pub(crate) turn: Option<&'a str>,
}

pub(crate) fn coordinates<'a>(binding: &Value, meta: &'a Value) -> io::Result<Coordinates<'a>> {
    let valid = |key: &str| {
        meta[key]
            .as_str()
            .filter(|id| {
                !id.is_empty()
                    && id.len() <= 256
                    && id.trim() == *id
                    && !id.chars().any(char::is_control)
            })
            .ok_or_else(|| invalid("manager tool call has no valid host coordinate"))
    };
    match binding["provider"].as_str() {
        Some("claude") => Ok(Coordinates {
            call: valid("claudecode/toolUseId")?,
            turn: None,
        }),
        Some("codex") => {
            let call = valid("callId")?;
            let thread = valid("threadId")?;
            let native = &meta["x-codex-turn-metadata"];
            let turn = native["turn_id"]
                .as_str()
                .filter(|id| {
                    !id.is_empty()
                        && id.len() <= 256
                        && id.trim() == *id
                        && !id.chars().any(char::is_control)
                })
                .ok_or_else(|| {
                    invalid("Codex manager tool call has no authenticated turn coordinate")
                })?;
            if binding["native_session_id"] != thread
                || meta["sessionId"] != thread
                || native["thread_id"] != thread
                || native["session_id"] != thread
            {
                return Err(invalid(
                    "Codex tool host metadata belongs to a different native session",
                ));
            }
            Ok(Coordinates {
                call,
                turn: Some(turn),
            })
        }
        _ => Err(invalid("unsupported manager provenance provider")),
    }
}
