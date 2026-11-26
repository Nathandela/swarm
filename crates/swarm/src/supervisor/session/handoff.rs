//! Bind a hands-off successor to the source provider's actual transcript and checkout.
use super::{Session, owner_gone};
use crate::{ipc::field, providers, supervisor::runtime};
use serde_json::{Value, json};
use std::{
    env, io,
    os::unix::fs::MetadataExt,
    path::{Path, PathBuf},
};

impl Session {
    pub(in crate::supervisor) fn hands_off_source(
        &self,
    ) -> io::Result<(PathBuf, PathBuf, String, Value)> {
        if self.agent == "agy" {
            return self.hands_off_agy_source();
        }
        if self.agent == "opencode" {
            return self.hands_off_opencode_source();
        }
        if !matches!(self.agent.as_str(), "claude" | "codex")
            || !self.owner_interactive
            || self.schedule.is_some()
        {
            return Err(io::Error::other(
                "hands-off source requires an ordinary worker with a verified native transcript",
            ));
        }
        if self.is_live() {
            self.dial()?;
        } else if !self.exit()?["error"].is_null() || !owner_gone(&self.dir, &self.token)? {
            return Err(io::Error::other("hands-off source ownership is unproven"));
        }
        let cwd = self.verify_effective()?.to_owned();
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if self.agent == "codex" {
            return self.codex_hands_off(cwd, &meta);
        }
        let profile = &meta["claude_profile"];
        let home = env::var_os("HOME")
            .map(PathBuf::from)
            .ok_or_else(|| io::Error::other("daemon home is missing"))?
            .canonicalize()?;
        if profile["home"].as_str().map(Path::new) != Some(home.as_path()) {
            return Err(io::Error::other(
                "Claude transcript is outside the daemon's trusted native profile",
            ));
        }
        let current = providers::claude::profile(
            &runtime::launch_env(&Value::Null),
            &Value::Null,
            &profile["options"],
        )?;
        if ["home", "home_id", "origin", "settings"]
            .iter()
            .any(|key| current[*key] != profile[*key])
        {
            return Err(io::Error::other("Claude source profile or account changed"));
        }
        let id = self.claude_conversation()?;
        let authority = self.claude_project_authority(&home, &cwd)?;
        let previous = &meta["claude_resume"];
        let (transcript, identity) = if let Some(recorded) = previous["source_cwd"].as_str() {
            providers::claude::relocation::transcript(&home, Path::new(recorded), &id)?
        } else {
            providers::claude::transcript(&home, &cwd, &id)?
        };
        if previous.is_object() && previous["transcript"] != identity {
            return Err(io::Error::other(
                "original Claude handoff transcript changed",
            ));
        }
        let dir = cwd.metadata()?;
        let mut proof = json!({"conversation":id,"transcript":transcript,"identity":identity,
            "cwd":cwd,"cwd_identity":[dir.dev(),dir.ino()],"home":home,"project_authority":authority});
        if let Some(origin) = previous.get("source_cwd") {
            proof["source_cwd"] = origin.clone();
        }
        Ok((cwd, transcript, id, proof))
    }

    fn codex_hands_off(
        &self,
        cwd: PathBuf,
        meta: &Value,
    ) -> io::Result<(PathBuf, PathBuf, String, Value)> {
        use providers::codex_continuity;
        if meta["token"] != self.token
            || meta["provider"]["mode"] != "managed"
            || !self
                .dir
                .join("codex-thread-changed.json")
                .symlink_metadata()
                .is_err_and(|err| err.kind() == io::ErrorKind::NotFound)
        {
            return Err(io::Error::other("unproved Codex thread custody"));
        }
        let thread = self.semantic_thread()?;
        let record = runtime::read_json(&self.dir.join("codex-profile.json"))?;
        if record["token"] != self.token || !record["profile"].is_object() {
            return Err(io::Error::other("original Codex profile is unproven"));
        }
        let old = &record["profile"];
        let mut env = runtime::launch_env(&Value::Null);
        let home = std::env::var_os("HOME")
            .map(PathBuf::from)
            .ok_or_else(|| io::Error::other("daemon home is missing"))?;
        providers::codex::refuse_unqualified_origin(&Value::Null)?;
        if let Some(native) = providers::codex::explicit_home(&Value::Null, &home)? {
            env.push(("CODEX_HOME".into(), native.to_string_lossy().into_owned()));
        }
        let current = codex_continuity::profile(&env, &cwd, &old["options"])?;
        if old["account"] != current["account"] || !codex_continuity::same_origin(old, &current) {
            return Err(io::Error::other("Codex native origin or authority changed"));
        }
        let rollout = codex_continuity::retained_rollout(
            Path::new(field(old, "native_home")),
            &cwd,
            &thread,
            &meta["codex_resume"],
        )?;
        let transcript = PathBuf::from(field(&rollout, "path"));
        let dir = cwd.metadata()?;
        let proof = json!({"kind":"codex","instance":self.incarnation(),"conversation":thread,"transcript":transcript,"identity":rollout["identity"],
            "cwd":cwd,"cwd_identity":[dir.dev(),dir.ino()],"profile":current});
        Ok((cwd, transcript, thread, proof))
    }
}
