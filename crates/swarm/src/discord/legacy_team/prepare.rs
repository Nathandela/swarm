//! Give Go the original connector's sending row while leaving its controller untouched.
use super::*;
use crate::discord::{legacy_discussions::operation, legacy_ingress};
use crate::ipc::field;
use bbolt_rs::{BucketApi, BucketRwApi, DbRwAPI, TxRwApi, TxRwRefApi};
use std::{
    fs,
    io::Read,
    os::unix::fs::MetadataExt,
    path::{Path, PathBuf},
};

fn unavailable() -> io::Error {
    invalid("original teammate manager or sending boundary is unavailable")
}

fn keys(value: &Value, allowed: &[&str]) -> bool {
    value
        .as_object()
        .is_some_and(|row| row.keys().all(|key| allowed.contains(&key.as_str())))
}

pub(super) fn verify_config(file: File, cfg: &Config) -> io::Result<()> {
    let mut original = Vec::new();
    file.take((1 << 20) + 1).read_to_end(&mut original)?;
    let rendered = crate::ipc::escape_json(serde_json::to_string_pretty(&cfg.0)?);
    if original.strip_suffix(b"\n") != Some(rendered.as_bytes())
        || !keys(
            &cfg.0,
            &[
                "project_access",
                "supervisor",
                "mode",
                "manager_cli",
                "projects",
                "enabled",
                "version",
                "installation_id",
                "name",
                "application_id",
                "owner_id",
                "token_file",
                "daemon_socket",
                "guild_ids",
                "channel_ids",
                "user_ids",
                "receptionist_cli",
                "receptionist_model",
                "work_dir",
            ],
        )
        || !keys(&cfg.0["supervisor"], &["state_dir", "lock", "log"])
    {
        return Err(unavailable());
    }
    Ok(())
}

pub(super) fn verify_ledger<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    route: &Route,
    guard: &Guard,
) -> io::Result<()> {
    let install = cfg.text("installation_id");
    for (bucket, suffix, allowed) in [
        (
            "teammate_discussions",
            route.channel_id.as_str(),
            &[
                "ProvisionalCancelled",
                "InputNoticeID",
                "InputNoticeAt",
                "StartupNoticePending",
                "StartupNoticeID",
                "StartupNoticeGeneration",
                "ID",
                "InstallationID",
                "supervisor_socket",
                "Route",
                "Starter",
                "ParentAnchor",
                "Gap",
                "Opened",
                "LastIngressSequence",
                "Boundary",
                "RepairUpper",
                "RepairLower",
                "RepairAfter",
                "RepairEpoch",
                "RepairTargetEpoch",
                "NotifiedEpoch",
                "ParentLow",
                "Error",
            ][..],
        ),
        (
            "teammate_meta",
            "checkpoint",
            &[
                "WorkerMailIssue",
                "Session",
                "GatewaySequence",
                "GapEpoch",
                "Error",
            ][..],
        ),
        (
            "teammate_ingress_guard",
            guard.route.message_id.as_str(),
            &[
                "RequireMention",
                "Route",
                "ActorID",
                "OperationID",
                "Revision",
                "DiscussionID",
                "Disposition",
                "LastChange",
            ][..],
        ),
    ] {
        let source = tx.bucket(bucket).ok_or_else(unavailable)?;
        let raw = source
            .get(format!("{install}\0{suffix}"))
            .ok_or_else(unavailable)?;
        let row: Value = serde_json::from_slice(raw)?;
        if !keys(&row, allowed)
            || raw != crate::ipc::escape_json(serde_json::to_string(&row)?).as_bytes()
            || ["Route", "Starter"].iter().any(|key| {
                row.get(key).is_some_and(|value| {
                    !keys(
                        value,
                        &["guild_id", "channel_id", "message_id", "parent_id"],
                    )
                })
            })
        {
            return Err(unavailable());
        }
    }
    Ok(())
}

pub(super) struct Manager {
    directory: PathBuf,
    id: String,
    binding: Value,
    voice: File,
    auth: File,
    auth_hash: String,
    socket: PathBuf,
    controller: LockDescription,
}

