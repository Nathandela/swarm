use super::{Session, codex_receipt, field, runtime};
use crate::{providers::codex_proof, supervisor::manager};
use serde_json::{Value, json};
use std::io;
use std::path::Path;

impl Session {
    fn native_manager_proof(&self, session: &str, runtime_status: bool) -> io::Result<Value> {
        if self.manager.is_null() || !self.is_live() {
            return Err(refuse("session is not an active enrolled manager"));
        }
        let current = crate::discord::native_manager_enrollment(&self.manager["launch"])?;
        for key in [
            "launch",
            "installation_id",
            "owner_id",
            "channel_id",
            "guild_id",
            "supervisor_socket",
            "call_place",
            "projects",
            "claude_home",
            "project_authority",
        ] {
            if current[key] != self.manager[key] {
                return Err(refuse("manager enrollment is no longer current"));
            }
        }
        let bridge = &self.manager["launch"]["manager"];
        let credential =
            crate::manager_mcp::read_credential(Path::new(field(bridge, "auth_file")))?;
        if self.manager["credential_hash"] != codex_receipt::hash(&credential) {
            return Err(refuse("manager bridge custody was replaced"));
        }
        if self.agent == "claude" {
            crate::providers::claude::verify_manager_origin(
                &self.manager,
                self.verify_effective()?,
            )?;
            let reply = self.status_exchange(&json!({"type":"claude_manager_proof"}))?;
            let evidence = &reply["evidence"];
            let binding = &evidence["binding"];
            if reply["type"] != "ok"
                || evidence["blocked"] != false
                || binding["session"] != session
                || binding["provider"] != "claude"
                || binding["session_instance"]
                    != codex_receipt::provider_instance("claude", &self.token)
                || binding["conversation_id"] != bridge["conversation_id"]
                || binding["login_epoch"] != self.manager["account"]
            {
                return Err(refuse("Claude manager native custody changed"));
            }
            return Ok(evidence.clone());
        }
        if self.manager["account"]
            != manager::account(Path::new(field(&self.manager, "account_home")))?
        {
            return Err(refuse("manager account custody was replaced"));
        }
        self.dial()?;
        let owner = runtime::read_json(&self.dir.join("manager-binding.json"))?;
        let observed = runtime::read_json(&self.dir.join("observation.json"))?;
        let binding = &owner["binding"];
        let pid: u32 = owner["pid"]
            .as_u64()
            .and_then(|n| n.try_into().ok())
            .ok_or_else(|| refuse("manager backend custody is unknown"))?;
        if owner["token"] != self.token
            || observed["token"] != self.token
            || observed["backend_available"] != true
            || observed["conversation_id"] != binding["native_session_id"]
            || binding["session"] != session
            || binding["provider"] != "codex"
            || binding["session_instance"] != codex_receipt::instance(&self.token)
            || binding["conversation_id"] != bridge["conversation_id"]
            || binding["login_epoch"] != self.manager["account"]
            || !owner["birth"]
                .as_u64()
                .is_some_and(|birth| runtime::birth(pid).ok() == Some(birth))
        {
            return Err(refuse("manager backend custody changed"));
        }
        let mut evidence = if runtime_status {
            codex_proof::Ledger::runtime_snapshot(&self.dir, binding)?
        } else {
            codex_proof::Ledger::snapshot(&self.dir, binding)?
        };
        evidence["owned_ingress_ready"] = observed["manager_ingress_ready"].clone();
        evidence["observed_quota_turn"] = observed["manager_quota_turn"].clone();
        Ok(evidence)
    }

