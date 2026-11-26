//! Settle a Go personal ingress candidate from the original voice controller's cutover receipt.
pub(super) mod activation;
mod grants;
pub(super) mod manager_publication;
pub(super) mod prepare;
pub(super) mod publication;
mod queue;
pub(super) mod service;
pub(super) mod source_attachment;
mod wake;
mod wake_intent;

use super::{
    config::{Config, LockDescription, Place, PlaceLock, open_owner_file, snowflake},
    legacy_discussions::operation,
    legacy_state::{bounded_writer, verify_controls_place, writable_pin},
};
use crate::{checkout::state::receipts::open_bolt, ipc::field, supervisor, supervisor::runtime};
use bbolt_rs::{Bolt, BucketApi, BucketRwApi, DbApi, DbRwAPI, TxApi, TxRwApi, TxRwRefApi};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    fs, io,
    os::unix::fs::MetadataExt,
    path::{Path, PathBuf},
};

fn invalid() -> io::Error {
    io::Error::other("original personal ingress custody or receipt is unqualified")
}

fn value<'tx>(tx: &impl TxApi<'tx>, bucket: &str, key: &[u8]) -> io::Result<Option<Value>> {
    tx.bucket(bucket)
        .ok_or_else(invalid)?
        .get(key)
        .map(|raw| {
            if raw.len() > 1 << 20 {
                return Err(invalid());
            }
            serde_json::from_slice(raw).map_err(io::Error::other)
        })
        .transpose()
}

struct Candidate {
    key: Vec<u8>,
    active_key: Vec<u8>,
    pending_key: Vec<u8>,
    binding: Value,
    guard: Value,
    origin: Value,
    pending_count: u64,
}

fn candidate<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    message: &str,
) -> io::Result<Option<Candidate>> {
    let install = cfg.text("installation_id");
    let owner = cfg.text("owner_id");
    let channel = cfg.text("dm_channel_id");
    let expected = operation(install, message)?;
    let key = format!("{install}\0{message}").into_bytes();
    let active_key = format!("{install}\0{channel}").into_bytes();
    let Some(row) = value(tx, "teammate_ingress", &key)? else {
        let guard = value(tx, "teammate_ingress_guard", &key)?.ok_or_else(invalid)?;
        if guard["Disposition"] == "sent"
            && guard["OperationID"] == expected
            && guard["ActorID"] == owner
            && guard["Route"]["channel_id"] == channel
            && guard["Route"]["message_id"] == message
            && guard["Route"]["guild_id"] == ""
        {
            return Ok(None);
        }
        return Err(invalid());
    };
    let ad = &row["Admission"];
    let route = &row["Route"];
    let discussion = &ad["Discussion"];
    let sequence = row["Sequence"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(invalid)?;
    let pending_key = [install.as_bytes(), b"\0", &sequence.to_be_bytes()].concat();
    let queue = tx.bucket(b"teammate_pending").ok_or_else(invalid)?;
    let count = tx
        .bucket(b"teammate_meta")
        .ok_or_else(invalid)?
        .get(b"active_ingress_count")
        .and_then(|n| <[u8; 8]>::try_from(n).ok())
        .map(u64::from_be_bytes)
        .filter(|n| *n > 0 && *n <= 32768)
        .ok_or_else(invalid)?;
    if queue.iter_buckets().next().is_some()
        || queue.iter_entries().take(32_769).count() as u64 != count
    {
        return Err(invalid());
    }
    let binding = value(tx, "teammate_discussions", &active_key)?.ok_or_else(invalid)?;
    let id = field(discussion, "ID");
    let ids = tx.bucket(b"teammate_discussion_ids").ok_or_else(invalid)?;
    let active = tx
        .bucket(b"teammate_active_discussions")
        .ok_or_else(invalid)?;
    let revision = field(ad, "Revision");
    if !matches!(field(&row, "State"), "sending" | "accepted")
        || row["Deleted"] != false
        || row["Edited"] != false
        || row["Author"]["id"] != owner
        || route["channel_id"] != channel
        || route["message_id"] != message
        || route["guild_id"] != ""
        || !route["parent_id"].is_null()
        || ad["Route"] != *route
        || ad["Author"]["id"] != owner
        || ad["InvokerID"] != owner
        || ad["Authority"] != "owner_direct"
        || ad["Sequence"] != sequence
        || ad["OperationID"] != expected
        || ad["RequireMention"] != false
        || !field(ad, "Supersedes").is_empty()
        || !ad["AttachmentManifest"].is_null()
        || !ad["QuestionID"].as_str().is_some_and(str::is_empty)
        || revision.len() != 64
        || !revision
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        || discussion["InstallationID"] != install
        || id.is_empty()
        || binding["ID"] != id
        || binding["InstallationID"] != install
        || binding["Route"] != discussion["Route"]
        || binding["Route"]["channel_id"] != channel
        || binding["Route"]["guild_id"] != ""
        || !binding["Route"]["parent_id"].is_null()
        || (!binding["supervisor_socket"].is_null()
            && binding["supervisor_socket"] != cfg.text("daemon_socket"))
        || binding["StartupNoticePending"] != false
        || binding["Opened"] != true
        || binding["ProvisionalCancelled"] != false
        || ids.get(format!("{install}\0{id}").as_bytes()) != Some(active_key.as_slice())
        || active.get(&active_key) != Some([1].as_slice())
        || queue.get(&pending_key) != Some(key.as_slice())
        || value(tx, "teammate_ingress_guard", &key)?.is_some()
    {
        return Err(invalid());
    }
    let name = field(&ad["Author"], "name");
    let mut origin = json!({"kind":"owner_direct","installation_id":install,"request_id":expected,
        "actor_id":owner,"requester_id":owner,"channel_id":channel,"message_id":message,
        "discussion_id":id,"revision":revision});
    if !name.is_empty() {
        origin["actor_name"] = json!(name);
    }
    let guard = json!({"RequireMention":false,"Route":route,"ActorID":owner,"OperationID":expected,
        "Revision":revision,"DiscussionID":id,"Disposition":"sent","LastChange":""});
    Ok(Some(Candidate {
        key,
        active_key,
        pending_key,
        binding,
        guard,
        origin,
        pending_count: count,
    }))
}

struct Receipt {
    header: Value,
    binding: Value,
    conversation: Value,
    admission: Value,
    key: Vec<u8>,
    raw: Vec<u8>,
}

fn record_hash(key: &[u8], raw: &[u8]) -> [u8; 32] {
    let mut hash = Sha256::new();
    hash.update(key);
    hash.update([0]);
    hash.update(raw);
    hash.finalize().into()
}

fn controller(
    path: &Path,
    id: &str,
    origin: &Value,
    cfg: &Config,
    endpoint: &str,
) -> io::Result<(fs::File, impl DbApi + use<>, Receipt)> {
    let dir = fs::symlink_metadata(path)?;
    if !dir.is_dir() || dir.uid() != unsafe { libc::geteuid() } || dir.mode() & 0o077 != 0 {
        return Err(invalid());
    }
    open_owner_file(&path.join("voice.initialized"))?;
    let voice_db = path.join("voice.db");
    let (pin, db) = open_bolt(&voice_db, 64 << 20)?;
    let receipt = inspect_voice(
        &db.begin().map_err(io::Error::other)?,
        id,
        origin,
        cfg,
        endpoint,
    )?;
    Ok((pin, db, receipt))
}

fn inspect_voice<'tx>(
    tx: &impl TxApi<'tx>,
    id: &str,
    origin: &Value,
    cfg: &Config,
    endpoint: &str,
) -> io::Result<Receipt> {
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let header = root.get(b"state").ok_or_else(invalid)?;
    if header.len() > 4096
        || root.get(b"digest") != Some(format!("{:x}", Sha256::digest(header)).as_bytes())
    {
        return Err(invalid());
    }
    let header: Value = serde_json::from_slice(header)?;
    let conversation = format!("discord-teammate-{id}");
    if !matches!(header["version"].as_u64(), Some(3 | 4))
        || header["current_conversation"] != conversation
    {
        return Err(invalid());
    }
    let bucket = root.bucket(b"records").ok_or_else(invalid)?;
    let mut sum = [0u8; 32];
    let mut count = 0;
    let mut admission = None;
    let mut binding = None;
    for (key, raw) in bucket.iter_entries() {
        count += 1;
        if count > 4096 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        for (byte, digest) in sum.iter_mut().zip(record_hash(key, raw)) {
            *byte ^= digest;
        }
        if key == format!("conversations\0{conversation}").as_bytes() {
            let entry: Value = serde_json::from_slice(raw)?;
            let b = entry["binding"].clone();
            if b["conversation_id"] != conversation
                || !native_cli_matches(&b["native_cli"], retained_vm(cfg))
                || b["provider"] != cfg.text("manager_cli")
                || !field(&b, "session_id").starts_with(&format!("{endpoint}/"))
                || [
                    "session_instance",
                    "native_session_id",
                    "native_generation",
                    "observation_generation",
                    "login_epoch",
                ]
                .iter()
                .any(|key| field(&b, key).is_empty())
            {
                return Err(invalid());
            }
            binding = Some((b, entry));
        }
        if key.starts_with(b"admissions\0") {
            let entry: Value = serde_json::from_slice(raw)?;
            if entry["origin"]["request_id"] != origin["request_id"] {
                continue;
            }
            let sequence = entry["sequence"]
                .as_u64()
                .filter(|n| *n > 0)
                .ok_or_else(invalid)?;
            let index = serde_json::to_string(&[conversation.as_str(), &sequence.to_string()])?;
            if admission.is_some()
                || entry["origin"] != *origin
                || entry["conversation_id"] != conversation
                || entry["source_id"] != format!("discussion:{}", field(origin, "request_id"))
                || key != format!("admissions\0{index}").as_bytes()
                || !matches!(entry["source_cutover"], Value::Bool(_) | Value::Null)
                || field(&entry, "operation_id").is_empty()
                || !matches!(
                    field(&entry, "phase"),
                    "prepared"
                        | "executing"
                        | "submitted"
                        | "admitted"
                        | "running"
                        | "uncertain"
                        | "completed"
                        | "rejected"
                )
            {
                return Err(invalid());
            }
            admission = Some((entry, key.to_vec(), raw.to_vec()));
        }
    }
    let (admission, key, raw) = admission.ok_or_else(invalid)?;
    let (binding, conversation) = binding.ok_or_else(invalid)?;
    let request = &admission["request"];
    if header["records"] != count
        || header["record_hash"] != json!(sum)
        || request["origin"] != *origin
        || request["session"] != binding["session_id"]
        || request["session_instance"] != binding["session_instance"]
        || request["manager_admission"]["nonce"] != admission["native_input_id"]
        || field(&admission, "native_input_id").len() != 64
    {
        return Err(invalid());
    }
    Ok(Receipt {
        header,
        binding,
        conversation,
        admission,
        key,
        raw,
    })
}

