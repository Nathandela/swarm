// The Go store remains authoritative throughout stopped-session compatibility.
pub(super) mod export;
pub(super) mod live;

use super::runtime;
use crate::ipc::field;
use serde::de::{DeserializeOwned, IgnoredAny};
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::ffi::CStr;
use std::fs::{self, File};
use std::io::{self, Write};
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

pub(super) const OPT_IN: &str = "SWARM_DAEMON_STOPPED_GO_COMPAT";

pub(super) struct Store {
    root: PathBuf,
    rows: Mutex<BTreeMap<String, Entry>>,
}

const ROLE_TEXT_FIELDS: &[&str] = &[
    "role",
    "manager_conversation_id",
    "manager_replay_id",
    "manager_resume_request_hash",
    "manager_resume_operation_id",
    "manager_auth_file",
    "manager_bridge_socket",
    "manager_claude_config_dir",
    "scheduled_codex_sandbox",
    "schedule_attempt",
    "worker_discussion",
    "worker_attempt",
    "worker_instance",
    "worker_prompt_hash",
    "worker_parent_input_hash",
    "spawned_from",
    "spawn_intent",
    "supervision",
    "required_runtime",
    "input_embargo",
    "resumed_from",
];
const ROLE_OBJECT_FIELDS: &[&str] = &["manager_retirement", "login_runtime", "opencode_binding"];
const ROLE_BOOL_FIELDS: &[&str] = &[
    "hermes_native",
    "manager_native_cli",
    "scheduled_completed",
    "schedule_projected",
    "schedule_exhausted",
    "roster_hidden",
];
const ROSTER_CUSTODY_FIELDS: &[&str] = &[
    "schema_version",
    "id",
    "agent_type",
    "name",
    "tag",
    "cwd",
    "agent_cwd",
    "created_at",
    "last_activity",
    "group_entered_at",
    "status",
    "exit_code",
    "shim_pid",
    "shim_start_time",
    "shim_wire_version",
    "launch_options",
    "conversation_id",
    "env",
    "cli_identity",
    "backend_plan_error",
    "resume_error",
];

#[derive(Clone, PartialEq, Eq)]
struct RawFields(BTreeMap<String, String>);

fn json_space(text: &str) -> &str {
    text.trim_start_matches([' ', '\n', '\r', '\t'])
}

fn next_json<T: DeserializeOwned>(input: &str) -> io::Result<(T, &str, &str)> {
    let mut stream = serde_json::Deserializer::from_str(input).into_iter::<T>();
    let value = stream
        .next()
        .ok_or_else(|| io::Error::other("missing Go JSON value"))??;
    let (raw, rest) = input.split_at(stream.byte_offset());
    Ok((value, raw, json_space(rest)))
}

impl RawFields {
    fn parse(text: &str) -> io::Result<Self> {
        let mut rest = json_space(text)
            .strip_prefix('{')
            .map(json_space)
            .ok_or_else(|| io::Error::other("Go metadata must be a JSON object"))?;
        let mut fields = BTreeMap::new();
        if !rest.starts_with('}') {
            loop {
                let (key, _, after_key) = next_json::<String>(rest)?;
                let after_colon = after_key
                    .strip_prefix(':')
                    .map(json_space)
                    .ok_or_else(|| io::Error::other("missing Go metadata field separator"))?;
                let (_, raw, after_value) = next_json::<IgnoredAny>(after_colon)?;
                if fields.insert(key, raw.into()).is_some() {
                    return Err(io::Error::other("duplicate Go metadata field"));
                }
                if let Some(after_comma) = after_value.strip_prefix(',') {
                    rest = json_space(after_comma);
                } else {
                    rest = after_value;
                    break;
                }
            }
        }
        if rest.strip_prefix('}').map(json_space) != Some("") {
            return Err(io::Error::other("invalid Go metadata object boundary"));
        }
        Ok(Self(fields))
    }

    fn write(&self, mut output: impl Write) -> io::Result<()> {
        output.write_all(b"{")?;
        for (index, (key, value)) in self.0.iter().enumerate() {
            if index > 0 {
                output.write_all(b",")?;
            }
            serde_json::to_writer(&mut output, key)?;
            output.write_all(b":")?;
            output.write_all(value.as_bytes())?;
        }
        output.write_all(b"}\n")
    }

