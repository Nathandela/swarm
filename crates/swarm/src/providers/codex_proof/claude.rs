//! Evidence received from the enrolled Claude manager's independent native guard.
use super::{Ledger, canonical_hash, codex_receipt, is_hash, refusal, valid};
use crate::ipc::field;
use serde_json::{Value, json};
use std::io;

pub(crate) const PREFIX: &str = "[SWARM_MANAGER_INPUT_V1:";
pub(crate) const ENVELOPE_OVERHEAD: usize = PREFIX.len() + 66;

pub(crate) fn envelope(nonce: &str, text: &str) -> io::Result<String> {
    if !is_hash(nonce)
        || text.is_empty()
        || text.contains(['\0'])
        || text.contains(PREFIX)
        || text.len() > 4096 - ENVELOPE_OVERHEAD
    {
        return Err(refusal("invalid Claude manager admission envelope"));
    }
    Ok(format!("{PREFIX}{nonce}]\n{text}"))
}

fn projection(sender: &str, envelope: &str) -> io::Result<String> {
    if !sender.is_ascii() || !sender.starts_with("swarm-manager-input-") {
        return Err(refusal("invalid Claude manager sender identity"));
    }
    let name = if sender.len() > 23 {
        format!("{}…", &sender[..23])
    } else {
        sender.to_owned()
    };
    Ok(format!(
        "The {name} plugin sent a message:\n{envelope}\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — it starts this turn in the user's place. Address the message above."
    ))
}

fn nonce_and_text(value: &str) -> io::Result<(&str, &str)> {
    let (nonce, text) = value
        .strip_prefix(PREFIX)
        .and_then(|record| record.split_once("]\n"))
        .ok_or_else(|| refusal("missing exact Claude manager envelope"))?;
    if envelope(nonce, text)? != value {
        return Err(refusal("invalid Claude manager envelope"));
    }
    Ok((nonce, text))
}

impl Ledger {
    pub(crate) fn claude_guard(
        &mut self,
        sender: &str,
        path: &str,
        req: &Value,
    ) -> io::Result<Value> {
        if self.data["binding"]["provider"] != "claude" || self.data["blocked"] != false {
            return Err(refusal("Claude manager guard has no live binding"));
        }
        let mut next = self.data.clone();
        let mut response = Value::Null;
        let result = match path {
            "/manager-prompt" => (|| {
                let raw = field(req, "text");
                let (nonce, text) = nonce_and_text(raw)?;
                let prepared = &mut next["prepared"][nonce];
                if prepared["text_hash"] != codex_receipt::hash(text)
                    || prepared["external_input"] != self.data["external_input"]
                    || !prepared["prompted"].is_null()
                {
                    return Err(refusal("unregistered or repeated Claude manager prompt"));
                }
                prepared["prompted"] = json!(true);
                response = json!({"nonce":nonce});
                Ok(())
            })(),
            "/manager-append" => (|| {
                let nonce = field(req, "native_input_id");
                let message = field(req, "message_id");
                let native = field(req, "projection");
                let prepared = &mut next["prepared"][nonce];
                let offset = native
                    .find(PREFIX)
                    .ok_or_else(|| refusal("missing native Claude projection"))?;
                let ending = "\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — it starts this turn in the user's place. Address the message above.";
                let raw = native
                    .get(offset..)
                    .and_then(|body| body.strip_suffix(ending))
                    .ok_or_else(|| refusal("missing exact native Claude projection"))?;
                let (actual, text) = nonce_and_text(raw)?;
                if nonce != actual
                    || !valid(message)
                    || prepared["prompted"] != true
                    || !prepared["message_id"].is_null()
                    || prepared["text_hash"] != codex_receipt::hash(text)
                    || native != projection(sender, raw)?
                    || !self.data["messages"][message].is_null()
                {
                    return Err(refusal(
                        "native Claude projection differs from prepared input",
                    ));
                }
                prepared["message_id"] = json!(message);
                prepared["projection_hash"] = json!(codex_receipt::hash(native));
                next["messages"][message] = json!(nonce);
                Ok(())
            })(),
            "/activity" => {
                if self.data["external_input"]
                    .as_u64()
                    .and_then(|n| n.checked_add(1))
                    != req["external_input"].as_u64()
                {
                    Err(refusal("Claude external input sequence changed"))
                } else {
                    next["external_input"] = req["external_input"].clone();
                    next["externally_steered"] = json!(true);
                    Ok(())
                }
            }
            "/turn"
                if self.data["current"].is_null()
                    && req["turn"]["state"] == "idle"
                    && field(&req["turn"], "turn_id").is_empty() =>
            {
                Ok(())
            }
            "/turn" => turn(&mut next, req),
            "/manager-idle"
                if self.data["current"].is_null() && field(req, "turn_id").is_empty()
                    || self.data["current"] == req["turn_id"]
                        && self.data["turns"][field(req, "turn_id")]["closed"] == true =>
            {
                next["thread_state"] = json!("idle");
                Ok(())
            }
            "/manager-tool-step" | "/manager-tool" | "/manager-tool-complete" => {
                tool(&mut next, path, &req["call"])
            }
            _ => Err(refusal("unexpected Claude manager guard evidence")),
        };
        if let Err(error) = result {
            // A mismatch in an authenticated observation permanently fences native effects.
            self.data["blocked"] = json!(true);
            self.commit(self.data.clone())?;
            return Err(error);
        }
        if next != self.data {
            self.commit(next)?;
        }
        Ok(response)
    }
}

