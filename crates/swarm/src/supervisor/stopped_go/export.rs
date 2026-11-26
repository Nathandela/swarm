use super::{Store, open_at, same_file, timestamp};
use crate::{
    ipc,
    supervisor::{runtime, session},
};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::ffi::{CStr, CString};
use std::fs::{self, File, OpenOptions};
use std::io::{self, Write};
use std::os::fd::AsRawFd;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};

pub(in crate::supervisor) mod codex;

const INTENT: &str = "go-rollback-intent.json";
const STAGING: &CStr = c".go-rollback-stage";
const PUBLISHED: &str = "go-rollback-published.json";
const ORIGIN: &CStr = c"rust-origin.json";

pub(in crate::supervisor) fn run() -> io::Result<()> {
    let root = fs::canonicalize(ipc::state_path()?)?;
    runtime::private_dir(&root)?;
    let lock_path = root.join("daemon.lock");
    if std::env::var(super::OPT_IN).ok().as_deref() != Some("1")
        || ipc::env_path("SWARM_DAEMON_LOCK").is_some_and(|path| path != lock_path)
    {
        return Err(io::Error::other(
            "stopped Go rollback requires the opt-in and canonical daemon lock",
        ));
    }
    let _flock = runtime::lock(&lock_path)?;
    super::verify_daemon_stopped(&root, &ipc::socket_path()?)?;
    let root_dir = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW)
        .open(&root)?;
    let adopt_live = std::env::var(super::live::OPT_IN).ok().as_deref() == Some("1");
    let imported = Store::open(&root, true, adopt_live)?.expect("opt-in store");
    let deleted = go_tombstones(&root)?;
    let prepared = collect(&root)?
        .into_iter()
        .map(|dir| {
            let attempt = validate(&dir)?;
            let plan = attempt.plan(&root_dir, &imported, deleted.contains(&attempt.id))?;
            let proof = if matches!(plan, Plan::Stage(_)) {
                attempt.resume_proof()?
            } else {
                None
            };
            Ok((attempt, plan, proof))
        })
        .collect::<io::Result<Vec<_>>>()?;
    // Verify all rows before taking publication action on any.
    for (attempt, plan, proof) in prepared {
        if !same_file(&root_dir.metadata()?, &fs::symlink_metadata(&root)?) {
            return Err(io::Error::other("rollback root changed while exporting"));
        }
        if let Plan::Stage(intent) = plan {
            attempt.export(&root_dir, intent, proof.as_ref())?;
        }
        root_dir.sync_all()?;
        let marker = attempt.marker();
        if !published(&attempt.dir, &marker)? {
            runtime::write_json(&attempt.dir.join(PUBLISHED), &marker)?;
        }
        File::open(&attempt.dir)?.sync_all()?;
    }
    Ok(())
}

fn go_tombstones(root: &Path) -> io::Result<std::collections::BTreeSet<String>> {
    let journal = root.join("journal");
    match fs::symlink_metadata(&journal) {
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Default::default()),
        result => {
            result?;
        }
    }
    runtime::private_dir(&journal)?;
    let mut segments = Vec::new();
    for entry in fs::read_dir(&journal)? {
        let entry = entry?;
        if let Some(sequence) = entry
            .file_name()
            .to_str()
            .and_then(|name| name.strip_prefix("seg-")?.strip_suffix(".log"))
            .filter(|sequence| sequence.len() == 20 && sequence.bytes().all(|b| b.is_ascii_digit()))
        {
            segments.push((sequence.to_owned(), entry.path()));
        }
    }
    if segments.len() > 16 {
        return Err(io::Error::other(
            "Go journal exceeds its pinned segment count",
        ));
    }
    segments.sort();
    let mut removed = std::collections::BTreeSet::new();
    let mut total = 0;
    let mut last_cursor = 0;
    for (_, path) in segments {
        let bytes = runtime::read_private(&path, 1 << 20)?;
        total += bytes.len();
        if total > 1 << 20 || !bytes.is_empty() && !bytes.ends_with(b"\n") {
            return Err(io::Error::other("ambiguous Go journal boundary"));
        }
        let records = bytes.strip_suffix(b"\n").unwrap_or(&bytes);
        for line in records
            .split(|b| *b == b'\n')
            .filter(|line| !line.is_empty())
        {
            let record: Value = serde_json::from_slice(line)?;
            let cursor = record["cursor"]
                .as_u64()
                .filter(|cursor| *cursor > last_cursor)
                .ok_or_else(|| io::Error::other("ambiguous Go deletion journal cursor"))?;
            if record["schema_version"] != 1 || !record["type"].is_string() {
                return Err(io::Error::other("unqualified Go deletion journal record"));
            }
            last_cursor = cursor;
            if let Some(id) = record["session_id"]
                .as_str()
                .filter(|id| runtime::valid_id(id))
            {
                if record["type"] == "deleted" {
                    removed.insert(id.to_owned());
                } else {
                    removed.remove(id);
                }
            }
        }
    }
    Ok(removed)
}

