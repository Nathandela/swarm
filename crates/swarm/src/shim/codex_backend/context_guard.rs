use super::{Channel, Observation, send};
use crate::{
    ipc::field,
    providers::{
        codex_context_guard::{self as model, Durable, Evidence, Machine, Phase, Signal, Support},
        codex_receipt,
    },
    supervisor::{self, context_guard as settings, runtime},
};
use serde_json::{Value, json};
use std::{
    io,
    os::fd::AsRawFd,
    path::PathBuf,
    sync::{Arc, atomic::Ordering},
    time::{Duration, Instant},
};

const CONTINUE: &str = "This session's context was automatically compacted by swarm to keep the task focused. Continue the task exactly where you left off. If the task was already complete and you were waiting for review, say so briefly and do not start new work.";

struct Compaction {
    rpc: u64,
    ack: bool,
    item: Option<(String, String)>,
    completed: Option<Instant>,
    attended_generation: u64,
    settings_revision: u64,
    expires: Instant,
}

impl Compaction {
    fn displaced_by(&self, thread: &str, frame: &Value) -> bool {
        let params = &frame["params"];
        let turn = field(&params["turn"], "id");
        let parent = field(&params["turn"], "rootTurnId");
        self.completed.is_some()
            && frame["method"] == "turn/started"
            && params["threadId"] == thread
            && (parent.is_empty() || parent == turn)
            && self
                .item
                .as_ref()
                .is_none_or(|(compaction_turn, _)| compaction_turn != turn)
    }
}

pub(super) struct Guard {
    state: Arc<super::super::State>,
    root: PathBuf,
    thread: String,
    machine: Machine,
    backend: (u32, u64),
    epoch: Instant,
    policy_seen_ms: u64,
    sequence: u64,
    inflight: Option<Compaction>,
    deadline: Option<Instant>,
    continuation: Option<(u64, Instant)>,
}

