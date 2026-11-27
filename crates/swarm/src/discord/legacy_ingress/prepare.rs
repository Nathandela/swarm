//! Prepare one plain owner DM in the original Go voice controller.
use super::*;
use std::io::Read;

pub(in crate::discord) fn go_bytes(value: &Value) -> io::Result<Vec<u8>> {
    Ok(crate::ipc::escape_json(serde_json::to_string(value)?).into_bytes())
}

pub(in crate::discord) const DISCUSSION_INSTRUCTIONS: &str = "Answer simple questions directly. Before longer work, acknowledge a new request once with final=false. Keep replies brief. Use swarm_discussion_yield when shared-thread participants are talking to each other. For a direct reply or clarification in a shared thread, specify recipient_id from the verified Discord actor. Use their message ID as recipient_message_id if needed. Use a distinct output_id and increasing sequence per post, with final=true on the last reply. Use the scoped manager tools and approved project. Verify results with swarm_read.\n";

pub(in crate::discord) fn human_instruction(
    author: &Value,
    message: &Value,
    text: &str,
) -> io::Result<String> {
    let source = json!({"Author":author,"MessageID":message,"ReplyTo":"","Text":text});
    Ok(format!(
        "Current human instruction (JSON) {}",
        crate::ipc::escape_json(serde_json::to_string(&source)?)
    ))
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

pub(in crate::discord) struct Voice {
    header: Value,
    conversation_key: Vec<u8>,
    pub(in crate::discord) conversation: Value,
    raw_conversation: Vec<u8>,
    admissions: u64,
    queued: Option<Value>,
}

impl Voice {
    pub(in crate::discord) fn generation(&self) -> io::Result<u64> {
        self.header["generation"]
            .as_u64()
            .filter(|value| *value > 0)
            .ok_or_else(invalid)
    }

    pub(in crate::discord) fn guild_publication_header(&self) -> io::Result<&Value> {
        let nonzero = |v: &Value| v.as_u64().is_some_and(|n| n > 0);
        let text = |v: &Value| v.as_str().is_some_and(|s| !s.is_empty());
        let allowed = |row: &Value, fields: &[&str]| {
            row.as_object()
                .is_some_and(|m| m.keys().all(|k| fields.contains(&k.as_str())))
        };
        let c = &self.conversation;
        let b = &c["binding"];
        // These are the Go v0.29.26 rows we copy. Unused optional fields are omitted by Go.
        const BINDING: [&str; 9] = [
            "conversation_id",
            "provider",
            "session_id",
            "session_instance",
            "native_session_id",
            "native_generation",
            "observation_generation",
            "login_epoch",
            "generation",
        ];
        if !allowed(b, &BINDING)
            || !BINDING[..8].iter().all(|name| text(&b[name]))
            || b["generation"].as_u64().is_none()
            || !c.as_object().is_some_and(|m| {
                m.iter().all(|(k, v)| match k.as_str() {
                    "binding" => true,
                    "manager_generation" | "last_speech_epoch" | "last_output" => nonzero(v),
                    "last_admission" | "retired_through" => v.as_u64().is_some(),
                    "project" => text(v),
                    "external_observed" => v == true,
                    _ => false,
                })
            })
            || !allowed(
                &self.header,
                &[
                    "version",
                    "generation",
                    "current_conversation",
                    "logical_bytes",
                    "records",
                    "record_hash",
                ],
            )
            || !nonzero(&self.header["generation"])
            || !self.header["logical_bytes"]
                .as_u64()
                .is_some_and(|n| n > 0 && n <= 64 << 20)
        {
            return Err(io::Error::other(
                "unqualified original guild Voice header or conversation",
            ));
        }
        Ok(&self.header)
    }

    pub(super) fn executing_guild_receipt(self) -> io::Result<Receipt> {
        self.guild_publication_header()?;
        let admission = self.queued.ok_or_else(invalid)?;
        let binding = self.conversation["binding"].clone();
        if admission["phase"] != "executing" || admission["source_cutover"] != true {
            return Err(invalid());
        }
        let raw = go_bytes(&admission)?;
        Ok(Receipt {
            header: self.header,
            key: admission_key(
                field(&binding, "conversation_id"),
                admission["sequence"].as_u64().ok_or_else(invalid)?,
            )?,
            binding,
            conversation: self.conversation,
            admission,
            raw,
        })
    }
}

fn settled_vm_history<'tx>(
    records: &impl BucketApi<'tx>,
    row_key: &[u8],
    row: &Value,
    conversation: &str,
) -> io::Result<bool> {
    if row["conversation_id"] != conversation {
        return Ok(false);
    }
    if row_key.starts_with(b"workers\0") {
        let session = field(row, "session");
        let instance = field(row, "session_instance");
        let worker = serde_json::to_string(&[session, instance])?;
        return Ok(row["vm_child"] == true
            && row["mailbox"] == true
            && row["action_id"] == ""
            && !session.is_empty()
            && !instance.is_empty()
            && field(row, "attempt_id").starts_with("vm-child-")
            && wake::record(records, "workers", &[conversation, &worker])? == *row);
    }
    let saved_wake = if row_key.starts_with(b"events\0") {
        let id = field(row, "id");
        if id.is_empty()
            || !(row["worker_message"] == true
                || row["worker_proof"] == true
                || row["vm_worker_status"] == true)
            || wake::record(records, "events", &[conversation, id])? != *row
        {
            return Ok(false);
        }
        let saved = wake::record(records, "wakes", &[conversation, field(row, "wake_id")])?;
        if saved["event_id"] != id {
            return Ok(false);
        }
        saved
    } else if row_key.starts_with(b"wakes\0") {
        let id = field(row, "id");
        if id.is_empty() || wake::record(records, "wakes", &[conversation, id])? != *row {
            return Ok(false);
        }
        row.clone()
    } else {
        return Ok(false);
    };
    if saved_wake["state"] != "settled"
        || saved_wake["conversation_id"] != conversation
        || (!field(&saved_wake, "worker_reply").is_empty()
            && saved_wake["worker_reply_exported"] != true)
    {
        return Ok(false);
    }
    let sequence = saved_wake["admission"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(invalid)?;
    let settled = wake::record(
        records,
        "admissions",
        &[conversation, &sequence.to_string()],
    )?;
    let time = field(&settled, "settled_at");
    Ok(settled["conversation_id"] == conversation
        && settled["sequence"] == sequence
        && settled["source_id"] == format!("wake:{}", field(&saved_wake, "id"))
        && !time.is_empty()
        && !time.starts_with("0001-01-01"))
}

fn settled_vm_file<'tx>(
    records: &impl BucketApi<'tx>,
    publication: &Value,
    conversation: &str,
) -> io::Result<bool> {
    let source = &publication["discussion_source"];
    let wake_id = field(source, "admission_id");
    if !field(source, "attempt_id").starts_with("vm-child-") || wake_id.is_empty() {
        return Ok(false);
    }
    let saved = wake::record(records, "wakes", &[conversation, wake_id])?;
    let event = wake::record(
        records,
        "events",
        &[conversation, field(&saved, "event_id")],
    )?;
    let published: Vec<Value> = serde_json::from_str(field(publication, "discussion_files"))?;
    Ok(saved["state"] == "settled"
        && saved["admission"] == publication["admission"]
        && event["worker_message"] == true
        && event["attempt_id"] == source["attempt_id"]
        && event["job_id"] == source["job_id"]
        && !published.is_empty()
        && event["metadata"]["files"]
            .as_array()
            .is_some_and(|original| published.iter().all(|file| original.contains(file))))
}

