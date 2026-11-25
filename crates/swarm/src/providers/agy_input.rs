use super::{codex_receipt as receipt, identity, probe_output};
use crate::{ipc::field, supervisor::runtime};
use serde_json::{Value, json};
use std::env;
use std::io;
use std::net::SocketAddr;
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::Duration;

const DESCRIPTOR: &str = "agy-input.json";
const NATIVE_ENV: &[&str] = &[
    "ANTIGRAVITY_CONVERSATION_ID",
    "ANTIGRAVITY_PROJECT_ID",
    "ANTIGRAVITY_TRAJECTORY_ID",
    "ANTIGRAVITY_LS_ADDRESS",
    "ANTIGRAVITY_CSRF_TOKEN",
    "ANTIGRAVITY_AGENTAPI_EXE",
    "ANTIGRAVITY_SOURCE_METADATA",
];

pub(crate) fn qualified(version: &str) -> bool {
    matches!(version, "1.4.0" | "1.4.5" | "1.4.7")
}

pub(crate) fn origin(input: &Value) -> io::Result<Value> {
    let permitted = [
        "kind",
        "installation_id",
        "request_id",
        "actor_id",
        "requester_id",
        "actor_name",
        "guild_id",
        "channel_id",
        "message_id",
        "endorser_id",
        "discussion_id",
        "revision",
    ];
    let mut normalized = input.clone();
    let fields = normalized.as_object_mut();
    let refusal = || io::Error::other("Antigravity native input requires a valid message origin");
    let Some(fields) = fields else {
        return Err(refusal());
    };
    if fields.iter().any(|(key, field)| {
        !permitted.contains(&key.as_str())
            || !field
                .as_str()
                .is_some_and(|s| s.len() <= 512 && !s.chars().any(char::is_control))
    }) {
        return Err(refusal());
    }
    fields.retain(|_, field| field != "");
    let value = |key| field(&normalized, key);
    let kind = value("kind");
    if !matches!(kind, "external" | "owner_direct" | "owner_endorsed")
        || ["installation_id", "request_id", "actor_id"]
            .into_iter()
            .any(|key| value(key).is_empty())
        || (kind == "owner_endorsed") == value("endorser_id").is_empty()
    {
        return Err(refusal());
    }
    if !value("discussion_id").is_empty() {
        let personal = kind == "owner_direct" && value("requester_id") == value("actor_id");
        if [
            "discussion_id",
            "revision",
            "requester_id",
            "channel_id",
            "message_id",
        ]
        .into_iter()
        .any(|key| value(key).is_empty())
            || kind == "owner_endorsed"
            || (value("guild_id").is_empty() && !personal)
        {
            return Err(refusal());
        }
    }
    normalized.sort_all_objects();
    Ok(normalized)
}

pub(crate) fn intent(text: &str, origin: &Value) -> io::Result<String> {
    Ok(receipt::hash(&serde_json::to_string(&(text, origin))?))
}

pub(crate) fn cli(args: &[String]) -> u8 {
    let [command, path] = args else {
        eprintln!("usage: swarm native-input register-agy <session descriptor>");
        return 2;
    };
    let path = Path::new(path);
    if command != "register-agy"
        || !path.is_absolute()
        || path.file_name().and_then(|name| name.to_str()) != Some(DESCRIPTOR)
    {
        eprintln!("usage: swarm native-input register-agy <session descriptor>");
        return 2;
    }
    if submit_registration(path).is_err() {
        eprintln!("native input registration could not prove this Antigravity tool context");
        return 1;
    }
    0
}

fn submit_registration(path: &Path) -> io::Result<()> {
    let mut source = serde_json::Map::new();
    for &key in NATIVE_ENV {
        source.insert(key.into(), json!(env::var(key).unwrap_or_default()));
    }
    let desc = runtime::read_json(path)?;
    let dir = path.parent().expect("absolute descriptor");
    let session = env::var("SWARM_SESSION_ID").unwrap_or_default();
    let token = field(&desc, "registration_token");
    if desc["schema"] != 1
        || desc["session_id"] != session
        || !runtime::valid_id(&session)
        || !runtime::valid_id(token)
        || dir.file_name().and_then(|name| name.to_str()) != Some(session.as_str())
    {
        return Err(io::Error::other("invalid registration descriptor"));
    }
    let mut peer = UnixStream::connect(dir.join("p"))?;
    peer.set_write_timeout(Some(Duration::from_secs(2)))?;
    peer.set_read_timeout(Some(Duration::from_secs(12)))?;
    runtime::write_control(
        &mut peer,
        &json!({
            "type":"agy_register", "token":token, "session_id":session,
            "cwd":env::current_dir()?, "source":source,
        }),
    )?;
    if runtime::read_control(&mut peer)?["type"] != "ok" {
        return Err(io::Error::other("registration refused"));
    }
    Ok(())
}

