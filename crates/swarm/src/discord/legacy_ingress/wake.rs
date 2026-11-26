//! Read the one already-admitted media wake from its original Go ledger.
use super::*;

fn record<'a>(
    records: &impl BucketApi<'a>,
    table: &str,
    coordinates: &[&str],
) -> io::Result<Value> {
    let key = serde_json::to_string(coordinates)?;
    let raw = records
        .get(format!("{table}\0{key}").as_bytes())
        .ok_or_else(|| io::Error::other(format!("missing original VM {table} record")))?;
    Ok(serde_json::from_slice(raw)?)
}

fn clear(flag: &Value) -> bool {
    matches!(flag, Value::Null | Value::Bool(false))
}

fn hash(value: &Value) -> io::Result<String> {
    let bytes = crate::ipc::escape_json(serde_json::to_string(value)?);
    Ok(format!("{:x}", Sha256::digest(bytes.as_bytes())))
}

pub(super) struct Admitted {
    pub(super) receipt: Receipt,
    pub(super) origin: Value,
    pub(super) source: Value,
    pub(super) output_source: Value,
    pub(super) checkout_identity: (u64, u64),
}

pub(super) fn inspect<'connector, 'voice>(
    place: &Place,
    cfg: &Config,
    connector: &impl TxApi<'connector>,
    voice: &impl TxApi<'voice>,
    route: &Value,
    id: &str,
    endpoint: &str,
) -> io::Result<Admitted> {
    let root = voice.bucket(b"controller").ok_or_else(invalid)?;
    let raw = root
        .get(b"state")
        .filter(|value| value.len() <= 4096)
        .ok_or_else(invalid)?;
    let header: Value = serde_json::from_slice(raw)?;
    let conversation = format!("discord-teammate-{id}");
    if header["version"] != 4
        || header["current_conversation"] != conversation
        || root.get(b"digest") != Some(format!("{:x}", Sha256::digest(raw)).as_bytes())
    {
        return Err(io::Error::other(
            "unqualified original media-v4 voice ledger header",
        ));
    }
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    if records.iter_buckets().next().is_some() {
        return Err(io::Error::other(
            "unexpected bucket in original VM voice records",
        ));
    }
    let row: Value = serde_json::from_slice(
        records
            .get(format!("conversations\0{conversation}").as_bytes())
            .ok_or_else(invalid)?,
    )?;
    let manager = &row["binding"];
    if manager["native_cli"] != true
        || manager["conversation_id"] != conversation
        || manager["provider"] != cfg.text("manager_cli")
        || !field(manager, "session_id").starts_with(&format!("{endpoint}/"))
        || !clear(&row["custody_lost"])
        || !clear(&row["scheduler_paused"])
        || !row["retirement"].is_null()
        || !clear(&row["parked"])
        || row["external_observed"] != true
        || !(row["external_input"].is_null() || row["external_input"] == 0)
    {
        return Err(io::Error::other(
            "unqualified original native VM ledger binding",
        ));
    }
    let caps = ["manager-provenance"];
    let (mut owner, rows) = super::super::migrate_supervisor::verified_owner(
        place.supervisor_root(),
        Path::new(cfg.text("daemon_socket")),
        Path::new(field(&cfg.0["supervisor"], "lock")),
        &caps,
    )?;
    cfg.check_supervisor(&owner, &caps)?;
    let answer = owner.request_body(json!({"op":"manager_runtime","session_id":manager["session_id"],
        "body_version":1,"manager_runtime_request":{"session_instance":manager["session_instance"]}}),"manager_runtime")?;
    let current = &answer["manager_runtime"];
    let active_nonce = field(&current["turn"], "native_input_id");
    if active_nonce.len() != 64
        || !active_nonce
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return Err(io::Error::other(
            "no live supervisor native input for original VM wake",
        ));
    }
    let mut checksum = [0; 32];
    let mut count = 0;
    let mut admitted = None;
    for (key, raw) in records.iter_entries() {
        count += 1;
        if count > 4096 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        for (left, right) in checksum.iter_mut().zip(record_hash(key, raw)) {
            *left ^= right;
        }
        if key.starts_with(b"admissions\0") {
            let admission: Value = serde_json::from_slice(raw)?;
            if field(&admission, "settled_at") == "0001-01-01T00:00:00Z"
                && admission["native_input_id"] == active_nonce
                && admitted
                    .replace((admission, key.to_vec(), raw.to_vec()))
                    .is_some()
            {
                return Err(io::Error::other(
                    "duplicate live original VM native admission",
                ));
            }
        }
    }
    if header["records"] != count || header["record_hash"] != json!(checksum) {
        return Err(io::Error::other(
            "original VM voice record checksum mismatch",
        ));
    }
    let (admission, admission_key, admission_raw) =
        admitted.ok_or_else(|| io::Error::other("no unsettled original VM admission"))?;
    let sequence = admission["sequence"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(invalid)?;
    let wake_id = field(&admission, "source_id")
        .strip_prefix("wake:")
        .ok_or_else(|| io::Error::other("original active VM admission is not a worker wake"))?;
    let nonce = field(&admission, "native_input_id");
    let request = &admission["request"];
    let native = &request["manager_admission"];
    let mut expected = manager.clone();
    let fields = expected.as_object_mut().ok_or_else(invalid)?;
    let session = fields.remove("session_id").ok_or_else(invalid)?;
    fields.insert("session".to_owned(), session);
    fields.remove("native_cli");
    fields.remove("generation");
    if nonce.len() != 64
        || wake_id.is_empty()
        || admission["conversation_id"] != conversation
        || !matches!(
            field(&admission, "phase"),
            "submitted" | "admitted" | "running"
        )
        || !clear(&admission["recovery_only"])
        || !clear(&admission["native_binding_retired"])
        || !admission["origin"].is_null()
        || !request["origin"].is_null()
        || request["session"] != manager["session_id"]
        || request["session_instance"] != manager["session_instance"]
        || native["binding"] != expected
        || native["nonce"] != nonce
        || native["expected_external_input"] != 0
        || record(
            &records,
            "admissions",
            &[&conversation, &sequence.to_string()],
        )? != admission
    {
        return Err(io::Error::other(
            "unqualified original VM admission and native request",
        ));
    }
    let wake = record(&records, "wakes", &[&conversation, wake_id])?;
    let event = record(
        &records,
        "events",
        &[&conversation, field(&wake, "event_id")],
    )?;
    let note = &event["metadata"];
    let attempt = field(&event, "attempt_id");
    let session = field(note, "Session");
    let instance = field(note, "Instance");
    let note_id = field(note, "NoteID");
    let event_id = format!(
        "worker-message-{}",
        hash(&json!([session, instance, attempt, note_id]))?
    );
    let job_id = format!("vm-child-{}", hash(&json!([conversation, session]))?);
    let worker_key = serde_json::to_string(&[session, instance])?;
    let worker = record(&records, "workers", &[&conversation, &worker_key])?;
    let parent_sequence = worker["admission"]
        .as_u64()
        .filter(|n| *n > 0 && *n < sequence)
        .ok_or_else(invalid)?;
    let parent = record(
        &records,
        "admissions",
        &[&conversation, &parent_sequence.to_string()],
    )?;
    let origin = &parent["origin"];
    let parent_local = field(manager, "session_id")
        .rsplit('/')
        .next()
        .ok_or_else(invalid)?;
    let checkout = field(&worker, "project");
    let project = Path::new(checkout);
    let project_before = fs::symlink_metadata(project)?;
    if wake["id"] != wake_id
        || wake["conversation_id"] != conversation
        || wake["state"] != "native_input"
        || wake["event_id"] != event_id
        || event["id"] != event_id
        || wake["request_hash"] != hash(&wake["request"])?
        || wake["native_input_id"] != nonce
        || wake["admission"] != sequence
        || wake["operation_id"] != admission["operation_id"]
        || !clear(&wake["recovery_only"])
        || event["conversation_id"] != conversation
        || event["wake_id"] != wake_id
        || event["worker_message"] != true
        || !clear(&event["worker_proof"])
        || !clear(&event["recovery_only"])
        || note["Kind"] != "complete"
        || note_id.is_empty()
        || !session.starts_with(&format!("{endpoint}/"))
        || instance.is_empty()
        || !attempt.starts_with("vm-child-")
        || worker["conversation_id"] != conversation
        || worker["session"] != session
        || worker["session_instance"] != instance
        || worker["attempt_id"] != attempt
        || worker["job_id"] != event["job_id"]
        || event["job_id"] != job_id
        || worker["mailbox"] != true
        || worker["vm_child"] != true
        || worker["action_id"] != ""
        || worker["parent"] != parent_local
        || !clear(&worker["recovery_required"])
        || !clear(&worker["reserved"])
        || parent["source_cutover"] != true
        || parent["phase"] != "completed"
        || !clear(&parent["recovery_only"])
        || field(&parent, "native_input_id").is_empty()
        || worker["parent_input_hash"]
            != supervisor::mail::parent_hash(field(&parent, "native_input_id"))
        || origin["kind"] != "owner_direct"
        || origin["installation_id"] != cfg.text("installation_id")
        || origin["actor_id"] != cfg.text("owner_id")
        || origin["requester_id"] != origin["actor_id"]
        || origin["discussion_id"] != route["ID"]
        || origin["channel_id"] != route["Route"]["channel_id"]
        || !origin["guild_id"].is_null()
        || parent["project"] != checkout
        || !project.is_absolute()
        || !project_before.is_dir()
        || fs::canonicalize(project)? != project
    {
        return Err(io::Error::other(
            "unqualified original owned VM worker completion",
        ));
    }
    publication::verify_guard(connector, cfg, origin)
        .map_err(|error| io::Error::other(format!("original VM parent receipt: {error}")))?;
    let guard_key = format!(
        "{}\0{}",
        cfg.text("installation_id"),
        field(origin, "message_id")
    );
    if value(connector, "teammate_ingress_guard", guard_key.as_bytes())?
        .is_none_or(|guard| guard["LastChange"] != "")
    {
        return Err(io::Error::other(
            "original VM worker's parent Discord source changed",
        ));
    }
    let binding = json!({"Discussion":conversation,"Parent":parent_local,"Session":session,"Instance":instance,
        "Attempt":attempt,"Project":checkout,"Agent":note["Agent"],"ParentInputHash":worker["parent_input_hash"],
        "Kind":note["Kind"],"Text":note["Text"],"Sequence":note["Sequence"]});
    let source = json!({"id":note_id,"binding":binding,"files":note["files"]});
    let captures =
        supervisor::mail_store::read_publication_files(place.supervisor_root(), &source)?;
    if captures.is_empty() {
        return Err(invalid());
    }
    if rows
        .iter()
        .filter(|row| {
            row["id"] == session
                && row["worker_discussion"] == conversation
                && row["worker_instance"] == instance
                && row["worker_attempt"] == attempt
                && row["worker_parent_input_hash"] == worker["parent_input_hash"]
                && row["cwd"] == checkout
                && row["spawned_from"] == parent_local
        })
        .count()
        != 1
        || rows
            .iter()
            .filter(|row| {
                row["id"] == manager["session_id"]
                    && row["capabilities"]["session_instance"] == manager["session_instance"]
                    && row["role"] == "manager"
                    && row["manager_conversation_id"] == conversation
                    && row["cwd"] == cfg.text("work_dir")
            })
            .count()
            != 1
    {
        return Err(io::Error::other(
            "unqualified original live VM worker and manager roster",
        ));
    }
    let current_project = fs::symlink_metadata(project)?;
    let auth = place.dir.join("discussions").join(id).join("bridge.auth");
    let bridge = fs::canonicalize("/tmp")?
        .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
        .join(&id[..32])
        .join("bridge.sock");
    open_owner_file(&auth)?;
    if current["binding"] != expected
        || current["auth_file"] != json!(auth)
        || current["bridge_socket"] != json!(bridge)
        || current["native_cli"] != true
        || current["turn"]["native_input_id"] != nonce
        || !matches!(field(&current["turn"], "state"), "admitted" | "running")
        || !clear(&current["turn"]["externally_steered"])
        || current["turn"]["external_input"] != 0
        || (current_project.dev(), current_project.ino())
            != (project_before.dev(), project_before.ino())
    {
        return Err(io::Error::other(
            "unqualified original live admitted VM turn",
        ));
    }
    let output_source =
        json!({"admission_id":wake_id,"job_id":event["job_id"],"attempt_id":attempt});
    let binding = manager.clone();
    let receipt = Receipt {
        header,
        binding,
        conversation: row,
        admission,
        key: admission_key,
        raw: admission_raw,
    };
    Ok(Admitted {
        receipt,
        origin: origin.clone(),
        source,
        output_source,
        checkout_identity: (project_before.dev(), project_before.ino()),
    })
}
