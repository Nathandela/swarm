//! Route explicitly addressed owner messages to one ordinary native session incarnation.
use super::{
    config::{Config, invalid},
    receipt::Inbox,
    rest::Discord,
};
use crate::ipc::{Client, field, spawn};
use serde_json::{Value, json};
use std::io::{self, Read};
use std::path::Path;

const CAPS: &[&str] = &[
    "semantic-send",
    "semantic-target",
    "message-origin",
    "worker-discord-origin-v1",
];
const DISCORD_EPOCH_MS: u64 = 1_420_070_400_000;
const WORKER_EVENT_MAX_AGE_MS: u64 = 15 * 60 * 1000;

pub(super) fn target_text(text: &str) -> Option<(&str, &str)> {
    let text = text.trim();
    let (target, tail) = text.split_once(char::is_whitespace).unwrap_or((text, ""));
    let (endpoint, local) = target.split_once('/')?;
    let hex = endpoint.strip_prefix("ep-")?;
    if hex.len() != 8
        || local.len() != 24
        || !hex
            .bytes()
            .chain(local.bytes())
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return None;
    }
    Some((target, tail.trim()))
}

pub(super) fn target_id(target: &str) -> bool {
    target_text(target) == Some((target, ""))
}

fn fresh(message: &str, now: u64) -> bool {
    let Some(id) = message.parse::<u64>().ok() else {
        return false;
    };
    let created = DISCORD_EPOCH_MS + (id >> 22);
    created <= now.saturating_add(120_000) && now.saturating_sub(created) <= WORKER_EVENT_MAX_AGE_MS
}

fn operation() -> io::Result<String> {
    let mut entropy = [0u8; 16];
    std::fs::File::open("/dev/urandom")?.read_exact(&mut entropy)?;
    let key: String = entropy.iter().map(|byte| format!("{byte:02x}")).collect();
    Ok(format!("semantic:{}:{key}", spawn::now_ms()))
}

pub(super) trait Transport {
    fn target(&mut self, id: &str) -> io::Result<(String, String)>;
    fn send(&mut self, operation: &str, composer: &Value) -> io::Result<()>;
    fn status(&mut self, operation: &str, composer: &Value) -> io::Result<Value>;
}

impl Transport for Client {
    fn target(&mut self, id: &str) -> io::Result<(String, String)> {
        if !id.starts_with(&format!("{}/", self.endpoint())) {
            return Err(invalid("worker belongs to a different supervisor"));
        }
        let row = self
            .sessions()?
            .into_iter()
            .find(|row| row["id"] == id && row["endpoint_id"] == self.endpoint())
            .ok_or_else(|| invalid("worker session is unavailable"))?;
        let provider = field(&row, "agent");
        if !matches!(provider, "agy" | "hermes")
            || !matches!(row["role"].as_str(), None | Some("" | "worker"))
            || row["status"]["Process"] != "running"
        {
            return Err(invalid("target is not a running ordinary Discord worker"));
        }
        let reply = self.request("semantic_target", Some(id), "semantic_target")?;
        let instance = field(&reply, "semantic_target_instance");
        if reply["endpoint_id"] != self.endpoint()
            || reply["session_id"] != id
            || instance.is_empty()
            || instance.len() > 256
            || instance.chars().any(char::is_control)
            || row["capabilities"]["session_instance"]
                .as_str()
                .is_some_and(|seen| seen != instance)
        {
            return Err(invalid("worker incarnation could not be authenticated"));
        }
        Ok((provider.to_owned(), instance.to_owned()))
    }

    fn send(&mut self, operation: &str, composer: &Value) -> io::Result<()> {
        let reply = self.request_body(
            json!({"op":"semantic_send","body_version":1,
            "session_id":composer["session"],"operation_id":operation,"composer_send":composer}),
            "ok",
        )?;
        if reply["endpoint_id"] != self.endpoint() || reply["operation_id"] != operation {
            return Err(invalid("worker acknowledgement does not match operation"));
        }
        Ok(())
    }

