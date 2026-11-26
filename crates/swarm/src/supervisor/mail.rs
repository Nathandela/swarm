//! Owner-only worker mailbox and dispatcher launch scope.
use super::{State, mail_store};
use crate::{ipc::field, teammate};
use hmac::{Hmac, Mac};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{io, path::Path};
use subtle::ConstantTimeEq;

pub(crate) fn proof(token: &str, purpose: &str) -> String {
    let mut mac =
        Hmac::<Sha256>::new_from_slice(token.as_bytes()).expect("HMAC accepts any key length");
    mac.update(purpose.as_bytes());
    format!("{:x}", mac.finalize().into_bytes())
}

fn refused(detail: &str) -> io::Error {
    io::Error::other(detail.to_owned())
}

pub(super) fn launch_scope(
    state: &State,
    launch: &Value,
    caps: &[&str],
    operation: &str,
) -> io::Result<Option<Value>> {
    let claim = field(launch, "dispatcher_proof");
    if claim.is_empty() {
        return Ok(None);
    }
    if !caps.contains(&teammate::MAIL_CAP)
        || launch["spawn_intent"] != "delegate"
        || operation.is_empty()
        || launch["worktree"] == true
        || launch["options"].get("resume_from").is_some()
    {
        return Err(refused(
            "worker mailbox requires an exact delegated owner operation",
        ));
    }
    let parent = field(launch, "spawned_from");
    let full = if parent.contains('/') {
        parent.to_owned()
    } else {
        format!("{}/{}", state.endpoint, parent)
    };
    let session = state.session(&json!({"session_id":full}))?;
    let mut scope = session.authorize_mail_child(&full, claim, Path::new(field(launch, "cwd")))?;
    scope["attempt"] = json!(operation);
    Ok(Some(scope))
}

pub(super) fn request(state: &State, request: &Value) -> io::Result<Value> {
    let session = state.session(request)?;
    let input = &request["worker_mail"];
    let full = field(request, "session_id");
    let binding = session.mail_binding(full, field(input, "proof"))?;
    let root = state
        .root
        .parent()
        .ok_or_else(|| refused("missing mailbox supervisor root"))?;
    let id = field(input, "id");
    if input.as_object().is_none_or(|fields| {
        fields.keys().any(|key| {
            !matches!(
                key.as_str(),
                "action" | "id" | "proof" | "text" | "kind" | "paths"
            )
        })
    }) || !input["paths"].is_null()
    {
        return Err(refused(
            "worker files require a compatible mailbox supervisor",
        ));
    }
    let note = match field(input, "action") {
        "message" => mail_store::submit(
            root,
            &binding,
            id,
            field(input, "kind"),
            field(input, "text"),
            crate::providers::codex_receipt::timely(id),
        )?,
        "receipt" => mail_store::lookup(root, &binding, id)?,
        "reply" => mail_store::settle(root, &binding, id, None, true)?,
        _ => return Err(refused("invalid worker mailbox action")),
    };
    let mut receipt = json!({"id":id,"state":note["State"]});
    if input["action"] == "reply" && note["State"] != "superseded" {
        if let Some(answer) = note["Answer"].as_str().filter(|text| !text.is_empty()) {
            receipt["state"] = json!("available");
            receipt["text"] = json!(answer);
        } else {
            receipt["state"] = json!("pending");
        }
    }
    Ok(json!({"op":"worker_mail","session_id":full,"worker_mail_receipt":receipt}))
}

pub(super) fn matches_binding(manager: &Value, token: &str, proof_token: &str) -> bool {
    !manager.is_null()
        && proof_token.len() == 64
        && bool::from(
            proof(token, "spawn")
                .as_bytes()
                .ct_eq(proof_token.as_bytes()),
        )
}

pub(crate) fn parent_hash(value: &str) -> String {
    format!("{:x}", Sha256::digest(value.as_bytes()))
}
