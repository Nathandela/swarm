use super::{Backend, in_scope};
use crate::{
    ipc::field,
    providers::{
        codex_receipt,
        opencode::{
            self,
            receipts::{self, canonical, intent},
        },
    },
    supervisor::runtime,
};
use serde_json::{Value, json};
use std::io;

const CARD_TTL_MS: u64 = 5 * 60 * 1000;

impl Backend {
    pub(in crate::shim) fn operation(
        &self,
        session: &str,
        operation: &str,
        kind: &str,
        request: Option<&Value>,
    ) -> io::Result<Option<Value>> {
        let _action = self
            .actions
            .try_lock()
            .map_err(|_| io::Error::other("OpenCode native action lane is busy"))?;
        let native = receipts::native(&self.dir, &self.token)?;
        let created = native["nativeCreatedAtMS"].as_u64().unwrap_or_default();
        if let Some(mut record) = receipts::read(&self.dir, &self.token, session, operation)? {
            if request.is_some_and(|req| record["intent"] != intent(req)) || record["kind"] != kind
            {
                return Err(io::Error::other(
                    "OpenCode operation is bound to another request",
                ));
            }
            self.advance(created, operation, &mut record, None)?;
            return Ok(Some(record["receipt"].clone()));
        }
        let Some(request) = request else {
            return Ok(None);
        };
        let mut body = self.envelope();
        body["operationID"] = json!(operation);
        let mut receipt = receipts::base(&native, operation, session);
        let expected_effect;
        if kind == "prompt" {
            if !codex_receipt::timely(operation)
                || request["session_instance"] != native["sessionInstance"]
            {
                return Err(io::Error::other("stale OpenCode semantic target"));
            }
            let text = field(request, "text");
            if text.is_empty() || text.len() > 4096 || text.contains('\0') {
                return Err(io::Error::other("invalid OpenCode semantic input"));
            }
            // A previous unresolved input must be inspected before submitting different text.
            let prior = self.dir.join("opencode-last-prompt.json");
            match runtime::read_json(&prior) {
                Ok(value) => {
                    let prior_id = value
                        .as_str()
                        .ok_or_else(|| io::Error::other("invalid OpenCode prompt fence"))?;
                    let Some(mut record) =
                        receipts::read(&self.dir, &self.token, session, prior_id)?
                    else {
                        return Err(io::Error::other(
                            "an earlier OpenCode input remains unresolved",
                        ));
                    };
                    if matches!(
                        field(&record["receipt"], "state"),
                        "outcome_unknown" | "native_accepted"
                    ) {
                        self.advance(created, prior_id, &mut record, None)?;
                        if matches!(
                            field(&record["receipt"], "state"),
                            "outcome_unknown" | "native_accepted"
                        ) {
                            return Err(io::Error::other(
                                "an earlier OpenCode input remains unresolved",
                            ));
                        }
                    }
                }
                Err(err) if err.kind() == io::ErrorKind::NotFound => {}
                Err(err) => return Err(err),
            }
            body["kind"] = json!("prompt");
            body["targetID"] = native["rootID"].clone();
            let digest = codex_receipt::hash(&format!(
                "{operation}\0{}\0{}",
                field(&native, "rootID"),
                field(&native, "backendGeneration")
            ));
            body["messageID"] = json!(format!("msg_{}", &digest[..32]));
            body["text"] = json!(text);
            receipt["state"] = json!("outcome_unknown");
            receipt["agent"] = json!("opencode");
            receipt["native_session_id"] = native["rootID"].clone();
            expected_effect = "";
        } else {
            opencode::validate_decision(request)?;
            let snapshot = self.snapshot(created, 1, "")?;
            let target = field(request, "native_session_id");
            let request_id = field(request, "request_id");
            let (pending, pending_kind) =
                pending(&snapshot, field(&native, "rootID"), request_id, target)?;
            let card = self.card(&native, session, pending, pending_kind, false)?;
            if card["request_hash"] != request["request_hash"]
                || ["session_instance", "generation", "root_id", "expires_at"]
                    .iter()
                    .any(|key| request[*key] != card[*key])
            {
                return Err(io::Error::other(
                    "OpenCode native request changed or approval expired",
                ));
            }
            let native_kind = field(request, "kind");
            let reply = field(request, "reply");
            expected_effect = match (pending_kind, native_kind, reply) {
                ("permission", "permission_reply", "once")
                | ("question", "question_reject", "") => "single_request",
                ("permission", "permission_reply", "always") => {
                    "approve_native_patterns_and_matching_session_requests"
                }
                ("permission", "permission_reply", "reject") => {
                    "reject_session_pending_permissions"
                }
                ("question", "question_reply", "") if request["answers"].is_array() => {
                    "single_request"
                }
                _ => return Err(io::Error::other("invalid OpenCode native decision")),
            };
            body["kind"] = json!(native_kind);
            body["targetID"] = json!(target);
            body["requestID"] = json!(request_id);
            body["expectedRequest"] = pending.clone();
            body["requestExpiresAtMS"] = card["expiry_ms"].clone();
            if pending_kind == "permission" {
                body["reply"] = json!(reply);
            } else if native_kind == "question_reply" {
                body["answers"] = request["answers"].clone();
            }
            receipt["state"] = json!("uncertain");
            receipt["kind"] = json!(native_kind);
            receipt["native_session_id"] = json!(target);
            receipt["request_id"] = json!(request_id);
            receipt["native_effect"] = json!(expected_effect);
        }
        // Use the provider bridge's canonical encoding for exact native request metadata.
        let digest = match self.http("/dispatchDigest", Some(&body)) {
            // A second read-only prompt preflight fits the owner deadline. Decision preflight
            // also spends a snapshot budget, and a native mutation must never retry here.
            Err(error) if kind == "prompt" && error.kind() == io::ErrorKind::TimedOut => {
                self.http("/dispatchDigest", Some(&body))
            }
            result => result,
        }
        .map_err(|error| {
            io::Error::new(
                error.kind(),
                format!("OpenCode dispatch preflight: {error}"),
            )
        })?;
        let request_hash = field(&digest, "requestHash");
        if !self.matches(&digest, created)
            || digest["operationID"] != operation
            || !opencode::hex_hash(request_hash)
        {
            return Err(io::Error::other("OpenCode dispatch digest is uncorrelated"));
        }
        let mut record = json!({"token":self.token,"kind":kind,"intent":intent(request),"receipt":receipt,
            "target":body["targetID"],"dispatch_kind":body["kind"],"expected_effect":expected_effect,
            "request_id":body["requestID"],"message_id":body["messageID"],"request_hash":request_hash,"decision_reply":body["reply"],
            "issued_ms":crate::ipc::spawn::now_ms(),"text_hash":codex_receipt::hash(field(&body,"text"))});
        receipts::save(&self.dir, operation, &record)?;
        if kind == "prompt" {
            runtime::write_json(
                &self.dir.join("opencode-last-prompt.json"),
                &json!(operation),
            )?;
        }
        // No caller can re-enter the native POST after the durable uncertain intent is recorded.
        if let Ok(reply) = self.http("/dispatch", Some(&body)) {
            self.advance(created, operation, &mut record, Some(reply))?;
        }
        Ok(Some(record["receipt"].clone()))
    }