fn identity(file: &fs::File, path: &Path) -> io::Result<bool> {
    let a = file.metadata()?;
    let b = fs::symlink_metadata(path)?;
    Ok((a.dev(), a.ino()) == (b.dev(), b.ino()))
}

fn scope(place: &Place, message: &str) -> io::Result<Config> {
    if !snowflake(message) {
        return Err(invalid());
    }
    retained_config(place)
}

fn retained_config(place: &Place) -> io::Result<Config> {
    let cfg = personal_enrollment(place)?;
    match fs::symlink_metadata(place.dir.join("bridge.sock")) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(cfg),
        Err(error) => Err(error),
        Ok(_) => Err(invalid()),
    }
}

fn native_cli_matches(flag: &Value, vm: bool) -> bool {
    if vm {
        flag == true
    } else {
        matches!(flag, Value::Null | Value::Bool(false))
    }
}

fn retained_vm(cfg: &Config) -> bool {
    cfg.text("project_access") == "vm"
}

fn selected_personal(place: &Place) -> io::Result<Config> {
    let current = place.load()?;
    let cfg = personal_config(place, retained_vm(&current))?;
    if current.0 != cfg.0 || !activation::selected(place)? {
        return Err(invalid());
    }
    Ok(cfg)
}

fn current_turn_enrollment(place: &Place, cfg: &Config) -> io::Result<()> {
    if personal_config(place, retained_vm(cfg))?.0 != cfg.0
        || (retained_vm(cfg) && !activation::selected(place)?)
    {
        return Err(io::Error::other(
            "the original personal enrollment changed during dispatch",
        ));
    }
    Ok(())
}

#[derive(Clone, Copy)]
enum VmAuthority {
    Activated,
    Explicit,
}

impl VmAuthority {
    fn verify(self, place: &Place, cfg: &Config) -> io::Result<()> {
        match self {
            Self::Activated => current_turn_enrollment(place, cfg),
            Self::Explicit => {
                if personal_config(place, true)?.0 != cfg.0 {
                    return Err(invalid());
                }
                Ok(())
            }
        }
    }
}

fn personal_enrollment(place: &Place) -> io::Result<Config> {
    personal_config(place, false)
}

fn personal_config(place: &Place, vm: bool) -> io::Result<Config> {
    let cfg = place.load()?;
    verify_controls_place(place, &cfg)?;
    if cfg.text("mode") != "personal"
        || (cfg.text("project_access") == "vm") != vm
        || !cfg.0["calls"].is_null()
        || !cfg.0["previous_supervisor"].is_null()
    {
        return Err(invalid());
    }
    Ok(cfg)
}

struct ConnectorLease {
    db: Bolt,
    source: PathBuf,
    pin: fs::File,
    _bridge: LockDescription,
    _configuration: PlaceLock,
}

impl ConnectorLease {
    fn open(place: &Place, cfg: &Config, expected: &fs::Metadata) -> io::Result<Self> {
        Self::acquire(place, cfg, expected, false)
    }

    fn acquire(
        place: &Place,
        cfg: &Config,
        expected: &fs::Metadata,
        foreground: bool,
    ) -> io::Result<Self> {
        let configuration = place.lock()?;
        let bridge = LockDescription(runtime::lock(&place.dir.join("bridge.lock"))?);
        let current = if foreground {
            personal_config(place, cfg.text("project_access") == "vm")?
        } else {
            retained_config(place)?
        };
        if current.0 != cfg.0 {
            return Err(invalid());
        }
        let source = place.dir.join("discord.db");
        let original = writable_pin(&source)?;
        let found = original.metadata()?;
        if (expected.dev(), expected.ino()) != (found.dev(), found.ino()) {
            return Err(invalid());
        }
        let (pin, db) = bounded_writer(original)?;
        if !identity(&pin, &source)? {
            return Err(invalid());
        }
        Ok(Self {
            _configuration: configuration,
            _bridge: bridge,
            source,
            pin,
            db,
        })
    }

    fn source_unchanged(&self) -> io::Result<bool> {
        identity(&self.pin, &self.source)
    }
}

fn voice_location(
    place: &Place,
    cfg: &Config,
    binding: &Value,
) -> io::Result<(PathBuf, String, String)> {
    let root = fs::canonicalize(place.supervisor_root())?;
    let endpoint = supervisor::endpoint(&root);
    let parent = fs::canonicalize(place.dir.join("discussions"))?;
    let parent_path = parent.to_str().ok_or_else(invalid)?;
    if parent != fs::canonicalize(&place.dir)?.join("discussions") {
        return Err(invalid());
    }
    let coordinate = [
        parent_path,
        endpoint.as_str(),
        cfg.text("installation_id"),
        "",
        cfg.text("dm_channel_id"),
        field(binding, "ID"),
    ]
    .join("\0");
    let id = format!("{:x}", Sha256::digest(coordinate.as_bytes()));
    let directory = parent.join(&id);
    if fs::canonicalize(&directory)? != directory {
        return Err(invalid());
    }
    Ok((directory, id, endpoint))
}

pub(super) fn settle(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    let cfg = scope(place, message)?;
    let source = place.dir.join("discord.db");
    let source_identity = fs::symlink_metadata(&source)?;
    let stage = |c: Candidate| {
        let (directory, id, endpoint) = voice_location(place, &cfg, &c.binding)?;
        let (pin, voice, receipt) = controller(&directory, &id, &c.origin, &cfg, &endpoint)?;
        if receipt.admission["source_cutover"] != true {
            return Err(invalid());
        }
        let phase = field(&receipt.admission, "phase").to_owned();
        Ok::<_, io::Error>((c, pin, voice, directory.join("voice.db"), phase))
    };
    {
        let (_hold, original) = open_bolt(&source, 64 << 20)?;
        let tx = original.begin().map_err(io::Error::other)?;
        let Some(row) = candidate(&tx, &cfg, message)? else {
            println!("Original personal ingress is already settled.");
            return Ok(true);
        };
        let (_, _, _, _, phase) = stage(row)?;
        if dry_run {
            println!(
                "Original voice controller has sealed this personal ingress. Native phase is {phase}."
            );
            return Ok(true);
        }
    }
    let mut lease = ConnectorLease::open(place, &cfg, &source_identity)?;
    settle_under_lease(place, &cfg, message, &mut lease)
}

