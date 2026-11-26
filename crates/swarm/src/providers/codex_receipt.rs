use crate::supervisor::runtime;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::fs::File;
use std::io::{self, Read};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

fn unix_ms() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
}

pub(crate) fn operation() -> io::Result<String> {
    let mut nonce = [0; 16];
    std::fs::File::open("/dev/urandom")?.read_exact(&mut nonce)?;
    let token: String = nonce.iter().map(|byte| format!("{byte:02x}")).collect();
    Ok(format!("semantic:{}:{token}", unix_ms()))
}

fn issued(id: &str) -> Option<u128> {
    let mut parts = id.split(':');
    if parts.next()? != "semantic" {
        return None;
    }
    let epoch = parts.next()?;
    let at: u128 = epoch.parse().ok()?;
    let nonce = parts.next()?;
    if at == 0
        || epoch != at.to_string()
        || epoch.len() > 13
        || nonce.len() != 32
        || !nonce
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        || parts.next().is_some()
    {
        return None;
    }
    Some(at)
}

pub(crate) fn timely(id: &str) -> bool {
    issued(id)
        .is_some_and(|at| at <= unix_ms() + 30_000 && unix_ms().saturating_sub(at) <= 3_600_000)
}

pub(crate) fn instance(token: &str) -> String {
    provider_instance("codex", token)
}

pub(crate) fn provider_instance(agent: &str, token: &str) -> String {
    format!(
        "{:x}",
        Sha256::digest(format!("{agent}-semantic-instance:{token}"))
    )
}

pub(crate) fn hash(text: &str) -> String {
    format!("{:x}", Sha256::digest(text))
}

fn path(dir: &Path, operation: &str) -> io::Result<PathBuf> {
    if issued(operation).is_none() {
        return Err(io::Error::other("invalid semantic operation ID"));
    }
    Ok(dir
        .join("semantic")
        .join(format!("{:x}.json", Sha256::digest(operation))))
}

pub(crate) fn canonical_thread(id: &str) -> bool {
    id.len() == 36
        && id.bytes().enumerate().all(|(index, ch)| {
            if matches!(index, 8 | 13 | 18 | 23) {
                ch == b'-'
            } else {
                hex(ch)
            }
        })
}

fn hex(ch: u8) -> bool {
    ch.is_ascii_digit() || (b'a'..=b'f').contains(&ch)
}

fn durable_dir(dir: &Path) -> io::Result<()> {
    runtime::private_dir(dir)?;
    File::open(
        dir.parent()
            .ok_or_else(|| io::Error::other("missing semantic parent directory"))?,
    )?
    .sync_all()
}

pub(crate) fn bind(
    root: &Path,
    operation: &str,
    target: &str,
    incarnation: &str,
    text: &str,
) -> io::Result<()> {
    bind_intent(root, operation, target, incarnation, &hash(text))
}

pub(crate) fn bind_intent(
    root: &Path,
    operation: &str,
    target: &str,
    incarnation: &str,
    intent: &str,
) -> io::Result<()> {
    if !timely(operation) {
        return Err(io::Error::other("invalid semantic operation ID"));
    }
    let dir = root.join("operations");
    durable_dir(root)?;
    durable_dir(&dir)?;
    let path = dir.join(format!("semantic-{:x}.json", Sha256::digest(operation)));
    let binding =
        json!({"operation":operation,"session":target,"instance":incarnation,"intent":intent});
    match runtime::read_json(&path) {
        Ok(saved) if saved == binding => Ok(()),
        Ok(_) => Err(io::Error::other(
            "semantic operation is already bound to another request",
        )),
        Err(err) if err.kind() == io::ErrorKind::NotFound => runtime::write_json(&path, &binding),
        Err(err) => Err(err),
    }
}

pub(crate) fn verify_binding(
    root: &Path,
    operation: &str,
    target: &str,
    incarnation: &str,
    intent: &str,
) -> io::Result<()> {
    if issued(operation).is_none() {
        return Err(io::Error::other("invalid semantic operation ID"));
    }
    let path = root
        .join("operations")
        .join(format!("semantic-{:x}.json", Sha256::digest(operation)));
    let stored = runtime::read_json(&path)?;
    let expected =
        json!({"operation":operation,"session":target,"instance":incarnation,"intent":intent});
    if stored != expected {
        return Err(io::Error::other(
            "semantic receipt has no exact operation binding",
        ));
    }
    Ok(())
}

pub(crate) fn fence(
    dir: &Path,
    operation: &str,
    session: &str,
    token: &str,
    thread: &str,
) -> io::Result<()> {
    fence_for(dir, operation, session, token, thread, "codex")
}