#[derive(PartialEq, Eq)]
pub(crate) struct Source {
    env: Vec<(String, String)>,
}

impl Source {
    pub(crate) fn conversation(&self) -> &str {
        &self.env[0].1
    }

    fn project(&self) -> &str {
        &self.env[1].1
    }
}

pub(crate) struct Broker {
    dir: PathBuf,
    command: String,
    session: String,
    executable: PathBuf,
    pinned: Value,
    cwd: PathBuf,
    version: String,
    environment: Vec<(String, String)>,
    registration_token: String,
    generation: String,
    installation: String,
    owner_socket: String,
    pub joined: Mutex<Option<Source>>,
}

impl Broker {
    pub(crate) fn prepare(
        dir: &Path,
        config: &Value,
        cwd: &Path,
        environment: &[(String, String)],
        owner_socket: &str,
    ) -> io::Result<Self> {
        let provider = &config["provider"];
        let version = field(provider, "version");
        if provider["name"] != "agy" || !qualified(version) {
            return Err(io::Error::other("unqualified Antigravity broker"));
        }
        let session = field(config, "id");
        let registration_token = runtime::random_id()?;
        let descriptor = dir.join(DESCRIPTOR);
        let shim = env::current_exe()?;
        let quote = |path: &Path| format!("'{}'", path.to_string_lossy().replace('\'', "'\\''"));
        let command = format!(
            "{} native-input register-agy {}",
            quote(&shim),
            quote(&descriptor)
        );
        runtime::write_json(
            &descriptor,
            &json!({
                "schema":1, "session_id":session, "registration_token":registration_token,
            }),
        )?;
        Ok(Self {
            dir: dir.to_owned(),
            command,
            session: session.into(),
            executable: PathBuf::from(field(provider, "executable")),
            pinned: provider["identity"].clone(),
            cwd: cwd.canonicalize()?,
            version: version.into(),
            environment: environment.to_vec(),
            registration_token,
            generation: runtime::random_id()?,
            installation: field(provider, "agy_native_installation").into(),
            owner_socket: owner_socket.into(),
            joined: Mutex::new(None),
        })
    }

    pub(crate) fn launch_args(&self, argv: &mut Vec<String>) {
        let prefix = format!(
            "Before handling the request below, register this managed session's native message input once by running this exact command in your current workspace, using normal tool permissions: {}\nThis registration grants no tool approvals. If it is declined, continue the owner's work and report that native messaging remains unavailable.\n\nOwner request:\n",
            self.command
        );
        // The classic adapter composes at most one --prompt-interactive pair at the end.
        if argv.len() >= 2 && argv[argv.len() - 2] == "--prompt-interactive" {
            argv.last_mut()
                .expect("interactive prompt")
                .insert_str(0, &prefix);
        } else {
            argv.extend([
                "--prompt-interactive".into(),
                prefix + "No additional request; finish after registration.",
            ]);
        }
    }

    pub(crate) fn validate_registration(&self, request: &Value) -> io::Result<Source> {
        let forbidden = || io::Error::other("unproven Antigravity tool context");
        if request["token"] != self.registration_token || request["session_id"] != self.session {
            return Err(forbidden());
        }
        let vars = &request["source"];
        let limits = [36, 128, 128, 128, 1024, 4096, 16 * 1024];
        if !vars.is_object()
            || vars
                .as_object()
                .is_some_and(|obj| obj.len() != limits.len())
        {
            return Err(forbidden());
        }
        let mut source = Source {
            env: Vec::with_capacity(limits.len()),
        };
        for (&name, size) in NATIVE_ENV.iter().zip(limits) {
            let value = field(vars, name);
            if value.is_empty() || value.len() > size || value.contains('\0') {
                return Err(forbidden());
            }
            source.env.push((name.into(), value.into()));
        }
        if !receipt::canonical_thread(source.conversation())
            || !loopback(&source.env[3].1)
            || Path::new(&source.env[5].1) != self.executable
        {
            return Err(forbidden());
        }
        let metadata: Value = serde_json::from_str(&source.env[6].1)?;
        let tool = &metadata["tool"];
        let call = &tool["toolCall"];
        let args: Value = serde_json::from_str(field(call, "argumentsJson"))?;
        let source_cwd = Path::new(field(request, "cwd"));
        if tool["conversationId"] != source.conversation()
            || tool["stepIndex"].as_u64().is_none()
            || call["name"] != "run_command"
            || args["CommandLine"] != self.command
            || !source_cwd.is_absolute()
            || source_cwd.canonicalize().ok().as_deref() != Some(self.cwd.as_path())
            || self.version == "1.4.0" && Path::new(field(&args, "Cwd")) != self.cwd
        {
            return Err(forbidden());
        }
        let version = self.run(&source, &["--version"])?;
        if version.trim() != self.version {
            return Err(forbidden());
        }
        self.verify(&source)?;
        Ok(source)
    }

