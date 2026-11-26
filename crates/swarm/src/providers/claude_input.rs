//! Private bridge for Claude's prompt queue and its independent lifecycle guard.
use super::{agy_input, claude, codex_proof, codex_receipt as receipt};
use crate::{ipc::field, supervisor::runtime};
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    fs::{self, File},
    io::{self, BufRead, BufReader, Read, Write},
    os::unix::{fs::DirBuilderExt, net::UnixStream},
    path::{Path, PathBuf},
    sync::{Arc, Condvar, Mutex},
    thread,
    time::{Duration, Instant},
};
use subtle::ConstantTimeEq;

const VERSION: &str = "2.1.329";
const SENDER: &str = "swarm-native-input";
const HEARTBEAT: Duration = Duration::from_secs(2);
const HANDOFF: Duration = Duration::from_secs(3);
const NATIVE_SUBMIT: &str = "const pending = $.prompt.submit({ text: message.text });";
const TIMED_SUBMIT: &str = r#"if (!Number.isSafeInteger(message.expires_ms) || Date.now() >= message.expires_ms) {
          await request($, '/exchange', { operation_id: message.operation_id, state: 'uncertain' });
          return;
        }
        const pending = $.prompt.submit({ text: message.text });"#;

pub(crate) fn qualified(version: &str) -> bool {
    version == VERSION
}

#[derive(Clone, PartialEq, serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Worker {
    native_session_id: String,
    generation: String,
    version: String,
}

#[derive(Default)]
struct Joined {
    sender: Option<(Worker, Instant)>,
    guard: Option<(Worker, Instant)>,
    revoked: bool,
    proof: Option<codex_proof::Ledger>,
    manager_initial_idle: bool,
    operations: BTreeMap<String, Pending>,
}

impl Joined {
    fn ready(&self) -> Option<&str> {
        let (sender, heard) = self.sender.as_ref()?;
        let (guard, guard_heard) = self.guard.as_ref()?;
        (!self.revoked
            && sender.native_session_id == guard.native_session_id
            && sender.generation != guard.generation
            && heard.elapsed() < HEARTBEAT
            && guard_heard.elapsed() < HEARTBEAT)
            .then_some(sender.native_session_id.as_str())
    }
}

struct Pending {
    composer: Value,
    intent: String,
    result: Value,
    claimed: bool,
    hold: Option<(File, File, File)>,
}

pub(crate) struct Broker {
    dir: PathBuf,
    supervisor: PathBuf,
    owner_socket: String,
    installation: String,
    token: String,
    auth: String,
    generation: String,
    session: String,
    manager: Value,
    sender_name: String,
    joined: Mutex<Joined>,
    progress: Condvar,
}