pub(in crate::supervisor) fn collect(root: &Path) -> io::Result<Vec<PathBuf>> {
    let dir = root.join("rust-v1");
    match fs::symlink_metadata(&dir) {
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        result => {
            result?;
        }
    }
    runtime::private_dir(&dir)?;
    let mut sessions = Vec::new();
    for entry in fs::read_dir(dir)? {
        let entry = entry?;
        let path = entry.path();
        let entry_type = entry.file_type()?;
        // Native attempts keep diagnostic output here while Go owns the original log.
        if entry.file_name() == "daemon.log" && entry_type.is_file() {
            let meta = fs::symlink_metadata(&path)?;
            if meta.is_file()
                && meta.uid() == runtime::uid()
                && meta.nlink() == 1
                && meta.mode() & 0o077 == 0
            {
                continue;
            }
        }
        if entry_type.is_dir()
            && let Some(id) = path
                .file_name()
                .and_then(|x| x.to_str())
                .filter(|id| runtime::valid_id(id))
        {
            configuration(&path, id)?;
            sessions.push(path);
        } else {
            return Err(io::Error::other(
                "unqualified Rust data in stopped Go rollback root",
            ));
        }
    }
    sessions.sort();
    Ok(sessions)
}

fn configuration(dir: &Path, id: &str) -> io::Result<Value> {
    fs::symlink_metadata(dir)?;
    runtime::private_dir(dir)?;
    let config = runtime::read_json(&dir.join("session.json"))?;
    let provider = &config["provider"];
    let admitted_provider = match ipc::field(provider, "name") {
        "claude" | "agy" => provider["mode"] == "classic",
        "codex" => {
            provider["mode"] == "managed"
                && provider["version"] == codex::RELEASE
                && provider["stable_release"] == true
                && config["go_rollback"]["cli_identity"].is_object()
                && config["go_rollback"]["codex_profile"].is_object()
                && config["codex_resume"].is_null()
        }
        _ => false,
    };
    if config["schema"] != 1
        || config["id"] != id
        || !runtime::valid_id(ipc::field(&config, "token"))
        || config["go_rollback"]["schema"] != 1
        || !admitted_provider
        || !config["agy_launch"]["options"]["resume_conversation_id"].is_null()
        || !config["agy_launch"]["resumed_from"].is_null()
        || !config["manager"].is_null()
        || !config["mailbox"].is_null()
        || !config["claude_resume"].is_null()
        || !config["schedule"].is_null()
        || !config["checkout"].is_null()
        || config["lineage"] != json!({})
        || !Path::new(ipc::field(&config, "cwd")).is_absolute()
        || !config["name"].is_string()
        || !config["tag"].is_string()
        || timestamp(&config["created_at"]).is_none()
        || !config["go_rollback"]["env"]
            .as_array()
            .is_some_and(|entries| {
                entries.iter().all(|entry| {
                    entry
                        .as_str()
                        .and_then(|s| s.split_once('='))
                        .is_some_and(|(key, _)| crate::environment::child_key(key))
                })
            })
    {
        return Err(io::Error::other(format!(
            "unqualified Rust rollback session {id}"
        )));
    }
    Ok(config)
}

pub(in crate::supervisor) fn was_published(dir: &Path) -> io::Result<bool> {
    let id = dir
        .file_name()
        .and_then(|x| x.to_str())
        .expect("verified Rust ID");
    let config = configuration(dir, id)?;
    let marker = marker(id, ipc::field(&config, "token"));
    if published(dir, &marker)? {
        return Ok(true);
    }
    let source = reopen_dir(dir)?;
    if read_intent(&source, &marker)?.is_some_and(|intent| intent.stage.is_some())
        && maybe_dir(&source, STAGING)?.is_none()
    {
        eprintln!(
            "daemon: {id}: Go export destination unobserved after publication intent; retaining the Rust evidence"
        );
        return Ok(true);
    }
    Ok(false)
}

