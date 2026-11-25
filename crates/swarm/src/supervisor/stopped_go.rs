// The Go store remains authoritative throughout stopped-session compatibility.
use super::runtime;
use crate::ipc::field;
use serde::de::{MapAccess, Visitor};
use serde::{Deserialize, Deserializer, Serialize, de};
use serde_json::value::{RawValue, to_raw_value};
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::ffi::CStr;
use std::fs::{self, File};
use std::io;
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
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
    "launch_options",
    "backend_plan_error",
    "resume_error",
];

#[derive(Clone, Serialize)]
#[serde(transparent)]
struct RawFields(BTreeMap<String, Box<RawValue>>);

impl<'de> Deserialize<'de> for RawFields {
    fn deserialize<D: Deserializer<'de>>(input: D) -> Result<Self, D::Error> {
        struct Object;
        impl<'de> Visitor<'de> for Object {
            type Value = RawFields;

            fn expecting(&self, out: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                out.write_str("a Go metadata object with unique fields")
            }

            fn visit_map<M: MapAccess<'de>>(self, mut map: M) -> Result<Self::Value, M::Error> {
                let mut fields = BTreeMap::new();
                while let Some((key, value)) = map.next_entry()? {
                    if fields.insert(key, value).is_some() {
                        return Err(de::Error::custom("duplicate Go metadata field"));
                    }
                }
                Ok(RawFields(fields))
            }
        }
        input.deserialize_map(Object)
    }
}

impl RawFields {
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
                    serde_json::from_str::<RawFields>(raw.get())?;
                }
                let parsed = serde_json::from_str(raw.get()).map_err(|error| {
                    io::Error::other(format!("invalid Go metadata field {key}: {error}"))
                })?;
                fields.insert((*key).to_owned(), parsed);
            }
        }
        Ok(Value::Object(fields))
    }

    fn same_as(&self, other: &Self) -> bool {
        self.0.len() == other.0.len()
            && self
                .0
                .iter()
                .zip(&other.0)
                .all(|((a_key, a_value), (b_key, b_value))| {
                    a_key == b_key && a_value.get() == b_value.get()
                })
    }
}