pub(in crate::discord) fn read_voice<'tx>(
    tx: &impl TxApi<'tx>,
    id: &str,
    cfg: &Config,
    endpoint: &str,
) -> io::Result<Voice> {
    read_voice_with_queue(tx, id, cfg, endpoint, None)
}

pub(in crate::discord) fn read_voice_with_queue<'tx>(
    tx: &impl TxApi<'tx>,
    id: &str,
    cfg: &Config,
    endpoint: &str,
    origin: Option<(&Value, bool)>,
) -> io::Result<Voice> {
    let phases = origin.map(|(origin, prepared)| {
        let phases: &[&str] = if prepared { &["", "prepared"] } else { &[""] };
        (origin, phases)
    });
    read_voice_with_admission(tx, id, cfg, endpoint, phases)
}

fn read_voice_with_admission<'tx>(
    tx: &impl TxApi<'tx>,
    id: &str,
    cfg: &Config,
    endpoint: &str,
    origin: Option<(&Value, &[&str])>,
) -> io::Result<Voice> {
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let header = voice_ledger::reopen(tx)?;
    let guild = cfg.text("mode") == "teammate";
    let conversation = format!("discord-teammate-{id}");
    let media_history = retained_vm(cfg) && header["version"] == 4;
    if (header["version"] != 3 && !media_history)
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
    let mut admissions = 0;
    let mut queued = None;
    for (key, raw) in records.iter_entries() {
        if key == conversation_key
            || key.starts_with(b"journal\0")
            || key.starts_with(b"journal_snapshots\0")
        {
            continue;
        }
        let value: Value = serde_json::from_slice(raw)?;
        if guild && !canonical_guild_voice(&value, raw)? {
            return Err(invalid());
        }
        if key.starts_with(b"admissions\0")
            && let Some((origin, phases)) = origin
            && value["origin"]["request_id"] == origin["request_id"]
        {
            let sequence = value["sequence"]
                .as_u64()
                .filter(|n| *n > 0)
                .ok_or_else(invalid)?;
            if !guild
                || queued.is_some()
                || value["origin"] != *origin
                || value["conversation_id"] != conversation
                || key != admission_key(&conversation, sequence)?.as_slice()
                || !(value["phase"].is_null() && phases.contains(&"")
                    || value["phase"]
                        .as_str()
                        .is_some_and(|phase| !phase.is_empty() && phases.contains(&phase)))
                || value["settled_at"] != "0001-01-01T00:00:00Z"
            {
                return Err(invalid());
            }
            queued = Some(value);
            admissions += 1;
            continue;
        }
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
        if media_history && settled_vm_history(&records, key, &value, &conversation)? {
            continue;
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
                    && grants::settled_project_history(&value["discussion_projects"])
                    && ["discussion_attempts", "recipient_id"]
                        .iter()
                        .all(|key| value[*key].is_null() || value[*key] == "")
                    && (value["discussion_files"].is_null()
                        || value["discussion_files"] == ""
                        || media_history && settled_vm_file(&records, &value, &conversation)?)
                {
                    continue;
                }
            }
        }
        // Avoid giving a second scheduler custody of other unresolved work.
        return Err(invalid());
    }
    let raw_conversation = records.get(&conversation_key).ok_or_else(invalid)?.to_vec();
    let state: Value = serde_json::from_slice(&raw_conversation)?;
    let binding = &state["binding"];
    if guild
        && (!canonical_guild_voice(&state, &raw_conversation)?
            || !snake_keys(binding)
            || !matches!(state["external_input"], Value::Null)
                && state["external_input"].as_u64() != Some(0)
            || [
                "custody_lost",
                "scheduler_paused",
                "parked",
                "needs_handoff",
            ]
            .iter()
            .any(|key| !matches!(state[*key], Value::Null | Value::Bool(false))))
    {
        return Err(invalid());
    }
    if binding["conversation_id"] != conversation
        || !native_cli_matches(&binding["native_cli"], cfg.text("project_access") == "vm")
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
        || !(state["project"].is_null()
            || state["project"]
                .as_str()
                .is_some_and(|path| path.is_empty() || Path::new(path).is_absolute()))
    {
        return Err(invalid());
    }
    Ok(Voice {
        header,
        conversation_key,
        conversation: state,
        raw_conversation,
        admissions,
        queued,
    })
}