impl Broker {
    pub(crate) fn prepare(
        dir: &Path,
        config: &Value,
        owner_socket: &str,
        argv: &mut Vec<String>,
    ) -> io::Result<Arc<Self>> {
        let provider = &config["provider"];
        let token = field(config, "token");
        let manager = &config["manager"];
        let installation = if manager.is_null() {
            field(provider, "claude_native_installation")
        } else {
            field(manager, "installation_id")
        };
        let supervisor = dir.parent().and_then(Path::parent).ok_or_else(refused)?;
        if provider["name"] != "claude"
            || !qualified(field(provider, "version"))
            || if manager.is_null() {
                !crate::discord::worker_installation_enrolled(
                    supervisor,
                    owner_socket,
                    installation,
                )
            } else {
                manager["launch"]["agent"] != "claude"
                    || manager["supervisor_socket"] != owner_socket
                    || crate::discord::connector_intent(manager).is_err()
            }
        {
            return Err(refused());
        }
        let mut random = [0; 48];
        File::open("/dev/urandom")?.read_exact(&mut random)?;
        let hex = |bytes: &[u8]| bytes.iter().map(|b| format!("{b:02x}")).collect::<String>();
        let generation = hex(&random[..16]);
        let auth = format!("Bearer {}", hex(&random[16..]));
        let socket = dir.join("cl");
        let descriptor = dir.join("claude-input.json");
        let descriptor_js = serde_json::to_string(&descriptor)?;
        let listener = runtime::bind(&socket)?;
        let sender_name = if manager.is_null() {
            SENDER.to_owned()
        } else {
            format!("swarm-manager-input-{}", runtime::random_id()?)
        };
        runtime::write_json(
            &descriptor,
            &json!({
                "socket_path":socket,"token":auth.strip_prefix("Bearer "),
                "generation":generation,"provider_version":VERSION,"sender_name":sender_name,
                "manager_conversation_id":manager["launch"]["manager"]["conversation_id"],
                "native_turns":!manager.is_null(),
            }),
        )?;
        for (name, source) in [
            (
                sender_name.as_str(),
                include_str!("../../assets/claude/plugin.js"),
            ),
            (
                "swarm-native-input-guard",
                include_str!("../../assets/claude/guard.js"),
            ),
        ] {
            let plugin = dir.join(name);
            for part in [".claude-plugin", "hooks", "types"] {
                fs::DirBuilder::new()
                    .mode(0o700)
                    .recursive(true)
                    .create(plugin.join(part))?;
            }
            runtime::write_json(
                &plugin.join(".claude-plugin/plugin.json"),
                &json!({"name":name,"version":"1.1.0","description":"Swarm native input","types":"./types/index.d.ts"}),
            )?;
            runtime::write_json(
                &plugin.join("hooks/hooks.json"),
                &json!({"modules":["./input.js"]}),
            )?;
            let mut body = source.replace("__CONFIG_PATH__", &descriptor_js);
            if name == sender_name {
                if body.matches(NATIVE_SUBMIT).count() != 1 {
                    return Err(refused());
                }
                body = body.replacen(NATIVE_SUBMIT, TIMED_SUBMIT, 1);
            }
            write_artifact(&plugin.join("hooks/input.js"), &body)?;
            write_artifact(&plugin.join("types/index.d.ts"), "export {};\n")?;
            let path = plugin.to_str().ok_or_else(refused)?;
            argv.extend(["--plugin-dir".into(), path.into()]);
        }
        let broker = Arc::new(Self {
            dir: dir.to_owned(),
            supervisor: supervisor.to_owned(),
            owner_socket: owner_socket.into(),
            session: field(config, "id").into(),
            installation: installation.into(),
            token: token.into(),
            auth,
            generation,
            manager: manager.clone(),
            sender_name,
            joined: Mutex::new(Joined::default()),
            progress: Condvar::new(),
        });
        let receiver = broker.clone();
        thread::spawn(move || {
            for stream in listener.incoming().flatten() {
                let _ = receiver.respond(stream);
            }
        });
        Ok(broker)
    }

    pub(crate) fn ready(&self) -> io::Result<String> {
        self.joined
            .lock()
            .unwrap()
            .ready()
            .map(str::to_owned)
            .ok_or_else(refused)
    }

