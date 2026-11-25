//! A bound client of the provider-owned gateway. The shim owns only the viewer.
use crate::{ipc::field, supervisor::runtime};
use serde_json::{Value, json};
use std::fs::{self, OpenOptions};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::os::unix::{
    fs::{FileTypeExt, MetadataExt, OpenOptionsExt},
    net::UnixStream,
};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tungstenite::{Message, WebSocket, client::IntoClientRequest, protocol::WebSocketConfig};

pub(crate) const DESCRIPTOR: &str = "hermes-input.json";
const PROTOCOL: &str = "hermes-gateway-v1";
pub(crate) const NORMAL_ORIGINS: &[&str] =
    &["HERMES_HOME", "HERMES_GATEWAY_LOCK_DIR", "XDG_STATE_HOME"];

pub(crate) fn descriptor_hash(descriptor: &Value) -> String {
    let mut descriptor = descriptor.clone();
    descriptor.sort_all_objects();
    crate::providers::codex_receipt::hash(&descriptor.to_string())
}

pub(crate) fn read_descriptor(dir: &Path, owner: &Value, meta: &Value) -> io::Result<Value> {
    let descriptor = runtime::read_json(&dir.join(DESCRIPTOR))?;
    if owner["hermes_descriptor_sha256"] != descriptor_hash(&descriptor)
        || meta["provider"]["name"] != "hermes"
        || meta["provider"]["mode"] != "shared-native"
        || owner["token"] != meta["token"]
        || descriptor["token"] != meta["token"]
        || descriptor["worker_id"] != meta["id"]
        || descriptor["provider_version"] != meta["provider"]["version"]
        || !runtime::valid_id(field(meta, "id"))
        || !runtime::valid_id(field(meta, "token"))
        || !runtime::valid_id(field(&descriptor, "generation"))
        || owner["pid"].as_u64().is_none_or(|pid| pid <= 1)
        || owner["birth"].as_u64().is_none_or(|birth| birth == 0)
        || owner["worker_pid"].as_u64().is_none_or(|pid| pid <= 1)
        || descriptor["shim_pid"] != owner["pid"]
        || descriptor["shim_birth"] != owner["birth"]
    {
        return Err(io::Error::other(
            "Hermes native descriptor does not match its shim owner",
        ));
    }
    Binding::load(descriptor["binding"].clone())?;
    Ok(descriptor)
}

pub(crate) fn origins(env: &Value, native: bool) -> io::Result<Vec<(String, String)>> {
    let entries = if let Some(list) = env.as_array() {
        list.iter()
            .filter_map(Value::as_str)
            .filter_map(|item| item.split_once('='))
            .map(|(key, value)| (key.to_owned(), value.to_owned()))
            .collect()
    } else {
        std::env::vars_os()
            .filter_map(|(key, value)| Some((key.into_string().ok()?, value.into_string().ok()?)))
            .collect::<Vec<_>>()
    };
    let mut result = Vec::new();
    for (key, value) in entries {
        match key.as_str() {
            "HERMES_TUI_GATEWAY_URL"
            | "HERMES_TUI_SIDECAR_URL"
            | "SWARM_HERMES_UNSUPPORTED_ORIGIN"
                if native && !value.is_empty() =>
            {
                return Err(io::Error::other(
                    "Hermes native input requires a local provider home",
                ));
            }
            key if NORMAL_ORIGINS.contains(&key) && !value.contains('\0') => {
                result.push((key.into(), value))
            }
            _ => {}
        }
    }
    Ok(result)
}

fn private_node(path: &Path, kind: fn(fs::FileType) -> bool) -> io::Result<()> {
    let meta = path.symlink_metadata()?;
    if !kind(meta.file_type())
        || meta.mode() & 0o077 != 0
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(io::Error::other("Hermes control path is not private"));
    }
    Ok(())
}

