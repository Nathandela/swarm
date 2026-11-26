//! One owner-personal discussion on the native supervisor's exact manager binding.
use super::config::{Config, invalid};
use super::receipt::{Inbox, Observation, hash, publication_nonce, verify_publication};
use super::rest::{Discord, Post};
use super::{mail_wake, manager::Manager};
use crate::{
    ipc::field,
    socket::DeadlineReader,
    supervisor::{mail_files::Captured, mail_store, runtime},
};
use serde_json::{Value, json};
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::os::unix::net::UnixListener;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use subtle::ConstantTimeEq;

pub(super) trait Audience {
    fn current(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>>;
    fn verify(&self, cfg: &Config) -> io::Result<()>;
    fn admit(&self, inbox: &mut Inbox, source: &Value) -> io::Result<Option<Value>> {
        inbox.admit(source)
    }
    fn post(&self, cfg: &Config, nonce: &str, body: &str, files: &[Captured]) -> Post;
}

impl Audience for Discord {
    fn current(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>> {
        self.owner_message(cfg, id)
    }

    fn verify(&self, cfg: &Config) -> io::Result<()> {
        self.verify_dm(cfg, None).map(|_| ())
    }

    fn post(&self, cfg: &Config, nonce: &str, body: &str, files: &[Captured]) -> Post {
        self.post_publication(cfg, nonce, body, files)
    }
}

pub(super) struct Dispatcher {
    owner: Manager,
    listener: UnixListener,
    launch: Value,
    socket: PathBuf,
    socket_identity: (u64, u64),
    credential: String,
    mail_cursor: std::cell::Cell<u64>,
    idle_since: std::cell::Cell<Option<Instant>>,
}

impl Dispatcher {
    pub(super) fn open(cfg: &Config, place: &Path) -> io::Result<Option<Self>> {
        // This first discussion path uses the native restricted manager harness.
        if cfg.text("project_access") == "vm" {
            return Ok(None);
        }
        let owner = Manager::new(cfg)?;
        if owner.verify_supervisor().is_err() {
            return Ok(None);
        }
        let dir = fs::canonicalize(place)?.join("manager-v1");
        runtime::private_dir(&dir)?;
        let auth = dir.join("bridge.auth");
        let credential = auth_file(&auth)?;
        let socket = dir.join("bridge.sock");
        runtime::reclaim_socket(&socket)?;
        let listener = runtime::bind(&socket)?;
        let meta = fs::symlink_metadata(&socket)?;
        let launch = owner.launch_request(&socket, &auth);
        let this = Self {
            owner,
            listener,
            launch,
            socket,
            credential,
            mail_cursor: std::cell::Cell::new(0),
            idle_since: std::cell::Cell::new(None),
            socket_identity: (meta.dev(), meta.ino()),
        };
        this.listener.set_nonblocking(true)?;
        Ok(Some(this))
    }

    pub(super) fn scope(&self, cfg: &Config) -> Value {
        let mut scope = json!({"launch":self.launch,"installation_id":cfg.text("installation_id"),
            "application_id":cfg.text("application_id"),"owner_id":cfg.text("owner_id"),
            "channel_id":self.owner.channel(),"supervisor_socket":cfg.text("daemon_socket")});
        if !self.owner.guild().is_empty() {
            scope["guild_id"] = json!(self.owner.guild());
        }
        scope
    }

    #[cfg(test)]
    pub(super) fn tick(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        api: &impl Audience,
    ) -> io::Result<()> {
        self.tick_calls(cfg, inbox, api, None)
    }

