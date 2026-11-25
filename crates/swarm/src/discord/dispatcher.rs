//! One owner-personal discussion on the native supervisor's exact manager binding.
use super::config::{Config, invalid};
use super::manager::Manager;
use super::receipt::{Inbox, Observation, hash, publication_nonce, verify_publication};
use super::rest::{Discord, Post};
use crate::{ipc::field, socket::DeadlineReader, supervisor::runtime};
use serde_json::{Value, json};
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::os::unix::net::UnixListener;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use subtle::ConstantTimeEq;

pub(super) trait Audience {
    fn current(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>>;
    fn verify(&self, cfg: &Config) -> io::Result<()>;
    fn post(&self, cfg: &Config, nonce: &str, body: &str) -> Post;
}

impl Audience for Discord {
    fn current(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>> {
        self.owner_message(cfg, id)
    }

    fn verify(&self, cfg: &Config) -> io::Result<()> {
        self.verify_dm(cfg, None).map(|_| ())
    }

    fn post(&self, cfg: &Config, nonce: &str, body: &str) -> Post {
        self.post_reply(cfg, nonce, body)
    }
}

pub(super) struct Dispatcher {
    owner: Manager,
    listener: UnixListener,
    launch: Value,
    socket: PathBuf,
    socket_identity: (u64, u64),
    credential: String,
}

impl Dispatcher {
    pub(super) fn open(cfg: &Config, place: &Path) -> io::Result<Option<Self>> {
        // This first discussion path uses the native restricted manager harness.
        if cfg.text("project_access") == "vm" {
            return Ok(None);
        }
        let owner = Manager::new(cfg)?;
        if owner.verify_supervisor().is_err() {
            return Ok(None);
        }
        let dir = fs::canonicalize(place)?.join("manager-v1");
        runtime::private_dir(&dir)?;
        let auth = dir.join("bridge.auth");
        let credential = auth_file(&auth)?;
        let socket = dir.join("bridge.sock");
        runtime::reclaim_socket(&socket)?;
        let listener = runtime::bind(&socket)?;
        let meta = fs::symlink_metadata(&socket)?;
        let launch = owner.launch_request(&socket, &auth);
        let this = Self {
            owner,
            listener,
            launch,
            socket,
            credential,
            socket_identity: (meta.dev(), meta.ino()),
        };
        this.listener.set_nonblocking(true)?;
        Ok(Some(this))
    }