pub(in crate::discord) fn read_executing_guild<'tx>(
    tx: &impl TxApi<'tx>,
    id: &str,
    cfg: &Config,
    endpoint: &str,
    origin: &Value,
) -> io::Result<Voice> {
    read_voice_with_admission(tx, id, cfg, endpoint, Some((origin, &["executing"])))
}

pub(in crate::discord) fn read_guild_publication<'tx>(
    tx: &impl TxApi<'tx>,
    id: &str,
    cfg: &Config,
    endpoint: &str,
    origin: &Value,
) -> io::Result<Voice> {
    let voice = read_executing_guild(tx, id, cfg, endpoint, origin)?;
    let header = voice.guild_publication_header()?;
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    let conversation = field(header, "current_conversation");
    let admission = voice.queued.as_ref().ok_or_else(invalid)?;
    let key = admission_key(
        conversation,
        admission["sequence"].as_u64().ok_or_else(invalid)?,
    )?;
    let error = || io::Error::other("the original guild Voice records cannot reopen in Go");
    let mut journal = false;
    let mut snapshot = false;
    for (record_key, _) in records.iter_entries() {
        let (table, suffix) = std::str::from_utf8(record_key)
            .map_err(io::Error::other)?
            .split_once('\0')
            .ok_or_else(error)?;
        match table {
            "conversations" if record_key == voice.conversation_key => (),
            "admissions" if record_key == key => (),
            "journal" if suffix == conversation => journal = true,
            "journal_snapshots" if suffix == conversation => snapshot = true,
            _ => return Err(error()),
        }
    }
    if !journal || !snapshot {
        return Err(error());
    }
    Ok(voice)
}

