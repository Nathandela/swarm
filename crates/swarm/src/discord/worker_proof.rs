//! Guild authority stays with the connected bot. The shim never reads its token.
use super::{
    config::{Config, Place, invalid},
    guild, worker_input,
};
use crate::{ipc::field, socket::DeadlineReader, supervisor::runtime};
use serde_json::{Value, json};
use std::os::unix::{
    fs::{FileTypeExt, MetadataExt},
    net::UnixStream,
};
use std::{
    fs, io,
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};

const SOCKET: &str = "g";
const EPOCH: &str = "guild-worker-epoch.json";
const PROOF_TIMEOUT: Duration = Duration::from_secs(7);

pub(super) struct Service {
    path: PathBuf,
    socket_identity: (u64, u64),
    stop: Arc<AtomicBool>,
}

impl Service {
    pub(super) fn bind(
        place: &Path,
        supervisor: PathBuf,
        cfg: Config,
        mut fetch: impl FnMut(&Config, &str, &str, &str) -> io::Result<Option<Value>> + Send + 'static,
    ) -> io::Result<Self> {
        let path = place.join(SOCKET);
        runtime::reclaim_socket(&path)?;
        let listener = runtime::bind(&path)?;
        let meta = fs::symlink_metadata(&path)?;
        listener.set_nonblocking(true)?;
        invalidate(place, None)?;
        let stop = Arc::new(AtomicBool::new(false));
        let running = stop.clone();
        let dir = place.to_owned();
        std::thread::spawn(move || {
            while !running.load(Ordering::Acquire) {
                if let Ok((mut stream, _)) = listener.accept() {
                    let deadline = Instant::now() + PROOF_TIMEOUT;
                    let request = runtime::read_control(&mut DeadlineReader(&stream, deadline));
                    if let Ok(request) = request {
                        let result = authorize(&dir, &supervisor, &cfg, &request, &mut fetch);
                        if let Ok(authorized) = result
                            && !running.load(Ordering::Acquire)
                            && Instant::now() < deadline
                        {
                            let _ = stream.set_write_timeout(Some(Duration::from_secs(1)));
                            let _ = runtime::write_control(
                                &mut stream,
                                &json!({"challenge":request["challenge"],"epoch":request["epoch"],"authorized":authorized}),
                            );
                        }
                    }
                } else {
                    std::thread::sleep(Duration::from_millis(10));
                }
            }
        });
        Ok(Self {
            path,
            socket_identity: (meta.dev(), meta.ino()),
            stop,
        })
    }
}

impl Drop for Service {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        if fs::symlink_metadata(&self.path)
            .is_ok_and(|meta| (meta.dev(), meta.ino()) == self.socket_identity)
        {
            let _ = invalidate(self.path.parent().unwrap(), None);
            let _ = fs::remove_file(&self.path);
        }
    }
}

