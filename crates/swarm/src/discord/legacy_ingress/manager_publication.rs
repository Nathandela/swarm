//! Persist one plain final native manager tool call in the original Go ledgers.
use super::*;
use crate::discord::provenance;
use jiff::Timestamp;

const EMPTY_TIME: &str = "0001-01-01T00:00:00Z";

fn go_json(value: &Value) -> io::Result<Vec<u8>> {
    Ok(crate::ipc::escape_json(serde_json::to_string(value)?).into_bytes())
}

fn digest(value: &Value) -> io::Result<String> {
    Ok(format!("{:x}", Sha256::digest(go_json(value)?)))
}

fn key(table: &str, conversation: &str, suffix: &str) -> io::Result<Vec<u8>> {
    let id = crate::ipc::escape_json(serde_json::to_string(&[conversation, suffix])?);
    Ok(format!("{table}\0{id}").into_bytes())
}

fn apply_voice<'tx>(
    mut tx: impl TxRwApi<'tx>,
    mut header: Value,
    changes: &[(Vec<u8>, Value)],
) -> io::Result<()> {
    let mut checksum: [u8; 32] = serde_json::from_value(header["record_hash"].clone())?;
    let mut logical = header["logical_bytes"].as_u64().ok_or_else(invalid)?;
    let mut rows = header["records"].as_u64().ok_or_else(invalid)?;
    {
        let mut root = tx.bucket_mut(b"controller").ok_or_else(invalid)?;
        let mut records = root.bucket_mut(b"records").ok_or_else(invalid)?;
        for (record_key, value) in changes {
            let record = go_json(value)?;
            if record.len() > 1 << 20 {
                return Err(invalid());
            }
            let prior = records.get(record_key).map(<[u8]>::to_vec);
            if let Some(prior) = prior {
                for (a, b) in checksum.iter_mut().zip(record_hash(record_key, &prior)) {
                    *a ^= b;
                }
                logical = logical
                    .checked_sub(prior.len() as u64)
                    .ok_or_else(invalid)?;
            } else {
                let name = std::str::from_utf8(record_key).map_err(io::Error::other)?;
                let (table, index) = name.split_once('\0').ok_or_else(invalid)?;
                let prefix = format!("{table}\0");
                let comma = records
                    .iter_entries()
                    .any(|(k, _)| k.starts_with(prefix.as_bytes()));
                logical = logical
                    .checked_add((go_json(&json!(index))?.len() + 1 + usize::from(comma)) as u64)
                    .ok_or_else(invalid)?;
                rows += 1;
            }
            for (a, b) in checksum.iter_mut().zip(record_hash(record_key, &record)) {
                *a ^= b;
            }
            logical = logical
                .checked_add(record.len() as u64)
                .filter(|n| *n <= 64 << 20)
                .ok_or_else(invalid)?;
            records.put(record_key, record).map_err(io::Error::other)?;
        }
    }
    if rows > 4096 {
        return Err(invalid());
    }
    header["logical_bytes"] = json!(logical);
    header["records"] = json!(rows);
    header["record_hash"] = json!(checksum);
    let raw = go_json(&header)?;
    {
        let mut root = tx.bucket_mut(b"controller").ok_or_else(invalid)?;
        root.put(b"state", &raw).map_err(io::Error::other)?;
        root.put(b"digest", format!("{:x}", Sha256::digest(raw)))
            .map_err(io::Error::other)?;
    }
    tx.commit().map_err(io::Error::other)
}

fn native_proof(
    place: &Place,
    cfg: &Config,
    receipt: &Receipt,
    args: &Value,
    meta: &Value,
) -> io::Result<String> {
    let caps = ["manager-provenance"];
    let (mut owner, _) = super::super::migrate_supervisor::verified_owner(
        place.supervisor_root(),
        Path::new(cfg.text("daemon_socket")),
        Path::new(field(&cfg.0["supervisor"], "lock")),
        &caps,
    )?;
    cfg.check_supervisor(&owner, &caps)?;
    let request = &receipt.admission["request"];
    let admission = &request["manager_admission"];
    let coordinate = provenance::coordinates(&admission["binding"], meta)?;
    let mut args = args.clone();
    args.sort_all_objects();
    let answer = owner.request_body(json!({
        "op":"manager_resolve_call","session_id":request["session"],"body_version":1,
        "manager_call":{"binding":admission["binding"],"tool_name":"publish","arguments_hash":digest(&args)?,"host_meta":meta}
    }),"manager_resolve_call")?;
    let proof = &answer["manager_call_proof"];
    let turn = field(proof, "native_turn_id");
    if answer["endpoint_id"] != owner.endpoint()
        || answer["session_id"] != request["session"]
        || proof["main"] != true
        || proof["native_input_id"] != admission["nonce"]
        || proof["native_call_id"] != coordinate.call
        || turn.is_empty()
        || coordinate.turn.is_some_and(|observed| observed != turn)
    {
        return Err(invalid());
    }
    let runtime = owner.request_body(
        json!({
            "op":"manager_runtime","session_id":request["session"],"body_version":1,
            "manager_runtime_request":{"session_instance":request["session_instance"]}
        }),
        "manager_runtime",
    )?;
    let native = &runtime["manager_runtime"];
    if native["binding"] != admission["binding"]
        || native["turn"]["native_input_id"] != admission["nonce"]
        || native["turn"]["native_turn_id"] != turn
        || native["turn"]["state"] != "running"
        || native["turn"]["externally_steered"] == true
        || native["turn"]["external_input"] != admission["expected_external_input"]
    {
        return Err(invalid());
    }
    Ok(turn.to_owned())
}

