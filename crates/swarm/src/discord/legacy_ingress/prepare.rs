//! Prepare one plain owner DM in the original Go voice controller.
use super::*;
use std::io::Read;

fn go_bytes(value: &Value) -> io::Result<Vec<u8>> {
    Ok(crate::ipc::escape_json(serde_json::to_string(value)?).into_bytes())
}

fn source_author(row: &Value, cfg: &Config, message: &str) -> io::Result<Value> {
    let author = &row["Author"];
    if row["State"] != "prepared"
        || row["Admission"] != Value::Null
        || row["Route"]["message_id"] != message
        || row["Route"]["channel_id"] != cfg.text("dm_channel_id")
        || row["Route"]["guild_id"] != ""
        || !row["Route"]["parent_id"].is_null()
        || author["id"] != cfg.text("owner_id")
        || field(author, "name").is_empty()
        || row["Opening"] != false
        || row["Deleted"] != false
        || row["Edited"] != false
        || !row["Sequence"].as_u64().is_some_and(|n| n > 0)
    {
        return Err(invalid());
    }
    Ok(author.clone())
}

pub(super) struct Voice {
    header: Value,
    conversation_key: Vec<u8>,
    pub(super) conversation: Value,
    raw_conversation: Vec<u8>,
    admissions: u64,
}

pub(super) fn read_voice<'tx>(
    tx: &impl TxApi<'tx>,
    id: &str,
    cfg: &Config,
    endpoint: &str,
) -> io::Result<Voice> {
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let raw = root.get(b"state").ok_or_else(invalid)?;
    if raw.len() > 4096
        || root.get(b"digest") != Some(format!("{:x}", Sha256::digest(raw)).as_bytes())
    {
        return Err(invalid());
    }
    let header: Value = serde_json::from_slice(raw)?;
    let conversation = format!("discord-teammate-{id}");
    if header["version"] != 3
        || header["current_conversation"] != conversation
        || header["reply_wait_consumed"] == true
    {
        return Err(invalid());
    }
    let conversation_key = format!("conversations\0{conversation}").into_bytes();
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    if records.iter_buckets().next().is_some() {
        return Err(invalid());
    }
    let mut count = 0;
    let mut admissions = 0;
    let mut sum = [0u8; 32];
    for (key, raw) in records.iter_entries() {
        count += 1;
        if count >= 4096 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        for (byte, item) in sum.iter_mut().zip(record_hash(key, raw)) {
            *byte ^= item;
        }
        if key == conversation_key
            || key.starts_with(b"journal\0")
            || key.starts_with(b"journal_snapshots\0")
        {
            continue;
        }
        let value: Value = serde_json::from_slice(raw)?;
        if key.starts_with(b"admissions\0")
            && value["conversation_id"] == conversation
            && value["settled_at"]
                .as_str()
                .is_some_and(|s| !s.starts_with("0001-01-01"))
            && value["reply_delivery_unknown"] != true
            && value["reply_check_after"].as_i64().unwrap_or_default() == 0
        {
            admissions += 1;
            continue;
        }
        if key.starts_with(b"closed_turns\0") && value == true {
            let id: [String; 2] = serde_json::from_slice(&key[b"closed_turns\0".len()..])?;
            if id[0] == conversation && !id[1].is_empty() {
                continue;
            }
        }
        let turn = field(&value, "native_turn_id");
        if value["conversation_id"] == conversation && !turn.is_empty() {
            let turn_key = serde_json::to_string(&[conversation.as_str(), turn])?;
            let closed = format!("closed_turns\0{turn_key}");
            if records.get(closed.as_bytes()) == Some(b"true".as_slice()) {
                if key.starts_with(b"permits\0") && value["revoked"] == true {
                    continue;
                }
                if key.starts_with(b"publications\0")
                    && value["exported"] == true
                    && value["final"] == true
                    && !field(&value, "id").is_empty()
                    && grants::publication_projects(cfg, &value["discussion_projects"]).is_ok()
                    && ["discussion_attempts", "discussion_files", "recipient_id"]
                        .iter()
                        .all(|key| value[*key].is_null() || value[*key] == "")
                {
                    continue;
                }
            }
        }
        // Avoid giving a second scheduler custody of other unresolved work.
        return Err(invalid());
    }
    if header["records"] != count || header["record_hash"] != json!(sum) {
        return Err(invalid());
    }
    let raw_conversation = records.get(&conversation_key).ok_or_else(invalid)?.to_vec();
    let state: Value = serde_json::from_slice(&raw_conversation)?;
    let binding = &state["binding"];
    if binding["conversation_id"] != conversation
        || !matches!(binding["native_cli"], Value::Null | Value::Bool(false))
        || binding["provider"] != cfg.text("manager_cli")
        || !field(binding, "session_id").starts_with(&format!("{endpoint}/"))
        || [
            "session_instance",
            "native_session_id",
            "native_generation",
            "observation_generation",
            "login_epoch",
        ]
        .iter()
        .any(|key| field(binding, key).is_empty())
        || state["external_input"].as_u64().unwrap_or_default() != 0
        || state["external_observed"] != true
        || state["custody_lost"] == true
        || state["scheduler_paused"] == true
        || state["parked"] == true
        || state["needs_handoff"] == true
        || !state["retirement"].is_null()
        || (!field(&state, "project").is_empty() && !grants::project(cfg, field(&state, "project")))
    {
        return Err(invalid());
    }
    Ok(Voice {
        header,
        conversation_key,
        conversation: state,
        raw_conversation,
        admissions,
    })
}

