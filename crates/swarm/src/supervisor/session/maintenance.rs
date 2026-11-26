use super::Session;
use crate::{ipc::field, providers, supervisor::runtime};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{io, path::Path};

pub(super) fn environment(plan: &providers::LaunchPlan) -> Value {
    if plan
        .env
        .iter()
        .any(|(key, value)| key.ends_with("_API_KEY") && !value.is_empty())
    {
        return Value::Null;
    }
    json!(
        plan.env
            .iter()
            .filter(|(key, _)| {
                matches!(
                    key.as_str(),
                    "HOME" | "PATH" | "CODEX_HOME" | "TERM" | "SHELL" | "LANG" | "LANGUAGE"
                ) || key.starts_with("LC_")
            })
            .map(|(key, value)| format!("{key}={value}"))
            .collect::<Vec<_>>()
    )
}

pub(in crate::supervisor) fn auth_identity(agent: &str, environment: &Value) -> Option<String> {
    if agent != "codex" {
        return None;
    }
    let entries = environment.as_array()?;
    let get = |name: &str| {
        entries
            .iter()
            .rev()
            .filter_map(Value::as_str)
            .filter_map(|entry| entry.split_once('='))
            .find_map(|(key, value)| (key == name).then_some(value))
    };
    let home = Path::new(get("HOME")?);
    if !home.is_absolute()
        || get("CODEX_HOME").is_some_and(|path| Path::new(path) != home.join(".codex"))
    {
        return None;
    }
    let auth = runtime::read_json(&home.join(".codex/auth.json")).ok()?;
    let mode = field(&auth, "auth_mode");
    let account = if mode == "apikey" {
        field(&auth, "OPENAI_API_KEY")
    } else {
        field(&auth["tokens"], "account_id")
    };
    (!account.is_empty()).then(|| format!("{:x}", Sha256::digest(format!("{mode}\0{account}"))))
}

impl Session {
    pub(in crate::supervisor) fn maintenance_source(&self) -> io::Result<Value> {
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if meta["token"] != self.token
            || !self.owner_interactive
            || !matches!(self.agent.as_str(), "agy" | "claude" | "codex" | "opencode")
            || self.schedule.is_some()
            || !self.manager.is_null()
            || !meta["checkout"].is_null()
            || !meta["lineage"]
                .as_object()
                .is_some_and(|lineage| lineage.is_empty())
            || !meta["maintenance_env"].is_array()
            || !meta["go_rollback"].is_null()
            || (self.agent == "agy" && self.dir.join("agy-input.json").try_exists()?)
        {
            return Err(io::Error::other(
                "session is outside ordinary native recovery scope",
            ));
        }
        self.verify_effective()?;
        Ok(meta)
    }

    pub(in crate::supervisor) fn maintenance_target(
        &self,
        meta: &Value,
    ) -> io::Result<providers::LaunchPlan> {
        let cwd = self.verify_effective()?;
        let launch = json!({"env":meta["maintenance_env"]});
        providers::prepare(
            &self.agent,
            cwd,
            "",
            &launch,
            runtime::launch_env(&launch["env"]),
        )
    }

    pub(in crate::supervisor) fn maintenance_preflight(
        &self,
        meta: &Value,
        target: &providers::LaunchPlan,
        thread: &str,
    ) -> io::Result<()> {
        let cwd = self.verify_effective()?;
        match self.agent.as_str() {
            "codex" => {
                if meta["provider"]["mode"] != "managed"
                    || !providers::codex::managed(&target.version)
                    || self.semantic_thread()? != thread
                    || self
                        .dir
                        .join("codex-thread-changed.json")
                        .symlink_metadata()
                        .is_ok()
                {
                    return Err(io::Error::other(
                        "original Codex thread custody is ambiguous",
                    ));
                }
                let origin = runtime::read_json(&self.dir.join("codex-profile.json"))?;
                let old = &origin["profile"];
                let current =
                    providers::codex_continuity::profile(&target.env, cwd, &old["options"])?;
                if origin["token"] != self.token
                    || [
                        "home",
                        "native_home",
                        "native_home_id",
                        "config",
                        "account",
                        "project",
                        "system_policy",
                        "cwd_config",
                    ]
                    .iter()
                    .any(|key| old[*key] != current[*key])
                {
                    return Err(io::Error::other("Codex native origin or authority changed"));
                }
                providers::codex_continuity::rollout(
                    Path::new(field(old, "native_home")),
                    cwd,
                    thread,
                )?;
            }
            "claude" => {
                let (_, _, original, _) = self.hands_off_claude_source()?;
                if original != thread {
                    return Err(io::Error::other("Claude conversation changed"));
                }
            }
            "agy" => {}
            _ => return Err(io::Error::other("no qualified recovery path")),
        }
        Ok(())
    }

    pub(in crate::supervisor) fn maintenance_observation(&self) -> io::Result<Value> {
        let row = runtime::read_json(&self.dir.join("observation.json"))?;
        if row["token"] != self.token {
            return Err(io::Error::other("native observation identity changed"));
        }
        Ok(row)
    }

    pub(in crate::supervisor) fn maintenance_lineage(&self, source: &str) -> bool {
        self.resumed_from.as_deref() == Some(source)
    }

    pub(in crate::supervisor) fn maintenance_stopped(&self, nonce: &str) -> io::Result<bool> {
        if self.verified_exit().is_none() {
            return Ok(false);
        }
        let stop = runtime::read_json(&self.dir.join("maintenance-stop.json"))?;
        if stop["token"] != self.token || stop["nonce"] != nonce {
            return Err(io::Error::other(
                "ended source has no matching maintenance stop proof",
            ));
        }
        Ok(true)
    }

    pub(in crate::supervisor) fn maintenance_identity(&self) -> io::Result<Value> {
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if meta["token"] != self.token {
            return Err(io::Error::other("session identity changed"));
        }
        Ok(meta["provider"].clone())
    }
}
