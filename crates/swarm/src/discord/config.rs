use super::{Request, rest::Discord};
use crate::{ipc, supervisor::runtime};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read};
use std::os::fd::AsRawFd;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Component, Path, PathBuf};

pub(super) fn invalid(detail: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, detail)
}

pub(super) fn path(value: &str) -> bool {
    Path::new(value).is_absolute() && text(value, 4096)
}

fn text(value: &str, max: usize) -> bool {
    !value.is_empty() && value.len() <= max && !value.chars().any(char::is_control)
}

fn id(value: &str) -> bool {
    !value.is_empty() && value.len() <= 20 && value.bytes().all(|byte| byte.is_ascii_digit())
}

pub(super) fn snowflake(value: &str) -> bool {
    value
        .parse::<u64>()
        .is_ok_and(|n| n > 0 && n.to_string() == value)
}

fn absolute(path: &Path) -> io::Result<PathBuf> {
    let mut result = PathBuf::new();
    for part in std::path::absolute(path)?.components() {
        match part {
            Component::ParentDir => {
                result.pop();
            }
            Component::CurDir => {}
            part => result.push(part),
        }
    }
    if !result.to_str().is_some_and(self::path) {
        return Err(invalid("invalid configuration path"));
    }
    Ok(result)
}

fn existing(path: &Path) -> io::Result<bool> {
    match fs::symlink_metadata(path) {
        Ok(_) => Ok(true),
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(false),
        Err(err) => Err(err),
    }
}

fn open_owner_file(path: &Path) -> io::Result<File> {
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    let meta = file.metadata()?;
    // SAFETY: geteuid takes no arguments.
    if !meta.is_file()
        || meta.mode() & 0o077 != 0
        || meta.uid() != unsafe { libc::geteuid() }
        || meta.nlink() != 1
    {
        return Err(invalid("expected a private regular owner file"));
    }
    Ok(file)
}

pub(super) fn load_token(path: &Path) -> io::Result<String> {
    let mut data = String::new();
    open_owner_file(path)
        .map_err(|_| invalid("cannot open private bot token file"))?
        .take(4097)
        .read_to_string(&mut data)
        .map_err(|_| invalid("cannot read bot token file"))?;
    if data.len() > 4096 || data.trim().is_empty() || data.trim().contains(['\r', '\n']) {
        return Err(invalid("invalid bot token file"));
    }
    Ok(data.trim().to_owned())
}

pub(super) struct Place {
    pub(super) dir: PathBuf,
    supervisor: PathBuf,
    explicit: bool,
}

const CONFIG: &str = "config.json";
pub(super) const SELECTION: &str = "discord-active.json";
const OWNER: &str = ".rust-offline-owner.json";
const ENROLLMENTS: &str = "discord-enrollments-v1.json";

impl Place {
    pub(super) fn supervisor_root(&self) -> &Path {
        &self.supervisor
    }
    #[cfg(test)]
    pub(super) fn fixture(dir: PathBuf) -> Self {
        Self {
            supervisor: dir.clone(),
            dir,
            explicit: true,
        }
    }

    pub(super) fn resolve(state: Option<&str>) -> io::Result<Self> {
        let supervisor = absolute(&ipc::state_path()?)?;
        let supplied = state.filter(|s| !s.is_empty());
        let dir = match supplied {
            Some(state) => absolute(Path::new(state))?,
            None => Self::selected(&supervisor)?,
        };
        Ok(Self {
            dir,
            supervisor,
            explicit: supplied.is_some(),
        })
    }

    pub(super) fn for_supervisor(path: &Path) -> io::Result<Self> {
        let supervisor = path.canonicalize()?;
        let dir = Self::selected(&supervisor)?;
        Ok(Self {
            dir,
            supervisor,
            explicit: false,
        })
    }

    fn registry(supervisor: &Path) -> io::Result<Value> {
        match runtime::read_json(&supervisor.join(ENROLLMENTS)) {
            Ok(data) if data.as_object().is_some_and(|entries| entries.len() <= 64) => Ok(data),
            Ok(_) => Err(invalid("malformed Discord enrollment registry")),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(json!({})),
            Err(error) => Err(error),
        }
    }