    pub(super) fn tick_calls(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        api: &impl Audience,
        calls: Option<(&Path, &super::call_runtime::Driver)>,
    ) -> io::Result<()> {
        self.serve_tool(cfg, inbox, api)?;
        let mut state = inbox.manager().clone();
        if state.is_null() {
            if inbox.next_prepared().is_none() {
                return Ok(());
            }
            state = json!({"generation":1, "launch_operation":operation("launch")?, "launch":self.launch});
            inbox.set_manager(state.clone())?;
        }
        if state["launch"] != self.launch || !state["generation"].as_u64().is_some_and(|n| n > 0) {
            return Err(invalid(
                "persisted native manager launch differs from this place",
            ));
        }
        if !state["retirement"].is_null() {
            return self.resume_parked(cfg, inbox, state, calls.is_some());
        }
        if state["session"].is_null() {
            let op = field(&state, "launch_operation");
            let status = self.owner.launch_status(op)?;
            let session = match field(&status, "state") {
                "applied" => status["session_id"].clone(),
                "unknown_operation" => self.owner.launch(op, &self.launch)?["id"].clone(),
                _ => {
                    return Err(io::Error::other(
                        "earlier manager launch remains unconfirmed",
                    ));
                }
            };
            if session.as_str().is_none_or(str::is_empty) {
                return Err(invalid("native manager launch lacks a scoped session"));
            }
            state["session"] = session;
            inbox.set_manager(state.clone())?;
        }
        let session = field(&state, "session");
        let row = match self.owner.session(session) {
            Ok(row) => row,
            Err(err) if state["binding"].is_null() && err.kind() == io::ErrorKind::InvalidInput => {
                return Ok(());
            }
            Err(err) => return Err(err),
        };
        let instance = field(&row["capabilities"], "session_instance");
        let status = match self.owner.runtime(session, instance, &self.launch) {
            Ok(status) => status,
            Err(_) if state["binding"].is_null() => return Ok(()),
            Err(err) => return Err(err),
        };
        if state["binding"].is_null() {
            state["binding"] = status["binding"].clone();
            inbox.set_manager(state.clone())?;
        }
        if state["binding"] != status["binding"]
            || status["turn"]["externally_steered"] == true
            || status["provider_exhausted"] == true
        {
            return Err(invalid("native manager requires owner reconciliation"));
        }
        if !state["input"].is_null() {
            self.idle_since.set(None);
            let had_turn = !state["input"]["turn_id"].is_null();
            let phase = self.reconcile_input(inbox, &mut state)?;
            if let Some((_, driver)) = calls
                && !had_turn
                && !state["input"]["turn_id"].is_null()
                && state["input"]["composer"]["origin"]["voice"].is_object()
            {
                let origin = state["input"]["composer"]["origin"].clone();
                let _ = driver
                    .readout
                    .try_send(super::call_runtime::Readout::Acknowledge(origin));
            }
            if phase == "unknown" && !state["input"]["worker_wake"].is_null() {
                let input = &state["input"];
                let id = field(input, "source_id");
                let revision = &input["worker_wake"]["owner_revision"];
                let wake_key = field(&input["worker_wake"], "key").to_owned();
                let Some(current) = Self::mail_owner_current(cfg, inbox, api, id, revision)? else {
                    return Ok(());
                };
                let expired = !crate::providers::codex_receipt::timely(field(input, "operation"));
                if !current || expired {
                    let _effect = match super::lock_manager_mail_effect(&self.scope(cfg)) {
                        Ok(lock) => lock,
                        Err(error) if error.kind() == io::ErrorKind::WouldBlock => return Ok(()),
                        Err(error) => return Err(error),
                    };
                    let observed = self.owner.input_status(&input["composer"])?;
                    if observed["state"] == "unknown"
                        && observed["external_input"]
                            == input["composer"]["manager_admission"]["expected_external_input"]
                        && field(&observed, "native_turn_id").is_empty()
                    {
                        if !current {
                            inbox.withdraw(id)?;
                        } else if let Some(wakes) = state["worker_wakes"].as_object_mut() {
                            wakes.shift_remove(&wake_key);
                        }
                        state["input"] = Value::Null;
                        inbox.set_manager(state)?;
                    }
                    return Ok(());
                }
                if status["ready_for_input"] == true {
                    // The shim serializes native preparation with cancellation.
                    let _ = self
                        .owner
                        .send(field(input, "operation"), &input["composer"]);
                }
            }
            self.deliver(cfg, inbox, api, &state, millis()?)?;
            if phase == "completed" {
                let nonce = field(&state["input"], "final_nonce");
                if nonce.is_empty() || inbox.publication(nonce)["state"] == "sent" {
                    let input = &state["input"];
                    let origin = if input["worker_wake"]["voice_origin"].is_object() {
                        &input["worker_wake"]["voice_origin"]
                    } else {
                        &input["composer"]["origin"]
                    };
                    if let Some((_, driver)) = calls
                        && !nonce.is_empty()
                        && origin["voice"].is_object()
                    {
                        let entry = inbox.publication(nonce);
                        let _ = driver
                            .readout
                            .try_send(super::call_runtime::Readout::Publication(
                                nonce.into(),
                                origin.clone(),
                                field(&entry, "body").into(),
                            ));
                    }
                    let publication = inbox.publication(nonce);
                    if !publication["proof"]["file_source"].is_null() {
                        mail_store::release_publication(
                            &self.owner.supervisor_root()?,
                            &publication["proof"]["file_source"],
                            nonce,
                        )?;
                    }
                    state["input"] = Value::Null;
                    inbox.set_manager(state)?;
                }
            }
            return Ok(());
        }
        let Some(input) = inbox
            .next_prepared()
            .or_else(|| calls.and_then(|_| inbox.prepared_voice()))
        else {
            self.wake_from_mail(cfg, inbox, api, state, &status)?;
            if inbox.manager()["input"].is_null() {
                self.park_if_idle(cfg, inbox, &status)?;
            }
            return Ok(());
        };
        if status["ready_for_input"] != true
            || !matches!(field(&status["turn"], "state"), "idle" | "completed")
        {
            return Ok(());
        }
        self.idle_since.set(None);
        let voice = input["voice_origin"].is_object();
        let (id, current) = if voice {
            let id = field(&input, "operation_id").to_owned();
            let Some((place, _)) = calls else {
                return Ok(());
            };
            if super::call_runtime::verify_live(place, &input["voice_origin"]).is_err() {
                return inbox.withdraw(&id);
            }
            api.verify(cfg)?;
            (id, input)
        } else {
            let id = field(&input["route"], "message_id").to_owned();
            let Some(source) = api.current(cfg, &id)? else {
                return inbox.withdraw(&id);
            };
            let Some(current) = api.admit(inbox, &source)? else {
                return inbox.withdraw(&id);
            };
            (id, current)
        };
        let starting_prompt = crate::manager_prompt::load(&self.owner.supervisor_root()?)?;
        let provider = field(&status["binding"], "provider");
        let body = field(&current, "text");
        if body.len() > crate::manager_prompt::provider_input_budget(provider, &starting_prompt) {
            return inbox.reject_manager_input(
                &id,
                &current["revision"],
                "manager input exceeds native dispatch limit",
            );
        }
        if provider == "claude"
            && [body, starting_prompt.as_str()].iter().any(|text| {
                text.contains(crate::providers::codex_proof::claude::PREFIX) || text.contains('\0')
            })
        {
            return inbox.reject_manager_input(
                &id,
                &current["revision"],
                "manager input contains a reserved Claude envelope delimiter",
            );
        }
        let composer = self.owner.composer(&current, &status, &starting_prompt)?;
        let semantic = operation("semantic")?;
        state["input"] = json!({"operation":semantic,"composer":composer,"source_id":id,"starting_prompt":starting_prompt});
        inbox.cutover(&id, field(&current, "revision"), state)?;
        // Ambiguous sends are never retried. Only the native pre-effect authority refusal is safe.
        match self.owner.send(&semantic, &composer) {
            Err(err) if voice && crate::ipc::refused_with(&err, super::call_runtime::WITHDRAWN) => {
                inbox.abort_voice(&id)
            }
            Err(_) if voice => {
                eprintln!(
                    "discord: voice admission is unconfirmed; reconciling its original native nonce"
                );
                Ok(())
            }
            result => result,
        }
    }

    #[cfg(test)]
    pub(super) fn fixture_idle_elapsed(&self) {
        self.idle_since
            .set(Some(Instant::now() - Duration::from_secs(121)));
    }

    fn park_if_idle(&self, cfg: &Config, inbox: &mut Inbox, status: &Value) -> io::Result<()> {
        if cfg.0["calls"].is_object()
            || status["ready_for_input"] != true
            || status["turn"]["state"] != "completed"
        {
            self.idle_since.set(None);
            return Ok(());
        }
        let Some(since) = self.idle_since.get() else {
            self.idle_since.set(Some(Instant::now()));
            return Ok(());
        };
        if since.elapsed() < Duration::from_secs(120) || !self.owner.can_retire() {
            return Ok(());
        }
        let mut state = inbox.manager().clone();
        let checkpoint = hash(&json!([
            state["binding"],
            state["generation"],
            status["turn"],
            operation("retire")?
        ]))?;
        let request = json!({"binding":state["binding"],"generation":state["generation"],
            "checkpoint":checkpoint,"expected_turn":status["turn"]});
        let id = crate::supervisor::manager::retirement_id(&request)?;
        state["retirement"] = json!({"operation_id":id,"request":request});
        inbox.set_manager(state)?;
        Ok(())
    }

