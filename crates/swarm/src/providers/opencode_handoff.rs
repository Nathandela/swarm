//! Source-bound, root-only OpenCode native history files for the hands-off prompt.
use super::{codex_receipt, opencode};
use crate::{ipc::field, supervisor::runtime};
use serde_json::{Value, json};
use std::{
    collections::BTreeSet,
    fs::{self, File, OpenOptions},
    io::{self, Read, Write},
    os::unix::fs::{MetadataExt, OpenOptionsExt},
    path::{Path, PathBuf},
};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
pub(crate) const MAX_BYTES: usize = 8 << 20;

pub(crate) fn validate(data: &Value, id: &Value) -> io::Result<()> {
    let info = &data["info"];
    if info["id"] != id["rootID"]
        || info["directory"] != id["directory"]
        || info["time"]["created"] != id["nativeCreatedAtMS"]
        || opencode::version(field(info, "version")).is_err()
    {
        return Err(io::Error::other(
            "OpenCode handoff native history identity or version is unconfirmed",
        ));
    }
    let messages = data["messages"]
        .as_array()
        .filter(|rows| rows.len() <= 4096)
        .ok_or_else(|| io::Error::other("OpenCode handoff history exceeds the message bound"))?;
    let mut message_ids = BTreeSet::new();
    let mut part_ids = BTreeSet::new();
    for row in messages {
        let message = &row["info"];
        let name = field(message, "id");
        if !opencode::valid_id(name, "msg_")
            || message["sessionID"] != id["rootID"]
            || !matches!(field(message, "role"), "user" | "assistant")
            || !message_ids.insert(name)
        {
            return Err(io::Error::other(
                "OpenCode handoff contains an unbound native message",
            ));
        }
        let parts = row["parts"]
            .as_array()
            .ok_or_else(|| io::Error::other("OpenCode handoff message is missing native parts"))?;
        for part in parts {
            let item = field(part, "id");
            if item.is_empty()
                || !part_ids.insert(item)
                || part["sessionID"] != id["rootID"]
                || part["messageID"] != name
                || field(part, "type").is_empty()
            {
                return Err(io::Error::other(
                    "OpenCode handoff contains an unbound native part",
                ));
            }
        }
    }
    if serde_json::to_vec(data)?.len() > MAX_BYTES {
        return Err(io::Error::other(
            "OpenCode handoff history exceeds the byte bound",
        ));
    }
    Ok(())
}

fn digest(data: &Value) -> String {
    let mut canonical = data.clone();
    canonical["swarm_provenance"]["captured_at"] = json!("0001-01-01T00:00:00Z");
    canonical.sort_all_objects();
    codex_receipt::hash(&canonical.to_string())
}

fn read(path: &Path) -> io::Result<(Value, Value)> {
    read_links(path, 1)
}

fn read_links(path: &Path, max_links: u64) -> io::Result<(Value, Value)> {
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    let info = file.metadata()?;
    // SAFETY: geteuid takes no arguments.
    if !info.is_file()
        || info.uid() != unsafe { libc::geteuid() }
        || info.mode() & 0o777 != 0o600
        || !(1..=max_links).contains(&info.nlink())
        || path.canonicalize()? != path
    {
        return Err(io::Error::other(
            "OpenCode handoff artifact is outside private owner custody",
        ));
    }
    let mut bytes = Vec::new();
    file.take(MAX_BYTES as u64 + 1).read_to_end(&mut bytes)?;
    if bytes.len() > MAX_BYTES {
        return Err(io::Error::other(
            "OpenCode handoff artifact exceeds the byte bound",
        ));
    }
    let observed = path.symlink_metadata()?;
    let identity = json!([info.dev(), info.ino()]);
    if identity != json!([observed.dev(), observed.ino()]) || !observed.is_file() {
        return Err(io::Error::other(
            "OpenCode handoff artifact changed during read",
        ));
    }
    Ok((serde_json::from_slice(&bytes)?, identity))
}

