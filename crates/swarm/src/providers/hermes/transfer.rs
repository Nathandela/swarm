//! Successor authority begins when its exact viewer owner record is durable.
use super::native;
use crate::{ipc::field, supervisor::runtime};
use serde_json::{Value, json};
use std::{fs::File, io, path::Path};

const SUCCESSOR: &str = "hermes-successor.json";

pub(crate) fn lock(source: &Path) -> io::Result<File> {
    // Opening the private predecessor lock is also the startup fence for its shim.
    runtime::lock(&source.join("hermes-transfer.lock"))
}

pub(crate) fn reservation(worker: &str, token: &str, binding: &Value) -> Value {
    json!({"worker":worker,"token":token,"binding":binding})
}

pub(crate) fn prepare(source: &Path, record: &Value) -> io::Result<()> {
    runtime::write_json(&source.join(SUCCESSOR), record)
}

pub(crate) fn owned(source: &Path, binding: &Value) -> io::Result<bool> {
    let record = match runtime::read_json(&source.join(SUCCESSOR)) {
        Ok(record) if record.is_null() => return Ok(false),
        Ok(record) => record,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(false),
        Err(err) => return Err(err),
    };
    let worker = field(&record, "worker");
    let token = field(&record, "token");
    if !runtime::valid_id(worker) || !runtime::valid_id(token) || record["binding"] != *binding {
        return Err(io::Error::other("invalid Hermes transfer reservation"));
    }
    let successor = source
        .parent()
        .ok_or_else(|| io::Error::other("missing Hermes session root"))?
        .join(worker);
    // Before owner.json exists the predecessor can revoke the reservation while
    // holding the lock. The successor must acquire it before opening its viewer.
    let owner = match runtime::read_json(&successor.join("owner.json")) {
        Ok(owner) => owner,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(false),
        Err(err) => return Err(err),
    };
    let meta = runtime::read_json(&successor.join("session.json"))?;
    let descriptor = native::read_descriptor(&successor, &owner, &meta)?;
    if meta["id"] != worker || meta["token"] != token || descriptor["binding"] != *binding {
        return Err(io::Error::other("unproven Hermes successor custody"));
    }
    Ok(true)
}

pub(crate) fn start(
    root: &Path,
    predecessor: &str,
    worker: &str,
    token: &str,
    binding: &Value,
) -> io::Result<File> {
    if !runtime::valid_id(predecessor) || worker == predecessor {
        return Err(io::Error::other("invalid Hermes resume predecessor"));
    }
    let source = root.join(predecessor);
    let custody = lock(&source)?;
    let owner = runtime::read_json(&source.join("owner.json"))?;
    let meta = runtime::read_json(&source.join("session.json"))?;
    if let Some(pid) = owner["pid"]
        .as_u64()
        .and_then(|pid| u32::try_from(pid).ok())
        && runtime::birth(pid).ok() == owner["birth"].as_u64()
        && !runtime::read_json(&source.join("exit.json"))
            .is_ok_and(|exit| exit["token"] == meta["token"])
    {
        return Err(io::Error::other(
            "Hermes predecessor viewer still has custody",
        ));
    }
    let descriptor = native::read_descriptor(&source, &owner, &meta)?;
    if meta["id"] != predecessor
        || descriptor["binding"] != *binding
        || runtime::read_json(&source.join(SUCCESSOR))? != reservation(worker, token, binding)
    {
        return Err(io::Error::other("Hermes viewer transfer was superseded"));
    }
    Ok(custody)
}
