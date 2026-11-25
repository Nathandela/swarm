//! Deduplicate passive PTY notifications at the process that owns the bytes.
use super::{State, write_input};
use crate::{ipc::field, supervisor::runtime};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::io;

pub(super) fn notify(state: &State, req: &Value) -> io::Result<Value> {
    let id = field(req, "child");
    let seq = req["seq"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(|| io::Error::other("invalid supervision event sequence"))?;
    let text = field(req, "data");
    if !runtime::valid_id(id) || text.is_empty() || text.len() > 4096 || state.semantic.is_some() {
        return Err(io::Error::other("unsupported supervision input"));
    }
    let owner = state.control.lock().unwrap();
    let path = state.dir.join(format!("notice-{id}.json"));
    let hash = format!("{:x}", Sha256::digest(text));
    match runtime::read_json(&path) {
        Ok(old) => {
            let n = old["seq"]
                .as_u64()
                .ok_or_else(|| io::Error::other("damaged supervision receipt"))?;
            if old["token"] != state.token
                || old["child"] != id
                || n > seq
                || n == seq && old["hash"] != hash
            {
                return Err(io::Error::other("conflicting supervision receipt"));
            }
            if old["state"] != "applied" {
                return Ok(json!({"type":"uncertain"}));
            }
            if n == seq {
                return Ok(json!({"type":"ok"}));
            }
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => {}
        Err(e) => return Err(e),
    }
    let status = if state.provider == "claude" {
        state.observations.lock().unwrap().state.status()
    } else {
        let view = runtime::read_json(&state.dir.join("observation.json"))?;
        if view["token"] != state.token {
            return Err(io::Error::other("unbound provider status"));
        }
        view["status"].clone()
    };
    if state
        .terminal_input
        .load(std::sync::atomic::Ordering::Acquire)
        || *owner != 0
        || status["Turn"] != "idle"
        || status["Interaction"] != "none"
        || status["Attention"] == true
    {
        return Ok(json!({"type":"held"}));
    }
    let mut receipt =
        json!({"state":"outcome_unknown","child":id,"seq":seq,"token":state.token,"hash":hash});
    runtime::write_json(&path, &receipt)?;
    write_input(state, text, true, true)?;
    receipt["state"] = json!("applied");
    runtime::write_json(&path, &receipt)?;
    Ok(json!({"type":"ok"}))
}
