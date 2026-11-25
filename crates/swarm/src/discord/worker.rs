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

pub(super) fn source_text(row: &Value) -> &str {
    row["addressed_text"]
        .as_str()
        .unwrap_or_else(|| field(row, "text"))
}

pub(super) fn target_id(target: &str) -> bool {
    target_text(target) == Some((target, ""))
}

pub(super) fn fresh(message: &str, now: u64) -> bool {
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

fn matching_provider_receipt(
    provider: &str,
    operation: &str,
    composer: &Value,
    receipt: &Value,
) -> bool {
    if receipt["agent"] != provider
        || receipt["operation_id"] != operation
        || receipt["session"] != composer["session"]
        || receipt["session_instance"] != composer["session_instance"]
        || receipt["origin"] != composer["origin"]
    {
        return false;
    }
    let native = field(receipt, "native_session_id");
    let generation = field(receipt, "native_generation");
    let state = field(receipt, "state");
    let bounded = |value: &str| {
        !value.is_empty()
            && value.len() <= 512
            && value.trim() == value
            && !value.chars().any(char::is_control)
    };
    let admission = &receipt["native_admission"];
    match provider {
        "agy" => {
            crate::providers::codex_receipt::canonical_thread(native)
                && crate::supervisor::runtime::valid_id(generation)
                && matches!(state, "submitted" | "rejected")
                && admission.is_null()
                && receipt["native_turn_id"].is_null()
                && receipt["native_refusal_code"].is_null()
        }
        "hermes" => {
            bounded(native)
                && bounded(generation)
                && receipt["native_generation"] == composer["session_instance"]
                && match state {
                    "submitted" => {
                        receipt["native_refusal_code"].is_null()
                            && admission["input_id"] == operation
                            && admission["ref"]["session_id"] == native
                            && bounded(field(&admission["ref"], "profile_id"))
                            && bounded(field(admission, "authority_epoch"))
                            && bounded(field(admission, "admission_id"))
                            && admission["execution_generation"].is_u64()
                            && matches!(
                                field(admission, "status"),
                                "queued" | "started" | "terminal"
                            )
                    }
                    "rejected" => admission.is_null() && receipt["native_refusal_code"].is_i64(),
                    _ => false,
                }
        }
        _ => false,
    }
}

pub(super) struct Flight {
    pub(super) operation: String,
    pub(super) join: std::thread::JoinHandle<()>,
}

struct Background<'a> {
    client: Option<Client>,
    running: &'a mut Option<Flight>,
}

impl Transport for Background<'_> {
    fn target(&mut self, id: &str) -> io::Result<(String, String)> {
        self.client.as_mut().unwrap().target(id)
    }
    fn status(&mut self, operation: &str, composer: &Value) -> io::Result<Value> {
        self.client.as_mut().unwrap().status(operation, composer)
    }
    fn send(&mut self, operation: &str, composer: &Value) -> io::Result<()> {
        let mut client = self.client.take().unwrap();
        let operation = operation.to_owned();
        let composer = composer.clone();
        let saved = operation.clone();
        *self.running = Some(Flight {
            operation: saved,
            join: std::thread::spawn(move || {
                let _ = Transport::send(&mut client, &operation, &composer);
            }),
        });
        Ok(())
    }
}

pub(super) fn tick(
    cfg: &Config,
    inbox: &mut Inbox,
    api: &Discord,
    read_after: &mut Option<String>,
    running: &mut Option<Flight>,
) -> io::Result<()> {
    if running.is_some() || !inbox.has_worker_work() {
        return Ok(());
    }
    let mut caps = CAPS.to_vec();
    if cfg.text("mode") != "personal" {
        caps.push("worker-discord-guild-origin-v1");
    }
    let Ok(transport) = Client::dial(Path::new(cfg.text("daemon_socket")), &caps) else {
        return Ok(());
    };
    if cfg.check_supervisor(&transport, &caps).is_err() {
        return Ok(());
    }
    if cfg.text("mode") == "personal" {
        let mut transport = transport;
        return dispatch(
            cfg,
            inbox,
            &mut transport,
            spawn::now_ms(),
            read_after,
            |_, id| api.owner_message(cfg, id),
        );
    }
    let mut transport = Background {
        client: Some(transport),
        running,
    };
    dispatch(
        cfg,
        inbox,
        &mut transport,
        spawn::now_ms(),
        read_after,
        |route, id| {
            if cfg.text("mode") == "personal" {
                api.owner_message(cfg, id)
            } else {
                super::guild::current(
                    api,
                    cfg,
                    field(route, "guild_id"),
                    field(route, "channel_id"),
                    id,
                )
            }
        },
    )
}

