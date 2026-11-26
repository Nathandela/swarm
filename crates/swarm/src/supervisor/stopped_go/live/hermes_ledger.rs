use super::super::{open_at, same_file};
use super::hermes::Viewer;
use crate::{
    ipc::field,
    providers::{codex_receipt, hermes::native::Admission},
    supervisor::runtime,
};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::HashMap,
    fs::{self, File},
    io::{self, BufRead, BufReader, Read, Write},
    os::unix::fs::MetadataExt,
    path::{Path, PathBuf},
};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

pub(in crate::supervisor) const OPT_IN: &str = "SWARM_DAEMON_GO_HERMES_SEMANTIC_COMPAT";
const MAX_RECORD: u64 = 16 << 20;
// shortcut: refuse large or torn logs, which the original Go supervisor can compact or repair.
const MAX_LEDGER: u64 = 128 << 20;

pub(in crate::supervisor) struct Ledger {
    path: PathBuf,
    directory: File,
    file: File,
    length: u64,
    records: HashMap<String, Value>,
}

impl Ledger {
    pub(in crate::supervisor) fn open(root: &Path) -> io::Result<Self> {
        let root_dir = File::open(root)?;
        let directory = open_at(
            &root_dir,
            c"idempotency",
            libc::O_RDONLY | libc::O_DIRECTORY,
        )?;
        let file = open_at(
            &directory,
            c"idempotency.log",
            libc::O_RDWR | libc::O_APPEND,
        )?;
        let mut ledger = Self {
            path: root.join("idempotency"),
            directory,
            length: file.metadata()?.len(),
            file,
            records: HashMap::new(),
        };
        ledger.validate()?;
        let mut input = BufReader::new(&ledger.file);
        let mut line = Vec::new();
        while input
            .by_ref()
            .take(MAX_RECORD + 1)
            .read_until(b'\n', &mut line)?
            != 0
        {
            if line.len() as u64 > MAX_RECORD || line.last() != Some(&b'\n') {
                return Err(io::Error::other(
                    "Go operation log has an oversized or incomplete row",
                ));
            }
            let record: Value = serde_json::from_slice(&line)?;
            let id = field(&record, "operation_id");
            if id.is_empty() {
                return Err(io::Error::other("Go operation log row has no operation ID"));
            }
            ledger.records.insert(id.to_owned(), record);
            line.clear();
        }
        ledger.validate()?;
        ledger.directory.sync_all()?;
        ledger.file.sync_all()?;
        Ok(ledger)
    }

    fn validate(&self) -> io::Result<()> {
        let directory = self.directory.metadata()?;
        let file = self.file.metadata()?;
        runtime::validate_file(&self.file)?;
        if !same_file(&directory, &fs::symlink_metadata(&self.path)?)
            || directory.uid() != file.uid()
            || directory.mode() & 0o077 != 0
            || !same_file(
                &file,
                &fs::symlink_metadata(self.path.join("idempotency.log"))?,
            )
            || file.len() != self.length
            || self.length > MAX_LEDGER
        {
            return Err(io::Error::other(
                "Go operation log changed or exceeds the qualification limit",
            ));
        }
        Ok(())
    }

    fn append(&mut self, mut record: Value) -> io::Result<()> {
        self.validate()?;
        record["updated_at"] = json!(
            OffsetDateTime::now_utc()
                .format(&Rfc3339)
                .map_err(io::Error::other)?
        );
        let mut line = serde_json::to_vec(&record)?;
        line.push(b'\n');
        let new_length = self.length + line.len() as u64;
        if line.len() > 4 << 20 || new_length > MAX_LEDGER {
            return Err(io::Error::other("Go operation log capacity reached"));
        }
        // A failed append or fsync leaves this writer fenced by the length check.
        self.file.write_all(&line)?;
        self.file.sync_all()?;
        self.length = new_length;
        self.records
            .insert(field(&record, "operation_id").to_owned(), record);
        self.validate()
    }

