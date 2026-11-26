use super::{Session, opencode, providers, runtime};
use crate::{ipc::field, providers::opencode_handoff as history};
use serde_json::{Value, json};
use std::{
    collections::BTreeSet,
    fs, io,
    path::{Path, PathBuf},
    time::{Duration, Instant},
};

impl Session {
    fn opencode_source_identity(&self) -> io::Result<(Value, Value)> {
        if self.agent != "opencode" || !self.owner_interactive || self.schedule.is_some() {
            return Err(io::Error::other(
                "OpenCode hands-off requires an ordinary managed source",
            ));
        }
        let cwd = self.verify_effective()?;
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if meta["token"] != self.token
            || meta["provider"]["mode"] != "managed"
            || !meta["opencode_profile"].is_object()
        {
            return Err(io::Error::other(
                "OpenCode managed source profile is unproved",
            ));
        }
        let native = self.opencode_native()?;
        let id = json!({"version":1,"workerID":self.id,"rootID":native["rootID"],"directory":cwd,
            "sessionInstance":native["sessionInstance"],"backendGeneration":native["backendGeneration"],
            "nativeCreatedAtMS":native["nativeCreatedAtMS"]});
        Ok((meta, id))
    }

    pub(in crate::supervisor) fn hands_off_opencode_source(
        &self,
    ) -> io::Result<(PathBuf, PathBuf, String, Value)> {
        let (meta, id) = self.opencode_source_identity()?;
        let version = field(&meta["provider"], "version");
        opencode::version(version)?;
        let (data, origin) = if self.opencode_stopped()? {
            (
                self.stopped_opencode_handoff(&meta, &id)?,
                "native_cli_export",
            )
        } else if self.is_live() {
            (self.live_opencode_handoff(&id, version)?, "live_sdk")
        } else {
            return Err(io::Error::other(
                "OpenCode source running or stopped custody is unconfirmed",
            ));
        };
        if self.opencode_source_identity()? != (meta.clone(), id.clone()) {
            return Err(io::Error::other(
                "OpenCode source custody changed during history read",
            ));
        }
        let (path, proof) = history::publish(&self.dir, &self.token, &id, version, origin, data)?;
        Ok((
            self.effective.clone(),
            path,
            field(&id, "rootID").into(),
            proof,
        ))
    }

    pub(in crate::supervisor) fn verify_opencode_handoff(&self, proof: &Value) -> io::Result<()> {
        let (_, id) = self.opencode_source_identity()?;
        if proof["native_identity"] != id
            || proof["source_dir"] != json!(self.dir)
            || proof["instance"] != self.incarnation()
        {
            return Err(io::Error::other(
                "OpenCode handoff source changed before reservation",
            ));
        }
        history::verify_artifact(proof)
    }

