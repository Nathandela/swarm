//! Inspect a single genuine original Go queued file without admitting it.
use super::*;

pub(super) fn inspect(place: &Place, cfg: &Config, connector: &impl DbApi) -> io::Result<()> {
    let tx = connector.begin().map_err(io::Error::other)?;
    let scope = format!(
        "{}\0{}",
        cfg.text("installation_id"),
        cfg.text("dm_channel_id")
    );
    let route = value(&tx, "teammate_discussions", scope.as_bytes())?.ok_or_else(invalid)?;
    if route["InstallationID"] != cfg.text("installation_id")
        || route["Opened"] != true
        || route["ProvisionalCancelled"] != false
        || route["Route"]["channel_id"] != cfg.text("dm_channel_id")
        || route["Route"]["guild_id"] != ""
        || !route["Route"]["parent_id"].is_null()
        || (!route["supervisor_socket"].is_null()
            && route["supervisor_socket"] != cfg.text("daemon_socket"))
    {
        return Err(invalid());
    }
    let discussion = field(&route, "ID");
    let active = tx
        .bucket(b"teammate_active_discussions")
        .ok_or_else(invalid)?;
    let ids = tx.bucket(b"teammate_discussion_ids").ok_or_else(invalid)?;
    if discussion.is_empty()
        || active.get(scope.as_bytes()) != Some([1].as_slice())
        || ids.get(format!("{}\0{discussion}", cfg.text("installation_id")).as_bytes())
            != Some(scope.as_bytes())
    {
        return Err(invalid());
    }
    require_repaired(&tx, cfg, &route)?;
    original_boundary(&tx, cfg, &route)?;
    let (directory, id, endpoint) = voice_location(place, cfg, &route)?;
    let conversation = format!("discord-teammate-{id}");
    let note =
        supervisor::mail_store::original_queued_file(place.supervisor_root(), &conversation)?
            .ok_or_else(invalid)?;
    let project = field(&note, "Project");
    if !grants::project(cfg, project)
        || !field(&note, "Session").starts_with(&format!("{endpoint}/"))
        || !field(&note, "Attempt").starts_with("vm-child-")
        || field(&note, "ParentInputHash").len() != 64
    {
        return Err(invalid());
    }
    open_owner_file(&directory.join("voice.initialized"))?;
    let voice_path = directory.join("voice.db");
    let (voice_pin, db) = open_bolt(&voice_path, 64 << 20)?;
    let voice = db.begin().map_err(io::Error::other)?;
    let root = voice.bucket(b"controller").ok_or_else(invalid)?;
    let header = root
        .get(b"state")
        .filter(|raw| raw.len() <= 4096)
        .ok_or_else(invalid)?;
    let parsed: Value = serde_json::from_slice(header)?;
    if !matches!(parsed["version"].as_u64(), Some(3 | 4))
        || parsed["current_conversation"] != conversation
        || root.get(b"digest") != Some(format!("{:x}", Sha256::digest(header)).as_bytes())
    {
        return Err(invalid());
    }
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    if records.iter_buckets().next().is_some() {
        return Err(invalid());
    }
    let event = format!(
        "worker-message-{:x}",
        Sha256::digest(
            crate::ipc::escape_json(serde_json::to_string(&json!([
                note["Session"],
                note["Instance"],
                note["Attempt"],
                note["ID"]
            ]))?)
            .as_bytes()
        )
    );
    let event_index = serde_json::to_string(&[conversation.as_str(), event.as_str()])?;
    if records
        .get(format!("events\0{event_index}").as_bytes())
        .is_some()
    {
        return Err(io::Error::other(
            "the original queued worker already has a Voice event",
        ));
    }
    let state: Value = serde_json::from_slice(
        records
            .get(format!("conversations\0{conversation}").as_bytes())
            .ok_or_else(invalid)?,
    )?;
    let binding = &state["binding"];
    let manager_session = field(binding, "session_id");
    if binding["native_cli"] != true
        || binding["provider"] != cfg.text("manager_cli")
        || binding["conversation_id"] != conversation
        || !manager_session.starts_with(&format!("{endpoint}/"))
        || manager_session.rsplit('/').next() != note["Parent"].as_str()
        || !matches!(state["custody_lost"], Value::Null | Value::Bool(false))
        || !matches!(state["scheduler_paused"], Value::Null | Value::Bool(false))
        || !matches!(state["parked"], Value::Null | Value::Bool(false))
        || !state["retirement"].is_null()
    {
        return Err(invalid());
    }
    let mut checksum = [0; 32];
    let mut count = 0;
    let mut parent = None;
    for (key, raw) in records.iter_entries() {
        count += 1;
        if count > 4096 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        for (left, right) in checksum.iter_mut().zip(record_hash(key, raw)) {
            *left ^= right;
        }
        if key.starts_with(b"admissions\0") {
            let row: Value = serde_json::from_slice(raw)?;
            let nonce = field(&row, "native_input_id");
            if nonce.len() == 64
                && supervisor::mail::parent_hash(nonce) == note["ParentInputHash"]
                && parent.replace(row).is_some()
            {
                return Err(invalid());
            }
        }
    }
    if parsed["records"] != count || parsed["record_hash"] != json!(checksum) {
        return Err(invalid());
    }
    let parent = parent.ok_or_else(invalid)?;
    let origin = &parent["origin"];
    if parent["conversation_id"] != conversation
        || parent["source_cutover"] != true
        || parent["project"] != project
        || !matches!(parent["recovery_only"], Value::Null | Value::Bool(false))
        || origin["kind"] != "owner_direct"
        || origin["installation_id"] != cfg.text("installation_id")
        || origin["discussion_id"] != discussion
        || origin["channel_id"] != cfg.text("dm_channel_id")
        || origin["actor_id"] != cfg.text("owner_id")
        || origin["requester_id"] != origin["actor_id"]
        || !origin["guild_id"].is_null()
        || !identity(&voice_pin, &voice_path)?
    {
        return Err(invalid());
    }
    publication::verify_guard(&tx, cfg, origin)?;
    let guard_key = format!(
        "{}\0{}",
        cfg.text("installation_id"),
        field(origin, "message_id")
    );
    if value(&tx, "teammate_ingress_guard", guard_key.as_bytes())?
        .is_none_or(|row| row["LastChange"] != "")
    {
        return Err(invalid());
    }
    let caps = ["manager-provenance"];
    let (mut owner, rows) = super::super::migrate_supervisor::verified_owner(
        place.supervisor_root(),
        Path::new(cfg.text("daemon_socket")),
        Path::new(field(&cfg.0["supervisor"], "lock")),
        &caps,
    )?;
    cfg.check_supervisor(&owner, &caps)?;
    if rows
        .iter()
        .filter(|row| {
            row["id"] == note["Session"]
                && row["worker_discussion"] == conversation
                && row["worker_instance"] == note["Instance"]
                && row["worker_attempt"] == note["Attempt"]
                && row["worker_parent_input_hash"] == note["ParentInputHash"]
                && row["spawned_from"] == note["Parent"]
                && row["agent"] == note["Agent"]
                && row["cwd"] == project
        })
        .count()
        != 1
        || rows
            .iter()
            .filter(|row| {
                row["id"] == manager_session
                    && row["role"] == "manager"
                    && row["manager_conversation_id"] == conversation
                    && row["cwd"] == cfg.text("work_dir")
                    && row["capabilities"]["session_instance"] == binding["session_instance"]
            })
            .count()
            != 1
    {
        return Err(invalid());
    }
    let runtime = owner.request_body(
        json!({
            "op":"manager_runtime","session_id":manager_session,"body_version":1,
            "manager_runtime_request":{"session_instance":binding["session_instance"]}
        }),
        "manager_runtime",
    )?;
    let current = &runtime["manager_runtime"];
    let mut expected = binding.clone();
    let fields = expected.as_object_mut().ok_or_else(invalid)?;
    let session = fields.remove("session_id").ok_or_else(invalid)?;
    fields.insert("session".to_owned(), session);
    fields.remove("native_cli");
    fields.remove("generation");
    let auth = directory.join("bridge.auth");
    open_owner_file(&auth)?;
    let bridge = fs::canonicalize("/tmp")?
        .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
        .join(&id[..32])
        .join("bridge.sock");
    if current["binding"] != expected
        || current["native_cli"] != true
        || current["auth_file"] != json!(auth)
        || current["bridge_socket"] != json!(bridge)
        || !matches!(
            current["turn"]["externally_steered"],
            Value::Null | Value::Bool(false)
        )
        || current["turn"]["external_input"] != 0
    {
        return Err(invalid());
    }
    Ok(())
}