impl Manager {
    pub(super) fn claim(place: &Place, cfg: &Config, discussion: &Value) -> io::Result<Self> {
        let (directory, id, endpoint) = legacy_ingress::voice_location(place, cfg, discussion)?;
        let root = fs::canonicalize("/tmp")?
            .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
            .join(&id[..32]);
        if fs::canonicalize(&root)? != root {
            return Err(unavailable());
        }
        runtime::private_dir(&root)?;
        let socket = root.join("bridge.sock");
        let lock = root.join("controller.lock");
        let saved = open_owner_file(&lock)?.metadata()?;
        let controller = LockDescription(runtime::lock(&lock)?);
        let held = controller.0.metadata()?;
        if (saved.dev(), saved.ino()) != (held.dev(), held.ino()) {
            return Err(unavailable());
        }
        open_owner_file(&directory.join("voice.initialized"))?;
        let auth = open_owner_file(&directory.join("bridge.auth"))?;
        let mut credential = Vec::new();
        (&auth).take(65).read_to_end(&mut credential)?;
        if credential.len() != 64
            || !credential
                .iter()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(b))
        {
            return Err(unavailable());
        }
        let auth_hash = format!("{:x}", Sha256::digest(credential));
        let (voice, db) = open_bolt(&directory.join("voice.db"), 64 << 20)?;
        let binding = legacy_ingress::prepare::read_voice(
            &db.begin().map_err(io::Error::other)?,
            &id,
            cfg,
            &endpoint,
        )?
        .conversation["binding"]
            .clone();
        let manager = Self {
            directory,
            id,
            binding,
            voice,
            auth,
            auth_hash,
            socket,
            controller,
        };
        manager.verify(place, cfg)?;
        Ok(manager)
    }

    pub(super) fn verify(&self, place: &Place, cfg: &Config) -> io::Result<()> {
        if !legacy_state::unchanged(&self.voice, &self.directory.join("voice.db"))?
            || !legacy_state::unchanged(&self.auth, &self.directory.join("bridge.auth"))?
            || legacy_state::file_digest(
                &open_owner_file(&self.directory.join("bridge.auth"))?,
                64,
                Instant::now() + Duration::from_secs(1),
            )? != self.auth_hash
            || !legacy_state::unchanged(
                &self.controller.0,
                &self.socket.with_file_name("controller.lock"),
            )?
            || !matches!(fs::symlink_metadata(&self.socket), Err(e) if e.kind() == io::ErrorKind::NotFound)
        {
            return Err(unavailable());
        }
        legacy_ingress::activation::verify_idle_manager(
            place,
            cfg,
            &self.directory,
            &self.id,
            &self.binding,
        )
    }
}

pub(super) fn commit(
    source: &Path,
    expected: &fs::Metadata,
    cfg: &Config,
    pending: &capture::Pending,
    discussion: &Value,
    recheck: impl FnOnce(&Route, &Guard) -> io::Result<()>,
) -> io::Result<()> {
    let open = legacy_state::writable_pin(source)?;
    let current = open.metadata()?;
    if (expected.dev(), expected.ino()) != (current.dev(), current.ino()) {
        return Err(unavailable());
    }
    let (pin, mut db) = legacy_state::bounded_writer(open)?;
    let mut tx = db.begin_rw().map_err(io::Error::other)?;
    if capture::pending(&tx, cfg)?.as_ref() != Some(pending) {
        return Err(unavailable());
    }
    let (route, guard) = read_scope(&tx, cfg, Some(pending))?;
    verify_ledger(&tx, cfg, &route, &guard)?;
    if capture::binding(&tx, cfg, &route, &guard, Some(pending))? != *discussion {
        return Err(unavailable());
    }
    recheck(&route, &guard)?;
    if !legacy_state::unchanged(&pin, source)? {
        return Err(unavailable());
    }
    let mut bucket = tx.bucket_mut(b"teammate_ingress").ok_or_else(unavailable)?;
    let mut row: Value =
        serde_json::from_slice(bucket.get(&pending.ingress_key).ok_or_else(unavailable)?)?;
    let candidate = &pending.candidate;
    let message = field(&candidate.route, "message_id");
    let actor = field(&candidate.author, "id");
    let authority = if actor == cfg.text("owner_id") {
        "owner_direct"
    } else {
        "external"
    };
    let mut bound = json!({});
    for key in [
        "ID",
        "InstallationID",
        "supervisor_socket",
        "Route",
        "Starter",
        "ParentAnchor",
        "Gap",
    ] {
        bound[key] = discussion[key].clone();
    }
    row["State"] = json!("sending");
    row["Admission"] = json!({"RequireMention":false,"Supersedes":"","Discussion":bound,
        "OperationID":operation(cfg.text("installation_id"), message)?,"Revision":candidate.revision,
        "Text":"","ReplyTo":"","QuestionID":"","QuestionHash":"","Route":candidate.route,
        "Author":candidate.author,"InvokerID":actor,"Authority":authority,"Sequence":pending.sequence});
    row.as_object_mut()
        .ok_or_else(unavailable)?
        .remove("CaptureRevision");
    bucket
        .put(&pending.ingress_key, serde_json::to_vec(&row)?)
        .map_err(io::Error::other)?;
    tx.commit().map_err(io::Error::other)
}

pub(super) fn ready_discussion(discussion: &Value) -> io::Result<()> {
    if discussion["Gap"] != discussion["RepairEpoch"]
        || discussion["NotifiedEpoch"] != discussion["RepairEpoch"]
        || discussion["Error"] != ""
        || !field(discussion, "StartupNoticeID").is_empty()
        || !field(discussion, "InputNoticeID").is_empty()
    {
        return Err(unavailable());
    }
    Ok(())
}
