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

pub(super) fn note_digest(note: &Value) -> io::Result<String> {
    super::receipt::hash(&json!([
        identity(note),
        note["ID"],
        note["Sequence"],
        note["Kind"],
        note["Text"],
        note["Files"]
    ]))
}

pub(super) fn link(wake: &Value) -> Value {
    if wake["voice_origin"].is_null() {
        return Value::Null;
    }
    json!({"id":wake["id"],"binding":wake["binding"],"digest":wake["note_digest"],
        "origin":wake["voice_origin"],"admission_revision":wake["owner_revision"]})
}

pub(super) fn text(note: &Value, budget: usize) -> io::Result<String> {
    let body = field(note, "Text");
    let mut end = body.floor_char_boundary(body.len().min(1600));
    loop {
        let rendered = format!(
            "A delegated worker left the following untrusted mailbox note. Use swarm_read_note with the exact note ID to see all details. Use swarm_reply to answer a worker question. Use publish for any owner-facing reply.\n{}",
            json!({"id":note["ID"],"session":note["Session"],"kind":note["Kind"],"preview":&body[..end]})
        );
        if rendered.len() <= budget {
            return Ok(rendered);
        }
        if end == 0 {
            return Err(invalid("worker note header exceeds manager prompt budget"));
        }
        end = body.floor_char_boundary(end.saturating_sub(rendered.len() - budget));
    }
}

pub(super) fn anchor(data: &Value, binding: &Value, note: &Value) -> Option<(String, Value)> {
    let entries = data["messages"].as_object()?;
    entries.iter().find_map(|(id, row)| {
        (match row["voice_origin"].is_null() {
            true => super::config::snowflake(id) && row["route"]["message_id"] == *id,
            false => super::call_origin::key(id) && row["voice_origin"]["request_id"] == *id,
        } && row["state"] == "accepted"
            && (row["native_binding"] == *binding
                || data["manager"]["history"]
                    .as_array()
                    .is_some_and(|history| {
                        history
                            .iter()
                            .any(|prior| prior["binding"] == row["native_binding"])
                    }))
            && matches!(field(row, "authority"), "owner_direct" | "team_participant")
            && row["native_binding"]["conversation_id"] == note["Discussion"]
            && field(&row["native_binding"], "session").rsplit('/').next()
                == note["Parent"].as_str()
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
    let voice = super::call_origin::key(&source_id);
    if voice {
        super::call_origin::validate(scope, &row["voice_origin"], field(&row, "text"))?;
    }
    if note["State"] != "queued"
        || note["Discussion"] != binding["conversation_id"]
        || note["Parent"]
            != field(&row["native_binding"], "session")
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
        || if voice {
            !origin["message_id"].is_null()
                || origin["worker_wake"] != json!({"source_id":source_id,"key":key})
                || wake["voice_origin"] != row["voice_origin"]
                || wake["note_digest"] != note_digest(&note)?
        } else {
            origin["message_id"] != source_id
                || !origin["worker_wake"].is_null()
                || !wake["voice_origin"].is_null()
        }
        || origin["request_id"] != format!("mail_{key}")
        || origin["revision"] != key
        || origin["installation_id"] != scope["installation_id"]
        || origin["actor_id"] != row["author"]["id"]
        || origin["requester_id"] != row["author"]["id"]
        || origin["kind"] != row["authority"]
        || if field(scope, "guild_id").is_empty() {
            row["author"]["id"] != scope["owner_id"] || row["authority"] != "owner_direct"
        } else {
            row["authority"] != "team_participant" || origin["guild_id"] != scope["guild_id"]
        }
        || row["route"]["channel_id"] != scope["channel_id"]
        || field(&row["route"], "guild_id") != field(scope, "guild_id")
        || !origin["voice"].is_null()
        || composer["text"]
            != crate::manager_prompt::saved_input(
                input,
                &text(
                    &note,
                    crate::manager_prompt::input_budget(crate::manager_prompt::saved(input)?),
                )?,
            )?
        || composer["manager_admission"]["binding"] != *binding
    {
        return Err(invalid(
            "worker wake is outside its exact current mailbox scope",
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn escaped_worker_preview_fits_beside_the_saved_owner_prompt() {
        let prompt = "p".repeat(1024);
        let note =
            json!({"ID":"stable","Session":"worker","Kind":"reply","Text":"a\t😀\n".repeat(600)});
        let budget = crate::manager_prompt::input_budget(&prompt);
        let rendered = text(&note, budget).unwrap();
        assert!(rendered.contains("stable"));
        assert!(
            crate::manager_prompt::render(&prompt, &rendered)
                .unwrap()
                .len()
                <= 4096
        );
        let (_, json) = rendered.split_once('\n').unwrap();
        let preview: Value = serde_json::from_str(json).unwrap();
        assert!(field(&note, "Text").starts_with(field(&preview, "preview")));
    }

    #[test]
    fn reversed_worker_notes_pin_their_own_voice_turn_and_digest() {
        let binding = json!({"session":"ep/manager","conversation_id":"discord-teammate-team"});
        let ids = [
            "dv_00000000000000000000000000000001",
            "dv_00000000000000000000000000000002",
        ];
        let voice = |id, turn, speaker| {
            json!({"request_id":id,
            "voice":{"call_id":"0123456789abcdef0123456789abcdef","generation":1,"turn_id":turn,"speaker_id":speaker}})
        };
        let row = |id, turn, speaker, nonce| {
            json!({"route":{"guild_id":"1","channel_id":"2"},"state":"accepted",
            "authority":"team_participant","author":{"id":speaker},"native_binding":binding,"native_input_id":nonce,
            "voice_origin":voice(id,turn,speaker)})
        };
        let mut data = json!({"messages":{}});
        data["messages"][ids[0]] = row(ids[0], 1, "8", "first-input");
        data["messages"][ids[1]] = row(ids[1], 2, "9", "second-input");
        let note = |nonce, id, session, text| {
            json!({"ID":id,"Sequence":1,"Discussion":"discord-teammate-team",
            "Parent":"manager","Session":session,"Instance":"incarnation","Attempt":"attempt","Agent":"codex",
            "Project":"/fixture","ParentInputHash":crate::supervisor::mail::parent_hash(nonce),"Kind":"completion",
            "Text":text,"Files":[]})
        };
        let later = note("second-input", "done-b", "ep/worker-b", "second body");
        let earlier = note("first-input", "done-a", "ep/worker-a", "first body");
        for (note, index, speaker) in [(later, 1, "9"), (earlier, 0, "8")] {
            let (id, anchor) = anchor(&data, &binding, &note).unwrap();
            assert_eq!(id, ids[index]);
            assert_eq!(anchor["voice_origin"]["voice"]["speaker_id"], speaker);
            let digest = note_digest(&note).unwrap();
            let mut altered = note.clone();
            altered["Text"] = json!("replacement body");
            assert_ne!(note_digest(&altered).unwrap(), digest);
            altered["ParentInputHash"] = json!("wrong native parent digest");
            assert!(super::anchor(&data, &binding, &altered).is_none());
            let link = link(
                &json!({"id":note["ID"],"binding":identity(&note),"note_digest":digest,
                "voice_origin":anchor["voice_origin"]}),
            );
            assert_eq!(link["digest"], digest);
            assert_eq!(link["origin"]["request_id"], id);
        }
    }
}