    fn enrolled_row(&self, cfg: &Config) -> io::Result<Value> {
        Ok(json!({"directory":fs::canonicalize(&self.dir)?,
            "application_id":cfg.text("application_id"),"owner_id":cfg.text("owner_id"),
            "supervisor_socket":cfg.text("daemon_socket")}))
    }

    fn register(&self, cfg: &Config) -> io::Result<()> {
        let mut registry = Self::registry(&self.supervisor)?;
        let expected = self.enrolled_row(cfg)?;
        let installation = cfg.text("installation_id");
        if !registry[installation].is_null() && registry[installation] != expected {
            return Err(invalid("Discord place changed its enrolled custody"));
        }
        for (id, row) in registry.as_object().unwrap() {
            if id == installation || row["application_id"] != cfg.text("application_id") {
                continue;
            }
            let (_, old) = Self::registered_entry(&self.supervisor, id, row).map_err(|_| {
                invalid("Discord app has another enrollment whose custody is unverified")
            })?;
            if old.enabled() {
                return Err(invalid("Discord app already has another enabled connector"));
            }
        }
        if let Ok(selected) = Self::for_supervisor(&self.supervisor)
            && let Ok(old) = selected.load()
            && old.enabled()
            && old.text("installation_id") != installation
            && old.text("application_id") == cfg.text("application_id")
        {
            return Err(invalid("Discord app already has a selected connector"));
        }
        if registry[installation].is_null() {
            if registry.as_object().unwrap().len() >= 64 {
                return Err(invalid("Discord enrollment registry is full"));
            }
            registry[installation] = expected;
            runtime::write_json(&self.supervisor.join(ENROLLMENTS), &registry)?;
        }
        Ok(())
    }

    fn registered_entry(
        supervisor: &Path,
        installation: &str,
        row: &Value,
    ) -> io::Result<(Self, Config)> {
        let directory = row["directory"]
            .as_str()
            .filter(|path| !path.is_empty())
            .ok_or_else(|| invalid("Discord installation has no enrolled directory"))?;
        if !Path::new(directory).is_absolute() {
            return Err(invalid("invalid registered Discord directory"));
        }
        let place = Self {
            dir: directory.into(),
            supervisor: supervisor.into(),
            explicit: true,
        };
        let cfg = place.load()?;
        place.ensure_owned(&cfg)?;
        if cfg.text("installation_id") != installation || place.enrolled_row(&cfg)? != *row {
            return Err(invalid(
                "Discord registered enrollment failed custody verification",
            ));
        }
        Ok((place, cfg))
    }

    pub(super) fn for_installation(
        supervisor: &Path,
        installation: &str,
    ) -> io::Result<(Self, Config)> {
        let registry = Self::registry(supervisor)?;
        let row = &registry[installation];
        if !row.is_null() {
            return Self::registered_entry(supervisor, installation, row);
        }
        let selected = Self::for_supervisor(supervisor)?;
        let cfg = selected.load()?;
        selected.ensure_owned(&cfg)?;
        if cfg.text("installation_id") != installation {
            return Err(invalid("unknown enrolled Discord installation"));
        }
        Ok((selected, cfg))
    }

    pub(super) fn enrolled(supervisor: &Path) -> io::Result<Vec<(Self, Config)>> {
        let registry = Self::registry(supervisor)?;
        let mut places = registry
            .as_object()
            .unwrap()
            .iter()
            .filter_map(|(id, row)| Self::registered_entry(supervisor, id, row).ok())
            .collect::<Vec<_>>();
        if let Ok(selected) = Self::for_supervisor(supervisor)
            && let Ok(cfg) = selected.load()
            && selected.ensure_owned(&cfg).is_ok()
            && registry[cfg.text("installation_id")].is_null()
        {
            places.push((selected, cfg));
        }
        Ok(places)
    }

