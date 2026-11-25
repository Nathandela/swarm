use crate::{ipc::field, providers::codex_receipt, supervisor::runtime};
use serde_json::{Value, json};
use std::{
    fs::File,
    io,
    path::{Path, PathBuf},
};

pub(crate) fn native(dir: &Path, token: &str) -> io::Result<Value> {
    let record = runtime::read_json(&dir.join("opencode-native.json"))?;
    if record["token"] != token
        || !super::valid_id(field(&record, "rootID"), "ses_")
        || field(&record, "sessionInstance").len() != 32
        || record["sessionInstance"] != record["backendGeneration"]
        || record["nativeCreatedAtMS"].as_u64().is_none_or(|n| n == 0)
    {
        return Err(io::Error::other("invalid OpenCode native identity"));
    }
    Ok(record)
}

fn path(dir: &Path, operation: &str) -> io::Result<PathBuf> {
    if operation.is_empty() || operation.len() > 128 || operation.chars().any(char::is_control) {
        return Err(io::Error::other("invalid OpenCode operation ID"));
    }
    Ok(dir
        .join("opencode-receipts")
        .join(format!("{}.json", codex_receipt::hash(operation))))
}

pub(crate) fn save(dir: &Path, operation: &str, record: &Value) -> io::Result<()> {
    let path = path(dir, operation)?;
    runtime::private_dir(path.parent().expect("receipt directory"))?;
    File::open(dir)?.sync_all()?;
    runtime::write_json(&path, record)
}

pub(crate) fn read(
    dir: &Path,
    token: &str,
    session: &str,
    operation: &str,
) -> io::Result<Option<Value>> {
    let record = match runtime::read_json(&path(dir, operation)?) {
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(err) => return Err(err),
        Ok(record) => record,
    };
    let native = native(dir, token)?;
    if !valid(&record, &native, token, session, operation) {
        return Err(io::Error::other("unverified OpenCode operation receipt"));
    }
    Ok(Some(record))
}

pub(crate) fn bridge_matches(record: &Value, bridge: &Value) -> bool {
    let receipt = &record["receipt"];
    bridge["version"] == 1
        && bridge["rootID"] == receipt["root_id"]
        && bridge["sessionInstance"] == receipt["session_instance"]
        && bridge["backendGeneration"] == receipt["generation"]
        && bridge["nativeCreatedAtMS"] == receipt["native_created_at_ms"]
        && bridge["operationID"] == receipt["operation_id"]
        && bridge["kind"] == record["dispatch_kind"]
        && bridge["targetID"] == record["target"]
        && bridge["messageID"] == record["message_id"]
        && bridge["requestID"] == record["request_id"]
        && super::hex_hash(field(record, "request_hash"))
        && bridge["requestHash"] == record["request_hash"]
}

fn valid(record: &Value, native: &Value, token: &str, session: &str, operation: &str) -> bool {
    let r = &record["receipt"];
    if record["token"] != token
        || r["session"] != session
        || r["operation_id"] != operation
        || r["session_instance"] != native["sessionInstance"]
        || r["root_id"] != native["rootID"]
        || r["generation"] != native["backendGeneration"]
        || r["native_generation"] != native["backendGeneration"]
        || r["native_created_at_ms"] != native["nativeCreatedAtMS"]
        || !super::hex_hash(field(record, "request_hash"))
        || !super::hex_hash(field(record, "intent"))
        || record["target"] != r["native_session_id"]
        || !super::valid_id(field(record, "target"), "ses_")
    {
        return false;
    }
    let bridge = &record["bridge_receipt"];
    let bridge_matches = bridge_matches(record, bridge)
        && bridge["directory"] == native["directory"]
        && bridge["workerID"] == native["workerID"];
    let rejected = bridge_matches
        && bridge["status"] == "rejected"
        && r["reason"] == bridge["reason"]
        && !field(r, "reason").is_empty();
    let state = field(r, "state");
    if record["kind"] == "decision" {
        let kind = field(r, "kind");
        let effect = field(record, "expected_effect");
        let expected = match kind {
            "permission_reply" => {
                let reply_effect = match field(record, "decision_reply") {
                    "once" => "single_request",
                    "always" => "approve_native_patterns_and_matching_session_requests",
                    "reject" => "reject_session_pending_permissions",
                    _ => return false,
                };
                super::valid_id(field(record, "request_id"), "per_") && effect == reply_effect
            }
            "question_reply" | "question_reject" => {
                super::valid_id(field(record, "request_id"), "que_")
                    && effect == "single_request"
                    && record["decision_reply"].is_null()
            }
            _ => false,
        };
        return expected
            && record["dispatch_kind"] == kind
            && r["request_id"] == record["request_id"]
            && record["message_id"].is_null()
            && r["native_message_id"].is_null()
            && r["native_turn_id"].is_null()
            && match state {
                "uncertain" => bridge.is_null() && r["native_effect"] == effect,
                "admitted" => {
                    bridge_matches
                        && bridge["status"] == "admitted"
                        && bridge["nativeEffect"] == effect
                        && r["native_effect"] == effect
                }
                "rejected" => rejected && r["native_effect"] == "none",
                _ => false,
            };
    }
    let input = field(record, "message_id");
    if record["kind"] != "prompt"
        || record["dispatch_kind"] != "prompt"
        || record["target"] != native["rootID"]
        || !record["request_id"].is_null()
        || !r["request_id"].is_null()
        || !super::valid_id(input, "msg_")
        || !super::hex_hash(field(record, "text_hash"))
        || record["issued_ms"].as_u64().is_none_or(|n| n == 0)
    {
        return false;
    }
    if matches!(state, "outcome_unknown" | "rejected") {
        return r["native_message_id"].is_null()
            && r["native_input_id"].is_null()
            && r["native_turn_id"].is_null()
            && if state == "rejected" {
                rejected
            } else {
                bridge.is_null()
            };
    }
    let model = field(r, "model");
    let agent = field(r, "agent");
    let message = &record["native_input"];
    let admission = match field(r, "selection_basis") {
        "native_session_selection" => {
            bridge_matches
                && bridge["status"] == "admitted"
                && bridge["selectionBasis"] == r["selection_basis"]
                && bridge["selectionMessageID"] == r["selection_message_id"]
                && bridge["agent"] == agent
                && bridge["variant"] == r["variant"]
                && format!(
                    "{}/{}",
                    field(&bridge["model"], "providerID"),
                    field(&bridge["model"], "modelID")
                ) == model
        }
        "native_message_receipt" => {
            message["id"] == input
                && message["session"] == record["target"]
                && message["created_ms"]
                    .as_u64()
                    .is_some_and(|n| n >= record["issued_ms"].as_u64().unwrap())
                && message["agent"] == agent
                && message["model"] == model
                && message["text_hash"] == record["text_hash"]
                && r["selection_message_id"] == input
        }
        _ => false,
    };
    if !admission
        || r["native_input_id"] != input
        || r["native_message_id"] != input
        || agent.is_empty()
        || !model
            .split_once('/')
            .is_some_and(|(provider, name)| !provider.is_empty() && !name.is_empty())
    {
        return false;
    }
    let proof = &record["terminal_proof"];
    match state {
        "native_accepted" => r["native_turn_id"].is_null() && proof.is_null(),
        "completed" | "cancelled" | "declined" | "failed" => {
            proof["state"] == state
                && proof["session"] == record["target"]
                && proof["parent"] == input
                && proof["assistant"] == r["native_turn_id"]
                && super::valid_id(field(proof, "assistant"), "msg_")
                && proof["assistant"] != input
                && r["outcome_source"] == "reconciled_snapshot"
        }
        _ => false,
    }
}

