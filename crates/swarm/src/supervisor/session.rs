use super::runtime::{self, IO_TIMEOUT, SHIM_VERSION};
use crate::ipc::field;
use crate::providers::{self, LaunchPlan, codex_receipt, opencode};
use serde_json::{Value, json};
use std::env;
use std::fs::{self, DirBuilder};
use std::io;
use std::os::unix::fs::DirBuilderExt;
use std::os::unix::net::UnixStream;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::{Mutex, OnceLock};
use std::thread;
use std::time::{Duration, Instant};
use time::OffsetDateTime;
use time::format_description::well_known::Rfc3339;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Phase {
    Starting,
    Running,
    Exited,
    Lost,
}

pub(super) struct Prepared {
    isolated: bool,
    cwd: PathBuf,
    label: String,
    tag: String,
    cols: u16,
    rows: u16,
    provider: LaunchPlan,
    pub(super) resume: Value,
}

struct Labels {
    name: String,
    tag: String,
}

fn sanitize_name(text: &str) -> String {
    text.chars().filter(|c| !c.is_control()).take(64).collect()
}

pub(super) struct Session {
    pub id: String,
    dir: PathBuf,
    token: String,
    agent: String,
    cwd: String,
    effective: PathBuf,
    labels: Mutex<Labels>,
    created: String,
    state: Mutex<(Phase, String)>,
    proven_owner: OnceLock<(u32, u64)>,
    launch_plan: Mutex<Option<LaunchPlan>>,
    observed_status: Mutex<Option<Value>>,
    opened: Instant,
}

impl Session {
    pub(super) fn load(dir: PathBuf) -> io::Result<Self> {
        runtime::private_dir(&dir)?;
        let data = runtime::read_json(&dir.join("session.json"))?;
        let id = field(&data, "id");
        let token = field(&data, "token");
        let created = field(&data, "created_at");
        let name = field(&data, "name");
        let cwd = field(&data, "cwd");
        if data["schema"] != 1
            || !runtime::valid_id(id)
            || dir.file_name().and_then(|name| name.to_str()) != Some(id)
            || !runtime::valid_id(token)
            || created.is_empty()
            || !data["name"].is_string()
            || cwd.is_empty()
        {
            return Err(io::Error::other("unproven Rust session reservation"));
        }
        Ok(Self {
            dir,
            id: id.into(),
            token: token.into(),
            agent: match field(&data["provider"], "name") {
                "codex" => "codex",
                "opencode" => "opencode",
                _ => "claude",
            }
            .into(),
            cwd: cwd.into(),
            effective: PathBuf::from(data["checkout"]["path"].as_str().unwrap_or(cwd)),
            labels: Mutex::new(Labels {
                name: name.into(),
                tag: field(&data, "tag").into(),
            }),
            created: created.into(),
            state: Mutex::new((Phase::Lost, created.into())),
            proven_owner: OnceLock::new(),
            launch_plan: Mutex::new(None),
            observed_status: Mutex::new(None),
            opened: Instant::now(),
        })
    }