    fn advance(
        &self,
        created: u64,
        operation: &str,
        record: &mut Value,
        reply: Option<Value>,
    ) -> io::Result<()> {
        let prompt = record["kind"] == "prompt";
        if reply.is_none()
            && prompt
            && matches!(
                field(&record["receipt"], "state"),
                "native_accepted" | "outcome_unknown"
            )
        {
            let _ = self.reconcile_prompt(created, operation, record);
        }
        if !matches!(
            field(&record["receipt"], "state"),
            "outcome_unknown" | "uncertain"
        ) {
            return Ok(());
        }
        let native = reply.or_else(|| {
            self.http(
                &format!(
                    "/receipt?operationID={}",
                    super::encode_component(operation)
                ),
                None,
            )
            .ok()
        });
        let Some(native) = native else { return Ok(()) };
        if !self.matches(&native, created) || !receipts::bridge_matches(record, &native) {
            return Ok(());
        }
        match field(&native, "status") {
            "rejected" if !field(&native, "reason").is_empty() => {
                record["receipt"]["state"] = json!("rejected");
                record["receipt"]["reason"] = native["reason"].clone();
                if !prompt {
                    record["receipt"]["native_effect"] = json!("none");
                }
            }
            "admitted"
                if prompt
                    && native["selectionBasis"] == "native_session_selection"
                    && !field(&native, "agent").is_empty()
                    && !field(&native["model"], "providerID").is_empty()
                    && !field(&native["model"], "modelID").is_empty() =>
            {
                record["receipt"]["state"] = json!("native_accepted");
                record["receipt"]["native_message_id"] = native["messageID"].clone();
                record["receipt"]["native_input_id"] = native["messageID"].clone();
                record["receipt"]["model"] = json!(format!(
                    "{}/{}",
                    field(&native["model"], "providerID"),
                    field(&native["model"], "modelID")
                ));
                record["receipt"]["agent"] = native["agent"].clone();
                record["receipt"]["variant"] = native["variant"].clone();
                record["receipt"]["selection_basis"] = native["selectionBasis"].clone();
                record["receipt"]["selection_message_id"] = native["selectionMessageID"].clone();
            }
            "admitted" if !prompt && native["nativeEffect"] == record["expected_effect"] => {
                record["receipt"]["state"] = json!("admitted");
            }
            _ => return Ok(()),
        }
        record["bridge_receipt"] = native;
        receipts::save(&self.dir, operation, record)
    }
    fn reconcile_prompt(
        &self,
        created: u64,
        operation: &str,
        record: &mut Value,
    ) -> io::Result<()> {
        if let Some(next) = self.observe_prompt(created, record)? {
            receipts::save(&self.dir, operation, &next)?;
            *record = next;
        }
        Ok(())
    }

