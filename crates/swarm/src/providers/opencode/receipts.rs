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
    let receipt = &record["receipt"];
    if record["token"] != token
        || receipt["session"] != session
        || receipt["operation_id"] != operation
        || receipt["session_instance"] != native["sessionInstance"]
        || receipt["root_id"] != native["rootID"]
        || receipt["generation"] != native["backendGeneration"]
        || receipt["native_created_at_ms"] != native["nativeCreatedAtMS"]
        || !matches!(
            field(receipt, "state"),
            "outcome_unknown"
                | "native_accepted"
                | "rejected"
                | "completed"
                | "cancelled"
                | "declined"
                | "failed"
                | "uncertain"
                | "admitted"
        )
    {
        return Err(io::Error::other("unverified OpenCode operation receipt"));
    }
    Ok(Some(record))
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