fn published(dir: &Path, marker: &Value) -> io::Result<bool> {
    match runtime::read_json(&dir.join(PUBLISHED)) {
        Ok(value) if &value == marker => Ok(true),
        Ok(_) => Err(io::Error::other("Rust rollback publication marker changed")),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(false),
        Err(e) => Err(e),
    }
}

fn marker(id: &str, token: &str) -> Value {
    json!({"schema":1, "id":id, "reservation":format!("{:x}", Sha256::digest(token.as_bytes()))})
}

#[derive(serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Intent {
    origin: Value,
    stage: Option<[u64; 2]>,
}

enum Plan {
    Stage(Option<Intent>),
    Recorded,
}

fn reopen_dir(dir: &Path) -> io::Result<File> {
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_DIRECTORY)
        .open(dir)?;
    let meta = file.metadata()?;
    if meta.uid() != unsafe { libc::geteuid() } || meta.mode() & 0o077 != 0 {
        return Err(io::Error::other(
            "Go export source directory is not owner private",
        ));
    }
    Ok(file)
}

fn read_intent(source: &File, marker: &Value) -> io::Result<Option<Intent>> {
    match open_at(source, c"go-rollback-intent.json", libc::O_RDONLY) {
        Ok(file) => {
            let bytes = runtime::read_private_file(file, 4096)?;
            let intent: Intent = serde_json::from_slice(&bytes)?;
            if intent.origin != *marker {
                return Err(io::Error::other(
                    "Go export intent does not match its Rust reservation",
                ));
            }
            Ok(Some(intent))
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e),
    }
}

fn maybe_dir(parent: &File, name: &CStr) -> io::Result<Option<File>> {
    match open_at(parent, name, libc::O_RDONLY | libc::O_DIRECTORY) {
        Ok(dir) => {
            let meta = dir.metadata()?;
            if meta.uid() != parent.metadata()?.uid() || meta.mode() & 0o077 != 0 {
                return Err(io::Error::other("Go export directory is not owner private"));
            }
            Ok(Some(dir))
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e),
    }
}

fn identity(file: &File) -> io::Result<[u64; 2]> {
    let meta = file.metadata()?;
    Ok([meta.dev(), meta.ino()])
}

struct Attempt {
    dir: PathBuf,
    id: String,
    token: String,
    meta: Value,
}

impl Attempt {
    fn marker(&self) -> Value {
        marker(&self.id, &self.token)
    }

    fn plan(&self, root: &File, imported: &Store, go_deleted: bool) -> io::Result<Plan> {
        let source = reopen_dir(&self.dir)?;
        let marker = self.marker();
        let intent = read_intent(&source, &marker)?;
        let stage = maybe_dir(&source, STAGING)?;
        let dest = CString::new(self.id.as_str())?;
        if let Some(target) = maybe_dir(root, &dest)? {
            if !imported.exported(&self.id)?
                || stage.is_some()
                || intent
                    .as_ref()
                    .and_then(|intent| intent.stage)
                    .is_some_and(|bound| identity(&target).ok() != Some(bound))
            {
                return Err(io::Error::other(
                    "Go rollback destination is already occupied or changed",
                ));
            }
            return Ok(Plan::Recorded);
        }
        if published(&self.dir, &marker)? || go_deleted {
            if stage.is_some() {
                return Err(io::Error::other(
                    "Go deletion proof conflicts with a surviving export stage",
                ));
            }
            return Ok(Plan::Recorded);
        }
        if let Some(bound) = intent.as_ref().and_then(|intent| intent.stage) {
            match stage {
                Some(stage) if identity(&stage)? == bound => {}
                _ => {
                    return Err(io::Error::other(format!(
                        "Go export {} is missing its bound stage and destination or has a foreign stage; no replacement was made",
                        self.id
                    )));
                }
            }
        } else if stage.is_some() && intent.is_none() {
            return Err(io::Error::other(
                "Go export found a staging directory without an intent",
            ));
        }
        Ok(Plan::Stage(intent))
    }