    fn observe_prompt(&self, created: u64, record: &Value) -> io::Result<Option<Value>> {
        let root = field(record, "target");
        let input = field(record, "message_id");
        let row = self.http(&format!("/message?messageID={input}"), None)?;
        let info = &row["info"];
        let at = info["time"]["created"].as_u64().unwrap_or_default();
        if row["info"]["id"] != input
            || row["info"]["sessionID"] != root
            || info["role"] != "user"
            || at < record["issued_ms"].as_u64().unwrap_or(u64::MAX)
        {
            return Ok(None);
        }
        let parts = row["parts"]
            .as_array()
            .ok_or_else(|| io::Error::other("OpenCode input parts unavailable"))?;
        let mut part_ids = std::collections::BTreeSet::new();
        if parts.iter().any(|part| {
            part["messageID"] != input
                || part["sessionID"] != root
                || field(part, "id").is_empty()
                || !part_ids.insert(field(part, "id"))
        }) {
            return Ok(None);
        }
        let text: Vec<_> = parts
            .iter()
            .filter(|part| part["synthetic"] != true && part["ignored"] != true)
            .collect();
        if text.len() != 1
            || text[0]["type"] != "text"
            || codex_receipt::hash(field(text[0], "text")) != record["text_hash"]
            || field(info, "agent").is_empty()
            || ["providerID", "modelID"]
                .iter()
                .any(|name| field(&info["model"], name).is_empty())
        {
            return Ok(None);
        }
        let state = field(&record["receipt"], "state");
        let mut receipt = record["receipt"].clone();
        if state == "outcome_unknown" {
            receipt["state"] = json!("native_accepted");
            receipt["native_message_id"] = json!(input);
            receipt["native_input_id"] = json!(input);
            receipt["model"] = json!(format!(
                "{}/{}",
                field(&info["model"], "providerID"),
                field(&info["model"], "modelID")
            ));
            receipt["variant"] = json!(info["model"]["variant"].as_str().unwrap_or("default"));
            receipt["agent"] = info["agent"].clone();
            receipt["selection_basis"] = json!("native_message_receipt");
            receipt["selection_message_id"] = json!(input);
        }
        let native_input = json!({
            "id":input,"session":root,"created_ms":at,"agent":info["agent"],
            "model":receipt["model"],"text_hash":record["text_hash"],
        });
        let outcome = find_terminal(root, input, at, |before| {
            let snapshot = self.snapshot(created, 50, before)?;
            let status = super::status(&snapshot, root);
            if status["Turn"] != "idle" || status["Interaction"] != "none" {
                return Err(io::Error::other("OpenCode native work is still active"));
            }
            Ok(snapshot)
        });
        let mut next = record.clone();
        if let Some((state, native_message)) = outcome {
            receipt["state"] = json!(state);
            receipt["native_turn_id"] = json!(native_message);
            receipt["outcome_source"] = json!("reconciled_snapshot");
            next["terminal_proof"] = json!({"state":state,"assistant":native_message,"parent":row["info"]["id"],"session":row["info"]["sessionID"]});
        }
        if record["receipt"] == receipt {
            return Ok(None);
        }
        next["receipt"] = receipt;
        next["native_input"] = native_input;
        Ok(Some(next))
    }
    pub(super) fn checkpoint_prompt(&self, created: u64) -> io::Result<()> {
        let Some((operation, before)) = receipts::pending_prompt(&self.dir, &self.token)? else {
            return Ok(());
        };
        let Some(next) = self.observe_prompt(created, &before)? else {
            return Ok(());
        };
        // SDK reads can wait on the worker. Acquire the mutation lane only for
        // the compare-and-save so a slow read cannot block a native decision.
        let Ok(_action) = self.actions.try_lock() else {
            return Ok(());
        };
        if receipts::pending_prompt(&self.dir, &self.token)?
            .is_some_and(|(current, receipt)| current == operation && receipt == before)
        {
            receipts::save(&self.dir, &operation, &next)?;
        }
        Ok(())
    }
}

