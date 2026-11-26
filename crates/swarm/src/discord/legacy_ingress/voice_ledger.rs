//! Prove that the retained v3/v4 Voice record set reopens with its exact Go size.
use super::{BucketApi, Digest, Sha256, TxApi, Value, io, json, prepare::go_bytes, record_hash};

const MAX_RECORDS: usize = 4096;
const MAX_BYTES: usize = 64 << 20;

fn invalid() -> io::Error {
    io::Error::other("the original Voice records cannot reopen in Go")
}

pub(super) fn reopen<'tx>(tx: &impl TxApi<'tx>) -> io::Result<Value> {
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let raw = root
        .get(b"state")
        .filter(|raw| raw.len() <= 4096)
        .ok_or_else(invalid)?;
    if root.get(b"digest") != Some(format!("{:x}", Sha256::digest(raw)).as_bytes()) {
        return Err(invalid());
    }
    let header: Value = serde_json::from_slice(raw)?;
    let generation = header["generation"]
        .as_u64()
        .filter(|value| *value > 0 && *value < u64::MAX)
        .ok_or_else(invalid)?;
    let conversation = header["current_conversation"]
        .as_str()
        .ok_or_else(invalid)?;
    let expected_size = header["logical_bytes"]
        .as_u64()
        .filter(|value| *value <= MAX_BYTES as u64)
        .ok_or_else(invalid)?;
    let expected_count = header["records"]
        .as_u64()
        .filter(|value| *value <= MAX_RECORDS as u64)
        .ok_or_else(invalid)?;
    let expected_hash: [u8; 32] = serde_json::from_value(header["record_hash"].clone())?;
    if !matches!(header["version"].as_u64(), Some(3 | 4))
        || header
            .as_object()
            .is_none_or(|map| map.len() != 6 + usize::from(map.contains_key("reply_wait_consumed")))
        || header
            .get("reply_wait_consumed")
            .is_some_and(|value| value != true)
        || go_bytes(&header)? != raw
    {
        return Err(invalid());
    }
    let rows = root.bucket(b"records").ok_or_else(invalid)?;
    if rows.iter_buckets().next().is_some() {
        return Err(invalid());
    }
    // Go hashes the stored bytes but measures the complete v1 State, including
    // empty maps and the escaped keys of the typed records.
    let mut state =
        json!({"version":1,"generation":generation,"current_conversation":conversation});
    for (table, _) in TABLES {
        state[*table] = json!({});
    }
    let mut size = go_bytes(&state)?.len();
    let mut checksum = [0; 32];
    let mut count = 0;
    let mut previous = "";
    for (key, raw) in rows.iter_entries() {
        let (table, index) = std::str::from_utf8(key)
            .map_err(io::Error::other)?
            .split_once('\0')
            .ok_or_else(invalid)?;
        let kind = TABLES
            .iter()
            .find(|(name, _)| *name == table)
            .map(|(_, kind)| *kind)
            .ok_or_else(invalid)?;
        count += 1;
        if count > expected_count || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        let row: Value = serde_json::from_slice(raw)?;
        if !kind.check(&row, false) || go_bytes(&row)? != raw {
            return Err(invalid());
        }
        size += go_bytes(&json!(index))?.len() + 1 + usize::from(table == previous) + raw.len();
        if size as u64 > expected_size {
            return Err(invalid());
        }
        previous = table;
        for (left, right) in checksum.iter_mut().zip(record_hash(key, raw)) {
            *left ^= right;
        }
    }
    if count != expected_count || checksum != expected_hash || size as u64 != expected_size {
        return Err(invalid());
    }
    Ok(header)
}