fn socket_path(home: &Path) -> io::Result<PathBuf> {
    if !home.is_absolute() {
        return Err(io::Error::other("Hermes control home must be absolute"));
    }
    private_node(home, |kind| kind.is_dir())?;
    let direct = home.join("gateway.sock");
    match private_node(&direct, |kind| kind.is_socket()) {
        Ok(()) => return Ok(direct),
        Err(err) if err.kind() == io::ErrorKind::NotFound => {}
        Err(err) => return Err(err),
    }
    let pointer = home.join("gateway.sock.path");
    private_node(&pointer, |kind| kind.is_file())?;
    let mut raw = String::new();
    OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(pointer)?
        .take(4097)
        .read_to_string(&mut raw)?;
    let path = PathBuf::from(raw.trim());
    if raw.len() > 4096
        || !path.is_absolute()
        || path.file_name().is_none_or(|name| name != "control.sock")
    {
        return Err(io::Error::other("invalid Hermes gateway control pointer"));
    }
    private_node(path.parent().unwrap(), |kind| kind.is_dir())?;
    private_node(&path, |kind| kind.is_socket())?;
    Ok(path)
}

fn timeout(deadline: Instant) -> io::Result<Duration> {
    let remaining = deadline.saturating_duration_since(Instant::now());
    if remaining.is_zero() {
        return Err(io::Error::new(
            io::ErrorKind::TimedOut,
            "Hermes native request timed out",
        ));
    }
    Ok(remaining)
}

fn control(
    home: &str,
    verb: &str,
    params: Value,
    expected_pid: Option<u32>,
    deadline: Instant,
) -> io::Result<(u32, Value)> {
    let mut socket = UnixStream::connect(socket_path(Path::new(home))?)?;
    let pid = runtime::peer_pid(&socket)?;
    if pid <= 1 || expected_pid.is_some_and(|expected| expected != pid) {
        return Err(io::Error::other("Hermes control peer changed"));
    }
    socket.set_read_timeout(Some(timeout(deadline)?))?;
    socket.set_write_timeout(Some(timeout(deadline)?))?;
    serde_json::to_writer(
        &mut socket,
        &json!({"protocol":1,"id":1,"verb":verb,"params":params}),
    )?;
    socket.write_all(b"\n")?;
    let mut raw = Vec::new();
    BufReader::new(crate::socket::DeadlineReader(&socket, deadline).take(65537))
        .read_until(b'\n', &mut raw)?;
    if raw.len() > 65536 || raw.last() != Some(&b'\n') {
        return Err(io::Error::other("invalid Hermes control response"));
    }
    let result: Value = serde_json::from_slice(&raw)?;
    if result["id"] != 1 || result["ok"] != true || !result["result"].is_object() {
        return Err(io::Error::other(
            "Hermes provider private bootstrap refused",
        ));
    }
    Ok((pid, result["result"].clone()))
}

#[derive(Clone)]
pub(crate) struct Binding(pub(crate) Value);

fn execution_state(snapshot: &Value) -> &str {
    if let Some(state) = snapshot["execution_state"].as_str() {
        return state;
    }
    // The v0.22.5 snapshot projects the execution handle through running and prompts.
    match snapshot["running"].as_bool() {
        Some(true) => "running",
        Some(false)
            if snapshot["prompts"]
                .as_array()
                .is_some_and(|prompts| !prompts.is_empty()) =>
        {
            "waiting"
        }
        Some(false) => "idle",
        None => "unknown",
    }
}

impl Binding {
    pub(crate) fn load(record: Value) -> io::Result<Self> {
        let binding = Self(record);
        let b = &binding.0;
        if !super::native_id(binding.id())
            || !["home", "control_home", "directory"]
                .iter()
                .all(|key| Path::new(field(b, key)).is_absolute())
            || !["instance_id", "code_sha"]
                .iter()
                .all(|key| !field(b, key).is_empty())
            || b["authority_epoch"].as_u64().is_none_or(|n| n == 0)
            || b["owner_birth"].as_u64().is_none_or(|n| n == 0)
            || binding.pid().is_none()
        {
            return Err(io::Error::other("invalid Hermes native binding"));
        }
        Ok(binding)
    }