    fn status(&mut self, operation: &str, composer: &Value) -> io::Result<Value> {
        let reply = self.request_body(
            json!({"op":"semantic_status","session_id":composer["session"],
            "operation_id":operation}),
            "semantic_status",
        )?;
        let receipt = &reply["semantic_receipt"];
        if reply["endpoint_id"] != self.endpoint()
            || reply["operation_id"] != operation
            || receipt["operation_id"] != operation
            || receipt["session"] != composer["session"]
            || receipt["session_instance"] != composer["session_instance"]
            || receipt["origin"] != composer["origin"]
        {
            return Err(invalid(
                "worker receipt does not match the bound operation and origin",
            ));
        }
        Ok(receipt.clone())
    }
}

pub(super) fn tick(cfg: &Config, inbox: &mut Inbox, api: &Discord) -> io::Result<()> {
    if !inbox.has_worker_work() {
        return Ok(());
    }
    let Ok(mut transport) = Client::dial(Path::new(cfg.text("daemon_socket")), CAPS) else {
        return Ok(());
    };
    if cfg.check_supervisor(&transport, CAPS).is_err() {
        return Ok(());
    }
    dispatch(cfg, inbox, &mut transport, spawn::now_ms(), |id| {
        api.owner_message(cfg, id)
    })
}