pub(crate) fn base(native: &Value, operation: &str, session: &str) -> Value {
    json!({
        "operation_id":operation,"session":session,
        "session_instance":native["sessionInstance"],"root_id":native["rootID"],
        "generation":native["backendGeneration"],"native_generation":native["backendGeneration"],
        "native_created_at_ms":native["nativeCreatedAtMS"],
    })
}
pub(crate) fn intent(request: &Value) -> String {
    codex_receipt::hash(&canonical(request))
}

pub(crate) fn canonical(value: &Value) -> String {
    match value {
        Value::Object(fields) => {
            let fields: std::collections::BTreeMap<_, _> = fields.iter().collect();
            let keys = fields
                .iter()
                .map(|(key, value)| format!("{}:{}", json!(key), canonical(value)))
                .collect::<Vec<_>>();
            format!("{{{}}}", keys.join(","))
        }
        Value::Array(items) => format!(
            "[{}]",
            items.iter().map(canonical).collect::<Vec<_>>().join(",")
        ),
        _ => value.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn question_receipt_requires_exact_hash_and_effect_before_or_after_restart() {
        let root = "ses_0011223344";
        let worker = "00112233445566778899aabb";
        let operation = "semantic:test";
        let session = "ep-test/session";
        let hash = "a".repeat(64);
        let native = json!({"rootID":root,"workerID":worker,"directory":"/project","sessionInstance":"instance",
            "backendGeneration":"incarnation","nativeCreatedAtMS":1});
        let mut r = base(&native, operation, session);
        let request = "que_0011223344";
        r["kind"] = json!("question_reply");
        r["request_id"] = json!(request);
        r["native_session_id"] = json!(root);
        r["native_effect"] = json!("single_request");
        r["state"] = json!("uncertain");
        let mut record = json!({"token":"token","kind":"decision","intent":"b".repeat(64),
            "target":root,"dispatch_kind":"question_reply","request_id":request,"expected_effect":"single_request",
            "request_hash":hash,"receipt":r});
        let is_valid = |record: &Value| valid(record, &native, "token", session, operation);
        assert!(is_valid(&record));
        record["receipt"]["state"] = json!("admitted");
        assert!(!is_valid(&record));
        let mut bridge = json!({"version":1,"workerID":worker,"directory":"/project","rootID":root,
            "sessionInstance":"instance","backendGeneration":"incarnation","nativeCreatedAtMS":1,
            "operationID":operation,"kind":"question_reply","targetID":root,"requestID":request,
            "requestHash":hash,"status":"admitted","nativeEffect":"single_request"});
        record["bridge_receipt"] = bridge.clone();
        assert!(is_valid(&record));
        record["bridge_receipt"]["requestHash"] = json!("c".repeat(64));
        assert!(!is_valid(&record));
        record["bridge_receipt"] = bridge.clone();
        record["receipt"]["state"] = json!("completed");
        assert!(!is_valid(&record));
        record["receipt"]["state"] = json!("rejected");
        record["receipt"]["native_effect"] = json!("none");
        record["receipt"]["reason"] = json!("answer_invalid");
        bridge["status"] = json!("rejected");
        bridge["reason"] = json!("answer_invalid");
        record["bridge_receipt"] = bridge;
        assert!(is_valid(&record));
        record["bridge_receipt"]["requestHash"] = Value::Null;
        assert!(!is_valid(&record));
    }
}