    pub(super) fn prepare(request: &Value) -> io::Result<Prepared> {
        let req = &request["launch"];
        if !req.is_object() {
            return Err(io::Error::other("missing request"));
        }
        let valid_optional =
            |key, valid: fn(&Value) -> bool| req[key].is_null() || valid(&req[key]);
        if !["agent", "cwd", "name", "tag", "initial_prompt"]
            .into_iter()
            .all(|key| valid_optional(key, Value::is_string))
            || !valid_optional("worktree", Value::is_boolean)
            || !valid_optional("env", |value| {
                value
                    .as_array()
                    .is_some_and(|env| env.iter().all(Value::is_string))
            })
            || !valid_optional("options", Value::is_object)
            || !(request["operation_id"].is_null() || request["operation_id"].is_string())
        {
            return Err(io::Error::other("invalid launch field type"));
        }
        if !["cols", "rows"].into_iter().all(|key| {
            valid_optional(key, |value| {
                value.as_i64().is_some_and(|n| (0..=300).contains(&n))
            })
        }) {
            return Err(io::Error::other("cols/rows out of range"));
        }
        if !field(request, "operation_id").is_empty()
            || req.as_object().unwrap().iter().any(|(key, value)| {
                !matches!(
                    key.as_str(),
                    "agent"
                        | "cwd"
                        | "name"
                        | "tag"
                        | "env"
                        | "initial_prompt"
                        | "cols"
                        | "rows"
                        | "options"
                        | "worktree"
                ) && !value.is_null()
            })
            || req["options"].as_object().is_some_and(|options| {
                options.iter().any(|(key, value)| {
                    let agent = field(req, "agent");
                    let supported = key == "model"
                        || (agent == "codex" && key == "sandbox")
                        || (agent == "opencode" && key == "agent")
                        || (matches!(agent, "claude" | "claude-code")
                            && key == "dangerously-skip-permissions");
                    !supported || !value.is_string()
                })
            })
        {
            return Err(io::Error::other("unsupported launch options"));
        }
        let raw_cwd = field(req, "cwd");
        if raw_cwd.is_empty() || !Path::new(raw_cwd).is_dir() {
            return Err(io::Error::other("cwd is not an existing directory"));
        }
        let cwd = fs::canonicalize(raw_cwd)?;
        if cwd.to_str().is_none() {
            return Err(io::Error::other("cwd is not representable as UTF-8"));
        }
        let label = sanitize_name(field(req, "name"));
        let provider = providers::prepare(
            field(req, "agent"),
            &cwd,
            &label,
            req,
            runtime::launch_env(&req["env"]),
        )?;
        let (cols, rows) = runtime::dims(req);
        let isolated = req["worktree"] == true;
        if isolated && !crate::workspace::is_repo(&cwd) {
            return Err(io::Error::other("worktree: cwd is not a git repository"));
        }
        let tag = sanitize_name(field(req, "tag")).trim().to_owned();
        Ok(Prepared {
            isolated,
            tag,
            cwd,
            label,
            cols,
            rows,
            provider,
            resume: Value::Null,
        })
    }

    pub(super) fn prepare_opencode_resume(&self, request: &Value) -> io::Result<Prepared> {
        if self.agent != "opencode" || self.is_live() || !owner_gone(&self.dir, &self.token)? {
            return Err(io::Error::other(
                "OpenCode resume requires a confirmed stopped worker",
            ));
        }
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if meta["token"] != self.token || !meta["checkout"].is_null() {
            return Err(io::Error::other(
                "OpenCode resume requires the original non-worktree checkout",
            ));
        }
        let binding = runtime::read_json(&self.dir.join("opencode-binding.json"))?;
        if binding["initialPrompt"] != false {
            let record = runtime::read_json(&self.dir.join("opencode-initial.json"))?;
            if record["token"] != self.token
                || !matches!(field(&record, "state"), "native_accepted" | "rejected")
            {
                return Err(io::Error::other(
                    "OpenCode prior initial input remains unresolved",
                ));
            }
        }
        let native = self.opencode_native()?;
        let mut prepared = Self::prepare(&json!({"launch":{
            "agent":"opencode","cwd":self.cwd,"env":request["env"],
            "name":meta["name"],"tag":meta["tag"],"rows":meta["rows"],"cols":meta["cols"],
        }}))?;
        if opencode::profile(&prepared.provider.env) != meta["opencode_profile"]
            || meta["provider"]["executable"].as_str().map(Path::new)
                != Some(&prepared.provider.executable)
            || meta["provider"]["identity"] != prepared.provider.identity
            || meta["provider"]["version"] != prepared.provider.version
            || prepared.cwd != self.effective
        {
            return Err(io::Error::other(
                "OpenCode native profile, checkout or installed executable changed",
            ));
        }
        prepared.resume =
            json!({"rootID":native["rootID"],"nativeCreatedAtMS":native["nativeCreatedAtMS"]});
        Ok(prepared)
    }

    pub(super) fn opencode_owns(&self, root: &Value) -> bool {
        if self.agent != "opencode" {
            return false;
        }
        let reserved = runtime::read_json(&self.dir.join("session.json")).is_ok_and(|meta| {
            meta["token"] == self.token && meta["opencode_resume"]["rootID"] == *root
        });
        let bound = self
            .opencode_native()
            .is_ok_and(|native| native["rootID"] == *root);
        (reserved || bound)
            && (self.is_live() || !owner_gone(&self.dir, &self.token).unwrap_or(false))
    }