    fn resume_proof(&self) -> io::Result<Option<Value>> {
        if self.meta["agent_type"] == "codex" {
            return codex::resume_proof(&self.dir, &self.meta, &self.token).map(Some);
        }
        if self.meta["agent_type"] != "agy" {
            return Ok(None);
        }
        let proof = crate::providers::agy::handoff::source(&self.dir)?;
        let home = Path::new(ipc::field(&proof, "home"));
        if std::env::var_os("HOME").as_deref() != Some(home.as_os_str())
            || !self.meta["env"].as_array().is_some_and(|env| {
                env.iter()
                    .filter_map(Value::as_str)
                    .filter_map(|entry| entry.strip_prefix("HOME="))
                    .eq([ipc::field(&proof, "home")])
            })
            || proof["conversation"] != self.meta["conversation_id"]
            || proof["cwd"] != self.meta["cwd"]
        {
            return Err(io::Error::other(
                "agy rollback requires the bound source home and conversation",
            ));
        }
        Ok(Some(proof))
    }

    fn export(
        &self,
        root: &File,
        intent: Option<Intent>,
        resume_proof: Option<&Value>,
    ) -> io::Result<()> {
        let source = reopen_dir(&self.dir)?;
        let mut intent = match intent {
            Some(intent) => intent,
            None => {
                let new = Intent {
                    origin: self.marker(),
                    stage: None,
                };
                runtime::write_json(&self.dir.join(INTENT), &serde_json::to_value(&new)?)?;
                new
            }
        };
        let staging = match maybe_dir(&source, STAGING)? {
            Some(dir) => dir,
            None if intent.stage.is_none() => {
                if unsafe { libc::mkdirat(source.as_raw_fd(), STAGING.as_ptr(), 0o700) } != 0 {
                    return Err(io::Error::last_os_error());
                }
                maybe_dir(&source, STAGING)?
                    .ok_or_else(|| io::Error::other("Go export stage disappeared"))?
            }
            _ => return Err(io::Error::other("Go export stage disappeared")),
        };
        let stamp = identity(&staging)?;
        let stage_path = self.dir.join(".go-rollback-stage");
        let verify_stage = || -> io::Result<()> {
            let current = maybe_dir(&source, STAGING)?
                .ok_or_else(|| io::Error::other("Go export stage disappeared"))?;
            if identity(&current)? != stamp
                || !same_file(&current.metadata()?, &fs::symlink_metadata(&stage_path)?)
            {
                return Err(io::Error::other("Go export staging directory changed"));
            }
            Ok(())
        };
        verify_stage()?;
        let contents = fs::read_dir(&stage_path)?.collect::<io::Result<Vec<_>>>()?;
        match intent.stage {
            None if contents.is_empty() => {
                // Never put a second copy of credentials in an unbound directory.
                intent.stage = Some(stamp);
                runtime::write_json(&self.dir.join(INTENT), &serde_json::to_value(&intent)?)?;
            }
            Some(bound)
                if bound == stamp
                    && contents.iter().all(|file| {
                        matches!(
                            file.file_name().to_str(),
                            Some("meta.json" | "rust-origin.json")
                        )
                    }) => {}
            _ => return Err(io::Error::other("unproven Go export staging contents")),
        }
        source.sync_all()?;
        for (name, data) in [(ORIGIN, &self.marker()), (c"meta.json", &self.meta)] {
            let mut file = match open_at(
                &staging,
                name,
                libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL,
            ) {
                Ok(file) => file,
                Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {
                    open_at(&staging, name, libc::O_WRONLY)?
                }
                Err(e) => return Err(e),
            };
            runtime::validate_file(&file)?;
            file.set_len(0)?;
            serde_json::to_writer(&mut file, data)?;
            file.write_all(b"\n")?;
            file.sync_all()?;
        }
        staging.sync_all()?;
        verify_stage()?;
        if let Some(proof) = resume_proof {
            if self.meta["agent_type"] == "agy" {
                crate::providers::agy::handoff::verify(&self.dir, proof)?;
            } else if self.resume_proof()?.as_ref() != Some(proof) {
                return Err(io::Error::other(
                    "Codex rollback source changed before publication",
                ));
            }
        }
        let dest = CString::new(self.id.as_str())?;
        let source_fd = source.as_raw_fd();
        let root_fd = root.as_raw_fd();
        #[cfg(target_os = "macos")]
        let status = unsafe {
            libc::renameatx_np(
                source_fd,
                STAGING.as_ptr(),
                root_fd,
                dest.as_ptr(),
                libc::RENAME_EXCL,
            )
        };
        #[cfg(target_os = "linux")]
        let status = unsafe {
            libc::syscall(
                libc::SYS_renameat2,
                source_fd,
                STAGING.as_ptr(),
                root_fd,
                dest.as_ptr(),
                libc::RENAME_NOREPLACE,
            )
        };
        if status != 0 {
            return Err(io::Error::last_os_error());
        }
        let target = maybe_dir(root, &dest)?
            .ok_or_else(|| io::Error::other("Go export destination disappeared"))?;
        if identity(&target)? != stamp {
            return Err(io::Error::other("Go export destination identity changed"));
        }
        source.sync_all()
    }
}

