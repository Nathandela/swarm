use super::{State, runtime, session::Session};
use crate::ipc::field;
use crate::schedule::{Backend, Engine, Launch, LaunchOutcome, Result, WorkerState, worker};
use jiff::Timestamp;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    io,
    os::unix::net::UnixListener,
    path::{Path, PathBuf},
    sync::Arc,
    thread,
};

pub(super) const CAPS: &[&str] = &[
    "schedule",
    "schedule-run-id",
    "schedule-workspace-check",
    "schedule-route-check",
    "schedule-host-time",
    "schedule-native",
];

pub(super) fn socket(owner: &Path) -> Result<(PathBuf, UnixListener)> {
    let digest = format!("{:x}", Sha256::digest(owner.as_os_str().as_encoded_bytes()));
    let path = owner.with_file_name(format!("sc-{}.sock", &digest[..6]));
    runtime::reclaim_socket(&path)?;
    let socket = runtime::bind(&path)?;
    Ok((path, socket))
}

pub(super) fn receipts(listener: UnixListener, state: Arc<State>) {
    thread::spawn(move || {
        for stream in listener.incoming().flatten() {
            let state = state.clone();
            thread::spawn(move || {
                let _ = worker::handle_receipt(
                    stream,
                    &state.endpoint,
                    |run, attempt, session, proof, report| {
                        let _transaction = state.transactions.read().unwrap();
                        engine(&state)?.complete(
                            run,
                            attempt,
                            session,
                            proof,
                            report,
                            Timestamp::now(),
                        )
                    },
                );
            });
        }
    });
}

fn engine(state: &State) -> Result<Engine<Scheduled<'_>>> {
    Engine::open(
        state.root.parent().ok_or("missing scheduler state path")?,
        Path::new("/etc/localtime"),
        Scheduled { state },
    )
}

pub(super) fn handle(state: &State, request: &Value, caps: &[&str]) -> Result<Value> {
    let op = field(request, "op");
    match op {
        "schedule_native_status" if caps.contains(&"schedule-native") => {
            let path = Path::new(field(request, "state_dir"));
            if Some(path) != state.root.parent() {
                return Err("timer and supervisor use different schedule catalogs".into());
            }
            Ok(json!({"op":op}))
        }
        "schedule_tick" if caps.contains(&"schedule") => {
            engine(state)?.tick(Timestamp::now())?;
            Ok(json!({"op":op}))
        }
        "schedule_run" if caps.contains(&"schedule") && caps.contains(&"schedule-run-id") => {
            let invocation = field(request, "operation_id");
            let run =
                engine(state)?.run(field(request, "schedule_id"), invocation, Timestamp::now())?;
            Ok(json!({"op":op,"operation_id":invocation,"schedule_id":run}))
        }
        "schedule_workspace_check" if caps.contains(&"schedule-workspace-check") => {
            let path = Path::new(field(request, "schedule_workspace"));
            if !path.is_absolute() {
                return Err("workspace check requires an absolute path".into());
            }
            let conflict = conflict(state, path, true).unwrap_or_default();
            Ok(json!({"op":op,"schedule_workspace":conflict}))
        }
        "schedule_route_check" if caps.contains(&"schedule-route-check") => {
            let agent = field(request, "schedule_agent");
            let unavailable = match agent {
                "codex" => Session::scheduled_codex_available(
                    state.root.parent().ok_or("missing scheduler state path")?,
                )
                .err()
                .map(|e| e.to_string())
                .unwrap_or_default(),
                "claude" | "agy" => {
                    format!("{agent} has no qualified native scheduling route in this build")
                }
                _ => return Err("unknown scheduled agent".into()),
            };
            Ok(json!({"op":op,"schedule_agent":agent,"schedule_unavailable":unavailable}))
        }
        _ => Err("scheduled operation requires a capable local owner socket".into()),
    }
}

pub(super) fn conflict(state: &State, path: &Path, scheduled: bool) -> Option<String> {
    let sessions: Vec<_> = state.sessions.read().unwrap().values().cloned().collect();
    sessions.iter().find_map(|session| {
        let occupied = (scheduled || session.schedule.is_some()) && session.workspace_unconfirmed();
        let directory = session.provider_directory();
        (occupied && (path.starts_with(directory) || directory.starts_with(path)))
            .then(|| directory.display().to_string())
    })
}

struct Scheduled<'a> {
    state: &'a State,
}