    pub(super) fn request(
        &mut self,
        viewer: &Viewer,
        instance: &str,
        request: &Value,
        revalidate: impl FnOnce() -> io::Result<()>,
    ) -> io::Result<Value> {
        self.validate()?;
        let session = field(request, "session_id");
        let local = field(&viewer.descriptor, "worker_id");
        let operation = field(request, "operation_id");
        let op = field(request, "op");
        if op == "semantic_target" {
            return Ok(json!({"op":op,"session_id":session,"semantic_target_instance":instance}));
        }
        if operation.len() > 128 || !operation.starts_with("semantic:") {
            return Err(io::Error::other("invalid semantic operation ID"));
        }
        let send = op == "semantic_send";
        let text = field(&request["composer_send"], "text");
        let prior = self.records.get(operation).cloned();
        if send {
            let body = &request["composer_send"];
            if request["body_version"] != 1
                || body["session"] != session
                || body["session_instance"] != instance
                || !codex_receipt::timely(operation)
                || text.is_empty()
                || text.len() > 4096
                || !matches!(body["expected_turn"], Value::Null | Value::String(_))
                || !field(body, "expected_turn").is_empty()
                || body.as_object().is_none_or(|object| {
                    object.iter().any(|(key, value)| {
                        !matches!(
                            key.as_str(),
                            "session" | "session_instance" | "text" | "expected_turn"
                        ) && !value.is_null()
                    })
                })
            {
                return Err(io::Error::other(
                    "invalid or stale Go Hermes semantic request",
                ));
            }
        }
        let hash = send.then(|| request_hash(session, instance, text));
        if let Some(record) = prior {
            if record["session_id"] != local
                || record["session_instance"] != instance
                || !matches!(field(&record, "action"), "semantic_send" | "composer_send")
                || send
                    && (record["action"] != "semantic_send"
                        || record["request_hash"].as_str() != hash.as_deref())
            {
                return Err(io::Error::other(
                    "Go operation is bound to another request or session",
                ));
            }
            let state = self.settle(viewer, record.clone())?;
            if !send {
                let mut receipt = json!({
                    "session":session,"session_instance":instance,"operation_id":operation,"state":state,
                });
                if hermes_intent(viewer, &record) {
                    receipt["native_session_id"] = json!(viewer.binding.id());
                    receipt["native_generation"] = viewer.descriptor["generation"].clone();
                }
                return Ok(
                    json!({"op":op,"session_id":session,"operation_id":operation,"semantic_receipt":receipt}),
                );
            }
            return Ok(send_reply(operation, state, &record["outcome"]));
        }
        if !send {
            return Err(io::Error::other("Go Hermes semantic operation is unknown"));
        }
        // Go uses the same last-write-wins records and treats every unresolved phase as an input fence.
        let mut unresolved = self.records.values().filter(|record| {
            record["session_id"] == local
                && matches!(field(record, "action"), "semantic_send" | "composer_send")
                && !matches!(field(record, "phase"), "prepared" | "completed" | "failed")
        });
        if let Some(record) = unresolved.next().cloned()
            && (unresolved.next().is_some()
                || record["session_instance"] != instance
                || self.settle(viewer, record)? != "submitted")
        {
            return Ok(send_reply(operation, "uncertain", &Value::Null));
        }
        if self.records.len() >= 100_000 {
            return Err(io::Error::other("Go operation log capacity reached"));
        }
        let now = OffsetDateTime::now_utc()
            .format(&Rfc3339)
            .map_err(io::Error::other)?;
        let mut record = json!({
            "operation_id":operation,"action":"semantic_send","session_id":local,
            "session_instance":instance,"request_hash":hash,"phase":"executing",
            "created_at":now,"updated_at":now,
            "request":{"composer_origin_version":1,"origin":null,"hermes_input":{
                "hash":codex_receipt::hash(text),"descriptor":viewer.descriptor,
            }},
        });
        self.append(record.clone())?;
        let state = match revalidate().and_then(|()| viewer.binding.submit(operation, text)) {
            Ok(Admission::Accepted(receipt)) => {
                complete(&mut record, receipt);
                if self.append(record.clone()).is_ok() {
                    "submitted"
                } else {
                    "uncertain"
                }
            }
            Ok(Admission::Rejected(code)) => {
                record["phase"] = json!("failed");
                record["outcome"] = json!({
                    "ok":false,"code":"input_busy","message":format!("hermes native admission refused the message (code {code})"),
                });
                if self.append(record.clone()).is_ok() {
                    "rejected"
                } else {
                    "uncertain"
                }
            }
            Err(_) => "uncertain",
        };
        Ok(send_reply(operation, state, &record["outcome"]))
    }