pub(super) fn invalidate(place: &Path, operation: Option<&str>) -> io::Result<()> {
    let path = place.join(EPOCH);
    let mut state = match runtime::read_json(&path) {
        Ok(state) if state["cancelled"].is_object() => state,
        Ok(_) => return Err(invalid("damaged guild cancellation ledger")),
        Err(err) if err.kind() == io::ErrorKind::NotFound => json!({"cancelled":{}}),
        Err(err) => return Err(err),
    };
    if let Some(operation) = operation {
        let cancelled = state["cancelled"].as_object_mut().unwrap();
        if !cancelled.contains_key(operation) && cancelled.len() >= 2048 {
            return Err(invalid("guild cancellation ledger exceeded capacity"));
        }
        cancelled.insert(operation.to_owned(), json!(true));
    }
    state["epoch"] = json!(runtime::random_id()?);
    runtime::write_json(&path, &state)
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum Invalidation {
    None,
    Refresh,
    Cancel,
}

pub(super) fn invalidates(cfg: &Config, source: &Value, event: &Value) -> Invalidation {
    use Invalidation::{Cancel, None, Refresh};
    let route = &source["route"];
    let guild = field(route, "guild_id");
    let channel = field(route, "channel_id");
    let parent = field(route, "parent_id");
    let message = field(route, "message_id");
    let body = &event["d"];
    if guild.is_empty()
        || channel.is_empty()
        || message.is_empty()
        || body["guild_id"]
            .as_str()
            .is_some_and(|actual| actual != guild)
    {
        return None;
    }
    let recipient = |id: &str| {
        !id.is_empty() && (id == cfg.text("owner_id") || id == cfg.text("application_id"))
    };
    let channel_id = field(body, "channel_id");
    let id = field(body, "id");
    let affected_channel = || !id.is_empty() && (id == channel || id == parent);
    match field(event, "t") {
        "MESSAGE_UPDATE" if channel_id == channel && id == message => {
            if body
                .get("content")
                .is_some_and(|text| text.is_string() && *text != source["text"])
                || body
                    .get("edited_timestamp")
                    .is_some_and(|edited| !edited.is_null() && *edited != source["edited_at"])
                || body["attachments"]
                    .as_array()
                    .is_some_and(|items| !items.is_empty())
            {
                Cancel
            } else {
                Refresh
            }
        }
        "MESSAGE_DELETE" if channel_id == channel && id == message => Cancel,
        "MESSAGE_DELETE_BULK"
            if channel_id == channel
                && body["ids"]
                    .as_array()
                    .is_some_and(|ids| ids.iter().any(|id| id == message)) =>
        {
            Cancel
        }
        "CHANNEL_UPDATE" | "THREAD_UPDATE" if affected_channel() => Refresh,
        "CHANNEL_DELETE" | "THREAD_DELETE" if affected_channel() => Cancel,
        "GUILD_UPDATE" if id == guild => Refresh,
        "GUILD_DELETE" if id == guild && body["unavailable"] == true => Refresh,
        "GUILD_DELETE" if id == guild => Cancel,
        "GUILD_MEMBER_UPDATE"
            if body["guild_id"] == guild
                && (recipient(field(&body["user"], "id"))
                    || field(&body["user"], "id").is_empty()) =>
        {
            Refresh
        }
        "GUILD_MEMBER_REMOVE"
            if body["guild_id"] == guild && recipient(field(&body["user"], "id")) =>
        {
            Cancel
        }
        "THREAD_MEMBER_UPDATE"
            if id == channel
                && (recipient(field(body, "user_id")) || field(body, "user_id").is_empty()) =>
        {
            Refresh
        }
        "THREAD_MEMBERS_UPDATE" if id == channel => {
            if body["removed_member_ids"]
                .as_array()
                .is_some_and(|ids| ids.iter().filter_map(Value::as_str).any(recipient))
            {
                Cancel
            } else if body["added_members"]
                .as_array()
                .is_some_and(|members| members.iter().any(|item| recipient(field(item, "user_id"))))
                || body["added_members"].is_null() && body["removed_member_ids"].is_null()
            {
                Refresh
            } else {
                None
            }
        }
        "GUILD_ROLE_UPDATE" | "GUILD_ROLE_DELETE" if body["guild_id"] == guild => {
            let role = body["role"]["id"]
                .as_str()
                .or_else(|| body["role_id"].as_str())
                .unwrap_or_default();
            // An unclassified role event can invalidate a snapshot, never terminalize a source.
            if role.is_empty()
                || role == guild
                || source["audience_roles"]
                    .as_array()
                    .is_none_or(|roles| roles.iter().any(|id| id == role))
            {
                Refresh
            } else {
                None
            }
        }
        _ => None,
    }
}

fn epoch(place: &Path, operation: &str) -> io::Result<Value> {
    let state = runtime::read_json(&place.join(EPOCH))?;
    if !state["cancelled"].is_object()
        || !state["cancelled"][operation].is_null()
        || !state["epoch"].as_str().is_some_and(runtime::valid_id)
    {
        return Err(invalid(
            "guild worker operation is cancelled or lacks a live proof epoch",
        ));
    }
    Ok(state["epoch"].clone())
}

fn authorize(
    place: &Path,
    supervisor: &Path,
    cfg: &Config,
    request: &Value,
    mut fetch: impl FnMut(&Config, &str, &str, &str) -> io::Result<Option<Value>>,
) -> io::Result<bool> {
    let refused = || invalid("guild audience is no longer authorized");
    if request["epoch"] != epoch(place, field(request, "operation"))?
        || !request["challenge"].as_str().is_some_and(runtime::valid_id)
        || request["installation"] != cfg.text("installation_id")
    {
        return Err(refused());
    }
    let verify = || {
        worker_input::verify_connector_worker_input(
            supervisor,
            cfg.text("daemon_socket"),
            cfg.text("installation_id"),
            field(request, "provider"),
            field(request, "operation"),
            &request["composer"],
        )
    };
    // The shim holds the selection and ledger locks. This path only reads them.
    verify()?;
    let ledger = runtime::read_json(&place.join("rust-text-v1.json"))?;
    let origin = &request["composer"]["origin"];
    let id = field(origin, "message_id");
    let guild = field(origin, "guild_id");
    let channel = field(origin, "channel_id");
    let prior = &ledger["messages"][id];
    let fetched = fetch(cfg, guild, channel, id)?;
    if request["epoch"] != epoch(place, field(request, "operation"))? {
        return Err(refused());
    }
    verify()?;
    let Some(seen) = fetched else {
        return Ok(false);
    };
    if seen["id"] != id
        || seen["guild_id"] != guild
        || seen["channel_id"] != channel
        || seen["author"]["id"] != cfg.text("owner_id")
        || seen["author"].get("bot").is_some_and(|bot| bot != false)
        || seen["author"]
            .get("system")
            .is_some_and(|system| system != false)
        || seen["type"] != 0
        || !seen["webhook_id"].is_null()
        || !seen["message_reference"].is_null()
        || !seen["attachments"].as_array().is_some_and(Vec::is_empty)
        || seen["message_snapshots"]
            .as_array()
            .is_some_and(|rows| !rows.is_empty())
        || seen["content"] != prior["text"]
        || seen["edited_timestamp"] != prior["edited_at"]
        || field(&seen, "verified_parent_id") != field(&prior["route"], "parent_id")
        || guild::address(cfg, &seen).as_deref() != prior["addressed_text"].as_str()
    {
        return Ok(false);
    }
    Ok(true)
}

fn recheck_response(
    place: &Place,
    installation: &str,
    provider: &str,
    operation: &str,
    composer: &Value,
) -> io::Result<bool> {
    let socket = place.dir.join(SOCKET);
    let meta = fs::symlink_metadata(&socket)?;
    // SAFETY: geteuid requires no arguments.
    if !meta.file_type().is_socket()
        || meta.nlink() != 1
        || meta.mode() & 0o777 != 0o600
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(invalid("unsafe guild authority socket"));
    }
    let expected = epoch(&place.dir, operation)?;
    let challenge = runtime::random_id()?;
    let mut stream = UnixStream::connect(socket)?;
    stream.set_write_timeout(Some(Duration::from_secs(1)))?;
    runtime::write_control(
        &mut stream,
        &json!({"epoch":expected,"challenge":challenge,
        "installation":installation,"provider":provider,"operation":operation,"composer":composer}),
    )?;
    let reply =
        runtime::read_control(&mut DeadlineReader(&stream, Instant::now() + PROOF_TIMEOUT))?;
    if reply["epoch"] != expected
        || reply["challenge"] != challenge
        || epoch(&place.dir, operation)? != expected
    {
        return Err(invalid(
            "guild authority was invalidated before the native effect",
        ));
    }
    reply["authorized"]
        .as_bool()
        .ok_or_else(|| invalid("missing guild authority verdict"))
}