    fn resume_parked(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        mut state: Value,
        voice: bool,
    ) -> io::Result<()> {
        let checkpoint = &state["retirement"];
        let phase = self.owner.retirement(checkpoint, true)?;
        if matches!(phase.as_str(), "prepared" | "uncertain") {
            let _ = self.owner.retirement(checkpoint, false);
            return Ok(());
        }
        let binding = &state["binding"];
        let root = self.owner.supervisor_root()?;
        let queued = inbox.next_prepared().is_some()
            || voice && inbox.prepared_voice().is_some()
            || !mail_store::pending(&root, field(binding, "conversation_id"), 0, |note| {
                inbox.worker_mail_anchor(binding, note).is_some()
                    && mail_wake::key(note).is_ok_and(|key| state["worker_wakes"][key].is_null())
            })?
            .is_empty();
        if phase == "unknown" {
            if queued {
                let _effect = match super::lock_manager_mail_effect(&self.scope(cfg)) {
                    Ok(lock) => lock,
                    Err(error) if error.kind() == io::ErrorKind::WouldBlock => return Ok(()),
                    Err(error) => return Err(error),
                };
                if self.owner.retirement(checkpoint, true)? == "unknown" {
                    state["retirement"] = Value::Null;
                    self.idle_since.set(None);
                    inbox.set_manager(state)?;
                }
            } else {
                let _ = self.owner.retirement(checkpoint, false);
            }
            return Ok(());
        }
        if !queued {
            return Ok(());
        }
        let generation = state["generation"]
            .as_u64()
            .and_then(|n| n.checked_add(1))
            .ok_or_else(|| invalid("manager generation exceeded"))?;
        let previous = checkpoint["request"].clone();
        let history = &mut state["history"];
        if history.is_null() {
            *history = json!([]);
        }
        history
            .as_array_mut()
            .ok_or_else(|| invalid("manager retirement history changed"))?
            .push(previous.clone());
        state["generation"] = json!(generation);
        state["resume_request"] = previous;
        state["session"] = Value::Null;
        state["binding"] = Value::Null;
        state["retirement"] = Value::Null;
        state["launch_operation"] = json!(operation("launch")?);
        self.idle_since.set(None);
        inbox.set_manager(state)
    }

    /// None is uncertain transport or a response without exact message coordinates.
    fn mail_owner_current(
        cfg: &Config,
        inbox: &mut Inbox,
        api: &impl Audience,
        id: &str,
        revision: &Value,
    ) -> io::Result<Option<bool>> {
        if !inbox.matches_owner_admission(id, revision) {
            return Ok(Some(false));
        }
        if super::call_origin::key(id) {
            return Ok(api.verify(cfg).ok().map(|_| true));
        }
        let source = match api.current(cfg, id) {
            Ok(Some(source)) => source,
            Ok(None) => return Ok(Some(false)),
            Err(_) => return Ok(None),
        };
        if source["id"] != id
            || !source["author"]["id"].is_string()
            || !source["content"].is_string()
            || !source["attachments"].is_array()
            || !source["type"].is_u64()
            || !(source["edited_timestamp"].is_null() || source["edited_timestamp"].is_string())
        {
            return Ok(None);
        }
        match api.admit(inbox, &source) {
            Ok(receipt) => Ok(Some(receipt.is_some_and(|current| {
                current["state"] == "accepted" && current["revision"] == *revision
            }))),
            // Exact scoped GET succeeded, but the accepted revision conflicts with it.
            Err(error) if error.kind() == io::ErrorKind::InvalidInput => Ok(Some(false)),
            Err(error) => Err(error),
        }
    }

    fn wake_from_mail(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        api: &impl Audience,
        mut state: Value,
        status: &Value,
    ) -> io::Result<()> {
        if status["ready_for_input"] != true
            || !matches!(field(&status["turn"], "state"), "idle" | "completed")
        {
            return Ok(());
        }
        let root = self.owner.supervisor_root()?;
        let binding = &state["binding"];
        let notes = crate::supervisor::mail_store::pending(
            &root,
            field(binding, "conversation_id"),
            self.mail_cursor.get(),
            |note| {
                mail_wake::key(note).is_ok_and(|key| state["worker_wakes"][key].is_null())
                    && inbox.worker_mail_anchor(binding, note).is_some()
            },
        )?;
        if notes.is_empty() {
            self.mail_cursor.set(0);
        }
        for note in notes {
            self.mail_cursor.set(
                note["Sequence"]
                    .as_u64()
                    .ok_or_else(|| invalid("invalid worker wake order"))?,
            );
            let (id, accepted) = inbox
                .worker_mail_anchor(binding, &note)
                .ok_or_else(|| invalid("worker wake has no current owner anchor"))?;
            match Self::mail_owner_current(cfg, inbox, api, &id, &accepted["revision"])? {
                Some(true) => {}
                Some(false) => {
                    inbox.withdraw(&id)?;
                    continue;
                }
                None => continue,
            }
            let key = mail_wake::key(&note)?;
            let accepted_revision = accepted["revision"].clone();
            let voice = accepted["voice_origin"].clone();
            let mut wake_input = accepted;
            wake_input
                .as_object_mut()
                .unwrap()
                .shift_remove("voice_origin");
            wake_input["operation_id"] = json!(format!("mail_{key}"));
            wake_input["revision"] = json!(key);
            let starting_prompt = crate::manager_prompt::load(&root)?;
            wake_input["text"] = json!(mail_wake::text(
                &note,
                crate::manager_prompt::provider_input_budget(
                    field(binding, "provider"),
                    &starting_prompt
                )
            )?);
            let mut composer = self.owner.composer(&wake_input, status, &starting_prompt)?;
            let semantic = operation("semantic")?;
            let mut wake = json!({"key":key,"binding":mail_wake::identity(&note),"id":note["ID"],"owner_revision":accepted_revision});
            if voice.is_object() {
                composer["origin"]["worker_wake"] = json!({"source_id":id,"key":key});
                wake["voice_origin"] = voice;
                wake["note_digest"] = json!(mail_wake::note_digest(&note)?);
            }
            state["input"] = json!({"operation":semantic,"composer":composer,"source_id":id,"worker_wake":wake,"starting_prompt":starting_prompt});
            state["worker_wakes"][&key] = json!(true);
            inbox.set_manager(state)?;
            if self.owner.send(&semantic, &composer).is_err() {
                eprintln!("discord: worker mailbox wake awaits native admission reconciliation");
            }
            return Ok(());
        }
        Ok(())
    }

