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
    let mut expected_binding = receipts.binding.clone();
    let fields = expected_binding.as_object_mut().ok_or_else(invalid)?;
    fields.remove("native_cli");
    fields.remove("generation");
    let session = fields.remove("session_id").ok_or_else(invalid)?;
    fields.insert("session".into(), session);
    let request_proof = &receipts.admission["request"]["manager_admission"];
    if request_proof["binding"] != expected_binding
        || !(request_proof["expected_external_input"].is_null()
            || request_proof["expected_external_input"] == 0)
    {
        return Err(invalid());
    }
    let current = &receipts.conversation;
    if current["external_observed"] != true
        || !(current["external_input"].is_null() || current["external_input"] == 0)
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
            let canonical = prepare::go_bytes(&row)?;
            if canonical != raw
                || origin["kind"] != "owner_direct"
                || origin["installation_id"] != cfg.text("installation_id")
                || origin["channel_id"] != cfg.text("dm_channel_id")
                || origin["actor_id"] != cfg.text("owner_id")
                || origin["requester_id"] != cfg.text("owner_id")
                || !origin["guild_id"].is_null()
                || !origin["voice"].is_null()
            {
                return Err(invalid());
            }
            return Ok((
                origin.clone(),
                inspect_voice(tx, ledger, origin, cfg, endpoint)?,
            ));
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
        || !(admission["reply_recovery"].is_null() || admission["reply_recovery"] == 0)
        || (!admission["reply_check_after"].is_null() && !admission["reply_check_after"].is_i64())
        || (!admission["reply_progress"].is_null() && !admission["reply_progress"].is_u64())
        || (!admission["reply_stall_since"].is_null() && !admission["reply_stall_since"].is_i64())
        || admission["reply_delivery_unknown"] == true
        || admission["reply_waiting_for_human"] == true
        || admission["native_binding_retired"] == true
        || admission["recovery_only"] == true
        || receipt.header["reply_wait_consumed"] == true
    {
        return Err(unsupported());
    }
    let after = admission["reply_check_after"].as_i64().unwrap_or_default();
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
        if row["conversation_id"] != conversation
            || row.as_object().is_none_or(|row| {
                row.keys()
                    .any(|key| key.bytes().any(|b| b.is_ascii_uppercase()))
            })
            || prepare::go_bytes(&row)? != raw
        {
            return Err(invalid());
        }
        if k.starts_with(b"admissions\0") {
            let seq = row["sequence"].as_u64().ok_or_else(invalid)?;
            let timer = row["reply_check_after"].as_i64().unwrap_or_default();
            if k != key("admissions", conversation, &seq.to_string())?
                || field(&row, "settled_at").parse::<Timestamp>().is_err()
                || row["settled_at"] == EMPTY_TIME
                || !row["origin"].is_null() && timer != 0 && (timer, seq) < (after, sequence)
            {
                return Err(unsupported());
            }
        }
        if k.starts_with(b"workers\0") {
            if row["admission"] != sequence {
                if row["admission"] == 0 && row["vm_child"] == true && row["ended"] != true {
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
            if k != key("publications", conversation, field(&row, "id"))? {
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
    if !admission["reply_stall_since"].is_i64()
        || admission["reply_stall_since"] == 0
        || progress > admission["reply_progress"].as_u64().unwrap_or_default()
    {
        admission["reply_stall_since"] = json!(now.as_second());
    }
    let mut changes = Vec::new();
    let description = if progress > admission["reply_progress"].as_u64().unwrap_or_default() {
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
        let next = receipt.conversation["last_output"]
            .as_u64()
            .unwrap_or_default()
            .checked_add(1)
            .ok_or_else(invalid)?;
        receipt.conversation["last_output"] = json!(next);
        admission["reply_check_after"] = json!(
            now.as_second()
                .checked_add(RECHECK_SECONDS)
                .ok_or_else(invalid)?
        );
        changes.push((
            format!("conversations\0{conversation}").into_bytes(),
            receipt.conversation,
        ));
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
    if let Some(out) = value(connector, "teammate_outbox", key.as_bytes())? {
        let nonce = hash(&json!([install, id, output, 0]))?;
        let chunks = out["Chunks"]
            .as_array()
            .filter(|chunks| chunks.len() == 1)
            .ok_or_else(unsupported)?;
        if out["Route"] == route["Route"]
            && out["DiscussionID"] == id
            && out["Internal"] == false
            && out["ExportAck"] == true
            && out["Publication"] == proof
            && out["BodyHash"] == body_hash
            && out["ChunkHashes"] == json!([body_hash])
            && chunks[0]["id"] == nonce[..24]
            && connector
                .bucket(b"teammate_meta")
                .ok_or_else(invalid)?
                .get(format!("{install}\0nonce:{}", &nonce[..24]).as_bytes())
                == Some(key.as_bytes())
            && chunks[0]["state"] == "sent"
            && snowflake(field(&chunks[0], "message_id"))
            && (chunks[0]["gateway_seen"] == true || chunks[0]["rest_acknowledged"] == true)
        {
            return Ok(());
        }
        return Err(unsupported());
    }
    let receipt = value(connector, "teammate_receipts", key.as_bytes())?.ok_or_else(unsupported)?;
    if receipt["Route"] == route["Route"]
        && receipt["PublicationHash"] == hash(&proof)?
        && receipt["BodyHash"] == body_hash
        && receipt["DiscordMessageIDs"]
            .as_array()
            .is_some_and(|ids| ids.len() == 1 && ids[0].as_str().is_some_and(snowflake))
        && receipt["UnverifiedMessageIDs"].is_null()
        && receipt["DiscordAttachmentIDs"].is_null()
    {
        return Ok(());
    }
    Err(unsupported())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;

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
            }
            if scenario == "worker" {
                let mut running = roster.clone();
                running[0]["status"]["Process"] = json!("running");
                assert!(rust_step("701", base + 30, &running).is_err());
                assert!(rust_step("701", base + 30, &[]).is_err());
                assert_eq!(fs::read(&db_path).unwrap(), bytes);
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