pub(in crate::discord) fn executing_guild_admission(
    voice: &mut Voice,
    cfg: &Config,
    origin: &Value,
    human: &str,
    prompt: &str,
    project: &str,
    settings: &str,
) -> io::Result<Value> {
    let running = voice.queued.as_mut().ok_or_else(invalid)?;
    if running["phase"] != "executing" || running["source_cutover"] != true {
        return Err(invalid());
    }
    let original = running.clone();
    // Go changes only these fields when it commits an already-prepared native request.
    running["phase"] = json!("prepared");
    running
        .as_object_mut()
        .ok_or_else(invalid)?
        .remove("source_cutover");
    let valid = guild_native_ready(voice, cfg, origin, human, prompt, project, settings);
    voice.queued = Some(original.clone());
    if !valid? {
        return Err(invalid());
    }
    Ok(original)
}

fn admission_key(conversation: &str, sequence: u64) -> io::Result<Vec<u8>> {
    let id = crate::ipc::escape_json(serde_json::to_string(&[
        conversation,
        &sequence.to_string(),
    ])?);
    Ok(format!("admissions\0{id}").into_bytes())
}

fn guild_admission(
    voice: &Voice,
    origin: &Value,
    text: &str,
    prompt: &str,
    project: &str,
    sequence: u64,
) -> Value {
    let conversation = field(&voice.conversation["binding"], "conversation_id");
    let mut expected = json!({"reply_tracked":true,"human_reply":text,"origin":origin,"project":project,
        "input_text":prompt,"conversation_id":conversation,"sequence":sequence,
        "source_id":format!("discussion:{}",field(origin,"request_id")),"settled_at":"0001-01-01T00:00:00Z"});
    if let Some(after) = voice.conversation["last_output"]
        .as_u64()
        .filter(|v| *v > 0)
    {
        expected["question_boundary"] = json!(after);
    }
    expected
}

pub(in crate::discord) fn queued_guild_admission(
    voice: &mut Voice,
    origin: &Value,
    text: &str,
    prompt: &str,
    project: &str,
) -> io::Result<Option<(Vec<u8>, Value)>> {
    let last = voice.conversation["last_admission"]
        .as_u64()
        .ok_or_else(invalid)?;
    let sequence = if voice.queued.is_some() {
        last
    } else {
        last.checked_add(1).ok_or_else(invalid)?
    };
    let conversation = field(&voice.conversation["binding"], "conversation_id");
    let key = admission_key(conversation, sequence)?;
    let expected = guild_admission(voice, origin, text, prompt, project, sequence);
    if let Some(saved) = &voice.queued {
        if *saved != expected {
            return Err(invalid());
        }
        return Ok(None);
    }
    voice.conversation["last_admission"] = json!(sequence);
    Ok(Some((key, expected)))
}

fn dispatch_text(settings: &str, prompt: &str, project: &str) -> io::Result<String> {
    let context = if project.is_empty() {
        "Workspace unresolved. Ask the discussion to choose an enrolled project before your first project-dependent action.".to_owned()
    } else {
        let selected = String::from_utf8(go_bytes(&json!(project))?).map_err(io::Error::other)?;
        format!("Approved project for new work {selected}")
    };
    crate::manager_prompt::render(settings, &format!("{context}\n{prompt}"))
}