    pub(super) fn tick(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        api: &impl Audience,
    ) -> io::Result<()> {
        self.serve_tool(inbox)?;
        let mut state = inbox.manager().clone();
        if state.is_null() {
            if inbox.next_prepared().is_none() {
                return Ok(());
            }
            state = json!({"generation":1, "launch_operation":operation("launch")?, "launch":self.launch});
            inbox.set_manager(state.clone())?;
        }
        if state["launch"] != self.launch || state["generation"] != 1 {
            return Err(invalid(
                "persisted native manager launch differs from this place",
            ));
        }
        if state["session"].is_null() {
            let op = field(&state, "launch_operation");
            let status = self.owner.launch_status(op)?;
            let session = match field(&status, "state") {
                "applied" => status["session_id"].clone(),
                "unknown_operation" => self.owner.launch(op, &self.launch)?["id"].clone(),
                _ => {
                    return Err(io::Error::other(
                        "earlier manager launch remains unconfirmed",
                    ));
                }
            };
            if session.as_str().is_none_or(str::is_empty) {
                return Err(invalid("native manager launch lacks a scoped session"));
            }
            state["session"] = session;
            inbox.set_manager(state.clone())?;
        }
        let session = field(&state, "session");
        let row = match self.owner.session(session) {
            Ok(row) => row,
            Err(err) if state["binding"].is_null() && err.kind() == io::ErrorKind::InvalidInput => {
                return Ok(());
            }
            Err(err) => return Err(err),
        };
        let instance = field(&row["capabilities"], "session_instance");
        let status = match self.owner.runtime(session, instance, &self.launch) {
            Ok(status) => status,
            Err(_) if state["binding"].is_null() => return Ok(()),
            Err(err) => return Err(err),
        };
        if state["binding"].is_null() {
            state["binding"] = status["binding"].clone();
            inbox.set_manager(state.clone())?;
        }
        if state["binding"] != status["binding"]
            || status["turn"]["externally_steered"] == true
            || status["provider_exhausted"] == true
        {
            return Err(invalid("native manager requires owner reconciliation"));
        }
        if !state["input"].is_null() {
            let input = &state["input"];
            let observation = self.owner.input_status(&input["composer"])?;
            if observation["external_input"]
                != input["composer"]["manager_admission"]["expected_external_input"]
            {
                return Err(invalid("native manager received external input"));
            }
            let phase = field(&observation, "state");
            if matches!(phase, "running" | "completed") {
                let native_turn = field(&observation, "native_turn_id");
                if native_turn.is_empty()
                    || input["turn_id"]
                        .as_str()
                        .is_some_and(|id| id != native_turn)
                {
                    return Err(invalid(
                        "native input observation changed its turn identity",
                    ));
                }
                if input["turn_id"].is_null() {
                    let id = field(input, "source_id").to_owned();
                    state["input"]["turn_id"] = json!(native_turn);
                    inbox.settled_input(&id, state.clone())?;
                }
            }
            self.deliver(cfg, inbox, api, &state, millis()?)?;
            if phase == "completed" {
                let nonce = field(&state["input"], "final_nonce");
                if !nonce.is_empty() && inbox.publication(nonce)["state"] == "sent" {
                    state["input"] = Value::Null;
                    inbox.set_manager(state)?;
                }
            }
            return Ok(());
        }
        let Some(input) = inbox.next_prepared() else {
            return Ok(());
        };
        if status["ready_for_input"] != true
            || !matches!(field(&status["turn"], "state"), "idle" | "completed")
        {
            return Ok(());
        }
        let id = field(&input["route"], "message_id");
        let Some(source) = api.current(cfg, id)? else {
            return inbox.withdraw(id);
        };
        let Some(current) = inbox.admit(&source)? else {
            return inbox.withdraw(id);
        };
        let composer = self.owner.composer(&current, &status)?;
        let semantic = operation("semantic")?;
        state["input"] = json!({"operation":semantic, "composer":composer, "source_id":id});
        inbox.cutover(id, field(&current, "revision"), state)?;
        // An error here may have crossed the native boundary. Reconcile by nonce, never resend.
        self.owner.send(&semantic, &composer)
    }

    fn deliver(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        api: &impl Audience,
        state: &Value,
        now: u64,
    ) -> io::Result<()> {
        for nonce in inbox.pending_publications() {
            let entry = inbox.publication(&nonce);
            verify_publication(&nonce, &entry)?;
            let publication = &entry["proof"];
            let input = &state["input"];
            if publication["binding"] != state["binding"]
                || publication["turn_id"] != input["turn_id"]
                || publication["source_id"] != input["source_id"]
                || input["turn_id"].is_null()
                || input["final_nonce"] != nonce
                || publication["call_receipt"]["native_input_id"]
                    != input["composer"]["manager_admission"]["nonce"]
            {
                return Err(invalid(
                    "queued publication is out of the current native input scope",
                ));
            }
            if entry["retry_at"].as_u64().is_some_and(|at| now < at) {
                continue;
            }
            api.verify(cfg)?;
            let source_id = field(input, "source_id");
            let source = api
                .current(cfg, source_id)?
                .filter(|source| source["id"] == source_id)
                .ok_or_else(|| {
                    invalid("publication requires the original visible Discord source")
                })?;
            let current = inbox
                .admit(&source)?
                .filter(|receipt| receipt["state"] == "accepted")
                .ok_or_else(|| invalid("publication source no longer proves native admission"))?;
            if current["revision"] != input["composer"]["origin"]["revision"] {
                return Err(invalid("publication source changed after native admission"));
            }
            let evidence = self.owner.input_status(&input["composer"])?;
            if evidence["native_turn_id"] != input["turn_id"]
                || evidence["external_input"]
                    != input["composer"]["manager_admission"]["expected_external_input"]
                || !matches!(field(&evidence, "state"), "running" | "completed")
            {
                return Err(invalid(
                    "native publication authority changed immediately before delivery",
                ));
            }
            let body = field(&entry, "body");
            inbox.publication_state(&nonce, "sending", None)?;
            match api.post(cfg, &nonce, body) {
                Post::Acknowledged(receipt)
                    if receipt["nonce"] == nonce
                        && inbox.observe(&receipt, Observation::Rest)? => {}
                Post::RateLimited(after) => {
                    inbox.publication_state(&nonce, "prepared", Some(now.saturating_add(after)))?
                }
                _ => inbox.publication_state(&nonce, "uncertain", None)?,
            }
        }
        Ok(())
    }

