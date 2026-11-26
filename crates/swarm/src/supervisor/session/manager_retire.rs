use super::{Session, field, runtime};
use crate::providers::{self, codex_receipt};
use serde_json::{Value, json};
use std::io;
use std::path::Path;

impl Session {
    fn manager_retirement_record(&self) -> io::Result<Option<Value>> {
        match runtime::read_json(&self.dir.join("manager-retire.json")) {
            Ok(record) if record["token"] == self.token && record["request"].is_object() => {
                Ok(Some(record))
            }
            Ok(_) => Err(io::Error::other("damaged manager retirement receipt")),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(error) => Err(error),
        }
    }

    pub(in crate::supervisor) fn manager_parked(&self, request: &Value) -> io::Result<bool> {
        Ok(self
            .manager_retirement_record()?
            .is_some_and(|record| record["request"] == *request)
            && self.verified_exit().is_some())
    }

    pub(in crate::supervisor) fn claude_park_resume_record(
        &self,
        binding: &Value,
    ) -> io::Result<Value> {
        let cwd = self.verify_effective()?;
        let home = Path::new(field(&self.manager, "account_home"));
        let id = self.claude_conversation()?;
        if self.agent != "claude"
            || binding["provider"] != "claude"
            || binding["session_instance"]
                != codex_receipt::provider_instance("claude", &self.token)
            || binding["native_session_id"] != id
            || binding["login_epoch"] != self.manager["account"]
            || self.manager["account"] != providers::claude::manager_account(home)?
        {
            return Err(io::Error::other(
                "Claude parking transcript has another native identity",
            ));
        }
        let authority = self.claude_project_authority(home, cwd)?;
        let (_, transcript) = providers::claude::transcript(home, cwd, &id)?;
        Ok(
            json!({"conversation_id":id,"transcript":transcript,"project_authority":authority,
            "resumed_from":binding["session"]}),
        )
    }

    pub(in crate::supervisor) fn manager_retire(&self, input: &Value) -> io::Result<Value> {
        let session = field(input, "session_id");
        let request = &input["manager_retire_request"];
        let id = field(input, "operation_id");
        let op = field(input, "op");
        if self.manager.is_null()
            || !matches!(self.agent.as_str(), "codex" | "claude")
            || input["body_version"] != 1
            || request["binding"]["session"] != session
            || request["binding"]["session_instance"]
                != codex_receipt::provider_instance(&self.agent, &self.token)
            || request["binding"]["conversation_id"]
                != self.manager["launch"]["manager"]["conversation_id"]
            || !request["replacement"].is_null()
            || !matches!(
                field(&request["expected_turn"], "state"),
                "idle" | "completed"
            )
            || id != crate::supervisor::manager::retirement_id(request)?
        {
            return Err(io::Error::other(
                "invalid or stale manager retirement request",
            ));
        }
        let reply = |state| {
            json!({"op":op,"operation_id":id,"session_id":session,
            "manager_retire":{"operation_id":id,"state":state}})
        };
        let _effect = (op != "manager_retire_status")
            .then(|| crate::discord::lock_manager_mail_effect(&self.manager))
            .transpose()?;
        let prior = self.manager_retirement_record()?;
        if prior
            .as_ref()
            .is_some_and(|record| record["request"] != *request)
        {
            return Err(io::Error::other(
                "a different retirement already owns this manager",
            ));
        }
        if prior.is_some() && self.verified_exit().is_some() {
            return Ok(reply("retired"));
        }
        if op == "manager_retire_status" {
            return Ok(reply(if prior.is_some() {
                "uncertain"
            } else {
                "unknown"
            }));
        }
        let intent = crate::discord::connector_intent(&self.manager)?;
        if intent["manager"]["session"] != session
            || intent["manager"]["generation"] != request["generation"]
            || !intent["manager"]["input"].is_null()
            || intent["manager"]["retirement"]["request"] != *request
            || intent["manager"]["retirement"]["operation_id"] != id
        {
            return Err(io::Error::other(
                "retirement is outside persisted connector custody",
            ));
        }
        let status = self
            .manager_operation(&json!({"op":"manager_runtime","session_id":session,
            "body_version":1,"manager_runtime_request":{"session_instance":codex_receipt::provider_instance(&self.agent,&self.token)}}))?;
        let status = &status["manager_runtime"];
        if status["ready_for_input"] != true
            || status["binding"] != request["binding"]
            || status["turn"] != request["expected_turn"]
            || self.agent == "claude" && status["retirement_ready"] != true
        {
            return Err(io::Error::other("manager or turn is no longer quiescent"));
        }
        let response = self.status_exchange(&json!({"type":"manager_park","retire":request}))?;
        if response["type"] != "ok" {
            return Err(io::Error::other(
                "manager shim did not prove quiescent retirement",
            ));
        }
        Ok(reply("prepared"))
    }
}