fn semantic_ids() -> io::Result<(u64, String, String)> {
    let mut nonce = [0; 32];
    let mut operation = [0; 16];
    let mut random = fs::File::open("/dev/urandom")?;
    random.read_exact(&mut nonce)?;
    random.read_exact(&mut operation)?;
    let hex = |slice: &[u8]| slice.iter().map(|b| format!("{b:02x}")).collect::<String>();
    let now = crate::ipc::spawn::now_ms();
    Ok((
        now,
        format!("semantic:{now}:{}", hex(&operation)),
        hex(&nonce),
    ))
}

fn guild_request(voice: &Voice, text: &str, origin: &Value, nonce: &str) -> Value {
    let b = &voice.conversation["binding"];
    let mut wire_origin = json!({});
    for key in [
        "kind",
        "installation_id",
        "request_id",
        "actor_id",
        "requester_id",
        "actor_name",
        "guild_id",
        "channel_id",
        "message_id",
        "discussion_id",
        "revision",
    ] {
        wire_origin[key] = origin[key].clone();
    }
    let binding = json!({"session":b["session_id"],"session_instance":b["session_instance"],
        "conversation_id":b["conversation_id"],"provider":b["provider"],"native_session_id":b["native_session_id"],
        "native_generation":b["native_generation"],"observation_generation":b["observation_generation"],"login_epoch":b["login_epoch"]});
    json!({"manager_admission":{"expected_external_input":0,"binding":binding,"nonce":nonce},
        "session":b["session_id"],"session_instance":b["session_instance"],"text":text,"origin":wire_origin})
}

pub(in crate::discord) fn guild_native_ready(
    voice: &Voice,
    cfg: &Config,
    origin: &Value,
    human: &str,
    prompt: &str,
    project: &str,
    settings: &str,
) -> io::Result<bool> {
    let queued = voice.queued.as_ref().ok_or_else(invalid)?;
    let last = voice.conversation["last_admission"]
        .as_u64()
        .filter(|seq| *seq > 0)
        .ok_or_else(invalid)?;
    let retired = voice.conversation["retired_through"]
        .as_u64()
        .ok_or_else(invalid)?;
    let last_output = &voice.conversation["last_output"];
    if last <= retired
        || !(last_output.is_null() || last_output.as_u64().is_some())
        || cfg.text("manager_cli") != "codex"
        || !grants::project(cfg, project)
    {
        return Err(invalid());
    }
    let text = dispatch_text(settings, prompt, project)?;
    let expected = guild_admission(voice, origin, human, prompt, project, last);
    if *queued == expected {
        return Ok(false);
    }
    let mut base = queued.clone();
    let fields = base.as_object_mut().ok_or_else(invalid)?;
    for key in ["phase", "request", "operation_id", "native_input_id"] {
        fields.remove(key);
    }
    let nonce = field(queued, "native_input_id");
    if base != expected
        || queued["phase"] != "prepared"
        || nonce.len() != 64
        || !nonce
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        || go_bytes(&queued["request"])? != go_bytes(&guild_request(voice, &text, origin, nonce))?
    {
        return Err(invalid());
    }
    validate_prepared_state(
        queued,
        &voice.conversation,
        &voice.conversation["binding"],
        cfg,
    )?;
    Ok(true)
}

pub(in crate::discord) fn stage_guild_native<'tx>(
    tx: &mut impl TxRwApi<'tx>,
    mut voice: Voice,
    cfg: &Config,
    origin: &Value,
    prompt: &str,
    project: &str,
    settings: &str,
) -> io::Result<()> {
    let mut queued = voice.queued.take().ok_or_else(invalid)?;
    if !queued["phase"].is_null() {
        return Err(invalid());
    }
    let sequence = queued["sequence"].as_u64().ok_or_else(invalid)?;
    let key = admission_key(field(&queued, "conversation_id"), sequence)?;
    let (_, operation, nonce) = semantic_ids()?;
    queued["request"] = guild_request(
        &voice,
        &dispatch_text(settings, prompt, project)?,
        origin,
        &nonce,
    );
    queued["operation_id"] = json!(operation);
    queued["native_input_id"] = json!(nonce);
    queued["phase"] = json!("prepared");
    validate_prepared_state(
        &queued,
        &voice.conversation,
        &voice.conversation["binding"],
        cfg,
    )?;
    manager_publication::stage_voice(tx, voice.header, &[(key, queued)])
}

