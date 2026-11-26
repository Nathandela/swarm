//! Recover publication pins acquired before the original Voice ledger commits.
use super::*;

const NAME: &str = ".rust-vm-file-intent-v1.json";

fn path(place: &Place) -> PathBuf {
    place.dir.join(NAME)
}

fn physical(file: &Path) -> io::Result<(u64, u64)> {
    let meta = fs::symlink_metadata(file)?;
    if !meta.is_file()
        || meta.uid() != unsafe { libc::geteuid() }
        || meta.nlink() != 1
        || meta.mode() & 0o077 != 0
    {
        return Err(invalid());
    }
    Ok((meta.dev(), meta.ino()))
}

fn hash(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}

fn key(table: &str, conversation: &str, id: &str) -> io::Result<String> {
    let coordinates = serde_json::to_string(&[conversation, id])?;
    Ok(format!("{table}\0{coordinates}"))
}

fn coordinates(cfg: &Config, binding: &Value, publication: &str) -> io::Result<(String, String)> {
    let install = cfg.text("installation_id");
    let discussion = field(binding, "ID");
    let raw = crate::ipc::escape_json(serde_json::to_string(&json!([
        install,
        discussion,
        publication,
        0
    ]))?);
    Ok((
        format!("{install}\0{discussion}\0{publication}"),
        hash(raw.as_bytes())[..24].to_owned(),
    ))
}