    pub(in crate::supervisor) fn manager_operation(&self, request: &Value) -> io::Result<Value> {
        if request["body_version"] != 1 {
            return Err(refuse("manager operation requires body version 1"));
        }
        let session = field(request, "session_id");
        let op = field(request, "op");
        let evidence = self.native_manager_proof(session, op == "manager_runtime")?;
        let binding = &evidence["binding"];
        let data = match op {
            "manager_runtime" => {
                if request["manager_runtime_request"]["session_instance"]
                    != binding["session_instance"]
                {
                    return Err(refuse("manager request names a stale instance"));
                }
                let turn = &evidence["turns"][field(&evidence, "current")];
                let nonce = field(turn, "nonce");
                let mut status = codex_proof::input_status(&evidence, nonce);
                if nonce.is_empty() {
                    status["state"] = json!(if evidence["thread_state"] == "idle" {
                        "idle"
                    } else {
                        "unknown"
                    });
                }
                let exhausted = codex_proof::provider_exhausted(&evidence);
                let ready = evidence["owned_ingress_ready"] == true
                    && evidence["system_error"] != true
                    && !exhausted
                    && evidence["thread_state"] == "idle"
                    && matches!(field(&status, "state"), "idle" | "completed");
                if exhausted {
                    status["state"] = json!("completed");
                    status["reason"] = json!("error");
                    status["native_turn_id"] = evidence["current"].clone();
                }
                let retirement_ready = if exhausted {
                    evidence["observed_quota_turn"] == status
                } else {
                    self.agent != "claude"
                        || field(&status, "state") == "completed"
                            && self.claude_park_resume_record(binding).is_ok()
                };
                let mut view = json!({"op":"manager_runtime","session_id":session,"manager_runtime":{
                    "binding":binding,"ready_for_input":ready,"turn":status,"retirement_ready":retirement_ready,
                    "native_cli":self.manager["launch"]["manager"]["native_cli"] == true,
                    "bridge_socket":self.manager["launch"]["manager"]["bridge_socket"],
                    "auth_file":self.manager["launch"]["manager"]["auth_file"]
                }});
                if exhausted {
                    view["manager_runtime"]["provider_exhausted"] = json!(true);
                }
                view
            }
            "manager_input_status" => {
                let input = &request["manager_input_status_request"];
                if input["binding"] != *binding {
                    return Err(refuse("stale native manager admission binding"));
                }
                json!({"op":"manager_input_status","session_id":session,
                    "manager_input_status":codex_proof::input_status(&evidence,field(input,"nonce"))})
            }
            "manager_resolve_call" => {
                let call = &request["manager_call"];
                let coordinates =
                    crate::discord::provenance::coordinates(binding, &call["host_meta"])?;
                if call["binding"] != *binding {
                    return Err(refuse("manager call names stale observation custody"));
                }
                let deadline = std::time::Instant::now() + std::time::Duration::from_secs(1);
                let mut observed = evidence;
                while observed["calls"][coordinates.call].is_null()
                    && std::time::Instant::now() < deadline
                {
                    std::thread::sleep(std::time::Duration::from_millis(15));
                    observed = self.native_manager_proof(session, false)?;
                }
                let result = codex_proof::resolve(&observed, call)?;
                json!({"op":"manager_resolve_call","session_id":session,"manager_call_proof":result})
            }
            _ => return Err(refuse("invalid manager provenance operation")),
        };
        Ok(data)
    }

    pub(in crate::supervisor) fn enrolled_tool_context(
        &self,
        request: &Value,
        call: &Value,
        tool: &str,
    ) -> io::Result<Value> {
        let session = field(request, "session_id");
        let data = crate::discord::connector_intent(&self.manager)?;
        let state = &data["manager"];
        let input = &state["input"];
        if self.manager["launch"]["manager"]["native_cli"] == true
            || request["body_version"] != 1
            || state["session"] != session
            || !input["turn_id"].is_string()
            || call["name"] != tool
        {
            return Err(refuse("enrolled worker tool lacks exact connector custody"));
        }
        let evidence = self.native_manager_proof(session, false)?;
        let mut arguments = call["arguments"].clone();
        arguments.sort_all_objects();
        let proof = codex_proof::resolve(
            &evidence,
            &json!({
                "binding":input["composer"]["manager_admission"]["binding"],"tool_name":tool,
                "arguments_hash":crate::discord::enrolled_worker::arguments_hash(&arguments)?,
                "host_meta":call["meta"]
            }),
        )?;
        if proof["native_turn_id"] != input["turn_id"]
            || proof["native_input_id"] != input["composer"]["manager_admission"]["nonce"]
        {
            return Err(refuse(
                "worker operation is outside the current enrolled native admission",
            ));
        }
        Ok(state.clone())
    }