    fn parsed(&self) -> io::Result<Value> {
        let mut fields = serde_json::Map::new();
        for key in ROSTER_CUSTODY_FIELDS
            .iter()
            .chain(ROLE_TEXT_FIELDS)
            .chain(ROLE_OBJECT_FIELDS)
            .chain(ROLE_BOOL_FIELDS)
        {
            // encoding/json accepts case-insensitive field names. Do not let
            // an alternate spelling silently change Go's interpretation.
            if self
                .0
                .keys()
                .any(|raw| raw != *key && raw.eq_ignore_ascii_case(key))
            {
                return Err(io::Error::other(format!(
                    "ambiguous Go metadata field {key}"
                )));
            }
            if let Some(raw) = self.0.get(*key) {
                if *key == "status" {
                    Self::parse(raw)?;
                }
                let parsed = serde_json::from_str(raw).map_err(|error| {
                    io::Error::other(format!("invalid Go metadata field {key}: {error}"))
                })?;
                fields.insert((*key).to_owned(), parsed);
            }
        }
        Ok(Value::Object(fields))
    }
}

struct Entry {
    dir: File,
    meta: Value,
    raw: RawFields,
    needs_sync: bool,
    exported: bool,
    live: Option<Arc<live::Owner>>,
    observed_exit: Option<Value>,
}

impl Entry {
    fn set_label(
        &mut self,
        key: &str,
        value: String,
        sync: impl FnOnce(&File) -> io::Result<()>,
    ) -> io::Result<bool> {
        if self.meta[key] != value {
            let mut revised = self.raw.clone();
            revised
                .0
                .insert(key.to_owned(), serde_json::to_string(&value)?);
            if key == "name" {
                let now = OffsetDateTime::now_utc()
                    .format(&Rfc3339)
                    .expect("UTC timestamp");
                revised
                    .0
                    .insert("name_set_at".into(), serde_json::to_string(&now)?);
            }
            replace_meta(&self.dir, &revised, || {})?;
            // renameat committed the new name. Keep the cache aligned even if
            // directory sync fails, and require a successful sync to acknowledge a retry.
            self.meta[key] = json!(value);
            self.raw = revised;
            self.needs_sync = true;
        }
        if self.needs_sync {
            sync(&self.dir)?;
            self.needs_sync = false;
            return Ok(true);
        }
        Ok(false)
    }
}

fn open_at(dir: &File, name: &CStr, flags: libc::c_int) -> io::Result<File> {
    // The directory descriptor pins the lookup and O_NOFOLLOW protects the final component.
    let fd = unsafe {
        libc::openat(
            dir.as_raw_fd(),
            name.as_ptr(),
            flags | libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_CLOEXEC,
            0o600,
        )
    };
    if fd == -1 {
        return Err(io::Error::last_os_error());
    }
    Ok(unsafe { File::from_raw_fd(fd) })
}

fn read_at(dir: &File, name: &CStr) -> io::Result<Value> {
    runtime::decode_json(open_at(dir, name, libc::O_RDONLY)?)
}

fn read_meta(dir: &File) -> io::Result<RawFields> {
    let file = open_at(dir, c"meta.json", libc::O_RDONLY)?;
    let bytes = runtime::read_private_file(file, 1 << 20)?;
    RawFields::parse(std::str::from_utf8(&bytes).map_err(io::Error::other)?)
}

fn same_file(a: &fs::Metadata, b: &fs::Metadata) -> bool {
    a.ino() == b.ino() && a.dev() == b.dev()
}

fn local_id(id: &str) -> bool {
    (1..=128).contains(&id.len())
        && !id.starts_with('-')
        && !matches!(id, "." | "..")
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
}

pub(super) fn timestamp(value: &Value) -> Option<OffsetDateTime> {
    OffsetDateTime::parse(value.as_str()?, &Rfc3339).ok()
}

fn nonzero_time(value: &Value) -> bool {
    timestamp(value).is_some_and(|t| t.year() > 1)
}

fn inactive(pid: u32, birth: u64) -> io::Result<()> {
    match runtime::birth(pid) {
        Ok(observed) if observed != birth => Ok(()),
        Err(e) if e.kind() == io::ErrorKind::NotFound || e.raw_os_error() == Some(libc::ESRCH) => {
            Ok(())
        }
        _ => Err(io::Error::other("live or uncertain process identity")),
    }
}

fn qualified_role(meta: &Value) -> io::Result<()> {
    let schema = meta["schema_version"].as_u64();
    if schema != Some(1) {
        return Err(io::Error::other(format!(
            "Go schema {schema:?} requires offline qualification"
        )));
    }
    if !matches!(field(meta, "agent_type"), "claude" | "agy" | "codex")
        || ROLE_TEXT_FIELDS
            .iter()
            .any(|key| !meta[*key].is_null() && meta[*key] != "")
        || ROLE_OBJECT_FIELDS.iter().any(|key| !meta[*key].is_null())
        || ROLE_BOOL_FIELDS
            .iter()
            .any(|key| !meta[*key].is_null() && meta[*key] != false)
        || meta["agent_cwd"]
            .as_str()
            .is_some_and(|p| !p.is_empty() && meta["cwd"] != p)
        || !meta["launch_options"]["worktree"].is_null()
        || meta["status"]["NativeWorkUnknown"] == true
    {
        return Err(io::Error::other(
            "unqualified Go role, lineage, workspace or native custody",
        ));
    }
    Ok(())
}