    pub(crate) fn submit(&self, request: &Value) -> io::Result<Value> {
        let operation = field(request, "operation_id");
        let session = field(request, "session");
        let text = field(request, "text");
        let instance = receipt::provider_instance("claude", &self.token);
        let origin = agy_input::origin(&request["origin"])?;
        let intent = agy_input::intent(text, &origin)?;
        if !receipt::timely(operation)
            || text.is_empty()
            || text.len() > 4096
            || text.contains('\0')
            || request["session_instance"] != instance
            || session
                .split_once('/')
                .is_none_or(|(_, local)| local != self.session)
        {
            return Err(refused());
        }
        let composer =
            json!({"session":session,"session_instance":instance,"text":text,"origin":origin});
        let mut joined = self.joined.lock().unwrap();
        let native = joined.ready().ok_or_else(refused)?.to_owned();
        if let Some(saved) = receipt::load_for(
            &self.dir,
            operation,
            session,
            &self.token,
            &native,
            "claude",
        )? {
            return if saved["intent"] == intent {
                Ok(saved["receipt"].clone())
            } else {
                Err(refused())
            };
        }
        if joined.operations.len() >= 128 {
            return Err(refused());
        }
        let root = self.dir.parent().ok_or_else(refused)?;
        receipt::verify_binding(root, operation, session, &instance, &intent)?;
        crate::discord::verify_connector_worker_input(
            &self.supervisor,
            &self.owner_socket,
            &self.installation,
            "claude",
            operation,
            &composer,
        )?;
        receipt::fence_for(
            &self.dir,
            operation,
            session,
            &self.token,
            &native,
            "claude",
        )?;
        let result = json!({
            "operation_id":operation,"session":session,"session_instance":instance,
            "state":"outcome_unknown","agent":"claude","native_session_id":native,
            "native_generation":self.generation,"origin":origin,
        });
        receipt::save(&self.dir, operation, &intent, &result)?;
        joined.operations.insert(
            operation.into(),
            Pending {
                composer,
                intent,
                result: result.clone(),
                claimed: false,
                hold: None,
            },
        );
        let (mut joined, _) = self
            .progress
            .wait_timeout_while(joined, Duration::from_secs(3), |state| {
                state
                    .operations
                    .get(operation)
                    .is_some_and(|entry| entry.result["state"] == "outcome_unknown")
            })
            .unwrap();
        if let Some(entry) = joined.operations.get_mut(operation)
            && !entry.claimed
        {
            self.settle(entry, "rejected")?;
            joined.operations.remove(operation);
        }
        Ok(receipt::load_for(
            &self.dir,
            operation,
            session,
            &self.token,
            &native,
            "claude",
        )?
        .map_or(result, |record| record["receipt"].clone()))
    }

    fn respond(self: &Arc<Self>, stream: UnixStream) -> io::Result<()> {
        stream.set_read_timeout(Some(Duration::from_secs(1)))?;
        stream.set_write_timeout(Some(Duration::from_secs(1)))?;
        let mut reader = BufReader::new(stream);
        let (path, request) = read_request(&mut reader, &self.auth)?;
        let (status, body) = match self.exchange(&path, &request) {
            Ok(data) => ("200 OK", serde_json::to_vec(&data)?),
            Err(_) => ("409 Conflict", b"null".to_vec()),
        };
        write!(
            reader.get_mut(),
            "HTTP/1.1 {status}\r\nContent-Length: {}\r\nContent-Type: application/json\r\nConnection: close\r\n\r\n",
            body.len()
        )?;
        reader.get_mut().write_all(&body)
    }