fn make_admission(
    voice: &mut Voice,
    origin: &Value,
    author: &Value,
    text: &str,
    settings: &str,
    project: &str,
) -> io::Result<(Vec<u8>, Value)> {
    let raw_text = String::from_utf8(go_bytes(
        &json!({"Author": author, "MessageID":origin["message_id"], "ReplyTo":"", "Text":text}),
    )?)
    .map_err(io::Error::other)?;
    let instructions = concat!(
        "Answer simple questions directly. Before longer work, acknowledge a new request once with final=false. Keep replies brief. Use swarm_discussion_yield when shared-thread participants are talking to each other. For a direct reply or clarification in a shared thread, specify recipient_id from the verified Discord actor. Use their message ID as recipient_message_id if needed. Use a distinct output_id and increasing sequence per post, with final=true on the last reply. Use the scoped manager tools and approved project. Verify results with swarm_read.\n",
        "Current human instruction (JSON) "
    );
    let prompt = format!("{instructions}{raw_text}");
    let selected = String::from_utf8(go_bytes(&json!(project))?).map_err(io::Error::other)?;
    let native_text = crate::manager_prompt::render(
        settings,
        &format!("Approved project for new work {selected}\n{prompt}"),
    )?;
    let n = voice.conversation["last_admission"]
        .as_u64()
        .and_then(|n| n.checked_add(1))
        .ok_or_else(invalid)?;
    voice.conversation["last_admission"] = json!(n);
    let mut binding = voice.conversation["binding"].clone();
    let fields = binding.as_object_mut().ok_or_else(invalid)?;
    let session = fields.remove("session_id").ok_or_else(invalid)?;
    fields.insert("session".into(), session.clone());
    fields.remove("generation");
    fields.remove("native_cli");
    let mut nonce = [0; 32];
    let mut operation = [0; 16];
    let mut random = fs::File::open("/dev/urandom")?;
    random.read_exact(&mut nonce)?;
    random.read_exact(&mut operation)?;
    let hex = |slice: &[u8]| slice.iter().map(|b| format!("{b:02x}")).collect::<String>();
    let nonce = hex(&nonce);
    let now = crate::ipc::spawn::now_ms();
    let operation = format!("semantic:{now}:{}", hex(&operation));
    let conversation = field(&binding, "conversation_id");
    let id = crate::ipc::escape_json(serde_json::to_string(&[conversation, &n.to_string()])?);
    let key = format!("admissions\0{id}").into_bytes();
    let mut admission = json!({
        "origin":origin, "reply_tracked":true, "reply_check_after":now / 1000 + 30,
        "human_reply":text, "project":project,
        "phase":"prepared", "source_id":format!("discussion:{}",field(origin,"request_id")),
        "conversation_id":conversation,"sequence":n,"input_text":prompt,"operation_id":operation,
        "native_input_id":nonce,"settled_at":"0001-01-01T00:00:00Z",
        "request":{"session":session, "session_instance":binding["session_instance"], "text":native_text,
            "origin":origin, "manager_admission":{"binding":binding,"nonce":nonce,"expected_external_input":0}}
    });
    if let Some(after) = voice.conversation["last_output"]
        .as_u64()
        .filter(|v| *v > 0)
    {
        admission["question_boundary"] = json!(after);
    }
    Ok((key, admission))
}

