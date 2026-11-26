//! Inspect a single genuine original Go queued file without admitting it.
use super::*;
use std::io::Read;

pub(super) struct QueuedFile {
    input: Value,
    note: Value,
    conversation: String,
    event: String,
    path: PathBuf,
    voice_identity: (u64, u64),
    mailbox_identity: (u64, u64),
    header: Value,
}

pub(super) fn inspect(
    place: &Place,
    cfg: &Config,
    connector: &impl DbApi,
) -> io::Result<QueuedFile> {
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
    let (note, mailbox_identity) =
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
    let existing_event = records.get(format!("events\0{event_index}").as_bytes());
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
    if let Some(raw) = existing_event {
        let previous: Value = serde_json::from_slice(raw)?;
        let wake = wake::record(&records, "wakes", &[&conversation, &event])?;
        if previous != event_row(&conversation, &event, &note)?
            || wake["state"] != "prepared"
            || wake["id"] != event
            || wake["event_id"] != event
            || wake["conversation_id"] != conversation
            || wake["admission"].as_u64().unwrap_or_default() != 0
        {
            return Err(io::Error::other(
                "the original queued worker already has conflicting Voice state",
            ));
        }
    }
    let pin = voice_pin.metadata()?;
    Ok(QueuedFile {
        input: parent,
        note,
        conversation,
        event,
        path: voice_path,
        voice_identity: (pin.dev(), pin.ino()),
        mailbox_identity,
        header: parsed,
    })
}

fn digest(value: &Value) -> io::Result<String> {
    let wire = crate::ipc::escape_json(serde_json::to_string(value)?);
    Ok(format!("{:x}", Sha256::digest(wire.as_bytes())))
}

fn event_row(conversation: &str, event: &str, note: &Value) -> io::Result<Value> {
    let job = format!(
        "vm-child-{}",
        digest(&json!([conversation, note["Session"]]))?
    );
    let cursor = json!({"binding":"","sequence":0});
    Ok(json!({
        "worker_message":true,"job_id":job,"attempt_id":note["Attempt"],"id":event,
        "conversation_id":conversation,"cursor":cursor,"examined_through":cursor,
        "metadata":{"files":note["Files"],"Session":note["Session"],"Instance":note["Instance"],
            "Agent":note["Agent"],"Text":note["Text"],"Kind":note["Kind"],"NoteID":note["ID"],
            "Sequence":note["Sequence"],"Created":note["Created"]},
        "wake_id":event
    }))
}

fn record_key(table: &str, conversation: &str, id: &str) -> io::Result<Vec<u8>> {
    Ok(format!("{table}\0{}", serde_json::to_string(&[conversation, id])?).into_bytes())
}