fn exited(file: &File, meta: &Value) -> io::Result<()> {
    let exit = read_at(file, c"exit.json")
        .map_err(|e| io::Error::other(format!("Go exit evidence unavailable: {e}")))?;
    if meta["exit_code"].as_i64().is_none()
        || exit["exit_code"] != meta["exit_code"]
        || !nonzero_time(&exit["finished_at"])
        || timestamp(&exit["finished_at"]) != timestamp(&meta["last_activity"])
    {
        return Err(io::Error::other("Go exit evidence disagrees with metadata"));
    }
    let pid = meta["shim_pid"]
        .as_u64()
        .and_then(|pid| u32::try_from(pid).ok())
        .filter(|pid| (2..=i32::MAX as u32).contains(pid));
    let birth = meta["shim_start_time"].as_u64().filter(|birth| *birth > 0);
    let (Some(pid), Some(birth)) = (pid, birth) else {
        return Err(io::Error::other("missing Go shim birth identity"));
    };
    inactive(pid, birth)?;
    absent_sidefiles(
        file,
        &[
            c"shim.sock",
            c"hooks.sock",
            c"hook.sock",
            c"codex.sock",
            c"backend.json",
        ],
    )?;
    let launch = launch_evidence(file, meta)?;
    if meta["agent_type"] == "agy" {
        // The classic agy shim writes exit.json after joining its PTY group.
        // Managed-native custody uses a different proof and is excluded here.
        absent_sidefiles(
            file,
            &[c"native-process.json", c"native-writers-stopped.json"],
        )?;
        if !exit["backend_exit"].is_null() {
            return Err(io::Error::other("unqualified Go agy backend exit"));
        }
        return Ok(());
    }
    let native = read_at(file, c"native-process.json")
        .map_err(|e| io::Error::other(format!("Go native identity unavailable: {e}")))?;
    let stopped = read_at(file, c"native-writers-stopped.json")
        .map_err(|e| io::Error::other(format!("Go native stop proof unavailable: {e}")))?;
    if native["schema_version"] != 3
        || stopped["schema_version"] != 3
        || stopped["native"] != native
        || stopped["writers_stopped"] != true
        || native["shim_pid"] != meta["shim_pid"]
        || native["shim_start_time"] != meta["shim_start_time"]
        || native["provider"] != meta["agent_type"]
        || field(&native, "generation").is_empty()
        || !native["incident_id"].is_null()
        || meta["agent_type"] == "codex"
            && native["runtime"] != json!({"provider":"","epoch":0,"identity":"","home":""})
    {
        return Err(io::Error::other(
            "Go native writer stop proof disagrees with session custody",
        ));
    }
    let native_pid = native["pid"]
        .as_u64()
        .and_then(|pid| u32::try_from(pid).ok())
        .filter(|pid| (2..=i32::MAX as u32).contains(pid));
    let native_birth = native["start_time"].as_u64().filter(|birth| *birth > 0);
    let (Some(native_pid), Some(native_birth)) = (native_pid, native_birth) else {
        return Err(io::Error::other("missing Go native writer birth identity"));
    };
    if native["pid"] != native["pgid"] {
        return Err(io::Error::other(
            "unproven Go native process group identity",
        ));
    }
    inactive(native_pid, native_birth)?;
    if meta["agent_type"] == "codex" && !field(&launch, "backend_program").is_empty() {
        let backend = native["backend_pid"]
            .as_u64()
            .and_then(|pid| u32::try_from(pid).ok())
            .filter(|backend| {
                (2..=i32::MAX as u32).contains(backend) && *backend != native_pid && *backend != pid
            });
        let epoch = native["backend_start_time"]
            .as_u64()
            .filter(|value| *value > 0);
        let (Some(backend), Some(epoch)) = (backend, epoch) else {
            return Err(io::Error::other("missing Go Codex backend birth identity"));
        };
        if exit["backend_exit"].as_i64().is_none() {
            return Err(io::Error::other("missing Go Codex backend exit"));
        }
        inactive(backend, epoch)
    } else if !native["backend_pid"].is_null()
        || !native["backend_start_time"].is_null()
        || !exit["backend_exit"].is_null()
    {
        Err(io::Error::other("unexpected Go session backend custody"))
    } else {
        Ok(())
    }
}

