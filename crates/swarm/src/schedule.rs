pub(crate) mod agy;
mod calendar;
pub(crate) mod catalog;
mod cli;
mod engine;
mod rpc;
mod store;
mod timer;
pub(crate) mod worker;
mod workspace;

use jiff::Timestamp;
use serde::{Deserialize, Serialize};
use std::{
    fs,
    io::{self, Read},
    path::{Path, PathBuf},
};

pub(crate) type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;

const ALPHABET: &[u8; 32] = b"0123456789ABCDEFGHJKMNPQRSTVWXYZ";

fn id(now: Timestamp) -> Result<String> {
    let mut bytes = [0; 16];
    bytes[..6].copy_from_slice(&now.as_millisecond().to_be_bytes()[2..]);
    fs::File::open("/dev/urandom")?.read_exact(&mut bytes[6..])?;
    let n = u128::from_be_bytes(bytes);
    Ok((0..26)
        .rev()
        .map(|i| char::from(ALPHABET[((n >> (i * 5)) & 31) as usize]))
        .collect())
}

fn valid_id(id: &str) -> bool {
    id.len() == 26 && id.as_bytes()[0] <= b'7' && id.bytes().all(|b| ALPHABET.contains(&b))
}

#[derive(Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
struct Calendar {
    kind: String,
    time_zone: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    time: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    date: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    weekday: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    cron: String,
}

#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
struct Spec {
    name: String,
    prompt: String,
    directory: String,
    authority: String,
    workspace: String,
    agents: Vec<String>,
    calendar: Calendar,
    grace: i64,
    allow_overlap: bool,
}

#[derive(Clone, Serialize, Deserialize)]
pub(crate) struct Job {
    pub(crate) id: String,
    revision_id: String,
    spec: Spec,
    pub(crate) state: String,
    created_at: Timestamp,
    updated_at: Timestamp,
}

impl Job {
    pub(crate) fn name(&self) -> &str {
        &self.spec.name
    }
    pub(crate) fn directory(&self) -> &str {
        &self.spec.directory
    }
    pub(crate) fn agents(&self) -> &[String] {
        &self.spec.agents
    }
}

#[derive(Serialize, Deserialize)]
struct Template {
    id: String,
    name: String,
    prompt: String,
    created_at: Timestamp,
    updated_at: Timestamp,
}

fn validate_text(name: &str, prompt: &str) -> Result<()> {
    if name.trim().is_empty() || prompt.trim().is_empty() {
        return Err("name and prompt are required".into());
    }
    if name.contains('\0') || prompt.contains('\0') {
        return Err("name and prompt must contain valid UTF-8 text".into());
    }
    if name.chars().any(char::is_control) {
        return Err("name must be one line without control characters".into());
    }
    if prompt
        .chars()
        .any(|c| c.is_control() && !matches!(c, '\n' | '\t' | '\r'))
    {
        return Err("prompt contains an unsupported control character".into());
    }
    Ok(())
}

impl Spec {
    fn prepare(&mut self, host: &Path) -> Result<()> {
        validate_text(&self.name, &self.prompt)?;
        if !matches!(
            self.authority.as_str(),
            "observe" | "workspace-write" | "agent-default"
        ) {
            return Err("select an authority".into());
        }
        if !matches!(self.workspace.as_str(), "worktree" | "directory") {
            return Err("select a workspace mode".into());
        }
        if self.agents.is_empty() {
            return Err("select at least one agent".into());
        }
        for (i, agent) in self.agents.iter().enumerate() {
            match agent.as_str() {
                "codex" => {}
                "claude" | "agy" if self.authority == "agent-default" => {}
                "claude" | "agy" => {
                    return Err(format!("{agent} supports agent-default authority only").into());
                }
                "hermes" | "opencode" => {
                    return Err(format!("{agent} has no scheduled execution route").into());
                }
                _ => return Err(format!("unknown agent {agent:?}").into()),
            }
            if self.agents[..i].contains(agent) {
                return Err(format!("agent {agent:?} is listed twice").into());
            }
        }
        if self.grace < 0 {
            return Err("catch-up grace must be nonnegative".into());
        }
        calendar::Rule::compile(&self.calendar, host)?;
        if self.directory.trim().is_empty() {
            return Err("select a directory".into());
        }
        let path = match self.directory.as_str() {
            "~" => home()?,
            s if s.starts_with("~/") => home()?.join(s[2..].trim_start_matches('/')),
            s => PathBuf::from(s),
        };
        let directory = fs::canonicalize(path).map_err(|e| format!("resolve directory: {e}"))?;
        if !directory.is_dir() {
            return Err(format!("{directory:?} is not a directory").into());
        }
        self.directory = directory
            .into_os_string()
            .into_string()
            .map_err(|_| "directory path must contain valid UTF-8 text")?;
        Ok(())
    }
}

fn home() -> Result<PathBuf> {
    crate::ipc::env_path("HOME").ok_or_else(|| "HOME is required".into())
}

fn file(path: &str) -> io::Result<String> {
    if path == "-" {
        io::read_to_string(io::stdin().lock())
    } else {
        fs::read_to_string(path)
    }
}

pub(crate) use cli::run;
pub(crate) use engine::{Backend, Engine, Launch, LaunchOutcome, WorkerState};
pub(crate) use store::Completion;

#[cfg(test)]
struct Temp(PathBuf);

#[cfg(test)]
impl Temp {
    fn new() -> Self {
        let parent = fs::canonicalize(std::env::temp_dir()).unwrap();
        let path = parent.join(format!("swarm-schedule-{}", id(Timestamp::now()).unwrap()));
        crate::supervisor::runtime::private_dir(&path).unwrap();
        Self(path)
    }
}

#[cfg(test)]
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}
