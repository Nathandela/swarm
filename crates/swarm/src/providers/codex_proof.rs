//! Native manager evidence from the owned Codex observer. The model's text is never authority.
pub(crate) mod claude;
use super::codex_receipt;
use crate::{ipc::field, supervisor::runtime};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::io;
use std::path::{Path, PathBuf};

const MAX_RECORDS: usize = 1024;

pub(crate) struct Ledger {
    file: PathBuf,
    data: Value,
}

impl Ledger {
    pub(crate) fn open(dir: &Path, binding: &Value) -> io::Result<Self> {
        let file = dir.join("manager-proof.json");
        let initial = json!({
            "schema":1,"binding":binding,"external_input":0,"blocked":false,"observed_turn":false,
            "prepared":{},"turns":{},"calls":{},"messages":{},"thread_state":"unknown"
        });
        let data = match read_evidence(&file) {
            Ok(data) => data,
            Err(err) if err.kind() == io::ErrorKind::NotFound => {
                save_evidence(&file, &initial)?;
                initial
            }
            Err(err) => return Err(err),
        };
        if data["schema"] != 1
            || data["binding"] != *binding
            || !data["external_input"].is_u64()
            || !data["blocked"].is_boolean()
            || ["prepared", "turns", "calls", "messages"]
                .iter()
                .any(|key| !data[*key].is_object())
        {
            return Err(refusal(
                "manager ledger has a different native custody binding",
            ));
        }
        Ok(Self { file, data })
    }

    pub(crate) fn is_virgin(&self) -> bool {
        self.data["blocked"] == false
            && self.data["observed_turn"] == false
            && self.data["turns"]
                .as_object()
                .is_some_and(|turns| turns.is_empty())
            && self.data["prepared"]
                .as_object()
                .is_some_and(|prepared| prepared.is_empty())
    }

    pub(crate) fn binding(&self) -> &Value {
        &self.data["binding"]
    }

    pub(crate) fn snapshot(dir: &Path, binding: &Value) -> io::Result<Value> {
        let data = read_evidence(&dir.join("manager-proof.json"))?;
        if data["schema"] != 1 || data["binding"] != *binding || data["blocked"] != false {
            return Err(refusal("native manager evidence unavailable"));
        }
        Ok(data)
    }

    pub(crate) fn prepare(
        &mut self,
        operation: &str,
        nonce: &str,
        text: &str,
        external: u64,
    ) -> io::Result<()> {
        if self.data["blocked"] != false
            || self.data["external_input"] != external
            || !is_hash(nonce)
            || !valid(operation)
            || text.is_empty()
        {
            return Err(refusal("unqualified native manager admission"));
        }
        let key = json!({"operation_id":operation,"text_hash":codex_receipt::hash(text),"external_input":external});
        if let Some(prior) = self.data["prepared"].get(nonce) {
            if prior == &key {
                return Ok(());
            }
            return Err(refusal("manager input nonce already binds another effect"));
        }
        if self.data["prepared"]
            .as_object()
            .unwrap()
            .values()
            .any(|row| row["operation_id"] == operation)
        {
            return Err(refusal("manager operation already binds another nonce"));
        }
        let mut next = self.data.clone();
        next["prepared"][nonce] = key;
        self.commit(next)
    }

    pub(crate) fn observe(&mut self, message: &Value) -> io::Result<()> {
        let params = &message["params"];
        let thread = field(&self.data["binding"], "native_session_id");
        if message["method"] == "thread/started" {
            if params["thread"]["id"] == thread && params["thread"]["status"]["type"] == "idle" {
                let mut next = self.data.clone();
                next["thread_state"] = json!("idle");
                self.commit(next)?;
            }
            return Ok(());
        }
        if params["threadId"] != thread {
            return Ok(());
        }
        let mut next = self.data.clone();
        match field(message, "method") {
            "thread/status/changed" => {
                let status = field(&params["status"], "type");
                next["thread_state"] = json!(status);
                if !matches!(status, "idle" | "active") {
                    next["blocked"] = json!(true);
                }
            }
            "turn/started" => {
                let turn = field(&params["turn"], "id");
                let parent = field(&params["turn"], "rootTurnId");
                if !parent.is_empty() && parent != turn {
                    return Ok(());
                }
                next["observed_turn"] = json!(true);
                if !valid(turn)
                    || next["turns"][turn]["closed"] == true
                    || next["current"].as_str().is_some_and(|prior| {
                        prior != turn && next["turns"][prior]["closed"] == false
                    })
                {
                    next["blocked"] = json!(true);
                } else if next["turns"][turn].is_null() {
                    next["turns"][turn] = json!({"closed":false});
                    next["current"] = json!(turn);
                    next["thread_state"] = json!("active");
                }
            }
            "turn/completed" => {
                let turn = field(&params["turn"], "id");
                let parent = field(&params["turn"], "rootTurnId");
                if !parent.is_empty() && parent != turn {
                    return Ok(());
                }
                if valid(turn) {
                    next["observed_turn"] = json!(true);
                }
                if next["current"] == turn && !next["turns"][turn].is_null() {
                    next["turns"][turn]["closed"] = json!(true);
                    next["turns"][turn]["reason"] = params["turn"]["status"].clone();
                }
            }
            "item/started" | "item/completed" => {
                let item = &params["item"];
                let turn = field(params, "turnId");
                let id = field(item, "id");
                match field(item, "type") {
                    "userMessage" if valid(id) => observe_user(&mut next, turn, id, item),
                    "mcpToolCall"
                        if item["server"] == "swarm_voice"
                            && matches!(
                                field(item, "tool"),
                                "publish"
                                    | "swarm_read_note"
                                    | "swarm_reply"
                                    | "swarm_read_attachment"
                                    | "swarm_create"
                                    | "swarm_status"
                            )
                            && valid(id) =>
                    {
                        if message["method"] == "item/completed" {
                            if !next["calls"][id].is_null() {
                                next["calls"][id]["closed"] = json!(true);
                            }
                        } else if next["current"] == turn && next["turns"][turn]["closed"] == false
                        {
                            let mut arguments = item["arguments"].clone();
                            arguments.sort_all_objects();
                            let argument_hash = canonical_hash(&arguments)?;
                            let candidate = json!({"turn":turn,"tool":item["tool"],"arguments_hash":argument_hash,"closed":false});
                            if next["calls"][id].is_null() {
                                next["calls"][id] = candidate;
                            } else if next["calls"][id] != candidate {
                                next["blocked"] = json!(true);
                            }
                        }
                    }
                    _ => return Ok(()),
                }
            }
            _ => return Ok(()),
        }
        if next != self.data {
            self.commit(next)?;
        }
        Ok(())
    }