fn absent_sidefiles(file: &File, names: &[&CStr]) -> io::Result<()> {
    for name in names {
        let mut info = std::mem::MaybeUninit::<libc::stat>::uninit();
        let present = unsafe {
            libc::fstatat(
                file.as_raw_fd(),
                name.as_ptr(),
                info.as_mut_ptr(),
                libc::AT_SYMLINK_NOFOLLOW,
            )
        };
        if present == 0 || io::Error::last_os_error().kind() != io::ErrorKind::NotFound {
            return Err(io::Error::other("Go shim or backend custody remains"));
        }
    }
    Ok(())
}

fn completed(file: &File, meta: &Value) -> io::Result<Option<Value>> {
    let exit = match read_at(file, c"exit.json") {
        Ok(exit) => exit,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e),
    };
    let mut ended = meta.clone();
    ended["status"]["Process"] = json!("exited");
    ended["exit_code"] = exit["exit_code"].clone();
    ended["last_activity"] = exit["finished_at"].clone();
    ended["group_entered_at"] = exit["finished_at"].clone();
    exited(file, &ended)?;
    Ok(Some(ended))
}

fn launch_evidence(file: &File, meta: &Value) -> io::Result<Value> {
    let launch = read_at(file, c"shim-launch.json")
        .map_err(|e| io::Error::other(format!("Go launch evidence unavailable: {e}")))?;
    // The original directory can differ on an offline copy, but the durable
    // ID and config's two physical path components must all identify the same session.
    let launch_dir = Path::new(field(&launch, "session_dir"));
    if launch["session_id"] != meta["id"]
        || launch["cwd"] != meta["cwd"]
        || !launch_dir.is_absolute()
        || launch_dir.file_name().and_then(|n| n.to_str()) != meta["id"].as_str()
        || Path::new(field(&launch, "socket_path")) != launch_dir.join("shim.sock")
        || (meta["agent_type"] == "claude" && launch["native_provider"] != "claude")
        || (meta["agent_type"] == "agy" && !classic_agy(meta, &launch))
        || (meta["agent_type"] == "codex" && !classic_codex(meta, &launch))
        || [
            "claude_input",
            "require_launch_identity",
            "scheduled_codex_backend",
            "backend_required",
        ]
        .iter()
        .any(|key| !launch[*key].is_null() && launch[*key] != false)
        || ["login_runtime", "opencode", "hermes_input", "agy_input"]
            .iter()
            .any(|key| !launch[*key].is_null())
        || (meta["agent_type"] != "codex" && backend_declared(&launch))
        || ["manager_replay_id", "input_embargo"]
            .iter()
            .any(|key| !launch[*key].is_null() && launch[*key] != "")
    {
        return Err(io::Error::other(
            "Go launch binding or native writer is unqualified",
        ));
    }
    Ok(launch)
}

fn executable_binding(meta: &Value, launch: &Value, agent: &str) -> bool {
    let identity = &launch["cli_identity"];
    let executable = Path::new(field(identity, "path"));
    let fingerprint = field(identity, "fingerprint");
    identity.is_object()
        && *identity == meta["cli_identity"]
        && executable.is_absolute()
        && executable.file_name().and_then(|n| n.to_str()) == Some(agent)
        && fingerprint.len() == 64
        && fingerprint.bytes().all(|b| b.is_ascii_hexdigit())
        && launch["argv"].as_array().and_then(|args| args.first()) == Some(&identity["path"])
}

fn backend_declared(launch: &Value) -> bool {
    ["backend_program", "backend_socket_path", "backend_cwd"]
        .iter()
        .any(|key| !launch[*key].is_null() && launch[*key] != "")
        || [
            "backend_args",
            "backend_agent_args",
            "backend_agent_command_args",
            "backend_env",
        ]
        .iter()
        .any(|key| !launch[*key].is_null() && !launch[*key].as_array().is_some_and(Vec::is_empty))
}