fn canonical_guild_voice(value: &Value, raw: &[u8]) -> io::Result<bool> {
    Ok((!value.is_object() || snake_keys(value)) && go_bytes(value)? == raw)
}

fn snake_keys(value: &Value) -> bool {
    value.as_object().is_some_and(|object| {
        object.keys().all(|key| {
            key.bytes()
                .all(|byte| byte.is_ascii_lowercase() || byte == b'_')
        })
    })
}

fn make_admission(
    voice: &mut Voice,
    origin: &Value,
    author: &Value,
    text: &str,
    settings: &str,
    projects: &std::collections::BTreeMap<String, String>,
) -> io::Result<(Vec<u8>, Value)> {
    let (project, explicit) = grants::choose(projects, field(&voice.conversation, "project"), text);
    let mut prompt = format!(
        "{DISCUSSION_INSTRUCTIONS}{}",
        human_instruction(author, &origin["message_id"], text)?
    );
    if projects.len() > 1 {
        let names = projects.keys().cloned().collect::<Vec<_>>().join(", ");
        if names.len() <= 512 {
            prompt = format!(
                "Enrolled project choices {names}. If unresolved and work requires one, ask using these names as choices.\n{prompt}"
            );
        }
    }
    let native_text = dispatch_text(settings, &prompt, &project)?;
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
    let (now, operation, nonce) = semantic_ids()?;
    let conversation = field(&binding, "conversation_id");
    let key = admission_key(conversation, n)?;
    let mut admission = json!({
        "origin":origin, "reply_tracked":true, "reply_check_after":now / 1000 + 30,
        "human_reply":text,
        "phase":"prepared", "source_id":format!("discussion:{}",field(origin,"request_id")),
        "conversation_id":conversation,"sequence":n,"input_text":prompt,"operation_id":operation,
        "native_input_id":nonce,"settled_at":"0001-01-01T00:00:00Z",
        "request":{"session":session, "session_instance":binding["session_instance"], "text":native_text,
            "origin":origin, "manager_admission":{"binding":binding,"nonce":nonce,"expected_external_input":0}}
    });
    if !project.is_empty() {
        admission["project"] = json!(project);
    }
    if explicit {
        admission["project_explicit"] = json!(true);
    }
    if let Some(after) = voice.conversation["last_output"]
        .as_u64()
        .filter(|v| *v > 0)
    {
        admission["question_boundary"] = json!(after);
    }
    Ok((key, admission))
}

pub(in crate::discord) fn stage<'tx>(
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
    let writable_limit = voice_ledger::writable_size_limit(voice.generation()?)?;
    let bytes = voice.header["logical_bytes"]
        .as_u64()
        .ok_or_else(invalid)?
        .checked_sub(voice.raw_conversation.len() as u64)
        .and_then(|v| v.checked_add((conversation.len() + quoted.len() + 1 + raw.len()) as u64))
        .and_then(|v| v.checked_add(u64::from(voice.admissions > 0)))
        .filter(|v| *v <= writable_limit)
        .ok_or_else(invalid)?;
    voice.header["logical_bytes"] = json!(bytes);
    voice.header["records"] = json!(
        voice.header["records"]
            .as_u64()
            .and_then(|records| records.checked_add(1))
            .filter(|records| *records <= voice_ledger::MAX_RECORDS)
            .ok_or_else(invalid)?
    );
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