pub(super) fn recheck(
    place: &Place,
    installation: &str,
    provider: &str,
    operation: &str,
    composer: &Value,
) -> io::Result<()> {
    match recheck_response(place, installation, provider, operation, composer) {
        Ok(true) => Ok(()),
        Ok(false) => Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "guild audience definitively refused the native effect",
        )),
        Err(_) => Err(io::Error::new(
            io::ErrorKind::Interrupted,
            "guild authority check did not authorize the native effect",
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::discord::receipt::Inbox;
    use std::sync::atomic::AtomicU8;

    #[test]
    fn gateway_invalidation_is_bound_to_the_source_and_its_actual_audience() {
        use Invalidation::{Cancel, None, Refresh};
        let cfg = Config(json!({"application_id":"200","owner_id":"300"}));
        let mut source = json!({"route":{"guild_id":"10","channel_id":"12","parent_id":"11","message_id":"90"},
            "audience_roles":["10","210","310"]});
        let decide =
            |source: &Value, name, data| invalidates(&cfg, source, &json!({"t":name,"d":data}));
        for (kind, data) in [
            ("MESSAGE_UPDATE", json!({"channel_id":"80","id":"90"})),
            (
                "MESSAGE_UPDATE",
                json!({"guild_id":"20","channel_id":"12","id":"90"}),
            ),
            (
                "MESSAGE_UPDATE",
                json!({"guild_id":"10","channel_id":"12","id":"91"}),
            ),
            (
                "MESSAGE_DELETE_BULK",
                json!({"guild_id":"10","channel_id":"12","ids":["91"]}),
            ),
            ("CHANNEL_DELETE", json!({"guild_id":"10","id":"13"})),
            ("GUILD_DELETE", json!({"id":"20"})),
            (
                "GUILD_ROLE_UPDATE",
                json!({"guild_id":"10","role":{"id":"299"}}),
            ),
            (
                "GUILD_MEMBER_REMOVE",
                json!({"guild_id":"10","user":{"id":"301"}}),
            ),
            (
                "GUILD_MEMBER_REMOVE",
                json!({"guild_id":"20","user":{"id":"300"}}),
            ),
            (
                "THREAD_MEMBERS_UPDATE",
                json!({"guild_id":"10","id":"12","removed_member_ids":["301"]}),
            ),
            (
                "THREAD_MEMBERS_UPDATE",
                json!({"guild_id":"10","id":"13","removed_member_ids":["300"]}),
            ),
        ] {
            assert_eq!(decide(&source, kind, data), None, "{kind}");
        }
        for (kind, data) in [
            (
                "MESSAGE_UPDATE",
                json!({"guild_id":"10","channel_id":"12","id":"90"}),
            ),
            ("CHANNEL_UPDATE", json!({"guild_id":"10","id":"11"})),
            (
                "GUILD_ROLE_UPDATE",
                json!({"guild_id":"10","role":{"id":"210"}}),
            ),
            (
                "GUILD_MEMBER_UPDATE",
                json!({"guild_id":"10","user":{"id":"200"}}),
            ),
            ("THREAD_MEMBERS_UPDATE", json!({"guild_id":"10","id":"12"})),
        ] {
            assert_eq!(decide(&source, kind, data), Refresh, "{kind}");
        }
        for (kind, data) in [
            (
                "MESSAGE_UPDATE",
                json!({"guild_id":"10","channel_id":"12","id":"90","content":"new body"}),
            ),
            (
                "MESSAGE_DELETE",
                json!({"guild_id":"10","channel_id":"12","id":"90"}),
            ),
            (
                "GUILD_MEMBER_REMOVE",
                json!({"guild_id":"10","user":{"id":"300"}}),
            ),
            (
                "THREAD_MEMBERS_UPDATE",
                json!({"guild_id":"10","id":"12","removed_member_ids":["200"]}),
            ),
        ] {
            assert_eq!(decide(&source, kind, data), Cancel, "{kind}");
        }
        source
            .as_object_mut()
            .unwrap()
            .shift_remove("audience_roles");
        assert_eq!(
            decide(
                &source,
                "GUILD_ROLE_UPDATE",
                json!({"guild_id":"10","role":{"id":"299"}})
            ),
            Refresh
        );
    }

    #[test]
    fn effect_socket_denies_revoked_membership_and_gateway_cancellation() {
        let root =
            std::path::Path::new("/tmp").join(format!("proof-{}", runtime::random_id().unwrap()));
        let place = root.join("discord");
        runtime::private_dir(&place).unwrap();
        let socket = root.join("daemon.sock");
        let cfg = Config(
            json!({"mode":"teammate","version":1,"enabled":true,"name":"fixture",
            "installation_id":"install","application_id":"200","owner_id":"300","manager_cli":"codex",
            "daemon_socket":socket,"work_dir":root,"token_file":root.join("unused"),"guild_ids":["10"],"channel_ids":["11"]}),
        );
        runtime::write_json(&place.join("config.json"), &cfg.0).unwrap();
        runtime::write_json(&place.join(".rust-offline-owner.json"), &json!("install")).unwrap();
        let mut inbox = Inbox::open(&place, &cfg).unwrap();
        inbox.checkpoint("").unwrap();
        let now = crate::ipc::spawn::now_ms();
        let id = ((now - 1_420_070_400_000) << 22).to_string();
        let target = "ep-1234abcd/1234567890abcdef12345678";
        let seen = json!({"id":id,"guild_id":"10","channel_id":"12","verified_parent_id":"11",
            "author":{"id":"300"},"content":format!("<@200> {target} inspect"),"mentions":[{"id":"200"}],
            "type":0,"attachments":[],"audience_verified_at_ms":now});
        let source = inbox.admit_guild(&cfg, &seen).unwrap().unwrap();
        let composer = json!({"session":target,"session_instance":"instance","text":"inspect","origin":{
            "kind":"owner_direct","installation_id":"install","actor_id":"300","guild_id":"10","channel_id":"12",
            "message_id":id,"request_id":source["operation_id"],"revision":source["revision"]}});
        let operation = format!("semantic:{now}:{}", "a".repeat(32));
        inbox
            .cutover_worker(&operation, &id, "agy", &composer)
            .unwrap();
        let mode = Arc::new(AtomicU8::new(0));
        let api = mode.clone();
        let changed = place.clone();
        let service = Service::bind(
            &place,
            root.clone(),
            cfg,
            move |_, guild, channel, message| {
                assert_eq!((guild, channel, message), ("10", "12", field(&seen, "id")));
                match api.load(Ordering::Acquire) {
                    0 => Ok(None),
                    1 => {
                        invalidate(&changed, None)?;
                        Ok(Some(seen.clone()))
                    }
                    _ => Ok(Some(seen.clone())),
                }
            },
        )
        .unwrap();
        let check = || {
            worker_input::lock_connector_worker_input(
                &root,
                socket.to_str().unwrap(),
                "install",
                "agy",
                &operation,
                &composer,
            )
        };
        assert_eq!(check().unwrap_err().kind(), io::ErrorKind::PermissionDenied);
        mode.store(1, Ordering::Release);
        assert_eq!(check().unwrap_err().kind(), io::ErrorKind::Interrupted);
        mode.store(2, Ordering::Release);
        assert!(check().is_ok());
        invalidate(&place, Some(&operation)).unwrap();
        assert!(
            check().is_err(),
            "a fresh proof request cannot reauthorize a cancelled operation"
        );
        drop(service);
        assert!(check().is_err());
        fs::remove_dir_all(root).unwrap();
    }
}