    pub(in crate::supervisor) fn enrolled_worker_action(
        &self,
        request: &Value,
    ) -> io::Result<(Value, Value)> {
        let action = &request["manager_worker_action"];
        let call = &action["call"];
        let state = self.enrolled_tool_context(request, call, "swarm_create")?;
        let input = &state["input"];
        if action["operation_id"] != request["operation_id"]
            || !input["worker_wake"].is_null()
            || input["worker_actions"][field(action, "key")] != *action
            || call["arguments"] != action["arguments"]
            || action["source_anchor"]
                != json!({"source_id":input["source_id"],
                "composer":{"origin":input["composer"]["origin"]}})
        {
            return Err(refuse(
                "enrolled worker action does not match its persisted launch intent",
            ));
        }
        crate::discord::enrolled_worker::launch(&self.manager, input, action)
    }

    pub(in crate::supervisor) fn enrolled_worker_send_context(
        &self,
        request: &Value,
    ) -> io::Result<Value> {
        let action = &request["manager_worker_send"];
        let session = field(request, "session_id");
        let data = crate::discord::connector_intent(&self.manager)?;
        let state = &data["manager"];
        let arguments = &action["arguments"];
        crate::discord::enrolled_worker::validate_send(arguments)?;
        if request["body_version"] != 1
            || self.manager["launch"]["manager"]["native_cli"] == true
            || state["session"] != session
            || action["kind"] != "send"
            || action["call"]["name"] != "swarm_send"
            || action["call"]["arguments"] != *arguments
            || action["binding"] != state["binding"]
            || action["state"] != "executing"
            || !codex_receipt::timely(field(action, "operation_id"))
            || state["worker_actions"][field(action, "key")] != *action
        {
            return Err(refuse(
                "worker send lacks exact persisted native authorization",
            ));
        }
        let evidence = self.native_manager_proof(session, false)?;
        crate::discord::enrolled_worker::verify_send_evidence(&evidence, action)?;
        Ok(self.manager.clone())
    }

    pub(in crate::supervisor) fn enrolled_worker_send(
        &self,
        state: &crate::supervisor::State,
        grant: &Value,
        action: &Value,
        submit: bool,
    ) -> io::Result<Value> {
        let args = &action["arguments"];
        let session = field(args, "session");
        let operation = field(action, "operation_id");
        let instance = codex_receipt::instance(&self.token);
        if self.agent != "codex"
            || self.mailbox["structured"] != true
            || self.mailbox["discussion"] != action["binding"]["conversation_id"]
            || self.mailbox["attempt"] != action["target"]["attempt"]
            || self.mailbox["project"] != action["target"]["project"]
            || args["session_instance"] != instance
        {
            return Err(refuse("worker send target or attempt changed"));
        }
        self.verify_effective()?;
        if let Some(receipt) = self.semantic_status(operation, session, &state.root)? {
            return Ok(receipt);
        }
        if !submit
            || self.semantic_target().ok().as_deref() != Some(&instance)
            || !self.confirm_idle_observation().unwrap_or(false)
        {
            return Ok(json!({"state":"pending"}));
        }
        let text = field(args, "text");
        codex_receipt::bind_intent(
            &state.root,
            operation,
            session,
            &instance,
            &crate::discord::enrolled_worker::send_intent(action)?,
        )?;
        let mut shim = self.dial()?;
        shim.set_read_timeout(Some(std::time::Duration::from_secs(14)))?;
        runtime::write_control(
            &mut shim,
            &json!({
                "type":"semantic","operation_id":operation,"session":session,
                "session_instance":instance,"text":text,"composer_send":{"session":session,"session_instance":instance,"text":text},
                "enrolled_worker_send":{"scope":grant,"action":action}
            }),
        )?;
        let reply = runtime::read_control(&mut shim)?;
        if reply["type"] == "semantic_result" {
            return self
                .semantic_status(operation, session, &state.root)?
                .ok_or_else(|| refuse("worker send receipt unavailable"));
        }
        Ok(json!({"state":"pending"}))
    }