fn classic_codex(meta: &Value, launch: &Value) -> bool {
    const NETWORK: &str = "sandbox_workspace_write.network_access=true";
    let mut argv = vec![launch["cli_identity"]["path"].clone()];
    // The pinned Go builder gives a fresh ordinary TUI a fixed flag order.
    // A trailing prompt is deliberately outside this proof because it can be a subcommand.
    for (option, flag) in [("model", "--model"), ("sandbox", "--sandbox")] {
        let value = field(&meta["launch_options"], option);
        if !value.is_empty() {
            argv.extend([json!(flag), json!(value)]);
        }
    }
    argv.extend([json!("-c"), json!(NETWORK)]);
    if launch["native_provider"] != "codex"
        || meta["shim_wire_version"] != 2
        || !executable_binding(meta, launch, "codex")
        || launch["argv"] != json!(argv)
        || !launch["backend_env"].is_null()
            && !launch["backend_env"]
                .as_array()
                .is_some_and(|items| items.iter().all(Value::is_string))
        || crate::providers::codex::version(field(&launch["cli_identity"], "version")).is_err()
    {
        return false;
    }
    if !backend_declared(launch) {
        return true;
    }
    let socket = Path::new(field(launch, "session_dir")).join("codex.sock");
    let address = format!("unix://{}", socket.display());
    Path::new(field(launch, "backend_socket_path")) == socket
        && launch["backend_program"] == launch["cli_identity"]["path"]
        && launch["backend_cwd"] == launch["cwd"]
        && launch["backend_args"] == json!(["app-server", "--listen", address, "-c", NETWORK])
        && launch["backend_agent_args"] == json!(["--remote", address])
        && launch["backend_agent_command_args"].is_null()
}

fn classic_agy(meta: &Value, launch: &Value) -> bool {
    if !launch["native_provider"].is_null()
        || meta["shim_wire_version"] != 2
        || !executable_binding(meta, launch, "agy")
        || crate::providers::agy::version(field(&launch["cli_identity"], "version")).is_err()
        || !launch["env"].as_array().is_some_and(|values| {
            values.iter().all(|v| {
                v.as_str().is_some_and(|v| {
                    !v.starts_with("SWARM_AGY_ACP") && !v.starts_with("SWARM_SCHEDULE")
                })
            })
        })
    {
        return false;
    }
    let remaining = &launch["argv"]
        .as_array()
        .expect("established launch arguments")[1..];
    let mut args = remaining.iter();
    while let Some(arg) = args.next() {
        match arg.as_str() {
            Some("--dangerously-skip-permissions") => {}
            Some("--model" | "--mode" | "--prompt-interactive" | "--conversation") => {
                if !args.next().is_some_and(Value::is_string) {
                    return false;
                }
            }
            _ => return false,
        }
    }
    true
}

fn valid_meta(meta: &Value, id: &str) -> bool {
    meta["id"] == id
        && ["name", "tag", "cwd", "agent_type"]
            .iter()
            .all(|key| meta[*key].is_string())
        && (meta["agent_cwd"].is_null() || meta["agent_cwd"].is_string())
        && (meta["launch_options"].is_null()
            || meta["launch_options"]
                .as_object()
                .is_some_and(|map| map.values().all(Value::is_string)))
        && meta["status"].as_object().is_some_and(|status| {
            status.keys().all(|key| {
                matches!(
                    key.as_str(),
                    "Process" | "Turn" | "Interaction" | "Attention" | "NativeWorkUnknown"
                )
            })
        })
        && Path::new(field(meta, "cwd")).is_absolute()
        && nonzero_time(&meta["created_at"])
        && timestamp(&meta["last_activity"]).is_some()
}

fn qualified(root: &Path, file: &File, meta: &Value) -> io::Result<bool> {
    qualified_role(meta)?;
    if meta["status"]["Process"] != "exited" {
        return Err(io::Error::other(
            "Go session is live or has uncertain custody",
        ));
    }
    match read_at(file, c"rust-origin.json") {
        Ok(origin) => export::verify(root, meta, &origin).map(|()| true),
        Err(e) if e.kind() == io::ErrorKind::NotFound => exited(file, meta).map(|()| false),
        Err(e) => Err(e),
    }
}