    pub(super) fn verify_selection(&self) -> io::Result<()> {
        if !self.explicit && self.dir != Self::selected(&self.supervisor)? {
            return Err(io::Error::other(
                "active connector selection changed, retry",
            ));
        }
        Ok(())
    }

    fn selected(supervisor: &Path) -> io::Result<PathBuf> {
        match Self::read_selection(supervisor) {
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(supervisor.join("discord")),
            result => result,
        }
    }

    pub(super) fn read_selection(supervisor: &Path) -> io::Result<PathBuf> {
        let selector = supervisor.join(SELECTION);
        let mut bytes = Vec::new();
        open_owner_file(&selector)?
            .take(8193)
            .read_to_end(&mut bytes)?;
        let value = serde_json::from_slice::<String>(&bytes)
            .ok()
            .filter(|s| bytes.len() <= 8192 && path(s))
            .ok_or_else(|| invalid("invalid active integration"))?;
        Ok(PathBuf::from(value))
    }

    pub(super) fn load(&self) -> io::Result<Config> {
        let meta = fs::symlink_metadata(&self.dir)?;
        // SAFETY: geteuid takes no arguments.
        if !meta.is_dir() || meta.mode() & 0o077 != 0 || meta.uid() != unsafe { libc::geteuid() } {
            return Err(invalid("invalid private state directory"));
        }
        let cfg = Config(runtime::read_json(&self.dir.join(CONFIG))?);
        cfg.validate()?;
        Ok(cfg)
    }

