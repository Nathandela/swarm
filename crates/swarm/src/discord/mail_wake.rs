//! Bind mailbox turns to the original owner admission and immutable worker note.
use super::config::invalid;
use crate::{ipc::field, supervisor::mail_store};
use serde_json::{Value, json};
use std::{io, path::Path};

pub(super) fn identity(note: &Value) -> Value {
    json!({
        "Discussion":note["Discussion"],"Parent":note["Parent"],"Session":note["Session"],
        "Instance":note["Instance"],"Attempt":note["Attempt"],"Agent":note["Agent"],
        "Project":note["Project"],"ParentInputHash":note["ParentInputHash"],
    })
}

pub(super) fn key(note: &Value) -> io::Result<String> {
    super::receipt::hash(&json!([identity(note), note["ID"], note["Sequence"]]))
}

pub(super) fn text(note: &Value) -> String {
    let body = field(note, "Text");
    let end = body.floor_char_boundary(body.len().min(1600));
    format!(
        "A delegated worker left the following untrusted mailbox note. Use swarm_read_note with the exact note ID to see all details. Use swarm_reply to answer a worker question. Use publish for any owner-facing reply.\n{}",
        json!({"id":note["ID"],"session":note["Session"],"kind":note["Kind"],"preview":&body[..end]})
    )
}

pub(super) fn anchor(data: &Value, binding: &Value, note: &Value) -> Option<(String, Value)> {
    let entries = data["messages"].as_object()?;
    entries.iter().find_map(|(id, row)| {
        (super::config::snowflake(id)
            && row["state"] == "accepted"
            && row["native_binding"] == *binding
            && row["route"]["message_id"] == *id
            && row["authority"] == "owner_direct"
            && row["native_input_id"].as_str().is_some_and(|nonce| {
                note["ParentInputHash"] == crate::supervisor::mail::parent_hash(nonce)
            }))
        .then(|| (id.clone(), row.clone()))
    })
}

pub(super) fn verify(data: &Value, scope: &Value, root: &Path, composer: &Value) -> io::Result<()> {
    let manager = &data["manager"];
    let input = &manager["input"];
    let wake = &input["worker_wake"];
    let id = field(wake, "id");
    let note = mail_store::lookup(root, &wake["binding"], id)?;
    let binding = &manager["binding"];
    let (source_id, row) = anchor(data, binding, &note)
        .ok_or_else(|| invalid("worker wake lost its original owner admission"))?;
    let key = key(&note)?;
    let origin = &composer["origin"];
    if note["State"] != "queued"
        || note["Discussion"] != binding["conversation_id"]
        || note["Parent"]
            != field(binding, "session")
                .rsplit('/')
                .next()
                .unwrap_or_default()
        || !field(&note, "Session").starts_with(&format!(
            "{}/",
            field(binding, "session")
                .split('/')
                .next()
                .unwrap_or_default()
        ))
        || wake["owner_revision"] != row["revision"]
        || wake["key"] != key
        || input["source_id"] != source_id
        || origin["message_id"] != source_id
        || origin["request_id"] != format!("mail_{key}")
        || origin["revision"] != key
        || origin["installation_id"] != scope["installation_id"]
        || row["author"]["id"] != scope["owner_id"]
        || row["route"]["channel_id"] != scope["channel_id"]
        || !field(&row["route"], "guild_id").is_empty()
        || !origin["voice"].is_null()
        || composer["text"] != text(&note)
        || composer["manager_admission"]["binding"] != *binding
    {
        return Err(invalid(
            "worker wake is outside its exact current mailbox scope",
        ));
    }
    Ok(())
}