    fn serve_tool(&self, inbox: &mut Inbox) -> io::Result<()> {
        let (mut stream, _) = match self.listener.accept() {
            Ok(pair) => pair,
            Err(err) if err.kind() == io::ErrorKind::WouldBlock => return Ok(()),
            Err(err) => return Err(err),
        };
        let result =
            read_tool(&stream, &self.credential).and_then(|request| self.tool(inbox, &request));
        let reply = match result {
            Ok(data) => json!({"data":data}),
            Err(err)
                if matches!(
                    err.kind(),
                    io::ErrorKind::InvalidInput
                        | io::ErrorKind::InvalidData
                        | io::ErrorKind::TimedOut
                        | io::ErrorKind::UnexpectedEof
                ) =>
            {
                json!({"error":"unconfirmed"})
            }
            Err(err) => return Err(err),
        };
        let body = serde_json::to_vec(&reply)?;
        stream.set_write_timeout(Some(Duration::from_secs(2)))?;
        let _ = write!(
            stream,
            "HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
            body.len()
        )
        .and_then(|_| stream.write_all(&body));
        Ok(())
    }

    fn tool(&self, inbox: &mut Inbox, request: &Value) -> io::Result<Value> {
        let mut state = inbox.manager().clone();
        let input = &state["input"];
        if request.as_object().is_none_or(|object| object.len() != 4)
            || request["call"]
                .as_object()
                .is_none_or(|call| call.len() != 3)
            || request["version"] != 1
            || request["conversation_id"] != state["binding"]["conversation_id"]
            || request["session_id"] != field(&state, "session").rsplit('/').next().unwrap_or("")
            || input["turn_id"].is_null()
            || request["call"]["name"] != "publish"
        {
            return Err(invalid("unbound manager publication"));
        }
        let call = &request["call"];
        let mut args = call["arguments"].clone();
        let fields = args
            .as_object()
            .ok_or_else(|| invalid("invalid publication arguments"))?;
        if fields.len() != 4 || args["sequence"] != 1 || args["final"] != true {
            return Err(invalid(
                "only one deliberate final text publication is supported",
            ));
        }
        let output = field(&args, "output_id").to_owned();
        let body = field(&args, "text").to_owned();
        if output.is_empty()
            || output.len() > 128
            || output.chars().any(char::is_control)
            || body.trim().is_empty()
            || body.encode_utf16().count() > 1900
        {
            return Err(invalid("invalid final publication ID or body"));
        }
        args.sort_all_objects();
        let proof = self.owner.prove_publication(
            &input["composer"],
            "publish",
            &hash(&args)?,
            &call["meta"],
        )?;
        if proof["native_turn_id"] != input["turn_id"] {
            return Err(invalid("manager publication belongs to another turn"));
        }
        let publication = json!({
            "binding":state["binding"], "turn_id":input["turn_id"],
            "source_id":input["source_id"], "output_id":output,"body":body,
            "native_call_id":proof["native_call_id"], "call_receipt":proof,
            "host_meta":call["meta"], "arguments":args, "arguments_hash":hash(&args)?
        });
        let nonce = publication_nonce(&publication)?;
        if input["final_nonce"]
            .as_str()
            .is_some_and(|saved| saved != nonce)
        {
            return Err(invalid("a final publication is already selected"));
        }
        state["input"]["final_nonce"] = json!(nonce);
        inbox.prepare_publication(&nonce, publication, state)?;
        Ok(json!({"output_id":output,"state":inbox.publication(&nonce)["state"]}))
    }
}

impl Drop for Dispatcher {
    fn drop(&mut self) {
        if fs::symlink_metadata(&self.socket)
            .is_ok_and(|meta| (meta.dev(), meta.ino()) == self.socket_identity)
        {
            let _ = fs::remove_file(&self.socket);
        }
    }
}

fn read_tool(stream: &std::os::unix::net::UnixStream, credential: &str) -> io::Result<Value> {
    let mut reader =
        BufReader::new(DeadlineReader(stream, Instant::now() + Duration::from_secs(2)).take(8193));
    let mut line = String::new();
    reader.read_line(&mut line)?;
    if !matches!(
        line.as_str(),
        "POST /manager/call HTTP/1.1\r\n" | "POST /manager/call HTTP/1.0\r\n"
    ) {
        return Err(invalid("invalid manager request path"));
    }
    let mut headers = std::collections::BTreeMap::new();
    loop {
        line.clear();
        if reader.read_line(&mut line)? == 0 {
            return Err(invalid("missing manager request headers"));
        }
        if line == "\r\n" {
            break;
        }
        let (key, value) = line
            .split_once(':')
            .ok_or_else(|| invalid("invalid manager request header"))?;
        if headers
            .insert(key.to_ascii_lowercase(), value.trim().to_owned())
            .is_some()
        {
            return Err(invalid("duplicate manager request header"));
        }
    }
    let expected = format!("Bearer {credential}");
    if !headers
        .get("authorization")
        .is_some_and(|value| bool::from(value.as_bytes().ct_eq(expected.as_bytes())))
        || headers.get("host").is_none_or(|v| v != "voice")
        || headers
            .get("content-type")
            .is_none_or(|v| v != "application/json")
        || headers.contains_key("origin")
        || headers.contains_key("transfer-encoding")
    {
        return Err(invalid("manager tool request not authenticated"));
    }
    let size = headers
        .get("content-length")
        .and_then(|n| n.parse::<usize>().ok())
        .filter(|n| (1..=8192).contains(n))
        .ok_or_else(|| invalid("invalid manager tool content length"))?;
    let remaining = reader.get_ref().limit();
    reader
        .get_mut()
        .set_limit(remaining.saturating_add(size as u64));
    let mut body = vec![0; size];
    reader.read_exact(&mut body)?;
    Ok(serde_json::from_slice(&body)?)
}

fn auth_file(path: &Path) -> io::Result<String> {
    let open = || {
        OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
            .open(path)
    };
    let mut file = match open() {
        Ok(file) => file,
        Err(err) if err.kind() == io::ErrorKind::NotFound => {
            let value = random_hex::<32>()?;
            let mut file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(path)?;
            file.write_all(value.as_bytes())?;
            file.sync_all()?;
            File::open(path.parent().unwrap())?.sync_all()?;
            open()?
        }
        Err(err) => return Err(err),
    };
    let meta = file.metadata()?;
    // SAFETY: the effective UID function takes no arguments.
    if !meta.is_file()
        || meta.nlink() != 1
        || meta.mode() & 0o777 != 0o600
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(invalid("manager auth file lacks private owner custody"));
    }
    let mut value = String::new();
    (&mut file).take(65).read_to_string(&mut value)?;
    if value.len() != 64
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return Err(invalid("manager auth file has an invalid credential"));
    }
    Ok(value)
}

