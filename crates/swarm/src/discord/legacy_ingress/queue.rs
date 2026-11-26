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

pub(super) fn semantic_issued_at(operation: &str) -> Option<u64> {
    let (millis, nonce) = operation.strip_prefix("semantic:")?.split_once(':')?;
    if millis.len() > 13
        || nonce.len() != 32
        || !nonce
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return None;
    }
    let issued: u64 = millis.parse().ok()?;
    (issued > 0 && issued.to_string() == millis).then_some(issued)
}

pub(super) struct Prepared {
    pub(super) parent_message: String,
    path: PathBuf,
    voice_identity: (u64, u64),
    mailbox_identity: (u64, u64),
    note: Value,
    conversation: String,
    event: String,
    request: Value,
    operation: String,
    parent: Value,
}

pub(super) fn prepare(
    place: &Place,
    cfg: &Config,
    lease: &ConnectorLease,
    room: &impl service::Room,
) -> io::Result<Prepared> {
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
    let mut operation = format!("semantic:{}:{nonce}", crate::ipc::spawn::now_ms());
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
            || semantic_issued_at(field(&saved, "operation_id")).is_none()
        {
            return Err(invalid());
        }
        operation = field(&saved, "operation_id").to_owned();
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
    Ok(Prepared {
        parent_message: field(&proof.input["origin"], "message_id").to_owned(),
        path: proof.path,
        voice_identity: proof.voice_identity,
        mailbox_identity: proof.mailbox_identity,
        note: proof.note,
        conversation: proof.conversation,
        event: proof.event,
        request,
        operation,
        parent: proof.input,
    })
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

pub(super) fn dispatch(
    place: &Place,
    cfg: &Config,
    lease: &ConnectorLease,
    prepared: Prepared,
    room: &impl service::Room,
) -> io::Result<()> {
    let issued = semantic_issued_at(&prepared.operation).ok_or_else(invalid)?;
    let fresh_operation = || {
        let now = crate::ipc::spawn::now_ms();
        issued <= now.saturating_add(30_000) && now.saturating_sub(issued) <= 3_600_000
    };
    if !fresh_operation() {
        return Err(io::Error::other(
            "the original prepared semantic operation is outside its replay window",
        ));
    }
    let origin = &prepared.parent["origin"];
    room.verify(cfg)?;
    let current = room.message(cfg, &prepared.parent_message)?;
    if !current["message_snapshots"].is_null()
        || original_revision(&current, origin)? != origin["revision"]
    {
        return Err(invalid());
    }
    current_turn_enrollment(place, cfg)?;
    supervisor::mail_store::verify_original_ingested_file(
        place.supervisor_root(),
        &prepared.note,
        prepared.mailbox_identity,
    )?;
    let (pin, mut db) = bounded_writer(writable_pin(&prepared.path)?)?;
    let identity_of = pin.metadata()?;
    if (identity_of.dev(), identity_of.ino()) != prepared.voice_identity
        || !identity(&pin, &prepared.path)?
        || !lease.source_unchanged()?
    {
        return Err(invalid());
    }
    let conn = lease.db.begin().map_err(io::Error::other)?;
    publication::verify_guard(&conn, cfg, origin)?;
    let source_guard = format!(
        "{}\0{}",
        cfg.text("installation_id"),
        prepared.parent_message
    );
    if value(&conn, "teammate_ingress_guard", source_guard.as_bytes())?
        .is_none_or(|guard| guard["LastChange"] != "")
    {
        return Err(invalid());
    }
    prepare::no_pending_output(&conn, cfg)?;
    let tx = db.begin_rw().map_err(io::Error::other)?;
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let bytes = root
        .get(b"state")
        .filter(|bytes| bytes.len() <= 4096)
        .ok_or_else(invalid)?;
    let header: Value = serde_json::from_slice(bytes)?;
    if root.get(b"digest") != Some(format!("{:x}", Sha256::digest(bytes)).as_bytes())
        || header["version"] != 4
        || header["current_conversation"] != prepared.conversation
    {
        return Err(invalid());
    }
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    if records.iter_buckets().next().is_some() {
        return Err(invalid());
    }
    let conversation = &prepared.conversation;
    let conv_key = format!("conversations\0{conversation}").into_bytes();
    let mut state: Value = serde_json::from_slice(records.get(&conv_key).ok_or_else(invalid)?)?;
    let binding = &state["binding"];
    let mut wake = wake::record(&records, "wakes", &[conversation, &prepared.event])?;
    let parent_n = prepared.parent["sequence"]
        .as_u64()
        .filter(|value| *value > 0)
        .ok_or_else(invalid)?;
    let parent = wake::record(
        &records,
        "admissions",
        &[conversation, &parent_n.to_string()],
    )?;
    if parent != prepared.parent
        || parent["phase"] != "completed"
        || field(&parent, "settled_at").is_empty()
        || parent["settled_at"] == "0001-01-01T00:00:00Z"
        || wake["state"] != "prepared"
        || wake["request"] != prepared.request
        || wake["request_hash"] != digest(&prepared.request)?
        || wake["operation_id"] != prepared.operation
        || !field(&wake, "native_input_id").is_empty()
        || wake["admission"].as_u64().unwrap_or_default() != 0
        || wake["id"] != prepared.event
        || wake["conversation_id"] != *conversation
        || binding["native_cli"] != true
        || binding["provider"] != cfg.text("manager_cli")
        || binding["conversation_id"] != *conversation
        || !matches!(state["custody_lost"], Value::Null | Value::Bool(false))
        || !matches!(state["scheduler_paused"], Value::Null | Value::Bool(false))
        || !matches!(state["parked"], Value::Null | Value::Bool(false))
        || !state["retirement"].is_null()
        || state["external_observed"] != true
        || !(state["external_input"].is_null() || state["external_input"] == 0)
    {
        return Err(invalid());
    }
    let last = state["last_admission"].as_u64().ok_or_else(invalid)?;
    if state["retired_through"].as_u64() != Some(last) {
        return Err(io::Error::other(format!(
            "an earlier original Go admission remains unsettled (retired through {}, last admission {last})",
            state["retired_through"]
        )));
    }
    let mut count = 0;
    let mut checksum = [0u8; 32];
    for (key, raw) in records.iter_entries() {
        count += 1;
        if count > 4096 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        for (left, right) in checksum.iter_mut().zip(record_hash(key, raw)) {
            *left ^= right;
        }
        if key.starts_with(b"wakes\0")
            && key != record_key("wakes", conversation, &prepared.event)?
            && !serde_json::from_slice::<Value>(raw).is_ok_and(|row| row["state"] == "settled")
        {
            return Err(io::Error::other(
                "another original Go wake remains for its scheduler",
            ));
        }
        if key.starts_with(b"requests\0")
            || key.starts_with(b"actions\0")
            || key.starts_with(b"admissions\0")
                && !serde_json::from_slice::<Value>(raw).is_ok_and(|row| {
                    row["settled_at"]
                        .as_str()
                        .is_some_and(|time| !time.is_empty() && !time.starts_with("0001-01-01"))
                        && matches!(
                            row["reply_delivery_unknown"],
                            Value::Null | Value::Bool(false)
                        )
                        && (row["sequence"] == parent_n
                            || row["reply_check_after"].is_null()
                            || row["reply_check_after"] == 0)
                })
        {
            return Err(io::Error::other(
                "an earlier original Go effect still needs its scheduler",
            ));
        }
    }
    if header["records"] != count || header["record_hash"] != json!(checksum) {
        return Err(invalid());
    }
    let caps = ["semantic-send", "manager-provenance", "message-origin"];
    let (mut owner, rows) = super::super::migrate_supervisor::verified_owner(
        place.supervisor_root(),
        Path::new(cfg.text("daemon_socket")),
        Path::new(field(&cfg.0["supervisor"], "lock")),
        &caps,
    )?;
    cfg.check_supervisor(&owner, &caps)?;
    let session = field(binding, "session_id");
    let instance = field(binding, "session_instance");
    if rows
        .iter()
        .filter(|row| {
            row["id"] == session
                && row["role"] == "manager"
                && row["manager_conversation_id"] == *conversation
                && row["cwd"] == cfg.text("work_dir")
                && row["capabilities"]["session_instance"] == instance
        })
        .count()
        != 1
    {
        return Err(invalid());
    }
    let mut native = binding.clone();
    let fields = native.as_object_mut().ok_or_else(invalid)?;
    let controller_session = fields.remove("session_id").ok_or_else(invalid)?;
    fields.insert("session".into(), controller_session);
    fields.remove("generation");
    fields.remove("native_cli");
    let runtime = owner.request_body(
        json!({"op":"manager_runtime","session_id":session,"body_version":1,
        "manager_runtime_request":{"session_instance":instance}}),
        "manager_runtime",
    )?;
    let current = &runtime["manager_runtime"];
    if current["binding"] != native
        || current["native_cli"] != true
        || current["ready_for_input"] != true
        || !matches!(
            current["provider_exhausted"],
            Value::Null | Value::Bool(false)
        )
        || current["turn"]["external_input"] != 0
        || !matches!(
            current["turn"]["externally_steered"],
            Value::Null | Value::Bool(false)
        )
        || !matches!(field(&current["turn"], "state"), "idle" | "completed")
    {
        return Err(io::Error::other("original Go native manager is still busy"));
    }
    let old_dir = prepared.path.parent().ok_or_else(invalid)?;
    let auth = old_dir.join("bridge.auth");
    let ledger = old_dir
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or_else(invalid)?;
    let bridge = fs::canonicalize("/tmp")?
        .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
        .join(&ledger[..32])
        .join("bridge.sock");
    if current["auth_file"] != json!(auth) || current["bridge_socket"] != json!(bridge) {
        return Err(invalid());
    }
    open_owner_file(&auth)?;
    current_turn_enrollment(place, cfg)?;
    let seq = last.checked_add(1).ok_or_else(invalid)?;
    let mut nonce_bytes = [0u8; 32];
    fs::File::open("/dev/urandom")?.read_exact(&mut nonce_bytes)?;
    let nonce: String = nonce_bytes
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect();
    let text = field(&prepared.request, "text");
    if text.is_empty() || text.len() > 4096 {
        return Err(invalid());
    }
    let req = json!({"session":session,"session_instance":instance,"text":text,
        "manager_admission":{"binding":native,"nonce":nonce,"expected_external_input":0}});
    let admission_key = record_key("admissions", conversation, &seq.to_string())?;
    let wake_key = record_key("wakes", conversation, &prepared.event)?;
    if records.get(admission_key.as_slice()).is_some() {
        return Err(invalid());
    }
    let mut admission = json!({
        "operation_id":prepared.operation,"request":req,"phase":"executing",
        "input_text":text,"conversation_id":conversation,"sequence":seq,
        "source_id":format!("wake:{}",prepared.event),"native_input_id":nonce,"settled_at":"0001-01-01T00:00:00Z"
    });
    state["last_admission"] = json!(seq);
    wake["admission"] = json!(seq);
    wake["state"] = json!("admitted");
    wake["native_input_id"] = json!(nonce);
    manager_publication::apply_voice(
        tx,
        header,
        &[
            (conv_key, state),
            (admission_key.clone(), admission.clone()),
            (wake_key.clone(), wake.clone()),
        ],
    )?;
    current_turn_enrollment(place, cfg)?;
    if !fresh_operation() {
        return Err(io::Error::other(
            "the original prepared semantic operation is outside its replay window",
        ));
    }
    supervisor::mail_store::verify_original_ingested_file(
        place.supervisor_root(),
        &prepared.note,
        prepared.mailbox_identity,
    )?;
    current_turn_enrollment(place, cfg)?;
    let reply = owner.exchange(
        json!({"op":"semantic_send","session_id":req["session"],"body_version":1,
        "operation_id":prepared.operation,"composer_send":req}),
        "ok",
        std::time::Duration::from_secs(35),
    );
    let reply = reply.map_err(|error| io::Error::other(format!(
        "original Go worker-wake native input remains uncertain under its saved operation: {error}"
    )))?;
    if reply["op"] != "ok" || reply["operation_id"] != prepared.operation {
        return Err(io::Error::other(
            "original Go worker-wake operation was not acknowledged",
        ));
    }
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
    let observed = loop {
        let response = owner.request_body(
            json!({"op":"manager_input_status","session_id":req["session"],
            "body_version":1,"manager_input_status_request":req["manager_admission"]}),
            "manager_input_status",
        )?;
        let observed = &response["manager_input_status"];
        if response["session_id"] == req["session"]
            && response["endpoint_id"] == owner.endpoint()
            && observed["native_input_id"] == nonce
            && observed["external_input"] == 0
            && matches!(
                observed["externally_steered"],
                Value::Null | Value::Bool(false)
            )
            && observed["state"] == "running"
            && !field(observed, "native_turn_id").is_empty()
        {
            break observed.clone();
        }
        if std::time::Instant::now() >= deadline {
            return Err(io::Error::other(
                "the original Go worker-wake input has no exact live native observation",
            ));
        }
        std::thread::sleep(std::time::Duration::from_millis(25));
    };
    let tx = db.begin_rw().map_err(io::Error::other)?;
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let header: Value = serde_json::from_slice(root.get(b"state").ok_or_else(invalid)?)?;
    admission["phase"] = json!(field(&observed, "state"));
    if let Some(turn) = observed["native_turn_id"]
        .as_str()
        .filter(|turn| !turn.is_empty())
    {
        admission["native_turn_id"] = json!(turn);
    }
    wake["state"] = json!("native_input");
    manager_publication::apply_voice(tx, header, &[(admission_key, admission), (wake_key, wake)])
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn original_semantic_operation_checks_syntax_independently_of_age() {
        let nonce = "1234567890abcdef1234567890abcdef";
        assert_eq!(semantic_issued_at(&format!("semantic:1:{nonce}")), Some(1));
        assert_eq!(
            semantic_issued_at(&format!("semantic:1795690628240:{nonce}")),
            Some(1795690628240)
        );
        for malformed in [
            format!("launch:1:{nonce}"),
            format!("semantic:0:{nonce}"),
            format!("semantic:01:{nonce}"),
            format!("semantic:+1:{nonce}"),
            format!("semantic:10000000000000:{nonce}"),
            format!("semantic:1:{}", nonce.to_uppercase()),
            format!("semantic:1:{nonce}:1"),
            "semantic:1:a1".into(),
        ] {
            assert_eq!(semantic_issued_at(&malformed), None, "{malformed}");
        }
    }
}
