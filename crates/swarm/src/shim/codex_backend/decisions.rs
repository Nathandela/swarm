use super::{Observation, SemanticTarget, observe_frame, read, send};
use crate::ipc::field;
use crate::providers::codex_receipt;
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::io;
use std::os::unix::net::UnixStream;
use std::time::{Duration, Instant};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tungstenite::WebSocket;

pub(super) struct Pending {
    pub kind: &'static str,
    pub raw: Value,
    card: Option<Value>,
}

impl Pending {
    pub(super) fn new(kind: &'static str, raw: Value) -> Self {
        Self {
            kind,
            raw,
            card: None,
        }
    }
    pub(super) fn id(&self) -> String {
        let id = &self.raw["id"];
        id.as_str()
            .map(str::to_owned)
            .unwrap_or_else(|| id.to_string())
    }

    fn preview(&self) -> io::Result<Value> {
        let details = &self.raw["params"];
        if self.kind != "user_input" {
            let command = self.kind == "command";
            if command
                && ((!details["kind"].is_null() && details["kind"] != "command")
                    || !details["networkApprovalContext"].is_null()
                    || field(details, "command").is_empty())
            {
                return Err(io::Error::other(
                    "approval scope requires the native viewer",
                ));
            }
            if !command && !details["grantRoot"].is_null() {
                return Err(io::Error::other(
                    "directory approval requires the native viewer",
                ));
            }
            return Ok(json!({
                "kind":"permission",
                "details_json":details.to_string(),
                "permission":if command {"command_execution"} else {"file_change"},
                "decisions":[
                    {"id":"once","label":"Allow once","effect":"single_request"},
                    {"id":"reject","label":"Deny","effect":"single_request"}
                ]
            }));
        }
        let questions = details["questions"]
            .as_array()
            .filter(|rows| (1..=16).contains(&rows.len()))
            .ok_or_else(|| io::Error::other("question scope requires the native viewer"))?;
        let mut public = Vec::new();
        let mut ids = std::collections::BTreeSet::new();
        for question in questions {
            let id = field(question, "id");
            let options = question["options"]
                .as_array()
                .filter(|rows| (1..=32).contains(&rows.len()))
                .ok_or_else(|| io::Error::other("question options require the native viewer"))?;
            if id.is_empty()
                || !ids.insert(id)
                || question["isSecret"] == true
                || options
                    .iter()
                    .any(|option| field(option, "label").is_empty())
            {
                return Err(io::Error::other(
                    "question scope requires the native viewer",
                ));
            }
            public.push(json!({
                "question":question["question"],"header":question["header"],"options":options,
                "multiple":false,"custom":false,
            }));
        }
        Ok(json!({"kind":"question","questions":public,"decisions":[
            {"id":"answer","label":"Answer","effect":"single_request"}
        ]}))
    }

    pub(super) fn issue(&mut self, token: &str, session: &str, root: &str) -> io::Result<Value> {
        if let Some(card) = &self.card
            && live(card)
            && card["session"] == session
        {
            return Ok(card.clone());
        }
        if self.raw.to_string().len() > 32 << 10 {
            return Err(io::Error::other(
                "Codex native request exceeds the owner decision boundary",
            ));
        }
        let mut card = self.preview()?;
        card["session"] = json!(session);
        card["session_instance"] = json!(codex_receipt::instance(token));
        card["generation"] = json!(codex_receipt::hash(&format!(
            "codex-owner-requests:{token}"
        )));
        card["root_id"] = json!(root);
        card["native_session_id"] = json!(root);
        card["request_id"] = json!(self.id());
        card["expires_at"] = json!(
            (OffsetDateTime::now_utc() + time::Duration::minutes(5))
                .format(&Rfc3339)
                .map_err(io::Error::other)?
        );
        card["request_hash"] = json!(codex_receipt::hash(&json!([card, self.raw]).to_string()));
        self.card = Some(card.clone());
        Ok(card)
    }