pub(crate) fn publish(
    source_dir: &Path,
    token: &str,
    id: &Value,
    version: &str,
    origin: &str,
    mut data: Value,
) -> io::Result<(PathBuf, Value)> {
    validate(&data, id)?;
    let rows = data["messages"].as_array_mut().expect("validated messages");
    rows.sort_by(|a, b| {
        a["info"]["time"]["created"]
            .as_i64()
            .cmp(&b["info"]["time"]["created"].as_i64())
            .then_with(|| field(&a["info"], "id").cmp(field(&b["info"], "id")))
    });
    data["swarm_provenance"] = json!({"identity":id,"source":origin,"native_version":version,
        "scope":"exact root; native child references remain in original parts",
        "captured_at":OffsetDateTime::now_utc().format(&Rfc3339).map_err(io::Error::other)?,
        "truncated":false,"mutable_during_read":true,
        "gaps":"native compaction and omitted native data are not reconstructed"});
    let hash = digest(&data);
    let root = source_dir
        .parent()
        .and_then(Path::parent)
        .ok_or_else(|| io::Error::other("OpenCode source state has no owner root"))?;
    let directory = root.join("handoff-exports");
    runtime::private_dir(&directory)?;
    if directory.canonicalize()? != directory {
        return Err(io::Error::other(
            "OpenCode handoff artifact directory is an alias",
        ));
    }
    File::open(root)?.sync_all()?;
    let path = directory.join(format!("native-history-{hash}.json"));
    let raw = serde_json::to_vec_pretty(&data)?;
    if raw.len() + 1 > MAX_BYTES {
        return Err(io::Error::other(
            "OpenCode handoff artifact exceeds the byte bound",
        ));
    }
    let _publication = runtime::lock(&directory.join(".publication.lock"))?;
    let temp = directory.join(format!(".pending-native-history-{hash}.json"));
    let staged = match read_links(&temp, 2) {
        Ok((raw, identity)) if digest(&raw) == hash => identity,
        Ok(_) => {
            return Err(io::Error::other(
                "OpenCode abandoned handoff staging differs",
            ));
        }
        Err(err) if err.kind() == io::ErrorKind::NotFound => {
            let mut file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&temp)?;
            file.write_all(&raw)?;
            file.write_all(b"\n")?;
            file.sync_all()?;
            let info = file.metadata()?;
            json!([info.dev(), info.ino()])
        }
        Err(err) => return Err(err),
    };
    let before = temp.symlink_metadata()?;
    if staged != json!([before.dev(), before.ino()]) {
        return Err(io::Error::other("OpenCode handoff staging changed"));
    }
    match read_links(&path, 2) {
        Ok((data, identity))
            if digest(&data) == hash && (before.nlink() == 1 || identity == staged) => {}
        Ok(_) => {
            return Err(io::Error::other(
                "existing OpenCode handoff artifact differs",
            ));
        }
        Err(err) if err.kind() == io::ErrorKind::NotFound && before.nlink() == 1 => {
            fs::hard_link(&temp, &path)?
        }
        Err(err) => return Err(err),
    }
    let after = temp.symlink_metadata()?;
    if staged != json!([after.dev(), after.ino()]) {
        return Err(io::Error::other(
            "OpenCode handoff staging changed before publication",
        ));
    }
    fs::remove_file(&temp)?;
    File::open(&directory)?.sync_all()?;
    let (saved, physical) = read(&path)?;
    if digest(&saved) != hash {
        return Err(io::Error::other(
            "existing OpenCode handoff artifact has different native history",
        ));
    }
    let cwd = Path::new(field(id, "directory"));
    let info = cwd.metadata()?;
    let proof = json!({"kind":"opencode","source_agent":"opencode","source_dir":source_dir,"instance":codex_receipt::instance(token),
        "cwd":cwd,"cwd_identity":[info.dev(),info.ino()],"transcript":path,
        "artifact_identity":physical,"digest":hash,"native_identity":id,"conversation":id["rootID"]});
    verify_artifact(&proof)?;
    Ok((path, proof))
}

pub(crate) fn verify_handoff(successor: &Path, lineage: &Value) -> io::Result<()> {
    let proof = &lineage["hands_off"];
    let id = field(lineage, "spawned_from");
    let source = Path::new(field(proof, "source_dir"));
    if !runtime::valid_id(id)
        || successor.parent().map(|root| root.join(id)).as_deref() != Some(source)
    {
        return Err(io::Error::other("OpenCode handoff source lineage changed"));
    }
    verify_artifact(proof)
}

pub(crate) fn verify_artifact(proof: &Value) -> io::Result<()> {
    let source = Path::new(field(proof, "source_dir"));
    let root = source
        .parent()
        .and_then(Path::parent)
        .ok_or_else(|| io::Error::other("unsafe OpenCode handoff source directory"))?;
    let path = Path::new(field(proof, "transcript"));
    let hash = field(proof, "digest");
    if proof["kind"] != "opencode"
        || !opencode::hex_hash(hash)
        || source.canonicalize()? != source
        || path
            != root
                .join("handoff-exports")
                .join(format!("native-history-{hash}.json"))
    {
        return Err(io::Error::other("invalid OpenCode handoff artifact path"));
    }
    let source_meta = runtime::read_json(&source.join("session.json"))?;
    let token = field(&source_meta, "token");
    if !runtime::valid_id(token)
        || proof["instance"] != codex_receipt::instance(token)
        || source_meta["provider"]["name"] != "opencode"
        || source_meta["provider"]["mode"] != "managed"
        || !source_meta["opencode_profile"].is_object()
    {
        return Err(io::Error::other("OpenCode managed source metadata changed"));
    }
    let native = opencode::receipts::native(source, token)?;
    let id = &proof["native_identity"];
    if [
        "rootID",
        "sessionInstance",
        "backendGeneration",
        "directory",
        "nativeCreatedAtMS",
    ]
    .into_iter()
    .any(|key| native[key] != id[key])
        || proof["cwd"] != id["directory"]
        || proof["conversation"] != id["rootID"]
        || source.file_name().and_then(|id| id.to_str()) != id["workerID"].as_str()
    {
        return Err(io::Error::other(
            "OpenCode handoff source native binding changed",
        ));
    }
    let cwd = Path::new(field(proof, "cwd"));
    let info = cwd.metadata()?;
    let (data, physical) = read(path)?;
    if proof["artifact_identity"] != physical
        || digest(&data) != hash
        || !info.is_dir()
        || cwd.canonicalize()? != cwd
        || proof["cwd_identity"] != json!([info.dev(), info.ino()])
        || data["swarm_provenance"]["identity"] != *id
        || data["swarm_provenance"]["truncated"] != false
    {
        return Err(io::Error::other(
            "OpenCode handoff source artifact or checkout changed",
        ));
    }
    validate(&data, id)
}