    fn pid(&self) -> Option<u32> {
        self.0["owner_pid"]
            .as_u64()
            .and_then(|pid| u32::try_from(pid).ok())
            .filter(|pid| *pid > 1)
    }

    pub(crate) fn id(&self) -> &str {
        field(&self.0, "session_id")
    }

    fn identify(&self, deadline: Instant) -> io::Result<Value> {
        let b = &self.0;
        let pid = self
            .pid()
            .ok_or_else(|| io::Error::other("missing Hermes owner PID"))?;
        if runtime::birth(pid).ok() != b["owner_birth"].as_u64() {
            return Err(io::Error::other("Hermes owner incarnation changed"));
        }
        let (_, endpoint) = control(
            field(b, "control_home"),
            "identify",
            json!({}),
            Some(pid),
            deadline,
        )?;
        if endpoint["pid"] != pid
            || endpoint["state"] != "ready"
            || endpoint["runtime_protocol"] != 1
            || endpoint["hermes_home"] != b["control_home"]
            || endpoint["authority_epoch"] != b["authority_epoch"]
            || endpoint["instance_id"] != b["instance_id"]
            || endpoint["code_sha"] != b["code_sha"]
            || endpoint["served_profiles"]
                .as_array()
                .is_none_or(|profiles| {
                    profiles
                        .iter()
                        .filter(|p| p["profile_id"] == b["home"] && p["home"] == b["home"])
                        .count()
                        != 1
                })
        {
            return Err(io::Error::other("Hermes native owner binding changed"));
        }
        Ok(endpoint)
    }