fn turn(next: &mut Value, req: &Value) -> io::Result<()> {
    let event = &req["turn"];
    let id = field(event, "turn_id");
    if !valid(id) {
        return Err(refusal("invalid Claude native turn ID"));
    }
    match field(event, "state") {
        "running" => {
            if !next["turns"][id].is_null()
                || next["turns"][field(next, "current")]["closed"] == false
            {
                return Err(refusal("overlapping or repeated Claude main turn"));
            }
            let mut entry = json!({"closed":false});
            if let Some(nonce) = event["native_input_id"].as_str() {
                let prepared = &next["prepared"][nonce];
                if !is_hash(nonce)
                    || prepared["prompted"] != true
                    || prepared["message_id"] != event["native_message_id"]
                    || prepared["projection_hash"] != codex_receipt::hash(field(req, "projection"))
                    || prepared["external_input"] != next["external_input"]
                    || next["turns"]
                        .as_object()
                        .is_none_or(|turns| turns.values().any(|turn| turn["nonce"] == nonce))
                {
                    return Err(refusal(
                        "Claude turn has no exact appended machine admission",
                    ));
                }
                entry["nonce"] = json!(nonce);
            }
            next["turns"][id] = entry;
            next["current"] = json!(id);
            next["observed_turn"] = json!(true);
            next["thread_state"] = json!("active");
        }
        "completed" if next["current"] == id && next["turns"][id]["closed"] == false => {
            if next["calls"].as_object().is_some_and(|calls| {
                calls.values().any(|call| {
                    call["turn"] == id
                        && call["closed"] == false
                        && !call["arguments_hash"].is_null()
                })
            }) {
                return Err(refusal("Claude turn completed with an open manager call"));
            }
            next["turns"][id]["closed"] = json!(true);
            next["turns"][id]["reason"] = event["reason"].clone();
        }
        _ => return Err(refusal("unexpected Claude native turn transition")),
    }
    Ok(())
}