fn settle_under_lease(
    place: &Place,
    cfg: &Config,
    message: &str,
    lease: &mut ConnectorLease,
) -> io::Result<bool> {
    let mut tx = lease.db.begin_rw().map_err(io::Error::other)?;
    let Some(row) = candidate(&tx, cfg, message)? else {
        println!("Original personal ingress is already settled.");
        return Ok(true);
    };
    let (directory, id, endpoint) = voice_location(place, cfg, &row.binding)?;
    let (voice, _voice_db, receipt) = controller(&directory, &id, &row.origin, cfg, &endpoint)?;
    let phase = field(&receipt.admission, "phase");
    let mut c = row;
    if receipt.admission["source_cutover"] != true
        || !identity(&lease.pin, &lease.source)?
        || !identity(&voice, &directory.join("voice.db"))?
    {
        return Err(invalid());
    }
    let boundary = c.binding["Boundary"].as_str().ok_or_else(invalid)?;
    let existing = if boundary.is_empty() {
        0
    } else {
        boundary.parse::<u64>().map_err(|_| invalid())?
    };
    if message.parse::<u64>().map_err(|_| invalid())? > existing {
        c.binding["Boundary"] = json!(message);
        tx.bucket_mut(b"teammate_discussions")
            .ok_or_else(invalid)?
            .put(&c.active_key, serde_json::to_vec(&c.binding)?)
            .map_err(io::Error::other)?;
    }
    tx.bucket_mut(b"teammate_ingress_guard")
        .ok_or_else(invalid)?
        .put(&c.key, serde_json::to_vec(&c.guard)?)
        .map_err(io::Error::other)?;
    tx.bucket_mut(b"teammate_meta")
        .ok_or_else(invalid)?
        .put(b"active_ingress_count", (c.pending_count - 1).to_be_bytes())
        .map_err(io::Error::other)?;
    tx.bucket_mut(b"teammate_pending")
        .ok_or_else(invalid)?
        .delete(&c.pending_key)
        .map_err(io::Error::other)?;
    tx.bucket_mut(b"teammate_ingress")
        .ok_or_else(invalid)?
        .delete(&c.key)
        .map_err(io::Error::other)?;
    tx.commit().map_err(io::Error::other)?;
    println!(
        "Settled original personal ingress from its voice controller receipt. Native phase remains {phase}."
    );
    Ok(true)
}

fn original_revision(message: &Value, origin: &Value) -> io::Result<String> {
    let content = message["content"]
        .as_str()
        .filter(|s| !s.is_empty())
        .ok_or_else(invalid)?;
    if message["id"] != origin["message_id"]
        || message["channel_id"] != origin["channel_id"]
        || message["author"]["id"] != origin["actor_id"]
        || message["type"] != 0
        || message["author"].get("bot").is_some_and(|b| b != false)
        || message["author"].get("system").is_some_and(|b| b != false)
        || !message["guild_id"].is_null()
        || !message["edited_timestamp"].is_null()
        || !message["message_reference"].is_null()
        || !message["webhook_id"].is_null()
        || !message["attachments"].as_array().is_some_and(Vec::is_empty)
    {
        return Err(invalid());
    }
    let tuple = json!([
        message["author"]["id"],
        content,
        message["channel_id"],
        "",
        message["id"],
        null,
        "",
        null
    ]);
    let canonical = crate::ipc::escape_json(serde_json::to_string(&tuple)?);
    Ok(format!("{:x}", Sha256::digest(canonical.as_bytes())))
}

fn validate_prepared(receipt: &Receipt, cfg: &Config) -> io::Result<()> {
    let admission = &receipt.admission;
    let active = &receipt.conversation;
    let request = &admission["request"];
    let runtime_binding = &request["manager_admission"]["binding"];
    let mut expected = receipt.binding.clone();
    let fields = expected.as_object_mut().ok_or_else(invalid)?;
    fields.remove("native_cli");
    fields.remove("generation");
    let session = fields.remove("session_id").ok_or_else(invalid)?;
    fields.insert("session".into(), session);
    if admission["phase"] != "prepared"
        || !matches!(
            admission["source_cutover"],
            Value::Null | Value::Bool(false)
        )
        || admission["recovery_only"] == true
        || admission["native_binding_retired"] == true
        || !matches!(admission["project_explicit"], Value::Bool(_) | Value::Null)
        || (!field(admission, "project").is_empty()
            && !grants::project(cfg, field(admission, "project")))
        || !field(admission, "waiting_on_input").is_empty()
        || active["scheduler_paused"] == true
        || active["custody_lost"] == true
        || active["parked"] == true
        || !active["retirement"].is_null()
        || active["external_observed"] != true
        || admission["sequence"] != active["last_admission"]
        || active["retired_through"].as_u64().unwrap_or(0)
            >= admission["sequence"].as_u64().ok_or_else(invalid)?
        || request["manager_admission"]["expected_external_input"].as_u64()
            != Some(active["external_input"].as_u64().unwrap_or_default())
        || runtime_binding != &expected
    {
        return Err(invalid());
    }
    let operation = field(admission, "operation_id");
    let fields: Vec<_> = operation.split(':').collect();
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_err(io::Error::other)?
        .as_millis();
    if !matches!(fields.as_slice(), ["semantic", time, id]
        if time.parse::<u128>().is_ok_and(|time| (now.saturating_sub(3_600_000)..=now+30_000).contains(&time))
            && id.len() == 32 && id.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)))
    {
        return Err(invalid());
    }
    Ok(())
}

fn persist_cutover<'tx>(tx: impl TxRwApi<'tx>, receipt: &mut Receipt) -> io::Result<()> {
    receipt.admission["phase"] = json!("executing");
    receipt.admission["source_cutover"] = json!(true);
    let mut records = vec![(receipt.key.clone(), receipt.admission.clone())];
    if !field(&receipt.admission, "project").is_empty() {
        receipt.conversation["project"] = receipt.admission["project"].clone();
        let conversation = field(&receipt.binding, "conversation_id");
        records.push((
            format!("conversations\0{conversation}").into_bytes(),
            receipt.conversation.clone(),
        ));
    }
    manager_publication::apply_voice(tx, receipt.header.clone(), &records)
}

pub(super) fn dispatch(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    dispatch_with(place, message, dry_run, |cfg| capture_source(cfg, message))
}

fn dispatch_with(
    place: &Place,
    message: &str,
    dry_run: bool,
    fetch: impl FnOnce(&Config) -> io::Result<Value>,
) -> io::Result<bool> {
    let cfg = scope(place, message)?;
    let source = place.dir.join("discord.db");
    let expected = fs::symlink_metadata(&source)?;
    if dry_run {
        let (_pin, db) = open_bolt(&source, 64 << 20)?;
        let c = pending_dispatch(&db, &cfg, message)?;
        let (directory, id, endpoint) = voice_location(place, &cfg, &c.binding)?;
        let (_pin, _voice, receipt) = controller(&directory, &id, &c.origin, &cfg, &endpoint)?;
        validate_prepared(&receipt, &cfg)?;
        println!("Verified a prepared original personal DM with no native cutover recorded.");
        return Ok(true);
    }
    let mut lease = ConnectorLease::open(place, &cfg, &expected)?;
    dispatch_under_lease(place, &cfg, message, &mut lease, fetch)
}

fn pending_dispatch(db: &impl DbApi, cfg: &Config, message: &str) -> io::Result<Candidate> {
    let tx = db.begin().map_err(io::Error::other)?;
    let c = candidate(&tx, cfg, message)?.ok_or_else(invalid)?;
    require_repaired(&tx, cfg, &c.binding)?;
    Ok(c)
}

fn dispatch_under_lease(
    place: &Place,
    cfg: &Config,
    message: &str,
    lease: &mut ConnectorLease,
    fetch: impl FnOnce(&Config) -> io::Result<Value>,
) -> io::Result<bool> {
    let c = pending_dispatch(&lease.db, cfg, message)?;
    let (directory, id, endpoint) = voice_location(place, cfg, &c.binding)?;
    let (voice_pin, voice_db, receipt) = controller(&directory, &id, &c.origin, cfg, &endpoint)?;
    validate_prepared(&receipt, cfg)?;
    let voice_identity = voice_pin.metadata()?;
    drop(voice_db);
    drop(voice_pin);
    let voice_path = directory.join("voice.db");
    let (write_pin, mut writer) = bounded_writer(writable_pin(&voice_path)?)?;
    let written_identity = write_pin.metadata()?;
    if (written_identity.dev(), written_identity.ino())
        != (voice_identity.dev(), voice_identity.ino())
    {
        return Err(invalid());
    }
    let tx = writer.begin_rw().map_err(io::Error::other)?;
    let mut read = inspect_voice(&tx, &id, &c.origin, cfg, &endpoint)?;
    validate_prepared(&read, cfg)?;
    if read.raw != receipt.raw {
        return Err(invalid());
    }
    let caps = ["semantic-send", "manager-provenance", "message-origin"];
    let (mut owner, rows) = super::migrate_supervisor::verified_owner(
        place.supervisor_root(),
        Path::new(cfg.text("daemon_socket")),
        Path::new(field(&cfg.0["supervisor"], "lock")),
        &caps,
    )?;
    cfg.check_supervisor(&owner, &caps)?;
    let request = read.admission["request"].clone();
    let binding = &request["manager_admission"]["binding"];
    if rows
        .iter()
        .filter(|row| {
            row["id"] == request["session"]
                && row["role"] == "manager"
                && row["manager_conversation_id"] == binding["conversation_id"]
                && row["capabilities"]["session_instance"] == request["session_instance"]
                && row["status"]["Process"] == "running"
                && row["agent"] == cfg.text("manager_cli")
                && row["cwd"] == cfg.text("work_dir")
        })
        .count()
        != 1
    {
        return Err(invalid());
    }
    let bridge_socket = fs::canonicalize("/tmp")?
        .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
        .join(&id[..32])
        .join("bridge.sock");
    let current = owner.request_body(json!({"op":"manager_runtime","session_id":request["session"],
        "body_version":1,"manager_runtime_request":{"session_instance":request["session_instance"]}}), "manager_runtime")?;
    let current = &current["manager_runtime"];
    if current["binding"] != request["manager_admission"]["binding"]
        || !native_cli_matches(&current["native_cli"], retained_vm(cfg))
        || current["ready_for_input"] != true
        || current["provider_exhausted"] == true
        || current["turn"]["externally_steered"] == true
        || !matches!(field(&current["turn"], "state"), "idle" | "completed")
        || current["turn"]["external_input"]
            != request["manager_admission"]["expected_external_input"]
        || current["auth_file"] != json!(directory.join("bridge.auth"))
        || current["bridge_socket"] != json!(bridge_socket)
    {
        return Err(invalid());
    }
    open_owner_file(&directory.join("bridge.auth"))?;
    let fresh = fetch(cfg)?;
    if original_revision(&fresh, &c.origin)? != c.origin["revision"]
        || !identity(&write_pin, &voice_path)?
        || !lease.source_unchanged()?
    {
        return Err(invalid());
    }
    current_turn_enrollment(place, cfg)?;
    validate_prepared(&read, cfg)?;
    let operation = field(&read.admission, "operation_id").to_owned();
    persist_cutover(tx, &mut read)?;
    let reply = owner
        .exchange(
            json!({
                "op":"semantic_send","session_id":request["session"],"body_version":1,
                "operation_id":operation,"composer_send":request
            }),
            "ok",
            std::time::Duration::from_secs(35),
        )
        .map_err(|_| {
            io::Error::other(
                "native result unresolved; original Go controller retained the exact operation",
            )
        })?;
    if reply["operation_id"] != operation || reply["endpoint_id"] != owner.endpoint() {
        return Err(io::Error::other(
            "native result unresolved; original Go controller retained the exact operation",
        ));
    }
    println!(
        "Native input acknowledged. The original Go controller retains the operation for reconciliation."
    );
    Ok(true)
}