    fn connect(&self, deadline: Instant) -> io::Result<Client> {
        let b = &self.0;
        let endpoint = self.identify(deadline)?;
        let address: SocketAddr = field(&endpoint, "api_origin")
            .strip_prefix("http://")
            .and_then(|addr| addr.parse().ok())
            .filter(|addr: &SocketAddr| addr.ip().is_loopback() && addr.port() != 0)
            .ok_or_else(|| io::Error::other("Hermes native origin must be explicit loopback"))?;
        let (_, grant) = control(
            field(b, "control_home"),
            "session-ticket",
            json!({"profile_id":b["home"],"instance_id":b["instance_id"],"purpose":"interactive"}),
            self.pid(),
            deadline,
        )?;
        let ticket = field(&grant, "ticket");
        if grant["runtime_protocol"] != 1
            || grant["profile_id"] != b["home"]
            || grant["instance_id"] != b["instance_id"]
            || ticket.is_empty()
            || ticket.len() > 8192
            || !ticket
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || matches!(c, b'-' | b'_' | b'.'))
        {
            return Err(io::Error::other("Hermes native ticket binding changed"));
        }
        let mut request = format!("ws://{address}/api/ws")
            .into_client_request()
            .map_err(|_| io::Error::other("invalid Hermes WebSocket request"))?;
        request.headers_mut().insert(
            "Sec-WebSocket-Protocol",
            format!("{PROTOCOL}, hermes-gateway-ticket.{ticket}")
                .parse()
                .map_err(|_| io::Error::other("invalid Hermes ticket header"))?,
        );
        let stream = TcpStream::connect_timeout(&address, timeout(deadline)?)?;
        stream.set_read_timeout(Some(timeout(deadline)?))?;
        stream.set_write_timeout(Some(timeout(deadline)?))?;
        let (socket, response) = tungstenite::client::client_with_config(
            request,
            stream,
            Some(
                WebSocketConfig::default()
                    .max_message_size(Some(8 << 20))
                    .max_frame_size(Some(8 << 20)),
            ),
        )
        .map_err(|_| io::Error::other("Hermes authenticated loopback handshake failed"))?;
        if response
            .headers()
            .get("Sec-WebSocket-Protocol")
            .and_then(|v| v.to_str().ok())
            != Some(PROTOCOL)
        {
            return Err(io::Error::other("Hermes WebSocket protocol mismatch"));
        }
        Ok(Client {
            socket,
            deadline,
            next: 0,
        })
    }

    fn snapshot(&self, client: &mut Client) -> io::Result<Value> {
        let snapshot = client.rpc("session.resume", json!({"session_id":self.id()}))?;
        if snapshot["session_id"] != self.id()
            || snapshot["authority_epoch"] != self.0["authority_epoch"]
            || snapshot["info"]["cwd"] != self.0["directory"]
            || field(&snapshot, "subscription_id").is_empty()
        {
            return Err(io::Error::other(
                "Hermes session scope or frozen cwd changed",
            ));
        }
        Ok(snapshot)
    }

    fn activity(&self, client: &mut Client, snapshot: &Value) -> io::Result<Value> {
        let interaction = snapshot["prompts"].as_array().and_then(|prompts| {
            if prompts.iter().any(|prompt| prompt["kind"] == "approval") {
                Some("permission")
            } else if !prompts.is_empty() {
                Some("prompt")
            } else {
                None
            }
        });
        if let Some(interaction) = interaction {
            return Ok(
                json!({"Process":"running","Turn":"active","Interaction":interaction,"Attention":true}),
            );
        }
        let params = json!({"session_id":self.id()});
        let processes = client.rpc("process.list", params.clone())?;
        let agents = client.rpc("subagent.list", params)?;
        let lists = (
            processes["processes"].as_array(),
            agents["subagents"].as_array(),
            agents["delegations"].as_array(),
        );
        let (Some(processes), Some(agents), Some(delegations)) = lists else {
            return Err(io::Error::other(
                "Hermes background work state is unavailable",
            ));
        };
        let mut active = !delegations.is_empty()
            || snapshot["pending"]
                .as_array()
                .is_some_and(|pending| !pending.is_empty());
        for process in processes {
            match (process["exited"].as_bool(), field(process, "status")) {
                (Some(exited), _) => active |= !exited,
                (None, "exited" | "completed" | "killed") => {}
                (None, "") => return Err(io::Error::other("invalid Hermes process projection")),
                _ => active = true,
            }
        }
        active |= agents.iter().any(|agent| {
            !matches!(
                field(agent, "status"),
                "completed" | "failed" | "interrupted" | "cancelled"
            )
        });
        match execution_state(snapshot) {
            "running" | "waiting" => active = true,
            "idle" => {}
            _ => return Err(io::Error::other("Hermes execution status is unknown")),
        }
        Ok(
            json!({"Process":"running","Turn":if active {"active"} else {"idle"},
            "Interaction":"none","Attention":false}),
        )
    }

    pub(crate) fn validate(&self) -> io::Result<()> {
        self.identify(Instant::now() + Duration::from_secs(2))
            .map(|_| ())
    }

    pub(crate) fn inspect(&self) -> io::Result<Value> {
        let mut client = self.connect(Instant::now() + Duration::from_secs(3))?;
        let snapshot = self.snapshot(&mut client)?;
        self.activity(&mut client, &snapshot)
    }

    pub(crate) fn submit(&self, operation: &str, text: &str) -> io::Result<Admission> {
        let mut client = self.connect(Instant::now() + Duration::from_secs(5))?;
        self.snapshot(&mut client)?;
        let mut receipt = match client.rpc(
            "prompt.submit",
            json!({"session_id":self.id(),"input_id":operation,"text":text}),
        ) {
            Ok(receipt) => receipt,
            Err(err) => {
                if let Some(refusal) = err.get_ref().and_then(|err| err.downcast_ref::<Refusal>()) {
                    return Ok(Admission::Rejected(refusal.0));
                }
                return Err(err);
            }
        };
        if receipt.is_object() && receipt["input_id"].is_null() {
            // The response is correlated to the current RPC ID.
            receipt["input_id"] = json!(operation);
        }
        if !self.admission(&receipt, operation) {
            return Err(io::Error::other(
                "Hermes native admission acknowledgement is unconfirmed",
            ));
        }
        Ok(Admission::Accepted(receipt))
    }

    pub(crate) fn pending(&self, operation: &str) -> io::Result<Option<Value>> {
        let mut client = self.connect(Instant::now() + Duration::from_secs(2))?;
        let snapshot = self.snapshot(&mut client)?;
        Ok(snapshot["pending"]
            .as_array()
            .and_then(|pending| {
                pending.iter().find(|receipt| {
                    receipt["input_id"] == operation && self.admission(receipt, operation)
                })
            })
            .cloned())
    }

    pub(crate) fn admission(&self, receipt: &Value, operation: &str) -> bool {
        receipt["ref"]["profile_id"] == self.0["home"]
            && receipt["ref"]["session_id"] == self.id()
            && receipt["authority_epoch"] == self.0["authority_epoch"]
            && !field(receipt, "admission_id").is_empty()
            && receipt
                .get("execution_generation")
                .is_some_and(|generation| {
                    generation.is_u64()
                        || generation.is_null()
                            && matches!(field(receipt, "status"), "queued" | "terminal")
                })
            && matches!(field(receipt, "status"), "queued" | "started" | "terminal")
            && receipt["input_id"] == operation
    }

    pub(crate) fn stop(&self) -> io::Result<()> {
        let mut client = self.connect(Instant::now() + Duration::from_secs(5))?;
        let snapshot = self.snapshot(&mut client)?;
        let mut uncertain = !matches!(execution_state(&snapshot), "idle" | "running" | "waiting");
        if let Some(pending) = snapshot["pending"].as_array() {
            for receipt in pending {
                match field(receipt, "status") {
                    "queued"
                        if self.admission(receipt, field(receipt, "input_id"))
                            || receipt["input_id"].is_null()
                                && receipt["ref"]["session_id"] == self.id()
                                && receipt["ref"]["profile_id"] == self.0["home"]
                                && receipt["authority_epoch"] == self.0["authority_epoch"]
                                && !field(receipt, "admission_id").is_empty() =>
                    {
                        uncertain |= client.rpc("prompt.cancel", json!({"session_id":self.id(),"admission_id":receipt["admission_id"]})).is_err();
                    }
                    "started" | "terminal" => {}
                    _ => uncertain = true,
                }
            }
        }
        if matches!(execution_state(&snapshot), "running" | "waiting") {
            uncertain |= snapshot["execution_generation"].as_u64().is_none_or(|generation| generation == 0)
                || client.rpc("session.interrupt", json!({"session_id":self.id(),"execution_generation":snapshot["execution_generation"]})).is_err();
        }
        uncertain |= client
            .rpc("process.stop", json!({"session_id":self.id()}))
            .is_err();
        if uncertain {
            return Err(io::Error::other(
                "Hermes scoped cancellation is unconfirmed",
            ));
        }
        Ok(())
    }

    pub(crate) fn detach(&self, stopped: bool) -> io::Result<()> {
        let mut client = self.connect(Instant::now() + Duration::from_secs(3))?;
        let snapshot = self.snapshot(&mut client)?;
        let mut params =
            json!({"session_id":self.id(),"subscription_id":snapshot["subscription_id"]});
        if stopped {
            params["end_reason"] = json!("swarm_viewer_close");
        }
        let reply = client.rpc("session.detach", params)?;
        if reply["detached"] != true
            || reply["session_id"] != self.id()
            || reply["subscription_id"] != snapshot["subscription_id"]
        {
            return Err(io::Error::other("Hermes viewer detach is unconfirmed"));
        }
        Ok(())
    }
}