fn dispatch(
    cfg: &Config,
    inbox: &mut Inbox,
    transport: &mut impl Transport,
    now: u64,
    current: impl FnOnce(&str) -> io::Result<Option<Value>>,
) -> io::Result<()> {
    for (operation, composer) in inbox.unsettled_workers() {
        if let Ok(receipt) = transport.status(&operation, &composer) {
            match field(&receipt, "state") {
                "native_accepted" | "admitted" | "submitted" | "rejected" => {
                    inbox.settle_worker(&operation, &receipt)?
                }
                _ => {}
            }
        }
    }
    let Some(prepared) = inbox.next_worker() else {
        return Ok(());
    };
    let source_id = field(&prepared["route"], "message_id");
    if !fresh(source_id, now) {
        return inbox.withdraw(source_id);
    }
    let Some(source) = current(source_id)?.filter(|source| source["id"] == source_id) else {
        return inbox.withdraw(source_id);
    };
    let Some(prepared) = inbox.admit(&source)? else {
        return inbox.withdraw(source_id);
    };
    let Some((target, text)) = target_text(field(&prepared, "text")) else {
        return Ok(());
    };
    if text.is_empty() || text.len() > 4096 || inbox.worker_excluded(target) {
        return inbox.withdraw(source_id);
    }
    let Ok((provider, instance)) = transport.target(target) else {
        return Ok(());
    };
    let composer = json!({
        "session":target, "session_instance":instance, "text":text,
        "origin":{
            "kind":"owner_direct","installation_id":cfg.text("installation_id"),
            "actor_id":cfg.text("owner_id"),"requester_id":cfg.text("owner_id"),
            "channel_id":cfg.text("dm_channel_id"),"message_id":source_id,
            "request_id":prepared["operation_id"],"revision":prepared["revision"]
        }
    });
    let operation = operation()?;
    inbox.cutover_worker(&operation, source_id, &provider, &composer)?;
    // The durable cutover has one writer. An uncorrelated/lost acknowledgement
    // is reconciled by readback on a new connection, never by another write.
    let _ = transport.send(&operation, &composer);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::fs;
    use std::path::PathBuf;

    const TARGET: &str = "ep-1234abcd/1234567890abcdef12345678";

    struct Provider {
        path: PathBuf,
        sends: usize,
        answer: bool,
    }

    impl Transport for Provider {
        fn target(&mut self, id: &str) -> io::Result<(String, String)> {
            assert_eq!(id, TARGET);
            Ok(("agy".into(), "actual-native-incarnation".into()))
        }

        fn send(&mut self, operation: &str, composer: &Value) -> io::Result<()> {
            self.sends += 1;
            let state = runtime::read_json(&self.path.join("rust-text-v1.json"))?;
            let row = &state["worker_inputs"][operation];
            assert_eq!(row["composer"], *composer);
            assert_eq!(row["provider"], "agy");
            let id = field(row, "source_id");
            assert_eq!(state["messages"][id]["worker_operation"], operation);
            assert_eq!(state["messages"][id]["state"], "sending");
            assert!(state["messages"][id]["native_binding"].is_null());
            Err(io::Error::other("fixture lost native reply"))
        }

        fn status(&mut self, _operation: &str, _composer: &Value) -> io::Result<Value> {
            self.answer
                .then(|| json!({"state":"submitted","native_session_id":"native-session"}))
                .ok_or_else(|| io::Error::other("fixture receipt unavailable"))
        }
    }

    fn root() -> PathBuf {
        let root =
            std::path::Path::new("/tmp").join(format!("worker-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        root
    }

    fn config() -> Config {
        Config(
            json!({"installation_id":"install","application_id":"200","owner_id":"300",
              "dm_channel_id":"400","daemon_socket":"/fixture/supervisor","mode":"personal"}),
        )
    }

    fn message(now: u64, body: &str) -> Value {
        let id = ((now - DISCORD_EPOCH_MS) << 22).to_string();
        json!({"id":id,"channel_id":"400","content":format!("{TARGET} {body}"),"type":0,
               "author":{"id":"300"},"attachments":[]})
    }

    #[test]
    fn native_worker_cutover_keeps_its_incarnation_and_never_redrives_a_lost_reply() {
        let dir = root();
        let cfg = config();
        let now = spawn::now_ms();
        let owner = message(now, "run the checks");
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("100").unwrap();
        inbox.admit(&owner).unwrap();
        assert!(
            inbox.next_prepared().is_none(),
            "explicit worker input must not reach the manager"
        );
        let mut client = Provider {
            path: dir.clone(),
            sends: 0,
            answer: false,
        };
        dispatch(&cfg, &mut inbox, &mut client, now, |id| {
            assert_eq!(owner["id"], id);
            Ok(Some(owner.clone()))
        })
        .unwrap();
        assert_eq!(client.sends, 1);
        assert_eq!(inbox.worker_status()[0]["state"], "unconfirmed");
        let op = inbox.worker_status()[0]["operation_id"].clone();
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        dispatch(&cfg, &mut inbox, &mut client, now, |_| {
            panic!("lost write replayed")
        })
        .unwrap();
        assert_eq!(client.sends, 1);
        let mut edit = owner.clone();
        edit["content"] = json!(format!("{TARGET} changed after cutover"));
        edit["edited_timestamp"] = json!("2026-11-25T01:01:01Z");
        assert!(inbox.admit(&edit).unwrap().is_none());
        client.answer = true;
        dispatch(&cfg, &mut inbox, &mut client, now, |_| {
            panic!("edit re-authorized the old operation")
        })
        .unwrap();
        assert_eq!(inbox.worker_status()[0]["operation_id"], op);
        assert_eq!(inbox.worker_status()[0]["state"], "submitted");
        assert_eq!(client.sends, 1);
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn excluded_expired_and_deleted_worker_sources_have_no_native_effect() {
        let dir = root();
        let cfg = config();
        let now = spawn::now_ms();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("100").unwrap();
        let mut client = Provider {
            path: dir.clone(),
            sends: 0,
            answer: false,
        };
        let stale = message(now - WORKER_EVENT_MAX_AGE_MS - 1, "expired source");
        inbox.admit(&stale).unwrap();
        dispatch(&cfg, &mut inbox, &mut client, now, |_| {
            panic!("expired source reached REST")
        })
        .unwrap();
        let exclude = message(now, "excluded source");
        inbox.admit(&exclude).unwrap();
        inbox.set_worker_excluded(TARGET, true).unwrap();
        dispatch(&cfg, &mut inbox, &mut client, now, |_| Ok(Some(exclude))).unwrap();
        inbox.set_worker_excluded(TARGET, false).unwrap();
        let deleting = message(now + 1, "deleted source");
        inbox.admit(&deleting).unwrap();
        dispatch(&cfg, &mut inbox, &mut client, now, |_| Ok(None)).unwrap();
        assert_eq!(client.sends, 0);
        assert!(inbox.worker_status().as_array().unwrap().is_empty());
        fs::remove_dir_all(dir).unwrap();
    }
}