fn validate(dir: &Path) -> io::Result<Attempt> {
    let id = dir
        .file_name()
        .and_then(|x| x.to_str())
        .filter(|id| runtime::valid_id(id))
        .ok_or_else(|| io::Error::other("invalid Rust rollback session ID"))?;
    let config = configuration(dir, id)?;
    let token = ipc::field(&config, "token");
    let owner = runtime::read_json(&dir.join("owner.json"))?;
    if owner["token"] != token || !session::owner_gone(dir, token)? {
        return Err(io::Error::other(format!(
            "live or uncertain Rust shim for {id}"
        )));
    }
    let exit = session::exit_report(dir, token)?;
    let proof = runtime::read_json(&dir.join("go-stop-proof.json"))?;
    let clock = timestamp(&exit["finished_at"]);
    if proof["token"] != token
        || clock.is_none()
        || proof["finished_at"] != exit["finished_at"]
        || proof["containment"]
            != if cfg!(target_os = "macos") {
                "darwin-process-group"
            } else {
                "linux-subreaper"
            }
    {
        return Err(io::Error::other(format!(
            "Rust native writer stop proof unavailable for {id}"
        )));
    }
    let code = exit["code"]
        .as_u64()
        .or_else(|| exit["signal"].as_u64().map(|signal| 128 + signal))
        .ok_or_else(|| io::Error::other("Rust launch has no observed process exit"))?;
    let observation = runtime::read_json(&dir.join("observation.json"))?;
    let conversation = &observation["conversation_id"];
    let agent = ipc::field(&config["provider"], "name");
    let valid_conversation = if matches!(agent, "agy" | "codex") {
        conversation
            .as_str()
            .is_some_and(crate::providers::codex_receipt::canonical_thread)
    } else {
        crate::providers::claude::conversation_id(&json!({"session_id":conversation})).is_some()
    };
    if observation["token"] != token || !valid_conversation {
        return Err(io::Error::other(format!(
            "Rust provider conversation identity unavailable for {id}"
        )));
    }
    let at = &exit["finished_at"];
    let created = &config["created_at"];
    let mut attempt = Attempt {
        dir: dir.to_owned(),
        id: id.into(),
        token: token.into(),
        meta: json!({
            "schema_version":1, "id":id, "agent_type":agent, "name":config["name"],
            "name_set_at":config["name_set_at"].as_str().or_else(|| created.as_str()),
            "tag":config["tag"], "cwd":config["cwd"], "agent_cwd":"",
            "launch_options":config["go_rollback"]["options"], "env":config["go_rollback"]["env"],
            "created_at":created, "status":{"Process":"exited","Turn":"unknown","Interaction":"none"},
            "group_entered_at":at, "last_activity":at, "shim_pid":owner["pid"],
            "shim_start_time":owner["birth"], "conversation_id":conversation, "exit_code":code,
            "resumed_from":"", "spawned_from":"", "spawn_intent":"", "supervision":"",
        }),
    };
    if agent == "codex" {
        attempt.meta["cli_identity"] = config["go_rollback"]["cli_identity"].clone();
    }
    Ok(attempt)
}

pub(super) fn verify(root: &Path, meta: &Value, origin: &Value) -> io::Result<()> {
    let id = ipc::field(meta, "id");
    if !runtime::valid_id(id) {
        return Err(io::Error::other("invalid Rust origin ID"));
    }
    let rust = root.join("rust-v1");
    fs::symlink_metadata(&rust)?;
    runtime::private_dir(&rust)?;
    let attempt = validate(&rust.join(id))?;
    let immutable = [
        "schema_version",
        "id",
        "agent_type",
        "cli_identity",
        "cwd",
        "agent_cwd",
        "launch_options",
        "env",
        "created_at",
        "status",
        "group_entered_at",
        "last_activity",
        "shim_pid",
        "shim_start_time",
        "conversation_id",
        "exit_code",
        "resumed_from",
        "spawned_from",
        "spawn_intent",
        "supervision",
    ];
    if *origin != attempt.marker() || immutable.iter().any(|key| meta[*key] != attempt.meta[*key]) {
        return Err(io::Error::other(
            "Rust rollback metadata does not match its recorded origin",
        ));
    }
    Ok(())
}