pub(crate) fn prepare(
    program: &Path,
    cwd: &Path,
    worker: &str,
    options: &Value,
    env: &[(String, String)],
    retained: &Value,
) -> io::Result<Binding> {
    let deadline = Instant::now() + Duration::from_secs(70);
    if !retained.is_null() {
        let binding = Binding::load(retained.clone())?;
        let mut client = binding.connect(deadline)?;
        binding.snapshot(&mut client)?;
        return Ok(binding);
    }
    let mut args = super::prefix(options);
    args.extend(["gateway".into(), "ensure".into()]);
    let args: Vec<_> = args.iter().map(String::as_str).collect();
    let ensured =
        super::super::command_output(program, cwd, env, &args, Duration::from_secs(60), 65536)?;
    let ensured: Value = serde_json::from_str(&ensured)?;
    let endpoint = &ensured["endpoint"];
    let home = field(endpoint, "profile_id");
    let control_home = endpoint["control_home"]
        .as_str()
        .filter(|s| !s.is_empty())
        .unwrap_or(home);
    if ensured["state"] != "ready"
        || endpoint["runtime_protocol"] != 1
        || endpoint["code_sha"] != ensured["client_code_sha"]
        || field(endpoint, "code_sha").is_empty()
        || field(endpoint, "instance_id").is_empty()
        || endpoint["authority_epoch"].as_u64().is_none_or(|n| n == 0)
        || !Path::new(home).is_absolute()
    {
        return Err(io::Error::other(
            "Hermes ensure result does not prove the provider owner",
        ));
    }
    let (pid, _) = control(control_home, "identify", json!({}), None, deadline)?;
    let mut binding = Binding(json!({"home":home,"control_home":control_home,
        "instance_id":endpoint["instance_id"],"authority_epoch":endpoint["authority_epoch"],"code_sha":endpoint["code_sha"],
        "owner_pid":pid,"owner_birth":runtime::birth(pid)?,"directory":cwd}));
    let mut client = binding.connect(deadline)?;
    if client.rpc("setup.status", json!({}))?["provider_configured"] != true {
        return Err(io::Error::other(
            "Hermes native provider requires setup in its configured home",
        ));
    }
    let mut params = json!({"request_id":format!("swarm:{worker}"),"source":"tui","cwd":cwd});
    for option in ["provider", "model", "reasoning", "toolsets", "skills"] {
        let value = field(options, option);
        if !value.is_empty() {
            params[option] = if matches!(option, "toolsets" | "skills") {
                json!(value.split(',').collect::<Vec<_>>())
            } else {
                json!(value)
            };
        }
    }
    if options["yolo"] == "true" {
        params["yolo"] = json!(true);
    }
    let snapshot = client.rpc("session.create", params)?;
    if snapshot["authority_epoch"] != binding.0["authority_epoch"]
        || snapshot["info"]["cwd"] != binding.0["directory"]
        || !super::native_id(field(&snapshot, "session_id"))
    {
        return Err(io::Error::other(
            "Hermes canonical session creation binding is unconfirmed",
        ));
    }
    binding.0["session_id"] = snapshot["session_id"].clone();
    Ok(binding)
}