fn dispatch(
    cfg: &Config,
    inbox: &mut Inbox,
    transport: &mut impl Transport,
    now: u64,
    read_after: &mut Option<String>,
    current: impl FnOnce(&Value, &str) -> io::Result<Option<Value>>,
) -> io::Result<()> {
    if let Some((operation, provider, composer)) = inbox.unsettled_worker(read_after.as_deref()) {
        *read_after = Some(operation.clone());
        if let Ok(receipt) = transport.status(&operation, &composer)
            && matching_provider_receipt(&provider, &operation, &composer, &receipt)
        {
            inbox.settle_worker(&operation, &receipt)?;
        }
    }
    let Some(prepared) = inbox.next_worker() else {
        return Ok(());
    };
    let source_id = field(&prepared["route"], "message_id");
    if !fresh(source_id, now) {
        return inbox.withdraw(source_id);
    }
    let Some(source) =
        current(&prepared["route"], source_id)?.filter(|source| source["id"] == source_id)
    else {
        return inbox.withdraw(source_id);
    };
    if cfg.text("mode") != "personal"
        && (source["guild_id"] != prepared["route"]["guild_id"]
            || source["channel_id"] != prepared["route"]["channel_id"]
            || field(&source, "verified_parent_id") != field(&prepared["route"], "parent_id"))
    {
        return inbox.withdraw(source_id);
    }
    let seen = if cfg.text("mode") == "personal" {
        inbox.admit(&source)?
    } else {
        inbox.admit_guild(cfg, &source)?
    };
    let Some(prepared) = seen else {
        return inbox.withdraw(source_id);
    };
    let Some((target, text)) = target_text(source_text(&prepared)) else {
        return Ok(());
    };
    if text.is_empty() || text.len() > 4096 || inbox.worker_excluded(target) {
        return inbox.withdraw(source_id);
    }
    let Ok((provider, instance)) = transport.target(target) else {
        return Ok(());
    };
    let mut composer = json!({
        "session":target, "session_instance":instance, "text":text,
        "origin":{
            "kind":"owner_direct","installation_id":cfg.text("installation_id"),
            "actor_id":cfg.text("owner_id"),"requester_id":cfg.text("owner_id"),
            "channel_id":prepared["route"]["channel_id"],"message_id":source_id,
            "request_id":prepared["operation_id"],"revision":prepared["revision"]
        }
    });
    if cfg.text("mode") != "personal" {
        composer["origin"]["guild_id"] = prepared["route"]["guild_id"].clone();
    }
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
        malformed: bool,
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

        fn status(&mut self, operation: &str, composer: &Value) -> io::Result<Value> {
            self.answer
                .then(|| json!({"state":"submitted","agent":"agy",
                    "native_session_id":if self.malformed { "" } else { "11111111-1111-4111-8111-111111111111" },"native_generation":"a".repeat(24),
                    "operation_id":operation,"session":composer["session"],"session_instance":composer["session_instance"],"origin":composer["origin"]}))
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
    fn provider_receipt_requires_the_native_identity_and_kind_of_evidence() {
        let composer =
            json!({"session":TARGET,"session_instance":"act","origin":{"request_id":"source"}});
        let base = json!({"agent":"agy","state":"submitted","operation_id":"op","session":TARGET,
            "session_instance":"act","origin":composer["origin"]});
        assert!(!matching_provider_receipt("agy", "op", &composer, &base));
        let mut agy = base;
        agy["native_session_id"] = json!("11111111-1111-4111-8111-111111111111");
        agy["native_generation"] = json!("a".repeat(24));
        assert!(matching_provider_receipt("agy", "op", &composer, &agy));
        assert!(!matching_provider_receipt("hermes", "op", &composer, &agy));
        agy["native_turn_id"] = json!("foreign-proof");
        assert!(!matching_provider_receipt("agy", "op", &composer, &agy));
        let mut hermes = agy;
        hermes["agent"] = json!("hermes");
        hermes["native_generation"] = json!("act");
        hermes["native_turn_id"] = Value::Null;
        assert!(!matching_provider_receipt(
            "hermes", "op", &composer, &hermes
        ));
        hermes["native_admission"] = json!({"input_id":"op","admission_id":"native-admission","authority_epoch":"epoch",
            "execution_generation":0,"status":"queued","ref":{"profile_id":"native-profile","session_id":hermes["native_session_id"]}});
        assert!(matching_provider_receipt(
            "hermes", "op", &composer, &hermes
        ));
        hermes["native_admission"]["ref"]["session_id"] = json!("otherwise");
        assert!(!matching_provider_receipt(
            "hermes", "op", &composer, &hermes
        ));
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
            malformed: false,
        };
        dispatch(&cfg, &mut inbox, &mut client, now, &mut None, |_, id| {
            assert_eq!(owner["id"], id);
            Ok(Some(owner.clone()))
        })
        .unwrap();
        assert_eq!(client.sends, 1);
        assert_eq!(inbox.worker_status()[0]["state"], "unconfirmed");
        let op = inbox.worker_status()[0]["operation_id"].clone();
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        dispatch(&cfg, &mut inbox, &mut client, now, &mut None, |_, _| {
            panic!("lost write replayed")
        })
        .unwrap();
        assert_eq!(client.sends, 1);
        client.answer = true;
        client.malformed = true;
        dispatch(&cfg, &mut inbox, &mut client, now, &mut None, |_, _| {
            panic!("malformed receipt triggered a new write")
        })
        .unwrap();
        assert_eq!(inbox.worker_status()[0]["state"], "unconfirmed");
        let disk = runtime::read_json(&dir.join("rust-text-v1.json")).unwrap();
        assert_eq!(
            disk["messages"][owner["id"].as_str().unwrap()]["state"],
            "sending"
        );
        client.malformed = false;
        let mut edit = owner.clone();
        edit["content"] = json!(format!("{TARGET} changed after cutover"));
        edit["edited_timestamp"] = json!("2026-11-25T01:01:01Z");
        assert!(inbox.admit(&edit).unwrap().is_none());
        client.answer = true;
        dispatch(&cfg, &mut inbox, &mut client, now, &mut None, |_, _| {
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
            malformed: false,
        };
        let stale = message(now - WORKER_EVENT_MAX_AGE_MS - 1, "expired source");
        inbox.admit(&stale).unwrap();
        dispatch(&cfg, &mut inbox, &mut client, now, &mut None, |_, _| {
            panic!("expired source reached REST")
        })
        .unwrap();
        let exclude = message(now, "excluded source");
        inbox.admit(&exclude).unwrap();
        inbox.set_worker_excluded(TARGET, true).unwrap();
        dispatch(&cfg, &mut inbox, &mut client, now, &mut None, |_, _| {
            Ok(Some(exclude))
        })
        .unwrap();
        inbox.set_worker_excluded(TARGET, false).unwrap();
        let deleting = message(now + 1, "deleted source");
        inbox.admit(&deleting).unwrap();
        dispatch(&cfg, &mut inbox, &mut client, now, &mut None, |_, _| {
            Ok(None)
        })
        .unwrap();
        assert_eq!(client.sends, 0);
        assert!(inbox.worker_status().as_array().unwrap().is_empty());
        fs::remove_dir_all(dir).unwrap();
    }
}