    pub(super) fn reserve(root: &Path, prepared: Prepared) -> io::Result<Self> {
        let Prepared {
            isolated,
            tag,
            cwd,
            label,
            cols,
            rows,
            provider,
            resume,
        } = prepared;
        let id = runtime::random_id()?;
        let checkout = if isolated {
            Some(crate::workspace::plan(&cwd, &id, &label)?)
        } else {
            None
        };
        let label = if label.is_empty() {
            match provider.agent {
                "codex" => "Codex",
                "opencode" => "OpenCode",
                _ => "Claude Code",
            }
        } else {
            &label
        };
        let dir = root.join(&id);
        DirBuilder::new().mode(0o700).create(&dir)?;
        runtime::write_json(
            &dir.join("session.json"),
            &json!({
                "schema": 1, "id": id, "token": runtime::random_id()?, "cwd": cwd, "name": label, "checkout":checkout,
                "created_at": now(), "cols": cols, "rows": rows, "tag":tag,
                "provider": {"name": provider.agent, "version": provider.version,
                    "mode": if provider.agent == "opencode" || provider.agent == "codex" && providers::codex::managed(&provider.version) {
                        "managed"
                    } else { "classic" },
                    "executable": provider.executable, "identity": provider.identity},
                "opencode_profile": (provider.agent == "opencode").then(|| opencode::profile(&provider.env)),
                "opencode_resume": resume,

            }),
        )?;
        let mut session = Self::load(dir)?;
        *session.launch_plan.get_mut().unwrap() = Some(provider);
        session.transition(Phase::Starting);
        Ok(session)
    }