    pub(in crate::supervisor) fn enrolled_worker_result(
        &self,
        state: &Value,
        args: &Value,
    ) -> io::Result<Value> {
        let instance = codex_receipt::provider_instance(&self.agent, &self.token);
        let actions = &state["worker_actions"];
        let child = &self.mailbox;
        let budget = args["limit"].as_u64().unwrap_or(2048);
        let grant = actions.as_object().and_then(|actions| {
            actions.iter().find(|(_, action)| {
                action["operation_id"] == child["attempt"]
                    && action["arguments"]["cwd"] == child["project"]
                    && action["arguments"]["agent"] == self.agent
            })
        });
        if !matches!(self.agent.as_str(), "claude" | "codex")
            || child["structured"] != true
            || args["session_instance"] != instance
            || child["discussion"] != state["binding"]["conversation_id"]
            || grant.is_none_or(|(job, _)| {
                !field(args, "job_id").is_empty() && args["job_id"] != *job
                    || !field(args, "attempt_id").is_empty()
                        && args["attempt_id"] != child["attempt"]
            })
            || args.as_object().is_none_or(|args| {
                args.iter().any(|(key, val)| match key.as_str() {
                    "session" | "session_instance" => !val.is_string(),
                    "limit" => !val.is_u64(),
                    "source" => val != "" && val != "native",
                    "job_id" | "attempt_id" | "native_turn_id" => !val.is_string(),
                    _ => true,
                })
            })
            || !(1..=4096).contains(&budget)
        {
            return Err(refuse("no exact enrolled worker result grant"));
        }
        let cwd = self.verify_effective()?;
        let (job, action) = grant.unwrap();
        let anchor = &action["source_anchor"];
        if !anchor["source_id"].is_string() || !anchor["composer"]["origin"].is_object() {
            return Err(refuse(
                "original enrolled worker task source is unavailable",
            ));
        }
        if self.agent == "codex" {
            let profile =
                runtime::read_json(&self.dir.join("codex-profile.json")).map_err(|error| {
                    io::Error::other(format!("read Codex enrolled worker profile: {error}"))
                })?;
            let stored = &profile["profile"];
            let current = crate::providers::codex_continuity::profile(
                &runtime::launch_env(&Value::Null),
                cwd,
                &stored["options"],
            )?;
            if profile["token"] != self.token || *stored != current {
                return Err(refuse("Codex enrolled worker profile changed"));
            }
            let thread = self.semantic_thread()?;
            let wanted = field(args, "native_turn_id");
            let send = actions.as_object().and_then(|actions| {
                actions.values().find(|send| {
                    !wanted.is_empty()
                        && send["kind"] == "send"
                        && send["state"] == "native_accepted"
                        && send["target"]["job"] == *job
                        && send["target"]["attempt"] == child["attempt"]
                        && send["arguments"]["session"] == args["session"]
                        && send["arguments"]["session_instance"] == instance
                        && send["receipt"]["operation_id"] == send["operation_id"]
                        && send["receipt"]["session"] == args["session"]
                        && send["receipt"]["session_instance"] == instance
                        && send["receipt"]["native_session_id"] == thread
                        && send["receipt"]["native_turn_id"] == wanted
                        && send["receipt"]["state"] == "native_accepted"
                        && send["receipt"]["origin"] == send["source_anchor"]["composer"]["origin"]
                        && send["receipt"]["native_input_id"]
                            == codex_receipt::hash(field(send, "operation_id"))
                })
            });
            let (prompt, native_target) = match send {
                Some(send) => (
                    codex_receipt::hash(field(&send["arguments"], "text")),
                    Some((wanted, field(&send["receipt"], "native_input_id"))),
                ),
                None => (field(child, "prompt_hash").to_owned(), None),
            };
            let evidence = if thread.is_empty() {
                None
            } else {
                crate::providers::codex_continuity::worker_turn(
                    Path::new(field(stored, "native_home")),
                    cwd,
                    &thread,
                    &prompt,
                    native_target,
                    budget as usize,
                )
                .map_err(|error| io::Error::other(format!("read Codex worker rollout: {error}")))?
            };
            if !wanted.is_empty()
                && send.is_none()
                && evidence.as_ref().is_none_or(|row| row["turn_id"] != wanted)
            {
                return Err(refuse(
                    "native turn is outside the enrolled worker's admitted intent",
                ));
            }
            if self.semantic_thread()? != thread {
                return Err(refuse("Codex worker thread changed during the read"));
            }
            return Ok(
                json!({"source":"authorized_worker_turn", "session":args["session"],
                "session_instance":instance, "job_id":job, "attempt_id":child["attempt"],
                "state":evidence.as_ref().map_or("pending",|row| field(row,"state")),
                "evidence":evidence,"source_anchor":anchor,
                "send_source_anchor":send.map(|send| &send["source_anchor"])}),
            );
        }
        if !field(args, "native_turn_id").is_empty() {
            return Err(refuse(
                "Claude worker does not expose a native turn identifier",
            ));
        }
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        let saved = &meta["claude_profile"];
        let home = Path::new(field(saved, "home"));
        let profile = crate::providers::claude::profile(
            &runtime::launch_env(&Value::Null),
            &Value::Null,
            &saved["options"],
        )?;
        if meta["token"] != self.token
            || ["home", "home_id", "origin", "settings"]
                .iter()
                .any(|key| profile[*key] != saved[*key])
        {
            return Err(refuse("Claude enrolled worker profile changed"));
        }
        let mut result = match self.claude_conversation() {
            Ok(id) => {
                self.claude_project_authority(home, cwd)?;
                crate::providers::claude::worker_result::read(
                    home,
                    cwd,
                    &id,
                    &child["prompt_hash"],
                    budget as usize,
                )?
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => json!({"state":"pending"}),
            Err(error) => return Err(error),
        };
        result["session"] = args["session"].clone();
        result["session_instance"] = json!(instance);
        result["attempt_id"] = child["attempt"].clone();
        result["job_id"] = json!(job);
        result["source_anchor"] = anchor.clone();
        Ok(result)
    }

    pub(super) fn check_manager_input(
        &self,
        session: &str,
        operation: &str,
        body: &Value,
    ) -> io::Result<()> {
        if self.manager.is_null() {
            if !body["manager_admission"].is_null() || !body["origin"].is_null() {
                return Err(refuse("ordinary session cannot receive manager authority"));
            }
            return Ok(());
        }
        let native = self.native_manager_proof(session, false)?;
        let admission = &body["manager_admission"];
        let origin = &body["origin"];
        let discussion = field(&native["binding"], "conversation_id")
            .strip_prefix("discord-teammate-")
            .unwrap_or("");
        let expected_nonce = crate::discord::manager_nonce(
            discussion,
            &origin["request_id"],
            &origin["revision"],
            &native["binding"],
            &native["external_input"],
        )?;
        if admission["binding"] != native["binding"]
            || admission["nonce"] != expected_nonce
            || admission["expected_external_input"] != native["external_input"]
            || origin["installation_id"] != self.manager["installation_id"]
            || origin["discussion_id"] != discussion
            || origin["channel_id"] != self.manager["channel_id"]
            || if field(&self.manager, "guild_id").is_empty() {
                origin["kind"] != "owner_direct"
                    || origin["actor_id"] != self.manager["owner_id"]
                    || origin["requester_id"] != self.manager["owner_id"]
                    || !field(origin, "guild_id").is_empty()
            } else {
                origin["kind"] != "team_participant"
                    || origin["actor_id"] != origin["requester_id"]
                    || origin["guild_id"] != self.manager["guild_id"]
            }
            || field(origin, "message_id").is_empty()
                && origin["voice"].is_null()
                && origin["worker_wake"].is_null()
            || field(origin, "revision").is_empty()
            || field(origin, "request_id").is_empty()
        {
            return Err(refuse(
                "semantic input does not match the exact personal manager authority",
            ));
        }
        crate::discord::verify_connector_input(
            &self.manager,
            operation,
            body,
            self.dir.parent().and_then(Path::parent),
        )
    }
}

fn refuse(detail: &str) -> io::Error {
    io::Error::other(detail)
}