fn capture_source(cfg: &Config, message: &str) -> io::Result<Value> {
    let api = super::rest::Discord::connect(cfg)?;
    api.verify_app(cfg)?;
    api.owner_message(cfg, message)?.ok_or_else(invalid)
}

pub(super) fn capture(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    capture_with(place, message, dry_run, capture_source)
}

fn capture_with(
    place: &Place,
    message: &str,
    dry_run: bool,
    fetch: impl FnOnce(&Config, &str) -> io::Result<Value>,
) -> io::Result<bool> {
    let cfg = scope(place, message)?;
    let source = place.dir.join("discord.db");
    let identity = fs::symlink_metadata(&source)?;
    let install = cfg.text("installation_id");
    let channel = cfg.text("dm_channel_id");
    let key = format!("{install}\0{message}").into_bytes();
    let active_key = format!("{install}\0{channel}").into_bytes();
    {
        let (_pin, db) = open_bolt(&source, 64 << 20)?;
        let tx = db.begin().map_err(io::Error::other)?;
        if capture_binding(&tx, &cfg, message, &key, &active_key)?.is_none() {
            println!("Original Go ingress already knows this personal DM.");
            return Ok(true);
        }
    }
    if dry_run {
        println!("Original Go ingress can accept a new owner DM after source verification.");
        return Ok(true);
    }
    let mut lease = ConnectorLease::open(place, &cfg, &identity)?;
    capture_under_lease(&cfg, message, &mut lease, fetch)
}

fn capture_under_lease(
    cfg: &Config,
    message: &str,
    lease: &mut ConnectorLease,
    fetch: impl FnOnce(&Config, &str) -> io::Result<Value>,
) -> io::Result<bool> {
    let install = cfg.text("installation_id");
    let channel = cfg.text("dm_channel_id");
    let actor = cfg.text("owner_id");
    let key = format!("{install}\0{message}").into_bytes();
    let active_key = format!("{install}\0{channel}").into_bytes();
    let mut tx = lease.db.begin_rw().map_err(io::Error::other)?;
    let Some(mut binding) = capture_binding(&tx, cfg, message, &key, &active_key)? else {
        return Ok(true);
    };
    let input = fetch(cfg, message)?;
    original_revision(
        &input,
        &json!({"message_id":message,"channel_id":channel,"actor_id":actor}),
    )?;
    let name = input["author"]["username"]
        .as_str()
        .filter(|name| !name.is_empty() && name.len() <= 256 && !name.chars().any(char::is_control))
        .ok_or_else(invalid)?;
    if !identity(&lease.pin, &lease.source)? {
        return Err(invalid());
    }
    let sequence = {
        let mut ingress = tx.bucket_mut(b"teammate_ingress").ok_or_else(invalid)?;
        let n = ingress.next_sequence().map_err(io::Error::other)?;
        if n == 0
            || n <= binding["LastIngressSequence"]
                .as_u64()
                .ok_or_else(invalid)?
        {
            return Err(invalid());
        }
        let captured = json!({"Route":{"guild_id":"","channel_id":channel,"message_id":message},
            "Author":{"id":actor,"name":name},"Opening":false,"Edited":false,"Deleted":false,
            "Mutation":0,"Sequence":n,"State":"prepared","Error":"","Admission":null});
        ingress
            .put(&key, serde_json::to_vec(&captured)?)
            .map_err(io::Error::other)?;
        n
    };
    binding["LastIngressSequence"] = json!(sequence);
    tx.bucket_mut(b"teammate_discussions")
        .ok_or_else(invalid)?
        .put(&active_key, serde_json::to_vec(&binding)?)
        .map_err(io::Error::other)?;
    let pending_key = [install.as_bytes(), b"\0", &sequence.to_be_bytes()].concat();
    tx.bucket_mut(b"teammate_pending")
        .ok_or_else(invalid)?
        .put(pending_key, &key)
        .map_err(io::Error::other)?;
    tx.bucket_mut(b"teammate_meta")
        .ok_or_else(invalid)?
        .put(b"active_ingress_count", 1u64.to_be_bytes())
        .map_err(io::Error::other)?;
    tx.commit().map_err(io::Error::other)?;
    println!("Captured the personal DM in the original Go ingress queue.");
    Ok(true)
}

fn require_repaired<'tx>(tx: &impl TxApi<'tx>, cfg: &Config, binding: &Value) -> io::Result<()> {
    let key = format!("{}\0checkpoint", cfg.text("installation_id"));
    let checkpoint = value(tx, "teammate_meta", key.as_bytes())?;
    let gap = checkpoint
        .as_ref()
        .map_or(Ok(0), |cp| cp["GapEpoch"].as_u64().ok_or_else(invalid))?;
    let repair = match &binding["PersonalRepairEpoch"] {
        Value::Null => 0,
        epoch => epoch.as_u64().ok_or_else(invalid)?,
    };
    if repair < gap {
        return Err(io::Error::other(
            "original Go personal history repair is still pending",
        ));
    }
    Ok(())
}

fn capture_binding<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    message: &str,
    key: &[u8],
    active_key: &[u8],
) -> io::Result<Option<Value>> {
    let install = cfg.text("installation_id");
    let channel = cfg.text("dm_channel_id");
    let actor = cfg.text("owner_id");
    let active = tx
        .bucket(b"teammate_active_discussions")
        .ok_or_else(invalid)?;
    let ids = tx.bucket(b"teammate_discussion_ids").ok_or_else(invalid)?;
    let binding = value(tx, "teammate_discussions", active_key)?.ok_or_else(invalid)?;
    let id = field(&binding, "ID");
    if id.is_empty()
        || id.len() > 512
        || id.contains('\0')
        || binding["InstallationID"] != install
        || binding["Route"]["channel_id"] != channel
        || binding["Route"]["guild_id"] != ""
        || !binding["Route"]["parent_id"].is_null()
        || binding["Opened"] != true
        || binding["ProvisionalCancelled"] != false
        || binding["StartupNoticePending"] != false
        || (!binding["supervisor_socket"].is_null()
            && binding["supervisor_socket"] != cfg.text("daemon_socket"))
        || active.get(active_key) != Some([1].as_slice())
        || ids.get(format!("{install}\0{id}").as_bytes()) != Some(active_key)
    {
        return Err(invalid());
    }
    for (bucket, author) in [
        ("teammate_ingress", "Author"),
        ("teammate_ingress_guard", "ActorID"),
    ] {
        if let Some(prior) = value(tx, bucket, key)? {
            let same_actor = if author == "Author" {
                prior[author]["id"] == actor
            } else {
                prior[author] == actor
            };
            if same_actor
                && prior["Route"]["channel_id"] == channel
                && prior["Route"]["guild_id"] == ""
                && prior["Route"]["message_id"] == message
            {
                return Ok(None);
            }
            return Err(invalid());
        }
    }
    require_repaired(tx, cfg, &binding)?;
    require_after_boundary(tx, cfg, &binding, message)?;
    let meta = tx.bucket(b"teammate_meta").ok_or_else(invalid)?;
    if meta.get(b"active_ingress_count") != Some([0; 8].as_slice()) {
        return Err(invalid());
    }
    for name in ["teammate_pending", "teammate_ingress"] {
        let bucket = tx.bucket(name).ok_or_else(invalid)?;
        if bucket.iter_buckets().next().is_some() || bucket.iter_entries().next().is_some() {
            return Err(invalid());
        }
    }
    Ok(Some(binding))
}