fn stage<'tx>(
    tx: &mut impl TxRwApi<'tx>,
    mut voice: Voice,
    key: &[u8],
    admission: &Value,
) -> io::Result<()> {
    let raw = go_bytes(admission)?;
    let conversation = go_bytes(&voice.conversation)?;
    let mut sum: [u8; 32] = serde_json::from_value(voice.header["record_hash"].clone())?;
    for hash in [
        record_hash(key, &raw),
        record_hash(&voice.conversation_key, &voice.raw_conversation),
        record_hash(&voice.conversation_key, &conversation),
    ] {
        for (byte, item) in sum.iter_mut().zip(hash) {
            *byte ^= item;
        }
    }
    let record = key.strip_prefix(b"admissions\0").ok_or_else(invalid)?;
    let quoted = go_bytes(&json!(
        std::str::from_utf8(record).map_err(io::Error::other)?
    ))?;
    let bytes = voice.header["logical_bytes"]
        .as_u64()
        .ok_or_else(invalid)?
        .checked_sub(voice.raw_conversation.len() as u64)
        .and_then(|v| v.checked_add((conversation.len() + quoted.len() + 1 + raw.len()) as u64))
        .and_then(|v| v.checked_add(u64::from(voice.admissions > 0)))
        .filter(|v| *v <= 64 << 20)
        .ok_or_else(invalid)?;
    voice.header["logical_bytes"] = json!(bytes);
    voice.header["records"] = json!(voice.header["records"].as_u64().ok_or_else(invalid)? + 1);
    voice.header["record_hash"] = json!(sum);
    let header = go_bytes(&voice.header)?;
    {
        let mut root = tx.bucket_mut(b"controller").ok_or_else(invalid)?;
        {
            let mut records = root.bucket_mut(b"records").ok_or_else(invalid)?;
            if records.get(key).is_some() {
                return Err(invalid());
            }
            records
                .put(&voice.conversation_key, conversation)
                .map_err(io::Error::other)?;
            records.put(key, raw).map_err(io::Error::other)?;
        }
        root.put(b"state", &header).map_err(io::Error::other)?;
        root.put(b"digest", format!("{:x}", Sha256::digest(&header)))
            .map_err(io::Error::other)?;
    }
    Ok(())
}

pub(in crate::discord) fn run(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    with(place, message, dry_run, capture_source)
}

fn prepared_config(place: &Place, message: &str) -> io::Result<Config> {
    if !snowflake(message) {
        return Err(invalid());
    }
    let cfg = personal_enrollment(place)?;
    grants::single_project(&cfg)?;
    Ok(cfg)
}