fn stage_records<'tx>(
    tx: impl TxRwApi<'tx>,
    mut original: Receipt,
    turn: &str,
    logical: &str,
    text: &str,
    origin: &Value,
) -> io::Result<Value> {
    let conversation = field(&original.binding, "conversation_id").to_owned();
    let seq = original.admission["sequence"]
        .as_u64()
        .ok_or_else(invalid)?;
    let nonce = field(&original.admission, "native_input_id").to_owned();
    let pub_ref = format!(
        "publication:{seq}:{}",
        digest(&json!({
            "Conversation":conversation,"Admission":seq,"Turn":turn,"Logical":logical
        }))?
    );
    let publication_key = key("publications", &conversation, &pub_ref)?;
    let permit_key = key("permits", &conversation, turn)?;
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    if let Some(prior) = records.get(&publication_key) {
        let saved: Value = serde_json::from_slice(prior)?;
        if saved["id"] == pub_ref
            && saved["text"] == text
            && saved["conversation_id"] == conversation
            && saved["admission"] == seq
            && saved["native_turn_id"] == turn
            && saved["final"] == true
        {
            return Ok(saved);
        }
        return Err(invalid());
    }
    if records.get(&permit_key).is_some()
        || records
            .get(&key("closed_turns", &conversation, turn)?)
            .is_some()
        || records
            .iter_entries()
            .any(|(k, _)| k.starts_with(b"publications\0"))
        || original.admission["settled_at"] != EMPTY_TIME
        || original.admission["native_binding_retired"] == true
        || original.admission["recovery_only"] == true
        || !matches!(field(&original.admission, "phase"), "executing" | "running")
    {
        return Err(invalid());
    }
    let epoch = original.conversation["last_speech_epoch"]
        .as_u64()
        .unwrap_or_default()
        .checked_add(1)
        .ok_or_else(invalid)?;
    let output = original.conversation["last_output"]
        .as_u64()
        .unwrap_or_default()
        .checked_add(1)
        .ok_or_else(invalid)?;
    original.admission["phase"] = json!("running");
    original.admission["native_turn_id"] = json!(turn);
    original.conversation["last_speech_epoch"] = json!(epoch);
    original.conversation["last_output"] = json!(output);
    let permit = json!({"conversation_id":conversation,"admission":seq,
        "native_input_id":nonce,"native_turn_id":turn,"epoch":epoch,"revoked":false});
    let result = json!({"id":pub_ref,"conversation_id":conversation,"admission":seq,
        "speech_epoch":epoch,"native_turn_id":turn,"sequence":1,"output_sequence":output,
        "text":text,"text_hash":format!("{:x}",Sha256::digest(text.as_bytes())),"final":true,"speak":true,
        "discussion_source":{"admission_id":origin["request_id"]}});
    let changes = [
        (
            format!("conversations\0{conversation}").into_bytes(),
            original.conversation,
        ),
        (original.key, original.admission),
        (permit_key, permit),
        (publication_key, result.clone()),
    ];
    apply_voice(tx, original.header, &changes)?;
    Ok(result)
}

fn queue(
    cfg: &Config,
    lease: &mut ConnectorLease,
    candidate: &Candidate,
    output: &Value,
) -> io::Result<()> {
    let install = cfg.text("installation_id");
    let id = field(output, "id");
    let discussion = field(&candidate.binding, "ID");
    let key = format!("{install}\0{discussion}\0{id}");
    let nonce = &digest(&json!([install, discussion, id, 0]))?[..24];
    let nonce_key = format!("{install}\0nonce:{nonce}");
    let count_key = format!("{install}\0outbox_count");
    let hash = digest(&output["text"])?;
    let mut tx = lease.db.begin_rw().map_err(io::Error::other)?;
    if let Some(old) = value(&tx, "teammate_outbox", key.as_bytes())? {
        if old["Publication"]["ID"] == id
            && old["BodyHash"] == hash
            && old["Chunks"][0]["id"] == nonce
        {
            return Ok(());
        }
        return Err(invalid());
    }
    let meta = tx.bucket(b"teammate_meta").ok_or_else(invalid)?;
    if meta.get(nonce_key.as_bytes()).is_some()
        || tx
            .bucket(b"teammate_receipts")
            .ok_or_else(invalid)?
            .get(key.as_bytes())
            .is_some()
    {
        return Err(invalid());
    }
    let count = match meta.get(count_key.as_bytes()) {
        None => 0,
        Some(raw) => u64::from_be_bytes(raw.try_into().map_err(io::Error::other)?),
    };
    if count >= 2048 {
        return Err(invalid());
    }
    let record = json!({
        "ExportAck":false,"Internal":false,"SettledAt":EMPTY_TIME,"Created":Timestamp::now().to_string(),
        "Publication":{"SystemCause":"","ID":id,"Body":"","AdmissionID":candidate.origin["request_id"],
            "JobID":"","AttemptID":"","QuestionID":"","QuestionHash":"","Sequence":output["output_sequence"]},
        "Route":candidate.binding["Route"],"DiscussionID":discussion,"BodyHash":hash,
        "Chunks":[{"id":nonce,"body":output["text"],"state":"prepared"}],
        "ChunkHashes":[hash],"Attempted":EMPTY_TIME,"Error":""
    });
    tx.bucket_mut(b"teammate_outbox")
        .ok_or_else(invalid)?
        .put(key.as_bytes(), go_json(&record)?)
        .map_err(io::Error::other)?;
    {
        let mut meta = tx.bucket_mut(b"teammate_meta").ok_or_else(invalid)?;
        meta.put(nonce_key.as_bytes(), key.as_bytes())
            .map_err(io::Error::other)?;
        meta.put(count_key.as_bytes(), (count + 1).to_be_bytes())
            .map_err(io::Error::other)?;
    }
    tx.commit().map_err(io::Error::other)
}