fn require_after_boundary<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    binding: &Value,
    message: &str,
) -> io::Result<()> {
    let minimum = original_boundary(tx, cfg, binding)?;
    if minimum >= message.parse::<u64>().map_err(io::Error::other)? {
        return Err(invalid());
    }
    Ok(())
}

fn original_boundary<'tx>(tx: &impl TxApi<'tx>, cfg: &Config, binding: &Value) -> io::Result<u64> {
    let install = cfg.text("installation_id");
    let channel = cfg.text("dm_channel_id");
    let actor = cfg.text("owner_id");
    let id = field(binding, "ID");
    let minimum = field(binding, "Boundary");
    if !snowflake(minimum) {
        return Err(invalid());
    }
    let expected = operation(install, minimum)?;
    let key = format!("{install}\0{minimum}");
    let known = value(tx, "teammate_ingress_guard", key.as_bytes())?.ok_or_else(invalid)?;
    if known["Disposition"] != "sent"
        || known["OperationID"] != expected
        || known["DiscussionID"] != id
        || known["ActorID"] != actor
        || known["Route"]["guild_id"] != ""
        || known["Route"]["channel_id"] != channel
        || known["Route"]["message_id"] != minimum
    {
        return Err(invalid());
    }
    minimum.parse::<u64>().map_err(io::Error::other)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;

    fn owner_fixture(
        listener: std::os::unix::net::UnixListener,
        endpoint: String,
        request: Value,
        operation: String,
        auth: PathBuf,
        bridge: PathBuf,
        acknowledge: bool,
    ) {
        use crate::ipc::wire;
        let binding = &request["manager_admission"]["binding"];
        let (mut stream, _) = listener.accept().unwrap();
        stream
            .set_read_timeout(Some(std::time::Duration::from_secs(5)))
            .unwrap();
        let expected = [
            (
                "hello",
                json!({"op":"hello","protocol_version":1,
                "capabilities":["semantic-send","manager-provenance","message-origin"]}),
            ),
            (
                "list",
                json!({"op":"list","sessions":[{"id":request["session"],"role":"manager",
                "manager_conversation_id":binding["conversation_id"],"agent":"codex",
                "cwd":listener.local_addr().unwrap().as_pathname().unwrap().parent().unwrap(),
                "status":{"Process":"running"},"capabilities":{"session_instance":request["session_instance"]}}]}),
            ),
            (
                "manager_runtime",
                json!({"op":"manager_runtime","manager_runtime":{"ready_for_input":true,
                "binding":binding,"bridge_socket":bridge,"auth_file":auth,
                "turn":{"state":"idle","external_input":0}}}),
            ),
            ("semantic_send", json!({"op":"ok","operation_id":operation})),
        ];
        for (op, mut response) in expected {
            let (tag, bytes) = wire::read_frame(&mut stream)
                .unwrap_or_else(|error| panic!("original owner fixture expected {op}: {error}"));
            assert_eq!(tag, wire::CONTROL);
            let actual: Value = serde_json::from_slice(&bytes).unwrap();
            assert_eq!(actual["op"], op);
            if op == "semantic_send" {
                assert_eq!(actual["composer_send"], request);
                assert_eq!(actual["operation_id"], operation);
                if !acknowledge {
                    return;
                }
            }
            response["endpoint_id"] = json!(endpoint);
            wire::write_frame(
                &mut stream,
                wire::CONTROL,
                &serde_json::to_vec(&response).unwrap(),
            )
            .unwrap();
        }
    }

    #[test]
    fn pinned_go_reopens_rust_personal_input_cutover() {
        let Some(fixture) = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE") else {
            return;
        };
        for (acknowledge, fresh, interrupted) in [
            (true, false, false),
            (true, true, false),
            (false, true, false),
            (true, true, true),
        ] {
            let root = std::env::temp_dir()
                .canonicalize()
                .unwrap()
                .join(format!("dm-{}", runtime::random_id().unwrap()));
            runtime::private_dir(&root).unwrap();
            let place = root.join("fixture");
            let run = |args: &[&str]| {
                let output = Command::new(&fixture).args(args).output().unwrap();
                assert!(
                    output.status.success(),
                    "{}",
                    String::from_utf8_lossy(&output.stderr)
                );
                String::from_utf8(output.stdout).unwrap()
            };
            let p = place.to_str().unwrap();
            run(&["create", p, root.to_str().unwrap(), "personal", "true"]);
            run(&["bind", p, "existing-dm", "400"]);
            let place = Place::for_supervisor(&root).unwrap();
            let current = json!({"author":{"id":"200","username":"offline fixture"},"channel_id":"400","id":"701",
                "content":"offline original owner prompt","type":0,"attachments":[]});
            if fresh {
                run(&["ingress", p, "existing-dm", "501", "settle"]);
                if interrupted {
                    run(&["voice-ingress", p, "existing-dm", "701", "quiet-pending"]);
                } else {
                    run(&["voice-ingress", p, "existing-dm", "701", "quiet"]);
                    assert!(
                        capture_with(&place, "701", false, |_, _| Ok(current.clone())).unwrap()
                    );
                }
                assert!(prepare::with(&place, "701", false, |_, _| Ok(current.clone())).unwrap());
                assert!(prepare::with(&place, "701", false, |_, _| Ok(current.clone())).unwrap());
            } else {
                run(&["voice-ingress", p, "existing-dm", "701", "prepared"]);
            }
            let cfg = place.load().unwrap();
            let (dir, id, endpoint, receipt) = {
                let source = place.dir.join("discord.db");
                let (_pin, db) = open_bolt(&source, 64 << 20).unwrap();
                let c = candidate(&db.begin().unwrap(), &cfg, "701")
                    .unwrap()
                    .unwrap();
                let (dir, id, endpoint) = voice_location(&place, &cfg, &c.binding).unwrap();
                let (_pin, _db, receipt) =
                    controller(&dir, &id, &c.origin, &cfg, &endpoint).unwrap();
                (dir, id, endpoint, receipt)
            };
            if fresh {
                assert_eq!(
                    run(&["voice-phase", dir.to_str().unwrap(), "701"]).trim(),
                    "prepared false"
                );
            }
            let socket = root.join("daemon.sock");
            let owner_lock = runtime::lock(&root.join("daemon.lock")).unwrap();
            let listener = runtime::bind(&socket).unwrap();
            let auth = dir.join("bridge.auth");
            runtime::write_atomic(&auth, |f| {
                std::io::Write::write_all(f, b"offline credential")
            })
            .unwrap();
            let bridge = fs::canonicalize("/tmp")
                .unwrap()
                .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
                .join(&id[..32])
                .join("bridge.sock");
            let req = receipt.admission["request"].clone();
            let operation = field(&receipt.admission, "operation_id").to_owned();
            let owner = std::thread::spawn(move || {
                owner_fixture(
                    listener,
                    endpoint,
                    req,
                    operation,
                    auth,
                    bridge,
                    acknowledge,
                )
            });
            let result = dispatch_with(&place, "701", false, |_| Ok(current));
            assert_eq!(result.is_ok(), acknowledge, "{result:?}");
            owner.join().unwrap_or_else(|_| {
                panic!("original owner fixture failed; dispatch returned {result:?}")
            });
            drop(owner_lock);
            assert!(
                dispatch_with(&place, "701", false, |_| panic!(
                    "sealed input cannot be fetched again"
                ))
                .is_err()
            );
            assert_eq!(
                run(&["voice-phase", dir.to_str().unwrap(), "701"]).trim(),
                "executing true"
            );
            assert!(settle(&place, "701", false).unwrap());
            assert_eq!(
                run(&["ingress", p, "existing-dm", "701", "replay"]).trim(),
                "0"
            );
            fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn pinned_go_continues_a_personal_dm_first_captured_by_rust() {
        let Some(fixture) = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE") else {
            return;
        };
        let root = std::env::temp_dir()
            .canonicalize()
            .unwrap()
            .join(format!("dm-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        let dir = root.join("fixture");
        let p = dir.to_str().unwrap();
        let run = |args: &[&str]| {
            let output = Command::new(&fixture).args(args).output().unwrap();
            assert!(
                output.status.success(),
                "{}",
                String::from_utf8_lossy(&output.stderr)
            );
            String::from_utf8(output.stdout).unwrap()
        };
        run(&["create", p, root.to_str().unwrap(), "personal", "true"]);
        run(&["bind", p, "existing-dm", "400"]);
        assert_eq!(
            run(&["ingress", p, "existing-dm", "501", "settle"]).trim(),
            "1"
        );
        let place = Place::for_supervisor(&root).unwrap();
        let database = dir.join("discord.db");
        let before = fs::read(&database).unwrap();
        assert!(capture_with(&place, "601", true, |_, _| panic!("dry run cannot fetch")).unwrap());
        let mut source = json!({"author":{"id":"201","username":"owner"},"channel_id":"400","id":"601",
            "content":"offline new owner DM", "type":0, "attachments":[]});
        assert!(capture_with(&place, "601", false, |_, _| Ok(source.clone())).is_err());
        assert_eq!(fs::read(&database).unwrap(), before);
        source["author"]["id"] = json!("200");
        assert!(capture_with(&place, "601", false, |_, _| Ok(source)).unwrap());
        let after = fs::read(&database).unwrap();
        assert_ne!(after, before);
        assert!(
            capture_with(&place, "601", false, |_, _| panic!(
                "duplicate cannot fetch"
            ))
            .unwrap()
        );
        assert_eq!(fs::read(&database).unwrap(), after);
        assert_eq!(
            run(&["ingress", p, "existing-dm", "601", "settle"]).trim(),
            "1"
        );
        assert_eq!(
            run(&["ingress", p, "existing-dm", "601", "replay"]).trim(),
            "0"
        );
        run(&["ingress-gap", p]);
        assert!(
            capture_with(&place, "602", true, |_, _| panic!(
                "history gap cannot fetch"
            ))
            .is_err()
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn retained_original_go_vm_inspection() {
        if std::env::var_os("SWARM_TEST_DISCORD_ACTIVE_VM").is_none() {
            return;
        }
        use super::super::{Request, execute};
        let root = std::env::var("SWARM_TEST_DISCORD_ACTIVE_ROOT").unwrap();
        let place = Place::for_supervisor(Path::new(&root)).unwrap();
        let connector = place.dir.join("discord.db");
        let voice = fs::read_dir(place.dir.join("discussions"))
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path()
            .join("voice.db");
        let before = [fs::read(&connector).unwrap(), fs::read(&voice).unwrap()];
        let run = |flag: &str| {
            let args = ["migrate-state".to_owned(), flag.to_owned()];
            execute(&Request::parse(&args).unwrap())
        };
        assert!(run("--inspect-personal-vm").unwrap());
        assert!(run("--activate-personal").is_err());
        assert!(!activation::selected(&place).unwrap());
        assert_eq!(
            before,
            [fs::read(&connector).unwrap(), fs::read(&voice).unwrap()]
        );

        let cfg = place.load().unwrap();
        let checkout = Path::new(cfg.text("work_dir"));
        let saved = checkout.with_extension("moved");
        let other = checkout.with_extension("substitute");
        runtime::private_dir(&other).unwrap();
        fs::rename(checkout, &saved).unwrap();
        std::os::unix::fs::symlink(&other, checkout).unwrap();
        assert!(run("--inspect-personal-vm").is_err());
        fs::remove_file(checkout).unwrap();
        fs::rename(saved, checkout).unwrap();
        assert_eq!(
            before,
            [fs::read(&connector).unwrap(), fs::read(&voice).unwrap()]
        );
    }

    #[test]
    fn retained_original_go_stages_native_vm_file_call() {
        let Some(root) = std::env::var_os("SWARM_TEST_DISCORD_ADMITTED_FILE_WAKE_ROOT") else {
            return;
        };
        let root = Path::new(&root);
        let place = Place::for_supervisor(root).unwrap();
        let cfg = place.load().unwrap();
        let port = std::env::var("SWARM_TEST_DISCORD_REST_PORT")
            .unwrap()
            .parse()
            .unwrap();
        let api = super::super::rest::Discord::loopback_fixture(&cfg, port).unwrap();
        let marker = root.join("rust-wake-ready");
        let deliver = std::env::var("SWARM_TEST_DISCORD_WAKE_DELIVERY").unwrap_or_default();
        if matches!(
            deliver.as_str(),
            "ordinary-sent" | "ordinary-mixed" | "ordinary-replace"
        ) {
            let mixed = deliver == "ordinary-mixed";
            let command = |args: &[&str]| {
                use super::super::{Request, execute};
                let args: Vec<_> = args.iter().map(|value| (*value).to_owned()).collect();
                execute(&Request::parse(&args).unwrap())
            };
            assert!(command(&["migrate-state", "--activate-personal-vm"]).unwrap());
            let (left, right) = std::os::unix::net::UnixStream::pair().unwrap();
            left.set_read_timeout(Some(std::time::Duration::from_millis(50)))
                .unwrap();
            let (finish, stop) = std::sync::mpsc::channel();
            let gateway = std::thread::spawn(move || {
                use super::super::gateway;
                let mut peer = tungstenite::accept(right).unwrap();
                gateway::send(&mut peer, json!({"op":10,"d":{"heartbeat_interval":45000}}))
                    .unwrap();
                let identify: Value =
                    serde_json::from_str(&peer.read().unwrap().into_text().unwrap()).unwrap();
                assert_eq!(identify["d"]["intents"], 1 << 12);
                gateway::send(
                    &mut peer,
                    json!({"op":0,"s":1,"t":"READY","d":{
                        "application":{"id":"100"},"user":{"id":"100","bot":true}
                    }}),
                )
                .unwrap();
                if mixed {
                    gateway::send(&mut peer,json!({"op":0,"s":2,"t":"MESSAGE_CREATE","d":{
                        "id":"702","type":0,"channel_id":"400","author":{"id":"200","username":"synthetic owner"},
                        "attachments":[],"content":"Continue after the retained VM file."
                    }})).unwrap();
                }
                stop.recv_timeout(std::time::Duration::from_secs(55))
                    .unwrap();
            });
            let directory = fs::read_dir(place.dir.join("discussions"))
                .unwrap()
                .next()
                .unwrap()
                .unwrap();
            let discussion = directory.file_name().into_string().unwrap();
            let socket = fs::canonicalize("/tmp")
                .unwrap()
                .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
                .join(&discussion[..32])
                .join("bridge.sock");
            let notify = std::thread::spawn(move || {
                let deadline = std::time::Instant::now() + std::time::Duration::from_secs(20);
                while std::time::Instant::now() < deadline {
                    if std::os::unix::net::UnixStream::connect(&socket).is_ok() {
                        fs::write(marker, b"ready").unwrap();
                        return;
                    }
                    std::thread::sleep(std::time::Duration::from_millis(25));
                }
                panic!("ordinary service did not own the old native wake bridge");
            });
            let mut left = Some(left);
            let run = service::with(&place, &api, Some(if mixed { 2 } else { 1 }), |cfg| {
                use super::super::gateway;
                let (mut socket, _) =
                    tungstenite::client("ws://localhost", left.take().unwrap()).unwrap();
                let mut cursor = gateway::ready(&mut socket, cfg, "synthetic-only").unwrap();
                Ok(move || cursor.event(&mut socket))
            });
            finish.send(()).unwrap();
            gateway.join().unwrap();
            notify.join().unwrap();
            assert!(run.unwrap());
            if mixed {
                let (_pin, db) = open_bolt(&place.dir.join("discord.db"), 64 << 20).unwrap();
                assert!(matches!(
                    activation::ready(&place, &cfg, &db).unwrap(),
                    activation::Ready::Idle
                ));
            }
            assert!(command(&["migrate-state", "--activate-personal-vm", "--rollback"]).unwrap());
            assert!(!activation::selected(&place).unwrap());
            return;
        }
        let run = service::with_vm_wake(&place, &api, !deliver.is_empty(), || {
            fs::write(marker, b"ready").unwrap()
        });
        if std::env::var("SWARM_TEST_DISCORD_WAKE_RECOVER")
            .is_ok_and(|fault| fault == "after-voice")
        {
            assert!(
                run.unwrap_err()
                    .to_string()
                    .contains("durable or ambiguous referent")
            );
        } else if std::env::var_os("SWARM_TEST_DISCORD_WAKE_REPLAY").is_some() {
            assert!(run.unwrap_err().to_string().contains("unconfirmed effects"));
        } else if deliver == "revoked" {
            assert!(
                run.unwrap_err()
                    .to_string()
                    .contains("outside the enrolled owner DM")
            );
        } else if deliver == "uncertain" {
            assert!(
                run.unwrap_err()
                    .to_string()
                    .contains("outcome remains unresolved")
            );
        } else {
            assert!(run.unwrap());
        }
        assert!(!activation::selected(&place).unwrap());
    }

    #[test]
    fn retained_original_go_admitted_file_wake_is_read_only() {
        let Some(root) = std::env::var_os("SWARM_TEST_DISCORD_ADMITTED_FILE_WAKE_ROOT") else {
            return;
        };
        use super::super::{Request, execute};
        let place = Place::for_supervisor(Path::new(&root)).unwrap();
        let files = [
            place.dir.join("discord.db"),
            place.supervisor_root().join("dispatcher-mail.db"),
            fs::read_dir(place.dir.join("discussions"))
                .unwrap()
                .next()
                .unwrap()
                .unwrap()
                .path()
                .join("voice.db"),
        ];
        let snapshot = || {
            files
                .iter()
                .map(fs::read)
                .collect::<io::Result<Vec<_>>>()
                .unwrap()
        };
        let before = snapshot();
        let args = [
            "migrate-state".to_owned(),
            "--inspect-personal-vm".to_owned(),
        ];
        assert!(execute(&Request::parse(&args).unwrap()).unwrap());
        assert!(!activation::selected(&place).unwrap());
        assert_eq!(snapshot(), before);

        let cfg = place.load().unwrap();
        let project = Path::new(cfg.text("work_dir"));
        let moved = project.with_extension("moved");
        let other = project.with_extension("substitute");
        runtime::private_dir(&other).unwrap();
        fs::rename(project, &moved).unwrap();
        std::os::unix::fs::symlink(&other, project).unwrap();
        assert!(execute(&Request::parse(&args).unwrap()).is_err());
        fs::remove_file(project).unwrap();
        fs::rename(moved, project).unwrap();
        assert_eq!(snapshot(), before);
    }

    #[test]
    fn native_go_queued_file_owner_inspection_is_read_only() {
        let Some(root) = std::env::var_os("SWARM_TEST_DISCORD_QUEUED_FILE_ROOT") else {
            return;
        };
        let place = Place::for_supervisor(Path::new(&root)).unwrap();
        let paths = [
            place.dir.join("discord.db"),
            place.supervisor_root().join("dispatcher-mail.db"),
            fs::read_dir(place.dir.join("discussions"))
                .unwrap()
                .next()
                .unwrap()
                .unwrap()
                .path()
                .join("voice.db"),
        ];
        let snapshot = || {
            paths
                .iter()
                .map(fs::read)
                .collect::<io::Result<Vec<_>>>()
                .unwrap()
        };
        let before = snapshot();
        use super::super::{Request, execute};
        let inspect = || {
            let args = [
                "migrate-state".to_owned(),
                "--inspect-personal-vm".to_owned(),
            ];
            execute(&Request::parse(&args).unwrap())
        };
        assert!(inspect().unwrap());
        assert!(!activation::selected(&place).unwrap());
        assert!(activation::migrate(&place, true, false, true).unwrap());
        assert!(!activation::selected(&place).unwrap());
        let original = place.load().unwrap();
        let config_path = place.dir.join("config.json");
        let mut foreign = original.0.clone();
        foreign["owner_id"] = json!("300");
        runtime::write_json(&config_path, &foreign).unwrap();
        assert!(inspect().is_err());
        runtime::write_json(&config_path, &original.0).unwrap();
        assert_eq!(snapshot(), before);
    }

    #[test]
    fn native_go_queued_file_prepares_old_scheduler_receipt() {
        let Some(root) = std::env::var_os("SWARM_TEST_DISCORD_QUEUED_FILE_ROOT") else {
            return;
        };
        let place = Place::for_supervisor(Path::new(&root)).unwrap();
        let cfg = place.load().unwrap();
        let port = std::env::var("SWARM_TEST_DISCORD_REST_PORT")
            .unwrap()
            .parse()
            .unwrap();
        let api = super::super::rest::Discord::loopback_fixture(&cfg, port).unwrap();
        if !activation::selected(&place).unwrap() {
            assert!(activation::migrate(&place, false, false, true).unwrap());
        }
        let run = service::with(&place, &api, None, |_| Ok(|| Ok(None)));
        let error = run.unwrap_err();
        if std::env::var_os("SWARM_TEST_DISCORD_INVALID_QUEUED_OPERATION").is_some() {
            assert!(error.to_string().contains("unqualified"));
        } else {
            assert!(error.to_string().contains("remains for the Go scheduler"));
        }
        assert!(activation::migrate(&place, false, true, true).unwrap());
        assert!(!activation::selected(&place).unwrap());
    }

    #[test]
    fn retained_original_go_vm_pending_file_stays_in_original_mailbox() {
        let (Some(root), Some(fixture), Ok(conversation)) = (
            std::env::var_os("SWARM_TEST_DISCORD_ACTIVE_ROOT"),
            std::env::var_os("SWARM_TEST_DISCORD_ACTIVE_FIXTURE"),
            std::env::var("SWARM_TEST_DISCORD_ACTIVE_VM_CONVERSATION"),
        ) else {
            return;
        };
        let place = Place::for_supervisor(Path::new(&root)).unwrap();
        let cfg = place.load().unwrap();
        let checkout = Path::new(cfg.text("work_dir"));
        let worker_file = checkout.join("completion.csv");
        let original_data = b"fixture,value\noriginal,1\n";
        fs::write(&worker_file, original_data).unwrap();
        let seed = Command::new(fixture)
            .arg("worker-file")
            .arg(&root)
            .arg(checkout)
            .arg(&worker_file)
            .arg(&conversation)
            .output()
            .unwrap();
        assert!(
            seed.status.success(),
            "{}",
            String::from_utf8_lossy(&seed.stderr)
        );
        let mailbox = place.supervisor_root().join("dispatcher-mail.db");
        let before = fs::read(&mailbox).unwrap();
        fs::write(&worker_file, b"changed after Go captured the completion").unwrap();
        assert!(
            activation::inspect_vm(&place)
                .unwrap_err()
                .to_string()
                .contains("pending completion")
        );
        assert!(!activation::selected(&place).unwrap());
        assert_eq!(before, fs::read(mailbox).unwrap());
    }

    #[test]
    fn retained_original_go_native_manager_roundtrip() {
        let Some(root) = std::env::var_os("SWARM_TEST_DISCORD_ACTIVE_ROOT") else {
            return;
        };
        use super::super::rest::Post;
        struct Room {
            current: Value,
            actual_rest: super::super::rest::Discord,
            posts: std::cell::Cell<u32>,
            status: PathBuf,
            acknowledge: bool,
            close_first: std::sync::mpsc::SyncSender<()>,
            disconnect: std::sync::mpsc::SyncSender<()>,
        }
        impl service::Room for Room {
            fn verify(&self, cfg: &Config) -> io::Result<()> {
                service::Room::verify(&self.actual_rest, cfg)
            }
            fn history(&self, cfg: &Config) -> io::Result<Vec<Value>> {
                service::Room::history(&self.actual_rest, cfg)
            }
            fn message(&self, cfg: &Config, id: &str) -> io::Result<Value> {
                service::Room::message(&self.actual_rest, cfg, id)
            }
            fn post(
                &self,
                cfg: &Config,
                nonce: &str,
                body: &str,
                files: &[crate::supervisor::mail_files::Captured],
            ) -> Post {
                let status = super::super::local::call(&self.status, "status", "", None).unwrap();
                assert_eq!(status["connected"], true);
                assert_eq!(status["installation_id"], "fixture-personal");
                assert_eq!(body, "Verified native final reply.");
                let count = self.posts.get() + 1;
                self.posts.set(count);
                let result = self.actual_rest.post_publication(cfg, nonce, body, files);
                if matches!(result, Post::Acknowledged(_)) {
                    if count == 1 {
                        self.close_first.send(()).unwrap();
                    } else if count == 2 {
                        self.disconnect.send(()).unwrap();
                    }
                }
                result
            }
        }
        let place = Place::for_supervisor(Path::new(&root)).unwrap();
        // Config alone cannot turn an already running restricted Go manager into VM authority.
        let config = place.dir.join("config.json");
        let original = place.load().unwrap();
        let vm = retained_vm(&original);
        if !vm {
            let mut spoofed = original.0.clone();
            spoofed["project_access"] = json!("vm");
            runtime::write_json(&config, &spoofed).unwrap();
            assert!(activation::inspect_vm(&place).is_err());
            assert!(activation::migrate(&place, true, false, true).is_err());
            runtime::write_json(&config, &original.0).unwrap();
        }
        let current = json!({"author":{"id":"200","username":"synthetic owner"},"channel_id":"400","id":"701",
            "content":"Use project former. Retained real native loopback input.", "type":0, "attachments":[]});
        let mut second = current.clone();
        second["id"] = json!("702");
        second["content"] =
            json!("Switch to secondary. Second retained real native loopback input.");
        let (close_first, first_closed) = std::sync::mpsc::sync_channel::<()>(1);
        let (disconnect, stop) = std::sync::mpsc::sync_channel::<()>(1);
        let room = Room {
            current: current.clone(),
            actual_rest: super::super::rest::Discord::loopback_fixture(
                &place.load().unwrap(),
                std::env::var("SWARM_TEST_DISCORD_REST_PORT")
                    .unwrap()
                    .parse()
                    .unwrap(),
            )
            .unwrap(),
            posts: std::cell::Cell::new(0),
            status: place.dir.clone(),
            acknowledge: std::env::var_os("SWARM_TEST_DISCORD_LOST_PUBLICATION_ACK").is_none(),
            close_first,
            disconnect,
        };
        // An interrupted foreground process leaves this private pathname behind.
        let dormant = runtime::bind(&place.dir.join("bridge.sock")).unwrap();
        drop(dormant);
        let source = place.dir.join("discord.db");
        let original = fs::read(&source).unwrap();
        let ordinary_run = || {
            use super::super::{Request, execute};
            let args = ["run".to_owned()];
            execute(&Request::parse(&args).unwrap())
                .unwrap_err()
                .to_string()
        };
        assert!(!activation::selected(&place).unwrap());
        assert!(activation::migrate(&place, true, false, vm).unwrap());
        assert!(!activation::selected(&place).unwrap());
        // The ordinary command changes routing only after the persisted handoff.
        let token = place.dir.join("fixture-token");
        let temporary = place.dir.join("fixture-token-away");
        fs::rename(&token, &temporary).unwrap();
        let prior_route = ordinary_run();
        assert!(activation::migrate(&place, false, false, vm).unwrap());
        let retained_route = ordinary_run();
        fs::rename(&temporary, &token).unwrap();
        assert!(!prior_route.contains("bot token"), "{prior_route}");
        assert!(retained_route.contains("bot token"), "{retained_route}");
        assert!(activation::selected(&place).unwrap());
        assert_eq!(fs::read(&source).unwrap(), original);
        use super::super::gateway;
        let worker = |frames: Vec<Value>, done: std::sync::mpsc::Receiver<()>| {
            let (local, remote) = std::os::unix::net::UnixStream::pair().unwrap();
            local
                .set_read_timeout(Some(std::time::Duration::from_millis(50)))
                .unwrap();
            let peer = std::thread::spawn(move || {
                let mut socket = tungstenite::accept(remote).unwrap();
                gateway::send(
                    &mut socket,
                    json!({"op":10,"d":{"heartbeat_interval":45000}}),
                )
                .unwrap();
                let frame = socket.read().unwrap().into_text().unwrap();
                let identify: Value = serde_json::from_str(&frame).unwrap();
                assert_eq!(identify["op"], 2);
                assert_eq!(identify["d"]["intents"], 1 << 12);
                gateway::send(
                    &mut socket,
                    json!({"op":0,"s":1,"t":"READY","d":{
                    "application":{"id":"100"},"user":{"id":"100","bot":true}}}),
                )
                .unwrap();
                for frame in frames {
                    gateway::send(&mut socket, frame).unwrap();
                }
                done.recv_timeout(std::time::Duration::from_secs(40))
                    .unwrap();
                let _ = socket.close(None);
            });
            (local, peer)
        };
        let (finish, done) = std::sync::mpsc::sync_channel::<()>(1);
        let frames = vec![
            json!({"op":0,"s":2,"t":"MESSAGE_CREATE","d":{"id":"699","channel_id":"400","author":{"id":"201"}}}),
            json!({"op":0,"s":3,"t":"MESSAGE_CREATE","d":current}),
        ];
        let mut sockets = std::collections::VecDeque::new();
        let mut peers = Vec::new();
        if room.acknowledge {
            let (local, first) = worker(frames, first_closed);
            sockets.push_back(local);
            peers.push(first);
            let (local, second) = worker(
                vec![json!({"op":0,"s":2,"t":"MESSAGE_CREATE","d":second})],
                done,
            );
            sockets.push_back(local);
            peers.push(second);
        } else {
            let (local, peer) = worker(frames, done);
            sockets.push_back(local);
            peers.push(peer);
        }
        let stopper = room.acknowledge.then(|| {
            std::thread::spawn(move || {
                stop.recv_timeout(std::time::Duration::from_secs(40))
                    .unwrap();
                use super::super::{Request, execute};
                let args = ["disconnect".to_owned()];
                assert!(execute(&Request::parse(&args).unwrap()).unwrap());
            })
        });
        let mut open = |cfg: &Config| {
            assert!(open_bolt(&source, 64 << 20).is_err());
            let status = super::super::local::call(&place.dir, "status", "", None).unwrap();
            assert_eq!(status["connected"], false);
            if let Some(fixture) = std::env::var_os("SWARM_TEST_DISCORD_ACTIVE_FIXTURE") {
                let other = Command::new(fixture)
                    .arg("activate")
                    .arg(&place.dir)
                    .arg("original-room")
                    .output()
                    .unwrap();
                assert!(!other.status.success());
                assert!(String::from_utf8_lossy(&other.stderr).contains("timeout"));
            }
            let (mut socket, _) = tungstenite::client(
                "ws://localhost",
                sockets
                    .pop_front()
                    .expect("only the expected Gateway peers should open"),
            )
            .unwrap();
            let mut cursor = gateway::ready(&mut socket, cfg, "synthetic-only").unwrap();
            Ok(move || cursor.event(&mut socket))
        };
        if vm && room.acknowledge {
            assert!(service::with(&place, &room, Some(1), &mut open).unwrap());
            assert_eq!(room.posts.get(), 1);
            assert!(activation::selected(&place).unwrap());
            assert!(!place.dir.join("bridge.sock").exists());
        }
        let result = service::with(&place, &room, None, open);
        if let Some(stopper) = stopper {
            stopper.join().unwrap();
            assert!(!place.load().unwrap().enabled());
        }
        finish.send(()).unwrap();
        for peer in peers {
            peer.join().unwrap();
        }
        assert!(sockets.is_empty());
        assert!(!place.dir.join("bridge.sock").exists());
        if room.acknowledge {
            assert!(result.unwrap());
        } else {
            assert!(
                result
                    .unwrap_err()
                    .to_string()
                    .contains("outcome remains unresolved")
            );
        }
        assert_eq!(room.posts.get(), if room.acknowledge { 2 } else { 1 });
        assert!(activation::migrate(&place, false, true, vm).unwrap());
        assert!(!activation::selected(&place).unwrap());
        let cfg = place.load().unwrap();
        let request = {
            let (_pin, db) = open_bolt(&source, 64 << 20).unwrap();
            let tx = db.begin().unwrap();
            let saved = value(&tx, "teammate_discussions", b"fixture-personal\x00400")
                .unwrap()
                .unwrap();
            let mut origin = json!({"kind":"owner_direct","installation_id":"fixture-personal",
                "request_id":operation("fixture-personal","701").unwrap(),"actor_id":"200","requester_id":"200",
                "actor_name":"synthetic owner","channel_id":"400","message_id":"701","discussion_id":"original-room"});
            origin["revision"] = json!(original_revision(&room.current, &origin).unwrap());
            let (directory, id, endpoint) = voice_location(&place, &cfg, &saved).unwrap();
            let (_pin, _db, receipt) =
                controller(&directory, &id, &origin, &cfg, &endpoint).unwrap();
            let phase = if room.acknowledge {
                "completed"
            } else {
                "running"
            };
            assert_eq!(receipt.admission["phase"], phase);
            assert_eq!(receipt.admission["project"], cfg.text("work_dir"));
            let current_project = if room.acknowledge && !vm {
                field(&cfg.0["projects"], "secondary")
            } else {
                cfg.text("work_dir")
            };
            assert_eq!(receipt.conversation["project"], current_project);
            assert!(
                field(&receipt.admission["request"], "text")
                    .contains("Approved project for new work")
            );
            assert_eq!(receipt.admission["source_cutover"], true);
            if room.acknowledge && !vm {
                // Reopening acknowledged history must not require a live grant
                // for the earlier project. Fresh admission uses only the remaining one.
                let mut revised = Config(cfg.0.clone());
                revised.0["projects"]
                    .as_object_mut()
                    .unwrap()
                    .remove("former");
                assert!(!grants::project(&revised, cfg.text("work_dir")));
                let (voice_pin, db) = open_bolt(&directory.join("voice.db"), 64 << 20).unwrap();
                let history =
                    prepare::read_voice(&db.begin().unwrap(), &id, &revised, &endpoint).unwrap();
                assert_eq!(history.conversation["project"], current_project);
                assert_eq!(
                    grants::choose(
                        &grants::enrolled(&revised).unwrap(),
                        cfg.text("work_dir"),
                        "Continue"
                    ),
                    (current_project.to_owned(), false)
                );
                drop(voice_pin);
            }
            receipt.admission["request"].clone()
        };
        let (mut owner, _) = super::super::migrate_supervisor::verified_owner(
            place.supervisor_root(),
            Path::new(cfg.text("daemon_socket")),
            Path::new(field(&cfg.0["supervisor"], "lock")),
            &["manager-provenance"],
        )
        .unwrap();
        let limit = std::time::Instant::now() + std::time::Duration::from_secs(15);
        loop {
            let reply = owner.request_body(
                json!({"op":"manager_input_status",
                    "session_id":request["session"],"body_version":1,
                    "manager_input_status_request":request["manager_admission"]
                }),
                "manager_input_status",
            );
            if let Ok(reply) = reply {
                let turn = &reply["manager_input_status"];
                if turn["state"] == "completed" {
                    assert_eq!(
                        turn["native_input_id"],
                        request["manager_admission"]["nonce"]
                    );
                    assert!(!field(turn, "native_turn_id").is_empty());
                    assert_ne!(turn["externally_steered"], true);
                    break;
                }
            }
            assert!(
                std::time::Instant::now() < limit,
                "real native turn did not complete"
            );
            std::thread::sleep(std::time::Duration::from_millis(50));
        }
    }

    #[test]
    fn retained_personal_revision_refuses_edited_or_forwarded_input() {
        let origin = json!({"actor_id":"200","channel_id":"400","message_id":"701"});
        let mut message = json!({"author":{"id":"200"},"channel_id":"400","id":"701",
            "content":"offline original owner prompt <&>", "type":0, "attachments":[]});
        let revision = original_revision(&message, &origin).unwrap();
        assert_eq!(revision.len(), 64);
        message["edited_timestamp"] = json!("2026-11-26T00:00:00Z");
        assert!(original_revision(&message, &origin).is_err());
        message["edited_timestamp"] = Value::Null;
        message["message_reference"] = json!({"type":1});
        assert!(original_revision(&message, &origin).is_err());
    }
}