    fn live_opencode_handoff(&self, id: &Value, version: &str) -> io::Result<Value> {
        let deadline = Instant::now() + Duration::from_secs(20);
        let mut messages = Vec::new();
        let mut info = Value::Null;
        let mut before = String::new();
        let mut cursors = BTreeSet::new();
        let mut bytes = 0usize;
        for _ in 0..64 {
            let mut limit = 100;
            let page = loop {
                let remaining = deadline.saturating_duration_since(Instant::now());
                if remaining.is_zero() {
                    return Err(io::Error::other(
                        "OpenCode handoff history deadline elapsed",
                    ));
                }
                let mut stream = self.dial()?;
                stream.set_read_timeout(Some(remaining))?;
                runtime::write_control(
                    &mut stream,
                    &json!({"type":"opencode_handoff_page","limit":limit,"before":before}),
                )?;
                let response = runtime::read_control(&mut stream)?;
                if response["type"] == "opencode_result" {
                    break response["body"].clone();
                }
                if limit <= 1 || !field(&response, "error").contains("exceeds the IPC limit") {
                    return Err(io::Error::other(
                        "OpenCode live native history is unavailable",
                    ));
                }
                limit /= 5;
            };
            if page["native_session_id"] != id["rootID"]
                || page["native_created_at_ms"] != id["nativeCreatedAtMS"]
                || page["session_instance"] != id["sessionInstance"]
                || page["backend_generation"] != id["backendGeneration"]
                || page["native_version"] != version
                || !page["info"].is_object()
                || page["info"]["id"] != id["rootID"]
                || page["info"]["directory"] != id["directory"]
                || page["info"]["time"]["created"] != id["nativeCreatedAtMS"]
            {
                return Err(io::Error::other(
                    "OpenCode live history page identity changed",
                ));
            }
            if info.is_null() {
                info = page["info"].clone();
            }
            let rows = page["history"]
                .as_array()
                .filter(|rows| rows.len() <= limit)
                .ok_or_else(|| io::Error::other("OpenCode live history page is malformed"))?;
            for row in rows {
                bytes += serde_json::to_vec(row)?.len();
            }
            if bytes > history::MAX_BYTES || messages.len() + rows.len() > 4096 {
                return Err(io::Error::other(
                    "OpenCode live history exceeds retained bounds",
                ));
            }
            messages.extend(rows.iter().cloned());
            let next = page["next_cursor"].as_str().unwrap_or_default();
            if next.is_empty() {
                let data = json!({"info":info,"messages":messages});
                history::validate(&data, id)?;
                return Ok(data);
            }
            if rows.is_empty() || next.len() > 4096 || !cursors.insert(next.to_owned()) {
                return Err(io::Error::other(
                    "OpenCode live history cursor did not progress",
                ));
            }
            before = next.into();
        }
        Err(io::Error::other(
            "OpenCode live history exceeds the page limit",
        ))
    }

    fn stopped_opencode_handoff(&self, meta: &Value, id: &Value) -> io::Result<Value> {
        use std::os::unix::fs::MetadataExt;
        let profile = &meta["opencode_profile"];
        let supplied = json!(
            profile
                .as_object()
                .expect("qualified profile")
                .iter()
                .map(|(key, value)| format!("{key}={}", value.as_str().unwrap_or_default()))
                .collect::<Vec<_>>()
        );
        let profile_env: Vec<_> = opencode::environment(&supplied)?.into_iter().collect();
        if opencode::profile(&profile_env) != *profile {
            return Err(io::Error::other("OpenCode original native profile changed"));
        }
        let home = Path::new(field(profile, "HOME"));
        let data = field(profile, "XDG_DATA_HOME");
        let data_root = if data.is_empty() {
            home.join(".local/share")
        } else {
            PathBuf::from(data)
        };
        let database = data_root.join("opencode/opencode.db");
        let db = database.symlink_metadata()?;
        // SAFETY: geteuid reads the current effective uid only.
        if !db.is_file()
            || db.uid() != unsafe { libc::geteuid() }
            || db.nlink() != 1
            || db.mode() & 0o022 != 0
            || database.canonicalize()? != database
        {
            return Err(io::Error::other(
                "OpenCode original native database is unproved",
            ));
        }
        let exe = Path::new(field(&meta["provider"], "executable"));
        let original = &meta["provider"]["identity"];
        if !exe.is_absolute()
            || exe.canonicalize()? != exe
            || &providers::identity(exe)? != original
        {
            return Err(io::Error::other("OpenCode source executable changed"));
        }
        let data = providers::opencode_export(
            exe,
            &self.effective,
            profile_env,
            field(id, "rootID"),
            field(&meta["provider"], "version"),
        )?;
        let current = fs::symlink_metadata(&database)?;
        if &providers::identity(exe)? != original
            || (db.dev(), db.ino()) != (current.dev(), current.ino())
            || !self.opencode_stopped()?
        {
            return Err(io::Error::other(
                "OpenCode source changed during native export",
            ));
        }
        history::validate(&data, id)?;
        Ok(data)
    }
}