fn incoming<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    message: &str,
    input: &Value,
) -> io::Result<(Vec<u8>, Value, Value, Candidate)> {
    let install = cfg.text("installation_id");
    let channel = cfg.text("dm_channel_id");
    let actor = cfg.text("owner_id");
    let key = format!("{install}\0{message}").into_bytes();
    let binding_key = format!("{install}\0{channel}").into_bytes();
    let mut row = value(tx, "teammate_ingress", &key)?.ok_or_else(invalid)?;
    if row["State"] == "sending" {
        let c = candidate(tx, cfg, message)?.ok_or_else(invalid)?;
        if original_revision(input, &c.origin)? != c.origin["revision"] {
            return Err(invalid());
        }
        return Ok((key, row, c.origin.clone(), c));
    }
    let author = source_author(&row, cfg, message)?;
    let binding = value(tx, "teammate_discussions", &binding_key)?.ok_or_else(invalid)?;
    require_repaired(tx, cfg, &binding)?;
    let id = field(&binding, "ID");
    if id.is_empty()
        || binding["InstallationID"] != install
        || binding["Route"]["guild_id"] != ""
        || binding["Route"]["channel_id"] != channel
        || !binding["Route"]["parent_id"].is_null()
        || binding["Opened"] != true
        || binding["ProvisionalCancelled"] != false
        || binding["StartupNoticePending"] != false
        || (!binding["supervisor_socket"].is_null()
            && binding["supervisor_socket"] != cfg.text("daemon_socket"))
        || input["author"]["username"] != author["name"]
    {
        return Err(invalid());
    }
    let mut origin = json!({"kind":"owner_direct","installation_id":install,
        "request_id":operation(install,message)?,"actor_id":actor,"requester_id":actor,
        "actor_name":author["name"],"channel_id":channel,"message_id":message,"discussion_id":id});
    origin["revision"] = json!(original_revision(input, &origin)?);
    let mut discussion = json!({"ID":id,"InstallationID":install,
        "Route":binding["Route"],"Starter":binding["Starter"],"ParentAnchor":binding["ParentAnchor"],"Gap":binding["Gap"]});
    if binding["supervisor_socket"].is_string() {
        discussion["supervisor_socket"] = binding["supervisor_socket"].clone();
    }
    row["State"] = json!("sending");
    row["Admission"] = json!({"Discussion":discussion,"OperationID":origin["request_id"],
        "Revision":origin["revision"],"Author":author,"Route":row["Route"],"Authority":"owner_direct",
        "InvokerID":actor,"Sequence":row["Sequence"],"RequireMention":false,"QuestionID":""});
    let c = Candidate {
        key: key.clone(),
        active_key: binding_key,
        pending_key: vec![],
        binding,
        guard: Value::Null,
        origin: origin.clone(),
        pending_count: 0,
    };
    Ok((key, row, origin, c))
}

pub(super) fn with(
    place: &Place,
    message: &str,
    dry_run: bool,
    fetch: impl FnOnce(&Config, &str) -> io::Result<Value>,
) -> io::Result<bool> {
    let cfg = prepared_config(place, message)?;
    if retained_config(place)?.0 != cfg.0 {
        return Err(invalid());
    }
    let source = place.dir.join("discord.db");
    if dry_run {
        let (_pin, db) = open_bolt(&source, 64 << 20)?;
        let tx = db.begin().map_err(io::Error::other)?;
        let key = format!("{}\0{message}", cfg.text("installation_id"));
        let row = value(&tx, "teammate_ingress", key.as_bytes())?.ok_or_else(invalid)?;
        if row["State"] == "sending" {
            candidate(&tx, &cfg, message)?.ok_or_else(invalid)?;
        } else {
            source_author(&row, &cfg, message)?;
        }
        println!("The original Go queue has this personal DM.");
        return Ok(true);
    }
    let expected = fs::symlink_metadata(&source)?;
    let mut lease = ConnectorLease::open(place, &cfg, &expected)?;
    under_lease(place, &cfg, message, &mut lease, fetch)
}