fn acknowledge_export(
    cfg: &Config,
    lease: &mut ConnectorLease,
    candidate: &Candidate,
    output: &Value,
) -> io::Result<()> {
    let key = format!(
        "{}\0{}\0{}",
        cfg.text("installation_id"),
        field(&candidate.binding, "ID"),
        field(output, "id")
    );
    let mut tx = lease.db.begin_rw().map_err(io::Error::other)?;
    let mut row = value(&tx, "teammate_outbox", key.as_bytes())?.ok_or_else(invalid)?;
    if row["BodyHash"] != digest(&output["text"])? || row["Publication"]["ID"] != output["id"] {
        return Err(invalid());
    }
    if row["ExportAck"] == true {
        return Ok(());
    }
    row["ExportAck"] = json!(true);
    tx.bucket_mut(b"teammate_outbox")
        .ok_or_else(invalid)?
        .put(key.as_bytes(), go_json(&row)?)
        .map_err(io::Error::other)?;
    tx.commit().map_err(io::Error::other)
}

pub(super) fn accept(
    place: &Place,
    cfg: &Config,
    lease: &mut ConnectorLease,
    candidate: &Candidate,
    request: &Value,
) -> io::Result<Value> {
    let call = &request["call"];
    let args = &call["arguments"];
    let logical = field(args, "output_id");
    let text = field(args, "text");
    if request["version"] != 1
        || request.as_object().is_none_or(|map| map.len() != 4)
        || call.as_object().is_none_or(|map| map.len() != 3)
        || call["name"] != "publish"
        || args.as_object().is_none_or(|map| map.len() != 4)
        || args["sequence"] != 1
        || args["final"] != true
        || text.trim() != text
        || text.is_empty()
        || text.encode_utf16().count() > 1800
        || text.contains('\0')
        || logical.is_empty()
        || logical.len() > 64
        || logical.starts_with("publication:")
        || !logical.bytes().all(|b| (33..=126).contains(&b))
    {
        return Err(invalid());
    }
    let (directory, id, endpoint) = voice_location(place, cfg, &candidate.binding)?;
    open_owner_file(&directory.join("voice.initialized"))?;
    let path = directory.join("voice.db");
    let (pin, mut voice) = bounded_writer(writable_pin(&path)?)?;
    let tx = voice.begin_rw().map_err(io::Error::other)?;
    let receipt = inspect_voice(
        &tx,
        &id,
        &candidate.origin,
        cfg.text("manager_cli"),
        &endpoint,
    )?;
    if request["conversation_id"] != receipt.binding["conversation_id"]
        || request["session_id"]
            != field(&receipt.binding, "session_id")
                .rsplit('/')
                .next()
                .unwrap_or("")
        || receipt.admission["source_cutover"] != true
        || !identity(&pin, &path)?
        || !lease.source_unchanged()?
    {
        return Err(invalid());
    }
    let turn = native_proof(place, cfg, &receipt, args, &call["meta"])?;
    let mut output = stage_records(tx, receipt, &turn, logical, text, &candidate.origin)?;
    queue(cfg, lease, candidate, &output)?;
    if output["exported"] != true {
        let tx = voice.begin_rw().map_err(io::Error::other)?;
        let receipt = inspect_voice(
            &tx,
            &id,
            &candidate.origin,
            cfg.text("manager_cli"),
            &endpoint,
        )?;
        let publication = key(
            "publications",
            field(&receipt.binding, "conversation_id"),
            field(&output, "id"),
        )?;
        output["exported"] = json!(true);
        apply_voice(tx, receipt.header, &[(publication, output.clone())])?;
    }
    acknowledge_export(cfg, lease, candidate, &output)?;
    Ok(output)
}