pub(crate) enum Admission {
    Accepted(Value),
    Rejected(i64),
}

#[derive(Debug)]
struct Refusal(i64);

impl std::fmt::Display for Refusal {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Hermes native request refused ({})", self.0)
    }
}

impl std::error::Error for Refusal {}

struct Client {
    socket: WebSocket<TcpStream>,
    deadline: Instant,
    next: u32,
}

impl Client {
    fn rpc(&mut self, method: &str, params: Value) -> io::Result<Value> {
        self.next += 1;
        self.socket
            .get_mut()
            .set_write_timeout(Some(timeout(self.deadline)?))?;
        self.socket
            .send(Message::Text(
                json!({"jsonrpc":"2.0","id":self.next,"method":method,"params":params})
                    .to_string()
                    .into(),
            ))
            .map_err(|_| io::Error::other("Hermes native request outcome is unconfirmed"))?;
        loop {
            self.socket
                .get_mut()
                .set_read_timeout(Some(timeout(self.deadline)?))?;
            let message = self
                .socket
                .read()
                .map_err(|_| io::Error::other("Hermes native response is unavailable"))?;
            if let Message::Text(raw) = message {
                let reply: Value = serde_json::from_str(&raw)?;
                if reply["id"] != self.next {
                    continue;
                }
                if let Some(code) = reply["error"]["code"].as_i64() {
                    return Err(io::Error::new(
                        io::ErrorKind::PermissionDenied,
                        Refusal(code),
                    ));
                }
                return reply
                    .get("result")
                    .cloned()
                    .ok_or_else(|| io::Error::other("Hermes native result is missing"));
            }
        }
    }
}