    pub(super) fn start(&self) -> io::Result<()> {
        let provider = self
            .launch_plan
            .lock()
            .unwrap()
            .take()
            .ok_or_else(|| io::Error::other("no unconsumed provider launch plan"))?;
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if !meta["checkout"].is_null() {
            crate::workspace::create(Path::new(&self.cwd), &self.id, &meta["checkout"])
                .inspect_err(|e| {
                    let _ = runtime::write_json(
                        &self.dir.join("exit.json"),
                        &json!({"token":self.token,"error":e.to_string()}),
                    );
                })?;
        }
        let mut shim = Command::new(env::current_exe()?);
        shim.env_clear().envs(provider.env);
        shim.arg("shim")
            .arg("--rust-session")
            .arg(&self.dir)
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null());
        // The child becomes session leader before exec; daemon/terminal teardown
        // has no controlling-terminal or process-group authority over it.
        unsafe {
            shim.pre_exec(|| {
                if libc::setsid() == -1 {
                    return Err(io::Error::last_os_error());
                }
                Ok(())
            });
        }
        let mut child = shim.spawn()?;
        let mut bootstrap = child.stdin.take().expect("piped shim input");
        // Even a long-lived supervisor reaps its detached shims when they exit.
        thread::spawn(move || {
            let _ = child.wait();
        });
        runtime::write_control(
            &mut bootstrap,
            &json!({"argv": provider.argv, "native": provider.native}),
        )
    }

    fn exit(&self) -> io::Result<Value> {
        exit_report(&self.dir, &self.token)
    }

    pub(super) fn await_ready(&self) -> io::Result<()> {
        let deadline = Instant::now()
            + Duration::from_secs(match self.agent.as_str() {
                "opencode" => 45,
                "codex" => 8,
                _ => 3,
            });
        loop {
            if self.dial().is_ok() {
                if self.agent == "opencode" {
                    if let Ok(launch) = runtime::read_json(&self.dir.join("opencode-launch.json"))
                        && launch["token"] == self.token
                    {
                        self.transition(Phase::Running);
                        if launch["ready"] == true {
                            return Ok(());
                        }
                        return Err(io::Error::other(format!(
                            "OpenCode launch unresolved (session {}): {}",
                            self.id,
                            field(&launch, "reason")
                        )));
                    }
                } else {
                    self.transition(Phase::Running);
                    return Ok(());
                }
            }
            if let Ok(exit) = self.exit() {
                self.transition(Phase::Exited);
                let reason = field(&exit, "error");
                return Err(io::Error::other(
                    if reason.is_empty() {
                        "worker exited during launch"
                    } else {
                        reason
                    }
                    .to_owned(),
                ));
            }
            if Instant::now() >= deadline {
                return Err(io::Error::other(
                    "shim startup outcome is uncertain; inspect the roster before retrying",
                ));
            }
            thread::sleep(Duration::from_millis(30));
        }
    }

    fn owner_identity(&self) -> io::Result<(u32, u64)> {
        let owner = runtime::read_json(&self.dir.join("owner.json"))?;
        let pid = owner["pid"]
            .as_u64()
            .and_then(|pid| u32::try_from(pid).ok())
            .filter(|pid| *pid > 1)
            .ok_or_else(|| io::Error::other("invalid shim process identity"))?;
        let birth = runtime::birth(pid)?;
        if field(&owner, "token") != self.token || owner["birth"].as_u64() != Some(birth) {
            return Err(io::Error::other("shim birth identity mismatch"));
        }
        Ok((pid, birth))
    }

    pub(super) fn dial(&self) -> io::Result<UnixStream> {
        let (pid, birth) = self.owner_identity()?;
        let mut stream = UnixStream::connect(self.dir.join("p"))?;
        stream.set_read_timeout(Some(IO_TIMEOUT))?;
        stream.set_write_timeout(Some(IO_TIMEOUT))?;
        if runtime::peer_pid(&stream)? != pid || runtime::birth(pid)? != birth {
            return Err(io::Error::other("shim socket peer identity mismatch"));
        }
        runtime::write_control(
            &mut stream,
            &json!({
                "type": "hello", "wire_version": SHIM_VERSION, "token": self.token
            }),
        )?;
        let reply = runtime::read_control(&mut stream)?;
        if field(&reply, "type") != "hello"
            || reply["wire_version"] != SHIM_VERSION
            || field(&reply, "token") != self.token
        {
            return Err(io::Error::other("shim custody handshake refused"));
        }
        self.proven_owner.get_or_init(|| (pid, birth));
        Ok(stream)
    }

    pub(super) fn kill(&self) -> io::Result<()> {
        if self.exit().is_ok() {
            return Ok(());
        }
        let reply = self.status_exchange(&json!({"type": "signal", "sig": "term"}));
        match reply {
            Ok(reply) if field(&reply, "type") == "ok" => Ok(()),
            _ if self.exit().is_ok() => Ok(()),
            Ok(_) => Err(io::Error::other("shim did not acknowledge termination")),
            Err(err) => Err(err),
        }
    }

    pub(super) fn confirm_exit(&self) -> io::Result<()> {
        match self.exit() {
            Ok(_) => {}
            Err(e) if e.kind() == io::ErrorKind::NotFound => self.kill()?,
            Err(e) => return Err(e),
        }
        let deadline = Instant::now() + Duration::from_secs(6);
        loop {
            match self.exit() {
                Ok(_) if owner_gone(&self.dir, &self.token)? => return Ok(()),
                Ok(_) => {}
                Err(e) if e.kind() != io::ErrorKind::NotFound => return Err(e),
                _ => {}
            }
            if Instant::now() >= deadline {
                return Err(io::Error::other(
                    "session exit is unconfirmed; directory retained",
                ));
            }
            thread::sleep(Duration::from_millis(25));
        }
    }

    pub(super) fn semantic_target(&self) -> io::Result<String> {
        if self.agent == "opencode" {
            self.dial()?;
            let record = self.opencode_native()?;
            let launch = runtime::read_json(&self.dir.join("opencode-launch.json"))?;
            if launch["token"] != self.token || launch["ready"] != true {
                return Err(io::Error::other("OpenCode native launch is unresolved"));
            }
            return Ok(field(&record, "sessionInstance").to_owned());
        }
        if self.agent != "codex" || !self.is_live() {
            return Err(io::Error::other("session has no live Codex app-server"));
        }
        let observed = runtime::read_json(&self.dir.join("observation.json"))?;
        if observed["backend_available"] != true
            || observed["requests_available"] != true
            || observed["token"] != self.token
            || field(&observed, "conversation_id").is_empty()
        {
            return Err(io::Error::other(
                "session has no confirmed Codex app-server thread",
            ));
        }
        Ok(codex_receipt::instance(&self.token))
    }

    pub(super) fn native_requests(&self, session: &str, body: &Value) -> io::Result<Value> {
        if self.agent == "opencode" {
            return self.opencode_exchange(&json!({
                "type":"opencode_requests","session":session,"request_id":body["request_id"],"native_session_id":body["native_session_id"],
            }));
        }
        if !["request_id", "native_session_id"]
            .into_iter()
            .all(|key| body[key].is_null() || body[key] == "")
        {
            return Err(io::Error::other(
                "only native request summary reads are supported",
            ));
        }
        self.semantic_target()?;
        // Confirm continued private shim custody at query time.
        self.dial()?;
        let observed = runtime::read_json(&self.dir.join("observation.json"))?;
        if observed["token"] != self.token || observed["requests_available"] != true {
            return Err(io::Error::other(
                "bound Codex request stream is not available",
            ));
        }
        let requests = observed["requests"]
            .as_array()
            .filter(|requests| requests.len() <= 128)
            .ok_or_else(|| io::Error::other("invalid Codex request projection"))?;
        Ok(if requests.is_empty() {
            json!({"session":session})
        } else {
            json!({"session":session,"requests":requests})
        })
    }

    fn semantic_thread(&self) -> io::Result<String> {
        match runtime::read_json(&self.dir.join("observation.json")) {
            Ok(record)
                if record["token"] == self.token
                    && codex_receipt::canonical_thread(field(&record, "conversation_id")) =>
            {
                Ok(field(&record, "conversation_id").to_owned())
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(String::new()),
            Err(err) => Err(err),
            Ok(_) => Err(io::Error::other("unverified Codex thread identity")),
        }
    }

    pub(super) fn semantic_status(
        &self,
        operation: &str,
        session: &str,
        root: &Path,
    ) -> io::Result<Option<Value>> {
        if self.agent == "opencode" {
            return self.opencode_operation_status(session, operation, "prompt", root);
        }
        let thread = self.semantic_thread()?;
        let record = codex_receipt::load(&self.dir, operation, session, &self.token, &thread)?;
        if let Some(record) = record {
            codex_receipt::verify_binding(
                root,
                operation,
                session,
                &codex_receipt::instance(&self.token),
                field(&record, "intent"),
            )?;
            return Ok(Some(record["receipt"].clone()));
        }
        Ok(None)
    }

    pub(super) fn semantic_send(
        &self,
        request: &Value,
        session: &str,
        state: &super::State,
    ) -> io::Result<Value> {
        let body = &request["composer_send"];
        let operation = field(request, "operation_id");
        let text = field(body, "text");
        let instance = field(body, "session_instance");
        if self.agent == "opencode" {
            if request["body_version"] != 1
                || body["session"] != session
                || body.as_object().is_none_or(|fields| {
                    fields
                        .keys()
                        .any(|key| !matches!(key.as_str(), "session" | "session_instance" | "text"))
                })
                || text.is_empty()
                || text.len() > 4096
                || text.contains('\0')
            {
                return Err(io::Error::other("invalid OpenCode semantic request"));
            }
            return self.opencode_action(session, operation, "prompt", body, state);
        }
        if request["body_version"] != 1
            || body["session"] != session
            || !codex_receipt::timely(operation)
            || text.is_empty()
            || text.len() > 4096
            || body.as_object().is_none_or(|fields| {
                fields.iter().any(|(key, value)| {
                    !matches!(
                        key.as_str(),
                        "session" | "session_instance" | "text" | "expected_turn"
                    ) && !value.is_null()
                })
            })
            || !(body["expected_turn"].is_null() || body["expected_turn"] == "")
            || instance != codex_receipt::instance(&self.token)
            || self.agent != "codex"
        {
            return Err(io::Error::other("invalid or stale Codex semantic request"));
        }
        let thread = self.semantic_thread()?;
        {
            use sha2::{Digest, Sha256};
            let digest = Sha256::digest(operation);
            let _operation = state.operation_locks
                [usize::from(digest[0]) % state.operation_locks.len()]
            .lock()
            .unwrap();
            codex_receipt::bind(&state.root, operation, session, instance, text)?;
        }
        if let Some(record) =
            codex_receipt::load(&self.dir, operation, session, &self.token, &thread)?
        {
            if record["intent"] != codex_receipt::hash(text)
                || record["receipt"]["session"] != session
            {
                return Err(io::Error::other(
                    "semantic operation is already bound to another request",
                ));
            }
            return Ok(record["receipt"].clone());
        }
        self.semantic_target()?;
        let mut shim = self.dial()?;
        shim.set_read_timeout(Some(Duration::from_secs(7)))?;
        runtime::write_control(
            &mut shim,
            &json!({
                "type":"semantic","operation_id":operation,"session":session,
                "session_instance":instance,"text":text,
            }),
        )?;
        let reply = runtime::read_control(&mut shim)?;
        if reply["type"] != "semantic_result" {
            return Err(io::Error::other(field(&reply, "error").to_owned()));
        }
        if reply["receipt"]["operation_id"] != operation || reply["receipt"]["session"] != session {
            return Err(io::Error::other(
                "uncorrelated Codex semantic receipt; outcome unknown",
            ));
        }
        self.semantic_status(operation, session, &state.root)?
            .ok_or_else(|| io::Error::other("semantic receipt is unavailable"))
    }

    fn opencode_native(&self) -> io::Result<Value> {
        let native = opencode::receipts::native(&self.dir, &self.token)?;
        if native["directory"].as_str().map(Path::new) != Some(self.effective.as_path()) {
            return Err(io::Error::other("OpenCode checkout identity changed"));
        }
        Ok(native)
    }

    fn opencode_exchange(&self, request: &Value) -> io::Result<Value> {
        if self.agent != "opencode" {
            return Err(io::Error::other("session has no managed OpenCode runtime"));
        }
        let mut stream = self.dial()?;
        stream.set_read_timeout(Some(Duration::from_secs(30)))?;
        runtime::write_control(&mut stream, request)?;
        let response = runtime::read_control(&mut stream)?;
        if response["type"] != "opencode_result" {
            return Err(io::Error::other(field(&response, "error").to_owned()));
        }
        Ok(response["body"].clone())
    }

    pub(super) fn opencode_history(&self, limit: u64, before: &str) -> io::Result<Value> {
        self.opencode_exchange(&json!({"type":"opencode_history","limit":limit,"before":before}))
    }

    pub(super) fn opencode_action(
        &self,
        session: &str,
        operation: &str,
        kind: &str,
        body: &Value,
        state: &super::State,
    ) -> io::Result<Value> {
        let native = self.opencode_native()?;
        let instance = field(&native, "sessionInstance");
        if body["session"] != session
            || body["session_instance"] != instance
            || !codex_receipt::timely(operation)
        {
            return Err(io::Error::other("invalid or stale OpenCode operation"));
        }
        {
            use sha2::{Digest, Sha256};
            let digest = Sha256::digest(operation);
            let _operation = state.operation_locks
                [usize::from(digest[0]) % state.operation_locks.len()]
            .lock()
            .unwrap();
            codex_receipt::bind(
                &state.root,
                operation,
                session,
                instance,
                &opencode::receipts::intent(body),
            )?;
        }
        if let Some(saved) =
            self.opencode_operation_status(session, operation, kind, &state.root)?
        {
            return Ok(saved);
        }
        let sent = self.opencode_exchange(&json!({
            "type":"opencode_operation","session":session,"operation_id":operation,"kind":kind,"body":body
        }));
        match self.opencode_operation_status(session, operation, kind, &state.root)? {
            Some(saved) => Ok(saved),
            None => match sent {
                Ok(_) => Err(io::Error::other(
                    "OpenCode operation outcome is unknown; inspect --status",
                )),
                Err(error) => Err(error),
            },
        }
    }

    pub(super) fn opencode_operation_status(
        &self,
        session: &str,
        operation: &str,
        kind: &str,
        root: &Path,
    ) -> io::Result<Option<Value>> {
        let Some(mut record) =
            opencode::receipts::read(&self.dir, &self.token, session, operation)?
        else {
            return Ok(None);
        };
        if record["kind"] != kind {
            return Err(io::Error::other(
                "operation belongs to a different OpenCode action",
            ));
        }
        codex_receipt::verify_binding(
            root,
            operation,
            session,
            field(&record["receipt"], "session_instance"),
            &codex_receipt::hash(field(&record, "intent")),
        )?;
        if matches!(
            field(&record["receipt"], "state"),
            "uncertain" | "outcome_unknown" | "native_accepted"
        ) && self
            .opencode_exchange(&json!({
                "type":"opencode_operation","session":session,"operation_id":operation,"kind":kind,
            }))
            .is_ok()
        {
            record = opencode::receipts::read(&self.dir, &self.token, session, operation)?
                .ok_or_else(|| io::Error::other("OpenCode operation receipt disappeared"))?;
        }
        Ok(Some(record["receipt"].clone()))
    }

    pub(super) fn status_exchange(&self, request: &Value) -> io::Result<Value> {
        let mut shim = self.dial()?;
        runtime::write_control(&mut shim, request)?;
        runtime::read_control(&mut shim)
    }

    pub(super) fn reconcile(&self) -> bool {
        let (previous, _) = self.state.lock().unwrap().clone();
        if previous == Phase::Exited {
            return false;
        }
        if previous == Phase::Running
            && self
                .proven_owner
                .get()
                .is_some_and(|(pid, start)| runtime::birth(*pid).ok() == Some(*start))
        {
            return self.refresh_provider_status();
        }
        if self.exit().is_ok() {
            return self.transition(Phase::Exited);
        }
        if self.dial().is_ok() {
            return self.transition(Phase::Running);
        }
        if self.opened.elapsed() >= Duration::from_secs(3) {
            return self.transition(Phase::Lost);
        }
        false
    }

    fn refresh_provider_status(&self) -> bool {
        let Ok(record) = runtime::read_json(&self.dir.join("observation.json")) else {
            return false;
        };
        let stale_open_code = json!({"Process":"running","Turn":"unknown","Interaction":"none","ObservationGap":true});
        let status = if self.agent == "opencode"
            && record["observed_ms"]
                .as_u64()
                .is_none_or(|ms| crate::ipc::spawn::now_ms().saturating_sub(ms) > 15_000)
        {
            &stale_open_code
        } else {
            &record["status"]
        };
        if field(&record, "token") != self.token
            || status["Process"] != "running"
            || !matches!(field(status, "Turn"), "active" | "idle" | "unknown")
            || !matches!(
                field(status, "Interaction"),
                "none" | "permission" | "prompt"
            )
        {
            return false;
        }
        let mut observed = self.observed_status.lock().unwrap();
        if observed.as_ref() == Some(status) {
            return false;
        }
        *observed = Some(status.clone());
        drop(observed);
        self.state.lock().unwrap().1 = now();
        true
    }

    fn transition(&self, next: Phase) -> bool {
        let mut current = self.state.lock().unwrap();
        if current.0 == next || current.0 == Phase::Exited {
            return false;
        }
        *current = (next, now());
        true
    }

    pub(super) fn is_live(&self) -> bool {
        matches!(
            self.state.lock().unwrap().0,
            Phase::Starting | Phase::Running
        )
    }

    pub(super) fn uses_directory(&self, root: &Path) -> bool {
        [Path::new(&self.cwd), self.effective.as_path()]
            .into_iter()
            .any(|p| {
                p.starts_with(root) || p.canonicalize().is_ok_and(|path| path.starts_with(root))
            })
    }
    pub(super) fn checkout_record(&self) -> io::Result<Option<(PathBuf, Value)>> {
        let data = runtime::read_json(&self.dir.join("session.json"))?;
        if data["token"] != self.token || data["id"] != self.id || data["cwd"] != self.cwd {
            return Err(io::Error::other("session reservation identity changed"));
        }
        if data["checkout"].is_null() {
            return Ok(None);
        }
        if data["checkout"]["path"].as_str().map(Path::new) != Some(&self.effective) {
            return Err(io::Error::other("session checkout identity changed"));
        }
        Ok(Some((PathBuf::from(&self.cwd), data["checkout"].clone())))
    }

    pub(super) fn set_label(&self, key: &str, input: &str) -> io::Result<bool> {
        let mut labels = self.labels.lock().unwrap();
        let mut value = sanitize_name(input);
        if key == "tag" {
            value = value.trim().to_owned();
        }
        if (key == "name" && labels.name == value) || (key == "tag" && labels.tag == value) {
            return Ok(false);
        }
        let path = self.dir.join("session.json");
        let mut meta = runtime::read_json(&path)?;
        if field(&meta, "id") != self.id || field(&meta, "token") != self.token {
            return Err(io::Error::other("session reservation identity changed"));
        }
        if key == "name" {
            meta["name_set_at"] = json!(now());
        }
        meta[key] = json!(value);
        runtime::write_json(&path, &meta)?;
        if key == "name" {
            labels.name = value;
        } else {
            labels.tag = value;
        }
        Ok(true)
    }

    pub(super) fn view(&self, endpoint: &str) -> Value {
        let state = self.state.lock().unwrap();
        let process = match state.0 {
            Phase::Starting | Phase::Running => "running",
            Phase::Exited => "exited",
            Phase::Lost => "lost",
        };
        let mut status = json!({"Process": process, "Turn": "unknown", "Interaction": "none"});
        if process == "running"
            && let Some(observed) = self.observed_status.lock().unwrap().as_ref()
        {
            status = observed.clone();
        }
        let group = if process != "running" {
            "completed"
        } else if status["Attention"] == true {
            "needs_input"
        } else if status["Turn"] != "idle" {
            "working"
        } else if status["Interaction"] != "none" {
            "needs_input"
        } else {
            "ready_for_review"
        };
        let labels = self.labels.lock().unwrap();
        let mut view = json!({
            "endpoint_id": endpoint, "id": format!("{endpoint}/{}", self.id),
            "agent": self.agent, "name": labels.name, "cwd": self.cwd,
            "status": status,
            "group": group, "group_entered_at": state.1, "last_activity": state.1,
            "created_at": self.created, "summary": "",
        });
        if !labels.tag.is_empty() {
            view["tag"] = json!(labels.tag);
        }
        view
    }
}