pub(super) fn no_pending_output<'tx>(tx: &impl TxApi<'tx>, cfg: &Config) -> io::Result<()> {
    let outbox = tx.bucket(b"teammate_outbox").ok_or_else(invalid)?;
    let prefix = format!("{}\0", cfg.text("installation_id"));
    for (count, (key, raw)) in outbox.iter_entries().enumerate() {
        if count >= 2048 || !key.starts_with(prefix.as_bytes()) || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        let row: Value = serde_json::from_slice(raw)?;
        if row["Route"]["channel_id"] != cfg.text("dm_channel_id")
            || row["Route"]["guild_id"] != ""
            || row["ExportAck"] != true
            || !row["Chunks"].as_array().is_some_and(|parts| {
                !parts.is_empty() && parts.iter().all(|part| part["state"] == "sent")
            })
        {
            return Err(io::Error::other(
                "original Go outbox contains unconfirmed effects",
            ));
        }
    }
    Ok(())
}

pub(super) fn under_lease(
    place: &Place,
    cfg: &Config,
    message: &str,
    lease: &mut ConnectorLease,
    fetch: impl FnOnce(&Config, &str) -> io::Result<Value>,
) -> io::Result<bool> {
    if prepared_config(place, message)?.0 != cfg.0 {
        return Err(invalid());
    }
    let mut tx = lease.db.begin_rw().map_err(io::Error::other)?;
    no_pending_output(&tx, cfg)?;
    let input = fetch(cfg, message)?;
    let text = input["content"]
        .as_str()
        .filter(|s| !s.trim().is_empty() && s.len() <= 4096 && !s.contains('\0'))
        .ok_or_else(invalid)?;
    let (key, row, origin, c) = incoming(&tx, cfg, message, &input)?;
    require_repaired(&tx, cfg, &c.binding)?;
    require_after_boundary(&tx, cfg, &c.binding, message)?;
    let (directory, id, endpoint) = voice_location(place, cfg, &c.binding)?;
    open_owner_file(&directory.join("voice.initialized"))?;
    let voice_path = directory.join("voice.db");
    let (voice_pin, mut voice_db) = bounded_writer(writable_pin(&voice_path)?)?;
    let mut voice_tx = voice_db.begin_rw().map_err(io::Error::other)?;
    if let Ok(prior) = inspect_voice(&voice_tx, &id, &origin, cfg.text("manager_cli"), &endpoint) {
        validate_prepared(&prior, cfg)?;
        if value(&tx, "teammate_ingress", &key)?.is_some_and(|saved| saved["State"] == "sending") {
            println!("The original Go voice controller already has this prepared input.");
            return Ok(true);
        }
        return Err(invalid());
    }
    let mut voice = read_voice(&voice_tx, &id, cfg, &endpoint)?;
    let settings = crate::manager_prompt::load(place.supervisor_root())?;
    let project = grants::single_project(cfg)?;
    let (voice_key, admission) = make_admission(
        &mut voice,
        &origin,
        &row["Author"],
        text,
        &settings,
        &project,
    )?;
    tx.bucket_mut(b"teammate_ingress")
        .ok_or_else(invalid)?
        .put(&key, go_bytes(&row)?)
        .map_err(io::Error::other)?;
    if candidate(&tx, cfg, message)?
        .ok_or_else(invalid)?
        .pending_count
        != 1
    {
        return Err(invalid());
    }
    stage(&mut voice_tx, voice, &voice_key, &admission)?;
    if !identity(&lease.pin, &lease.source)? || !identity(&voice_pin, &voice_path)? {
        return Err(invalid());
    }
    // Connector first. If interrupted, Go sees a genuine pending input with
    // no native effect and may admit it under the original voice controller.
    tx.commit().map_err(io::Error::other)?;
    voice_tx.commit().map_err(io::Error::other)?;
    println!("Prepared the new personal DM in the original Go voice controller.");
    Ok(true)
}