    fn reconcile_input(&self, inbox: &mut Inbox, state: &mut Value) -> io::Result<String> {
        let input = &state["input"];
        let observation = self.owner.input_status(&input["composer"])?;
        if observation["external_input"]
            != input["composer"]["manager_admission"]["expected_external_input"]
        {
            return Err(invalid("native manager received external input"));
        }
        let phase = field(&observation, "state");
        if matches!(phase, "running" | "completed") {
            let native_turn = field(&observation, "native_turn_id");
            if native_turn.is_empty()
                || input["turn_id"]
                    .as_str()
                    .is_some_and(|id| id != native_turn)
            {
                return Err(invalid(
                    "native input observation changed its turn identity",
                ));
            }
            if input["turn_id"].is_null() {
                let id = field(input, "source_id").to_owned();
                state["input"]["turn_id"] = json!(native_turn);
                if state["input"]["worker_wake"].is_null() {
                    inbox.settled_input(&id, state.clone())?;
                } else {
                    inbox.set_manager(state.clone())?;
                }
            }
        }
        Ok(phase.to_owned())
    }

    fn deliver(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        api: &impl Audience,
        state: &Value,
        now: u64,
    ) -> io::Result<()> {
        for nonce in inbox.pending_publications() {
            let entry = inbox.publication(&nonce);
            verify_publication(&nonce, &entry)?;
            let publication = &entry["proof"];
            let input = &state["input"];
            if publication["binding"] != state["binding"]
                || publication["turn_id"] != input["turn_id"]
                || publication["source_id"] != input["source_id"]
                || input["turn_id"].is_null()
                || input["final_nonce"] != nonce
                || publication["voice_worker"] != mail_wake::link(&input["worker_wake"])
                || publication["call_receipt"]["native_input_id"]
                    != input["composer"]["manager_admission"]["nonce"]
            {
                return Err(invalid(
                    "queued publication is out of the current native input scope",
                ));
            }
            if entry["retry_at"].as_u64().is_some_and(|at| now < at) {
                continue;
            }
            api.verify(cfg)?;
            let source_id = field(input, "source_id");
            let origin = &input["composer"]["origin"];
            if super::call_origin::key(source_id) {
                let voice = if input["worker_wake"]["voice_origin"].is_object() {
                    &input["worker_wake"]["voice_origin"]
                } else {
                    origin
                };
                if !inbox.accepted_voice(source_id, voice) {
                    return Err(invalid(
                        "publication requires the original accepted voice admission",
                    ));
                }
            } else {
                let source = api
                    .current(cfg, source_id)?
                    .filter(|source| source["id"] == source_id)
                    .ok_or_else(|| {
                        invalid("publication requires the original visible Discord source")
                    })?;
                let current = api
                    .admit(inbox, &source)?
                    .filter(|receipt| receipt["state"] == "accepted")
                    .ok_or_else(|| {
                        invalid("publication source no longer proves native admission")
                    })?;
                let expected_revision = if input["worker_wake"].is_null() {
                    &origin["revision"]
                } else {
                    &input["worker_wake"]["owner_revision"]
                };
                if current["revision"] != *expected_revision {
                    return Err(invalid("publication source changed after native admission"));
                }
            }
            let evidence = self.owner.input_status(&input["composer"])?;
            if evidence["native_turn_id"] != input["turn_id"]
                || evidence["external_input"]
                    != input["composer"]["manager_admission"]["expected_external_input"]
                || !matches!(field(&evidence, "state"), "running" | "completed")
            {
                return Err(invalid(
                    "native publication authority changed immediately before delivery",
                ));
            }
            let body = field(&entry, "body");
            let file_source = &publication["file_source"];
            let files = if file_source.is_null() {
                Vec::new()
            } else {
                let selected = &input["selected_note"];
                if selected["id"] != file_source["id"]
                    || selected["binding"] != file_source["binding"]
                    || !file_source["files"].as_array().is_some_and(|files| {
                        files.iter().all(|file| {
                            selected["files"]
                                .as_array()
                                .is_some_and(|known| known.contains(file))
                        })
                    })
                {
                    return Err(invalid(
                        "queued publication no longer matches the selected worker note",
                    ));
                }
                mail_store::pin_publication(&self.owner.supervisor_root()?, file_source, &nonce)?
            };
            inbox.publication_state(&nonce, "sending", None)?;
            match api.post(cfg, &nonce, body, &files) {
                Post::Acknowledged(receipt)
                    if receipt["nonce"] == nonce
                        && inbox.observe(&receipt, Observation::Rest)? => {}
                Post::RateLimited(after) => {
                    inbox.publication_state(&nonce, "prepared", Some(now.saturating_add(after)))?
                }
                _ => inbox.publication_state(&nonce, "uncertain", None)?,
            }
        }
        Ok(())
    }

    fn serve_tool(&self, cfg: &Config, inbox: &mut Inbox, api: &impl Audience) -> io::Result<()> {
        let (mut stream, _) = match self.listener.accept() {
            Ok(pair) => pair,
            Err(err) if err.kind() == io::ErrorKind::WouldBlock => return Ok(()),
            Err(err) => return Err(err),
        };
        let result = read_tool(&stream, &self.credential)
            .and_then(|request| self.tool(cfg, inbox, api, &request));
        let reply = match result {
            Ok(data) => json!({"data":data}),
            Err(err)
                if matches!(
                    err.kind(),
                    io::ErrorKind::InvalidInput
                        | io::ErrorKind::InvalidData
                        | io::ErrorKind::TimedOut
                        | io::ErrorKind::UnexpectedEof
                ) =>
            {
                json!({"error":"unconfirmed"})
            }
            Err(err) => return Err(err),
        };
        let body = serde_json::to_vec(&reply)?;
        stream.set_write_timeout(Some(Duration::from_secs(2)))?;
        let _ = write!(
            stream,
            "HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
            body.len()
        )
        .and_then(|_| stream.write_all(&body));
        Ok(())
    }

