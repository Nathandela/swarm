use crate::{
    ipc::field,
    providers::{self, codex_continuity},
    supervisor::runtime,
};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{io, os::unix::fs::MetadataExt, path::Path};

pub(in crate::supervisor) const RELEASE: &str = "0.172.0";

pub(in crate::supervisor) fn legacy_identity(
    executable: &Path,
    version: &str,
    effective_identity: &Value,
) -> io::Result<Value> {
    let invalid = || io::Error::other("unproved Codex rollback executable");
    if version != RELEASE
        || !executable.is_absolute()
        || executable.file_name().and_then(|name| name.to_str()) != Some("codex")
        || executable.canonicalize()? != executable
    {
        return Err(invalid());
    }
    let before = executable.metadata()?;
    if before.mode() & 0o7000 != 0 || &providers::identity(executable)? != effective_identity {
        return Err(invalid());
    }
    let unix_ns = i128::from(before.mtime()) * 1_000_000_000 + i128::from(before.mtime_nsec());
    // The pinned Go persisted CLI identity uses os.FileMode's ordinary permissions
    // and the physical launch path. Keep the Rust admission identity separate.
    let legacy = format!(
        "{}:{}:{}:{}:{}:{}",
        executable.display(),
        before.dev(),
        before.ino(),
        before.size(),
        unix_ns,
        before.mode() & 0o777,
    );
    Ok(json!({"path":executable,"version":version,
        "fingerprint":format!("{:x}",Sha256::digest(legacy.as_bytes()))}))
}

pub(super) fn resume_proof(dir: &Path, exported: &Value, token: &str) -> io::Result<Value> {
    for name in [
        "semantic",
        "codex-thread-changed.json",
        "context-guard.json",
    ] {
        match dir.join(name).symlink_metadata() {
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
            Ok(_) => {
                return Err(io::Error::other(
                    "Codex rollback cannot transfer Rust-native operation or continuity state",
                ));
            }
        }
    }
    let source = runtime::read_json(&dir.join("session.json"))?;
    let record = runtime::read_json(&dir.join("codex-profile.json"))?;
    let provider = &source["provider"];
    let original = &record["profile"];
    let cwd = Path::new(field(exported, "cwd"));
    if source["token"] != token
        || record["token"] != token
        || !original.is_object()
        || source["go_rollback"]["codex_profile"] != *original
        || source["codex_resume"].is_object()
        || legacy_identity(
            Path::new(field(provider, "executable")),
            field(provider, "version"),
            &provider["identity"],
        )? != exported["cli_identity"]
    {
        return Err(io::Error::other("unproved stopped Codex rollback source"));
    }
    let env: Vec<(String, String)> = exported["env"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .filter_map(|entry| entry.split_once('='))
        .map(|(name, value)| (name.to_owned(), value.to_owned()))
        .collect();
    let home = Path::new(field(original, "home"));
    if std::env::var_os("HOME").as_deref() != Some(home.as_os_str())
        || original["native_home"] != json!(home.join(".codex"))
    {
        return Err(io::Error::other(
            "Codex rollback requires the original default native home",
        ));
    }
    let effective = codex_continuity::profile(&env, cwd, &exported["launch_options"])?;
    if !codex_continuity::same_origin(original, &effective)
        || original["account"] != effective["account"]
        || original["options"] != effective["options"]
        || original["path"] != effective["path"]
    {
        return Err(io::Error::other(
            "Codex rollback native origin or authority changed",
        ));
    }
    let rollout = codex_continuity::rollout(
        Path::new(field(original, "native_home")),
        cwd,
        field(exported, "conversation_id"),
    )?;
    Ok(json!({"profile":effective,"rollout":rollout}))
}