    fn commit(&mut self, next: Value) -> io::Result<()> {
        let existing: usize = ["prepared", "turns", "calls", "messages"]
            .iter()
            .map(|key| next[*key].as_object().unwrap().len())
            .sum();
        if existing > MAX_RECORDS {
            return Err(refusal("manager evidence exceeds its capacity"));
        }
        save_evidence(&self.file, &next)?;
        self.data = next;
        Ok(())
    }
}

fn read_evidence(file: &Path) -> io::Result<Value> {
    let durable = runtime::read_json(file)?;
    let evidence = &durable["evidence"];
    if durable["digest"] != canonical_hash(evidence)? || !evidence.is_object() {
        return Err(refusal(
            "native manager evidence failed its integrity check",
        ));
    }
    Ok(evidence.clone())
}

fn save_evidence(file: &Path, evidence: &Value) -> io::Result<()> {
    runtime::write_json(
        file,
        &json!({"evidence":evidence,"digest":canonical_hash(evidence)?}),
    )
}

pub(crate) fn input_status(evidence: &Value, nonce: &str) -> Value {
    let coordinate = evidence["turns"]
        .as_object()
        .into_iter()
        .flat_map(|turns| turns.iter())
        .find(|(_, turn)| turn["nonce"] == nonce);
    let (turn, state) = match coordinate {
        Some((turn, entry)) if entry["closed"] == true => (turn.as_str(), "completed"),
        Some((turn, _)) => (turn.as_str(), "running"),
        None if !evidence["prepared"][nonce].is_null() => ("", "prepared"),
        None => ("", "unknown"),
    };
    let mut result = json!({"native_input_id":nonce,"native_turn_id":turn,
        "state":state,"external_input":evidence["external_input"]});
    if evidence["externally_steered"] == true {
        result["externally_steered"] = json!(true);
    }
    result
}

pub(crate) fn resolve(evidence: &Value, input: &Value) -> io::Result<Value> {
    let binding = &evidence["binding"];
    let meta = &input["host_meta"];
    let coordinates = crate::discord::provenance::coordinates(binding, meta)?;
    let call = &evidence["calls"][coordinates.call];
    let turn = field(call, "turn");
    let receipt = &evidence["turns"][turn];
    let nonce = field(receipt, "nonce");
    if evidence["blocked"] != false
        || evidence["externally_steered"] == true
        || input["binding"] != *binding
        || field(input, "tool_name") != call["tool"].as_str().unwrap_or("publish")
        || call["closed"] != false
        || call["arguments_hash"] != input["arguments_hash"]
        || match binding["provider"].as_str() {
            Some("claude") => coordinates.turn.is_some() || !call["agent"].is_null(),
            _ => coordinates.turn != Some(turn),
        }
        || evidence["current"] != turn
        || receipt["closed"] != false
        || !is_hash(nonce)
    {
        return Err(refusal(
            "native manager tool call has no exact active admission",
        ));
    }
    Ok(
        json!({"native_input_id":nonce,"native_turn_id":turn,"native_call_id":coordinates.call,"main":true}),
    )
}

fn observe_user(next: &mut Value, turn: &str, id: &str, item: &Value) {
    let client = field(item, "clientId");
    let text = item["content"]
        .as_array()
        .filter(|items| items.len() == 1 && items[0]["type"] == "text")
        .and_then(|items| items[0]["text"].as_str());
    let witness = json!([turn, client, text.map(codex_receipt::hash)]);
    if !next["messages"][id].is_null() {
        if next["messages"][id] != witness {
            next["blocked"] = json!(true);
        }
        return;
    }
    next["messages"][id] = witness;
    let pending = &next["prepared"][client];
    if !is_hash(client)
        || text.is_none_or(|text| pending["text_hash"] != codex_receipt::hash(text))
        || pending["external_input"] != next["external_input"]
        || next["current"] != turn
        || next["turns"][turn]["closed"] != false
        || next["turns"]
            .as_object()
            .unwrap()
            .values()
            .any(|prior| prior["nonce"] == client)
        || !next["turns"][turn]["nonce"].is_null()
    {
        next["blocked"] = json!(true);
        next["external_input"] = json!(
            next["external_input"]
                .as_u64()
                .unwrap_or_default()
                .saturating_add(1)
        );
        return;
    }
    next["turns"][turn]["nonce"] = json!(client);
}

fn valid(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 256
        && value.trim() == value
        && !value.chars().any(char::is_control)
}

fn is_hash(value: &str) -> bool {
    value.len() == 64
        && value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

fn canonical_hash(value: &Value) -> io::Result<String> {
    let body = serde_json::to_string(value)?
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    Ok(format!("{:x}", Sha256::digest(body.as_bytes())))
}

fn refusal(reason: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, reason)
}