impl Store {
    pub(super) fn open(root: &Path, opted_in: bool, adopt_live: bool) -> io::Result<Option<Self>> {
        let mut rows = BTreeMap::new();
        let directory = File::open(root)?;
        for dir in fs::read_dir(root)? {
            let dir = dir?;
            if !dir.file_type()?.is_dir() {
                continue;
            }
            let file_name = dir.file_name();
            let Some(id) = file_name.to_str().filter(|id| local_id(id)) else {
                continue;
            };
            let path = dir.path();
            let meta_path = path.join("meta.json");
            match fs::symlink_metadata(&meta_path) {
                Err(e) if e.kind() == io::ErrorKind::NotFound => continue,
                result => {
                    result?;
                }
            }
            if !opted_in {
                return Err(io::Error::other(
                    "Go session state requires a separately qualified migration",
                ));
            }
            let loaded = (|| {
                let id_name = std::ffi::CString::new(id).expect("validated ID");
                let file = open_at(&directory, &id_name, libc::O_RDONLY | libc::O_DIRECTORY)?;
                let info = file.metadata()?;
                if info.mode() & 0o077 != 0
                    || info.uid() != directory.metadata()?.uid()
                    || !same_file(&info, &fs::symlink_metadata(&path)?)
                {
                    return Err(io::Error::other(
                        "Go session directory is not physically bound and owner-private",
                    ));
                }
                let raw = read_meta(&file)?;
                let meta = raw.parsed()?;
                if meta["schema_version"] != 1 && meta["schema_version"].is_u64() {
                    return Ok((file, meta, raw));
                }
                if !valid_meta(&meta, id) {
                    return Err(io::Error::other(
                        "malformed Go metadata or directory ID mismatch",
                    ));
                }
                Ok((file, meta, raw))
            })();
            let (file, meta, raw) = match loaded {
                Ok(entry) => entry,
                Err(e) => {
                    eprintln!("daemon: skipped corrupt Go session {id}: {e}");
                    continue;
                }
            };
            let (exported, live, observed_exit) =
                if adopt_live && meta["status"]["Process"] == "running" {
                    let adopt = (|| {
                        qualified_role(&meta)?;
                        if let Some(ended) = completed(&file, &meta)? {
                            return Ok((false, None, Some(ended)));
                        }
                        let owner = live::Owner::open(path, &file, &meta)?;
                        Ok::<_, io::Error>((false, Some(Arc::new(owner)), None))
                    })();
                    adopt.map_err(|err| io::Error::other(format!("Go session {id}: {err}")))?
                } else {
                    let exported = qualified(root, &file, &meta)
                        .map_err(|err| io::Error::other(format!("Go session {id}: {err}")))?;
                    (exported, None, None)
                };
            rows.insert(
                id.to_owned(),
                Entry {
                    dir: file,
                    meta,
                    raw,
                    needs_sync: false,
                    exported,
                    live,
                    observed_exit,
                },
            );
        }
        if opted_in {
            if rows.is_empty() && export::collect(root)?.is_empty() {
                return Err(io::Error::other(
                    "no qualified stopped Go sessions in the state root",
                ));
            }
            Ok(Some(Self {
                root: root.to_owned(),
                rows: Mutex::new(rows),
            }))
        } else {
            Ok(None)
        }
    }

    pub(super) fn rows(&self, endpoint: &str) -> Vec<Value> {
        self.rows
            .lock()
            .unwrap()
            .values()
            .map(|entry| {
                view(
                    entry.observed_exit.as_ref().unwrap_or(&entry.meta),
                    endpoint,
                )
            })
            .collect()
    }

    pub(super) fn contains(&self, id: &str) -> bool {
        self.rows.lock().unwrap().contains_key(id)
    }

    pub(super) fn exported(&self, id: &str) -> io::Result<bool> {
        match self.rows.lock().unwrap().get(id) {
            Some(entry) if entry.exported => Ok(true),
            Some(_) => Err(io::Error::other("Go and Rust session IDs collide")),
            None => Ok(false),
        }
    }

    pub(super) fn live_ids(&self) -> Vec<String> {
        self.rows
            .lock()
            .unwrap()
            .iter()
            .filter(|(_, entry)| entry.live.is_some())
            .map(|(id, _)| id.clone())
            .collect()
    }

    pub(super) fn observe_exit(&self, id: &str, endpoint: &str) -> io::Result<Option<Value>> {
        let (dir, meta) = {
            let rows = self.rows.lock().unwrap();
            let Some(entry) = rows.get(id).filter(|entry| entry.live.is_some()) else {
                return Ok(None);
            };
            (entry.dir.try_clone()?, entry.meta.clone())
        };
        let Some(ended) = completed(&dir, &meta)? else {
            return Ok(None);
        };
        if !same_file(&dir.metadata()?, &fs::symlink_metadata(self.root.join(id))?) {
            return Err(io::Error::other("live Go exit lost its directory binding"));
        }
        let raw = read_meta(&dir)?;
        let row = view(&ended, endpoint);
        let owner = {
            let mut rows = self.rows.lock().unwrap();
            let entry = rows.get_mut(id).expect("adopted Go entry is retained");
            if raw != entry.raw {
                return Err(io::Error::other(
                    "live Go exit lost its original state binding",
                ));
            }
            entry.observed_exit = Some(ended);
            entry.live.take()
        };
        if let Some(owner) = owner {
            owner.retire();
        }
        Ok(Some(row))
    }

    pub(super) fn has_live(&self) -> bool {
        self.rows
            .lock()
            .unwrap()
            .values()
            .any(|entry| entry.live.is_some())
    }

    pub(super) fn live(&self, endpoint: &str, id: &str) -> Option<Arc<live::Owner>> {
        let (want, local) = id.split_once('/')?;
        if want != endpoint {
            return None;
        }
        self.rows.lock().unwrap().get(local)?.live.clone()
    }