    fn tool(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        api: &impl Audience,
        request: &Value,
    ) -> io::Result<Value> {
        let mut state = inbox.manager().clone();
        if request.as_object().is_none_or(|object| object.len() != 4)
            || request["call"]
                .as_object()
                .is_none_or(|call| call.len() != 3)
            || request["version"] != 1
            || request["conversation_id"] != state["binding"]["conversation_id"]
            || request["session_id"] != field(&state, "session").rsplit('/').next().unwrap_or("")
            || state["input"].is_null()
            || !matches!(
                field(&request["call"], "name"),
                "publish" | "swarm_read_note" | "swarm_reply" | "swarm_read_attachment"
            )
        {
            return Err(invalid("unbound manager publication"));
        }
        if state["input"]["turn_id"].is_null() {
            let _ = self.reconcile_input(inbox, &mut state)?;
        }
        let input = &state["input"];
        if input["turn_id"].is_null() {
            return Err(invalid("native manager input is not yet observed"));
        }
        let call = &request["call"];
        let mut args = call["arguments"].clone();
        let tool = field(call, "name");
        if tool == "swarm_read_attachment" {
            if args.as_object().is_none_or(|fields| fields.len() != 3)
                || !args["id"].as_str().is_some_and(super::config::snowflake)
                || !args["sha256"].as_str().is_some_and(|hash| {
                    hash.len() == 64 && hash.bytes().all(|b| b.is_ascii_hexdigit())
                })
                || !args["offset"].is_u64()
            {
                return Err(invalid("malformed exact received attachment request"));
            }
            args.sort_all_objects();
            let proof = self.owner.prove_publication(
                &input["composer"],
                tool,
                &hash(&args)?,
                &call["meta"],
            )?;
            if proof["native_turn_id"] != input["turn_id"] {
                return Err(invalid("attachment read belongs to another native turn"));
            }
            return inbox.read_active_attachment(
                input,
                field(&args, "id"),
                field(&args, "sha256"),
                args["offset"].as_u64().unwrap(),
                |id| api.current(cfg, id),
            );
        }
        if tool != "publish" {
            if args.as_object().is_none_or(|fields| {
                fields.keys().any(|key| {
                    if tool == "swarm_reply" {
                        !matches!(key.as_str(), "id" | "session" | "session_instance" | "text")
                    } else {
                        !matches!(key.as_str(), "id" | "session" | "session_instance")
                    }
                })
            }) {
                return Err(invalid("invalid worker mailbox tool arguments"));
            }
            args.sort_all_objects();
            let proof = self.owner.prove_publication(
                &input["composer"],
                tool,
                &hash(&args)?,
                &call["meta"],
            )?;
            if proof["native_turn_id"] != input["turn_id"] {
                return Err(invalid("native mailbox tool belongs to another turn"));
            }
            let scope = self.owner.supervisor_root()?;
            let session = if input["worker_wake"].is_null() {
                field(&state, "session")
                    .rsplit('/')
                    .next()
                    .unwrap_or_default()
            } else {
                field(&input["worker_wake"]["binding"], "Parent")
            };
            let input_hash = if input["worker_wake"].is_null() {
                crate::supervisor::mail::parent_hash(field(&proof, "native_input_id"))
            } else {
                let wake = &input["worker_wake"];
                if args.as_object().is_some_and(|fields| !fields.is_empty())
                    && (!args["id"].is_null() && args["id"] != wake["id"]
                        || !args["session"].is_null()
                            && args["session"] != wake["binding"]["Session"]
                        || !args["session_instance"].is_null()
                            && args["session_instance"] != wake["binding"]["Instance"])
                {
                    return Err(invalid(
                        "worker wake tools require their exact selected note",
                    ));
                }
                args["id"] = wake["id"].clone();
                args["session"] = wake["binding"]["Session"].clone();
                args["session_instance"] = wake["binding"]["Instance"].clone();
                field(&wake["binding"], "ParentInputHash").to_owned()
            };
            let mut result = mail_store::manager_note(
                &scope,
                field(&state["binding"], "conversation_id"),
                session,
                &input_hash,
                &args,
                tool == "swarm_reply",
            )?;
            if let Some(binding) = result
                .as_object_mut()
                .and_then(|result| result.shift_remove("file_binding"))
            {
                if !state["input"]["final_nonce"].is_null() {
                    return Err(invalid(
                        "cannot change file selection after final publication",
                    ));
                }
                state["input"]["selected_note"] = json!({
                    "id":result["id"],"binding":binding,"files":result["files"]
                });
                inbox.set_manager(state)?;
            }
            return Ok(result);
        }
        let fields = args
            .as_object()
            .ok_or_else(|| invalid("invalid publication arguments"))?;
        if fields.keys().any(|key| {
            !matches!(
                key.as_str(),
                "output_id" | "sequence" | "text" | "final" | "file_ids"
            )
        }) || args["sequence"] != 1
            || args["final"] != true
        {
            return Err(invalid(
                "only one deliberate final text publication is supported",
            ));
        }
        let output = field(&args, "output_id").to_owned();
        let body = field(&args, "text").to_owned();
        if output.is_empty()
            || output.len() > 128
            || output.chars().any(char::is_control)
            || args["text"].as_str().is_none()
            || body.trim().is_empty() && args["file_ids"].is_null()
            || body.encode_utf16().count() > 1900
        {
            return Err(invalid("invalid final publication ID or body"));
        }
        args.sort_all_objects();
        let proof = self.owner.prove_publication(
            &input["composer"],
            "publish",
            &hash(&args)?,
            &call["meta"],
        )?;
        if proof["native_turn_id"] != input["turn_id"] {
            return Err(invalid("manager publication belongs to another turn"));
        }
        let mut publication = json!({
            "binding":state["binding"], "turn_id":input["turn_id"],
            "source_id":input["source_id"], "output_id":output,"body":body,
            "native_call_id":proof["native_call_id"], "call_receipt":proof,
            "host_meta":call["meta"], "arguments":args, "arguments_hash":hash(&args)?
        });
        let link = mail_wake::link(&input["worker_wake"]);
        if !link.is_null() {
            publication["voice_worker"] = link;
        }
        if !self.owner.guild().is_empty() {
            publication["audience"] =
                json!({"guild_id":self.owner.guild(),"thread_id":self.owner.channel()});
        }
        if !args["file_ids"].is_null() {
            let ids = args["file_ids"]
                .as_array()
                .filter(|ids| !ids.is_empty() && ids.len() <= 10)
                .ok_or_else(|| invalid("select 1 to 10 exact worker file IDs"))?;
            let selected_note = &input["selected_note"];
            let candidates = selected_note["files"].as_array().ok_or_else(|| {
                invalid("read the exact worker completion before publishing its files")
            })?;
            let mut picked = Vec::new();
            for requested in ids {
                let file = candidates
                    .iter()
                    .find(|file| requested.is_string() && &file["id"] == requested)
                    .ok_or_else(|| {
                        invalid("publication file does not belong to the read worker note")
                    })?;
                if picked.contains(&file) {
                    return Err(invalid("publication requested a duplicate worker file"));
                }
                picked.push(file);
            }
            publication["file_source"] =
                json!({"id":selected_note["id"],"binding":selected_note["binding"],"files":picked});
        }
        let nonce = publication_nonce(&publication)?;
        if input["final_nonce"]
            .as_str()
            .is_some_and(|saved| saved != nonce)
        {
            return Err(invalid("a final publication is already selected"));
        }
        state["input"]["final_nonce"] = json!(nonce);
        inbox.prepare_publication(&nonce, publication, state)?;
        Ok(json!({"output_id":output,"state":inbox.publication(&nonce)["state"]}))
    }
}

impl Drop for Dispatcher {
    fn drop(&mut self) {
        if fs::symlink_metadata(&self.socket)
            .is_ok_and(|meta| (meta.dev(), meta.ino()) == self.socket_identity)
        {
            let _ = fs::remove_file(&self.socket);
        }
    }
}

fn read_tool(stream: &std::os::unix::net::UnixStream, credential: &str) -> io::Result<Value> {
    let mut reader =
        BufReader::new(DeadlineReader(stream, Instant::now() + Duration::from_secs(2)).take(8193));
    let mut line = String::new();
    reader.read_line(&mut line)?;
    if !matches!(
        line.as_str(),
        "POST /manager/call HTTP/1.1\r\n" | "POST /manager/call HTTP/1.0\r\n"
    ) {
        return Err(invalid("invalid manager request path"));
    }
    let mut headers = std::collections::BTreeMap::new();
    loop {
        line.clear();
        if reader.read_line(&mut line)? == 0 {
            return Err(invalid("missing manager request headers"));
        }
        if line == "\r\n" {
            break;
        }
        let (key, value) = line
            .split_once(':')
            .ok_or_else(|| invalid("invalid manager request header"))?;
        if headers
            .insert(key.to_ascii_lowercase(), value.trim().to_owned())
            .is_some()
        {
            return Err(invalid("duplicate manager request header"));
        }
    }
    let expected = format!("Bearer {credential}");
    if !headers
        .get("authorization")
        .is_some_and(|value| bool::from(value.as_bytes().ct_eq(expected.as_bytes())))
        || headers.get("host").is_none_or(|v| v != "voice")
        || headers
            .get("content-type")
            .is_none_or(|v| v != "application/json")
        || headers.contains_key("origin")
        || headers.contains_key("transfer-encoding")
    {
        return Err(invalid("manager tool request not authenticated"));
    }
    let size = headers
        .get("content-length")
        .and_then(|n| n.parse::<usize>().ok())
        .filter(|n| (1..=8192).contains(n))
        .ok_or_else(|| invalid("invalid manager tool content length"))?;
    let remaining = reader.get_ref().limit();
    reader
        .get_mut()
        .set_limit(remaining.saturating_add(size as u64));
    let mut body = vec![0; size];
    reader.read_exact(&mut body)?;
    Ok(serde_json::from_slice(&body)?)
}

fn auth_file(path: &Path) -> io::Result<String> {
    let open = || {
        OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
            .open(path)
    };
    let mut file = match open() {
        Ok(file) => file,
        Err(err) if err.kind() == io::ErrorKind::NotFound => {
            let value = random_hex::<32>()?;
            let mut file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(path)?;
            file.write_all(value.as_bytes())?;
            file.sync_all()?;
            File::open(path.parent().unwrap())?.sync_all()?;
            open()?
        }
        Err(err) => return Err(err),
    };
    let meta = file.metadata()?;
    // SAFETY: the effective UID function takes no arguments.
    if !meta.is_file()
        || meta.nlink() != 1
        || meta.mode() & 0o777 != 0o600
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(invalid("manager auth file lacks private owner custody"));
    }
    let mut value = String::new();
    (&mut file).take(65).read_to_string(&mut value)?;
    if value.len() != 64
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return Err(invalid("manager auth file has an invalid credential"));
    }
    Ok(value)
}