impl Scheduled<'_> {
    fn exact(&self, session: &str, attempt: &str) -> Option<Arc<Session>> {
        let session = self.state.session(&json!({"session_id":session})).ok()?;
        session
            .schedule
            .as_ref()
            .filter(|binding| binding.attempt_id == attempt)?;
        Some(session)
    }

    fn by_operation(&self, operation: &str) -> Option<Arc<Session>> {
        self.state
            .sessions
            .read()
            .unwrap()
            .values()
            .find(|s| {
                s.schedule
                    .as_ref()
                    .is_some_and(|binding| binding.operation_id == operation)
            })
            .cloned()
    }

    fn public_id(&self, session: &Session) -> String {
        format!("{}/{}", self.state.endpoint, session.id)
    }
}

impl Backend for Scheduled<'_> {
    fn launch(&self, launch: Launch<'_>) -> LaunchOutcome {
        let Some(socket) = self.state.schedule_socket.as_ref() else {
            return LaunchOutcome::Refused("scheduled worker receipt socket is unavailable".into());
        };
        if launch.agent != "codex" {
            return LaunchOutcome::Refused(format!(
                "{} has no qualified native scheduling route in this build",
                launch.agent
            ));
        }
        let (Some(root), workspace) = (self.state.root.parent(), Path::new(launch.workspace))
        else {
            return LaunchOutcome::Refused("missing scheduler state path".into());
        };
        if workspace.starts_with(root) || root.starts_with(workspace) {
            return LaunchOutcome::Refused(
                "choose a directory outside the Swarm state and its ancestors".into(),
            );
        }
        let prepared = match Session::prepare_scheduled(&launch, socket) {
            Ok(plan) => plan,
            Err(e) => return LaunchOutcome::Refused(e.to_string()),
        };
        let session = match self.state.reserve_prepared(prepared, true, None, None) {
            Ok(session) => session,
            Err(e) if e.kind() == io::ErrorKind::WouldBlock => {
                return LaunchOutcome::Busy(e.to_string());
            }
            Err(e) => return LaunchOutcome::Refused(e.to_string()),
        };
        // The attempt and operation are persisted in the reservation before the shim receives its launch pipe.
        match self.state.start(&session) {
            Ok(_) => LaunchOutcome::Started(self.public_id(&session)),
            Err(e) if matches!(session.scheduled_state(), WorkerState::StartupRefused) => {
                LaunchOutcome::Refused(e.to_string())
            }
            Err(e) => LaunchOutcome::Unknown(e.to_string()),
        }
    }

    fn reconcile(&self, operation: &str) -> Option<String> {
        let session = self.by_operation(operation)?;
        // A durable reservation fences a second dispatch. A live handshake or verified exit can resolve it.
        if matches!(session.scheduled_state(), WorkerState::Unknown) {
            return None;
        }
        Some(self.public_id(&session))
    }

    fn session(&self, session: &str, attempt: &str) -> Option<WorkerState> {
        Some(self.exact(session, attempt)?.scheduled_state())
    }

    fn stop(&self, session: &str, attempt: &str) -> Result<()> {
        self.exact(session, attempt)
            .ok_or("scheduled worker binding changed")?
            .kill()?;
        Ok(())
    }

    fn workspace_available(&self, path: &str) -> Result<bool> {
        Ok(conflict(self.state, Path::new(path), true).is_none())
    }

    fn send_prompt(
        &self,
        session: &str,
        attempt: &str,
        text: &str,
        begin: &mut dyn FnMut() -> Result<()>,
    ) -> Result<()> {
        self.exact(session, attempt)
            .ok_or("scheduled worker binding changed")?
            .send_scheduled(self.state, session, attempt, text, begin)
    }

    fn closing_matches(&self, session: &str, attempt: &str, summary: &str) -> bool {
        self.exact(session, attempt)
            .is_some_and(|worker| worker.scheduled_closing(session, attempt, summary))
    }

    fn project(&self, session: &str, attempt: &str) -> Result<()> {
        let session = self
            .exact(session, attempt)
            .ok_or("scheduled worker binding changed")?;
        if session.project_schedule()? {
            self.state.publish(&session);
        }
        Ok(())
    }

    fn handoff(&self, session: &str, attempt: &str) -> String {
        self.exact(session, attempt)
            .and_then(|worker| worker.scheduled_handoff(session, attempt))
            .unwrap_or_default()
    }
}
