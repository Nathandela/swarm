//! Owner protocol client for a single historical Discord dispatcher binding.
use super::config::{Config, invalid};
use crate::ipc::{Client, field};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::io;
use std::path::Path;

pub(super) const CAPS: &[&str] = &[
    "owner-launch",
    "manager-launch",
    "manager-provenance",
    "semantic-send",
    "message-origin",
];

pub(super) struct Manager {
    cfg: Config,
    discussion: String,
    conversation: String,
    route: Option<super::team_thread::Route>,
}

impl Manager {
    pub(super) fn new(cfg: &Config) -> io::Result<Self> {
        let route = super::team_thread::Route::configured(cfg)?;
        if cfg.text("mode") != "personal" && route.is_none() {
            return Err(invalid("manager binding requires a linked discussion"));
        }
        let coordinate = serde_json::to_vec(&json!([
            cfg.text("installation_id"),
            route.as_ref().map_or("", |route| &route.guild),
            route
                .as_ref()
                .map_or(cfg.text("dm_channel_id"), |route| &route.thread)
        ]))?;
        let digest = format!("{:x}", Sha256::digest(coordinate));
        let discussion = format!("dt_{}", &digest[..32]);
        let conversation = format!("discord-teammate-{discussion}");
        Ok(Self {
            cfg: cfg.clone(),
            discussion,
            conversation,
            route,
        })
    }

    pub(super) fn channel(&self) -> &str {
        self.route
            .as_ref()
            .map_or(self.cfg.text("dm_channel_id"), |route| &route.thread)
    }
    pub(super) fn guild(&self) -> &str {
        self.route.as_ref().map_or("", |route| &route.guild)
    }

    pub(super) fn supervisor_root(&self) -> io::Result<std::path::PathBuf> {
        let _ = self.dial()?;
        match self.cfg.0["supervisor"]["state_dir"].as_str() {
            Some(root) => std::fs::canonicalize(root),
            None => std::fs::canonicalize(crate::ipc::state_path()?),
        }
    }

    fn dial(&self) -> io::Result<Client> {
        let mut caps = CAPS.to_vec();
        if self.cfg.text("project_access") == "vm" {
            caps.push(crate::supervisor::manager::NATIVE_CLI_CAP);
        }
        let client = Client::dial(Path::new(self.cfg.text("daemon_socket")), &caps)?;
        self.cfg.check_supervisor(&client, &caps)?;
        Ok(client)
    }

    pub(super) fn verify_supervisor(&self) -> io::Result<()> {
        self.dial().map(drop)
    }

    pub(super) fn launch_request(&self, bridge_socket: &Path, auth_file: &Path) -> Value {
        let mut launch = json!({
            "agent": self.cfg.text("manager_cli"),
            "name": self.cfg.text("name"),
            "cwd": self.cfg.text("work_dir"),
            "options": {},
            "env": null,
            "cols":120, "rows":32, "initial_prompt":"",
            "manager":{
                "conversation_id": self.conversation,
                "bridge_socket": bridge_socket, "auth_file": auth_file
            }
        });
        if self.cfg.text("project_access") == "vm" {
            launch["manager"]["native_cli"] = json!(true);
        }
        launch
    }

    pub(super) fn launch(&self, operation: &str, launch: &Value) -> io::Result<Value> {
        if launch["manager"]["conversation_id"] != self.conversation {
            return Err(invalid("manager launch has a different discussion binding"));
        }
        let mut client = self.dial()?;
        let reply = client.exchange(
            json!({
                "op":"owner_launch", "operation_id": operation, "launch": launch
            }),
            "launch",
            crate::ipc::launch_timeout(self.cfg.text("manager_cli")),
        )?;
        self.verify_endpoint(&client, &reply)?;
        let session = &reply["session"];
        if reply["operation_id"] != operation
            || session["role"] != "manager"
            || session["manager_conversation_id"] != self.conversation
            || session["agent"] != self.cfg.text("manager_cli")
            || !field(session, "id").starts_with(&format!("{}/", client.endpoint()))
            || session["endpoint_id"] != client.endpoint()
            || session["cwd"] != self.cfg.text("work_dir")
        {
            return Err(invalid("uncorrelated scoped manager launch receipt"));
        }
        Ok(session.clone())
    }

    pub(super) fn can_retire(&self) -> bool {
        let caps = ["manager-retire", "manager-retire-resume-v1"];
        Client::dial(Path::new(self.cfg.text("daemon_socket")), &caps)
            .is_ok_and(|client| self.cfg.check_supervisor(&client, &caps).is_ok())
    }

    pub(super) fn retirement(&self, checkpoint: &Value, status_only: bool) -> io::Result<String> {
        let mut client = Client::dial(
            Path::new(self.cfg.text("daemon_socket")),
            &["manager-retire"],
        )?;
        self.cfg.check_supervisor(&client, &["manager-retire"])?;
        let op = if status_only {
            "manager_retire_status"
        } else {
            "manager_retire"
        };
        let request = &checkpoint["request"];
        let session = &request["binding"]["session"];
        let id = &checkpoint["operation_id"];
        let reply = client.request_body(
            json!({"op":op,"body_version":1,
            "session_id":session,"operation_id":id,"manager_retire_request":request}),
            op,
        )?;
        self.verify_endpoint(&client, &reply)?;
        let receipt = &reply["manager_retire"];
        if reply["operation_id"] != *id
            || reply["session_id"] != *session
            || receipt["operation_id"] != *id
        {
            return Err(invalid(
                "manager retirement receipt does not match the checkpoint",
            ));
        }
        let state = field(receipt, "state");
        if !matches!(state, "unknown" | "prepared" | "uncertain" | "retired") {
            return Err(invalid("unconfirmed manager retirement outcome"));
        }
        Ok(state.to_owned())
    }

    pub(super) fn launch_status(&self, operation: &str) -> io::Result<Value> {
        let mut client = self.dial()?;
        let reply = client.request_body(
            json!({
                "op":"owner_launch_status", "subject_operation_id":operation
            }),
            "owner_launch_status",
        )?;
        self.verify_endpoint(&client, &reply)?;
        if reply["subject_operation_id"] != operation || !reply["operation_outcome"].is_object() {
            return Err(invalid("uncorrelated scoped manager launch status"));
        }
        Ok(reply["operation_outcome"].clone())
    }