    pub(super) fn lock(&self) -> io::Result<(File, File)> {
        runtime::private_dir(&self.supervisor)?;
        runtime::private_dir(&self.dir)?;
        let lock = |path: &Path| {
            let file = OpenOptions::new()
                .read(true)
                .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW)
                .open(path)?;
            Ok::<_, io::Error>(file)
        };
        let supervisor = lock(&self.supervisor)?;
        let connector = lock(&self.dir)?;
        let first = supervisor.metadata()?;
        let second = connector.metadata()?;
        let acquire = |file: &File| {
            // SAFETY: flock borrows the live file descriptor.
            if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } == -1 {
                let error = io::Error::last_os_error();
                if error.kind() == io::ErrorKind::WouldBlock {
                    return Err(io::Error::other(
                        "another configuration change is in progress",
                    ));
                }
                return Err(error);
            }
            Ok(())
        };
        acquire(&supervisor)?;
        if (first.dev(), first.ino()) != (second.dev(), second.ino()) {
            acquire(&connector)?;
        }
        Ok((supervisor, connector))
    }

    pub(super) fn ensure_owned(&self, cfg: &Config) -> io::Result<()> {
        if existing(&self.dir.join("discord.db"))?
            || runtime::read_json(&self.dir.join(OWNER)).ok().as_ref()
                != Some(&json!(cfg.text("installation_id")))
        {
            return Err(io::Error::other(
                "existing connector custody requires a qualified migration",
            ));
        }
        Ok(())
    }

    pub(super) fn claim(
        &self,
        cfg: &Config,
    ) -> io::Result<(File, File, std::os::unix::net::UnixListener)> {
        let _locks = self.lock()?;
        self.verify_selection()?;
        let current = self.load()?;
        if current.0 != cfg.0 || !cfg.enabled() {
            return Err(io::Error::other(
                "connector is disabled or its configuration changed",
            ));
        }
        self.ensure_owned(cfg)?;
        let lease = runtime::lock(&self.dir.join("bridge.lock"))?;
        self.ensure_owned(cfg)?;
        self.register(cfg)?;
        let app_lease = runtime::lock(
            &self
                .supervisor
                .join(format!(".discord-app-{}.lock", cfg.text("application_id"))),
        )
        .map_err(|_| invalid("Discord application already has an active Gateway lease"))?;
        let path = self.dir.join("bridge.sock");
        runtime::reclaim_socket(&path)?;
        Ok((lease, app_lease, runtime::bind(&path)?))
    }

    fn ensure_offline(&self, cfg: Option<&Config>) -> io::Result<()> {
        if existing(&self.dir.join("bridge.sock"))? || existing(&self.dir.join("discord.db"))? {
            return Err(io::Error::other(
                "existing connector custody requires a qualified migration",
            ));
        }
        if let Some(cfg) = cfg {
            self.ensure_owned(cfg)?;
        }
        Ok(())
    }

    pub(super) fn configure(&self, req: &Request<'_>) -> io::Result<Config> {
        let _locks = self.lock()?;
        self.verify_selection()?;
        let mut previous = None;
        if existing(&self.dir.join(CONFIG))? {
            previous = Some(self.load()?);
        }
        self.ensure_offline(previous.as_ref())?;
        let mut cfg = match &previous {
            Some(cfg) => cfg.clone(),
            None => Config(json!({
                "enabled": true, "version": 1,
                "installation_id": if existing(&self.dir.join(OWNER))? {
                    let id = runtime::read_json(&self.dir.join(OWNER))?;
                    id.as_str().filter(|id| id.len() == 32 && id.bytes().all(|b| b.is_ascii_hexdigit())).ok_or_else(|| invalid("invalid connector owner marker"))?.to_owned()
                } else {
                    let mut bytes = [0; 16];
                    File::open("/dev/urandom")?.read_exact(&mut bytes)?;
                    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
                },
                "daemon_socket": absolute(&ipc::socket_path()?)?,
                "work_dir": absolute(&std::env::current_dir()?)?,
                "guild_ids": null, "channel_ids": null, "user_ids": null,
                "receptionist_cli": "codex", "receptionist_model": "",
            })),
        };
        for (flag, key) in [
            ("name", "name"),
            ("application-id", "application_id"),
            ("owner-id", "owner_id"),
            ("mode", "mode"),
            ("manager", "manager_cli"),
            ("project-access", "project_access"),
            ("receptionist-cli", "receptionist_cli"),
            ("receptionist-model", "receptionist_model"),
        ] {
            if let Some(value) = req.get(flag) {
                cfg.0[key] = json!(value);
            }
        }
        for (flag, key) in [("workdir", "work_dir"), ("token-file", "token_file")] {
            if let Some(value) = req.get(flag) {
                cfg.0[key] = if value.is_empty() {
                    json!("")
                } else {
                    json!(absolute(Path::new(value))?)
                };
            }
        }
        cfg.0["owner_id"] = json!(cfg.text("owner_id"));
        for (flag, key) in [
            ("guild", "guild_ids"),
            ("channel", "channel_ids"),
            ("allow-user", "user_ids"),
        ] {
            if !req.values(flag).is_empty() {
                cfg.0[key] = json!(req.values(flag));
            }
        }
        if req.on("all-channels") {
            if !req.values("channel").is_empty() {
                return Err(invalid("--all-channels conflicts with --channel"));
            }
            cfg.0["channel_ids"] = Value::Null;
        }
        if let Some(old) = &previous {
            if (old.text("mode") == "personal" || cfg.text("mode") == "personal")
                && old.text("mode") != cfg.text("mode")
            {
                return Err(invalid(
                    "changing a personal DM enrollment requires a new --state-dir",
                ));
            }
            if ["owner_id", "application_id"]
                .iter()
                .any(|key| old.text(key) != cfg.text(key))
            {
                return Err(invalid(
                    "changing the Discord owner or app requires a new --state-dir",
                ));
            }
        }
        if cfg.managed() {
            if !req.values("allow-user").is_empty() {
                return Err(invalid(
                    "every human participant can direct a teammate; --allow-user is a legacy setting",
                ));
            }
            if cfg.text("manager_cli").is_empty() {
                cfg.0["manager_cli"] = json!("codex");
            }
            if previous.is_none() && cfg.text("project_access").is_empty() {
                cfg.0["project_access"] = json!("vm");
            }
            if cfg.0["supervisor"].is_null() {
                let select = |var, default| {
                    absolute(&ipc::env_path(var).unwrap_or_else(|| self.supervisor.join(default)))
                };
                cfg.0["supervisor"] = json!({
                    "state_dir": self.supervisor,
                    "lock": select("SWARM_DAEMON_LOCK", "daemon.lock")?,
                    "log": select("SWARM_DAEMON_LOG", "daemon.log")?,
                });
            }
            cfg.0["work_dir"] = json!(fs::canonicalize(cfg.text("work_dir"))?);
            if !req.values("project").is_empty() {
                let mut projects = serde_json::Map::new();
                for project in req.values("project") {
                    let (name, dir) = project
                        .split_once('=')
                        .ok_or_else(|| invalid("expected a unique name=directory"))?;
                    let name = name.trim().to_lowercase();
                    if name.is_empty() || dir.is_empty() || projects.contains_key(&name) {
                        return Err(invalid("expected a unique name=directory"));
                    }
                    let dir = fs::canonicalize(dir)?;
                    if !dir.is_dir() {
                        return Err(invalid("project workspace must be a directory"));
                    }
                    projects.insert(name, json!(dir));
                }
                cfg.0["projects"] = Value::Object(projects);
            }
        } else {
            if req.get("manager").is_some()
                || req.get("project").is_some()
                || req.get("project-access").is_some()
            {
                return Err(invalid(
                    "--manager, --project and --project-access require teammate or personal mode",
                ));
            }
            for key in [
                "mode",
                "manager_cli",
                "projects",
                "supervisor",
                "project_access",
            ] {
                if cfg.text("mode") == "legacy" || key != "mode" {
                    cfg.0.as_object_mut().unwrap().remove(key);
                }
            }
        }
        if cfg.managed()
            && (cfg.text("mode") == "personal"
                || cfg.text("application_id").is_empty()
                || cfg.text("name").is_empty()
                || cfg.0["guild_ids"].as_array().is_none_or(Vec::is_empty))
        {
            Discord::connect(&cfg)?.discover(&mut cfg)?;
        }
        cfg.0["enabled"] = json!(true);
        cfg.validate()?;
        load_token(Path::new(cfg.text("token_file")))?;
        if !Path::new(cfg.text("work_dir")).is_dir() {
            return Err(invalid("working directory does not exist"));
        }
        self.register(&cfg)?;
        runtime::write_json(&self.dir.join(OWNER), &json!(cfg.text("installation_id")))?;
        runtime::write_json(&self.dir.join(CONFIG), &cfg.0)?;
        runtime::write_json(&self.supervisor.join(SELECTION), &json!(self.dir))?;
        Ok(cfg)
    }

    pub(super) fn link_automatic(&self, cfg: &Config, route: Value) -> io::Result<Config> {
        let _locks = self.lock()?;
        let current = self.load()?;
        self.ensure_owned(&current)?;
        if current.0 != cfg.0 || !current.enabled() {
            return Err(invalid(
                "connector changed during automatic thread readback",
            ));
        }
        if super::team_thread::linked_values(&current).any(|existing| existing == &route) {
            return Ok(current);
        }
        let mut updated = current;
        if updated.0["team_discussion"].is_null() {
            updated.0["team_discussion"] = route;
        } else {
            if updated.0["team_discussions"].is_null() {
                updated.0["team_discussions"] = json!([]);
            }
            let entries = updated.0["team_discussions"]
                .as_array_mut()
                .ok_or_else(|| invalid("malformed linked discussions"))?;
            if entries.len() >= 16 {
                return Err(invalid("additional linked discussion limit reached"));
            }
            entries.push(route);
        }
        updated.validate()?;
        runtime::write_json(&self.dir.join(CONFIG), &updated.0)?;
        Ok(updated)
    }

    pub(super) fn probe_gateway(&self, cfg: &Config) -> io::Result<&'static str> {
        if let Ok(status) = super::local::call(&self.dir, "status", "", None)
            && status["connected"] == true
            && status["installation_id"] == cfg.text("installation_id")
        {
            return Ok("installed connector reports an active Gateway");
        }
        let _locks = self.lock()?;
        self.verify_selection()?;
        let current = self.load()?;
        if current.0 != cfg.0 {
            return Err(io::Error::other("configuration changed, retry doctor"));
        }
        self.ensure_offline(Some(cfg))?;
        if !cfg.enabled() {
            return Err(io::Error::other("connector is disabled"));
        }
        // The Go service acquires the same bridge lease before opening Gateway.
        let _lease = runtime::lock(&self.dir.join("bridge.lock"))?;
        self.ensure_offline(Some(cfg))?;
        super::gateway::probe(cfg)
    }

    pub(super) fn disconnect(&self) -> io::Result<()> {
        let _locks = self.lock()?;
        self.verify_selection()?;
        if !existing(&self.dir.join(CONFIG))? {
            return self.ensure_offline(None);
        }
        let mut cfg = self.load()?;
        self.ensure_owned(&cfg)?;
        let socket = self.dir.join("bridge.sock");
        let _lease = match runtime::lock(&self.dir.join("bridge.lock")) {
            Ok(lease) => lease,
            Err(err) if existing(&socket)? => {
                super::call_runtime::stop_if_running(&self.dir, cfg.text("installation_id"))?;
                let body = json!({});
                let response = super::local::call(
                    &self.dir,
                    "shutdown",
                    "",
                    Some((cfg.text("installation_id"), &body)),
                )?;
                if response["stopped"] != true {
                    return Err(err);
                }
                let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
                loop {
                    if let Ok(lease) = runtime::lock(&self.dir.join("bridge.lock")) {
                        break lease;
                    }
                    if std::time::Instant::now() >= deadline {
                        return Err(err);
                    }
                    std::thread::sleep(std::time::Duration::from_millis(50));
                }
            }
            Err(err) => return Err(err),
        };
        self.ensure_owned(&cfg)?;
        runtime::reclaim_socket(&socket)?;
        if cfg.enabled() {
            cfg.0["enabled"] = json!(false);
            runtime::write_json(&self.dir.join(CONFIG), &cfg.0)?;
        }
        Ok(())
    }
}