fn random_hex<const N: usize>() -> io::Result<String> {
    let mut bytes = [0; N];
    File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

fn millis() -> io::Result<u64> {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(io::Error::other)?
        .as_millis()
        .try_into()
        .map_err(io::Error::other)
}

fn operation(kind: &str) -> io::Result<String> {
    Ok(format!("{kind}:{}:{}", millis()?, random_hex::<16>()?))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ipc::wire;
    use sha2::{Digest, Sha256};
    use std::cell::RefCell;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{Arc, Mutex};

    struct MockDiscord {
        outcome: RefCell<Post>,
        posted: RefCell<Vec<(String, String)>>,
    }

    impl Audience for MockDiscord {
        fn current(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>> {
            Ok(Some(source(cfg, id)))
        }

        fn verify(&self, cfg: &Config) -> io::Result<()> {
            assert_eq!(cfg.text("dm_channel_id"), "400");
            Ok(())
        }

        fn post(&self, cfg: &Config, nonce: &str, body: &str, _files: &[Captured]) -> Post {
            self.posted
                .borrow_mut()
                .push((nonce.to_owned(), body.to_owned()));
            match self.outcome.replace(Post::Unconfirmed) {
                Post::Acknowledged(_) => Post::Acknowledged(json!({
                    "id":"102", "author":{"id":"200","bot":true},"channel_id":cfg.text("dm_channel_id"),
                    "nonce":nonce,"content":body
                })),
                result => result,
            }
        }
    }

    fn source(cfg: &Config, id: &str) -> Value {
        json!({"id":id, "type":0, "channel_id":cfg.text("dm_channel_id"),"content":if id=="100" {"x".repeat(3500)} else {"hello".into()},
            "author":{"id":cfg.text("owner_id")},"attachments":[]})
    }

    struct Record {
        row: Value,
        binding: Value,
        launch: Value,
        composer: Value,
        sends: usize,
        wrong_turn: bool,
        wrong_binding: bool,
        completed: bool,
    }

    impl Record {
        fn respond(&mut self, endpoint: &str, request: Value) -> Value {
            let mut response = match field(&request, "op") {
                "owner_launch_status" => json!({
                    "op":"owner_launch_status", "subject_operation_id":request["subject_operation_id"],
                    "operation_outcome":{"state":"unknown_operation"}
                }),
                "owner_launch" => {
                    assert!(self.launch.is_null(), "manager launched more than once");
                    self.launch = request["launch"].clone();
                    assert!(
                        self.launch["env"].is_null(),
                        "Discord credentials must not cross into native launch environment"
                    );
                    assert!(self.launch["manager"].get("token").is_none());
                    let conversation = &self.launch["manager"]["conversation_id"];
                    self.row = json!({"id":format!("{endpoint}/local"),"endpoint_id":endpoint,
                        "agent":"codex","role":"manager","manager_conversation_id":conversation,
                        "cwd":self.launch["cwd"],"status":{"Process":"running"},
                        "capabilities":{"session_instance":"incarnation"}});
                    self.binding = json!({"session":self.row["id"],"session_instance":"incarnation",
                        "conversation_id":conversation,"provider":"codex","native_session_id":"provider-thread",
                        "native_generation":"native","observation_generation":"observed","login_epoch":"account"});
                    json!({"op":"launch","operation_id":request["operation_id"],"session":self.row})
                }
                "list" => json!({"op":"list","sessions":[self.row]}),
                "manager_runtime" => {
                    let mut binding = self.binding.clone();
                    if self.wrong_binding {
                        binding["observation_generation"] = json!("different-writer");
                    }
                    assert_eq!(
                        request["manager_runtime_request"]["session_instance"],
                        "incarnation"
                    );
                    json!({"op":"manager_runtime", "session_id":self.row["id"],
                        "manager_runtime":{
                            "ready_for_input":true,"binding":binding,
                            "bridge_socket":self.launch["manager"]["bridge_socket"],
                            "auth_file":self.launch["manager"]["auth_file"],
                            "turn":{"state":if self.composer.is_null() {"idle"} else {"running"},"external_input":0}
                        }
                    })
                }
                "semantic_send" => {
                    self.sends += 1;
                    self.composer = request["composer_send"].clone();
                    assert_eq!(self.composer["manager_admission"]["binding"], self.binding);
                    assert_eq!(self.composer["origin"]["installation_id"], "fixture");
                    assert_eq!(self.composer["origin"]["actor_id"], "300");
                    assert_eq!(self.composer["origin"]["channel_id"], "400");
                    assert_eq!(
                        self.composer["text"],
                        crate::manager_prompt::render(&"P".repeat(800), "hello").unwrap()
                    );
                    json!({"op":"ok","operation_id":request["operation_id"]})
                }
                "manager_input_status" => {
                    assert_eq!(
                        request["manager_input_status_request"],
                        self.composer["manager_admission"]
                    );
                    json!({"op":"manager_input_status","session_id":self.row["id"],
                    "manager_input_status":{
                       "state":if self.completed {"completed"} else {"running"}, "external_input":0,
                       "native_input_id":self.composer["manager_admission"]["nonce"],"native_turn_id":"authorized-turn"
                    }})
                }
                "manager_resolve_call" => {
                    assert_eq!(request["manager_call"]["binding"], self.binding);
                    assert_eq!(request["manager_call"]["tool_name"], "publish");
                    json!({"op":"manager_resolve_call","session_id":self.row["id"],
                       "manager_call_proof":{"main":true, "native_input_id":self.composer["manager_admission"]["nonce"],
                           "native_turn_id":if self.wrong_turn {"old-turn"} else {"authorized-turn"},"native_call_id":"provider-call"}})
                }
                op => panic!("unsupported fixture operation {op}"),
            };
            response["endpoint_id"] = json!(endpoint);
            response
        }
    }

    #[test]
    fn exact_rest_acknowledgement_has_distinct_evidence() {
        let dir = Path::new("/tmp").join(format!("r-{}", &runtime::random_id().unwrap()[..12]));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"installation_id":"fixture","application_id":"200","owner_id":"300","dm_channel_id":"400"}),
        );
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("100").unwrap();
        let (nonce, proof) = super::super::receipt::fixture_publication("deliberate reply");
        inbox
            .prepare_publication(&nonce, proof, Value::Null)
            .unwrap();
        inbox.publication_state(&nonce, "sending", None).unwrap();
        let api = MockDiscord {
            outcome: RefCell::new(Post::Acknowledged(Value::Null)),
            posted: RefCell::new(Vec::new()),
        };
        let Post::Acknowledged(receipt) = api.post(&cfg, &nonce, "deliberate reply", &[]) else {
            panic!("expected acknowledgement")
        };
        assert!(inbox.observe(&receipt, Observation::Rest).unwrap());
        assert_eq!(inbox.publication(&nonce)["rest_acknowledged"], true);
        assert!(inbox.publication(&nonce)["history_seen"].is_null());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn owner_dm_runs_through_bound_manager_and_exact_reply_once() {
        let dir = Path::new("/tmp").join(format!("d-{}", &runtime::random_id().unwrap()[..12]));
        runtime::private_dir(&dir).unwrap();
        let dir = fs::canonicalize(dir).unwrap();
        let socket = dir.join("daemon.sock");
        let digest = format!("{:x}", Sha256::digest(dir.as_os_str().as_encoded_bytes()));
        let endpoint = format!("ep-{}", &digest[..8]);
        let cfg = Config(
            json!({"version":1,"enabled":true,"name":"fixture","mode":"personal",
            "installation_id":"fixture","application_id":"200","owner_id":"300","dm_channel_id":"400",
            "daemon_socket":socket,"work_dir":dir,"manager_cli":"codex","supervisor":{"state_dir":dir}}),
        );
        let listener = runtime::bind(&socket).unwrap();
        let done = Arc::new(AtomicBool::new(false));
        let state = Arc::new(Mutex::new(Record {
            row: Value::Null,
            binding: Value::Null,
            launch: Value::Null,
            composer: Value::Null,
            sends: 0,
            wrong_turn: false,
            wrong_binding: false,
            completed: false,
        }));
        let server = {
            let done = done.clone();
            let state = state.clone();
            std::thread::spawn(move || {
                while !done.load(Ordering::Relaxed) {
                    let (mut socket, _) = listener.accept().unwrap();
                    let send = |socket: &mut _, frame: Value| {
                        wire::write_frame(
                            socket,
                            wire::CONTROL,
                            &serde_json::to_vec(&frame).unwrap(),
                        )
                        .unwrap()
                    };
                    let Ok((_, hello)) = wire::read_frame(&mut socket) else {
                        continue;
                    };
                    let hello: Value = serde_json::from_slice(&hello).unwrap();
                    assert_eq!(hello["op"], "hello");
                    send(
                        &mut socket,
                        json!({"op":"hello","protocol_version":1,"endpoint_id":endpoint,"capabilities":super::super::manager::CAPS}),
                    );
                    let Ok((_, raw)) = wire::read_frame(&mut socket) else {
                        continue;
                    };
                    let request = serde_json::from_slice(&raw).unwrap();
                    let response = state.lock().unwrap().respond(&endpoint, request);
                    send(&mut socket, response);
                }
            })
        };
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("99").unwrap();
        crate::manager_prompt::save(&dir, &"P".repeat(800)).unwrap();
        inbox.admit(&source(&cfg, "100")).unwrap();
        inbox.admit(&source(&cfg, "101")).unwrap();
        let dispatcher = Dispatcher::open(&cfg, &dir).unwrap().unwrap();
        let api = MockDiscord {
            outcome: RefCell::new(Post::RateLimited(60_000)),
            posted: RefCell::new(Vec::new()),
        };
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        assert_eq!(state.lock().unwrap().sends, 0);
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        assert_eq!(inbox.next_prepared().unwrap()["route"]["message_id"], "101");
        assert!(inbox.admit(&source(&cfg, "100")).unwrap().is_none());
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        assert_eq!(state.lock().unwrap().sends, 1);
        assert!(api.posted.borrow().is_empty());
        let publish = json!({"version":1, "conversation_id":inbox.manager()["binding"]["conversation_id"],
            "session_id":"local", "call":{"name":"publish","arguments":{
                "output_id":"final","sequence":1,"text":"deliberate reply","final":true
            },"meta":{"callId":"provider-call","threadId":"provider-thread","sessionId":"provider-thread",
                "x-codex-turn-metadata":{"turn_id":"authorized-turn","thread_id":"provider-thread","session_id":"provider-thread"}}}});
        let mut alien = publish.clone();
        alien["call"]["meta"]["threadId"] = json!("unbound-thread");
        assert!(dispatcher.tool(&cfg, &mut inbox, &api, &alien).is_err());
        let mut wrong_recipient = publish.clone();
        wrong_recipient["call"]["arguments"]["recipient_id"] = json!("401");
        assert!(
            dispatcher
                .tool(&cfg, &mut inbox, &api, &wrong_recipient)
                .is_err()
        );
        assert!(inbox.pending_publications().is_empty());
        state.lock().unwrap().wrong_turn = true;
        assert!(dispatcher.tool(&cfg, &mut inbox, &api, &publish).is_err());
        assert!(inbox.pending_publications().is_empty());
        state.lock().unwrap().wrong_turn = false;
        dispatcher.tool(&cfg, &mut inbox, &api, &publish).unwrap();
        dispatcher.tool(&cfg, &mut inbox, &api, &publish).unwrap();
        let nonce = field(&inbox.manager()["input"], "final_nonce").to_owned();
        assert_eq!(inbox.pending_publications(), std::slice::from_ref(&nonce));
        let outbox = dir.join("rust-text-v1.json");
        let original = runtime::read_json(&outbox).unwrap();
        drop(inbox);
        for damage in ["body", "nonce", "arguments", "source"] {
            let mut corrupted = original.clone();
            let entries = corrupted["outbox"].as_object_mut().unwrap();
            if damage == "nonce" {
                let entry = entries.shift_remove(&nonce).unwrap();
                entries.insert("wrong_nonce".into(), entry);
            } else {
                let entry = entries.get_mut(&nonce).unwrap();
                match damage {
                    "body" => entry["body"] = json!("unapproved text"),
                    "arguments" => entry["proof"]["arguments"]["text"] = json!("altered call"),
                    "source" => entry["proof"]["source_id"] = json!("991"),
                    _ => unreachable!(),
                }
            }
            runtime::write_json(&outbox, &corrupted).unwrap();
            assert!(Inbox::open(&dir, &cfg).is_err());
            assert!(api.posted.borrow().is_empty());
        }
        runtime::write_json(&outbox, &original).unwrap();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let mut displaced = inbox.manager().clone();
        displaced["input"]["final_nonce"] = json!("other-output");
        assert!(
            dispatcher
                .deliver(&cfg, &mut inbox, &api, &displaced, millis().unwrap())
                .is_err()
        );
        assert!(api.posted.borrow().is_empty());
        state.lock().unwrap().wrong_binding = true;
        assert!(dispatcher.tick(&cfg, &mut inbox, &api).is_err());
        assert!(api.posted.borrow().is_empty());
        state.lock().unwrap().wrong_binding = false;
        state.lock().unwrap().completed = true;
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        let retry_at = inbox.publication(&nonce)["retry_at"].as_u64().unwrap();
        let manager = inbox.manager().clone();
        dispatcher
            .deliver(&cfg, &mut inbox, &api, &manager, retry_at - 1)
            .unwrap();
        assert_eq!(api.posted.borrow().len(), 1);
        dispatcher
            .deliver(&cfg, &mut inbox, &api, &manager, retry_at)
            .unwrap();
        assert_eq!(inbox.publication(&nonce)["state"], "uncertain");
        drop(inbox);

        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        assert_eq!(api.posted.borrow().len(), 2);
        let wrong = json!({
            "id":"102", "author":{"id":"200", "bot":true},"channel_id":"401",
            "nonce":nonce,"content":"deliberate reply"
        });
        assert!(!inbox.observe(&wrong, Observation::Gateway).unwrap());
        let mut observed = wrong.clone();
        observed["channel_id"] = json!("400");
        assert!(inbox.observe(&observed, Observation::History).unwrap());
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        drop(inbox);
        let inbox = Inbox::open(&dir, &cfg).unwrap();
        assert_eq!(inbox.publication(&nonce)["state"], "sent");
        assert_eq!(inbox.publication(&nonce)["history_seen"], true);
        let mut inbox = inbox;
        let reused = inbox.publication(&nonce)["proof"].clone();
        assert!(
            inbox
                .prepare_publication("different-operation", reused, Value::Null)
                .is_err()
        );
        assert_eq!(
            api.posted.borrow().as_slice(),
            &[
                (nonce.clone(), "deliberate reply".into()),
                (nonce, "deliberate reply".into())
            ]
        );
        assert_eq!(state.lock().unwrap().sends, 1);
        done.store(true, Ordering::Relaxed);
        let _ = std::os::unix::net::UnixStream::connect(&socket).unwrap();
        server.join().unwrap();
        drop(dispatcher);
        fs::remove_dir_all(dir).unwrap();
    }
}
