//! Reconcile one settled original Go VM reply without taking native or outbox custody.
use super::*;
use crate::discord::rest::Discord;
use jiff::Timestamp;

const UNVERIFIED: &str =
    "I don’t have a confirmed final answer for this yet. Ask me here for a status update.";
const RECHECK_SECONDS: i64 = 120;
const WINDOW_SECONDS: i64 = 30 * 60;
const EMPTY_TIME: &str = "0001-01-01T00:00:00Z";

fn unsupported() -> io::Error {
    io::Error::other("the original VM reply still requires the Go discussion controller")
}

fn hash(value: &Value) -> io::Result<String> {
    Ok(format!("{:x}", Sha256::digest(prepare::go_bytes(value)?)))
}

fn counter<T: Default>(row: &Value, name: &str, read: fn(&Value) -> Option<T>) -> io::Result<T> {
    match row.get(name) {
        Some(value) => read(value).ok_or_else(invalid),
        None => Ok(T::default()),
    }
}

fn canonical_voice_row(row: &Value, raw: &[u8]) -> io::Result<bool> {
    Ok(row.as_object().is_some_and(|object| {
        object
            .keys()
            .all(|name| name.bytes().all(|b| b.is_ascii_lowercase() || b == b'_'))
    }) && prepare::go_bytes(row)? == raw)
}

// Go matches JSON member names case-insensitively. Validate the typed source
// objects this check reads, while letting opaque journal payloads keep their keys.
fn string_fields(row: &Value, required: &[&str], optional: &[&str]) -> bool {
    row.as_object().is_some_and(|fields| {
        required.iter().all(|name| fields.contains_key(*name))
            && fields.iter().all(|(name, value)| {
                required.contains(&name.as_str()) && value.is_string()
                    || optional.contains(&name.as_str())
                        && value.as_str().is_some_and(|s| !s.is_empty())
            })
    })
}

fn message_origin(origin: &Value) -> bool {
    string_fields(
        origin,
        &["kind", "installation_id", "request_id", "actor_id"],
        &[
            "requester_id",
            "actor_name",
            "guild_id",
            "channel_id",
            "message_id",
            "endorser_id",
            "discussion_id",
            "revision",
        ],
    )
}

fn exact_admission_request(receipt: &Receipt, origin: &Value) -> io::Result<()> {
    let mut binding = receipt.binding.clone();
    let fields = binding.as_object_mut().ok_or_else(invalid)?;
    if fields.shift_remove("native_cli") != Some(json!(true))
        || fields.shift_remove("generation") != Some(json!(0))
    {
        return Err(invalid());
    }
    let session = fields.shift_remove("session_id").ok_or_else(invalid)?;
    fields.insert("session".into(), session);
    if !string_fields(
        &binding,
        &[
            "session",
            "session_instance",
            "conversation_id",
            "provider",
            "native_session_id",
            "native_generation",
            "observation_generation",
            "login_epoch",
        ],
        &[],
    ) {
        return Err(invalid());
    }
    let request = &receipt.admission["request"];
    let grant = &request["manager_admission"];
    if request.as_object().is_none_or(|fields| {
        fields.iter().any(|(name, value)| match name.as_str() {
            "manager_admission" | "origin" => false,
            "session" | "text" => !value.is_string(),
            "worker_manager_conversation" | "session_instance" | "expected_turn" => {
                !value.as_str().is_some_and(|s| !s.is_empty())
            }
            _ => true,
        })
    }) || !request["text"].is_string()
        || request["origin"] != *origin
        || grant
            != &json!({
                "expected_external_input":0,"binding":binding,"nonce":receipt.admission["native_input_id"]
            })
    {
        return Err(invalid());
    }
    Ok(())
}

fn key(table: &str, conversation: &str, suffix: &str) -> io::Result<Vec<u8>> {
    Ok(format!(
        "{table}\0{}",
        serde_json::to_string(&[conversation, suffix])?
    )
    .into_bytes())
}