pub(super) fn prepare(
    place: &Place,
    cfg: &Config,
    lease: &ConnectorLease,
    room: &impl service::Room,
) -> io::Result<()> {
    current_turn_enrollment(place, cfg)?;
    let first = inspect(place, cfg, &lease.db)?;
    let origin = &first.input["origin"];
    room.verify(cfg)?;
    let current = room.message(cfg, field(origin, "message_id"))?;
    if !current["message_snapshots"].is_null()
        || original_revision(&current, origin)? != origin["revision"]
    {
        return Err(invalid());
    }
    let proof = inspect(place, cfg, &lease.db)?;
    if proof.note != first.note
        || proof.input != first.input
        || proof.conversation != first.conversation
        || proof.voice_identity != first.voice_identity
        || proof.mailbox_identity != first.mailbox_identity
        || proof.header != first.header
    {
        return Err(invalid());
    }
    let (pin, mut db) = bounded_writer(writable_pin(&proof.path)?)?;
    let current = pin.metadata()?;
    if (current.dev(), current.ino()) != proof.voice_identity
        || !identity(&pin, &proof.path)?
        || !lease.source_unchanged()?
    {
        return Err(invalid());
    }
    current_turn_enrollment(place, cfg)?;
    let conversation = &proof.conversation;
    let event_id = &proof.event;
    let note = &proof.note;
    let event = event_row(conversation, event_id, note)?;
    let event_key = record_key("events", conversation, event_id)?;
    let wake_key = record_key("wakes", conversation, event_id)?;
    let worker_index = serde_json::to_string(&[&note["Session"], &note["Instance"]])?;
    let worker_key = record_key("workers", conversation, &worker_index)?;
    let worker = json!({
        "parent_input_hash":note["ParentInputHash"],"mailbox":true,"vm_child":true,
        "parent":note["Parent"],"project":note["Project"],"attempt_id":note["Attempt"],
        "job_id":event["job_id"],"conversation_id":conversation,"session":note["Session"],
        "session_instance":note["Instance"],"admission":proof.input["sequence"],
        "source_id":"","action_id":""
    });
    let request = wake_request(&event)?;
    let request_hash = digest(&request)?;
    let mut random = [0u8; 16];
    fs::File::open("/dev/urandom")?.read_exact(&mut random)?;
    let nonce: String = random.iter().map(|byte| format!("{byte:02x}")).collect();
    let operation = format!("semantic:{}:{nonce}", crate::ipc::spawn::now_ms());
    let wake = json!({"id":event_id,"conversation_id":conversation,"event_id":event_id,
        "operation_id":operation,"request":request,"request_hash":request_hash,"state":"prepared"});
    let tx = db.begin_rw().map_err(io::Error::other)?;
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let current_header: Value = serde_json::from_slice(root.get(b"state").ok_or_else(invalid)?)?;
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    if current_header != proof.header {
        return Err(invalid());
    }
    let have_worker = if let Some(raw) = records.get(&worker_key) {
        let previous: Value = serde_json::from_slice(raw)?;
        if worker
            .as_object()
            .ok_or_else(invalid)?
            .iter()
            .any(|(field, value)| previous[field] != *value)
            || !matches!(
                previous["recovery_required"],
                Value::Null | Value::Bool(false)
            )
            || !matches!(previous["reserved"], Value::Null | Value::Bool(false))
        {
            return Err(invalid());
        }
        true
    } else {
        false
    };
    if let Some(raw) = records.get(&event_key) {
        if !have_worker {
            return Err(invalid());
        }
        let prior: Value = serde_json::from_slice(raw)?;
        let saved: Value = serde_json::from_slice(records.get(&wake_key).ok_or_else(invalid)?)?;
        if prior != event
            || saved["state"] != "prepared"
            || saved["request"] != request
            || saved["request_hash"] != request_hash
            || saved["conversation_id"] != *conversation
            || saved["id"] != *event_id
            || saved["event_id"] != *event_id
            || field(&saved, "operation_id").is_empty()
        {
            return Err(invalid());
        }
        drop(tx);
    } else {
        if records.get(&wake_key).is_some() {
            return Err(invalid());
        }
        let mut changes = vec![(event_key, event), (wake_key, wake)];
        if !have_worker {
            changes.push((worker_key, worker));
        }
        let mut header = proof.header;
        header["version"] = json!(4);
        manager_publication::apply_voice(tx, header, &changes)?;
    }
    #[cfg(test)]
    if std::env::var_os("SWARM_TEST_DISCORD_QUEUE_EXIT_AFTER_VOICE").is_some() {
        std::process::exit(87);
    }
    current_turn_enrollment(place, cfg)?;
    supervisor::mail_store::acknowledge_original_file(
        place.supervisor_root(),
        note,
        proof.mailbox_identity,
    )?;
    Ok(())
}

fn wake_request(event: &Value) -> io::Result<Value> {
    let wire = crate::ipc::escape_json(serde_json::to_string(&event["metadata"])?);
    let metadata = wire.replace("[swarm-manager-input", "\\u005bswarm-manager-input");
    let text = format!(
        "Message from your delegated worker. The queued receipt delivered the worker’s note to you. Verify its result against the attached launch-turn evidence or inspect the same worker with swarm peek. If only the note is available, attribute the result to the worker. Use publish with final=true during this turn to report the result in Discord, unless this completion already has a final publication. To send the worker's files to Discord, read this exact completion with swarm_read_note and pass the selected IDs as file_ids to publish. Continue only the original authorized task. Worker message and identity: {metadata}"
    );
    if text.len() > 4096 - "[SWARM_MANAGER_INPUT_V1:".len() - 64 - "]\n".len() {
        return Err(invalid());
    }
    Ok(json!({"text":text}))
}