#[derive(Clone)]
pub(super) struct Config(pub(super) Value);

impl Config {
    pub(super) fn text(&self, key: &str) -> &str {
        self.0[key].as_str().unwrap_or("")
    }

    pub(super) fn enabled(&self) -> bool {
        self.0["enabled"].as_bool() == Some(true)
    }

    pub(super) fn managed(&self) -> bool {
        matches!(self.text("mode"), "personal" | "teammate")
    }

    fn validate(&self) -> io::Result<()> {
        let text = |key| self.text(key);
        let map = self
            .0
            .as_object()
            .ok_or_else(|| invalid("malformed config"))?;
        if self.0["version"].as_u64() != Some(1) {
            return Err(invalid("unsupported config version"));
        }
        let known_text = [
            "project_access",
            "mode",
            "manager_cli",
            "installation_id",
            "name",
            "application_id",
            "dm_channel_id",
            "owner_id",
            "token_file",
            "daemon_socket",
            "receptionist_cli",
            "receptionist_model",
            "work_dir",
        ];
        let collections = ["guild_ids", "channel_ids", "user_ids"];
        for (key, value) in map {
            let valid = if known_text.contains(&key.as_str()) {
                value.is_string()
            } else if collections.contains(&key.as_str()) {
                value.is_null()
                    || value
                        .as_array()
                        .is_some_and(|ids| ids.iter().all(|value| value.as_str().is_some_and(id)))
            } else {
                match key.as_str() {
                    "version" => true,
                    "enabled" => value.is_boolean(),
                    "projects"
                    | "supervisor"
                    | "previous_supervisor"
                    | "calls"
                    | "team_discussion" => value.is_object() || value.is_null(),
                    "team_discussions" => value.is_array() || value.is_null(),
                    _ => false,
                }
            };
            if !valid {
                return Err(invalid("malformed config"));
            }
        }
        if !self::text(text("installation_id"), 128) || !self::text(text("name"), 256) {
            return Err(invalid("invalid installation identity or name"));
        }
        if !id(text("application_id")) || (!text("owner_id").is_empty() && !id(text("owner_id"))) {
            return Err(invalid("invalid application or owner ID"));
        }
        let access = text("project_access");
        if !access.is_empty() && (!self.managed() || !matches!(access, "enrolled" | "vm")) {
            return Err(invalid(
                "project access must be enrolled or vm for a teammate or personal manager",
            ));
        }
        let ids = |key| self.0[key].as_array().map_or(0, Vec::len);
        match text("mode") {
            "" | "legacy" if !id(text("owner_id")) => return Err(invalid("invalid owner ID")),
            "teammate" if ids("guild_ids") == 0 => {
                return Err(invalid("teammate requires an enrolled server"));
            }
            "personal"
                if !snowflake(text("owner_id"))
                    || !snowflake(text("dm_channel_id"))
                    || text("owner_id") == text("application_id")
                    || collections.iter().any(|key| ids(key) != 0) =>
            {
                return Err(invalid(
                    "personal mode requires one owner and a verified DM channel",
                ));
            }
            "" | "legacy" | "teammate" | "personal" => {}
            _ => return Err(invalid("unknown connection mode")),
        }
        if text("mode") != "personal" && !text("dm_channel_id").is_empty() {
            return Err(invalid("DM channel requires personal mode"));
        }
        if self.managed() && !matches!(text("manager_cli"), "codex" | "claude") {
            return Err(invalid("teammate requires a Codex or Claude manager"));
        }
        if let Some(projects) = self.0["projects"].as_object()
            && (projects.len() > 32
                || projects.iter().any(|(name, root)| {
                    !self::text(name, 128)
                        || name.trim() != name
                        || !root.as_str().is_some_and(|root| {
                            path(root)
                                && absolute(Path::new(root))
                                    .ok()
                                    .as_deref()
                                    .and_then(Path::to_str)
                                    == Some(root)
                        })
                }))
        {
            return Err(invalid("invalid named project"));
        }
        for key in ["token_file", "daemon_socket", "work_dir"] {
            if !path(text(key)) {
                return Err(invalid("invalid config path"));
            }
        }
        for key in ["receptionist_cli", "receptionist_model"] {
            if !text(key).is_empty() && !self::text(text(key), 256) {
                return Err(invalid("invalid receptionist setting"));
            }
        }
        let supervisor = &self.0["supervisor"];
        let valid_supervisor = |value: &Value, fields: &[&str]| {
            value.as_object().is_some_and(|obj| {
                obj.len() == fields.len()
                    && fields.iter().all(|key| {
                        value[*key].as_str().is_some_and(|v| {
                            if *key == "endpoint" {
                                self::text(v, 128)
                            } else {
                                path(v)
                            }
                        })
                    })
            })
        };
        if !supervisor.is_null() && !valid_supervisor(supervisor, &["state_dir", "lock", "log"]) {
            return Err(invalid("invalid enrolled supervisor paths"));
        }
        super::team_thread::Route::configured(self)?;
        super::team_thread::Route::additional(self)?;
        let previous = &self.0["previous_supervisor"];
        if !previous.is_null()
            && (!self.managed()
                || supervisor.is_null()
                || !valid_supervisor(
                    previous,
                    &["state_dir", "lock", "log", "socket", "endpoint"],
                )
                || previous["state_dir"] == supervisor["state_dir"]
                || previous["socket"] == self.0["daemon_socket"])
        {
            return Err(invalid("invalid previous supervisor enrollment"));
        }
        Ok(())
    }