pub(in crate::discord) fn run(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    let cfg = personal_config(place, true)?;
    if !snowflake(message) {
        return Err(invalid());
    }
    let room = Discord::connect(&cfg)?;
    let source = place.dir.join("discord.db");
    let expected = fs::symlink_metadata(&source)?;
    let lease = ConnectorLease::acquire(place, &cfg, &expected, true)?;
    let connector = lease.db.begin().map_err(io::Error::other)?;
    let route = activation::qualified_binding(&connector, &cfg)?;
    if route["Route"]["message_id"] != "" {
        return Err(invalid());
    }
    let (directory, ledger, endpoint) = voice_location(place, &cfg, &route)?;
    let custody = bridge::Custody::claim(&directory, &ledger, true)?;
    custody.verify_stopped()?;
    open_owner_file(&directory.join("voice.initialized"))?;
    let checkout = Path::new(cfg.text("work_dir"));
    if !grants::project(&cfg, cfg.text("work_dir")) {
        return Err(invalid());
    }
    let checkout_pin = fs::File::open(checkout)?;
    let source = directory.join("voice.db");
    let (pin, mut voice) = bounded_writer(writable_pin(&source)?)?;
    let tx = voice.begin_rw().map_err(io::Error::other)?;
    let (origin, receipts) = source_and_receipts(&tx, &cfg, &ledger, message, &endpoint)?;
    if origin["discussion_id"] != route["ID"] {
        return Err(invalid());
    }
    publication::verify_guard(&connector, &cfg, &origin)?;
    let current = &receipts.conversation;
    if current["external_observed"] != true
        || counter(current, "external_input", Value::as_u64)? != 0
        || [
            "custody_lost",
            "scheduler_paused",
            "parked",
            "needs_handoff",
        ]
        .iter()
        .any(|flag| !matches!(current[*flag], Value::Null | Value::Bool(false)))
        || !current["retirement"].is_null()
    {
        return Err(unsupported());
    }
    let roster =
        activation::idle_manager_roster(place, &cfg, &directory, &ledger, &receipts.binding)?;
    let now = Timestamp::now();
    let binding = receipts.binding.clone();
    let decision = decide(&tx, receipts, &roster, now, |partial| {
        observed(&connector, &route, partial)
    })?;
    // Original connector repair receipts and a fresh Discord read fence this stopped handoff.
    service::Room::verify(&room, &cfg)?;
    let (rechecked_origin, rechecked) =
        source_and_receipts(&tx, &cfg, &ledger, message, &endpoint)?;
    let roster = activation::idle_manager_roster(place, &cfg, &directory, &ledger, &binding)?;
    let final_decision = decide(&tx, rechecked, &roster, now, |partial| {
        observed(&connector, &route, partial)
    })?;
    if rechecked_origin != origin || final_decision != decision {
        return Err(invalid());
    }
    let fresh = service::Room::message(&room, &cfg, message)?;
    if !fresh["message_snapshots"].is_null()
        || original_revision(&fresh, &origin)? != origin["revision"]
    {
        return Err(invalid());
    }
    activation::require_no_pending_vm_mail(place, &format!("discord-teammate-{ledger}"))?;
    custody.verify_stopped()?;
    if !lease.source_unchanged()?
        || !identity(&pin, &source)?
        || !identity(&checkout_pin, checkout)?
        || personal_config(place, true)?.0 != cfg.0
    {
        return Err(invalid());
    }
    let description = match decision {
        Decision::Wait => "The original reply check is scheduled for later.",
        Decision::Existing => "The original Go reply check is already closed or has a notice.",
        Decision::Write {
            description,
            changes,
            header,
        } => {
            if !dry_run {
                manager_publication::apply_voice(tx, header, &changes)?;
            }
            description
        }
    };
    if dry_run {
        println!("Dry run. {description}");
    } else {
        println!("{description}");
    }
    Ok(true)
}

fn source_and_receipts<'a>(
    tx: &impl TxApi<'a>,
    cfg: &Config,
    ledger: &str,
    message: &str,
    endpoint: &str,
) -> io::Result<(Value, Receipt)> {
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    if records.iter_buckets().next().is_some() {
        return Err(invalid());
    }
    for (count, (k, raw)) in records.iter_entries().enumerate() {
        if count >= 4096 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        if !k.starts_with(b"admissions\0") {
            continue;
        }
        let row: Value = serde_json::from_slice(raw)?;
        let origin = &row["origin"];
        if origin["message_id"] == message {
            if !canonical_voice_row(&row, raw)?
                || !message_origin(origin)
                || origin["kind"] != "owner_direct"
                || origin["installation_id"] != cfg.text("installation_id")
                || origin["channel_id"] != cfg.text("dm_channel_id")
                || origin["actor_id"] != cfg.text("owner_id")
                || origin["requester_id"] != cfg.text("owner_id")
                || origin.get("guild_id").is_some()
                || origin.get("endorser_id").is_some()
            {
                return Err(invalid());
            }
            let receipt = inspect_voice(tx, ledger, origin, cfg, endpoint)?;
            exact_admission_request(&receipt, origin)?;
            return Ok((origin.clone(), receipt));
        }
    }
    Err(invalid())
}

#[derive(PartialEq)]
enum Decision {
    Wait,
    Existing,
    Write {
        description: &'static str,
        changes: Vec<(Vec<u8>, Value)>,
        header: Value,
    },
}