pub(super) fn prepare(
    place: &Place,
    cfg: &Config,
    binding: &Value,
    voice: &Path,
    request: Value,
) -> io::Result<(u64, u64)> {
    let file = path(place);
    match fs::symlink_metadata(&file) {
        Ok(_) => {
            return Err(io::Error::other(
                "an original VM file publication intent awaits recovery",
            ));
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(error),
    }
    let (_, nonce) = coordinates(cfg, binding, field(&request, "publication"))?;
    let mailbox = physical(&place.supervisor_root().join("dispatcher-mail.db"))?;
    runtime::write_json(
        &file,
        &json!({
            "version":1,"installation":cfg.text("installation_id"),"discussion":binding["ID"],
            "connector":physical(&place.dir.join("discord.db"))?,
            "voice":physical(voice)?,"mailbox":mailbox,
            "nonce":nonce,"request":request
        }),
    )?;
    Ok(mailbox)
}

pub(super) fn clear(place: &Place) -> io::Result<()> {
    fs::remove_file(path(place))?;
    fs::File::open(&place.dir)?.sync_all()
}

pub(super) fn reconcile(
    place: &Place,
    cfg: &Config,
    binding: &Value,
    lease: &ConnectorLease,
) -> io::Result<bool> {
    let marker = match runtime::read_json(&path(place)) {
        Ok(marker) => marker,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(false),
        Err(error) => return Err(error),
    };
    let (directory, id, _) = voice_location(place, cfg, binding)?;
    let voice_path = directory.join("voice.db");
    let mailbox = physical(&place.supervisor_root().join("dispatcher-mail.db"))?;
    let request = &marker["request"];
    let conversation = format!("discord-teammate-{id}");
    let publication = field(request, "publication");
    let sequence = request["sequence"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(invalid)?;
    let expected = manager_publication::publication_id(
        &conversation,
        sequence,
        field(request, "turn"),
        field(request, "logical"),
    )?;
    let (outbox_key, nonce) = coordinates(cfg, binding, publication)?;
    let source = &request["source"];
    if marker["version"] != 1
        || marker["installation"] != cfg.text("installation_id")
        || marker["discussion"] != binding["ID"]
        || marker["nonce"] != nonce
        || marker["connector"] != json!(physical(&place.dir.join("discord.db"))?)
        || marker["voice"] != json!(physical(&voice_path)?)
        || marker["mailbox"] != json!(mailbox)
        || !lease.source_unchanged()?
        || request["conversation"] != conversation
        || expected != publication
        || field(request, "turn").is_empty()
        || field(request, "logical").is_empty()
        || field(request, "native_input").len() != 64
        || field(request, "admission_hash").len() != 64
        || field(request, "text_hash").len() != 64
        || source["binding"]["Discussion"] != conversation
        || !source["files"]
            .as_array()
            .is_some_and(|files| (1..=10).contains(&files.len()))
    {
        return Err(invalid());
    }
    let connector = lease.db.begin().map_err(io::Error::other)?;
    let index = format!("{}\0nonce:{nonce}", cfg.text("installation_id"));
    let nonce_index = connector
        .bucket(b"teammate_meta")
        .ok_or_else(invalid)?
        .get(index.as_bytes())
        .map(<[u8]>::to_vec);
    let old = value(&connector, "teammate_outbox", outbox_key.as_bytes())?;
    let receipt = connector
        .bucket(b"teammate_receipts")
        .ok_or_else(invalid)?
        .get(outbox_key.as_bytes())
        .is_some();
    let (voice_pin, voice) = open_bolt(&voice_path, 64 << 20)?;
    let tx = voice.begin().map_err(io::Error::other)?;
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let header = root
        .get(b"state")
        .filter(|raw| raw.len() <= 4096)
        .ok_or_else(invalid)?;
    let decoded: Value = serde_json::from_slice(header)?;
    if decoded["version"] != 4
        || decoded["current_conversation"] != conversation
        || root.get(b"digest") != Some(hash(header).as_bytes())
    {
        return Err(invalid());
    }
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    if records.iter_buckets().next().is_some() {
        return Err(invalid());
    }
    let mut checksum = [0; 32];
    let mut count = 0;
    for (entry, raw) in records.iter_entries() {
        count += 1;
        if count > 4096 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        for (left, right) in checksum.iter_mut().zip(record_hash(entry, raw)) {
            *left ^= right;
        }
    }
    if decoded["records"] != count || decoded["record_hash"] != json!(checksum) {
        return Err(invalid());
    }
    let prepared = records.get(key("publications", &conversation, publication)?.as_bytes());
    if prepared.is_some() || old.is_some() || receipt || nonce_index.is_some() {
        if let (Some(raw), Some(out)) = (prepared, old) {
            let durable: Value = serde_json::from_slice(raw)?;
            let files: Value = serde_json::from_str(field(&durable, "discussion_files"))?;
            if durable["id"] == publication
                && durable["conversation_id"] == conversation
                && durable["admission"] == sequence
                && durable["native_turn_id"] == request["turn"]
                && durable["text_hash"] == request["text_hash"]
                && durable["discussion_source"] == request["output_source"]
                && files == source["files"]
                && durable["exported"] == true
                && out["ExportAck"] == true
                && out["Publication"]["ID"] == publication
                && out["Publication"]["Files"] == files
                && out["Chunks"][0]["id"] == nonce
                && out["Publication"]["Sequence"] == durable["output_sequence"]
                && out["BodyHash"]
                    == hash(
                        crate::ipc::escape_json(serde_json::to_string(&json!([
                            durable["text"],
                            files,
                            ""
                        ]))?)
                        .as_bytes(),
                    )
                && nonce_index.as_deref() == Some(outbox_key.as_bytes())
                && identity(&voice_pin, &voice_path)?
                && lease.source_unchanged()?
            {
                clear(place)?;
                println!(
                    "The original Go VM file publication already has its durable outbox receipt."
                );
                return Ok(true);
            }
        }
        return Err(io::Error::other(
            "original Go VM publication has a durable or ambiguous referent; its file pin is retained",
        ));
    }
    let admission = records
        .get(key("admissions", &conversation, &sequence.to_string())?.as_bytes())
        .ok_or_else(invalid)?;
    let row: Value = serde_json::from_slice(admission)?;
    let wake_id = field(request, "wake")
        .strip_prefix("wake:")
        .ok_or_else(invalid)?;
    let wake: Value = serde_json::from_slice(
        records
            .get(key("wakes", &conversation, wake_id)?.as_bytes())
            .ok_or_else(invalid)?,
    )?;
    let event_id = field(&wake, "event_id");
    let event: Value = serde_json::from_slice(
        records
            .get(key("events", &conversation, event_id)?.as_bytes())
            .ok_or_else(invalid)?,
    )?;
    let note = &event["metadata"];
    let scope = &source["binding"];
    if wake["id"] != wake_id
        || wake["admission"] != sequence
        || wake["state"] != "native_input"
        || wake["native_input_id"] != request["native_input"]
        || wake["conversation_id"] != conversation
        || event["id"] != event_id
        || event["wake_id"] != wake_id
        || event["conversation_id"] != conversation
        || event["worker_message"] != true
        || note["Kind"] != "complete"
        || request["output_source"]["admission_id"] != wake_id
        || request["output_source"]["attempt_id"] != event["attempt_id"]
        || request["output_source"]["job_id"] != event["job_id"]
        || source["id"] != note["NoteID"]
        || scope["Attempt"] != event["attempt_id"]
        || ["Session", "Instance", "Agent", "Kind", "Text", "Sequence"]
            .iter()
            .any(|key| scope[key] != note[key])
        || !note["files"].as_array().is_some_and(|manifest| {
            source["files"]
                .as_array()
                .is_some_and(|files| files.iter().all(|file| manifest.contains(file)))
        })
    {
        return Err(invalid());
    }
    if hash(admission) != request["admission_hash"]
        || row["native_input_id"] != request["native_input"]
        || row["source_id"] != request["wake"]
        || !identity(&voice_pin, &voice_path)?
        || !lease.source_unchanged()?
        || personal_config(place, true)?.0 != cfg.0
    {
        return Err(invalid());
    }
    supervisor::mail_store::release_prepared_publication(
        place.supervisor_root(),
        source,
        publication,
        mailbox,
    )?;
    clear(place)?;
    println!("Recovered an original Go VM file intent without changing Voice or the outbox.");
    Ok(true)
}