    pub(super) fn status(&self) -> Value {
        let mut status = json!({
            "name": self.text("name"), "installation_id": self.text("installation_id"),
            "connected": false, "workers": null,
        });
        if !self.text("mode").is_empty() {
            status["mode"] = json!(self.text("mode"));
        }
        if self.managed() {
            status["project_access"] = json!(if self.text("project_access") == "vm" {
                "vm"
            } else {
                "enrolled"
            });
        }
        if self.0["calls"].is_object() {
            status["calls"] = json!("unavailable");
        }
        status
    }

    pub(super) fn manager_capabilities(&self) -> Vec<&'static str> {
        let mut caps = vec![
            "manager-launch",
            "manager-provenance",
            "manager-retire",
            "teammate-worker-bridge",
            "owner-launch",
            "message-origin",
            "journal-subscribe-from",
            "logins.manage.v1",
        ];
        if self.text("project_access") == "vm" {
            caps.extend(["manager-retire-cancel", "dispatcher-worker-mail"]);
        }
        caps
    }

    pub(super) fn check_supervisor(&self, client: &ipc::Client, caps: &[&str]) -> io::Result<()> {
        let state = match self.0["supervisor"]["state_dir"].as_str() {
            Some(state) => PathBuf::from(state),
            None => {
                if absolute(&ipc::socket_path()?)? != Path::new(self.text("daemon_socket")) {
                    return Err(invalid("select the original supervisor environment"));
                }
                absolute(&ipc::state_path()?)?
            }
        };
        let state = fs::canonicalize(&state).unwrap_or(state);
        let digest = Sha256::digest(state.as_os_str().as_encoded_bytes());
        let expected = format!(
            "ep-{:02x}{:02x}{:02x}{:02x}",
            digest[0], digest[1], digest[2], digest[3]
        );
        if client.endpoint() != expected {
            return Err(invalid(
                "socket answered for a different enrolled supervisor",
            ));
        }
        for cap in caps {
            if !client.supports(cap) {
                return Err(io::Error::other(format!(
                    "daemon lacks {cap}; upgrade the supervisor"
                )));
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod registry_tests {
    use super::*;

    #[test]
    fn two_enrollments_survive_selection_changes_and_refuse_aliases() {
        let dir =
            std::env::temp_dir().join(format!("swarm-places-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&dir).unwrap();
        let make = |name: &str, app: &str| {
            let path = dir.join(name);
            runtime::private_dir(&path).unwrap();
            let cfg = Config(
                json!({"version":1,"enabled":true,"installation_id":name,"name":name,
                "application_id":app,"owner_id":"10","mode":"personal","manager_cli":"codex","dm_channel_id":"44",
                "work_dir":dir,"token_file":dir.join("token"),"daemon_socket":dir.join("daemon")}),
            );
            let place = Place {
                dir: path,
                supervisor: dir.clone(),
                explicit: true,
            };
            runtime::write_json(&place.dir.join(OWNER), &json!(name)).unwrap();
            runtime::write_json(&place.dir.join(CONFIG), &cfg.0).unwrap();
            (place, cfg)
        };
        let (a, acfg) = make("first", "21");
        a.register(&acfg).unwrap();
        runtime::write_json(&dir.join(SELECTION), &json!(a.dir)).unwrap();
        let (b, bcfg) = make("second", "22");
        b.register(&bcfg).unwrap();
        runtime::write_json(&dir.join(SELECTION), &json!(b.dir)).unwrap();
        assert_eq!(
            Place::for_installation(&dir, "first").unwrap().0.dir,
            a.dir.canonicalize().unwrap()
        );
        assert_eq!(
            Place::for_installation(&dir, "second").unwrap().0.dir,
            b.dir.canonicalize().unwrap()
        );
        assert_eq!(Place::enrolled(&dir).unwrap().len(), 2);
        let (duplicate, cfg) = make("conflict", "21");
        assert!(duplicate.register(&cfg).is_err());
        assert!(Place::for_installation(&dir, "conflict").is_err());
        runtime::write_json(&a.dir.join(OWNER), &json!("wrong")).unwrap();
        assert!(
            duplicate
                .register(&cfg)
                .unwrap_err()
                .to_string()
                .contains("unverified")
        );
        assert!(Place::for_installation(&dir, "first").is_err());
        assert_eq!(Place::enrolled(&dir).unwrap().len(), 1);
        fs::remove_dir_all(dir).unwrap();
    }
}
