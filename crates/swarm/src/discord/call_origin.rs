//! Stable voice admission keys and scope checks at the native boundary.
use super::config::invalid;
use super::receipt::hash;
use serde_json::{Value, json};
use std::io;

pub(super) fn key(id: &str) -> bool {
    id.strip_prefix("dv_").is_some_and(|id| hex(id, 32))
}

fn hex(id: &str, len: usize) -> bool {
    id.len() == len
        && id
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

pub(super) fn origin(
    scope: &Value,
    call: &str,
    generation: u64,
    turn: u64,
    text: &str,
) -> io::Result<Value> {
    let body_hash = hash(&json!(text))?;
    let voice = json!({
        "call_id":call, "generation":generation, "turn_id":turn, "body_hash":body_hash,
        "speaker_id":scope["owner_id"], "supervisor_socket":scope["supervisor_socket"],
    });
    let id = operation(&scope["installation_id"], &voice)?;
    let conversation = scope["launch"]["manager"]["conversation_id"]
        .as_str()
        .unwrap_or_default();
    let discussion = conversation
        .strip_prefix("discord-teammate-")
        .ok_or_else(|| invalid("voice discussion has no binding"))?;
    let origin = json!({
        "kind":"owner_direct","installation_id":scope["installation_id"],"discussion_id":discussion,
        "actor_id":scope["owner_id"],"requester_id":scope["owner_id"],"channel_id":scope["channel_id"],
        "request_id":id,"revision":body_hash,"voice":voice,
    });
    validate(scope, &origin, text)?;
    Ok(origin)
}

fn operation(installation: &Value, voice: &Value) -> io::Result<String> {
    Ok(format!(
        "dv_{}",
        &hash(&json!([installation, voice["call_id"], voice["turn_id"]]))?[..32]
    ))
}

pub(super) fn validate(scope: &Value, origin: &Value, text: &str) -> io::Result<()> {
    let voice = &origin["voice"];
    let conversation = scope["launch"]["manager"]["conversation_id"]
        .as_str()
        .unwrap_or_default();
    if !voice.as_object().is_some_and(|fields| fields.len() == 6)
        || !voice["call_id"].as_str().is_some_and(|id| hex(id, 32))
        || !voice["generation"]
            .as_u64()
            .is_some_and(|generation| generation > 0)
        || !voice["turn_id"]
            .as_u64()
            .is_some_and(|turn| turn > 0 && turn <= 256)
        || voice["speaker_id"] != scope["owner_id"]
        || voice["supervisor_socket"] != scope["supervisor_socket"]
        || origin["installation_id"] != scope["installation_id"]
        || origin["kind"] != "owner_direct"
        || origin["actor_id"] != scope["owner_id"]
        || origin["requester_id"] != scope["owner_id"]
        || origin["channel_id"] != scope["channel_id"]
        || !origin["message_id"].is_null()
        || !origin["guild_id"].is_null()
        || origin["request_id"] != operation(&scope["installation_id"], voice)?
        || conversation.strip_prefix("discord-teammate-") != origin["discussion_id"].as_str()
        || voice["body_hash"] != origin["revision"]
        || voice["body_hash"] != hash(&json!(text))?
        || text.trim().is_empty()
        || text.len() > 4096
        || text.contains('\0')
    {
        return Err(invalid("voice origin does not match the scoped transcript"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::discord::{config::Config, receipt::Inbox};
    use crate::supervisor::runtime;

    #[test]
    fn exact_durable_cutover_still_requires_live_room() {
        use std::io::{Read, Write};
        let dir = std::path::Path::new("/tmp")
            .join(format!("v-{}", &runtime::random_id().unwrap()[..12]));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"mode":"personal","installation_id":"fixture","owner_id":"4",
            "application_id":"3","dm_channel_id":"6","daemon_socket":"/tmp/scoped.sock"}),
        );
        let scope = json!({"installation_id":"fixture","owner_id":"4","application_id":"3",
            "channel_id":"6","supervisor_socket":"/tmp/scoped.sock",
            "launch":{"manager":{"conversation_id":"discord-teammate-dt_fixture","auth_file":dir.join("manager-v1/bridge.auth")}}});
        let text = "owner marker";
        let origin = origin(&scope, "0123456789abcdef0123456789abcdef", 8, 1, text).unwrap();
        let id = origin["request_id"].as_str().unwrap();
        let binding = json!({"provider":"codex","native_session_id":"fixture-native-session"});
        let composer = json!({"session":"s","text":text,"origin":origin,
            "manager_admission":{"binding":binding,"nonce":"a".repeat(64)}});
        let state = json!({"generation":1,"launch":scope["launch"],"binding":binding,"session":"s",
            "input":{"operation":"attempt","source_id":id,"composer":composer}});
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("10").unwrap();
        inbox.admit_voice(&scope, origin.clone(), text).unwrap();
        inbox
            .cutover(id, origin["revision"].as_str().unwrap(), state)
            .unwrap();
        assert!(
            crate::discord::verify_connector_input(&scope, "another-attempt", &composer).is_err()
        );
        assert_eq!(
            crate::discord::verify_connector_input(&scope, "attempt", &composer)
                .unwrap_err()
                .to_string(),
            crate::discord::call_runtime::WITHDRAWN
        );
        let listener = runtime::bind(&dir.join("call-auth.sock")).unwrap();
        let server = {
            let origin = origin.clone();
            std::thread::spawn(move || {
                let (mut client, _) = listener.accept().unwrap();
                let mut bytes = Vec::new();
                client.read_to_end(&mut bytes).unwrap();
                assert_eq!(serde_json::from_slice::<Value>(&bytes).unwrap(), origin);
                client.write_all(b"y").unwrap();
            })
        };
        crate::discord::verify_connector_input(&scope, "attempt", &composer).unwrap();
        server.join().unwrap();
        assert_eq!(
            crate::discord::verify_connector_input(&scope, "attempt", &composer)
                .unwrap_err()
                .to_string(),
            crate::discord::call_runtime::WITHDRAWN
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn admission_reopens_with_same_receipt_and_conflicting_transcript_fails() {
        let dir = std::env::temp_dir().join(format!("swarm-typed-call-{}", std::process::id()));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"mode":"personal","installation_id":"voice-fixture","owner_id":"4",
            "application_id":"3","dm_channel_id":"6","daemon_socket":"/tmp/scoped.sock"}),
        );
        let scope = json!({"installation_id":"voice-fixture","owner_id":"4","application_id":"3",
            "channel_id":"6","supervisor_socket":"/tmp/scoped.sock",
            "launch":{"manager":{"conversation_id":"discord-teammate-dt_fixture"}}});
        let call_id = "0123456789abcdef0123456789abcdef";
        let original = origin(&scope, call_id, 1, 2, "the marker is <fox>").unwrap();
        let altered = origin(&scope, call_id, 1, 2, "the marker is not fox").unwrap();
        assert_eq!(original["request_id"], altered["request_id"]);
        assert_ne!(original["revision"], altered["revision"]);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        assert!(
            inbox
                .admit_voice(&scope, original.clone(), "the marker is <fox>")
                .is_err()
        );
        inbox.checkpoint("100").unwrap();
        let receipt = inbox
            .admit_voice(&scope, original.clone(), "the marker is <fox>")
            .unwrap();
        assert!(inbox.next_prepared().is_none());
        assert_eq!(inbox.prepared_voice().unwrap(), receipt);
        assert_eq!(
            runtime::read_json(&dir.join("rust-text-v1.json")).unwrap()["version"],
            3
        );
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        assert_eq!(
            inbox
                .admit_voice(&scope, original.clone(), "the marker is <fox>")
                .unwrap(),
            receipt
        );
        assert!(
            inbox
                .admit_voice(&scope, altered, "the marker is not fox")
                .is_err()
        );
        let mut wrong_scope = scope.clone();
        wrong_scope["supervisor_socket"] = json!("/tmp/another.sock");
        assert!(
            inbox
                .admit_voice(&wrong_scope, original, "the marker is <fox>")
                .is_err()
        );
        std::fs::remove_dir_all(dir).unwrap();
    }
}
