use super::{Session, exit_report, owner_gone};
use crate::{ipc::field, providers::agy};
use serde_json::{Value, json};
use std::{
    io,
    path::{Path, PathBuf},
};

impl Session {
    pub(in crate::supervisor) fn hands_off_agy_source(
        &self,
    ) -> io::Result<(PathBuf, PathBuf, String, Value)> {
        self.verify_agy_custody()?;
        let proof = agy::handoff::source(&self.dir)?;
        self.verify_agy_handoff(&proof)?;
        Ok((
            PathBuf::from(field(&proof, "cwd")),
            PathBuf::from(field(&proof, "transcript")),
            field(&proof, "conversation").into(),
            proof,
        ))
    }

    pub(in crate::supervisor) fn verify_agy_handoff(&self, proof: &Value) -> io::Result<()> {
        self.verify_agy_custody()?;
        if std::env::var_os("HOME").as_deref() != proof["home"].as_str().map(std::ffi::OsStr::new)
            || proof["token"] != self.token
            || proof["instance"] != self.incarnation()
            || proof["cwd"] != json!(self.verify_effective()?)
        {
            return Err(io::Error::other(
                "Antigravity handoff source identity changed",
            ));
        }
        agy::verify_handoff(&self.dir, proof)
    }

    fn verify_agy_custody(&self) -> io::Result<()> {
        if self.agent != "agy" || !self.owner_interactive || self.schedule.is_some() {
            return Err(io::Error::other(
                "Antigravity hands-off requires an ordinary source",
            ));
        }
        if self.is_live() {
            self.dial()?;
        } else if !self.exit()?["error"].is_null() || !owner_gone(&self.dir, &self.token)? {
            return Err(io::Error::other(
                "Antigravity handoff source custody is unproved",
            ));
        }
        Ok(())
    }
}

pub(crate) fn verify_owner(dir: &Path, token: &str) -> io::Result<()> {
    if owner_gone(dir, token)? {
        if !exit_report(dir, token)?["error"].is_null() {
            return Err(io::Error::other(
                "Antigravity source did not reach native execution",
            ));
        }
    } else {
        let source = Session::load(dir.to_owned())?;
        if source.token != token {
            return Err(io::Error::other("Antigravity source instance changed"));
        }
        source.dial()?;
    }
    Ok(())
}