fn tool(next: &mut Value, path: &str, call: &Value) -> io::Result<()> {
    let id = field(call, "call_id");
    let turn = field(call, "turn_id");
    let name = field(call, "tool_name");
    if !valid(id)
        || !valid(turn)
        || !valid(name)
        || !call["agent_id"].is_null() && !valid(field(call, "agent_id"))
    {
        return Err(refusal("invalid Claude native tool coordinates"));
    }
    let old = &mut next["calls"][id];
    if path == "/manager-tool-step" {
        if old.is_null() {
            *old = json!({"turn":turn,"tool":name,"agent":call["agent_id"],"closed":false});
        } else if old["turn"] != turn || old["tool"] != name || old["agent"] != call["agent_id"] {
            return Err(refusal("Claude native tool ID changed"));
        }
    } else {
        if old["turn"] != turn
            || old["tool"] != name
            || old["agent"] != call["agent_id"]
            || !call["arguments"].is_object()
        {
            return Err(refusal("Claude tool call has no matching streamed witness"));
        }
        let mut args = call["arguments"].clone();
        args.sort_all_objects();
        let digest = canonical_hash(&args)?;
        if path == "/manager-tool-complete" {
            if old["arguments_hash"] != digest {
                return Err(refusal("Claude tool closure differs from native arguments"));
            }
            old["closed"] = json!(true);
        } else if old["closed"] == false
            && (old["arguments_hash"].is_null() || old["arguments_hash"] == digest)
        {
            old["arguments_hash"] = json!(digest);
        } else {
            return Err(refusal("Claude native tool opening changed or closed"));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;

    #[test]
    fn native_claude_requires_projection_and_main_tool_lifecycle() -> io::Result<()> {
        let root = std::env::temp_dir().join(format!("cl-proof-{}", runtime::random_id()?));
        runtime::private_dir(&root)?;
        let binding = json!({"provider":"claude","native_session_id":"native"});
        let mut ledger = Ledger::open(&root, &binding)?;
        let sender = "swarm-manager-input-01234567890123456789012345678901";
        let nonce = "b".repeat(64);
        let text = "Check \"one\".\nSecond line.";
        let envelope = envelope(&nonce, text)?;
        let projection = projection(sender, &envelope)?;
        ledger.prepare("operation", &nonce, text, 0)?;
        ledger.claude_guard(sender, "/manager-idle", &json!({"turn_id":""}))?;
        assert_eq!(
            ledger.claude_guard(sender, "/manager-prompt", &json!({"text":envelope}))?["nonce"],
            nonce
        );
        ledger.claude_guard(
            sender,
            "/manager-append",
            &json!({
                "native_input_id":nonce,"message_id":"message","projection":projection
            }),
        )?;
        ledger.claude_guard(sender, "/turn", &json!({
            "turn":{"turn_id":"main","state":"running","native_input_id":nonce,"native_message_id":"message"},
            "projection":projection
        }))?;
        let args = json!({"text":"Native reply"});
        let query = |id: &str| {
            json!({
                "binding":binding,"host_meta":{"claudecode/toolUseId":id},
                "tool_name":"publish","arguments_hash":canonical_hash(&args).unwrap()
            })
        };
        assert!(super::super::resolve(&ledger.data, &query("tool")).is_err());
        for (id, agent) in [("descendant", json!("agent")), ("tool", Value::Null)] {
            let mut call =
                json!({"call_id":id,"turn_id":"main","tool_name":"publish","agent_id":agent});
            ledger.claude_guard(sender, "/manager-tool-step", &json!({"call":call}))?;
            call["arguments"] = args.clone();
            ledger.claude_guard(sender, "/manager-tool", &json!({"call":call}))?;
            if id == "tool" {
                assert_eq!(
                    super::super::resolve(&Ledger::snapshot(&root, &binding)?, &query(id))?["native_input_id"],
                    nonce
                );
            } else {
                assert!(super::super::resolve(&ledger.data, &query(id)).is_err());
            }
            ledger.claude_guard(sender, "/manager-tool-complete", &json!({"call":call}))?;
            assert!(super::super::resolve(&ledger.data, &query(id)).is_err());
        }
        ledger.claude_guard(
            sender,
            "/turn",
            &json!({"turn":{"turn_id":"main","state":"completed","reason":"end_turn"}}),
        )?;
        let outcome = super::super::input_status(&Ledger::snapshot(&root, &binding)?, &nonce);
        assert_eq!(outcome["state"], "completed");
        assert_eq!(outcome["native_turn_id"], "main");
        assert!(
            ledger
                .claude_guard(sender, "/manager-prompt", &json!({"text":envelope}))
                .is_err()
        );
        assert!(Ledger::snapshot(&root, &binding).is_err());
        std::fs::remove_dir_all(&root)
    }
}
