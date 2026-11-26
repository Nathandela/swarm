use crate::{ipc::field, providers::codex_continuity, supervisor::runtime};
use serde_json::{Value, json};
use std::{io, path::Path};

pub(in crate::supervisor) const RELEASE: &str = "0.172.0";

pub(in crate::supervisor) fn legacy_identity(
    executable: &Path,
    version: &str,
    effective_identity: &Value,
) -> io::Result<Value> {
    if version != RELEASE {
        return Err(io::Error::other("unproved Codex rollback executable"));
    }
    super::legacy_identity(executable, version, effective_identity, "codex")
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
    let native = Path::new(field(original, "native_home"));
    let thread = field(exported, "conversation_id");
    let resumed = &source["codex_resume"];
    let rollout = if resumed.is_object() {
        let rollout = codex_continuity::go_rollout(native, cwd, thread, RELEASE)?;
        if resumed["profile"] != *original
            || resumed["thread"] != thread
            || resumed["source_cwd"] != json!(cwd)
            || resumed["rollout"] != rollout
            || !exported["auth_identity"].is_string()
            || crate::refresh::auth_identity("codex", &exported["env"]).as_deref()
                != exported["auth_identity"].as_str()
            || !codex_continuity::verify_observed_rollout(dir, token, thread, &rollout)?
        {
            return Err(io::Error::other(
                "Codex rollback resumed thread or account changed",
            ));
        }
        rollout
    } else {
        codex_continuity::rollout(native, cwd, thread)?
    };
    Ok(json!({"profile":effective,"rollout":rollout}))
}