fn decide<'a>(
    tx: &impl TxApi<'a>,
    mut receipt: Receipt,
    roster: &[Value],
    now: Timestamp,
    mut observed: impl FnMut(&Value) -> io::Result<()>,
) -> io::Result<Decision> {
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    let admission = &mut receipt.admission;
    let conversation = field(&receipt.binding, "conversation_id");
    let conversation_key = format!("conversations\0{conversation}").into_bytes();
    if !canonical_voice_row(
        &receipt.conversation,
        records.get(&conversation_key).ok_or_else(invalid)?,
    )? || !canonical_voice_row(&receipt.header, root.get(b"state").ok_or_else(invalid)?)?
    {
        return Err(invalid());
    }
    let last_output = counter(&receipt.conversation, "last_output", Value::as_u64)?;
    let last_admission = receipt.conversation["last_admission"]
        .as_u64()
        .ok_or_else(invalid)?;
    let retired_through = receipt.conversation["retired_through"]
        .as_u64()
        .ok_or_else(invalid)?;
    let after = counter(admission, "reply_check_after", Value::as_i64)?;
    let prior_progress = counter(admission, "reply_progress", Value::as_u64)?;
    let stall_since = counter(admission, "reply_stall_since", Value::as_i64)?;
    let receipt_checks = counter(admission, "reply_receipt_checks", Value::as_i64)?;
    let recovery = counter(admission, "reply_recovery", Value::as_u64)?;
    let request = field(&admission["origin"], "request_id").to_owned();
    let revision = field(&admission["origin"], "revision");
    let notice = format!("reply-unresolved-{}", hash(&json!([request, revision]))?);
    let notice_key = key("publications", conversation, &notice)?;
    let sequence = admission["sequence"].as_u64().ok_or_else(invalid)?;
    let turn = field(admission, "native_turn_id");
    if admission["reply_tracked"] != true
        || admission["phase"] != "completed"
        || admission["source_cutover"] != true
        || !field(admission, "settled_at")
            .parse::<Timestamp>()
            .is_ok_and(|when| when < now)
        || admission["settled_at"] == EMPTY_TIME
        || turn.is_empty()
        || turn.len() > 512
        || records.get(&key("closed_turns", conversation, turn)?) != Some(b"true".as_slice())
        || sequence == 0
        || sequence > last_admission
        || retired_through > last_admission
        || prior_progress > last_output
        || receipt_checks < 0
        || recovery != 0
        || admission["reply_delivery_unknown"] == true
        || admission["reply_waiting_for_human"] == true
        || admission["native_binding_retired"] == true
        || admission["recovery_only"] == true
        || !matches!(
            receipt.header.get("reply_wait_consumed"),
            None | Some(Value::Bool(false))
        )
    {
        return Err(unsupported());
    }
    if after == 0 || records.get(&notice_key).is_some() {
        return Ok(Decision::Existing);
    }
    if after > now.as_second() {
        return Ok(Decision::Wait);
    }

    let parent_hash = format!(
        "{:x}",
        Sha256::digest(field(admission, "native_input_id").as_bytes())
    );
    let parent = field(&receipt.binding, "session_id")
        .rsplit('/')
        .next()
        .ok_or_else(invalid)?;
    let mut worker_attempts = std::collections::BTreeSet::new();
    let mut checked_workers = std::collections::BTreeSet::new();
    let mut publications = Vec::new();
    let mut wakes = Vec::new();
    for (k, raw) in records.iter_entries() {
        let Some(table) = k.split(|b| *b == 0).next() else {
            continue;
        };
        if !matches!(
            table,
            b"admissions"
                | b"workers"
                | b"publications"
                | b"requests"
                | b"actions"
                | b"events"
                | b"wakes"
        ) {
            continue;
        }
        let row: Value = serde_json::from_slice(raw)?;
        if row["conversation_id"] != conversation || !canonical_voice_row(&row, raw)? {
            return Err(invalid());
        }
        if matches!(table, b"publications" | b"requests")
            && row.get("discussion_source").is_some_and(|source| {
                !string_fields(
                    source,
                    &[],
                    &["admission_id", "job_id", "attempt_id", "system_cause"],
                ) || source.as_object().is_some_and(|fields| fields.is_empty())
            })
        {
            return Err(invalid());
        }
        if table == b"admissions" {
            if row
                .get("origin")
                .is_some_and(|origin| !message_origin(origin))
            {
                return Err(invalid());
            }
            let seq = row["sequence"].as_u64().ok_or_else(invalid)?;
            let timer = counter(&row, "reply_check_after", Value::as_i64)?;
            if seq == 0
                || seq > last_admission
                || k != key("admissions", conversation, &seq.to_string())?
                || field(&row, "settled_at").parse::<Timestamp>().is_err()
                || row["settled_at"] == EMPTY_TIME
                || !row["origin"].is_null() && timer != 0 && (timer, seq) < (after, sequence)
            {
                return Err(unsupported());
            }
        }
        if k.starts_with(b"workers\0") {
            let linked = row["admission"].as_u64().ok_or_else(invalid)?;
            if linked != sequence {
                if linked == 0 && row["vm_child"] == true && row["ended"] != true {
                    return Err(unsupported());
                }
                continue;
            }
            let session = field(&row, "session");
            let instance = field(&row, "session_instance");
            let attempt = field(&row, "attempt_id");
            let pair = serde_json::to_string(&[session, instance])?;
            let job = format!("vm-child-{}", hash(&json!([conversation, session]))?);
            if row["vm_child"] != true
                || row["mailbox"] != true
                || row["parent"] != parent
                || row["parent_input_hash"] != parent_hash
                || row["ended"] != true
                || row["recovery_required"] == true
                || row["reserved"] == true
                || row["evidence_limited"] == true
                || row["action_id"] != ""
                || row["job_id"] != job
                || !attempt.starts_with("vm-child-")
                || session.is_empty()
                || instance.is_empty()
                || k != key("workers", conversation, &pair)?
                || !worker_attempts.insert(attempt.to_owned())
                || !checked_workers.insert(session.to_owned())
                || roster.iter().filter(|r| r["id"] == session).count() != 1
                || !roster.iter().any(|r| {
                    r["id"] == session
                        && r["worker_instance"] == instance
                        && r["worker_attempt"] == attempt
                        && r["worker_discussion"] == conversation
                        && r["worker_parent_input_hash"] == parent_hash
                        && r["spawned_from"] == parent
                        && r["status"]["Process"] == "exited"
                })
            {
                return Err(unsupported());
            }
        } else if k.starts_with(b"publications\0") {
            if k != key("publications", conversation, field(&row, "id"))?
                || !row["admission"].is_u64()
                || counter(&row, "output_sequence", Value::as_u64)? > last_output
            {
                return Err(invalid());
            }
            publications.push(row);
        } else if k.starts_with(b"requests\0")
            && row["discussion_source"]["admission_id"] == request
            || k.starts_with(b"actions\0") && row["state"] != "settled"
        {
            return Err(unsupported());
        } else if k.starts_with(b"wakes\0") {
            wakes.push(row);
        }
    }
    if wakes.iter().any(|wake| wake["state"] != "settled")
        || roster.iter().any(|r| {
            let worker = r["worker_discussion"] == conversation;
            let unlinked = field(r, "worker_parent_input_hash").is_empty();
            worker
                && r["worker_parent_input_hash"] == parent_hash
                && !checked_workers.contains(field(r, "id"))
                || (worker || r["spawned_from"] == parent)
                    && unlinked
                    && r["status"]["Process"] != "exited"
        })
    {
        return Err(unsupported());
    }
    let mut progress = 0;
    let mut recent = false;
    for publication in &publications {
        let src = &publication["discussion_source"];
        if !field(src, "system_cause").is_empty() {
            continue;
        }
        if src["admission_id"] == request {
            if publication["final"] != false || publication["reply_monitor"] == true {
                return Err(unsupported());
            }
            if publication["admission"] != sequence
                || publication["native_turn_id"] != turn
                || publication["exported"] != true
            {
                return Err(unsupported());
            }
            observed(publication)?;
            progress = progress.max(
                publication["output_sequence"]
                    .as_u64()
                    .filter(|n| *n > 0)
                    .ok_or_else(invalid)?,
            );
        } else if publication["final"] == true && worker_attempts.contains(field(src, "attempt_id"))
        {
            // Go owns worker report attribution through the exact original wake.
            return Err(unsupported());
        }
        if publication["text"] == UNVERIFIED && publication["admission"] != sequence {
            let seq = publication["admission"].as_u64().ok_or_else(invalid)?;
            // A missing or compacted prior admission has the zero Go time and is outside the window.
            if let Some(raw) = records.get(&key("admissions", conversation, &seq.to_string())?) {
                let old: Value = serde_json::from_slice(raw)?;
                let settled = field(&old, "settled_at")
                    .parse::<Timestamp>()
                    .map_err(io::Error::other)?;
                let limit = settled
                    .checked_add(jiff::SignedDuration::from_secs(WINDOW_SECONDS))
                    .map_err(io::Error::other)?;
                recent |= now < limit;
            }
        }
    }

    admission
        .as_object_mut()
        .ok_or_else(invalid)?
        .shift_remove("reply_receipt_checks");
    if stall_since == 0 || progress > prior_progress {
        admission["reply_stall_since"] = json!(now.as_second());
    }
    let mut changes = Vec::new();
    let description = if progress > prior_progress {
        admission["reply_progress"] = json!(progress);
        admission["reply_check_after"] = json!(
            now.as_second()
                .checked_add(RECHECK_SECONDS)
                .ok_or_else(invalid)?
        );
        "The original reply check records observed progress."
    } else if progress > 0 && worker_attempts.is_empty() || recent {
        admission
            .as_object_mut()
            .ok_or_else(invalid)?
            .shift_remove("reply_check_after");
        "The original reply check closes quietly."
    } else {
        let next = last_output.checked_add(1).ok_or_else(invalid)?;
        receipt.conversation["last_output"] = json!(next);
        admission["reply_check_after"] = json!(
            now.as_second()
                .checked_add(RECHECK_SECONDS)
                .ok_or_else(invalid)?
        );
        changes.push((conversation_key, receipt.conversation));
        changes.push((notice_key, json!({
            "reply_monitor":true,"discussion_source":{"admission_id":request},
            "output_sequence":next,"admission":sequence,"speech_epoch":0,"id":notice,
            "conversation_id":conversation,"native_turn_id":"","sequence":0,"text":UNVERIFIED,
            "text_hash":format!("{:x}", Sha256::digest(UNVERIFIED.as_bytes())),"final":true,"speak":false
        })));
        "The original reply check stages a notice for Go delivery."
    };
    changes.push((receipt.key, receipt.admission));
    Ok(Decision::Write {
        description,
        changes,
        header: receipt.header,
    })
}