pub(crate) fn inherit_selection(launch: &mut Value, proof: &Value) -> io::Result<()> {
    if launch["agent"] != "opencode"
        || (launch["options"].get("model").is_some() && launch["options"].get("agent").is_some())
    {
        return Ok(());
    }
    verify_artifact(proof)?;
    let (data, _) = read(Path::new(field(proof, "transcript")))?;
    if digest(&data) != field(proof, "digest") {
        return Err(io::Error::other(
            "OpenCode handoff selection artifact changed",
        ));
    }
    let info = &data["info"];
    let agent = field(info, "agent");
    let model = &info["model"];
    if agent.is_empty() && model.is_null() {
        return Ok(());
    }
    let provider = field(model, "providerID");
    let name = field(model, "id");
    let valid = |value: &str, max| {
        !value.is_empty() && value.len() <= max && !value.chars().any(char::is_control)
    };
    if !valid(agent, 128) || !valid(provider, 128) || !valid(name, 256) {
        return Err(io::Error::other(
            "OpenCode committed native selection is unconfirmed",
        ));
    }
    let options = launch["options"]
        .as_object_mut()
        .ok_or_else(|| io::Error::other("OpenCode handoff launch options are invalid"))?;
    options
        .entry("model")
        .or_insert_with(|| json!(format!("{provider}/{name}")));
    options.entry("agent").or_insert_with(|| json!(agent));
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn authenticates_native_root_selection_and_recovers_pending_link() {
        let root = Path::new("/tmp")
            .canonicalize()
            .unwrap()
            .join(runtime::random_id().unwrap());
        let source = root.join("rust-v1/session");
        let cwd = root.join("work");
        runtime::private_dir(&source).unwrap();
        runtime::private_dir(&cwd).unwrap();
        let thread = "ses_0123456789";
        let generation = "0123456789abcdef0123456789abcdef";
        let id = json!({"version":1,"workerID":"session","rootID":thread,"directory":cwd,
            "sessionInstance":generation,"backendGeneration":generation,"nativeCreatedAtMS":123});
        let mut native = id.clone();
        native["token"] = json!("0123456789abcdef01234567");
        runtime::write_json(&source.join("opencode-native.json"), &native).unwrap();
        runtime::write_json(&source.join("session.json"),&json!({
            "token":"0123456789abcdef01234567","provider":{"name":"opencode","mode":"managed"},"opencode_profile":{}
        })).unwrap();
        let original = json!({"info":{"id":thread,"directory":cwd,"version":"1.19.3","time":{"created":123},
            "agent":"plan","model":{"providerID":"fixture","id":"historical"}},
            "messages":[{"info":{"id":"msg_0123456789","sessionID":thread,"role":"user",
              "time":{"created":124}},"parts":[{"id":"part_1","sessionID":thread,"messageID":"msg_0123456789","type":"text","text":"fixture"}]}]});
        let (path, proof) = publish(
            &source,
            "0123456789abcdef01234567",
            &id,
            "1.19.3",
            "live_sdk",
            original.clone(),
        )
        .unwrap();
        verify_artifact(&proof).unwrap();
        let mut successor = json!({"agent":"opencode","options":{"model":"explicit/model"}});
        inherit_selection(&mut successor, &proof).unwrap();
        assert_eq!(
            successor["options"],
            json!({"model":"explicit/model","agent":"plan"})
        );
        let staged = path.with_file_name(format!(
            ".pending-native-history-{}.json",
            field(&proof, "digest")
        ));
        fs::hard_link(&path, &staged).unwrap();
        assert!(verify_artifact(&proof).is_err());
        let recovered = publish(
            &source,
            "0123456789abcdef01234567",
            &id,
            "1.19.3",
            "live_sdk",
            original.clone(),
        )
        .unwrap();
        assert_eq!(recovered, (path.clone(), proof.clone()));
        assert!(!staged.exists());
        let bytes = fs::read(&path).unwrap();
        let mut forged: Value = serde_json::from_slice(&bytes).unwrap();
        forged["messages"][0]["parts"][0]["sessionID"] = json!("ses_foreign0000");
        assert!(validate(&forged, &id).is_err());
        fs::write(&path, serde_json::to_vec(&forged).unwrap()).unwrap();
        assert!(verify_artifact(&proof).is_err());
        fs::write(&path, bytes).unwrap();
        verify_artifact(&proof).unwrap();
        fs::remove_dir_all(root).unwrap();
    }
}