    pub(crate) fn verify(&self, source: &Source) -> io::Result<()> {
        let reply: Value = serde_json::from_str(&self.run(
            source,
            &[
                "agentapi",
                "get-conversation-metadata",
                source.conversation(),
            ],
        )?)?;
        let meta = &reply["response"]["conversationMetadata"]["metadata"];
        if reply.get("error").is_some()
            || meta["rootConversationId"] != source.conversation()
            || meta["projectId"] != source.project()
        {
            return Err(io::Error::other(
                "native metadata does not prove the registered owner",
            ));
        }
        Ok(())
    }

    pub(crate) fn send(
        &self,
        source: &Source,
        token: &str,
        request: &Value,
        ensure_live: impl FnOnce() -> io::Result<()>,
    ) -> io::Result<Value> {
        let operation = field(request, "operation_id");
        let session = field(request, "session");
        let text = field(request, "text");
        let instance = receipt::provider_instance("agy", token);
        let conversation = source.conversation();
        if !receipt::timely(operation)
            || text.is_empty()
            || text.len() > 4096
            || text.contains('\0')
            || request["session_instance"] != instance
            || session
                .split_once('/')
                .is_none_or(|(_, local)| local != self.session)
        {
            return Err(io::Error::other(
                "invalid or stale Antigravity semantic request",
            ));
        }
        let origin = origin(&request["origin"])?;
        let hash = intent(text, &origin)?;
        if let Some(prior) =
            receipt::load_for(&self.dir, operation, session, token, conversation, "agy")?
        {
            if prior["intent"] != hash {
                return Err(io::Error::other(
                    "semantic operation is already bound to another request",
                ));
            }
            return Ok(prior["receipt"].clone());
        }

        self.verify(source)?;
        ensure_live()?;
        receipt::fence_for(&self.dir, operation, session, token, conversation, "agy")?;
        let mut result = json!({
            "operation_id":operation, "session":session, "session_instance":instance,
            "state":"outcome_unknown", "agent":"agy", "native_session_id":conversation,
            "native_generation":self.generation, "origin":origin,
        });
        receipt::save(&self.dir, operation, &hash, &result)?;
        let supervisor = self
            .dir
            .parent()
            .and_then(Path::parent)
            .ok_or_else(|| io::Error::other("missing supervisor root"))?;
        let composer =
            json!({"session":session,"session_instance":instance,"text":text,"origin":origin});
        let _connector = match crate::discord::lock_connector_worker_input(
            supervisor,
            &self.owner_socket,
            &self.installation,
            "agy",
            operation,
            &composer,
        ) {
            Ok(guard) => guard,
            Err(err) if err.kind() == io::ErrorKind::Interrupted => return Ok(result),
            Err(_) => {
                result["state"] = json!("rejected");
                receipt::save(&self.dir, operation, &hash, &result)?;
                return Ok(result);
            }
        };
        if let Ok(raw) = self.run(source, &["agentapi", "send-message", conversation, text])
            && let Ok(reply) = serde_json::from_str::<Value>(&raw)
            && reply.get("error").is_none()
            && reply["response"]["sendMessage"]["recipientId"] == conversation
            && reply["response"]["sendMessage"]["content"] == text
        {
            result["state"] = json!("submitted");
            receipt::save(&self.dir, operation, &hash, &result)?;
        }
        Ok(result)
    }

    fn run(&self, source: &Source, args: &[&str]) -> io::Result<String> {
        if identity(&self.executable)? != self.pinned {
            return Err(io::Error::other("Antigravity executable identity changed"));
        }
        let mut environment = self.environment.clone();
        environment.extend_from_slice(&source.env);
        let result = probe_output(&self.executable, &self.cwd, &environment, args)?;
        if identity(&self.executable)? != self.pinned {
            return Err(io::Error::other("Antigravity executable identity changed"));
        }
        Ok(result)
    }
}

fn loopback(address: &str) -> bool {
    if let Some(port) = address.strip_prefix("localhost:") {
        return port.parse::<u16>().is_ok_and(|port| port != 0);
    }
    address
        .parse::<SocketAddr>()
        .is_ok_and(|addr| addr.ip().is_loopback() && addr.port() != 0)
}
