use super::Session;
use crate::{ipc::field, providers, supervisor::runtime};
use serde_json::{Value, json};
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
                    "HOME"
                        | "PATH"
                        | "CODEX_HOME"
                        | "TERM"
                        | "SHELL"
                        | "LANG"
                        | "LANGUAGE"
                        | "XDG_CONFIG_HOME"
                        | "XDG_DATA_HOME"
                        | "XDG_CACHE_HOME"
                        | "XDG_STATE_HOME"
                ) || key.starts_with("LC_")
            })
            .map(|(key, value)| format!("{key}={value}"))
            .collect::<Vec<_>>()
    )
}

impl Session {
    pub(in crate::supervisor) fn maintenance_source(&self) -> io::Result<Value> {
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if meta["token"] != self.token
            || !self.owner_interactive
            || !matches!(self.agent.as_str(), "agy" | "claude" | "codex" | "opencode")
            || self.schedule.is_some()
            || !self.manager.is_null()
            || !self.mailbox.is_null()
            || self.worker_native_installation.is_some()
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
        changed_auth: bool,
    ) -> io::Result<Value> {
        if !providers::codex_continuity::newer_or_same(
            &target.version,
            field(&meta["provider"], "version"),
        ) {
            return Err(io::Error::other(
                "native recovery will not downgrade the source CLI",
            ));
        }
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
                if changed_auth {
                    return providers::codex_continuity::relogin_proof(
                        &self.dir,
                        &self.token,
                        cwd,
                        &target.env,
                        thread,
                    );
                }
                let origin = runtime::read_json(&self.dir.join("codex-profile.json"))?;
                let old = &origin["profile"];
                let current =
                    providers::codex_continuity::profile(&target.env, cwd, &old["options"])?;
                if origin["token"] != self.token
                    || !providers::codex_continuity::same_origin(old, &current)
                    || old["account"] != current["account"]
                {
                    return Err(io::Error::other("Codex native origin or authority changed"));
                }
                providers::codex_continuity::retained_rollout(
                    Path::new(field(old, "native_home")),
                    cwd,
                    thread,
                    &meta["codex_resume"],
                )?;
            }
            "claude" => {
                let (_, _, original, _) = self.hands_off_source()?;
                if original != thread {
                    return Err(io::Error::other("Claude conversation changed"));
                }
            }
            "agy" => {}
            _ => return Err(io::Error::other("no qualified recovery path")),
        }
        Ok(Value::Null)
    }

    pub(in crate::supervisor) fn maintenance_observation(&self) -> io::Result<Value> {
        let row = runtime::read_json(&self.dir.join("observation.json"))?;
        if row["token"] != self.token {
            return Err(io::Error::other("native observation identity changed"));
        }
        Ok(row)
    }

    pub(in crate::supervisor) fn maintenance_joined(&self, thread: &str) -> bool {
        self.dial().is_ok()
            && self.maintenance_observation().is_ok_and(|row| {
                row["status"]["Process"] == "running"
                    && row["conversation_id"] == thread
                    && match self.agent.as_str() {
                        "agy" => row["native_joined"] == true,
                        "codex" => {
                            row["backend_available"] == true && row["requests_available"] == true
                        }
                        "claude" => true,
                        _ => false,
                    }
            })
    }

    pub(in crate::supervisor) fn maintenance_exited(&self) -> bool {
        self.verified_exit().is_some()
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