    pub(super) fn set_label(
        &self,
        endpoint: &str,
        session: &str,
        key: &str,
        value: String,
    ) -> io::Result<Option<Value>> {
        let id = session
            .strip_prefix(endpoint)
            .and_then(|s| s.strip_prefix('/'))
            .filter(|id| local_id(id))
            .ok_or_else(|| io::Error::other("invalid session id"))?;
        let mut rows = self.rows.lock().unwrap();
        let entry = rows
            .get_mut(id)
            .ok_or_else(|| io::Error::other("unknown stopped Go session"))?;
        if entry.live.is_some() {
            return Err(io::Error::other(
                "live Go label mutation requires separate rollback qualification",
            ));
        }
        let path = self.root.join(id);
        let current = fs::symlink_metadata(&path)?;
        if current.mode() & 0o077 != 0
            || current.uid() != entry.dir.metadata()?.uid()
            || !same_file(&entry.dir.metadata()?, &current)
            || read_meta(&entry.dir)? != entry.raw
        {
            return Err(io::Error::other(
                "stopped Go session changed outside the supervisor",
            ));
        }
        if qualified(&self.root, &entry.dir, &entry.meta)? != entry.exported {
            return Err(io::Error::other(
                "stopped Go origin changed outside the supervisor",
            ));
        }
        let updated = entry.set_label(key, value, |dir| {
            if !same_file(&dir.metadata()?, &fs::symlink_metadata(&path)?) {
                return Err(io::Error::other(
                    "stopped Go directory changed during commit",
                ));
            }
            dir.sync_all()
        })?;
        Ok(updated.then(|| view(&entry.meta, endpoint)))
    }
}

fn replace_meta(dir: &File, meta: &RawFields, before_replace: impl FnOnce()) -> io::Result<()> {
    let temporary =
        std::ffi::CString::new(format!("meta.json.tmp{}", runtime::random_id()?)).unwrap();
    let result = (|| {
        let mut file = open_at(
            dir,
            &temporary,
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL,
        )?;
        meta.write(&mut file)?;
        file.sync_all()?;
        before_replace();
        // Both names resolve inside the same verified directory; a swapped path cannot redirect the write.
        let fd = dir.as_raw_fd();
        if unsafe { libc::renameat(fd, temporary.as_ptr(), fd, c"meta.json".as_ptr()) } != 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    })();
    if result.is_err() {
        unsafe { libc::unlinkat(dir.as_raw_fd(), temporary.as_ptr(), 0) };
    }
    result
}

fn view(meta: &Value, endpoint: &str) -> Value {
    let group_entered = ["group_entered_at", "last_activity", "created_at"]
        .into_iter()
        .find(|key| nonzero_time(&meta[*key]))
        .expect("validated creation time");
    let mut status = json!({
        "Process": meta["status"]["Process"], "Turn": meta["status"]["Turn"], "Interaction": meta["status"]["Interaction"]
    });
    if meta["status"]["Attention"] == true {
        status["Attention"] = json!(true);
    }
    let group = if status["Process"] != "running" {
        "completed"
    } else if status["Attention"] == true {
        "needs_input"
    } else if status["Turn"] != "idle" {
        "working"
    } else if matches!(
        field(&status, "Interaction"),
        "permission" | "prompt" | "error"
    ) {
        "needs_input"
    } else {
        "ready_for_review"
    };
    let mut row = json!({
        "id":format!("{endpoint}/{}", field(meta, "id")), "endpoint_id":endpoint,
        "agent":meta["agent_type"], "cwd":meta["cwd"],
        "created_at":meta["created_at"], "last_activity":meta["last_activity"],
        "group_entered_at":meta[group_entered], "group":group, "status":status, "summary":"",
    });
    for key in ["name", "tag", "backend_plan_error", "resume_error"] {
        if !field(meta, key).is_empty() {
            row[key] = meta[key].clone();
        }
    }
    row
}