    fn exchange(self: &Arc<Self>, path: &str, req: &Value) -> io::Result<Value> {
        let worker: Worker = serde_json::from_value(req["worker"].clone())?;
        if req["generation"] != self.generation
            || !claude::canonical_id(&worker.native_session_id)
            || !claude::canonical_id(&worker.generation)
            || !qualified(&worker.version)
        {
            return Err(refused());
        }
        let mut joined = self.joined.lock().unwrap();
        if !matches!(path, "/guard" | "/exchange" | "/revoke") {
            if self.manager.is_null()
                || joined.revoked
                || joined
                    .guard
                    .as_ref()
                    .is_none_or(|(guard, heard)| guard != &worker || heard.elapsed() >= HEARTBEAT)
            {
                return Err(refused());
            }
            if path == "/turn"
                && req["turn"]["state"] == "idle"
                && field(&req["turn"], "turn_id").is_empty()
                && joined.proof.is_none()
            {
                return Ok(Value::Null);
            }
            if path == "/manager-idle" && field(req, "turn_id").is_empty() && joined.proof.is_none()
            {
                joined.manager_initial_idle = true;
                return Ok(Value::Null);
            }
            if joined.ready().is_none() {
                return Err(refused());
            }
            let proof = joined.proof.as_mut().ok_or_else(refused)?;
            return proof.claude_guard(&self.sender_name, path, req);
        }
        let role = if path == "/guard" {
            &mut joined.guard
        } else {
            &mut joined.sender
        };
        if role
            .as_ref()
            .is_some_and(|(existing, _)| existing != &worker)
        {
            return Err(refused());
        }
        *role = Some((worker, Instant::now()));
        if path == "/revoke" || path == "/guard" && req["state"] == "revoked" {
            joined.revoked = true;
            for entry in joined.operations.values_mut() {
                entry.hold.take();
                if !entry.claimed {
                    self.settle(entry, "rejected")?;
                }
            }
            self.progress.notify_all();
            return Ok(Value::Null);
        }
        if path == "/exchange" && !field(req, "operation_id").is_empty() {
            let operation = field(req, "operation_id");
            let Some(entry) = joined
                .operations
                .get_mut(operation)
                .filter(|entry| entry.claimed)
            else {
                return Err(refused());
            };
            let disposition = field(req, "state");
            if !matches!(
                disposition,
                "submitted" | "started" | "rejected" | "uncertain"
            ) {
                return Err(refused());
            }
            entry.hold.take();
            match disposition {
                "submitted" => self.settle(entry, "submitted")?,
                "started" => {
                    self.settle(entry, "submitted")?;
                    joined.operations.remove(operation);
                }
                "rejected" => {
                    self.settle(entry, "rejected")?;
                    joined.operations.remove(operation);
                }
                "uncertain" => {
                    self.settle(entry, "outcome_unknown")?;
                    joined.operations.remove(operation);
                }
                _ => return Err(refused()),
            }
            self.progress.notify_all();
            return Ok(Value::Null);
        }
        let Some(id) = joined.ready() else {
            return Ok(Value::Null);
        };
        if !self.manager.is_null() && joined.proof.is_none() {
            let guard = &joined.guard.as_ref().ok_or_else(refused)?.0;
            let resume = &self.manager["resume_request"]["binding"];
            if resume["native_session_id"]
                .as_str()
                .is_some_and(|expected| id != expected)
            {
                joined.revoked = true;
                return Err(refused());
            }
            let binding = json!({
                "session":format!("{}/{}", field(&self.manager,"endpoint_id"),self.session),
                "session_instance":receipt::provider_instance("claude",&self.token),
                "conversation_id":self.manager["launch"]["manager"]["conversation_id"],
                "provider":"claude","native_session_id":id,"native_generation":self.generation,
                "observation_generation":format!("{}:{}",self.generation,guard.generation),
                "login_epoch":self.manager["account"]
            });
            let mut proof = codex_proof::Ledger::open(&self.dir, &binding)?;
            if joined.manager_initial_idle {
                proof.claude_guard(&self.sender_name, "/manager-idle", &json!({"turn_id":""}))?;
            }
            joined.proof = Some(proof);
            return Ok(Value::Null);
        }
        let registration = self.dir.join("claude-native.json");
        if !registration.exists() {
            runtime::write_json(
                &registration,
                &json!({"token":self.token,"conversation_id":id}),
            )?;
        }
        if path == "/exchange"
            && let Some((operation, entry)) = joined
                .operations
                .iter_mut()
                .find(|(_, entry)| !entry.claimed)
        {
            let guard = crate::discord::lock_connector_worker_input(
                &self.supervisor,
                &self.owner_socket,
                &self.installation,
                "claude",
                operation,
                &entry.composer,
            );
            let Ok(guard) = guard else {
                let operation = operation.clone();
                self.settle(entry, "rejected")?;
                joined.operations.remove(&operation);
                self.progress.notify_all();
                return Ok(Value::Null);
            };
            entry.claimed = true;
            entry.hold = Some(guard);
            let broker = Arc::clone(self);
            let pending_id = operation.clone();
            let expires_ms = crate::ipc::spawn::now_ms() + HANDOFF.as_millis() as u64;
            thread::spawn(move || {
                thread::sleep(HANDOFF);
                if let Some(entry) = broker
                    .joined
                    .lock()
                    .unwrap()
                    .operations
                    .get_mut(&pending_id)
                {
                    entry.hold.take();
                }
            });
            return Ok(
                json!({"operation_id":operation,"text":entry.composer["text"],"expires_ms":expires_ms}),
            );
        }
        Ok(Value::Null)
    }