    fn result(&self, body: &Value) -> io::Result<Value> {
        let card = self
            .card
            .as_ref()
            .filter(|card| live(card))
            .ok_or_else(|| io::Error::other("inspect the complete current Codex request first"))?;
        for key in [
            "session",
            "session_instance",
            "generation",
            "root_id",
            "native_session_id",
            "request_id",
            "request_hash",
            "expires_at",
        ] {
            if body[key] != card[key] {
                return Err(io::Error::other(
                    "Codex request authority is stale or changed",
                ));
            }
        }
        match field(body, "kind") {
            "permission_reply" if self.kind != "user_input" && body["answers"].is_null() => {
                let decision = match field(body, "reply") {
                    "once" => "accept",
                    "reject" => "decline",
                    _ => {
                        return Err(io::Error::other(
                            "Codex approval is limited to this one request",
                        ));
                    }
                };
                Ok(json!({"decision":decision}))
            }
            "question_reply" if self.kind == "user_input" && body["reply"].is_null() => {
                let questions = self.raw["params"]["questions"].as_array().unwrap();
                let choices = body["answers"]
                    .as_array()
                    .filter(|answers| answers.len() == questions.len())
                    .ok_or_else(|| io::Error::other("provide one answer per native question"))?;
                let mut answers = serde_json::Map::new();
                for (question, group) in questions.iter().zip(choices) {
                    let choice = group
                        .as_array()
                        .filter(|group| group.len() == 1)
                        .and_then(|group| group[0].as_str())
                        .ok_or_else(|| {
                            io::Error::other("provide exactly one offered answer per question")
                        })?;
                    if !question["options"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .any(|option| option["label"] == choice)
                    {
                        return Err(io::Error::other(
                            "answer was not offered by this native question",
                        ));
                    }
                    answers.insert(
                        field(question, "id").to_owned(),
                        json!({"answers":[choice]}),
                    );
                }
                Ok(json!({"answers":answers}))
            }
            _ => Err(io::Error::other("decision requires the native viewer")),
        }
    }
}

fn live(card: &Value) -> bool {
    OffsetDateTime::parse(field(card, "expires_at"), &Rfc3339)
        .is_ok_and(|expiry| expiry > OffsetDateTime::now_utc())
}

pub(super) fn select<'a>(
    observation: &'a mut Observation,
    request: &str,
    native: &str,
) -> io::Result<&'a mut Pending> {
    if native != observation.thread
        || !observation.joined
        || observation.ambiguous
        || observation.requests_unverified
    {
        return Err(io::Error::other(
            "Codex native request scope is unavailable",
        ));
    }
    let key = observation
        .requests
        .iter()
        .filter(|(_, pending)| pending.id() == request)
        .map(|(key, _)| key.to_owned())
        .collect::<Vec<_>>();
    if key.len() != 1 {
        return Err(io::Error::other("Codex request ID is unknown or ambiguous"));
    }
    let pending = observation.requests.get_mut(&key[0]).unwrap();
    if !observation
        .turn
        .as_deref()
        .is_some_and(|turn| pending.raw["params"]["turnId"] == turn)
    {
        return Err(io::Error::other(
            "Codex request no longer belongs to the active turn",
        ));
    }
    Ok(pending)
}

pub(super) fn apply(
    ws: &mut WebSocket<UnixStream>,
    observation: &mut Observation,
    target: &SemanticTarget<'_>,
    operation: &str,
    body: &Value,
    expires: Instant,
) -> io::Result<Value> {
    use crate::providers::codex_decision_receipt as durable;
    let intent = durable::intent(body);
    let root = target
        .dir
        .parent()
        .ok_or_else(|| io::Error::other("missing Codex runtime root"))?;
    let session = field(body, "session");
    codex_receipt::verify_binding(
        root,
        operation,
        session,
        &codex_receipt::instance(target.token),
        &intent,
    )?;
    if let Some(record) = durable::read(target.dir, target.token, session, operation)? {
        return Ok(record["receipt"].clone());
    }
    let pending = select(
        observation,
        field(body, "request_id"),
        field(body, "native_session_id"),
    )?;
    let result = pending.result(body)?;
    let id = pending.raw["id"].clone();
    let turn = pending.raw["params"]["turnId"].clone();
    let fence = target.dir.join(format!(
        "codex-request-{}.json",
        codex_receipt::hash(&json!([body["native_session_id"], turn, id]).to_string())
    ));
    match std::fs::symlink_metadata(&fence) {
        Ok(_) => {
            return Err(io::Error::other(
                "Codex request already admitted a decision",
            ));
        }
        Err(err) if err.kind() == io::ErrorKind::NotFound => {}
        Err(err) => return Err(err),
    }
    if Instant::now() >= expires {
        return Err(io::Error::other(
            "Codex decision authority expired before dispatch",
        ));
    }
    let mut receipt = body.clone();
    let fields = receipt.as_object_mut().unwrap();
    for key in ["expires_at", "reply", "answers"] {
        fields.shift_remove(key);
    }
    receipt["operation_id"] = json!(operation);
    receipt["state"] = json!("uncertain");
    runtime::write_json(&fence, &json!({"operation_id":operation,"intent":intent}))?;
    durable::save(target.dir, operation, &intent, &receipt)?;
    send(ws, json!({"id":id,"result":result}))?;
    let deadline = Instant::now() + Duration::from_secs(4);
    while Instant::now() < deadline {
        let Some(frame) = read(ws)? else { continue };
        let resolved = frame["method"] == "serverRequest/resolved"
            && frame["params"]["threadId"] == receipt["native_session_id"]
            && frame["params"]["requestId"] == id;
        observe_frame(observation, &frame, target);
        if resolved {
            receipt["state"] = json!("applied");
            receipt["native_effect"] = json!("single_request");
            durable::save(target.dir, operation, &intent, &receipt)?;
            return Ok(receipt);
        }
    }
    Ok(receipt)
}