enum Assistant<'a> {
    Continuation(u64),
    Terminal {
        started: u64,
        state: &'static str,
        id: &'a str,
    },
}

fn classify_assistant<'a>(row: &'a Value, root: &str) -> Option<Assistant<'a>> {
    let message = &row["info"];
    let id = field(message, "id");
    let started = message["time"]["created"].as_u64()?;
    let finish = field(message, "finish");
    let terminal = |state| Some(Assistant::Terminal { started, state, id });
    if message["error"].is_object() {
        if matches!(finish, "stop" | "length") {
            return None;
        }
        return terminal(match field(&message["error"], "name") {
            "MessageAbortedError" => "cancelled",
            "ContentFilterError" => "declined",
            _ => "failed",
        });
    }
    let completed = message["time"]["completed"]
        .as_u64()
        .filter(|time| *time >= started)?;
    if finish == "content-filter" {
        return terminal("declined");
    }
    let mut part_ids = std::collections::BTreeSet::new();
    let mut local_tool = false;
    for part in row["parts"].as_array()? {
        if part["sessionID"] != root
            || part["messageID"] != id
            || field(part, "id").is_empty()
            || !part_ids.insert(field(part, "id"))
        {
            return None;
        }
        if part["type"] == "tool"
            && part["metadata"]["providerExecuted"] != true
            && !(part["state"]["status"] == "error"
                && part["state"]["metadata"]["interrupted"] == true)
        {
            if !matches!(field(&part["state"], "status"), "completed" | "error") {
                return None;
            }
            local_tool = true;
        }
    }
    match (local_tool, finish) {
        (true, "stop" | "length" | "tool-calls") => Some(Assistant::Continuation(completed)),
        (false, "stop" | "length") => terminal("completed"),
        _ => None,
    }
}

fn find_terminal(
    root: &str,
    input: &str,
    at: u64,
    mut read: impl FnMut(&str) -> io::Result<Value>,
) -> Option<(&'static str, String)> {
    let started = std::time::Instant::now();
    let mut before = String::new();
    let mut cursors = std::collections::BTreeSet::new();
    let mut seen = std::collections::BTreeSet::new();
    let mut newer_floor = u64::MAX;
    let mut terminal = None;
    let mut latest_continuation = 0;
    let mut verified_input = false;
    for _ in 0..8 {
        let snapshot = read(&before).ok()?;
        if started.elapsed() >= std::time::Duration::from_secs(6) {
            return None;
        }
        let rows = snapshot["messages"].as_array()?;
        let cursor = snapshot["nextCursor"].as_str()?;
        if rows.is_empty() && !cursor.is_empty() {
            return None;
        }
        let mut floor = newer_floor;
        for row in rows {
            let message = &row["info"];
            let id = field(message, "id");
            let created = message["time"]["created"].as_u64()?;
            if !opencode::valid_id(id, "msg_")
                || message["sessionID"] != root
                || !seen.insert(id.to_owned())
                || created > newer_floor
            {
                return None;
            }
            floor = floor.min(created);
            if id == input {
                if message["role"] != "user" || created != at {
                    return None;
                }
                verified_input = true;
            }
            if message["parentID"] == input && message["summary"] != true {
                if message["role"] != "assistant" || created < at {
                    return None;
                }
                match classify_assistant(row, root)? {
                    Assistant::Continuation(until) => {
                        latest_continuation = latest_continuation.max(until)
                    }
                    Assistant::Terminal { started, state, id } => {
                        if terminal.replace((started, state, id.to_owned())).is_some() {
                            return None;
                        }
                    }
                }
            }
        }
        if verified_input && (floor < at || cursor.is_empty()) {
            return terminal
                .filter(|(started, _, _)| *started >= latest_continuation)
                .map(|(_, state, id)| (state, id));
        }
        if cursor.is_empty() || !cursors.insert(cursor.to_owned()) {
            return None;
        }
        cursor.clone_into(&mut before);
        newer_floor = floor;
    }
    None
}

