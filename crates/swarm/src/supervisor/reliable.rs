//! Persist the owner operation binding before allowing a shim to consume input.
use super::{State, runtime};
use crate::ipc::{field, spawn};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::io::{self, ErrorKind};
use std::path::Path;

fn read(path: &Path, id: &str) -> io::Result<Option<Value>> {
    match runtime::read_json(path) {
        Err(e) if e.kind() == ErrorKind::NotFound => Ok(None),
        Ok(record)
            if record["operation_id"] == id
                && record["schema"] == 1
                && matches!(
                    record["state"].as_str(),
                    Some("outcome_unknown" | "applied")
                )
                && record["session_id"].as_str().is_some_and(runtime::valid_id)
                && record["request_hash"]
                    .as_str()
                    .is_some_and(|s| s.len() == 64) =>
        {
            Ok(Some(record))
        }
        Ok(_) => Err(io::Error::other("damaged owner launch receipt")),
        Err(e) => Err(e),
    }
}

fn outcome(state: &State, record: Option<&Value>) -> Value {
    match record {
        Some(record) if record["state"] == "applied" => {
            json!({"state":"applied","session_id":format!("{}/{}",state.endpoint,field(record,"session_id"))})
        }
        Some(_) => json!({"state":"outcome_unknown"}),
        None => json!({"state":"unknown_operation"}),
    }
}

fn refusal(id: &str, code: &str, error: &str) -> Value {
    json!({"op":"error","operation_id":id,"error_code":code,"error":error})
}

pub(super) fn handle(state: &State, request: &Value, negotiated: bool) -> io::Result<Value> {
    let status = request["op"] == "owner_launch_status";
    let id = field(
        request,
        if status {
            "subject_operation_id"
        } else {
            "operation_id"
        },
    );
    if !negotiated {
        return Ok(refusal(
            id,
            "op_not_implemented",
            "owner-launch capability was not negotiated",
        ));
    }
    if id.is_empty() || id.len() > 128 || !status && !spawn::replayable(id, spawn::now_ms()) {
        return Ok(refusal(
            id,
            "invalid_field",
            if status {
                "launch status requires subject_operation_id"
            } else {
                "owner launch requires an exact request and current timestamped operation ID"
            },
        ));
    }
    let key = Sha256::digest(id);
    let path = state.root.join("operations").join(format!("{key:x}.json"));
    if status {
        return Ok(
            json!({"op":"owner_launch_status","subject_operation_id":id,"operation_outcome":outcome(state,read(&path,id)?.as_ref())}),
        );
    }
    let _gate = state.operation_locks[usize::from(key[0]) % state.operation_locks.len()]
        .lock()
        .unwrap();
    let mut normalized = request["launch"].clone();
    let Some(object) = normalized.as_object_mut() else {
        return Ok(refusal(id, "invalid_field", "missing launch request"));
    };
    // Go emits optional zeros sparsely. Explicit and omitted defaults are one intent.
    for (name, default) in [
        ("name", json!("")),
        ("worktree", json!(false)),
        ("cols", json!(0)),
        ("rows", json!(0)),
        ("initial_prompt", json!("")),
        ("options", Value::Null),
        ("env", Value::Null),
    ] {
        if object.get(name) == Some(&default) {
            object.shift_remove(name);
        }
    }
    normalized.sort_all_objects();
    let hash = format!("{:x}", Sha256::digest(serde_json::to_vec(&normalized)?));
    if let Some(record) = read(&path, id)? {
        if record["request_hash"] != hash {
            return Ok(refusal(
                id,
                "invalid_field",
                "owner launch: operation ID is already bound to another request",
            ));
        }
        if record["state"] == "applied" {
            let sessions = state.sessions.read().unwrap();
            if let Some(s) = sessions.get(field(&record, "session_id")) {
                return Ok(
                    json!({"op":"launch","operation_id":id,"session":s.view(&state.endpoint)}),
                );
            }
        }
        return Ok(refusal(
            id,
            "outcome_unknown",
            "owner launch outcome is uncertain; inspect its status",
        ));
    }
    let session = match state.reserve(&json!({"launch":request["launch"]})) {
        Ok(s) => s,
        Err(e) => return Ok(refusal(id, "invalid_field", &format!("launch: {e}"))),
    };
    let mut record = json!({"schema":1,"operation_id":id,"request_hash":hash,"state":"outcome_unknown","session_id":session.id});
    runtime::write_json(&path, &record)?;
    let view = match state.start(&session) {
        Ok(v) => v,
        Err(e) => return Ok(refusal(id, "outcome_unknown", &e.to_string())),
    };
    record["state"] = json!("applied");
    runtime::write_json(&path, &record)?;
    Ok(json!({"op":"launch","operation_id":id,"session":view}))
}