#[cfg(test)]
pub(in crate::discord) fn guild_storage_witness(
    place: &Place,
    cfg: &Config,
    saved: &Value,
    origin: &Value,
    exit_before_commit: bool,
) -> io::Result<()> {
    let unqualified = || io::Error::other("unqualified fixture Voice copy");
    let root = place.supervisor_root();
    let fixture = root.parent().ok_or_else(unqualified)?;
    if root != fixture.join("state")
        || fixture.parent() != Some(fs::canonicalize("/tmp")?.as_path())
        || !fixture
            .file_name()
            .and_then(|name| name.to_str())
            .is_some_and(|name| name.starts_with("gp-"))
        || fs::canonicalize(fixture)? != fixture
        || place.dir != fixture.join("connector")
        || cfg.text("installation_id") != "fixture-teammate"
    {
        return Err(unqualified());
    }
    let copy_dir = fixture.join("voice-copy");
    let directory_meta = fs::symlink_metadata(&copy_dir)?;
    if !directory_meta.is_dir()
        || directory_meta.mode() & 0o777 != 0o700
        || directory_meta.uid() != unsafe { libc::geteuid() }
        || fs::canonicalize(&copy_dir)? != copy_dir
    {
        return Err(unqualified());
    }
    let copy = copy_dir.join("voice.db");
    let (directory, id, endpoint) = voice_location(place, cfg, saved)?;
    let (pin, _db, oracle) = controller(&directory, &id, origin, cfg, &endpoint)?;
    validate_prepared(&oracle, cfg)?;
    activation::verify_idle_manager(place, cfg, &directory, &id, &oracle.binding)?;
    let original = pin.metadata()?;
    let candidate = fs::symlink_metadata(&copy)?;
    if !candidate.is_file()
        || (original.dev(), original.ino()) == (candidate.dev(), candidate.ino())
    {
        return Err(unqualified());
    }
    let file = voice_ledger::pin_write(&copy)?;
    let opened = file.metadata()?;
    if (opened.dev(), opened.ino()) != (candidate.dev(), candidate.ino())
        || fs::canonicalize(&copy_dir)? != copy_dir
    {
        return Err(unqualified());
    }
    let (_copy_pin, mut db) = bounded_writer(file)?;
    let mut tx = db.begin_rw().map_err(io::Error::other)?;
    let mut initial = read_voice(&tx, &id, cfg, &endpoint)?;
    if initial.conversation["binding"] != oracle.binding {
        return Err(invalid());
    }
    let author = json!({"id":origin["actor_id"],"name":origin["actor_name"]});
    let prompt = crate::manager_prompt::load(place.supervisor_root())?;
    let (key, direct) = make_admission(
        &mut initial,
        origin,
        &author,
        field(&oracle.admission, "human_reply"),
        &prompt,
        &grants::enrolled(cfg)?,
    )?;
    assert_eq!(key, oracle.key);
    assert_ne!(
        direct["request"]["text"],
        oracle.admission["request"]["text"]
    );
    assert!(direct["reply_check_after"].as_u64().is_some());
    assert!(oracle.admission["reply_check_after"].is_null());
    assert_eq!(
        initial.conversation["last_admission"],
        oracle.conversation["last_admission"]
    );
    stage(&mut tx, initial, &key, &oracle.admission)?;
    if exit_before_commit {
        std::process::exit(76);
    }
    tx.commit().map_err(io::Error::other)?;
    let readback = inspect_voice(
        &db.begin().map_err(io::Error::other)?,
        &id,
        origin,
        cfg,
        &endpoint,
    )?;
    validate_prepared(&readback, cfg)?;
    assert_eq!(readback.admission, oracle.admission);
    println!(
        "Rust Voice storage preserved the Go tuple in a disposable copy. The personal renderer differed from Go's guild input."
    );
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
    grants::enrolled(&cfg)?;
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
    if !snowflake(message) {
        return Err(invalid());
    }
    current_turn_enrollment(place, cfg)?;
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
    let (voice_pin, mut voice_db) = bounded_writer(voice_ledger::pin_write(&voice_path)?)?;
    let mut voice_tx = voice_db.begin_rw().map_err(io::Error::other)?;
    if let Ok(prior) = inspect_voice(&voice_tx, &id, &origin, cfg, &endpoint) {
        validate_prepared(&prior, cfg)?;
        if value(&tx, "teammate_ingress", &key)?.is_some_and(|saved| saved["State"] == "sending") {
            println!("The original Go voice controller already has this prepared input.");
            return Ok(true);
        }
        return Err(invalid());
    }
    let mut voice = read_voice(&voice_tx, &id, cfg, &endpoint)?;
    let settings = crate::manager_prompt::load(place.supervisor_root())?;
    let projects = grants::enrolled(cfg)?;
    let (voice_key, admission) = make_admission(
        &mut voice,
        &origin,
        &row["Author"],
        text,
        &settings,
        &projects,
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