fn unexpired(record: &Value, now: u64) -> Option<u64> {
    record["expiry_ms"].as_u64().filter(|ms| *ms > now)
}

fn pending<'a>(
    snapshot: &'a Value,
    root: &'a str,
    id: &str,
    target: &str,
) -> io::Result<(&'a Value, &'static str)> {
    let mut rows =
        requests(snapshot, root).filter(|(row, _)| row["id"] == id && row["sessionID"] == target);
    let row = rows
        .next()
        .ok_or_else(|| io::Error::other("exact OpenCode native request is no longer pending"))?;
    if rows.next().is_some() {
        return Err(io::Error::other(
            "duplicate OpenCode native request identity",
        ));
    }
    Ok(row)
}

fn requests<'a>(
    snapshot: &'a Value,
    root: &'a str,
) -> impl Iterator<Item = (&'a Value, &'static str)> {
    [
        ("permissions", "permission", "per_"),
        ("questions", "question", "que_"),
    ]
    .into_iter()
    .flat_map(move |(key, kind, prefix)| {
        snapshot[key]
            .as_array()
            .into_iter()
            .flatten()
            .filter(move |row| {
                in_scope(snapshot, root, field(row, "sessionID"))
                    && opencode::valid_id(field(row, "id"), prefix)
            })
            .map(move |row| (row, kind))
    })
}

impl Backend {
    pub(in crate::shim) fn requests(
        &self,
        session: &str,
        request: &str,
        target: &str,
    ) -> io::Result<Value> {
        let native = receipts::native(&self.dir, &self.token)?;
        let snapshot = self.snapshot(
            native["nativeCreatedAtMS"].as_u64().unwrap_or_default(),
            1,
            "",
        )?;
        let root = field(&native, "rootID");
        if request.is_empty() {
            let summaries = requests(&snapshot,root).map(|(row,kind)| {
                let label = if kind == "permission" { field(row,"permission") } else { field(&row["questions"][0],"question") };
                json!({"kind":kind,"request_id":row["id"],"native_session_id":row["sessionID"],"summary":label.chars().filter(|ch| !ch.is_control()).take(512).collect::<String>()})
            }).collect::<Vec<_>>();
            return Ok(if summaries.is_empty() {
                json!({"session":session})
            } else {
                json!({"session":session,"requests":summaries})
            });
        }
        let _action = self
            .actions
            .try_lock()
            .map_err(|_| io::Error::other("OpenCode native action lane is busy"))?;
        let (pending, kind) = pending(&snapshot, root, request, target)?;
        let mut card = self.card(&native, session, pending, kind, true)?;
        card.as_object_mut().unwrap().shift_remove("expiry_ms");
        Ok(json!({"session":session,"request":card}))
    }