struct Entry {
    dir: File,
    meta: Value,
    raw: RawFields,
    needs_sync: bool,
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
            revised.0.insert(key.to_owned(), to_raw_value(&value)?);
            if key == "name" {
                let now = OffsetDateTime::now_utc()
                    .format(&Rfc3339)
                    .expect("UTC timestamp");
                revised.0.insert("name_set_at".into(), to_raw_value(&now)?);
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
    Ok(serde_json::from_slice(&runtime::read_private_file(
        file,
        1 << 20,
    )?)?)
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

fn timestamp(value: &Value) -> Option<OffsetDateTime> {
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

fn exited(file: &File, meta: &Value) -> io::Result<()> {
    let schema = meta["schema_version"].as_u64();
    if schema != Some(1) {
        return Err(io::Error::other(format!(
            "Go schema {schema:?} requires offline qualification"
        )));
    }
    // Scope one fixture-backed role first. A schema-1 stamp alone does not prove ordinary custody.
    if meta["agent_type"] != "claude"
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
    if meta["status"]["Process"] != "exited" {
        return Err(io::Error::other(
            "Go session is live or has uncertain custody",
        ));
    }
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
    for name in [
        c"shim.sock",
        c"hooks.sock",
        c"hook.sock",
        c"codex.sock",
        c"backend.json",
    ] {
        // A crashed shim can leave its socket. Recovery of that custody is outside this slice.
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
        || native["provider"] != "claude"
        || field(&native, "generation").is_empty()
        || !native["backend_pid"].is_null()
        || !native["backend_start_time"].is_null()
        || !native["incident_id"].is_null()
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
        || launch["native_provider"] != "claude"
        || launch["claude_input"] == true
        || ["login_runtime", "opencode", "hermes_input", "agy_input"]
            .iter()
            .any(|key| !launch[*key].is_null())
        || [
            "backend_program",
            "backend_socket_path",
            "manager_replay_id",
            "input_embargo",
        ]
        .iter()
        .any(|key| !launch[*key].is_null() && launch[*key] != "")
    {
        return Err(io::Error::other(
            "Go launch binding or native writer is unqualified",
        ));
    }
    Ok(())
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

impl Store {
    pub(super) fn open(root: &Path, opted_in: bool) -> io::Result<Option<Self>> {
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
            exited(&file, &meta).map_err(|e| io::Error::other(format!("Go session {id}: {e}")))?;
            rows.insert(
                id.to_owned(),
                Entry {
                    dir: file,
                    meta,
                    raw,
                    needs_sync: false,
                },
            );
        }
        if opted_in {
            if rows.is_empty() {
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
        let rows = self.rows.lock().unwrap();
        let mut entries: Vec<_> = rows.values().collect();
        entries.sort_by_key(|entry| std::cmp::Reverse(timestamp(&entry.meta["created_at"])));
        entries
            .into_iter()
            .map(|entry| view(&entry.meta, endpoint))
            .collect()
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
        let path = self.root.join(id);
        let current = fs::symlink_metadata(&path)?;
        if current.mode() & 0o077 != 0
            || current.uid() != entry.dir.metadata()?.uid()
            || !same_file(&entry.dir.metadata()?, &current)
            || !read_meta(&entry.dir)?.same_as(&entry.raw)
        {
            return Err(io::Error::other(
                "stopped Go session changed outside the supervisor",
            ));
        }
        exited(&entry.dir, &entry.meta)?;
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

fn replace_meta(
    dir: &File,
    meta: &impl Serialize,
    before_replace: impl FnOnce(),
) -> io::Result<()> {
    use std::io::Write;
    let temporary =
        std::ffi::CString::new(format!("meta.json.tmp{}", runtime::random_id()?)).unwrap();
    let result = (|| {
        let mut file = open_at(
            dir,
            &temporary,
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL,
        )?;
        serde_json::to_writer(&mut file, meta)?;
        file.write_all(b"\n")?;
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
        "Process": "exited", "Turn": meta["status"]["Turn"], "Interaction": meta["status"]["Interaction"]
    });
    if meta["status"]["Attention"] == true {
        status["Attention"] = json!(true);
    }
    let mut row = json!({
        "id":format!("{endpoint}/{}", field(meta, "id")), "endpoint_id":endpoint,
        "agent":meta["agent_type"], "cwd":meta["cwd"],
        "created_at":meta["created_at"], "last_activity":meta["last_activity"],
        "group_entered_at":meta[group_entered], "group":"completed", "status":status, "summary":"",
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
        const RESERVED: &str = r#"{"$serde_json::private::Number":"47"}"#;
        let raw: RawFields = serde_json::from_str(&format!(
            r#"{{"tag":"old","extra":[{OPAQUE}, {RESERVED}, 1e10000]}}"#
        ))
        .unwrap();
        let mut entry = Entry {
            dir: File::open(&dir).unwrap(),
            meta: raw.parsed().unwrap(),
            raw,
            needs_sync: false,
        };
        replace_meta(&entry.dir, &entry.raw, || {}).unwrap();
        let error = entry.set_label("tag", "new".into(), |_| {
            Err(io::Error::other("sync failed"))
        });
        let invalid: RawFields = serde_json::from_str(r#"{"shim_pid":1e10000}"#).unwrap();
        assert!(
            invalid
                .parsed()
                .unwrap_err()
                .to_string()
                .contains("shim_pid")
        );
        let alias: RawFields = serde_json::from_str(r#"{"Role":"manager"}"#).unwrap();
        assert!(alias.parsed().unwrap_err().to_string().contains("role"));
        assert_eq!(error.unwrap_err().to_string(), "sync failed");
        assert_eq!(entry.meta["tag"], "new");
        assert!(read_meta(&entry.dir).unwrap().same_as(&entry.raw));
        let raw = fs::read_to_string(dir.join("meta.json")).unwrap();
        assert!(
            raw.contains(&format!("[{OPAQUE}, {RESERVED}, 1e10000]")),
            "{raw}"
        );
        assert!(serde_json::from_str::<RawFields>(r#"{"tag":"one","tag":"two"}"#).is_err());
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
            replace_meta(&file, &json!({"name":"new"}), || std::process::exit(97)).unwrap();
            panic!("crash injection did not fire");
        }
        let dir = std::env::temp_dir().join(runtime::random_id().unwrap());
        runtime::private_dir(&dir).unwrap();
        let file = File::open(&dir).unwrap();
        replace_meta(&file, &json!({"name":"old"}), || {}).unwrap();
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
        replace_meta(&file, &json!({"name":"new"}), || {}).unwrap();
        assert_eq!(read_at(&file, c"meta.json").unwrap()["name"], "new");
        fs::remove_dir_all(dir).unwrap();
    }
}
