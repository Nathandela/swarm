use serde_json::{Value, json};
use sha2::{Digest, Sha256};
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
    format!(
        "{:x}",
        Sha256::digest(format!("codex-semantic-instance:{token}"))
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

pub(crate) fn save(dir: &Path, operation: &str, hash: &str, receipt: &Value) -> io::Result<()> {
    let path = path(dir, operation)?;
    crate::supervisor::runtime::private_dir(path.parent().expect("receipt directory"))?;
    crate::supervisor::runtime::write_json(&path, &json!({"intent":hash,"receipt":receipt}))
}

pub(crate) fn load(dir: &Path, operation: &str) -> io::Result<Option<Value>> {
    match crate::supervisor::runtime::read_json(&path(dir, operation)?) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
        Ok(record)
            if record["receipt"]["operation_id"] == operation
                && record["intent"]
                    .as_str()
                    .is_some_and(|hash| hash.len() == 64)
                && matches!(
                    record["receipt"]["state"].as_str(),
                    Some("native_accepted" | "outcome_unknown" | "rejected")
                ) =>
        {
            Ok(Some(record))
        }
        Ok(_) => Err(io::Error::other("damaged semantic receipt")),
        Err(error) => Err(error),
    }
}