    fn settle(&self, entry: &mut Pending, state: &str) -> io::Result<()> {
        let mut result = entry.result.clone();
        result["state"] = json!(state);
        receipt::save(
            &self.dir,
            field(&result, "operation_id"),
            &entry.intent,
            &result,
        )?;
        entry.result = result;
        Ok(())
    }
}

fn write_artifact(path: &Path, body: &str) -> io::Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)?
        .write_all(body.as_bytes())
}

fn refused() -> io::Error {
    io::Error::other("Claude native input is unproven; explicitly refresh this session")
}

fn read_request(reader: &mut impl BufRead, auth: &str) -> io::Result<(String, Value)> {
    let mut header = String::new();
    let mut limited = reader.take(4097);
    limited.read_line(&mut header)?;
    let path = header
        .strip_prefix("POST ")
        .and_then(|line| line.strip_suffix(" HTTP/1.1\r\n"))
        .ok_or_else(refused)?
        .to_owned();
    let mut authorization = None;
    let mut length = None;
    loop {
        header.clear();
        limited.read_line(&mut header)?;
        if header == "\r\n" {
            break;
        }
        let (key, value) = header.trim_end().split_once(':').ok_or_else(refused)?;
        if key.eq_ignore_ascii_case("authorization") {
            if authorization.replace(value.trim().to_owned()).is_some() {
                return Err(refused());
            }
        } else if key.eq_ignore_ascii_case("content-length") {
            if length
                .replace(value.trim().parse::<usize>().map_err(|_| refused())?)
                .is_some()
            {
                return Err(refused());
            }
        } else if key.eq_ignore_ascii_case("transfer-encoding") {
            return Err(refused());
        }
    }
    if !authorization.is_some_and(|value| bool::from(value.as_bytes().ct_eq(auth.as_bytes()))) {
        return Err(refused());
    }
    let len = length.filter(|len| *len <= 16 * 1024).ok_or_else(refused)?;
    let mut body = vec![0; len];
    reader.read_exact(&mut body)?;
    Ok((path, serde_json::from_slice(&body)?))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn native_input_requires_two_distinct_live_roles_and_stays_retired() {
        let mut sender = Worker {
            native_session_id: "2eb27cbe-8007-4ff3-b852-c92b77bbd112".into(),
            generation: "9ee3ad51-2785-46aa-a173-9da51d2c61fd".into(),
            version: VERSION.into(),
        };
        let mut state = Joined {
            sender: Some((sender.clone(), Instant::now())),
            ..Joined::default()
        };
        assert_eq!(state.ready(), None);
        state.guard = Some((sender.clone(), Instant::now()));
        assert_eq!(state.ready(), None);
        sender.generation = "e39c2a76-8551-4ef2-9da3-be5ee2db0dba".into();
        state.guard = Some((sender.clone(), Instant::now() - HEARTBEAT));
        assert_eq!(state.ready(), None);
        state.guard = Some((sender.clone(), Instant::now()));
        assert_eq!(state.ready(), Some(sender.native_session_id.as_str()));
        state.revoked = true;
        assert_eq!(state.ready(), None);
    }

    #[test]
    fn private_http_requires_one_exact_authorization_and_bounded_body() {
        let prefix = "POST /guard HTTP/1.1\r\nAuthorization: Bearer example\r\n";
        let parse = |headers: &str| {
            read_request(
                &mut format!("{prefix}{headers}\r\n{{}}").as_bytes(),
                "Bearer example",
            )
        };
        assert_eq!(
            parse("Content-Length: 2\r\n").unwrap(),
            ("/guard".into(), json!({}))
        );
        assert!(parse("Content-Length: 2\r\nContent-Length: 2\r\n").is_err());
        assert!(parse("Content-Length: 2\r\nAuthorization: Bearer fake\r\n").is_err());
        assert!(parse("Content-Length: 20000\r\n").is_err());
    }
}
