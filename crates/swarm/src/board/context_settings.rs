//! Settings on the selected owner. The worker only applies an explicit draft
//! against the revision loaded for the currently open form.
use super::{Editor, Key};
use crate::ipc::{self, Client};
use serde_json::{Value, json};
use std::io;
use std::path::{Path, PathBuf};
use std::sync::mpsc::{self, Receiver, TryRecvError};
use std::thread;

pub(super) const CAP: &str = "context-guard-settings";
pub(super) const DESCRIPTION: &str =
    "Context guard: Codex only · compacts when quiet and unattended, then continues the task";

#[derive(Clone, PartialEq, Eq)]
pub(super) struct Owner {
    path: PathBuf,
    endpoint: String,
}

impl Owner {
    pub(super) fn from_connection(path: &Path, client: &Client) -> Option<Self> {
        client.supports(CAP).then(|| Self {
            path: path.to_owned(),
            endpoint: client.endpoint().to_owned(),
        })
    }

    fn request(&self, op: &str, body: Value) -> io::Result<Policy> {
        let mut client = Client::dial(&self.path, &[CAP])?;
        if client.endpoint() != self.endpoint || !client.supports(CAP) {
            return Err(io::Error::other("daemon connection changed"));
        }
        let reply = client.request_body(body, op)?;
        Policy::decode(&reply["context_guard_settings"])
    }
}

#[derive(Clone, Copy)]
struct Policy {
    revision: u64,
    enabled: bool,
    threshold: u8,
}

impl Policy {
    fn decode(value: &Value) -> io::Result<Self> {
        let auto = &value["auto_compact"];
        let decoded = (
            value["schema_version"].as_u64(),
            value["revision"].as_u64(),
            auto["enabled"].as_bool(),
            auto["threshold_percent"].as_u64(),
        );
        match decoded {
            (Some(1), Some(revision), Some(enabled), Some(threshold @ 40..=95)) => Ok(Self {
                revision,
                enabled,
                threshold: threshold as u8,
            }),
            _ => Err(io::Error::other("invalid context guard settings reply")),
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Request {
    Read,
    Save,
}

struct Pending {
    kind: Request,
    response: Receiver<io::Result<Policy>>,
}

pub(super) enum Update {
    Pending,
    Changed,
    Saved,
}

pub(super) struct Context {
    owner: Owner,
    policy: Option<Policy>,
    pending: Option<Pending>,
    reconnect: Option<(bool, String)>,
    invalidated: bool,
    pub(super) enabled: bool,
    pub(super) threshold: Editor,
    pub(super) error: String,
}

impl Context {
    pub(super) fn open(owner: Owner) -> Self {
        let mut settings = Self {
            owner,
            policy: None,
            pending: None,
            reconnect: None,
            invalidated: false,
            enabled: false,
            threshold: Editor::new(""),
            error: String::new(),
        };
        settings.load();
        settings
    }

    fn load(&mut self) {
        self.exchange(
            Request::Read,
            "context_guard_get",
            json!({"op":"context_guard_get"}),
        );
    }

    fn exchange(&mut self, kind: Request, op: &'static str, body: Value) {
        let (send, response) = mpsc::sync_channel(1);
        let owner = self.owner.clone();
        match thread::Builder::new()
            .name("board-context-settings".into())
            .spawn(move || {
                let _ = send.send(owner.request(op, body));
            }) {
            Ok(_) => self.pending = Some(Pending { kind, response }),
            Err(_) => self.error = "context guard settings unavailable".into(),
        }
    }

    pub(super) fn poll(&mut self) -> Update {
        let Some(pending) = &self.pending else {
            return Update::Pending;
        };
        let result = match pending.response.try_recv() {
            Ok(result) => result,
            Err(TryRecvError::Empty) => return Update::Pending,
            Err(TryRecvError::Disconnected) => Err(io::Error::other("settings worker closed")),
        };
        let kind = pending.kind;
        self.pending = None;
        match result {
            Ok(policy) => {
                self.policy = Some(policy);
                let (enabled, threshold) = self
                    .reconnect
                    .take()
                    .unwrap_or_else(|| (policy.enabled, policy.threshold.to_string()));
                self.enabled = enabled;
                self.threshold = Editor::new(&threshold);
                self.error.clear();
                if kind == Request::Save {
                    return Update::Saved;
                }
            }
            Err(error) => {
                self.error =
                    if ipc::refused_with(&error, "context guard settings revision is stale") {
                        "context guard settings changed elsewhere; reopen options to reload"
                    } else {
                        "context guard settings unavailable"
                    }
                    .into();
            }
        }
        Update::Changed
    }

    pub(super) fn loaded(&self) -> bool {
        self.policy.is_some()
    }

    pub(super) fn loading(&self) -> bool {
        self.pending
            .as_ref()
            .is_some_and(|p| p.kind == Request::Read)
    }

    pub(super) fn saving(&self) -> bool {
        self.pending
            .as_ref()
            .is_some_and(|p| p.kind == Request::Save)
    }

    pub(super) fn pending(&self) -> bool {
        self.pending.is_some()
    }

    pub(super) fn dirty(&self) -> bool {
        self.policy.is_some_and(|p| {
            self.enabled != p.enabled || self.threshold.text != p.threshold.to_string()
        })
    }

    pub(super) fn update_threshold(&mut self, key: Key) {
        let previous = self.threshold.text.clone();
        self.threshold.apply(key);
        if self.threshold.text != previous && self.error.starts_with("threshold must") {
            self.error.clear();
        }
    }

    pub(super) fn save(&mut self) {
        let Some(policy) = self.policy else { return };
        if self.pending() || self.invalidated {
            return;
        }
        let Ok(threshold @ 40..=95) = self.threshold.text.parse::<u8>() else {
            self.error = "threshold must be an integer from 40–95".into();
            return;
        };
        self.exchange(
            Request::Save,
            "context_guard_set",
            json!({
                "op":"context_guard_set",
                "context_guard_set":{
                    "expected_revision":policy.revision,
                    "auto_compact":{"enabled":self.enabled,"threshold_percent":threshold}
                }
            }),
        );
    }

    pub(super) fn disconnect(&mut self) {
        if self.dirty() && !self.saving() {
            self.reconnect = Some((self.enabled, self.threshold.text.clone()));
        }
        self.invalidated = true;
        self.pending = None;
        self.error = "daemon connection changed; reopen options to reload settings".into();
    }

    pub(super) fn reconnect(&mut self, owner: Option<&Owner>) {
        if self.invalidated && owner == Some(&self.owner) {
            self.invalidated = false;
            self.policy = None;
            self.error.clear();
            self.load();
        }
    }
}