pub(crate) fn fence_for(
    dir: &Path,
    operation: &str,
    session: &str,
    token: &str,
    thread: &str,
    agent: &str,
) -> io::Result<()> {
    let semantic = dir.join("semantic");
    durable_dir(&semantic)?;
    let pending = semantic.join("pending.json");
    require_resolved(dir, session, token, thread, agent)?;
    runtime::write_json(&pending, &json!(operation))
}

pub(crate) fn require_resolved(
    dir: &Path,
    session: &str,
    token: &str,
    thread: &str,
    agent: &str,
) -> io::Result<()> {
    let pending = dir.join("semantic/pending.json");
    match runtime::read_json(&pending) {
        Ok(value) => {
            let prior = value
                .as_str()
                .ok_or_else(|| io::Error::other("damaged pending semantic record"))?;
            let receipt = load_for(dir, prior, session, token, thread, agent)?;
            if !receipt.is_some_and(|record| {
                matches!(
                    record["receipt"]["state"].as_str(),
                    Some("native_accepted" | "submitted" | "rejected")
                )
            }) {
                return Err(io::Error::other(
                    "an earlier semantic operation remains unresolved",
                ));
            }
        }
        Err(err) if err.kind() == io::ErrorKind::NotFound => {}
        Err(err) => return Err(err),
    }
    Ok(())
}
pub(crate) fn save(dir: &Path, operation: &str, hash: &str, receipt: &Value) -> io::Result<()> {
    let path = path(dir, operation)?;
    durable_dir(path.parent().expect("receipt directory"))?;
    runtime::write_json(&path, &json!({"intent":hash,"receipt":receipt}))
}

fn valid(
    record: &Value,
    operation: &str,
    session: &str,
    token: &str,
    thread: &str,
    agent: &str,
) -> bool {
    let receipt = &record["receipt"];
    matches!(agent, "agy" | "codex" | "claude")
        && receipt["operation_id"] == operation
        && receipt["session"] == session
        && receipt["session_instance"] == provider_instance(agent, token)
        && receipt["agent"] == agent
        && canonical_thread(thread)
        && receipt["native_session_id"] == thread
        && record["intent"]
            .as_str()
            .is_some_and(|hash| hash.len() == 64 && hash.bytes().all(hex))
        && (agent == "codex" || super::agy_input::origin(&receipt["origin"]).is_ok())
        && (agent == "codex"
            || receipt["native_generation"].as_str().is_some_and(|id| {
                if agent == "agy" {
                    runtime::valid_id(id)
                } else {
                    id.len() == 32 && id.bytes().all(hex)
                }
            }))
        && match receipt["state"].as_str() {
            Some("submitted") if matches!(agent, "agy" | "claude") => {
                receipt["native_turn_id"].is_null()
            }
            Some("native_accepted") if agent == "codex" => {
                receipt["native_turn_id"].as_str().is_some_and(|id| {
                    !id.is_empty() && id.len() <= 128 && !id.chars().any(char::is_control)
                })
            }
            Some("outcome_unknown" | "rejected") => receipt["native_turn_id"].is_null(),
            _ => false,
        }
}

pub(crate) fn load(
    dir: &Path,
    operation: &str,
    session: &str,
    token: &str,
    thread: &str,
) -> io::Result<Option<Value>> {
    load_for(dir, operation, session, token, thread, "codex")
}

pub(crate) fn load_for(
    dir: &Path,
    operation: &str,
    session: &str,
    token: &str,
    thread: &str,
    agent: &str,
) -> io::Result<Option<Value>> {
    match runtime::read_json(&path(dir, operation)?) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
        Ok(record) if valid(&record, operation, session, token, thread, agent) => Ok(Some(record)),
        Ok(_) => Err(io::Error::other("damaged semantic receipt")),
        Err(error) => Err(error),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepted_receipt_requires_all_authority_fields_and_native_turn() {
        let thread = "01997f00-face-7000-8000-00000000cde0";
        let mut record = json!({"intent":hash("message"),"receipt":{
            "operation_id":"fixture","session":"ep/s","session_instance":instance("token"),
            "native_session_id":thread,"agent":"codex","state":"native_accepted","native_turn_id":"turn",
        }});
        assert!(valid(&record, "fixture", "ep/s", "token", thread, "codex"));
        assert!(!valid(
            &record, "fixture", "ep/other", "token", thread, "codex"
        ));
        assert!(!valid(
            &record,
            "fixture",
            "ep/s",
            "other-token",
            thread,
            "codex"
        ));
        record["receipt"]["native_turn_id"] = Value::Null;
        assert!(!valid(&record, "fixture", "ep/s", "token", thread, "codex"));
        record["receipt"]["state"] = json!("outcome_unknown");
        assert!(valid(&record, "fixture", "ep/s", "token", thread, "codex"));
        record["intent"] = json!("z".repeat(64));
        assert!(!valid(&record, "fixture", "ep/s", "token", thread, "codex"));
    }
}
