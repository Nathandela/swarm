use super::{Session, codex_receipt, field, runtime};
use crate::{providers::codex_proof, supervisor::manager};
use serde_json::{Value, json};
use std::io;
use std::path::Path;

impl Session {
    fn native_manager_proof(&self, session: &str) -> io::Result<Value> {
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
        ] {
            if current[key] != self.manager[key] {
                return Err(refuse("manager enrollment is no longer current"));
            }
        }
        let bridge = &self.manager["launch"]["manager"];
        let credential =
            crate::manager_mcp::read_credential(Path::new(field(bridge, "auth_file")))?;
        if self.manager["credential_hash"] != codex_receipt::hash(&credential)
            || self.manager["account"]
                != manager::account(Path::new(field(&self.manager, "account_home")))?
        {
            return Err(refuse("manager bridge or account custody was replaced"));
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
        let mut evidence = codex_proof::Ledger::snapshot(&self.dir, binding)?;
        evidence["owned_ingress_ready"] = observed["manager_ingress_ready"].clone();
        Ok(evidence)
    }

    pub(in crate::supervisor) fn manager_operation(&self, request: &Value) -> io::Result<Value> {
        if request["body_version"] != 1 {
            return Err(refuse("manager operation requires body version 1"));
        }
        let session = field(request, "session_id");
        let evidence = self.native_manager_proof(session)?;
        let binding = &evidence["binding"];
        let data = match field(request, "op") {
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
                let ready = evidence["owned_ingress_ready"] == true
                    && evidence["thread_state"] == "idle"
                    && matches!(field(&status, "state"), "idle" | "completed");
                json!({"op":"manager_runtime","session_id":session,"manager_runtime":{
                    "binding":binding,"ready_for_input":ready,"turn":status,
                    "bridge_socket":self.manager["launch"]["manager"]["bridge_socket"],
                    "auth_file":self.manager["launch"]["manager"]["auth_file"]
                }})
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
                    observed = self.native_manager_proof(session)?;
                }
                let result = codex_proof::resolve(&observed, call)?;
                json!({"op":"manager_resolve_call","session_id":session,"manager_call_proof":result})
            }
            _ => return Err(refuse("invalid manager provenance operation")),
        };
        Ok(data)
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
        let native = self.native_manager_proof(session)?;
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
