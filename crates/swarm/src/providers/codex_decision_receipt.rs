use super::codex_receipt;
use crate::ipc::field;
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::{
    io,
    path::{Path, PathBuf},
};

pub(crate) fn intent(body: &Value) -> String {
    let mut sorted = body.clone();
    sorted.sort_all_objects();
    codex_receipt::hash(&format!("codex-native-decision:{sorted}"))
}

fn path(dir: &Path, operation: &str) -> PathBuf {
    dir.join(format!(
        "codex-decision-{}.json",
        codex_receipt::hash(operation)
    ))
}

pub(crate) fn save(dir: &Path, operation: &str, intent: &str, receipt: &Value) -> io::Result<()> {
    runtime::write_json(
        &path(dir, operation),
        &json!({"intent":intent,"receipt":receipt}),
    )
}

pub(crate) fn read(
    dir: &Path,
    token: &str,
    session: &str,
    operation: &str,
) -> io::Result<Option<Value>> {
    let mut record = match runtime::read_json(&path(dir, operation)) {
        Ok(record) => record,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(err) => return Err(err),
    };
    let receipt = &record["receipt"];
    let instance = codex_receipt::instance(token);
    let root = dir
        .parent()
        .ok_or_else(|| io::Error::other("missing Codex runtime root"))?;
    let hex = |value: &str| value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit());
    if receipt["session"] != session
        || receipt["session_instance"] != instance
        || receipt["operation_id"] != operation
        || !codex_receipt::canonical_thread(field(receipt, "root_id"))
        || receipt["root_id"] != receipt["native_session_id"]
        || receipt["generation"] != codex_receipt::hash(&format!("codex-owner-requests:{token}"))
        || field(receipt, "request_id").is_empty()
        || !hex(field(receipt, "request_hash"))
        || !hex(field(&record, "intent"))
        || !matches!(
            field(receipt, "kind"),
            "permission_reply" | "question_reply"
        )
        || !(receipt["state"] == "applied" && receipt["native_effect"] == "single_request"
            || matches!(field(receipt, "state"), "uncertain" | "outcome_unknown")
                && receipt["native_effect"].is_null())
    {
        return Err(io::Error::other("uncorrelated Codex decision receipt"));
    }
    codex_receipt::verify_binding(
        root,
        operation,
        session,
        &instance,
        field(&record, "intent"),
    )?;
    if matches!(field(receipt, "state"), "applied" | "uncertain") {
        let closed = receipt["state"] == "applied";
        let receipt = record["receipt"]
            .as_object_mut()
            .expect("validated receipt");
        receipt.insert("state".into(), json!("outcome_unknown"));
        receipt.shift_remove("native_effect");
        if closed {
            receipt.insert(
                "reason".into(),
                json!("native request closed; responder and chosen decision are unconfirmed"),
            );
        }
    }
    Ok(Some(record))
}
