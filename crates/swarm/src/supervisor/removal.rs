//! Retire only an authenticated, exited session; retry from either durable namespace.
use super::{State, runtime};
use crate::ipc::field;
use serde_json::{Value, json};
use std::fs::{self, File};
use std::io::{self, ErrorKind};
use std::os::unix::fs::MetadataExt;
use std::path::Path;
use std::sync::Arc;

fn receipt(root: &Path, id: &str) -> io::Result<Option<Value>> {
    match runtime::read_json(&root.join("deleted").join(id)) {
        Ok(v)
            if v["schema"] == 1 && v["id"] == id && v["device"].is_u64() && v["inode"].is_u64() =>
        {
            Ok(Some(v))
        }
        Ok(_) => Err(io::Error::other("invalid deletion receipt")),
        Err(e) if e.kind() == ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e),
    }
}

// Returns true if this deletion is in progress or already complete.
fn resume(root: &Path, id: &str) -> io::Result<bool> {
    let path = root.join(format!(".deleting-{id}"));
    let record = receipt(root, id)?;
    if record.is_some() && root.join(id).try_exists()? {
        return Err(io::Error::other(
            "ambiguous active session and deletion receipt",
        ));
    }
    let info = match path.symlink_metadata() {
        Ok(meta) if meta.is_dir() => meta,
        Ok(_) => return Err(io::Error::other("unexpected retired session entry")),
        Err(e) if e.kind() == ErrorKind::NotFound => return Ok(record.is_some()),
        Err(e) => return Err(e),
    };
    if root.join(id).try_exists()? {
        return Err(io::Error::other(
            "ambiguous active and retired session directories",
        ));
    }
    if let Some(record) = record {
        if record["device"] != info.dev() || record["inode"] != info.ino() {
            return Err(io::Error::other("retired directory identity changed"));
        }
    } else {
        let meta = runtime::read_json(&path.join("session.json"))?;
        let token = field(&meta, "token");
        super::session::exit_report(&path, token)?;
        if meta["id"] != id
            || !super::session::owner_gone(&path, token)?
            || !runtime::valid_id(field(&meta, "token"))
        {
            return Err(io::Error::other("unproven retired session"));
        }
        File::open(root)?.sync_all()?;
        runtime::write_json(
            &root.join("deleted").join(id),
            &json!({"schema":1,"id":id,"device":info.dev(),"inode":info.ino()}),
        )?;
    }
    fs::remove_dir_all(&path)?;
    File::open(root)?.sync_all()?;
    Ok(true)
}

pub(super) fn recover(root: &Path) -> io::Result<()> {
    runtime::private_dir(&root.join("deleted"))?;
    for entry in fs::read_dir(root)? {
        let entry = entry?;
        let name = entry.file_name();
        if let Some(id) = name
            .to_str()
            .and_then(|s| s.strip_prefix(".deleting-"))
            .filter(|id| runtime::valid_id(id))
            && let Err(e) = resume(root, id)
        {
            eprintln!("daemon: retaining incomplete deletion for {id}: {e}");
        }
    }
    Ok(())
}

pub(super) fn delete(state: &State, request: &Value) -> io::Result<()> {
    let (endpoint, id) = field(request, "session_id")
        .split_once('/')
        .ok_or_else(|| io::Error::other("invalid session id"))?;
    if endpoint != state.endpoint || !runtime::valid_id(id) {
        return Err(io::Error::other("invalid session id"));
    }
    let session = {
        let _cleanup = state.deletion_gate.lock().unwrap();
        if resume(&state.root, id)? {
            state.sessions.write().unwrap().remove(id);
            return Ok(());
        }
        state.session(request)?
    };
    let stopped = session.confirm_exit();
    let _cleanup = state.deletion_gate.lock().unwrap();
    // A concurrent delete may have completed while we waited for the exact exit.
    if resume(&state.root, id)? {
        state.sessions.write().unwrap().remove(id);
        return Ok(());
    }
    stopped?;
    let mut sessions = state.sessions.write().unwrap();
    if !sessions.get(id).is_some_and(|s| Arc::ptr_eq(s, &session)) {
        return Err(io::Error::other("session changed during deletion"));
    }
    fs::rename(
        state.root.join(id),
        state.root.join(format!(".deleting-{id}")),
    )?;
    sessions.remove(id);
    drop(sessions);
    resume(&state.root, id).map(|_| ())
}