    fn settle(&mut self, viewer: &Viewer, mut record: Value) -> io::Result<&'static str> {
        match field(&record, "phase") {
            "prepared" => return Ok("prepared"),
            "failed" => return Ok("rejected"),
            "executing" | "outcome_unknown" | "completed" => {}
            _ => return Ok("uncertain"),
        }
        if !hermes_intent(viewer, &record) {
            return Ok("uncertain");
        }
        let operation = field(&record, "operation_id");
        let evidence = &record["evidence"];
        let mut receipt = evidence["receipt"].clone();
        // Go accepts the RPC-correlated response even if the gateway omits input_id.
        if receipt.is_object() && receipt["input_id"].is_null() {
            receipt["input_id"] = json!(operation);
        }
        if evidence["descriptor"] == viewer.descriptor
            && evidence["hash"] == record["request"]["hermes_input"]["hash"]
            && viewer.binding.admission(&receipt, operation)
        {
            if record["phase"] != "completed" {
                complete(&mut record, receipt);
                self.append(record)?;
            }
            return Ok("submitted");
        }
        if matches!(field(&record, "phase"), "executing" | "outcome_unknown")
            && let Ok(Some(receipt)) = viewer.binding.pending(operation)
        {
            complete(&mut record, receipt);
            self.append(record)?;
            return Ok("submitted");
        }
        Ok("uncertain")
    }
}

fn hermes_intent(viewer: &Viewer, record: &Value) -> bool {
    let intent = &record["request"];
    let hermes = &intent["hermes_input"];
    intent["composer_origin_version"] == 1
        && intent["origin"].is_null()
        && hermes["descriptor"] == viewer.descriptor
        && super::hermes::hex(field(hermes, "hash"), 64)
}

fn complete(record: &mut Value, receipt: Value) {
    let intent = &record["request"]["hermes_input"];
    record["evidence"] = json!({
        "hash":intent["hash"],"descriptor":intent["descriptor"],"receipt":receipt,
    });
    record["phase"] = json!("completed");
    record["outcome"] = json!({"ok":true});
}

fn send_reply(operation: &str, state: &str, outcome: &Value) -> Value {
    match state {
        "submitted" => json!({"op":"ok","operation_id":operation}),
        "rejected"
            if outcome["ok"] == false
                && !field(outcome, "code").is_empty()
                && !field(outcome, "message").is_empty() =>
        {
            json!({
                "op":"error","operation_id":operation,"error_code":outcome["code"],"error":format!("composer_send: {}", field(outcome, "message")),
            })
        }
        _ => {
            json!({"op":"error","operation_id":operation,"error_code":"outcome_unknown","error":"Hermes admission is unconfirmed; the request was not replayed"})
        }
    }
}

fn request_hash(session: &str, instance: &str, text: &str) -> String {
    let mut hash = Sha256::new();
    for value in [session, instance, "", text] {
        hash.update((value.len() as u32).to_be_bytes());
        hash.update(value.as_bytes());
    }
    format!("{:x}", hash.finalize())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_log_rolls_forward_and_rejects_torn_tail() -> io::Result<()> {
        let root = std::env::temp_dir().join(runtime::random_id()?);
        let directory = root.join("idempotency");
        runtime::private_dir(&root)?;
        runtime::private_dir(&directory)?;
        let path = directory.join("idempotency.log");
        runtime::write_atomic(&path, |file| {
            file.write_all(br#"{"operation_id":"first","phase":"completed"}"#)
        })?;
        assert!(Ledger::open(&root).is_err());
        fs::OpenOptions::new()
            .append(true)
            .open(&path)?
            .write_all(b"\n")?;
        let mut ledger = Ledger::open(&root)?;
        let record = json!({"operation_id":"next","phase":"executing"});
        ledger.append(record.clone())?;
        let mut terminal = record;
        terminal["phase"] = json!("completed");
        ledger.append(terminal)?;
        drop(ledger);
        let reopened = Ledger::open(&root)?;
        assert_eq!(reopened.records["next"]["phase"], "completed");
        fs::remove_dir_all(root)?;
        Ok(())
    }
}