    fn card(
        &self,
        native: &Value,
        session: &str,
        pending: &Value,
        kind: &str,
        issue: bool,
    ) -> io::Result<Value> {
        let details = canonical(pending);
        if details.len() > 32 << 10 {
            return Err(io::Error::other(
                "OpenCode request context exceeds decision boundary",
            ));
        }
        let id = field(pending, "id");
        let target = field(pending, "sessionID");
        let key = intent(&json!([native["sessionInstance"], target, id]));
        let content = codex_receipt::hash(&details);
        let dir = self.dir.join("opencode-cards");
        let path = dir.join(format!("{key}.json"));
        let now = crate::ipc::spawn::now_ms();
        let old = match runtime::read_json(&path) {
            Ok(record) if record["content"] == content => unexpired(&record, now),
            Ok(_) => None,
            Err(err) if err.kind() == io::ErrorKind::NotFound => None,
            Err(err) => return Err(err),
        };
        let expiry = match old {
            Some(expiry) => expiry,
            None if issue => {
                let expiry = now + CARD_TTL_MS;
                runtime::private_dir(&dir)?;
                std::fs::File::open(&self.dir)?.sync_all()?;
                runtime::write_json(&path, &json!({"content":content,"expiry_ms":expiry}))?;
                expiry
            }
            None => {
                return Err(io::Error::other(
                    "inspect the full current OpenCode request before deciding",
                ));
            }
        };
        let expires_at =
            time::OffsetDateTime::from_unix_timestamp_nanos(i128::from(expiry) * 1_000_000)
                .map_err(io::Error::other)?
                .format(&time::format_description::well_known::Rfc3339)
                .map_err(io::Error::other)?;
        let hash = intent(&json!([key, content, expiry]));
        let mut card = json!({
            "session":session,"session_instance":native["sessionInstance"],"root_id":native["rootID"],
            "generation":native["backendGeneration"],"native_session_id":target,"request_id":id,
            "kind":kind,"details_json":details,"request_hash":hash,"expires_at":expires_at,"expiry_ms":expiry,
        });
        if kind == "permission" {
            for key in ["permission", "patterns", "always"] {
                card[key] = pending[key].clone();
            }
            card["decisions"] = json!([
                {"id":"once","label":"Allow once","effect":"single_request"},
                {"id":"always","label":"Always allow","effect":"approve_native_patterns_and_matching_session_requests"},
                {"id":"reject","label":"Reject","effect":"reject_session_pending_permissions"},
            ]);
        } else {
            card["questions"] = pending["questions"].clone();
            card["decisions"] = json!([
                {"id":"answer","label":"Answer","effect":"single_request"},
                {"id":"reject","label":"Reject","effect":"single_request"},
            ]);
        }
        Ok(card)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn request_identity_scopes_descendants_and_hash_preserves_ordered_answers() {
        let root = "ses_012345678900";
        let child = "ses_012345678901";
        let outsider = "ses_012345678902";
        let request = "que_012345678900";
        let snapshot = json!({"descendants":[{"id":child}],"questions":[
            {"id":request,"sessionID":child},{"id":request,"sessionID":outsider}
        ]});
        assert!(pending(&snapshot, root, request, child).is_ok());
        assert!(pending(&snapshot, root, request, outsider).is_err());
        assert_ne!(
            intent(&json!({"answers":[["a"],["b"]]})),
            intent(&json!({"answers":[["b"],["a"]]}))
        );
        assert_eq!(intent(&json!({"a":1,"z":2})), intent(&json!({"z":2,"a":1})));
    }
    #[test]
    fn completion_requires_the_exact_parent_and_tool_free_terminal_parts() {
        let root = "ses_012345678900";
        let input = "msg_012345678900";
        let reply = "msg_012345678901";
        let mut snapshot = json!({"messages":[{"info":{
            "id":reply,"sessionID":root,"parentID":input,"role":"assistant",
            "time":{"created":2,"completed":3},"finish":"stop"
        },"parts":[{"id":"prt_safe","sessionID":root,"messageID":reply,"type":"tool"}]}]});
        assert!(classify_assistant(&snapshot["messages"][0], root).is_none());
        snapshot["messages"][0]["parts"][0]["metadata"]["providerExecuted"] = json!(true);
        assert_eq!(
            match classify_assistant(&snapshot["messages"][0], root) {
                Some(Assistant::Terminal { state, id, .. }) => Some((state, id)),
                _ => None,
            },
            Some(("completed", reply))
        );
        snapshot["messages"][0]["parts"][0]["metadata"] = Value::Null;
        snapshot["messages"][0]["parts"][0]["state"] =
            json!({"status":"error","metadata":{"interrupted":true}});
        assert_eq!(
            match classify_assistant(&snapshot["messages"][0], root) {
                Some(Assistant::Terminal { state, id, .. }) => Some((state, id)),
                _ => None,
            },
            Some(("completed", reply))
        );
        snapshot["messages"][0]["parts"][0]["state"]["metadata"]["interrupted"] = json!(false);
        assert!(
            classify_assistant(&snapshot["messages"][0], root)
                .is_some_and(|result| matches!(result, Assistant::Continuation(3)))
        );
        snapshot["messages"][0]["parts"][0]["type"] = json!("text");
        assert_eq!(
            match classify_assistant(&snapshot["messages"][0], root) {
                Some(Assistant::Terminal { state, id, .. }) => Some((state, id)),
                _ => None,
            },
            Some(("completed", reply))
        );
    }
    #[test]
    fn request_card_still_authorizes_after_two_minutes_and_expires_at_five() {
        let issued = 1_000;
        let record = json!({"expiry_ms":issued + CARD_TTL_MS});
        assert!(unexpired(&record, issued + 180_000).is_some());
        assert!(unexpired(&record, issued + 300_000).is_none());
    }
    #[test]
    fn recovers_an_exact_terminal_outside_fifty_newer_native_rows() {
        let root = "ses_012345678900";
        let input = "msg_012345678900";
        let reply = "msg_012345678901";
        let mut cursors = Vec::new();
        let rows: Vec<_> = (0..50)
            .map(|index| {
                json!({"info":{
            "id":format!("msg_{index:012}"),"sessionID":root,"role":"user",
            "time":{"created":100+index}
        },"parts":[]})
            })
            .collect();
        let outcome = find_terminal(root, input, 1, |cursor| {
            cursors.push(cursor.to_owned());
            Ok(if cursor.is_empty() {
                json!({"messages":rows,"nextCursor":"older"})
            } else {
                json!({"messages":[{"info":{
                    "id":reply,"sessionID":root,"parentID":input,"role":"assistant",
                    "time":{"created":2,"completed":3},"finish":"stop"
                },"parts":[]},{"info":{"id":input,"sessionID":root,"role":"user",
                    "time":{"created":1}},"parts":[]}],"nextCursor":""})
            })
        });
        assert_eq!(cursors, ["", "older"]);
        assert_eq!(outcome, Some(("completed", reply.to_owned())));
    }
    #[test]
    fn terminal_history_requires_verified_input_and_one_final_child() {
        let root = "ses_012345678900";
        let input = "msg_012345678900";
        let reply = "msg_012345678901";
        let assistant = json!({"info":{"id":reply,"sessionID":root,"parentID":input,
            "role":"assistant","time":{"created":4,"completed":5},"finish":"stop"},"parts":[]});
        let user = json!({"info":{"id":input,"sessionID":root,"role":"user","time":{"created":1}},"parts":[]});
        let error = json!({"info":{"id":"msg_012345678902","sessionID":root,"parentID":input,
            "role":"assistant","time":{"created":2,"completed":3},"error":{"name":"MessageAbortedError"}},"parts":[]});
        let continuation = json!({"info":{"id":"msg_012345678903","sessionID":root,"parentID":input,
        "role":"assistant","time":{"created":2,"completed":3},"finish":"stop"},"parts":[{
          "id":"prt_valid","sessionID":root,"messageID":"msg_012345678903","type":"tool","state":{"status":"completed"}
        }]});
        let check = |rows: Vec<Value>| {
            find_terminal(root, input, 1, |_| {
                Ok(json!({"messages":rows,"nextCursor":""}))
            })
        };
        assert_eq!(check(vec![assistant.clone()]), None);
        assert_eq!(check(vec![assistant.clone(), error, user.clone()]), None);
        assert_eq!(
            check(vec![assistant.clone(), continuation, user]),
            Some(("completed", reply.to_owned()))
        );
        assert_eq!(
            find_terminal(root, input, 1, |cursor| if cursor.is_empty() {
                Ok(json!({"messages":[assistant],"nextCursor":"deeper"}))
            } else {
                Err(io::Error::other("history lost"))
            }),
            None
        );
        assert_eq!(
            find_terminal(root, input, 1, |_| Ok(
                json!({"messages":[assistant],"nextCursor":"repeated"})
            )),
            None
        );
    }
}
