//! Settle a Go personal ingress candidate from the original voice controller's cutover receipt.
use super::{
    config::{Config, Place, open_owner_file, snowflake},
    legacy_discussions::operation,
    legacy_state::{bounded_writer, verify_controls_place, writable_pin},
};
use crate::{checkout::state::receipts::open_bolt, ipc::field, supervisor, supervisor::runtime};
use bbolt_rs::{BucketApi, BucketRwApi, DbApi, DbRwAPI, TxApi, TxRwApi, TxRwRefApi};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{fs, io, os::unix::fs::MetadataExt, path::Path};

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

fn controller(
    path: &Path,
    id: &str,
    origin: &Value,
    provider: &str,
    endpoint: &str,
) -> io::Result<(fs::File, impl DbApi + use<>, String)> {
    let dir = fs::symlink_metadata(path)?;
    if !dir.is_dir() || dir.uid() != unsafe { libc::geteuid() } || dir.mode() & 0o077 != 0 {
        return Err(invalid());
    }
    open_owner_file(&path.join("voice.initialized"))?;
    let voice_db = path.join("voice.db");
    let (pin, db) = open_bolt(&voice_db, 64 << 20)?;
    let tx = db.begin().map_err(io::Error::other)?;
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let header = root.get(b"state").ok_or_else(invalid)?;
    if header.len() > 4096
        || root.get(b"digest") != Some(format!("{:x}", Sha256::digest(header)).as_bytes())
    {
        return Err(invalid());
    }
    let header: Value = serde_json::from_slice(header)?;
    let conversation = format!("discord-teammate-{id}");
    if header["version"] != 3 || header["current_conversation"] != conversation {
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
        let mut hash = Sha256::new();
        hash.update(key);
        hash.update([0]);
        hash.update(raw);
        for (byte, digest) in sum.iter_mut().zip(hash.finalize()) {
            *byte ^= digest;
        }
        if key == format!("conversations\0{conversation}").as_bytes() {
            let entry: Value = serde_json::from_slice(raw)?;
            let b = entry["binding"].clone();
            if b["conversation_id"] != conversation
                || b["native_cli"] != true
                || b["provider"] != provider
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
            binding = Some(b);
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
                || entry["source_cutover"] != true
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
            admission = Some(entry);
        }
    }
    let admission = admission.ok_or_else(invalid)?;
    let binding = binding.ok_or_else(invalid)?;
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
    let phase = field(&admission, "phase").to_owned();
    drop(tx);
    Ok((pin, db, phase))
}

fn identity(file: &fs::File, path: &Path) -> io::Result<bool> {
    let a = file.metadata()?;
    let b = fs::symlink_metadata(path)?;
    Ok((a.dev(), a.ino()) == (b.dev(), b.ino()))
}

pub(super) fn settle(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    let cfg = place.load()?;
    verify_controls_place(place, &cfg)?;
    if cfg.text("mode") != "personal"
        || cfg.text("project_access") == "vm"
        || !cfg.0["calls"].is_null()
        || !cfg.0["previous_supervisor"].is_null()
        || !snowflake(message)
        || place.dir.join("bridge.sock").symlink_metadata().is_ok()
    {
        return Err(invalid());
    }
    let source = place.dir.join("discord.db");
    let source_identity = fs::symlink_metadata(&source)?;
    let stage = |c: Candidate| {
        let root = fs::canonicalize(place.supervisor_root())?;
        let endpoint = supervisor::endpoint(&root);
        let parent = fs::canonicalize(place.dir.join("discussions"))?;
        let parent_path = parent.to_str().ok_or_else(invalid)?;
        if parent != fs::canonicalize(&place.dir)?.join("discussions") {
            return Err(invalid());
        }
        let id = [
            parent_path,
            endpoint.as_str(),
            cfg.text("installation_id"),
            "",
            cfg.text("dm_channel_id"),
            field(&c.binding, "ID"),
        ]
        .join("\0");
        let id = format!("{:x}", Sha256::digest(id.as_bytes()));
        let directory = parent.join(&id);
        if fs::canonicalize(&directory)? != directory {
            return Err(invalid());
        }
        let (pin, voice, phase) = controller(
            &directory,
            &id,
            &c.origin,
            cfg.text("manager_cli"),
            &endpoint,
        )?;
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
    let _config = place.lock()?;
    let _bridge = runtime::lock(&place.dir.join("bridge.lock"))?;
    if place.load()?.0 != cfg.0 || place.dir.join("bridge.sock").symlink_metadata().is_ok() {
        return Err(invalid());
    }
    verify_controls_place(place, &cfg)?;
    let original = writable_pin(&source)?;
    let opened = original.metadata()?;
    if (source_identity.dev(), source_identity.ino()) != (opened.dev(), opened.ino()) {
        return Err(invalid());
    }
    let (pin, mut db) = bounded_writer(original)?;
    let mut tx = db.begin_rw().map_err(io::Error::other)?;
    let Some(row) = candidate(&tx, &cfg, message)? else {
        println!("Original personal ingress is already settled.");
        return Ok(true);
    };
    let (mut c, voice, _voice_db, voice_path, phase) = stage(row)?;
    if !identity(&pin, &source)? || !identity(&voice, &voice_path)? {
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