impl Guard {
    pub(super) fn bind(
        state: Arc<super::super::State>,
        thread: &str,
        version: &str,
        channel: &Channel,
    ) -> Option<Self> {
        let support = model::support(version);
        if support == Support::Unsupported || !codex_receipt::canonical_thread(thread) {
            return None;
        }
        let root = state.dir.parent()?.parent()?.to_owned();
        let backend = (channel.pid, channel.birth);
        let (prior, bad) = match runtime::read_json(&state.dir.join("context-guard.json")) {
            Ok(record)
                if record["schema"] == 1
                    && record["token"] == state.token
                    && record["thread"] == thread =>
            {
                match serde_json::from_value::<Durable>(record["durable"].clone()) {
                    Ok(durable) if durable.trigger == 0 || (40..=95).contains(&durable.trigger) => {
                        (Some(durable), false)
                    }
                    _ => (None, true),
                }
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => (None, false),
            _ => (None, true),
        };
        let policy = settings::read_runtime(&root);
        let corrupt = bad || policy.is_err();
        let machine = Machine::new(
            policy.unwrap_or(model::Settings {
                enabled: false,
                threshold: 80,
                revision: 0,
            }),
            support,
            prior,
            corrupt,
        );
        let mut guard = Self {
            state,
            root,
            thread: thread.to_owned(),
            machine,
            backend,
            epoch: Instant::now(),
            policy_seen_ms: crate::ipc::spawn::now_ms(),
            sequence: 0,
            inflight: None,
            deadline: None,
            continuation: None,
        };
        guard.persist_or_block();
        Some(guard)
    }

    fn time(&self) -> u64 {
        self.epoch
            .elapsed()
            .as_millis()
            .min(u128::from(u64::MAX - 1)) as u64
            + 1
    }

    fn persist_or_block(&mut self) -> bool {
        if runtime::write_json(
            &self.state.dir.join("context-guard.json"),
            &json!({
                "schema":1,"token":self.state.token,"thread":self.thread,"backend":self.backend,
                "durable":self.machine.durable,"settings_revision":self.machine.settings.revision,
            }),
        )
        .is_ok()
        {
            return true;
        }
        self.machine.durable.phase = Phase::BlockedCorrupt;
        self.inflight = None;
        self.state
            .context_guard_busy
            .store(false, Ordering::Release);
        false
    }

    fn reduce(&mut self, signal: Signal) -> bool {
        let old = self.machine.durable;
        if !self.machine.reduce(signal, self.time()) {
            return false;
        }
        if self.machine.durable != old || matches!(signal, Signal::Settings(_)) {
            self.persist_or_block()
        } else {
            true
        }
    }

    fn refresh_policy(&mut self) {
        match settings::read_runtime(&self.root) {
            Ok(policy) => {
                if policy.revision > self.machine.settings.revision {
                    self.policy_seen_ms = crate::ipc::spawn::now_ms();
                    self.reduce(Signal::Settings(policy));
                } else if policy != self.machine.settings {
                    self.block_policy();
                }
            }
            Err(_) => self.block_policy(),
        }
    }

    fn block_policy(&mut self) {
        if self.machine.durable.phase != Phase::BlockedCorrupt {
            self.machine.durable.phase = Phase::BlockedCorrupt;
            self.inflight = None;
            self.persist_or_block();
        }
        self.sync_busy();
    }

    pub(super) fn view(&self) -> Value {
        let usage = self.machine.usage.map_or(0, |(used, limit)| {
            (u128::from(used) * 100 / u128::from(limit)).min(999)
        });
        let automatic = self.machine.support == Support::Automatic;
        let mut view = json!({"usage_percent":usage,"phase":self.machine.durable.phase,
            "support":if automatic {"automatic"} else {"observe_only"}});
        if !automatic {
            view["error_code"] = json!("action_unverified");
        }
        if self.machine.last_compacted {
            view["last_result"] = json!("compacted");
        }
        view
    }

    pub(super) fn owner_input(&mut self) {
        // A later idle observation cannot revive a continuation displaced by new work.
        self.inflight.take_if(|cycle| cycle.completed.is_some());
    }

    pub(super) fn frame(&mut self, frame: &Value) {
        self.inflight
            .take_if(|cycle| cycle.displaced_by(&self.thread, frame));
        let evidence = model::evidence(frame, &self.thread);
        let lifecycle = frame["params"]["threadId"] == self.thread
            && (frame["params"]["item"]["type"] == "contextCompaction"
                || frame["method"] == "thread/compacted");
        let response = self
            .continuation
            .is_some_and(|(id, _)| frame["id"] == id && frame["method"].is_null())
            || self
                .inflight
                .as_ref()
                .is_some_and(|cycle| frame["id"] == cycle.rpc && frame["method"].is_null());
        if evidence.is_none() && !lifecycle && !response {
            return;
        }
        self.refresh_policy();
        if lifecycle && evidence.is_none() {
            self.inflight = None;
            self.reduce(Signal::EventLoss);
            self.sync_busy();
            return;
        }
        if self
            .continuation
            .is_some_and(|(id, _)| frame["id"] == id && frame["method"].is_null())
        {
            self.continuation = None;
            self.state
                .context_guard_busy
                .store(false, Ordering::Release);
        }
        if let Some(cycle) = self.inflight.as_mut()
            && frame["id"] == cycle.rpc
            && frame["method"].is_null()
        {
            if frame["result"].is_object() && frame["error"].is_null() {
                cycle.ack = true;
            } else {
                self.inflight = None;
                self.reduce(Signal::Unknown);
            }
        }
        let Some(evidence) = evidence else {
            return;
        };
        if matches!(evidence, Evidence::Usage { .. }) && self.machine.support == Support::Automatic
        {
            let now = crate::ipc::spawn::now_ms();
            if frame["emittedAtMs"]
                .as_u64()
                .is_none_or(|time| time <= self.policy_seen_ms || time > now || now - time > 30_000)
            {
                return;
            }
        }
        if matches!(evidence, Evidence::CompactCompleted { legacy: true })
            && self.inflight.is_some()
        {
            return;
        }
        self.sequence = self.sequence.saturating_add(1);
        if !self.reduce(Signal::Provider {
            sequence: self.sequence,
            evidence,
        }) {
            return;
        }
        let body = &frame["params"];
        let item = || {
            (
                field(body, "turnId").to_owned(),
                field(&body["item"], "id").to_owned(),
            )
        };
        match evidence {
            Evidence::CompactStarted => {
                self.deadline = Some(Instant::now() + Duration::from_secs(300));
                if let Some(cycle) = &mut self.inflight {
                    let current = item();
                    if cycle.item.as_ref().is_some_and(|old| old != &current) {
                        self.inflight = None;
                    } else {
                        cycle.item = Some(current);
                    }
                }
            }
            Evidence::CompactCompleted { legacy: false } => {
                self.deadline = None;
                if let Some(cycle) = &mut self.inflight {
                    if cycle.item.as_ref() == Some(&item()) {
                        cycle.completed = Some(Instant::now());
                    } else {
                        self.inflight = None;
                    }
                }
            }
            Evidence::CompactCompleted { legacy: true } => {
                self.deadline = None;
                self.inflight = None;
            }
            Evidence::Usage { .. } => {}
        }
        self.sync_busy();
    }

    fn sync_busy(&self) {
        self.state.context_guard_busy.store(
            matches!(
                self.machine.durable.phase,
                Phase::Executing | Phase::AwaitingConfirmation | Phase::ProviderCompacting
            ) || self.continuation.is_some(),
            Ordering::Release,
        );
    }

    fn empty_draft(state: &super::super::State) -> bool {
        let Ok(screen) = state.screen.try_lock() else {
            return false;
        };
        let screen = screen.parser.screen();
        let (row, col) = screen.cursor_position();
        let (_, cols) = screen.size();
        if screen.hide_cursor() || col > 3 {
            return false;
        }
        let Some(line) = screen.rows(0, cols.get()).nth(usize::from(row)) else {
            return false;
        };
        let label = "Ask Codex to do anything";
        line.trim_end() == "›"
            || (line.trim_end() == format!("› {label}")
                && label.bytes().enumerate().all(|(index, byte)| {
                    byte == b' '
                        || screen
                            .cell(row, 2 + index as u16)
                            .is_some_and(|cell| cell.dim())
                }))
    }

    fn socket_quiet(channel: &Channel) -> bool {
        let mut fd = libc::pollfd {
            fd: channel.connection.get_ref().as_raw_fd(),
            events: libc::POLLIN,
            revents: 0,
        };
        // SAFETY: fd is the observer's live descriptor and poll never blocks here.
        unsafe { libc::poll(&mut fd, 1, 0) == 0 }
    }

    fn owes_input(&self) -> bool {
        let local = self
            .state
            .dir
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or("");
        let session = format!("{}/{local}", supervisor::endpoint(&self.root));
        codex_receipt::require_resolved(
            &self.state.dir,
            &session,
            &self.state.token,
            &self.thread,
            "codex",
        )
        .is_err()
    }

    pub(super) fn tick(
        &mut self,
        channel: &mut Channel,
        observation: &Observation,
        serial: &mut u64,
    ) -> io::Result<()> {
        self.refresh_policy();
        if self.deadline.is_some_and(|until| Instant::now() >= until) {
            self.reduce(Signal::Unknown);
            self.inflight = None;
            self.deadline = None;
        }
        if self
            .continuation
            .is_some_and(|(_, until)| Instant::now() >= until)
        {
            self.continuation = None;
        }
        self.sync_busy();
        let state = self.state.clone();
        let Ok(control) = state.control.try_lock() else {
            return Ok(());
        };
        let generation = state.context_guard_activity.load(Ordering::Acquire);
        if *control != 0
            || state.terminal_input.load(Ordering::Acquire)
            || self.inflight.as_ref().is_some_and(|cycle| {
                cycle.attended_generation != generation
                    || cycle.settings_revision != self.machine.settings.revision
            })
        {
            self.inflight = None;
            return Ok(());
        }
        if !observation.joined
            || !observation.idle
            || observation.ambiguous
            || observation.turn.is_some()
            || observation.requests_unverified
            || !observation.requests.is_empty()
            || self.owes_input()
            || !Self::empty_draft(&state)
            || runtime::birth(channel.pid).ok() != Some(channel.birth)
            || !Self::socket_quiet(channel)
            || self.thread != observation.thread
        {
            self.reduce(Signal::Busy);
            return Ok(());
        }
        if self.machine.support != Support::Automatic || !self.machine.settings.enabled {
            return Ok(());
        }
        if self.inflight.is_none() && self.machine.durable.phase != Phase::PendingIdle {
            return Ok(());
        }
        let Ok(_settings) = settings::acquire_write(&self.root) else {
            return Ok(());
        };
        let revision = self.machine.settings.revision;
        self.refresh_policy();
        if revision != self.machine.settings.revision || !self.machine.settings.enabled {
            return Ok(());
        }

        if let Some(cycle) = &self.inflight {
            if cycle.ack
                && cycle
                    .completed
                    .is_some_and(|at| at.elapsed() <= Duration::from_secs(120))
                && self.machine.durable.phase == Phase::Latched
            {
                if !Self::socket_quiet(channel) || !Self::empty_draft(&state) {
                    return Ok(());
                }
                self.inflight = None;
                // The latch is already durable. Continuations are never reconstructed.
                *serial += 1;
                self.continuation = Some((*serial, Instant::now() + Duration::from_secs(5)));
                self.sync_busy();
                send(
                    &mut channel.connection,
                    json!({"id":*serial,"method":"turn/start","params":{
                        "threadId":self.thread,"input":[{"type":"text","text":CONTINUE}]
                    }}),
                )?;
            } else if Instant::now() >= cycle.expires {
                self.inflight = None;
            }
            return Ok(());
        }
        if !self.reduce(Signal::Idle) || !self.reduce(Signal::Begin) {
            return Ok(());
        }
        if runtime::birth(channel.pid).ok() != Some(channel.birth)
            || !Self::empty_draft(&state)
            || !Self::socket_quiet(channel)
        {
            self.reduce(Signal::Refused);
            return Ok(());
        }
        *serial += 1;
        self.sync_busy();
        send(
            &mut channel.connection,
            json!({"id":*serial,"method":"thread/compact/start","params":{"threadId":self.thread}}),
        )?;
        self.reduce(Signal::Written);
        let expiry = Instant::now() + Duration::from_secs(300);
        self.deadline = Some(expiry);
        self.inflight = Some(Compaction {
            rpc: *serial,
            ack: false,
            item: None,
            completed: None,
            attended_generation: generation,
            settings_revision: revision,
            expires: expiry,
        });
        Ok(())
    }

    pub(super) fn disconnected(&mut self) {
        if matches!(
            self.machine.durable.phase,
            Phase::Executing | Phase::AwaitingConfirmation
        ) {
            self.reduce(Signal::Unknown);
        } else if self.machine.durable.phase != Phase::BlockedCorrupt {
            self.reduce(Signal::EventLoss);
        }
        self.state
            .context_guard_busy
            .store(false, Ordering::Release);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unrelated_native_turn_permanently_displaces_a_completed_compactions_continuation() {
        let new_cycle = || Compaction {
            rpc: 3,
            ack: true,
            item: Some(("guard-turn".into(), "item".into())),
            completed: Some(Instant::now()),
            attended_generation: 0,
            settings_revision: 4,
            expires: Instant::now() + Duration::from_secs(120),
        };
        let started = |thread: &str, id: &str, root: &str| {
            json!({
                "method":"turn/started","params":{"threadId":thread,"turn":{"id":id,"rootTurnId":root}}
            })
        };
        let mut pending = Some(new_cycle());
        for frame in [
            started("ours", "guard-turn", ""),
            started("other", "owner-turn", ""),
            started("ours", "child", "guard-turn"),
        ] {
            pending.take_if(|cycle| cycle.displaced_by("ours", &frame));
            assert!(pending.is_some());
        }
        for frame in [
            started("ours", "owner-turn", ""),
            json!({"method":"thread/status/changed","params":{"threadId":"ours","status":{"type":"idle"}}}),
        ] {
            pending.take_if(|cycle| cycle.displaced_by("ours", &frame));
            assert!(pending.is_none());
        }
    }
}