    pub(super) fn session(&self, id: &str) -> io::Result<Value> {
        let mut client = self.dial()?;
        let endpoint = client.endpoint().to_owned();
        let rows = client.sessions()?;
        let Some(row) = rows.into_iter().find(|row| row["id"] == id) else {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                "manager session is missing",
            ));
        };
        if row["endpoint_id"] != endpoint
            || row["role"] != "manager"
            || row["manager_conversation_id"] != self.conversation
            || row["agent"] != self.cfg.text("manager_cli")
            || row["cwd"] != self.cfg.text("work_dir")
            || row["status"]["Process"] != "running"
            || !valid(field(&row["capabilities"], "session_instance"))
        {
            return Err(invalid(
                "session does not prove the enrolled manager incarnation",
            ));
        }
        Ok(row)
    }

    pub(super) fn runtime(
        &self,
        session: &str,
        instance: &str,
        launch: &Value,
    ) -> io::Result<Value> {
        let mut client = self.dial()?;
        if !valid(instance) || !session.starts_with(&format!("{}/", client.endpoint())) {
            return Err(invalid(
                "manager runtime requires the exact historical supervisor session",
            ));
        }
        let reply = client.request_body(
            json!({
                "op":"manager_runtime", "session_id":session, "body_version":1,
                "manager_runtime_request":{"session_instance":instance}
            }),
            "manager_runtime",
        )?;
        self.verify_endpoint(&client, &reply)?;
        let status = &reply["manager_runtime"];
        let binding = &status["binding"];
        if reply["session_id"] != session
            || binding["session"] != session
            || binding["session_instance"] != instance
            || binding["conversation_id"] != self.conversation
            || binding["provider"] != self.cfg.text("manager_cli")
            || (status["native_cli"] == true) != (launch["manager"]["native_cli"] == true)
            || status["bridge_socket"] != launch["manager"]["bridge_socket"]
            || status["auth_file"] != launch["manager"]["auth_file"]
            || [
                "native_session_id",
                "native_generation",
                "observation_generation",
                "login_epoch",
            ]
            .iter()
            .any(|key| !valid(field(binding, key)))
            || !status["turn"]["external_input"].is_u64()
        {
            return Err(invalid(
                "manager runtime custody or provenance no longer matches",
            ));
        }
        Ok(status.clone())
    }

    pub(super) fn composer(
        &self,
        input: &Value,
        runtime: &Value,
        starting_prompt: &str,
    ) -> io::Result<Value> {
        let binding = &runtime["binding"];
        let actor = field(&input["author"], "id");
        let channel = field(&input["route"], "channel_id");
        if runtime["ready_for_input"] != true
            || runtime["provider_exhausted"] == true
            || runtime["turn"]["externally_steered"] == true
            || !matches!(field(&runtime["turn"], "state"), "idle" | "completed")
            || binding["conversation_id"] != self.conversation
            || input["supervisor_socket"] != self.cfg.text("daemon_socket")
            || if self.guild().is_empty() {
                input["authority"] != "owner_direct" || actor != self.cfg.text("owner_id")
            } else {
                input["authority"] != "team_participant" || !super::config::snowflake(actor)
            }
            || field(&input["route"], "guild_id") != self.guild()
            || channel != self.channel()
        {
            return Err(invalid(
                "manager or source is outside the current personal discussion grant",
            ));
        }
        let nonce = super::manager_nonce(
            &self.discussion,
            &input["operation_id"],
            &input["revision"],
            binding,
            &runtime["turn"]["external_input"],
        )?;
        let text = crate::manager_prompt::render_dispatch(
            starting_prompt,
            field(input, "text"),
            self.cfg.text("project_access") == "vm",
        )?;
        let mut composer = json!({
            "session": binding["session"], "session_instance": binding["session_instance"],
            "text": text,
            "manager_admission": {
                "binding": binding, "expected_external_input": runtime["turn"]["external_input"],
                "nonce":nonce,
            },
            "origin": {
                "kind":input["authority"], "installation_id":self.cfg.text("installation_id"),
                "request_id":input["operation_id"], "actor_id":actor, "requester_id":actor,
                "channel_id":channel, "message_id":input["route"]["message_id"],
                "discussion_id":self.discussion, "revision":input["revision"],
            }
        });
        if !self.guild().is_empty() {
            composer["origin"]["guild_id"] = json!(self.guild());
        }
        if input["voice_origin"].is_object() {
            composer["origin"] = input["voice_origin"].clone();
        }
        Ok(composer)
    }

    pub(super) fn send(&self, operation: &str, composer: &Value) -> io::Result<()> {
        let mut client = self.dial()?;
        if composer["manager_admission"]["binding"]["conversation_id"] != self.conversation {
            return Err(invalid("refusing unbound native manager input"));
        }
        let reply = client.request_body(
            json!({
                "op":"semantic_send", "session_id": composer["session"], "body_version":1,
                "operation_id":operation, "composer_send":composer
            }),
            "ok",
        )?;
        self.verify_endpoint(&client, &reply)?;
        if reply["operation_id"] != operation {
            return Err(invalid(
                "uncorrelated scoped manager admission acknowledgement",
            ));
        }
        Ok(())
    }

    pub(super) fn input_status(&self, composer: &Value) -> io::Result<Value> {
        let mut client = self.dial()?;
        let admission = &composer["manager_admission"];
        let reply = client.request_body(
            json!({
                "op":"manager_input_status", "session_id":composer["session"], "body_version":1,
                "manager_input_status_request":admission
            }),
            "manager_input_status",
        )?;
        self.verify_endpoint(&client, &reply)?;
        let turn = &reply["manager_input_status"];
        if reply["session_id"] != composer["session"]
            || turn["native_input_id"] != admission["nonce"]
            || !turn["external_input"].is_u64()
            || turn["externally_steered"] == true
        {
            return Err(invalid("uncorrelated manager native input observation"));
        }
        Ok(turn.clone())
    }

    pub(super) fn prove_publication(
        &self,
        composer: &Value,
        tool: &str,
        arguments_hash: &str,
        meta: &Value,
    ) -> io::Result<Value> {
        let mut client = self.dial()?;
        let admission = &composer["manager_admission"];
        let coordinates = super::provenance::coordinates(&admission["binding"], meta)?;
        let reply = client.request_body(
            json!({
                "op":"manager_resolve_call", "session_id":composer["session"], "body_version":1,
                "manager_call":{"binding":admission["binding"],"tool_name":tool,
                                "arguments_hash":arguments_hash,"host_meta":meta}
            }),
            "manager_resolve_call",
        )?;
        self.verify_endpoint(&client, &reply)?;
        let proof = &reply["manager_call_proof"];
        if reply["session_id"] != composer["session"]
            || proof["main"] != true
            || proof["native_input_id"] != admission["nonce"]
            || !valid(field(proof, "native_turn_id"))
            || proof["native_call_id"] != coordinates.call
            || coordinates
                .turn
                .is_some_and(|turn| proof["native_turn_id"] != turn)
        {
            return Err(invalid(
                "publication is not from the admitted native manager turn",
            ));
        }
        Ok(proof.clone())
    }

    pub(super) fn enrolled_read(&self, session: &str, call: &Value) -> io::Result<Value> {
        let mut caps = CAPS.to_vec();
        caps.push(super::enrolled_worker::CAP);
        let mut client = Client::dial(Path::new(self.cfg.text("daemon_socket")), &caps)?;
        self.cfg.check_supervisor(&client, &caps)?;
        let reply = client
            .request_body(
                json!({"op":"manager_worker_read","body_version":1,
            "session_id":session,"manager_worker_read":call}),
                "manager_worker_read",
            )
            .map_err(|_| invalid("enrolled native worker result is unavailable"))?;
        self.verify_endpoint(&client, &reply)?;
        let result = &reply["worker_result"];
        if reply["session_id"] != session
            || result["session"] != call["arguments"]["session"]
            || result["session_instance"] != call["arguments"]["session_instance"]
            || !matches!(
                field(result, "state"),
                "pending" | "completed" | "interrupted" | "failed"
            )
        {
            return Err(invalid("uncorrelated enrolled worker result"));
        }
        Ok(result.clone())
    }

    pub(super) fn enrolled_send(
        &self,
        session: &str,
        action: &Value,
        submit: bool,
    ) -> io::Result<Value> {
        let mut caps = CAPS.to_vec();
        caps.push(super::enrolled_worker::CAP);
        let mut client = Client::dial(Path::new(self.cfg.text("daemon_socket")), &caps)?;
        self.cfg.check_supervisor(&client, &caps)?;
        let operation = &action["operation_id"];
        let reply = client.exchange(
            json!({
                "op":"manager_worker_send","body_version":1,"session_id":session,
                "operation_id":operation,"manager_worker_send":action,"submit":submit
            }),
            "manager_worker_send",
            std::time::Duration::from_secs(16),
        )?;
        self.verify_endpoint(&client, &reply)?;
        let value = &reply["worker_send"];
        let args = &action["arguments"];
        if reply["session_id"] != session || reply["operation_id"] != *operation {
            return Err(invalid("uncorrelated enrolled worker send response"));
        }
        if value["state"] == "pending" {
            return Ok(value.clone());
        }
        if value["operation_id"] != *operation
            || value["session"] != args["session"]
            || value["session_instance"] != args["session_instance"]
            || value["agent"] != "codex"
            || value["origin"] != action["source_anchor"]["composer"]["origin"]
            || !crate::providers::codex_receipt::canonical_thread(field(value, "native_session_id"))
            || !matches!(
                field(value, "state"),
                "native_accepted" | "outcome_unknown" | "rejected"
            )
            || value["state"] == "native_accepted"
                && (!crate::providers::codex_receipt::canonical_thread(field(
                    value,
                    "native_turn_id",
                )) || value["native_input_id"]
                    != crate::providers::codex_receipt::hash(field(action, "operation_id")))
        {
            return Err(invalid(
                "uncorrelated native receipt for enrolled worker send",
            ));
        }
        Ok(value.clone())
    }

    pub(super) fn enrolled_send_target<'a>(
        &self,
        actions: &'a Value,
        args: &Value,
    ) -> io::Result<&'a Value> {
        let mut client = self.dial()?;
        let endpoint = client.endpoint().to_owned();
        let row = client
            .sessions()?
            .into_iter()
            .find(|row| {
                row["id"] == args["session"]
                    && row["endpoint_id"] == endpoint
                    && row["worker_discussion"] == self.conversation
            })
            .ok_or_else(|| invalid("missing enrolled Codex worker"))?;
        super::enrolled_worker::send_target(actions, &row, args)
            .ok_or_else(|| invalid("send target is not the exact enrolled Codex worker"))
    }

    pub(super) fn enrolled_roster(&self, actions: &Value, args: &Value) -> io::Result<Value> {
        let fields = args
            .as_object()
            .ok_or_else(|| invalid("invalid enrolled worker roster request"))?;
        if fields
            .keys()
            .any(|name| !matches!(name.as_str(), "limit" | "after_session_id"))
            || !args["after_session_id"].is_null()
                && !args["after_session_id"].as_str().is_some_and(valid)
            || !args["limit"].is_null() && !args["limit"].as_u64().is_some_and(|limit| limit <= 50)
        {
            return Err(invalid("invalid enrolled worker roster page"));
        }
        let limit = args["limit"]
            .as_u64()
            .filter(|limit| *limit > 0)
            .unwrap_or(50) as usize;
        let after = field(args, "after_session_id");
        let mut client = self.dial()?;
        let mut rows = client.sessions()?;
        rows.sort_by(|a, b| field(a, "id").cmp(field(b, "id")));
        let mut workers = Vec::new();
        let mut bytes = 2;
        let mut next = None;
        for row in rows {
            let Some(reference) = actions.as_object().and_then(|actions| {
                actions.values().find(|action| {
                    row["worker_attempt"] == action["operation_id"]
                        && row["agent"] == action["arguments"]["agent"]
                        && row["cwd"] == action["arguments"]["cwd"]
                })
            }) else {
                continue;
            };
            let id = field(&row, "id");
            if id <= after
                || row["endpoint_id"] != client.endpoint()
                || row["role"] != "worker"
                || row["worker_discussion"] != self.conversation
                || !valid(field(&row, "worker_instance"))
            {
                continue;
            }
            let worker = json!({"session":id,"session_instance":row["worker_instance"],"agent":row["agent"],
                    "name":row["name"],"cwd":row["cwd"],"group":row["group"],
                    "tracking":{"action_id":reference["key"],"job_id":reference["key"],"attempt_id":row["worker_attempt"]}});
            let size = serde_json::to_vec(&worker)?.len() + 1;
            if workers.len() >= limit || bytes + size > 8000 {
                next = workers
                    .last()
                    .map(|previous| field(previous, "session").to_owned());
                break;
            }
            bytes += size;
            workers.push(worker);
        }
        let mut page = json!({"workers":workers});
        if let Some(next) = next {
            page["next_session_id"] = json!(next);
        }
        Ok(page)
    }

    pub(super) fn enrolled_worker(
        &self,
        session: &str,
        action: &Value,
        launch: bool,
    ) -> io::Result<Value> {
        let id = field(action, "operation_id");
        if launch {
            let mut caps = CAPS.to_vec();
            caps.push(super::enrolled_worker::CAP);
            let mut client = Client::dial(Path::new(self.cfg.text("daemon_socket")), &caps)?;
            self.cfg.check_supervisor(&client, &caps)?;
            if let Ok(reply) = client.exchange(
                json!({"op":"manager_worker_action","body_version":1,
                "operation_id":id,"session_id":session,"manager_worker_action":action}),
                "launch",
                crate::ipc::launch_timeout(field(&action["arguments"], "agent")),
            ) {
                self.verify_endpoint(&client, &reply)?;
                if reply["operation_id"] != id {
                    return Err(invalid("uncorrelated enrolled worker response"));
                }
            }
        }
        let mut result = json!({"action_id":action["key"],"state":"outcome_unknown"});
        let status = self.launch_status(id)?;
        if status["state"] != "applied" {
            return Ok(result);
        }
        let id = &status["session_id"];
        let mut client = self.dial()?;
        let child = client
            .sessions()?
            .into_iter()
            .find(|row| {
                row["id"] == *id
                    && row["endpoint_id"] == client.endpoint()
                    && row["role"] == "worker"
                    && row["worker_discussion"] == self.conversation
                    && row["worker_attempt"] == action["operation_id"]
                    && row["agent"] == action["arguments"]["agent"]
                    && row["cwd"] == action["arguments"]["cwd"]
                    && valid(field(row, "worker_instance"))
            })
            .ok_or_else(|| invalid("enrolled worker receipt no longer matches its scope"))?;
        result["state"] = json!("created");
        result["session"] = child["id"].clone();
        result["session_instance"] = child["worker_instance"].clone();
        result["job_id"] = action["key"].clone();
        result["attempt_id"] = action["operation_id"].clone();
        Ok(result)
    }

    fn verify_endpoint(&self, client: &Client, reply: &Value) -> io::Result<()> {
        if reply["endpoint_id"] != client.endpoint() {
            return Err(invalid("uncorrelated historical supervisor response"));
        }
        Ok(())
    }
}