pub(super) fn verify_daemon_stopped(root: &Path, socket: &Path) -> io::Result<()> {
    if socket != root.join("daemon.sock") {
        return Err(io::Error::other(
            "stopped Go compatibility requires the canonical owner socket",
        ));
    }
    if UnixStream::connect(socket).is_ok() {
        return Err(io::Error::other("owner socket already has a live listener"));
    }
    // Go also records identity when started with a nonstandard lock override.
    let path = root.join("daemon.pid");
    match runtime::read_private(&path, 128) {
        Ok(bytes) => {
            let token = std::str::from_utf8(&bytes)
                .ok()
                .map(|s| s.split_whitespace().collect::<Vec<_>>());
            match token.as_deref() {
                Some([pid, birth]) => {
                    let pid: u32 = pid
                        .parse()
                        .ok()
                        .filter(|pid| (2..=i32::MAX as u32).contains(pid))
                        .ok_or_else(|| io::Error::other("unproven Go daemon PID"))?;
                    let birth: u64 = birth
                        .parse()
                        .ok()
                        .filter(|birth| *birth > 0)
                        .ok_or_else(|| io::Error::other("unproven Go daemon birth identity"))?;
                    inactive(pid, birth)
                }
                _ => Err(io::Error::other("unproven Go daemon identity")),
            }
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(e),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn label_retry_syncs_after_rename_and_preserves_opaque_numbers() {
        const OPAQUE: &str = "12345678901234567890.98765432101234567890123";
        let dir = std::env::temp_dir().join(runtime::random_id().unwrap());
        runtime::private_dir(&dir).unwrap();
        const RESERVED: &str =
            r#"[{"$serde_json::private::Number":"47"}, {"$serde_json::private::RawValue":"null"}]"#;
        let raw = RawFields::parse(&format!(
            r#"{{"tag":"old","extra":[{OPAQUE}, {RESERVED}, 1e10000]}}"#
        ))
        .unwrap();
        let mut entry = Entry {
            dir: File::open(&dir).unwrap(),
            meta: raw.parsed().unwrap(),
            raw,
            needs_sync: false,
            exported: false,
            live: None,
            observed_exit: None,
        };
        replace_meta(&entry.dir, &entry.raw, || {}).unwrap();
        let error = entry.set_label("tag", "new".into(), |_| {
            Err(io::Error::other("sync failed"))
        });
        let invalid = RawFields::parse(r#"{"shim_pid":1e10000}"#).unwrap();
        assert!(
            invalid
                .parsed()
                .unwrap_err()
                .to_string()
                .contains("shim_pid")
        );
        let alias = RawFields::parse(r#"{"Role":"manager"}"#).unwrap();
        assert!(alias.parsed().unwrap_err().to_string().contains("role"));
        assert_eq!(error.unwrap_err().to_string(), "sync failed");
        assert_eq!(entry.meta["tag"], "new");
        assert_eq!(read_meta(&entry.dir).unwrap().0, entry.raw.0);
        let raw = fs::read_to_string(dir.join("meta.json")).unwrap();
        assert!(
            raw.contains(&format!("[{OPAQUE}, {RESERVED}, 1e10000]")),
            "{raw}"
        );
        for malformed in [
            r#"{"tag":"one","tag":"two"}"#,
            r#"{"tag":"one",}"#,
            r#"{"tag" "one"}"#,
            r#"{"tag": "one" "cwd": "/tmp"}"#,
            r#"{"tag": "one"} {}"#,
            r#"{"tag": "one", "broken": [1,]}"#,
            r#"{"tag": "one", "broken": 01}"#,
        ] {
            assert!(RawFields::parse(malformed).is_err(), "{malformed}");
        }
        assert!(
            entry
                .set_label("tag", "new".into(), File::sync_all)
                .unwrap()
        );
        assert!(
            !entry
                .set_label("tag", "new".into(), |_| panic!("unnecessary sync"))
                .unwrap()
        );
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn process_crash_after_temp_sync_keeps_the_go_record() {
        const CHILD: &str = "SWARM_TEST_STOPPED_GO_PRE_REPLACE";
        if let Some(path) = std::env::var_os(CHILD) {
            let file = File::open(path).unwrap();
            replace_meta(
                &file,
                &RawFields::parse(r#"{"name":"new"}"#).unwrap(),
                || std::process::exit(97),
            )
            .unwrap();
            panic!("crash injection did not fire");
        }
        let dir = std::env::temp_dir().join(runtime::random_id().unwrap());
        runtime::private_dir(&dir).unwrap();
        let file = File::open(&dir).unwrap();
        replace_meta(
            &file,
            &RawFields::parse(r#"{"name":"old"}"#).unwrap(),
            || {},
        )
        .unwrap();
        let child = std::process::Command::new(std::env::current_exe().unwrap())
            .args([
                "--exact",
                "supervisor::stopped_go::tests::process_crash_after_temp_sync_keeps_the_go_record",
            ])
            .env(CHILD, &dir)
            .status()
            .unwrap();
        assert_eq!(child.code(), Some(97));
        assert_eq!(read_at(&file, c"meta.json").unwrap()["name"], "old");
        assert_eq!(fs::read_dir(&dir).unwrap().count(), 2);
        replace_meta(
            &file,
            &RawFields::parse(r#"{"name":"new"}"#).unwrap(),
            || {},
        )
        .unwrap();
        assert_eq!(read_at(&file, c"meta.json").unwrap()["name"], "new");
        fs::remove_dir_all(dir).unwrap();
    }
}