fn random_hex<const N: usize>() -> io::Result<String> {
    let mut bytes = [0; N];
    File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

fn millis() -> io::Result<u64> {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(io::Error::other)?
        .as_millis()
        .try_into()
        .map_err(io::Error::other)
}

fn operation(kind: &str) -> io::Result<String> {
    Ok(format!("{kind}:{}:{}", millis()?, random_hex::<16>()?))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ipc::wire;
    use sha2::{Digest, Sha256};
    use std::cell::RefCell;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{Arc, Mutex};

    struct MockDiscord {
        outcome: RefCell<Post>,
        posted: RefCell<Vec<(String, String)>>,
    }

    impl Audience for MockDiscord {
        fn current(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>> {
            Ok(Some(source(cfg, id)))
        }

        fn verify(&self, cfg: &Config) -> io::Result<()> {
            assert_eq!(cfg.text("dm_channel_id"), "400");
            Ok(())
        }

        fn post(&self, cfg: &Config, nonce: &str, body: &str) -> Post {
            self.posted
                .borrow_mut()
                .push((nonce.to_owned(), body.to_owned()));
            match self.outcome.replace(Post::Unconfirmed) {
                Post::Acknowledged(_) => Post::Acknowledged(json!({
                    "id":"102", "author":{"id":"200","bot":true},"channel_id":cfg.text("dm_channel_id"),
                    "nonce":nonce,"content":body
                })),
                result => result,
            }
        }
    }

    fn source(cfg: &Config, id: &str) -> Value {
        json!({"id":id, "type":0, "channel_id":cfg.text("dm_channel_id"),"content":"hello",
            "author":{"id":cfg.text("owner_id")},"attachments":[]})
    }

    struct Record {
        row: Value,
        binding: Value,
        launch: Value,
        composer: Value,
        sends: usize,
        wrong_turn: bool,
        wrong_binding: bool,
        completed: bool,
    }

    impl Record {
        fn respond(&mut self, endpoint: &str, request: Value) -> Value {
            let mut response = match field(&request, "op") {
                "owner_launch_status" => json!({
                    "op":"owner_launch_status", "subject_operation_id":request["subject_operation_id"],
                    "operation_outcome":{"state":"unknown_operation"}
                }),
                "owner_launch" => {
                    assert!(self.launch.is_null(), "manager launched more than once");
                    self.launch = request["launch"].clone();
                    assert!(
                        self.launch["env"].is_null(),
                        "Discord credentials must not cross into native launch environment"
                    );
                    assert!(self.launch["manager"].get("token").is_none());
                    let conversation = &self.launch["manager"]["conversation_id"];
                    self.row = json!({"id":format!("{endpoint}/local"),"endpoint_id":endpoint,
                        "agent":"codex","role":"manager","manager_conversation_id":conversation,
                        "cwd":self.launch["cwd"],"status":{"Process":"running"},
                        "capabilities":{"session_instance":"incarnation"}});
                    self.binding = json!({"session":self.row["id"],"session_instance":"incarnation",
                        "conversation_id":conversation,"provider":"codex","native_session_id":"provider-thread",
                        "native_generation":"native","observation_generation":"observed","login_epoch":"account"});
                    json!({"op":"launch","operation_id":request["operation_id"],"session":self.row})
                }
                "list" => json!({"op":"list","sessions":[self.row]}),
                "manager_runtime" => {
                    let mut binding = self.binding.clone();
                    if self.wrong_binding {
                        binding["observation_generation"] = json!("different-writer");
                    }
                    assert_eq!(
                        request["manager_runtime_request"]["session_instance"],
                        "incarnation"
                    );
                    json!({"op":"manager_runtime", "session_id":self.row["id"],
                        "manager_runtime":{
                            "ready_for_input":true,"binding":binding,
                            "bridge_socket":self.launch["manager"]["bridge_socket"],
                            "auth_file":self.launch["manager"]["auth_file"],
                            "turn":{"state":if self.composer.is_null() {"idle"} else {"running"},"external_input":0}
                        }
                    })
                }
                "semantic_send" => {
                    self.sends += 1;
                    self.composer = request["composer_send"].clone();
                    assert_eq!(self.composer["manager_admission"]["binding"], self.binding);
                    assert_eq!(self.composer["origin"]["installation_id"], "fixture");
                    assert_eq!(self.composer["origin"]["actor_id"], "300");
                    assert_eq!(self.composer["origin"]["channel_id"], "400");
                    assert_eq!(self.composer["text"], "hello");
                    json!({"op":"ok","operation_id":request["operation_id"]})
                }
                "manager_input_status" => {
                    assert_eq!(
                        request["manager_input_status_request"],
                        self.composer["manager_admission"]
                    );
                    json!({"op":"manager_input_status","session_id":self.row["id"],
                    "manager_input_status":{
                       "state":if self.completed {"completed"} else {"running"}, "external_input":0,
                       "native_input_id":self.composer["manager_admission"]["nonce"],"native_turn_id":"authorized-turn"
                    }})
                }
                "manager_resolve_call" => {
                    assert_eq!(request["manager_call"]["binding"], self.binding);
                    assert_eq!(request["manager_call"]["tool_name"], "publish");
                    json!({"op":"manager_resolve_call","session_id":self.row["id"],
                       "manager_call_proof":{"main":true, "native_input_id":self.composer["manager_admission"]["nonce"],
                           "native_turn_id":if self.wrong_turn {"old-turn"} else {"authorized-turn"},"native_call_id":"provider-call"}})
                }
                op => panic!("unsupported fixture operation {op}"),
            };
            response["endpoint_id"] = json!(endpoint);
            response
        }
    }

    #[test]
    fn exact_rest_acknowledgement_has_distinct_evidence() {
        let dir = Path::new("/tmp").join(format!("r-{}", &runtime::random_id().unwrap()[..12]));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"installation_id":"fixture","application_id":"200","owner_id":"300","dm_channel_id":"400"}),
        );
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("100").unwrap();
        let (nonce, proof) = super::super::receipt::fixture_publication("deliberate reply");
        inbox
            .prepare_publication(&nonce, proof, Value::Null)
            .unwrap();
        inbox.publication_state(&nonce, "sending", None).unwrap();
        let api = MockDiscord {
            outcome: RefCell::new(Post::Acknowledged(Value::Null)),
            posted: RefCell::new(Vec::new()),
        };
        let Post::Acknowledged(receipt) = api.post(&cfg, &nonce, "deliberate reply") else {
            panic!("expected acknowledgement")
        };
        assert!(inbox.observe(&receipt, Observation::Rest).unwrap());
        assert_eq!(inbox.publication(&nonce)["rest_acknowledged"], true);
        assert!(inbox.publication(&nonce)["history_seen"].is_null());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn owner_dm_runs_through_bound_manager_and_exact_reply_once() {
        let dir = Path::new("/tmp").join(format!("d-{}", &runtime::random_id().unwrap()[..12]));
        runtime::private_dir(&dir).unwrap();
        let dir = fs::canonicalize(dir).unwrap();
        let socket = dir.join("daemon.sock");
        let digest = format!("{:x}", Sha256::digest(dir.as_os_str().as_encoded_bytes()));
        let endpoint = format!("ep-{}", &digest[..8]);
        let cfg = Config(
            json!({"version":1,"enabled":true,"name":"fixture","mode":"personal",
            "installation_id":"fixture","application_id":"200","owner_id":"300","dm_channel_id":"400",
            "daemon_socket":socket,"work_dir":dir,"manager_cli":"codex","supervisor":{"state_dir":dir}}),
        );
        let listener = runtime::bind(&socket).unwrap();
        let done = Arc::new(AtomicBool::new(false));
        let state = Arc::new(Mutex::new(Record {
            row: Value::Null,
            binding: Value::Null,
            launch: Value::Null,
            composer: Value::Null,
            sends: 0,
            wrong_turn: false,
            wrong_binding: false,
            completed: false,
        }));
        let server = {
            let done = done.clone();
            let state = state.clone();
            std::thread::spawn(move || {
                while !done.load(Ordering::Relaxed) {
                    let (mut socket, _) = listener.accept().unwrap();
                    let send = |socket: &mut _, frame: Value| {
                        wire::write_frame(
                            socket,
                            wire::CONTROL,
                            &serde_json::to_vec(&frame).unwrap(),
                        )
                        .unwrap()
                    };
                    let Ok((_, hello)) = wire::read_frame(&mut socket) else {
                        continue;
                    };
                    let hello: Value = serde_json::from_slice(&hello).unwrap();
                    assert_eq!(hello["op"], "hello");
                    send(
                        &mut socket,
                        json!({"op":"hello","protocol_version":1,"endpoint_id":endpoint,"capabilities":super::super::manager::CAPS}),
                    );
                    let Ok((_, raw)) = wire::read_frame(&mut socket) else {
                        continue;
                    };
                    let request = serde_json::from_slice(&raw).unwrap();
                    let response = state.lock().unwrap().respond(&endpoint, request);
                    send(&mut socket, response);
                }
            })
        };
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("100").unwrap();
        inbox.admit(&source(&cfg, "101")).unwrap();
        let dispatcher = Dispatcher::open(&cfg, &dir).unwrap().unwrap();
        let api = MockDiscord {
            outcome: RefCell::new(Post::RateLimited(60_000)),
            posted: RefCell::new(Vec::new()),
        };
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        assert_eq!(state.lock().unwrap().sends, 1);
        assert!(api.posted.borrow().is_empty());
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        let publish = json!({"version":1, "conversation_id":inbox.manager()["binding"]["conversation_id"],
            "session_id":"local", "call":{"name":"publish","arguments":{
                "output_id":"final","sequence":1,"text":"deliberate reply","final":true
            },"meta":{"callId":"provider-call","threadId":"provider-thread","sessionId":"provider-thread",
                "x-codex-turn-metadata":{"turn_id":"authorized-turn","thread_id":"provider-thread","session_id":"provider-thread"}}}});
        let mut alien = publish.clone();
        alien["call"]["meta"]["threadId"] = json!("unbound-thread");
        assert!(dispatcher.tool(&mut inbox, &alien).is_err());
        let mut wrong_recipient = publish.clone();
        wrong_recipient["call"]["arguments"]["recipient_id"] = json!("401");
        assert!(dispatcher.tool(&mut inbox, &wrong_recipient).is_err());
        assert!(inbox.pending_publications().is_empty());
        state.lock().unwrap().wrong_turn = true;
        assert!(dispatcher.tool(&mut inbox, &publish).is_err());
        assert!(inbox.pending_publications().is_empty());
        state.lock().unwrap().wrong_turn = false;
        dispatcher.tool(&mut inbox, &publish).unwrap();
        dispatcher.tool(&mut inbox, &publish).unwrap();
        let nonce = field(&inbox.manager()["input"], "final_nonce").to_owned();
        assert_eq!(inbox.pending_publications(), std::slice::from_ref(&nonce));
        let outbox = dir.join("rust-text-v1.json");
        let original = runtime::read_json(&outbox).unwrap();
        drop(inbox);
        for damage in ["body", "nonce", "arguments", "source"] {
            let mut corrupted = original.clone();
            let entries = corrupted["outbox"].as_object_mut().unwrap();
            if damage == "nonce" {
                let entry = entries.shift_remove(&nonce).unwrap();
                entries.insert("wrong_nonce".into(), entry);
            } else {
                let entry = entries.get_mut(&nonce).unwrap();
                match damage {
                    "body" => entry["body"] = json!("unapproved text"),
                    "arguments" => entry["proof"]["arguments"]["text"] = json!("altered call"),
                    "source" => entry["proof"]["source_id"] = json!("991"),
                    _ => unreachable!(),
                }
            }
            runtime::write_json(&outbox, &corrupted).unwrap();
            assert!(Inbox::open(&dir, &cfg).is_err());
            assert!(api.posted.borrow().is_empty());
        }
        runtime::write_json(&outbox, &original).unwrap();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let mut displaced = inbox.manager().clone();
        displaced["input"]["final_nonce"] = json!("other-output");
        assert!(
            dispatcher
                .deliver(&cfg, &mut inbox, &api, &displaced, millis().unwrap())
                .is_err()
        );
        assert!(api.posted.borrow().is_empty());
        state.lock().unwrap().wrong_binding = true;
        assert!(dispatcher.tick(&cfg, &mut inbox, &api).is_err());
        assert!(api.posted.borrow().is_empty());
        state.lock().unwrap().wrong_binding = false;
        state.lock().unwrap().completed = true;
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        let retry_at = inbox.publication(&nonce)["retry_at"].as_u64().unwrap();
        let manager = inbox.manager().clone();
        dispatcher
            .deliver(&cfg, &mut inbox, &api, &manager, retry_at - 1)
            .unwrap();
        assert_eq!(api.posted.borrow().len(), 1);
        dispatcher
            .deliver(&cfg, &mut inbox, &api, &manager, retry_at)
            .unwrap();
        assert_eq!(inbox.publication(&nonce)["state"], "uncertain");
        drop(inbox);

        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        assert_eq!(api.posted.borrow().len(), 2);
        let wrong = json!({
            "id":"102", "author":{"id":"200", "bot":true},"channel_id":"401",
            "nonce":nonce,"content":"deliberate reply"
        });
        assert!(!inbox.observe(&wrong, Observation::Gateway).unwrap());
        let mut observed = wrong.clone();
        observed["channel_id"] = json!("400");
        assert!(inbox.observe(&observed, Observation::History).unwrap());
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        drop(inbox);
        let inbox = Inbox::open(&dir, &cfg).unwrap();
        assert_eq!(inbox.publication(&nonce)["state"], "sent");
        assert_eq!(inbox.publication(&nonce)["history_seen"], true);
        let mut inbox = inbox;
        let reused = inbox.publication(&nonce)["proof"].clone();
        assert!(
            inbox
                .prepare_publication("different-operation", reused, Value::Null)
                .is_err()
        );
        assert_eq!(
            api.posted.borrow().as_slice(),
            &[
                (nonce.clone(), "deliberate reply".into()),
                (nonce, "deliberate reply".into())
            ]
        );
        assert_eq!(state.lock().unwrap().sends, 1);
        done.store(true, Ordering::Relaxed);
        let _ = std::os::unix::net::UnixStream::connect(&socket).unwrap();
        server.join().unwrap();
        drop(dispatcher);
        fs::remove_dir_all(dir).unwrap();
    }
}