fn observed<'a>(connector: &impl TxApi<'a>, route: &Value, publication: &Value) -> io::Result<()> {
    let body = field(publication, "text");
    let src = &publication["discussion_source"];
    if body.is_empty()
        || body.trim() != body
        || body.encode_utf16().count() > 1800
        || publication["text_hash"] != format!("{:x}", Sha256::digest(body.as_bytes()))
        || [
            "recipient_id",
            "discussion_files",
            "discussion_projects",
            "discussion_attempts",
        ]
        .iter()
        .any(|f| !field(publication, f).is_empty())
    {
        return Err(unsupported());
    }
    let install = field(route, "InstallationID");
    let id = field(route, "ID");
    let output = field(publication, "id");
    let key = format!("{install}\0{id}\0{output}");
    let proof = json!({
        "SystemCause":"","ID":output,"Body":"","AdmissionID":src["admission_id"],
        "JobID":field(src,"job_id"),"AttemptID":field(src,"attempt_id"),"QuestionID":"",
        "QuestionHash":"","Sequence":publication["output_sequence"]
    });
    let body_hash = hash(&json!(body))?;
    let audience = json!({"guild_id":"","channel_id":route["Route"]["channel_id"],"message_id":""});
    if route["Route"] != audience || !snowflake(field(&audience, "channel_id")) {
        return Err(unsupported());
    }
    let outbox = connector.bucket(b"teammate_outbox").ok_or_else(invalid)?;
    if let Some(raw) = outbox.get(key.as_bytes()) {
        if raw.len() > 1 << 20 {
            return Err(invalid());
        }
        let out: Value = serde_json::from_slice(raw)?;
        let nonce = hash(&json!([install, id, output, 0]))?;
        let nonce = &nonce[..24];
        let chunks = out["Chunks"]
            .as_array()
            .filter(|chunks| chunks.len() == 1)
            .ok_or_else(unsupported)?;
        let message = field(&chunks[0], "message_id");
        let gateway = chunks[0]["gateway_seen"] == true;
        let rest = chunks[0]["rest_acknowledged"] == true;
        let settled = field(&out, "SettledAt");
        let created = field(&out, "Created");
        let attempted = field(&out, "Attempted");
        if !snowflake(message)
            || !(gateway || rest)
            || [settled, created].contains(&EMPTY_TIME)
            || [settled, created, attempted]
                .iter()
                .any(|time| time.parse::<Timestamp>().is_err())
            || connector
                .bucket(b"teammate_meta")
                .ok_or_else(invalid)?
                .get(format!("{install}\0nonce:{nonce}").as_bytes())
                != Some(key.as_bytes())
        {
            return Err(unsupported());
        }
        let mut chunk = json!({"id":nonce,"body":"","state":"sent","message_id":message});
        if gateway {
            chunk["gateway_seen"] = json!(true);
        }
        if rest {
            chunk["rest_acknowledged"] = json!(true);
        }
        let expected = json!({
            "ExportAck":true,"Internal":false,"SettledAt":settled,"Created":created,
            "Publication":proof,"Route":audience,"DiscussionID":id,"BodyHash":body_hash,
            "Chunks":[chunk],"ChunkHashes":[body_hash],"Attempted":attempted,"Error":""
        });
        // Go accepts case aliases and duplicate keys. Match the exact plain receipt the Go writer emits.
        return if prepare::go_bytes(&expected)? == raw {
            Ok(())
        } else {
            Err(unsupported())
        };
    }
    let receipts = connector.bucket(b"teammate_receipts").ok_or_else(invalid)?;
    let raw = receipts
        .get(key.as_bytes())
        .filter(|raw| raw.len() <= 1 << 20)
        .ok_or_else(unsupported)?;
    let receipt: Value = serde_json::from_slice(raw)?;
    let ids = receipt["DiscordMessageIDs"]
        .as_array()
        .filter(|ids| ids.len() == 1 && ids[0].as_str().is_some_and(snowflake))
        .ok_or_else(unsupported)?;
    let expected = json!({
        "PublicationHash":hash(&proof)?,"BodyHash":body_hash,
        "Route":audience,"DiscordMessageIDs":ids,
    });
    if prepare::go_bytes(&expected)? == raw {
        return Ok(());
    }
    Err(unsupported())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;

    const PROGRESS_KEY: &[u8] = b"fixture-personal\x00original-room\x00fixture-progress";

    fn change_progress(path: &Path, bucket: &[u8], change: impl FnOnce(&mut Value)) {
        let (_pin, mut db) = bounded_writer(writable_pin(path).unwrap()).unwrap();
        let mut tx = db.begin_rw().unwrap();
        {
            let mut table = tx.bucket_mut(bucket).unwrap();
            let mut row: Value = serde_json::from_slice(table.get(PROGRESS_KEY).unwrap()).unwrap();
            change(&mut row);
            table
                .put(PROGRESS_KEY, prepare::go_bytes(&row).unwrap())
                .unwrap();
        }
        tx.commit().unwrap();
    }

    fn change_voice(
        path: &Path,
        cfg: &Config,
        table: &str,
        suffix: &str,
        change: impl FnOnce(&mut Value) -> usize,
    ) {
        let (_pin, mut voice) = bounded_writer(writable_pin(path).unwrap()).unwrap();
        let tx = voice.begin_rw().unwrap();
        let (_, receipt) =
            source_and_receipts(&tx, cfg, "fixture-room", "701", "ep-fixture").unwrap();
        let conversation = field(&receipt.binding, "conversation_id");
        let index = if suffix.is_empty() {
            format!("{table}\0{conversation}").into_bytes()
        } else {
            key(table, conversation, suffix).unwrap()
        };
        let mut row: Value = {
            let root = tx.bucket(b"controller").unwrap();
            let records = root.bucket(b"records").unwrap();
            serde_json::from_slice(records.get(&index).unwrap()).unwrap()
        };
        let raw_surplus = change(&mut row);
        let mut header = receipt.header;
        // Go counts the re-encoded typed state, while the record checksum covers the raw bytes.
        header["logical_bytes"] =
            json!(header["logical_bytes"].as_u64().unwrap() - raw_surplus as u64);
        manager_publication::apply_voice(tx, header, &[(index, row)]).unwrap();
    }

    #[test]
    #[ignore = "requires the exact v0.29.25 Go Discord fixture"]
    fn pinned_go_reply_noise_preserves_original_receipt_and_discussion_window_after_reopen() {
        let fixture = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE").unwrap();
        let call = |args: &[&str]| {
            let output = Command::new(&fixture).args(args).output().unwrap();
            assert!(
                output.status.success(),
                "{}",
                String::from_utf8_lossy(&output.stderr)
            );
            output.stdout
        };
        let scratch =
            std::env::temp_dir().join(format!("reply-noise-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&scratch).unwrap();
        let base = 1_795_651_200;
        for scenario in ["partial", "worker", "window"] {
            let root = scratch.join(scenario);
            runtime::private_dir(&root).unwrap();
            let go = root.join("go");
            let rust = root.join("rust");
            let connector = root.join("connector");
            let connector_path = connector.to_str().unwrap();
            let go_path = go.to_str().unwrap();
            let rust_path = rust.to_str().unwrap();
            let run = |path, action, at: i64| -> Value {
                serde_json::from_slice(&call(&[
                    "vm-reply-noise",
                    path,
                    scenario,
                    action,
                    &at.to_string(),
                ]))
                .unwrap()
            };
            let original = run(go_path, "create", base);
            runtime::private_dir(&rust).unwrap();
            for file in ["voice.initialized", "voice.db"] {
                fs::copy(go.join(file), rust.join(file)).unwrap();
            }
            call(&[
                "create",
                connector_path,
                root.to_str().unwrap(),
                "personal",
                "true",
            ]);
            call(&["bind", connector_path, "original-room", "400"]);
            let cfg = Config(
                json!({"installation_id":"fixture-personal","project_access":"vm",
                "owner_id":"200","dm_channel_id":"400","manager_cli":"codex","mode":"personal"}),
            );
            let roster: Vec<Value> = original["workers"].as_object().unwrap().values().map(|worker| {
                json!({"id":worker["session"],"spawned_from":worker["parent"],
                    "worker_instance":worker["session_instance"],"worker_attempt":worker["attempt_id"],
                    "worker_discussion":worker["conversation_id"],"worker_parent_input_hash":worker["parent_input_hash"],
                    "status":{"Process":"exited"}})
            }).collect();
            let compare = |left: Value, right: Value| {
                assert_eq!(left["admissions"], right["admissions"], "{scenario}");
                assert_eq!(left["publications"], right["publications"], "{scenario}");
                assert_eq!(
                    left["conversations"]["discord-teammate-fixture-room"]["last_output"],
                    right["conversations"]["discord-teammate-fixture-room"]["last_output"]
                );
            };
            let db_path = rust.join("voice.db");
            let rust_step =
                |message: &str, when: i64, roster: &[Value]| -> io::Result<&'static str> {
                    let (_pin, connection) = open_bolt(&connector.join("discord.db"), 64 << 20)?;
                    let connector_tx = connection.begin().map_err(io::Error::other)?;
                    let route = value(
                        &connector_tx,
                        "teammate_discussions",
                        b"fixture-personal\x00400",
                    )?
                    .ok_or_else(invalid)?;
                    let (_pin, mut voice) = bounded_writer(writable_pin(&db_path)?)?;
                    let tx = voice.begin_rw().map_err(io::Error::other)?;
                    let (_, receipt) =
                        source_and_receipts(&tx, &cfg, "fixture-room", message, "ep-fixture")?;
                    match decide(
                        &tx,
                        receipt,
                        roster,
                        Timestamp::from_second(when).unwrap(),
                        |partial| observed(&connector_tx, &route, partial),
                    )? {
                        Decision::Wait => Ok("wait"),
                        Decision::Existing => Ok("existing"),
                        Decision::Write {
                            changes, header, ..
                        } => {
                            manager_publication::apply_voice(tx, header, &changes)?;
                            Ok("wrote")
                        }
                    }
                };

            let bytes = fs::read(&db_path).unwrap();
            assert_eq!(rust_step("701", base + 29, &roster).unwrap(), "wait");
            assert_eq!(fs::read(&db_path).unwrap(), bytes);
            if scenario != "window" {
                assert!(rust_step("701", base + 30, &roster).is_err());
                assert_eq!(fs::read(&db_path).unwrap(), bytes);
                let mode = if scenario == "worker" {
                    "compact"
                } else {
                    "direct"
                };
                call(&["vm-reply-progress", connector_path, mode]);
                let connector_file = connector.join("discord.db");
                let original_connector = fs::read(&connector_file).unwrap();
                let (table, conflicts) = if scenario == "worker" {
                    (
                        b"teammate_receipts".as_slice(),
                        [
                            ("", "unverifiedmessageids", json!(["909"])),
                            ("", "discordmessageids", json!([])),
                        ],
                    )
                } else {
                    (
                        b"teammate_outbox".as_slice(),
                        [
                            ("", "internal", json!(true)),
                            ("/Chunks/0", "Gateway_Seen", json!(false)),
                        ],
                    )
                };
                for (section, alias, bad) in conflicts {
                    change_progress(&connector_file, table, |row| {
                        row.pointer_mut(section).unwrap()[alias] = bad;
                    });
                    let damaged_connector = fs::read(&connector_file).unwrap();
                    assert_eq!(
                        rust_step("701", base + 30, &roster)
                            .unwrap_err()
                            .to_string(),
                        unsupported().to_string(),
                        "{scenario}",
                    );
                    assert_eq!(fs::read(&db_path).unwrap(), bytes);
                    assert_eq!(fs::read(&connector_file).unwrap(), damaged_connector);
                    fs::write(&connector_file, &original_connector).unwrap();
                }
            }
            if scenario == "worker" {
                let mut running = roster.clone();
                running[0]["status"]["Process"] = json!("running");
                assert!(rust_step("701", base + 30, &running).is_err());
                assert!(rust_step("701", base + 30, &[]).is_err());
                assert_eq!(fs::read(&db_path).unwrap(), bytes);
            }
            let reject_damaged_voice = |description: &str, typed: bool| {
                let expected = if typed {
                    "the original Voice records cannot reopen in Go".to_owned()
                } else {
                    invalid().to_string()
                };
                let damaged = fs::read(&db_path).unwrap();
                assert_eq!(
                    rust_step("701", base + 30, &roster)
                        .unwrap_err()
                        .to_string(),
                    expected,
                    "{description}",
                );
                assert_eq!(fs::read(&db_path).unwrap(), damaged);
            };
            if scenario == "partial" {
                change_voice(&db_path, &cfg, "publications", "fixture-progress", |row| {
                    row["discussion_source"]["ADMISSION_ID"] = json!("unrelated");
                    let mut go_decoded = row.clone();
                    let source = go_decoded["discussion_source"].as_object_mut().unwrap();
                    let alias = source.shift_remove("ADMISSION_ID").unwrap();
                    source.insert("admission_id".into(), alias);
                    prepare::go_bytes(row).unwrap().len()
                        - prepare::go_bytes(&go_decoded).unwrap().len()
                });
                reject_damaged_voice("partial attribution alias", true);
                let reopened = run(rust_path, "read", base + 30);
                assert_eq!(
                    reopened["publications"]
                        .as_object()
                        .unwrap()
                        .values()
                        .next()
                        .unwrap()["discussion_source"]["admission_id"],
                    "unrelated",
                );
                fs::write(&db_path, &bytes).unwrap();
                for (table, suffix, section, name) in [
                    ("admissions", "1", "/origin", "REQUEST_ID"),
                    ("admissions", "1", "/request", "SESSION"),
                    ("admissions", "1", "/request/manager_admission", "NONCE"),
                    (
                        "admissions",
                        "1",
                        "/request/manager_admission/binding",
                        "CONVERSATION_ID",
                    ),
                    ("conversations", "", "/binding", "SESSION_ID"),
                ] {
                    change_voice(&db_path, &cfg, table, suffix, |row| {
                        row.pointer_mut(section).unwrap()[name] = json!("unrelated");
                        0
                    });
                    reject_damaged_voice(name, name == "SESSION_ID");
                    fs::write(&db_path, &bytes).unwrap();
                }
            }
            if scenario == "window" {
                for (table, suffix, counter_name, bad) in [
                    ("conversations", "", "last_output", json!("wrong type")),
                    ("conversations", "", "last_admission", Value::Null),
                    ("admissions", "1", "reply_receipt_checks", json!([])),
                    ("admissions", "2", "reply_check_after", json!("wrong type")),
                ] {
                    change_voice(&db_path, &cfg, table, suffix, |row| {
                        row[counter_name] = bad;
                        0
                    });
                    reject_damaged_voice(counter_name, true);
                    fs::write(&db_path, &bytes).unwrap();
                }
            }
            let step = |message, at| {
                let expected = run(go_path, "check", at);
                assert_eq!(
                    rust_step(message, at, &roster).unwrap(),
                    "wrote",
                    "{scenario} {message} at {at}"
                );
                let actual = run(rust_path, "read", at);
                compare(expected, actual.clone());
                actual
            };
            let monitor_count = |state: &Value| {
                state["publications"]
                    .as_object()
                    .unwrap()
                    .values()
                    .filter(|p| p["reply_monitor"] == true)
                    .count()
            };
            let result = step("701", base + 30);
            assert_eq!(monitor_count(&result), usize::from(scenario == "window"));
            if scenario == "window" {
                assert_eq!(monitor_count(&step("702", base + 35)), 1);
                compare(
                    run(go_path, "acknowledge", base + 150),
                    run(rust_path, "acknowledge", base + 150),
                );
                assert_eq!(monitor_count(&step("703", base + 1_800)), 2);
            } else {
                let result = step("701", base + 150);
                assert_eq!(monitor_count(&result), usize::from(scenario == "worker"));
                let bytes = fs::read(&db_path).unwrap();
                assert_eq!(rust_step("701", base + 270, &roster).unwrap(), "existing");
                assert_eq!(fs::read(&db_path).unwrap(), bytes);
                if scenario == "worker" {
                    compare(
                        run(go_path, "acknowledge", base + 270),
                        run(rust_path, "acknowledge", base + 270),
                    );
                }
            }
        }
        fs::remove_dir_all(scratch).unwrap();
    }
}