fn valid(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 256
        && value.trim() == value
        && !value.chars().any(char::is_control)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::fs::{self, File, OpenOptions};
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    use std::process::{Child, Command, Stdio};
    use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

    struct OwnedFixture {
        child: Child,
        root: std::path::PathBuf,
        socket: std::path::PathBuf,
    }

    impl Drop for OwnedFixture {
        fn drop(&mut self) {
            if let Ok(mut client) = Client::dial(&self.socket, &[])
                && let Ok(rows) = client.sessions()
            {
                for row in rows {
                    let id = field(&row, "id");
                    let _ = client.request("kill", Some(id), "ok");
                }
            }
            let _ = self.child.kill();
            let _ = self.child.wait();
            let _ = fs::remove_dir_all(&self.root);
        }
    }

    struct FixtureAudience {
        source: Value,
        posts: std::cell::RefCell<Vec<(String, String)>>,
        file_ack: std::cell::RefCell<Value>,
        forwarded_revocation: Option<std::path::PathBuf>,
        source_deletion: Option<std::path::PathBuf>,
    }

    impl super::super::dispatcher::Audience for FixtureAudience {
        fn current(&self, _cfg: &Config, id: &str) -> io::Result<Option<Value>> {
            if self
                .source_deletion
                .as_ref()
                .is_some_and(|marker| marker.is_file())
            {
                return Ok(None);
            }
            Ok((self.source["id"] == id).then(|| {
                let mut source = self.source.clone();
                if self
                    .forwarded_revocation
                    .as_ref()
                    .is_some_and(|marker| marker.is_file())
                {
                    source["message_snapshots"][0]["message"]["content"] =
                        json!("Changed forwarded content");
                }
                source
            }))
        }

        fn verify(&self, cfg: &Config) -> io::Result<()> {
            assert_eq!(cfg.text("dm_channel_id"), "400");
            Ok(())
        }

        fn post(
            &self,
            cfg: &Config,
            nonce: &str,
            text: &str,
            files: &[crate::supervisor::mail_files::Captured],
        ) -> super::super::rest::Post {
            self.posts.borrow_mut().push((nonce.into(), text.into()));
            let mut receipt = json!({
                "id":(401 + self.posts.borrow().len()).to_string(),
                "channel_id":cfg.text("dm_channel_id"),"author":{"id":"200","bot":true},
                "nonce":nonce,"content":text
            });
            if !files.is_empty() {
                assert_eq!(files.len(), 2);
                assert_eq!(files[0].data, b"source,result\nfixture,ok\n");
                assert_eq!(files[1].data, b"%PDF-1.7\n");
                receipt["attachments"] =
                    json!(files.iter().enumerate().map(|(i, file)| json!({
                    "id":format!("42{i}"),"filename":file.file.name,"size":file.file.size
                })).collect::<Vec<_>>());
                self.file_ack.replace(receipt);
                return super::super::rest::Post::Unconfirmed;
            }
            super::super::rest::Post::Acknowledged(receipt)
        }
    }

    #[test]
    #[ignore = "requires the guarded installed-Claude loopback fixture"]
    fn installed_claude_manager_producer() {
        let root = std::path::PathBuf::from(
            std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_ROOT").unwrap(),
        );
        let place = root.join("s/discord");
        let cfg = super::super::config::Place::for_supervisor(&root.join("s"))
            .unwrap()
            .load()
            .unwrap();
        let fault = std::env::var("SWARM_CLAUDE_MANAGER_FIXTURE_PROJECT_FAULT").unwrap_or_default();
        if fault == "before" {
            assert!(
                super::super::dispatcher::Dispatcher::open(&cfg, &place)
                    .unwrap()
                    .is_none()
            );
            let legacy = Client::dial(Path::new(cfg.text("daemon_socket")), CAPS).unwrap();
            assert!(!legacy.supports("manager-launch"));
            assert!(!root.join(".fixture-provider-executed").is_file());
            return;
        }
        let dispatcher = super::super::dispatcher::Dispatcher::open(&cfg, &place)
            .unwrap()
            .expect("Claude owner supervisor capability");
        let mut inbox = super::super::receipt::Inbox::open(&place, &cfg).unwrap();
        let forwarded = std::env::var("SWARM_CLAUDE_MANAGER_FIXTURE_FORWARDED").unwrap_or_default();
        let mut source = json!({"id":"401","channel_id":"400","author":{"id":"300","bot":false},
            "type":0,"content":"Use the approved publish tool with the bounded answer.","attachments":[]});
        if !forwarded.is_empty() {
            source["content"] = json!("");
            source["message_reference"] = json!({"type":1,"channel_id":"901","message_id":"991"});
            source["message_snapshots"] = json!([{"message":{"content":"Visible forwarded fixture quote","attachments":[{
                "id":"421","filename":"data.csv","size":8,
                "url":"https://cdn.discordapp.com/attachments/400/421/data.csv?sig=fixture"
            }]}}]);
            let files = super::super::received_files::fixture_capture_bytes(
                &place,
                &source,
                "400",
                b"a,b\n1,2\n",
            )
            .unwrap();
            assert_eq!(files[0]["forwarded"], true);
        }
        inbox.checkpoint("400").unwrap();
        let api = FixtureAudience {
            source: source.clone(),
            posts: Default::default(),
            file_ack: Default::default(),
            forwarded_revocation: (forwarded == "revoked")
                .then(|| root.join(".fixture-hide-forward")),
            source_deletion: (std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_DELETED").is_some()
                || std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_SEND_REVOKED").is_some()
                || std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_REPLACED_ISSUER").is_some())
            .then(|| root.join(".fixture-enrolled-deleted")),
        };
        let _send_guard = std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_CODEX_SEND").map(|_| {
            if std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_SEND_REVOKED").is_none() {
                dispatcher.lose_enrolled_ack.set(true);
            }
            let scope = dispatcher.scope(&cfg);
            let source = source.clone();
            let after_preflight = std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_SEND_REVOKED")
                .map(|_| root.join(".fixture-enrolled-deleted"));
            let service = super::super::enrolled_worker::authority::Service::bind(
                &scope,
                cfg.clone(),
                move |cfg, inbox, anchor| {
                    let api = FixtureAudience {
                        source: source.clone(),
                        posts: Default::default(),
                        file_ack: Default::default(),
                        forwarded_revocation: None,
                        source_deletion: after_preflight.clone(),
                    };
                    if let Some(marker) = &after_preflight {
                        std::fs::write(marker, b"late authority refusal")?;
                    }
                    super::super::dispatcher::enrolled_source(cfg, inbox, &api, anchor)
                },
            )
            .unwrap();
            dispatcher.send_issuer.replace(service.issuer().clone());
            service
        });
        let launch = dispatcher.scope(&cfg)["launch"].clone();
        let operation = format!("launch:{}:{:032x}", crate::ipc::spawn::now_ms(), 1);
        if cfg.text("project_access") == "vm" {
            let mut legacy = Client::dial(Path::new(cfg.text("daemon_socket")), CAPS).unwrap();
            let denied = legacy
                .request_body(
                    json!({"op":"owner_launch","operation_id":operation,
                "launch":launch}),
                    "launch",
                )
                .unwrap_err();
            assert!(denied.to_string().contains("manager-native-cli"));
            assert!(legacy.sessions().unwrap().is_empty());
        }
        inbox
            .set_manager(json!({"generation":1,"launch_operation":operation,"launch":launch}))
            .unwrap();
        let manager = Manager::new(&cfg).unwrap();
        let limit = std::time::Instant::now() + std::time::Duration::from_secs(15);
        let runtime = loop {
            dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
            let session = field(inbox.manager(), "session");
            let instance = field(&inbox.manager()["binding"], "session_instance");
            if let Ok(status) = manager.runtime(session, instance, &launch)
                && status["ready_for_input"] == true
            {
                break status;
            }
            assert!(
                std::time::Instant::now() < limit,
                "Claude native composer did not become ready"
            );
            std::thread::sleep(std::time::Duration::from_millis(50));
        };
        let session = field(&runtime["binding"], "session");
        let instance = field(&runtime["binding"], "session_instance");
        if std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_VM_REVOKED").is_some() {
            let local = session.rsplit('/').next().unwrap();
            let data = runtime::read_json(&root.join("s/rust-v1").join(local).join("session.json"))
                .unwrap();
            let scope = &data["manager"];
            assert!(crate::discord::verify_manager_vm_scope(scope, Some(&root.join("s"))).is_ok());
            fs::remove_file(place.join(".rust-vm-manager-grant-v1.json")).unwrap();
            assert!(crate::discord::verify_manager_vm_scope(scope, Some(&root.join("s"))).is_err());
            assert!(manager.runtime(session, instance, &launch).is_err());
            inbox.admit(&source).unwrap().unwrap();
            assert!(dispatcher.tick(&cfg, &mut inbox, &api).is_err());
            assert!(inbox.manager()["input"].is_null());
            assert!(api.posts.borrow().is_empty());
            return;
        }
        if fault == "after" {
            let config = root.join("work/.claude");
            runtime::private_dir(&config).unwrap();
            runtime::write_json(
                &config.join("settings.local.json"),
                &json!({"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3"}}),
            )
            .unwrap();
            assert!(manager.runtime(session, instance, &launch).is_err());
            inbox.admit(&source).unwrap().unwrap();
            assert!(dispatcher.tick(&cfg, &mut inbox, &api).is_err());
            assert!(inbox.manager()["input"].is_null());
            assert!(api.posts.borrow().is_empty());
            let local = session.rsplit('/').next().unwrap();
            let evidence = runtime::read_json(
                &root
                    .join("s/rust-v1")
                    .join(local)
                    .join("manager-proof.json"),
            )
            .unwrap();
            assert_eq!(evidence["evidence"]["observed_turn"], false);
            assert_eq!(evidence["evidence"]["prepared"], json!({}));
            return;
        }
        let mut terminal = Client::dial(Path::new(cfg.text("daemon_socket")), &["attach"]).unwrap();
        let lease = terminal.request("attach", Some(session), "lease").unwrap();
        let snapshot_len = lease["snapshot_len"].as_u64().unwrap() as usize;
        terminal.take_snapshot(snapshot_len).unwrap();
        assert_eq!(
            manager.runtime(session, instance, &launch).unwrap()["ready_for_input"],
            false
        );
        inbox.admit(&source).unwrap().unwrap();
        dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
        assert!(inbox.manager()["input"].is_null());
        drop(terminal);
        let credentials = root.join("h/.claude.json");
        let original = runtime::read_json(&credentials).unwrap();
        let mut changed = original.clone();
        changed["oauthAccount"]["organizationUuid"] = json!("different-org");
        runtime::write_json(&credentials, &changed).unwrap();
        assert!(manager.runtime(session, instance, &launch).is_err());
        runtime::write_json(&credentials, &original).unwrap();
        let limit = std::time::Instant::now() + std::time::Duration::from_secs(35);
        loop {
            dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
            if root.join(".fixture-enrolled-deleted-checked").is_file() {
                assert!(api.posts.borrow().is_empty());
                assert_eq!(
                    manager
                        .dial()
                        .unwrap()
                        .sessions()
                        .unwrap()
                        .iter()
                        .filter(|row| row["role"] == "worker")
                        .count(),
                    1
                );
                return;
            }
            if forwarded == "revoked" && root.join(".fixture-forwarded-checked").is_file() {
                assert!(api.posts.borrow().is_empty());
                return;
            }
            if !api.posts.borrow().is_empty() {
                break;
            }
            assert!(
                std::time::Instant::now() < limit,
                "Claude publication unavailable: {}, native {:?}",
                inbox.manager(),
                Manager::new(&cfg).unwrap().runtime(
                    field(inbox.manager(), "session"),
                    field(&inbox.manager()["binding"], "session_instance"),
                    &inbox.manager()["launch"]
                )
            );
            std::thread::sleep(std::time::Duration::from_millis(65));
        }
        let posts = api.posts.borrow();
        assert_eq!(posts.len(), 1);
        if _send_guard.is_some() {
            assert!(
                !dispatcher.lose_enrolled_ack.get(),
                "native receipt loss arm not exercised"
            );
        }
        assert_eq!(
            posts[0].1,
            if forwarded.is_empty() {
                "Bounded native Claude reply."
            } else {
                "Read the pinned forwarded file."
            }
        );
        let entry = inbox.publication(&posts[0].0);
        assert_eq!(entry["rest_acknowledged"], true);
        assert_eq!(entry["proof"]["binding"]["provider"], "claude");
        assert_eq!(
            entry["proof"]["native_call_id"],
            "toolu_fixture_swarm_publish"
        );
        drop(posts);
        if std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_ENROLLED").is_some() {
            assert_eq!(runtime["native_cli"], false);
            let rows = manager.dial().unwrap().sessions().unwrap();
            let workers: Vec<_> = rows.iter().filter(|row| row["role"] == "worker").collect();
            assert_eq!(workers.len(), 1);
            assert_eq!(workers[0]["cwd"], json!(root.join("approved")));
            let local = field(workers[0], "id").rsplit('/').next().unwrap();
            let metadata =
                runtime::read_json(&root.join("s/rust-v1").join(local).join("session.json"))
                    .unwrap();
            assert_eq!(metadata["mailbox"]["structured"], true);
        }
        if std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_VM").is_some() {
            assert_eq!(runtime["native_cli"], true);
            let worker = manager
                .dial()
                .unwrap()
                .sessions()
                .unwrap()
                .into_iter()
                .find(|row| row["role"] == "worker" && row["cwd"] == json!(root.join("child")))
                .unwrap();
            assert_eq!(
                worker["worker_discussion"],
                runtime["binding"]["conversation_id"]
            );
            assert!(
                worker["worker_attempt"]
                    .as_str()
                    .is_some_and(|attempt| attempt.starts_with("launch:"))
            );
            if std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_WORKTREE").is_some() {
                let binary = std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_BINARY").unwrap();
                let local = field(&worker, "id").rsplit('/').next().unwrap();
                let meta =
                    runtime::read_json(&root.join("s/rust-v1").join(local).join("session.json"))
                        .unwrap();
                let checkout = Path::new(field(&meta["checkout"], "path"));
                assert!(checkout.is_dir() && checkout != root.join("child"));
                let file = checkout.join("result.csv");
                fs::write(&file, "scope,result\nworker,ok\n").unwrap();
                let note = Command::new(binary)
                    .env(crate::teammate::SOCKET_ENV, cfg.text("daemon_socket"))
                    .env(
                        crate::teammate::PROOF_ENV,
                        crate::supervisor::mail::proof(field(&meta, "token"), "worker"),
                    )
                    .env("SWARM_SESSION_ID", local)
                    .args(["teammate", "message", "--kind", "complete", "--file"])
                    .arg(&file)
                    .output()
                    .unwrap();
                assert!(
                    note.status.success(),
                    "{}",
                    String::from_utf8_lossy(&note.stderr)
                );
                let note: Value = serde_json::from_slice(&note.stdout).unwrap();
                assert_eq!(note["files"][0]["name"], "result.csv");
                assert_eq!(note["files"][0]["size"], 23);
            }
        }

        if std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_PARK").is_some() {
            let binary = std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_BINARY").unwrap();
            let local = session.rsplit('/').next().unwrap();
            let state = root.join("s/rust-v1");
            let manager_meta = runtime::read_json(&state.join(local).join("session.json")).unwrap();
            let task = root.join("work/fixture-delegation.md");
            fs::write(
                &task,
                "Perform the bounded fixture task and await owner observation.",
            )
            .unwrap();
            // The synthetic owner establishes a prior association without granting
            // the restricted Claude manager an ambient VM shell credential.
            let launch = Command::new(&binary)
                .env("SWARM_SESSION_ID", local)
                .env(
                    "SWARM_DISPATCHER_SPAWN_PROOF",
                    crate::supervisor::mail::proof(field(&manager_meta, "token"), "spawn"),
                )
                .arg("spawn")
                .args(["--cli", "claude", "--worktree=false", "--delegate"])
                .arg(&task)
                .args(["--dir", cfg.text("work_dir")])
                .output()
                .unwrap();
            assert!(
                launch.status.success(),
                "{}",
                String::from_utf8_lossy(&launch.stderr)
            );
            let worker = String::from_utf8(launch.stdout).unwrap().trim().to_owned();
            let worker_local = worker.rsplit('/').next().unwrap();
            fs::write(root.join(".fixture-release-turn"), "").unwrap();
            let deadline = Instant::now() + Duration::from_secs(18);
            loop {
                dispatcher.fixture_idle_elapsed();
                dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
                let retire = &inbox.manager()["retirement"];
                if !retire.is_null() && manager.retirement(retire, true).unwrap() == "retired" {
                    break;
                }
                assert!(
                    Instant::now() < deadline,
                    "Claude did not park: {}, status {:?}",
                    inbox.manager(),
                    manager.runtime(session, instance, &inbox.manager()["launch"])
                );
                std::thread::sleep(Duration::from_millis(40));
            }
            assert!(
                manager
                    .dial()
                    .unwrap()
                    .sessions()
                    .unwrap()
                    .iter()
                    .any(|row| row["id"] == worker && row["status"]["Process"] == "running")
            );
            drop(dispatcher);
            let worker_meta =
                runtime::read_json(&state.join(worker_local).join("session.json")).unwrap();
            let note = Command::new(&binary)
                .env(crate::teammate::SOCKET_ENV, cfg.text("daemon_socket"))
                .env(
                    crate::teammate::PROOF_ENV,
                    crate::supervisor::mail::proof(field(&worker_meta, "token"), "worker"),
                )
                .env("SWARM_SESSION_ID", worker_local)
                .args([
                    "teammate",
                    "message",
                    "--kind",
                    "complete",
                    "--text",
                    "Fixture worker retained across parking.",
                ])
                .output()
                .unwrap();
            assert!(
                note.status.success(),
                "{}",
                String::from_utf8_lossy(&note.stderr)
            );
            let note: Value = serde_json::from_slice(&note.stdout).unwrap();
            assert_eq!(note["state"], "queued");
            let dispatcher = super::super::dispatcher::Dispatcher::open(&cfg, &place)
                .unwrap()
                .unwrap();
            let mut inbox = super::super::receipt::Inbox::open(&place, &cfg).unwrap();
            let deadline = Instant::now() + Duration::from_secs(20);
            while api.posts.borrow().len() < 2 {
                dispatcher.tick(&cfg, &mut inbox, &api).unwrap();
                assert!(
                    Instant::now() < deadline,
                    "Claude did not resume and publish its original worker's note: {}",
                    inbox.manager()
                );
                std::thread::sleep(Duration::from_millis(45));
            }
            let posts = api.posts.borrow();
            assert_eq!(posts.len(), 2);
            assert_eq!(posts[1].1, "Restored worker note reached Claude.");
            let reply = inbox.publication(&posts[1].0);
            assert_eq!(reply["rest_acknowledged"], true);
            assert_eq!(
                reply["proof"]["binding"]["native_session_id"],
                runtime["binding"]["native_session_id"]
            );
            assert_ne!(
                reply["proof"]["binding"]["session_instance"],
                runtime["binding"]["session_instance"]
            );
            assert_eq!(inbox.manager()["generation"], 2);
        }
    }

    fn dispatcher_input_routes(
        binary: &std::ffi::OsStr,
        fixture: &OwnedFixture,
        manager: &Manager,
        id: &str,
        instance: &str,
        rust: bool,
    ) {
        let cli = |args: &[&str]| {
            let output = Command::new(binary)
                .env_clear()
                .env("HOME", &fixture.root)
                .env(
                    "PATH",
                    format!("{}:/usr/bin:/bin", fixture.root.join("bin").display()),
                )
                .env("SWARM_DAEMON_STATE", fixture.root.join("state"))
                .env("SWARM_DAEMON_SOCK", &fixture.socket)
                .args(args)
                .output()
                .unwrap();
            (
                output.status.code().unwrap(),
                String::from_utf8(output.stdout).unwrap(),
                String::from_utf8(output.stderr).unwrap(),
            )
        };
        let state = fixture.root.join("state");
        let session_dir = if rust { state.join("rust-v1") } else { state };
        let local = id.split_once('/').unwrap().1;
        let marker = session_dir.join(local).join("direct-input-unresolved.json");
        let route = "use Discord or a bound worker note (swarm teammate message) to contact this dispatcher, or attach for manual input";
        for flags in [
            &["--text", "refused text"][..],
            &["--text", "refused draft", "--no-submit"],
        ] {
            let mut args = vec!["send", id];
            args.extend_from_slice(flags);
            assert_eq!(
                cli(&args),
                (1, String::new(), format!("send: send_input: {route}\n"))
            );
        }
        let operation = crate::providers::codex_receipt::operation().unwrap();
        let expected = (
            1,
            String::new(),
            format!(
                "message: operation_id={operation}: semantic message rejected without a new effect: composer_send: {route} (structured_unsupported)\n"
            ),
        );
        for _ in 0..2 {
            assert_eq!(
                cli(&[
                    "message",
                    id,
                    "--text",
                    "refused semantic input",
                    "--operation-id",
                    &operation
                ]),
                expected
            );
        }
        let status = cli(&["message", id, "--status", "--operation-id", &operation]);
        assert_eq!(status.0, 0, "{status:?}");
        assert_eq!(
            serde_json::from_str::<Value>(&status.1).unwrap(),
            json!({"operation_id":operation,"session":id,"session_instance":instance,"state":"rejected"})
        );
        let peek = cli(&["peek", id]);
        assert_eq!(peek.0, 0, "{peek:?}");
        assert!(!peek.1.contains("refused"));
        assert!(
            !marker.exists(),
            "refusal reached the terminal write boundary"
        );

        let ordinary = cli(&[
            "spawn",
            "--cli",
            "codex",
            "--dir",
            fixture.root.to_str().unwrap(),
            "--prompt",
            "ordinary warmup",
        ]);
        assert_eq!(ordinary.0, 0, "{ordinary:?}");
        let ordinary = manager
            .dial()
            .unwrap()
            .sessions()
            .unwrap()
            .into_iter()
            .find(|row| row["id"] != id && row["status"]["Process"] == "running")
            .expect("fixture spawned an ordinary session");
        let ordinary_id = field(&ordinary, "id");
        assert_eq!(
            cli(&["send", ordinary_id, "--text", "ordinary-terminal-text"]),
            (0, String::new(), String::new())
        );
        let wait_for_screen = |session, text| {
            let deadline = Instant::now() + Duration::from_secs(3);
            loop {
                let screen = cli(&["peek", session]);
                assert_eq!(screen.0, 0, "{screen:?}");
                if screen.1.contains(text) {
                    break;
                }
                assert!(
                    Instant::now() < deadline,
                    "terminal never showed {text:?}: {screen:?}"
                );
                std::thread::sleep(Duration::from_millis(40));
            }
        };
        wait_for_screen(ordinary_id, "ordinary-terminal-text");

        let mut attachment = Client::dial(&fixture.socket, &[]).unwrap();
        let lease = attachment.request("attach", Some(id), "lease").unwrap();
        attachment
            .take_snapshot(lease["snapshot_len"].as_u64().unwrap() as usize)
            .unwrap();
        let mut stream = attachment.stream();
        crate::ipc::wire::write_frame(&mut stream, runtime::DATA_IN, b"manual-dispatcher-text")
            .unwrap();
        wait_for_screen(id, "manual-dispatcher-text");
        assert_eq!(runtime::read_json(&marker).unwrap()["class"], "draft");
        assert_eq!(
            cli(&["send", id, "--key", "enter"]),
            (0, String::new(), String::new())
        );
        drop(attachment);
        assert_eq!(runtime::read_json(&marker).unwrap()["class"], "submitted");

        let mut owner = manager.dial().unwrap();
        let endpoint = owner.endpoint().to_owned();
        owner
            .send(
                &json!({"op":"manager_runtime","endpoint_id":endpoint,"session_id":id,
            "body_version":1,"manager_runtime_request":{"session_instance":instance}}),
            )
            .unwrap();
        let hold = owner
            .receive(Instant::now() + Duration::from_secs(2))
            .unwrap();
        assert_eq!(hold["op"], "error", "{hold}");
        // The older synthetic app-server sometimes loses native custody on the signed Go binary.
        // Go intentionally gives the native custody refusal precedence over the persisted hold.
        if rust || hold["error_code"] != "manager_custody_lost" {
            assert_eq!(
                hold["error_code"], "manager_direct_input_unresolved",
                "{hold}"
            );
        }
    }

    #[test]
    #[ignore = "requires an isolated Go or Rust supervisor and fake-Codex binaries; no network calls"]
    fn pinned_go_owner_protocol_and_scoped_launch() {
        let rust = std::env::var_os("SWARM_RUST_MANAGER_ORACLE");
        let go = rust
            .clone()
            .or_else(|| std::env::var_os("SWARM_GO_ORACLE"))
            .expect("set exact owner oracle binary");
        let codex = std::env::var_os("SWARM_FAKE_CODEX").expect("set exact fake-Codex binary");
        let root =
            crate::test_temp::root().join(format!("gdm-{}", &runtime::random_id().unwrap()[..10]));
        runtime::private_dir(&root).unwrap();
        let root = fs::canonicalize(root).unwrap();
        let bin = root.join("bin");
        runtime::private_dir(&bin).unwrap();
        let codex_fixture = bin.join("fixture");
        fs::copy(codex, &codex_fixture).unwrap();
        let mut wrapper = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o700)
            .open(bin.join("codex"))
            .unwrap();
        let cold_start = if rust.is_some() {
            "app-server) /bin/sleep 5 ;;"
        } else {
            ""
        };
        writeln!(wrapper, "#!/bin/sh\ncase \"$1\" in\n--version) echo 'codex-cli 0.167.0'; exit 0 ;;\n{cold_start}\nesac\nexec '{}/fixture' \"$@\"", bin.display()).unwrap();
        drop(wrapper);
        let state = root.join("state");
        let socket = root.join("daemon.sock");
        runtime::private_dir(&state).unwrap();
        let native_home = root.join(".codex");
        runtime::private_dir(&native_home).unwrap();
        runtime::write_json(
            &native_home.join("auth.json"),
            &json!({
                "auth_mode":"chatgpt", "tokens":{"account_id":"fixture-account"}
            }),
        )
        .unwrap();
        let enrollment = state.join("discord");
        runtime::private_dir(&enrollment).unwrap();
        runtime::write_json(&enrollment.join("config.json"), &json!({
            "version":1,"enabled":true,"mode":"personal","name":"fixture","installation_id":"fixture",
            "owner_id":"300","application_id":"200","dm_channel_id":"400","manager_cli":"codex",
            "daemon_socket":socket,"token_file":root.join("synthetic-token"),"work_dir":root
        })).unwrap();
        if rust.is_some() {
            runtime::write_json(
                &enrollment.join(".rust-offline-owner.json"),
                &json!("fixture"),
            )
            .unwrap();
        }
        let launch = Command::new(&go)
            .arg("daemon")
            .env_clear()
            .env("HOME", &root)
            .env("PATH", format!("{}:/usr/bin:/bin", bin.display()))
            .env("SWARM_DAEMON_STATE", &state)
            .env("SWARM_DAEMON_SOCK", &socket)
            .env("SWARM_DAEMON_LOCK", root.join("daemon.lock"))
            .env("SWARM_DAEMON_LOG", root.join("daemon.log"))
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(File::create(root.join("startup.log")).unwrap())
            .spawn()
            .unwrap();
        let _cleanup = OwnedFixture {
            child: launch,
            root: root.clone(),
            socket: socket.clone(),
        };
        let cfg = Config(
            json!({"mode":"personal","installation_id":"fixture","dm_channel_id":"400","owner_id":"300",
            "manager_cli":"codex","name":"fixture","application_id":"200","work_dir":root,"daemon_socket":socket,"supervisor":{"state_dir":state}}),
        );
        let manager = Manager::new(&cfg).unwrap();
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            if manager.verify_supervisor().is_ok() {
                break;
            }
            assert!(
                Instant::now() < deadline,
                "pinned Go owner endpoint did not negotiate manager capability: {:?}. {}",
                manager.verify_supervisor(),
                fs::read_to_string(root.join("startup.log")).unwrap_or_default()
            );
            std::thread::sleep(Duration::from_millis(25));
        }
        let bridge = enrollment.join("manager-v1");
        runtime::private_dir(&bridge).unwrap();
        let bridge_socket = bridge.join("bridge.sock");
        let auth = bridge.join("bridge.auth");
        let _listener = runtime::bind(&bridge_socket).unwrap();
        let mut credential = OpenOptions::new()
            .create_new(true)
            .write(true)
            .mode(0o600)
            .open(&auth)
            .unwrap();
        write!(credential, "{}", "a".repeat(64)).unwrap();
        let request = manager.launch_request(&bridge_socket, &auth);
        let operation = format!(
            "launch:{}:{:032x}",
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_millis(),
            1
        );
        assert_eq!(
            manager.launch_status(&operation).unwrap()["state"],
            "unknown_operation"
        );
        if rust.is_some() {
            assert!(manager.launch(&operation, &request).is_err());
            let mut inbox = super::super::receipt::Inbox::open(&enrollment, &cfg).unwrap();
            inbox
                .set_manager(json!({"generation":1,"launch":request,"launch_operation":operation}))
                .unwrap();
        }
        let session = manager.launch(&operation, &request).unwrap();
        let receipt = manager.launch_status(&operation).unwrap();
        assert_eq!(receipt["state"], "applied");
        assert_eq!(receipt["session_id"], session["id"]);
        if rust.is_some() {
            let duplicate = format!(
                "launch:{}:{:032x}",
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_millis(),
                3
            );
            let err = manager.launch(&duplicate, &request).unwrap_err();
            assert!(err.to_string().contains("persisted operation"));
        }
        let id = field(&session, "id");
        let deadline = Instant::now() + Duration::from_secs(7);
        let row = loop {
            if let Ok(row) = manager.session(id) {
                assert!(valid(field(&row["capabilities"], "session_instance")));
                break row;
            }
            assert!(
                Instant::now() < deadline,
                "pinned Go fixture did not expose a scoped running incarnation: {:?}, daemon log: {}",
                manager.dial().unwrap().sessions().unwrap(),
                fs::read_to_string(root.join("daemon.log")).unwrap_or_default()
            );
            std::thread::sleep(Duration::from_millis(100));
        };
        if std::env::var_os("SWARM_FAKE_CODEX_MANAGER_PROOF").is_some() {
            let instance = field(&row["capabilities"], "session_instance");
            let deadline = Instant::now() + Duration::from_secs(8);
            let native = loop {
                let status = manager.runtime(id, instance, &request);
                if let Ok(status) = &status
                    && status["ready_for_input"] == true
                {
                    break status.clone();
                }
                assert!(
                    Instant::now() < deadline,
                    "fixture did not produce native manager custody: {status:?}"
                );
                std::thread::sleep(Duration::from_millis(100));
            };
            let source = json!({"id":"401","type":0,"content":"go fixture input","attachments":[],
                "channel_id":"400","author":{"id":"300"}});
            let mut inbox = super::super::receipt::Inbox::open(&enrollment, &cfg).unwrap();
            inbox.checkpoint("400").unwrap();
            let admitted = inbox.admit(&source).unwrap().unwrap();
            let setting = "Check the real output.";
            crate::manager_prompt::save(&state, setting).unwrap();
            let starting_prompt =
                crate::manager_prompt::load(&manager.supervisor_root().unwrap()).unwrap();
            let composer = manager
                .composer(&admitted, &native, &starting_prompt)
                .unwrap();
            assert_eq!(starting_prompt, setting);
            assert_eq!(
                composer["text"],
                crate::manager_prompt::render(setting, field(&admitted, "text")).unwrap()
            );
            let launch_operation = operation.clone();
            let operation = format!(
                "semantic:{}:{:032x}",
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_millis(),
                2
            );
            if rust.is_some() {
                inbox.cutover("401",field(&admitted,"revision"),json!({
                    "generation":1,"launch":request,"launch_operation":launch_operation,"session":id,"binding":native["binding"],
                    "input":{"operation":operation,"source_id":"401","composer":composer,"starting_prompt":starting_prompt}
                })).unwrap();
            }
            if rust.is_some() {
                crate::manager_prompt::save(&state, "Inspect the next worker reply.").unwrap();
                let mut forged = composer.clone();
                forged["text"] = json!("fabricated Discord text");
                let err = manager.send(&operation, &forged).unwrap_err();
                assert!(err.to_string().contains("exact connector cutover"));
            }
            let park_fixture = std::env::var_os("SWARM_NATIVE_MAIL_PARK_FIXTURE").is_some();
            let wake_fixture =
                park_fixture || std::env::var_os("SWARM_NATIVE_MAIL_WAKE_FIXTURE").is_some();
            let publish_files_fixture =
                std::env::var_os("SWARM_NATIVE_MAIL_PUBLISH_FILES_FIXTURE").is_some();
            if publish_files_fixture {
                std::fs::write(root.join(".fixture-mail-publish-files"), "").unwrap();
            }
            if wake_fixture {
                std::fs::write(root.join(".fixture-mail-wake-enabled"), "").unwrap();
            }
            manager.send(&operation, &composer).unwrap();
            let deadline = Instant::now() + Duration::from_secs(5);
            let turn = loop {
                let evidence = manager.input_status(&composer);
                if let Ok(evidence) = &evidence
                    && evidence["state"] == "running"
                {
                    break evidence.clone();
                }
                assert!(
                    Instant::now() < deadline,
                    "fixture did not produce observed native input: {evidence:?}"
                );
                std::thread::sleep(Duration::from_millis(100));
            };
            if rust.is_some() {
                let mut altered = composer.clone();
                altered["origin"]["installation_id"] = json!("other-installation");
                assert!(manager.send(&operation, &altered).is_err());
                let mut unbound = composer.clone();
                unbound.as_object_mut().unwrap().shift_remove("origin");
                unbound
                    .as_object_mut()
                    .unwrap()
                    .shift_remove("manager_admission");
                let mut owner = manager.dial().unwrap();
                assert!(
                    owner
                        .request_body(
                            json!({"op":"semantic_send","body_version":1,"operation_id":operation,
                    "session_id":id,"composer_send":unbound}),
                            "ok"
                        )
                        .is_err()
                );
                assert!(
                    owner
                        .request_body(
                            json!({"op":"send_input","session_id":id,
                    "send_input":{"text":"raw fixture refusal"}}),
                            "ok"
                        )
                        .is_err()
                );
            }
            let session = &native["binding"]["native_session_id"];
            let host = json!({"callId":"fixture-native-call","threadId":session,"sessionId":session,
                "x-codex-turn-metadata":{"turn_id":turn["native_turn_id"],"thread_id":session,"session_id":session}});
            let mut args =
                json!({"output_id":"final","sequence":1,"text":"go-fixture text","final":true});
            args.sort_all_objects();
            let proof = manager
                .prove_publication(
                    &composer,
                    "publish",
                    &super::super::receipt::hash(&args).unwrap(),
                    &host,
                )
                .unwrap();
            assert_eq!(
                proof["native_input_id"],
                composer["manager_admission"]["nonce"]
            );
            assert_eq!(proof["native_call_id"], "fixture-native-call");
            if let Some(binary) = rust.as_ref() {
                drop(_listener);
                let dispatcher = super::super::dispatcher::Dispatcher::open(&cfg, &enrollment)
                    .unwrap()
                    .unwrap();
                let fake = FixtureAudience {
                    source,
                    posts: std::cell::RefCell::new(Vec::new()),
                    file_ack: Default::default(),
                    forwarded_revocation: None,
                    source_deletion: None,
                };
                let local = id.split_once('/').unwrap().1;
                let mut retained_worker = None;
                if std::env::var_os("SWARM_NATIVE_MAIL_FIXTURE").is_some() {
                    let owner =
                        runtime::read_json(&state.join("rust-v1").join(local).join("session.json"))
                            .unwrap();
                    let proof = crate::supervisor::mail::proof(field(&owner, "token"), "spawn");
                    let attempt = format!("launch:{}:{:032x}", crate::ipc::spawn::now_ms(), 47);
                    let document = root.join("fixture-delegate.md");
                    std::fs::write(
                        &document,
                        "# Fixture delegation\nUse your own worker note.\n",
                    )
                    .unwrap();
                    let launch_worker = || {
                        Command::new(binary)
                            .env_clear()
                            .env("HOME", &root)
                            .env("TMPDIR", &root)
                            .env("PATH", format!("{}:/usr/bin:/bin", bin.display()))
                            .env("SWARM_DAEMON_STATE", &state)
                            .env("SWARM_DAEMON_SOCK", &socket)
                            .env("SWARM_SESSION_ID", local)
                            .env("SWARM_DISPATCHER_SPAWN_PROOF", &proof)
                            .arg("spawn")
                            .arg("--cli")
                            .arg("codex")
                            .arg("--dir")
                            .arg(&root)
                            .args([
                                "--worktree=false",
                                "--delegate",
                                document.to_str().unwrap(),
                                "--operation-id",
                                &attempt,
                            ])
                            .output()
                            .unwrap()
                    };
                    let worker = launch_worker();
                    assert!(
                        worker.status.success(),
                        "worker launch: {}",
                        String::from_utf8_lossy(&worker.stderr)
                    );
                    let worker = String::from_utf8(worker.stdout).unwrap().trim().to_owned();
                    let replay = launch_worker();
                    assert!(replay.status.success());
                    assert_eq!(String::from_utf8(replay.stdout).unwrap().trim(), worker);
                    let worker_local = worker.split_once('/').unwrap().1;
                    let meta = runtime::read_json(
                        &state
                            .join("rust-v1")
                            .join(worker_local)
                            .join("session.json"),
                    )
                    .unwrap();
                    assert_eq!(meta["mailbox"]["attempt"], attempt);
                    assert_eq!(meta["mailbox"]["parent"], local);
                    let worker_proof =
                        crate::supervisor::mail::proof(field(&meta, "token"), "worker");
                    let cli = |args: &[&str], credential: &str| {
                        Command::new(binary)
                            .arg("teammate")
                            .args(args)
                            .env_clear()
                            .env(crate::teammate::SOCKET_ENV, &socket)
                            .env(crate::teammate::PROOF_ENV, credential)
                            .env("SWARM_SESSION_ID", worker_local)
                            .output()
                            .unwrap()
                    };
                    let decode = |out: std::process::Output| {
                        assert!(
                            out.status.success(),
                            "teammate: {}",
                            String::from_utf8_lossy(&out.stderr)
                        );
                        serde_json::from_slice::<Value>(&out.stdout).unwrap()
                    };
                    let submitted = decode(cli(
                        &["message", "--kind", "needs-input", "--text", "Which path?"],
                        &worker_proof,
                    ));
                    let note = field(&submitted, "id");
                    assert_eq!(submitted["state"], "queued");
                    assert!(
                        !cli(&["receipt", "--id", note], &"a".repeat(64))
                            .status
                            .success()
                    );
                    assert_eq!(
                        decode(cli(
                            &[
                                "message",
                                "--id",
                                note,
                                "--kind",
                                "needs-input",
                                "--text",
                                "Which path?"
                            ],
                            &worker_proof
                        )),
                        submitted
                    );
                    let mut invoke = |tool: &str, call_id: &str, arguments: Value| {
                        let mut meta = host.clone();
                        meta["callId"] = json!(call_id);
                        let mut proxy = Command::new(binary)
                            .arg("manager-mcp")
                            .arg(&bridge_socket)
                            .arg(&auth)
                            .arg(field(&native["binding"], "conversation_id"))
                            .arg(local)
                            .stdin(Stdio::piped())
                            .stdout(Stdio::piped())
                            .stderr(Stdio::null())
                            .spawn()
                            .unwrap();
                        writeln!(
                            proxy.stdin.take().unwrap(),
                            "{}",
                            json!({"jsonrpc":"2.0","id":1,"method":"tools/call",
                            "params":{"name":tool,"arguments":arguments,"_meta":meta}})
                        )
                        .unwrap();
                        let deadline = Instant::now() + Duration::from_secs(5);
                        while proxy.try_wait().unwrap().is_none() {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            assert!(Instant::now() < deadline, "mailbox tool did not answer");
                            std::thread::sleep(Duration::from_millis(10));
                        }
                        let result = decode(proxy.wait_with_output().unwrap());
                        assert!(result["result"]["isError"].is_null(), "{result}");
                        serde_json::from_str::<Value>(
                            result["result"]["content"][0]["text"].as_str().unwrap(),
                        )
                        .unwrap()
                    };
                    let listed = invoke("swarm_read_note", "fixture-mail-list", json!({}));
                    assert_eq!(listed["notes"][0]["id"], note);
                    let answer = invoke(
                        "swarm_reply",
                        "fixture-mail-reply",
                        json!({"text":"Use src"}),
                    );
                    assert_eq!(answer["id"], note);
                    assert_eq!(
                        decode(cli(&["receipt", "--id", note], &worker_proof))["state"],
                        "answered"
                    );
                    let readback = decode(cli(&["reply", "--id", note], &worker_proof));
                    assert_eq!(readback["text"], "Use src");
                    assert_eq!(
                        decode(cli(&["reply", "--id", note], &worker_proof)),
                        readback
                    );
                    if park_fixture {
                        retained_worker = Some((worker_local.to_owned(), worker_proof.clone()));
                    }
                    if !publish_files_fixture && !park_fixture {
                        let complete = decode(cli(
                            &["message", "--kind", "complete", "--text", "Finished"],
                            &worker_proof,
                        ));
                        let listed = invoke("swarm_read_note", "fixture-mail-list", json!({}));
                        assert_eq!(listed["notes"][0]["id"], complete["id"]);
                    }
                    if !park_fixture
                        && std::env::var_os("SWARM_NATIVE_MAIL_FILES_FIXTURE").is_some()
                    {
                        let csv = root.join("data.csv");
                        let pdf = root.join("report.pdf");
                        std::fs::write(&csv, "source,result\nfixture,ok\n").unwrap();
                        std::fs::write(&pdf, "%PDF-1.7\n").unwrap();
                        let receipt = decode(cli(
                            &[
                                "message",
                                "--kind",
                                "complete",
                                "--file",
                                csv.to_str().unwrap(),
                                "--file",
                                pdf.to_str().unwrap(),
                            ],
                            &worker_proof,
                        ));
                        let note = receipt["id"].as_str().unwrap();
                        assert_eq!(receipt["files"].as_array().unwrap().len(), 2);
                        std::fs::write(&csv, "edited after queue").unwrap();
                        assert_eq!(
                            decode(cli(&["receipt", "--id", note], &worker_proof)),
                            receipt
                        );
                        let listed = invoke("swarm_read_note", "fixture-mail-list", json!({}));
                        assert!(
                            listed["notes"]
                                .as_array()
                                .unwrap()
                                .iter()
                                .any(|entry| entry["id"] == note
                                    && entry["files"] == receipt["files"])
                        );
                    }
                }
                let mut proxy = Command::new(binary)
                    .arg("manager-mcp")
                    .arg(&bridge_socket)
                    .arg(&auth)
                    .arg(field(&native["binding"], "conversation_id"))
                    .arg(local)
                    .stdin(Stdio::piped())
                    .stdout(Stdio::piped())
                    .stderr(Stdio::null())
                    .spawn()
                    .unwrap();
                let mut to_proxy = proxy.stdin.take().unwrap();
                writeln!(
                    to_proxy,
                    "{}",
                    json!({"jsonrpc":"2.0","id":1,"method":"tools/call",
                    "params":{"name":"publish","arguments":args,"_meta":host}})
                )
                .unwrap();
                drop(to_proxy);
                let deadline = Instant::now() + Duration::from_secs(5);
                while fake.posts.borrow().is_empty() {
                    dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                    assert!(
                        Instant::now() < deadline,
                        "native tool never reached scoped publication"
                    );
                    std::thread::sleep(Duration::from_millis(10));
                }
                dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                let response = proxy.wait_with_output().unwrap();
                assert!(response.status.success());
                let receipt: Value = serde_json::from_slice(&response.stdout).unwrap();
                assert!(receipt["result"]["isError"].is_null());
                assert!(!String::from_utf8_lossy(&response.stdout).contains(&"a".repeat(64)));
                let posts = fake.posts.borrow();
                assert_eq!(posts.len(), 1);
                assert_eq!(posts[0].1, "go-fixture text");
                assert_eq!(inbox.publication(&posts[0].0)["rest_acknowledged"], true);
                drop(posts);
                if wake_fixture {
                    drop(dispatcher);
                    std::fs::write(root.join(".fixture-mail-complete"), "").unwrap();
                    let dispatcher = super::super::dispatcher::Dispatcher::open(&cfg, &enrollment)
                        .unwrap()
                        .unwrap();
                    let dispatcher = if park_fixture {
                        let old_id = field(inbox.manager(), "session").to_owned();
                        let deadline = Instant::now() + Duration::from_secs(8);
                        let unclaimed = loop {
                            dispatcher.fixture_idle_elapsed();
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            let retirement = &inbox.manager()["retirement"];
                            if !retirement.is_null() {
                                break retirement.clone();
                            }
                            assert!(Instant::now() < deadline, "no idle retirement checkpoint");
                            std::thread::sleep(Duration::from_millis(30));
                        };
                        let mut late = fake.source.clone();
                        late["id"] = json!("408");
                        late["content"] = json!("Ingress just before retirement");
                        inbox.admit(&late).unwrap().unwrap();
                        dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                        assert!(inbox.manager()["retirement"].is_null());
                        assert_eq!(manager.retirement(&unclaimed, true).unwrap(), "unknown");
                        assert!(manager.retirement(&unclaimed, false).is_err());
                        inbox.withdraw("408").unwrap();
                        let deadline = Instant::now() + Duration::from_secs(12);
                        loop {
                            dispatcher.fixture_idle_elapsed();
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            let retirement = &inbox.manager()["retirement"];
                            if !retirement.is_null()
                                && manager.retirement(retirement, true).unwrap() == "retired"
                            {
                                break;
                            }
                            assert!(
                                Instant::now() < deadline,
                                "manager did not prove native retirement: {}",
                                inbox.manager()
                            );
                            std::thread::sleep(Duration::from_millis(30));
                        }
                        let (worker, proof) = retained_worker.take().unwrap();
                        assert!(
                            manager
                                .dial()
                                .unwrap()
                                .sessions()
                                .unwrap()
                                .iter()
                                .any(|row| field(row, "id").ends_with(&format!("/{worker}"))
                                    && row["status"]["Process"] == "running")
                        );
                        let deadline = Instant::now() + Duration::from_secs(3);
                        while manager.session(&old_id).is_ok() {
                            assert!(
                                Instant::now() < deadline,
                                "supervisor did not project its confirmed manager exit"
                            );
                            std::thread::sleep(Duration::from_millis(30));
                        }
                        drop(dispatcher);
                        std::fs::write(root.join(".fixture-parked-manager"), "").unwrap();
                        let completion = Command::new(binary)
                            .env_clear()
                            .env(crate::teammate::SOCKET_ENV, &socket)
                            .env(crate::teammate::PROOF_ENV, proof)
                            .env("SWARM_SESSION_ID", &worker)
                            .args([
                                "teammate",
                                "message",
                                "--kind",
                                "complete",
                                "--text",
                                "Finished after manager parked",
                            ])
                            .output()
                            .unwrap();
                        assert!(
                            completion.status.success(),
                            "{}",
                            String::from_utf8_lossy(&completion.stderr)
                        );
                        inbox = super::super::receipt::Inbox::open(&enrollment, &cfg).unwrap();
                        let dispatcher =
                            super::super::dispatcher::Dispatcher::open(&cfg, &enrollment)
                                .unwrap()
                                .unwrap();
                        let deadline = Instant::now() + Duration::from_secs(14);
                        loop {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            if !inbox.manager()["binding"].is_null()
                                && inbox.manager()["generation"] == 2
                            {
                                break;
                            }
                            assert!(
                                Instant::now() < deadline,
                                "manager did not resume from parked predecessor"
                            );
                            std::thread::sleep(Duration::from_millis(30));
                        }
                        assert_ne!(inbox.manager()["session"], old_id);
                        assert_eq!(
                            inbox.manager()["binding"]["native_session_id"],
                            native["binding"]["native_session_id"]
                        );
                        assert_eq!(
                            std::fs::read_to_string(root.join(".fixture-resumed-thread")).unwrap(),
                            field(&native["binding"], "native_session_id")
                        );
                        dispatcher
                    } else {
                        dispatcher
                    };
                    let deadline = Instant::now() + Duration::from_secs(8);
                    let cancel = std::env::var_os("SWARM_NATIVE_MAIL_CANCEL_FIXTURE").is_some();
                    if cancel {
                        let prepared = std::env::var("SWARM_NATIVE_MAIL_CANCEL_FIXTURE").as_deref()
                            == Ok("prepared");
                        if prepared {
                            std::fs::write(root.join(".fixture-mail-hold-turn"), "").unwrap();
                        }
                        let lock = (!prepared).then(|| {
                            super::super::lock_manager_mail_effect(&dispatcher.scope(&cfg)).unwrap()
                        });
                        loop {
                            let attempt = dispatcher.tick(&cfg, &mut inbox, &fake);
                            if !inbox.manager()["input"]["worker_wake"].is_null() {
                                attempt.unwrap();
                                break;
                            }
                            attempt.unwrap();
                            assert!(
                                Instant::now() < deadline,
                                "did not reserve mailbox wake: {}, observed {:?}",
                                inbox.manager(),
                                manager.input_status(&composer)
                            );
                            std::thread::sleep(Duration::from_millis(20));
                        }
                        let refused = inbox.manager()["input"]["composer"].clone();
                        assert_eq!(
                            manager.input_status(&refused).unwrap()["state"],
                            if prepared { "prepared" } else { "unknown" }
                        );
                        let mut withdrawal = fake.source.clone();
                        match std::env::var("SWARM_NATIVE_MAIL_CANCEL_FIXTURE").as_deref() {
                            Ok("edited") => {
                                withdrawal["content"] = json!("Edited after cutover");
                                withdrawal["edited_timestamp"] = json!("2026-01-01T00:00:00Z");
                            }
                            Ok("empty") => withdrawal["content"] = json!(""),
                            Ok("attachment") => withdrawal["attachments"] = json!([{"id":"430"}]),
                            _ => withdrawal["id"] = json!("407"),
                        }
                        let withdrawn = FixtureAudience {
                            source: withdrawal,
                            posts: Default::default(),
                            file_ack: Default::default(),
                            forwarded_revocation: None,
                            source_deletion: None,
                        };
                        dispatcher.tick(&cfg, &mut inbox, &withdrawn).unwrap();
                        assert_eq!(inbox.manager()["input"]["composer"], refused);
                        drop(lock);
                        dispatcher.tick(&cfg, &mut inbox, &withdrawn).unwrap();
                        if prepared {
                            assert_eq!(inbox.manager()["input"]["composer"], refused);
                            assert_eq!(
                                manager.input_status(&refused).unwrap()["state"],
                                "prepared"
                            );
                        } else {
                            assert!(inbox.manager()["input"].is_null());
                            assert_eq!(manager.input_status(&refused).unwrap()["state"], "unknown");
                            let mut replacement = fake.source.clone();
                            replacement["id"] = json!("407");
                            replacement["content"] = json!("Independent DM after withdrawn source");
                            let other = FixtureAudience {
                                source: replacement.clone(),
                                posts: Default::default(),
                                file_ack: Default::default(),
                                forwarded_revocation: None,
                                source_deletion: None,
                            };
                            inbox.admit(&replacement).unwrap().unwrap();
                            dispatcher.tick(&cfg, &mut inbox, &other).unwrap();
                            assert_eq!(inbox.manager()["input"]["source_id"], "407");
                            assert!(inbox.manager()["input"]["worker_wake"].is_null());
                        }
                    } else {
                        let wake = loop {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            let wake = &inbox.manager()["input"];
                            if !wake["worker_wake"].is_null() && !wake["turn_id"].is_null() {
                                break wake.clone();
                            }
                            assert!(
                                Instant::now() < deadline,
                                "did not observe a mailbox wake: {}",
                                inbox.manager()
                            );
                            std::thread::sleep(Duration::from_millis(30));
                        };
                        assert_ne!(wake["turn_id"], turn["native_turn_id"]);
                        assert_eq!(wake["source_id"], "401");
                        assert_eq!(wake["starting_prompt"], "Inspect the next worker reply.");
                        assert!(field(&wake["composer"], "text").starts_with("Current owner settings (supersede earlier settings)\nInspect the next worker reply.\n\nCurrent input\n"));
                        let expected = if publish_files_fixture {
                            "Worker attached files"
                        } else {
                            "Finished"
                        };
                        assert!(field(&wake["composer"], "text").contains(expected));
                        for _ in 0..3 {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            assert_eq!(inbox.manager()["input"], wake);
                        }
                        let current_local = field(inbox.manager(), "session")
                            .rsplit('/')
                            .next()
                            .unwrap()
                            .to_owned();
                        let mut wake_host = host.clone();
                        wake_host["callId"] = json!("fixture-wake-read");
                        wake_host["x-codex-turn-metadata"]["turn_id"] = wake["turn_id"].clone();
                        let mut proxy = Command::new(binary)
                            .arg("manager-mcp")
                            .arg(&bridge_socket)
                            .arg(&auth)
                            .arg(field(&native["binding"], "conversation_id"))
                            .arg(&current_local)
                            .stdin(Stdio::piped())
                            .stdout(Stdio::piped())
                            .spawn()
                            .unwrap();
                        writeln!(
                            proxy.stdin.take().unwrap(),
                            "{}",
                            json!({"jsonrpc":"2.0","id":1,"method":"tools/call",
                        "params":{"name":"swarm_read_note","arguments":{},"_meta":wake_host}})
                        )
                        .unwrap();
                        let deadline = Instant::now() + Duration::from_secs(5);
                        while proxy.try_wait().unwrap().is_none() {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            assert!(Instant::now() < deadline, "wake note tool timed out");
                            std::thread::sleep(Duration::from_millis(15));
                        }
                        let response = proxy.wait_with_output().unwrap();
                        let result: Value = serde_json::from_slice(&response.stdout).unwrap();
                        assert!(result["result"]["isError"].is_null(), "{result}");
                        let selected: Value =
                            serde_json::from_str(field(&result["result"]["content"][0], "text"))
                                .unwrap();
                        assert_eq!(selected["id"], wake["worker_wake"]["id"]);
                        assert_eq!(selected["state"], "ingested");
                        if park_fixture {
                            let mut meta = host.clone();
                            meta["callId"] = json!("fixture-park-publish");
                            meta["x-codex-turn-metadata"]["turn_id"] = wake["turn_id"].clone();
                            let mut proxy = Command::new(binary)
                                .arg("manager-mcp")
                                .arg(&bridge_socket)
                                .arg(&auth)
                                .arg(field(&native["binding"], "conversation_id"))
                                .arg(&current_local)
                                .stdin(Stdio::piped())
                                .stdout(Stdio::piped())
                                .spawn()
                                .unwrap();
                            writeln!(proxy.stdin.take().unwrap(), "{}", json!({"jsonrpc":"2.0","id":1,"method":"tools/call",
                                "params":{"name":"publish","arguments":{"output_id":"park-final","sequence":1,
                                "text":"Worker finished after the manager parked","final":true},"_meta":meta}})).unwrap();
                            let deadline = Instant::now() + Duration::from_secs(5);
                            while fake.posts.borrow().len() < 2 {
                                dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                                assert!(
                                    Instant::now() < deadline,
                                    "post-park publication timed out"
                                );
                                std::thread::sleep(Duration::from_millis(15));
                            }
                            let response = proxy.wait_with_output().unwrap();
                            let result: Value = serde_json::from_slice(&response.stdout).unwrap();
                            assert!(result["result"]["isError"].is_null(), "{result}");
                            let posts = fake.posts.borrow();
                            assert_eq!(posts[1].1, "Worker finished after the manager parked");
                            assert_eq!(inbox.publication(&posts[1].0)["rest_acknowledged"], true);
                        }
                        if publish_files_fixture {
                            let ids: Vec<_> = selected["files"]
                                .as_array()
                                .unwrap()
                                .iter()
                                .map(|file| file["id"].clone())
                                .collect();
                            let mut meta = host.clone();
                            meta["callId"] = json!("fixture-wake-attachments");
                            meta["x-codex-turn-metadata"]["turn_id"] = wake["turn_id"].clone();
                            let mut proxy = Command::new(binary)
                                .arg("manager-mcp")
                                .arg(&bridge_socket)
                                .arg(&auth)
                                .arg(field(&native["binding"], "conversation_id"))
                                .arg(local)
                                .stdin(Stdio::piped())
                                .stdout(Stdio::piped())
                                .spawn()
                                .unwrap();
                            writeln!(proxy.stdin.take().unwrap(), "{}", json!({"jsonrpc":"2.0","id":1,"method":"tools/call",
                                "params":{"name":"publish","arguments":{"output_id":"file-final","sequence":1,
                                "text":"worker attachment fixture","final":true,"file_ids":ids},"_meta":meta}})).unwrap();
                            let deadline = Instant::now() + Duration::from_secs(5);
                            while fake.file_ack.borrow().is_null() {
                                dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                                assert!(Instant::now() < deadline, "file publication timed out");
                                std::thread::sleep(Duration::from_millis(15));
                            }
                            let response = proxy.wait_with_output().unwrap();
                            let result: Value = serde_json::from_slice(&response.stdout).unwrap();
                            assert!(result["result"]["isError"].is_null(), "{result}");
                            let ack = fake.file_ack.borrow();
                            let nonce = field(&ack, "nonce");
                            assert_eq!(inbox.publication(nonce)["state"], "uncertain");
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            let mut incomplete = ack.clone();
                            incomplete["attachments"].as_array_mut().unwrap().pop();
                            assert!(
                                !inbox
                                    .observe(
                                        &incomplete,
                                        super::super::receipt::Observation::Gateway
                                    )
                                    .unwrap()
                            );
                            assert!(
                                inbox
                                    .observe(&ack, super::super::receipt::Observation::Gateway)
                                    .unwrap()
                            );
                            assert_eq!(inbox.publication(nonce)["state"], "sent");
                        }
                    }
                    assert_eq!(
                        fake.posts.borrow().len(),
                        if publish_files_fixture || park_fixture {
                            2
                        } else {
                            1
                        }
                    );
                }
            }
            let mut stale = host;
            stale["x-codex-turn-metadata"]["turn_id"] = json!("old-turn");
            assert!(
                manager
                    .prove_publication(
                        &composer,
                        "publish",
                        &super::super::receipt::hash(&args).unwrap(),
                        &stale
                    )
                    .is_err()
            );
        }
        let mut different = request.clone();
        different["manager"]["conversation_id"] = json!("discord-teammate-different");
        assert!(manager.launch(&operation, &different).is_err());
        if std::env::var_os("SWARM_DISPATCHER_INPUT_FIXTURE").is_some() {
            dispatcher_input_routes(
                &go,
                &_cleanup,
                &manager,
                id,
                field(&row["capabilities"], "session_instance"),
                rust.is_some(),
            );
        }
    }
}