fn now() -> String {
    OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .expect("current UTC timestamp")
}

pub(super) fn exit_report(dir: &Path, token: &str) -> io::Result<Value> {
    let exit = runtime::read_json(&dir.join("exit.json"))?;
    let prelaunch = exit["error"].as_str().is_some_and(|s| !s.is_empty())
        && exit["code"].is_null()
        && exit["signal"].is_null();
    let normal = exit["code"].as_u64().is_some_and(|v| v <= 255) && exit["signal"].is_null();
    let signal = exit["signal"]
        .as_u64()
        .is_some_and(|v| (1..128).contains(&v))
        && exit["code"].is_null();
    if exit["token"] != token || !(prelaunch || exit["error"].is_null() && (normal || signal)) {
        return Err(io::Error::other("unproven session exit report"));
    }
    Ok(exit)
}

pub(super) fn owner_gone(dir: &Path, token: &str) -> io::Result<bool> {
    let owner = match runtime::read_json(&dir.join("owner.json")) {
        Ok(v) => v,
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            // A native exec refusal precedes owner registration.
            return Ok(exit_report(dir, token)?["error"].is_string());
        }
        Err(e) => return Err(e),
    };
    let pid = owner["pid"]
        .as_u64()
        .and_then(|v| i32::try_from(v).ok())
        .filter(|v| *v > 1);
    let birth = owner["birth"].as_u64().filter(|n| *n > 0);
    let (Some(pid), Some(birth)) = (pid, birth) else {
        return Err(io::Error::other("invalid shim owner identity"));
    };
    if owner["token"] != token {
        return Err(io::Error::other("unproven shim owner record"));
    }
    match runtime::birth(pid as u32) {
        Ok(actual) => Ok(actual != birth),
        Err(e) if e.kind() == io::ErrorKind::NotFound || e.raw_os_error() == Some(libc::ESRCH) => {
            Ok(true)
        }
        Err(e) => Err(e),
    }
}