#[derive(Clone, Copy)]
enum Kind {
    String,
    Bool,
    Unsigned,
    Signed,
    Raw,
    Time,
    Object(&'static [Field]),
    NonzeroObject(&'static [Field]),
}

struct Field(&'static str, Kind, bool);

macro_rules! fields {
    (required { $($req:ident: $kind:expr,)* } optional { $($opt:ident: $opt_kind:expr,)* }) => {
        &[$(Field(stringify!($req), $kind, false),)* $(Field(stringify!($opt), $opt_kind, true),)*]
    };
}

impl Kind {
    fn check(self, value: &Value, optional: bool) -> bool {
        match self {
            Self::String => value
                .as_str()
                .is_some_and(|value| !optional || !value.is_empty()),
            Self::Bool => value.as_bool().is_some_and(|value| !optional || value),
            Self::Unsigned => value.as_u64().is_some_and(|value| !optional || value != 0),
            Self::Signed => value.as_i64().is_some_and(|value| !optional || value != 0),
            Self::Raw => true,
            Self::Time => value.as_str().is_some_and(go_timestamp),
            Self::Object(fields) | Self::NonzeroObject(fields) => {
                value.as_object().is_some_and(|map| {
                    (!matches!(self, Self::NonzeroObject(_)) || !map.is_empty())
                        && map
                            .keys()
                            .all(|name| fields.iter().any(|Field(field, _, _)| field == name))
                        && fields.iter().all(|Field(name, kind, optional)| {
                            map.get(*name)
                                .map_or(*optional, |value| kind.check(value, *optional))
                        })
                })
            }
        }
    }
}

fn go_timestamp(value: &str) -> bool {
    // Go emits RFC3339Nano, trimming fractional zeroes and spelling a zero
    // offset as Z. Require the original spelling before counting its bytes.
    value.parse::<jiff::Timestamp>().is_ok()
        && value.as_bytes().get(10) == Some(&b'T')
        && value.as_bytes().get(17).is_some_and(|byte| *byte <= b'5')
        && !value.ends_with("+00:00")
        && !value.ends_with("-00:00")
        && (value.ends_with('Z')
            || value
                .get(value.len().saturating_sub(3)..)
                .is_some_and(|offset| offset.starts_with(':')))
        && value.split_once('.').is_none_or(|(_, tail)| {
            tail.bytes()
                .take_while(u8::is_ascii_digit)
                .last()
                .is_some_and(|byte| byte != b'0')
        })
}

// Fields match the Go Voice State at v0.29.26. An optional time.Time remains
// present in Go JSON because omitempty does not omit a zero struct.
const WORKER_ASSOCIATION: &[Field] = fields! {
    required {
        conversation_id: Kind::String,
        session: Kind::String,
        session_instance: Kind::String,
        admission: Kind::Unsigned,
        source_id: Kind::String,
        action_id: Kind::String,
    }
    optional {
        parent_input_hash: Kind::String,
        mailbox: Kind::Bool,
        vm_child: Kind::Bool,
        evidence_limited: Kind::Bool,
        parent: Kind::String,
        ended: Kind::Bool,
        project: Kind::String,
        initial_source_id: Kind::String,
        attempt_id: Kind::String,
        job_id: Kind::String,
        recovery_required: Kind::Bool,
        reserved: Kind::Bool,
    }
};
const CONVERSATION: &[Field] = fields! {
    required {
        binding: Kind::Object(MANAGER_BINDING),
        last_admission: Kind::Unsigned,
        retired_through: Kind::Unsigned,
    }
    optional {
        replacement_after: Kind::Signed,
        failed_resume: Kind::Object(FAILED_RESUME),
        needs_handoff: Kind::Bool,
        last_output: Kind::Unsigned,
        manager_generation: Kind::Unsigned,
        project: Kind::String,
        retirement: Kind::Object(MANAGER_RETIRE_REQ),
        parked: Kind::Bool,
        custody_lost: Kind::Bool,
        last_speech_epoch: Kind::Unsigned,
        scheduler_paused: Kind::Bool,
        external_observed: Kind::Bool,
        external_input: Kind::Unsigned,
    }
};
const UTTERANCE: &[Field] = fields! {
    required {
        id: Kind::String,
        conversation_id: Kind::String,
        device_id: Kind::String,
        voice_session_id: Kind::String,
        media_epoch: Kind::Unsigned,
        phase: Kind::String,
    }
    optional {
        recovery_only: Kind::Bool,
        text: Kind::String,
        text_hash: Kind::String,
        audio_hash: Kind::String,
        admission: Kind::Unsigned,
    }
};
const ADMISSION: &[Field] = fields! {
    required {
        conversation_id: Kind::String,
        sequence: Kind::Unsigned,
        source_id: Kind::String,
        settled_at: Kind::Time,
    }
    optional {
        reply_stall_since: Kind::Signed,
        reply_receipt_checks: Kind::Signed,
        reply_delivery_unknown: Kind::Bool,
        reply_tracked: Kind::Bool,
        reply_waiting_for_human: Kind::Bool,
        reply_progress: Kind::Unsigned,
        reply_check_after: Kind::Signed,
        reply_recovery: Kind::Unsigned,
        waiting_on_input: Kind::String,
        waiting_notice: Kind::Bool,
        reused_final: Kind::Object(REUSED_FINAL_RECEIPT),
        recovery_output: Kind::Object(DISCUSSION_OUTPUT_SOURCE),
        worker_attempts: Kind::String,
        worker_projects: Kind::String,
        source_cutover: Kind::Bool,
        project_explicit: Kind::Bool,
        question_boundary: Kind::Unsigned,
        human_reply: Kind::String,
        origin: Kind::Object(MESSAGE_ORIGIN),
        project: Kind::String,
        native_binding_retired: Kind::Bool,
        recovery_only: Kind::Bool,
        recovery_acknowledged: Kind::Bool,
        operation_id: Kind::String,
        request: Kind::Raw,
        phase: Kind::String,
        native_turn_id: Kind::String,
        reason: Kind::String,
        external_input: Kind::Unsigned,
        input_text: Kind::String,
        native_input_id: Kind::String,
    }
};
const ACTION: &[Field] = fields! {
    required {
        id: Kind::String,
        conversation_id: Kind::String,
        admission: Kind::Unsigned,
        native_input_id: Kind::String,
        target: Kind::String,
        operation_id: Kind::String,
        request: Kind::Raw,
        request_hash: Kind::String,
        state: Kind::String,
        settled_at: Kind::Time,
    }
    optional {
        receipt: Kind::Raw,
    }
};
const REQUEST: &[Field] = fields! {
    required {
        id: Kind::String,
        conversation_id: Kind::String,
        kind: Kind::String,
        native_input_id: Kind::String,
        native_turn_id: Kind::String,
        task_id: Kind::String,
        payload: Kind::Raw,
        hash: Kind::String,
        expires_at: Kind::Time,
        state: Kind::String,
    }
    optional {
        recipient_id: Kind::String,
        discussion_attempts: Kind::String,
        discussion_projects: Kind::String,
        discussion_source: Kind::NonzeroObject(DISCUSSION_OUTPUT_SOURCE),
        output_sequence: Kind::Unsigned,
        exported: Kind::Bool,
        recovery_context: Kind::Bool,
        answer_id: Kind::String,
        answer: Kind::Raw,
    }
};
const SPEECH_PERMIT: &[Field] = fields! {
    required {
        conversation_id: Kind::String,
        admission: Kind::Unsigned,
        native_input_id: Kind::String,
        native_turn_id: Kind::String,
        epoch: Kind::Unsigned,
        revoked: Kind::Bool,
    }
    optional {
    }
};
const PUBLICATION: &[Field] = fields! {
    required {
        admission: Kind::Unsigned,
        speech_epoch: Kind::Unsigned,
        id: Kind::String,
        conversation_id: Kind::String,
        native_turn_id: Kind::String,
        sequence: Kind::Unsigned,
        text: Kind::String,
        text_hash: Kind::String,
        final: Kind::Bool,
        speak: Kind::Bool,
    }
    optional {
        recipient_id: Kind::String,
        discussion_files: Kind::String,
        reply_monitor: Kind::Bool,
        discussion_attempts: Kind::String,
        discussion_projects: Kind::String,
        discussion_source: Kind::NonzeroObject(DISCUSSION_OUTPUT_SOURCE),
        output_sequence: Kind::Unsigned,
        exported: Kind::Bool,
    }
};
const JOURNAL_CURSOR: &[Field] = fields! {
    required {
        binding: Kind::String,
        sequence: Kind::Unsigned,
    }
    optional {
    }
};
const JOURNAL_SNAPSHOT: &[Field] = fields! {
    required {
        previous: Kind::Object(JOURNAL_CURSOR),
        cursor: Kind::Object(JOURNAL_CURSOR),
        metadata: Kind::Raw,
    }
    optional {
    }
};
const EVENT: &[Field] = fields! {
    required {
        id: Kind::String,
        conversation_id: Kind::String,
        cursor: Kind::Object(JOURNAL_CURSOR),
        examined_through: Kind::Object(JOURNAL_CURSOR),
        metadata: Kind::Raw,
        wake_id: Kind::String,
    }
    optional {
        vm_worker_status: Kind::Bool,
        worker_message: Kind::Bool,
        worker_proof: Kind::Bool,
        native_turn_id: Kind::String,
        job_id: Kind::String,
        attempt_id: Kind::String,
        recovery_only: Kind::Bool,
    }
};
const WAKE: &[Field] = fields! {
    required {
        id: Kind::String,
        conversation_id: Kind::String,
        event_id: Kind::String,
        operation_id: Kind::String,
        request: Kind::Raw,
        request_hash: Kind::String,
        state: Kind::String,
    }
    optional {
        mailbox_files_settled: Kind::Bool,
        worker_reply_exported: Kind::Bool,
        worker_reply: Kind::String,
        recovery_only: Kind::Bool,
        admission: Kind::Unsigned,
        native_input_id: Kind::String,
    }
};
const FAILED_RESUME: &[Field] = fields! {
    required {
        attempt: Kind::Signed,
        session: Kind::String,
        instance: Kind::String,
        diagnostic: Kind::String,
    }
    optional {
    }
};
const MANAGER_RETIRE_REQ: &[Field] = fields! {
    required {
        binding: Kind::Object(MANAGER_RUNTIME_BINDING),
        generation: Kind::Unsigned,
        checkpoint: Kind::String,
        expected_turn: Kind::Object(MANAGER_NATIVE_TURN),
    }
    optional {
        replacement: Kind::String,
    }
};
const MANAGER_BINDING: &[Field] = fields! {
    required {
        conversation_id: Kind::String,
        provider: Kind::String,
        session_id: Kind::String,
        session_instance: Kind::String,
        native_session_id: Kind::String,
        native_generation: Kind::String,
        observation_generation: Kind::String,
        login_epoch: Kind::String,
        generation: Kind::Unsigned,
    }
    optional {
        native_cli: Kind::Bool,
    }
};
const REUSED_FINAL_RECEIPT: &[Field] = fields! {
    required {
        publication_id: Kind::String,
        output_id: Kind::String,
        native_turn_id: Kind::String,
        sequence: Kind::Unsigned,
    }
    optional {
    }
};
const DISCUSSION_OUTPUT_SOURCE: &[Field] = fields! {
    required {
    }
    optional {
        admission_id: Kind::String,
        job_id: Kind::String,
        attempt_id: Kind::String,
        system_cause: Kind::String,
    }
};
const MESSAGE_ORIGIN: &[Field] = fields! {
    required {
        kind: Kind::String,
        installation_id: Kind::String,
        request_id: Kind::String,
        actor_id: Kind::String,
    }
    optional {
        requester_id: Kind::String,
        actor_name: Kind::String,
        guild_id: Kind::String,
        channel_id: Kind::String,
        message_id: Kind::String,
        endorser_id: Kind::String,
        discussion_id: Kind::String,
        revision: Kind::String,
    }
};
const MANAGER_RUNTIME_BINDING: &[Field] = fields! {
    required {
        session: Kind::String,
        session_instance: Kind::String,
        conversation_id: Kind::String,
        provider: Kind::String,
        native_session_id: Kind::String,
        native_generation: Kind::String,
        observation_generation: Kind::String,
        login_epoch: Kind::String,
    }
    optional {
    }
};
const MANAGER_NATIVE_TURN: &[Field] = fields! {
    required {
        state: Kind::String,
        external_input: Kind::Unsigned,
    }
    optional {
        externally_steered: Kind::Bool,
        native_input_id: Kind::String,
        native_turn_id: Kind::String,
        reason: Kind::String,
    }
};

const TABLES: &[(&str, Kind)] = &[
    ("workers", Kind::Object(WORKER_ASSOCIATION)),
    ("conversations", Kind::Object(CONVERSATION)),
    ("utterances", Kind::Object(UTTERANCE)),
    ("admissions", Kind::Object(ADMISSION)),
    ("actions", Kind::Object(ACTION)),
    ("closed_turns", Kind::Bool),
    ("cancelled_tasks", Kind::Bool),
    ("requests", Kind::Object(REQUEST)),
    ("permits", Kind::Object(SPEECH_PERMIT)),
    ("publications", Kind::Object(PUBLICATION)),
    ("journal", Kind::Object(JOURNAL_CURSOR)),
    ("journal_snapshots", Kind::Object(JOURNAL_SNAPSHOT)),
    ("events", Kind::Object(EVENT)),
    ("wakes", Kind::Object(WAKE)),
];

#[cfg(test)]
mod tests {
    use super::*;
    use bbolt_rs::{Bolt, BucketRwApi, DbRwAPI, TxRwApi, TxRwRefApi};

    #[test]
    fn logical_size_and_typed_rows_are_checked_in_the_same_transaction() {
        let mut db = Bolt::open_mem().unwrap();
        let mut tx = db.begin_rw().unwrap();
        let first = b"journal\0discussion<&>";
        let second = b"journal\0older";
        let raw = br#"{"binding":"binding","sequence":9}"#;
        let raw2 = br#"{"binding":"before","sequence":7}"#;
        let mut root = tx.create_bucket(b"controller").unwrap();
        let mut records = root.create_bucket(b"records").unwrap();
        records.put(first, raw).unwrap();
        records.put(second, raw2).unwrap();

        // Exact Go encoding of the full v1 State, independent of the validator.
        let state = br#"{"version":1,"generation":1,"current_conversation":"discussion","workers":{},"conversations":{},"utterances":{},"admissions":{},"actions":{},"closed_turns":{},"cancelled_tasks":{},"requests":{},"permits":{},"publications":{},"journal":{"discussion\u003c\u0026\u003e":{"binding":"binding","sequence":9},"older":{"binding":"before","sequence":7}},"journal_snapshots":{},"events":{},"wakes":{}}"#;
        let header = |size: usize, raw2: &[u8]| {
            let mut checksum = record_hash(first, raw);
            for (left, right) in checksum.iter_mut().zip(record_hash(second, raw2)) {
                *left ^= right;
            }
            json!({"version":4,"generation":1,"current_conversation":"discussion",
                   "logical_bytes":size,"records":2,"record_hash":checksum})
        };
        fn write_header<'tx>(tx: &mut impl TxRwApi<'tx>, val: &Value) {
            let bytes = go_bytes(val).unwrap();
            let mut root = tx.bucket_mut(b"controller").unwrap();
            root.put(b"state", &bytes).unwrap();
            root.put(b"digest", format!("{:x}", Sha256::digest(bytes)))
                .unwrap();
        }
        write_header(&mut tx, &header(state.len(), raw2));
        assert!(reopen(&tx).is_ok());
        write_header(&mut tx, &header(state.len() + 1, raw2));
        assert!(reopen(&tx).is_err());

        // Correct keyed digest and size for raw JSON are insufficient when Go
        // drops an unknown field or defaults an absent required typed field.
        let unknown = br#"{"binding":"before","unknowns":7}"#;
        tx.bucket_mut(b"controller")
            .unwrap()
            .bucket_mut(b"records")
            .unwrap()
            .put(second, unknown)
            .unwrap();
        write_header(&mut tx, &header(state.len(), unknown));
        assert!(reopen(&tx).is_err());
    }
}
