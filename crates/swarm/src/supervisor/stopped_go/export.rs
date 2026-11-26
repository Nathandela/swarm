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
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};

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
    let imported = Store::open(&root, true)?.expect("opt-in store");
    let prepared = collect(&root)?
        .into_iter()
        .map(|dir| {
            let attempt = validate(&dir)?;
            let dest = root.join(&attempt.id);
            let marker = attempt.marker();
            let prior = published(&dir, &marker)?;
            match fs::symlink_metadata(&dest) {
                Err(e) if e.kind() == io::ErrorKind::NotFound => {}
                Ok(info) if info.is_dir() => {
                    if !imported.exported(&attempt.id)? {
                        return Err(io::Error::other(
                            "Go rollback destination is already occupied",
                        ));
                    }
                    return Ok((attempt, false));
                }
                Ok(_) => {
                    return Err(io::Error::other(
                        "Go rollback destination is already occupied",
                    ));
                }
                Err(e) => return Err(e),
            }
            Ok((attempt, !prior))
        })
        .collect::<io::Result<Vec<_>>>()?;
    // Validate the entire root before publishing any row. Each row is independently
    // committed and recognized on retry, even if the process stops between rows.
    for (attempt, needs_export) in prepared {
        if !same_file(&root_dir.metadata()?, &fs::symlink_metadata(&root)?) {
            return Err(io::Error::other("rollback root changed while exporting"));
        }
        if needs_export {
            attempt.export(&root_dir)?;
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
    if config["schema"] != 1
        || config["id"] != id
        || !runtime::valid_id(ipc::field(&config, "token"))
        || config["go_rollback"]["schema"] != 1
        || config["provider"]["name"] != "claude"
        || config["provider"]["mode"] != "classic"
        || !config["manager"].is_null()
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
    published(dir, &marker(id, ipc::field(&config, "token")))
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

    fn export(&self, root: &File) -> io::Result<()> {
        // Stage outside the roster and pin the destination directory. Neither
        // the staged files nor the published row may overwrite foreign state.
        let from = CString::new(format!(".rust-go-export-{}", runtime::random_id()?))?;
        let to = CString::new(self.id.as_str())?;
        let root_fd = root.as_raw_fd();
        if unsafe { libc::mkdirat(root_fd, from.as_ptr(), 0o700) } != 0 {
            return Err(io::Error::last_os_error());
        }
        let staging = open_at(root, &from, libc::O_RDONLY | libc::O_DIRECTORY)?;
        let bound = |name| -> io::Result<()> {
            let current = open_at(root, name, libc::O_RDONLY | libc::O_DIRECTORY)?;
            if !same_file(&staging.metadata()?, &current.metadata()?) {
                return Err(io::Error::other("Go export staging directory changed"));
            }
            Ok(())
        };
        let result = (|| {
            for (name, data) in [(ORIGIN, &self.marker()), (c"meta.json", &self.meta)] {
                let mut file = open_at(
                    &staging,
                    name,
                    libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL,
                )?;
                serde_json::to_writer(&mut file, data)?;
                file.write_all(b"\n")?;
                file.sync_all()?;
            }
            staging.sync_all()?;
            bound(&from)?;
            #[cfg(target_os = "macos")]
            let status = unsafe {
                libc::renameatx_np(
                    root_fd,
                    from.as_ptr(),
                    root_fd,
                    to.as_ptr(),
                    libc::RENAME_EXCL,
                )
            };
            #[cfg(target_os = "linux")]
            let status = unsafe {
                libc::syscall(
                    libc::SYS_renameat2,
                    root_fd,
                    from.as_ptr(),
                    root_fd,
                    to.as_ptr(),
                    libc::RENAME_NOREPLACE,
                )
            };
            if status == 0 {
                Ok(())
            } else {
                Err(io::Error::last_os_error())
            }
        })();
        if result.is_err() {
            for name in [ORIGIN, c"meta.json"] {
                unsafe { libc::unlinkat(staging.as_raw_fd(), name.as_ptr(), 0) };
            }
            unsafe { libc::unlinkat(root_fd, from.as_ptr(), libc::AT_REMOVEDIR) };
        }
        result?;
        bound(&to)
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
    if observation["token"] != token
        || crate::providers::claude::conversation_id(&json!({"session_id":conversation})).is_none()
    {
        return Err(io::Error::other(format!(
            "Rust provider conversation identity unavailable for {id}"
        )));
    }
    let at = &exit["finished_at"];
    let created = &config["created_at"];
    Ok(Attempt {
        dir: dir.to_owned(),
        id: id.into(),
        token: token.into(),
        meta: json!({
            "schema_version":1, "id":id, "agent_type":"claude", "name":config["name"],
            "name_set_at":config["name_set_at"].as_str().or_else(|| created.as_str()),
            "tag":config["tag"], "cwd":config["cwd"], "agent_cwd":"",
            "launch_options":config["go_rollback"]["options"], "env":config["go_rollback"]["env"],
            "created_at":created, "status":{"Process":"exited","Turn":"unknown","Interaction":"none"},
            "group_entered_at":at, "last_activity":at, "shim_pid":owner["pid"],
            "shim_start_time":owner["birth"], "conversation_id":conversation, "exit_code":code,
            "resumed_from":"", "spawned_from":"", "spawn_intent":"", "supervision":"",
        }),
    })
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
