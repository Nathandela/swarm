//! Completion evidence and the enrolled text-only quiet reply decision.
use super::*;
use crate::ipc::field;

const FIRST_CHECK_MS: u64 = 30_000;
const PROGRESS_CHECK_MS: u64 = 120_000;
const STALL_MS: u64 = 10 * 60_000;

impl Inbox {
    pub(in crate::discord) fn retire_completed_input(
        &mut self,
        mut manager: Value,
        observation: &Value,
        now: u64,
        publications: &[String],
        enrolled: bool,
    ) -> io::Result<()> {
        let _writer = self.lock_writer()?;
        if runtime::read_json(&self.file)? != self.data || self.manager() != &manager {
            return Err(invalid(
                "Discord completion no longer belongs to the active input",
            ));
        }
        let input = &manager["input"];
        let origin = &input["composer"]["origin"];
        let id = field(input, "source_id");
        let mut next = self.data.clone();
        if input["worker_wake"].is_null() && origin["voice"].is_null() && snowflake(id) {
            let admission = &input["composer"]["manager_admission"];
            let admitted_at = input["reply_admitted_at"]
                .as_u64()
                .filter(|at| *at > 0 && *at <= now);
            let actions = &input["worker_actions"];
            let covered = enrolled
                && (actions == &json!({})
                    || one_codex_launch(actions, id, origin, &observation["native_input_id"])
                        .is_some());
            let quiet = admitted_at
                .filter(|_| covered && !publications.is_empty() && input["final_nonce"].is_null())
                .map(|at| json!({"scope":"enrolled","admitted_at":at,"worker_actions":input["worker_actions"]}));

            let proof = json!({
                "binding":manager["binding"],"origin":origin,"observation":observation,
                "completed_at":now,"publications":publications,"quiet":quiet
            });
            let row = &next["messages"][id];
            if !row["native_completion"].is_null()
                || proof["observation"]["external_input"] != admission["expected_external_input"]
                || admission["binding"] != manager["binding"]
                || admission["nonce"] != row["native_input_id"]
                || input["turn_id"] != row["native_turn_id"]
                || !completion_matches(id, row, &proof, self.identity()[0])
            {
                return Err(invalid(
                    "completed native turn differs from the original Discord admission",
                ));
            }
            next["messages"][id]["native_completion"] = json!({
                "proof":proof,"digest":hash(&proof)?
            });
        }
        manager["input"] = Value::Null;
        next["manager"] = manager;
        self.write_locked(next)
    }

    fn next_quiet_reply(
        &self,
        conversation: &str,
        now: u64,
        after: Option<(u64, u64)>,
    ) -> Option<(u64, u64, &str)> {
        let due = self.data["messages"]
            .as_object()?
            .iter()
            .filter_map(|(id, row)| {
                let completion = &row["native_completion"];
                let proof = &completion["proof"];
                if !completion["quiet_at"].is_null()
                    || proof["binding"]["conversation_id"] != conversation
                    || row["state"] != "accepted"
                {
                    return None;
                }
                let first_due = quiet_first_due(proof)?;
                let due = match completion.get("progress_at") {
                    None => first_due,
                    Some(at) => at.as_u64()?.checked_add(PROGRESS_CHECK_MS)?,
                };
                (due <= now).then_some((due, id.parse::<u64>().ok()?, id.as_str()))
            });
        due.clone()
            .filter(|(at, id, _)| after.is_none_or(|after| (*at, *id) > after))
            .min()
            .or_else(|| due.min())
    }

    pub(in crate::discord) fn quiet_reply_due(&self, conversation: &str, now: u64) -> bool {
        self.next_quiet_reply(conversation, now, None).is_some()
    }

    pub(in crate::discord) fn keeps_manager_for_reply(&self, now: u64) -> bool {
        self.data["messages"]
            .as_object()
            .unwrap()
            .iter()
            .any(|(id, row)| {
                let completion = &row["native_completion"];
                let Ok((proof, _)) = reply_worker_anchor(&self.data, id) else {
                    return false;
                };
                let completed = proof["completed_at"].as_u64().unwrap_or(u64::MAX);
                let first_due = quiet_first_due(proof).unwrap_or(u64::MAX);
                let progress = match completion.get("progress_at") {
                    None => first_due.max(completed),
                    Some(at) => match at.as_u64() {
                        Some(at) if (first_due.max(completed)..=now).contains(&at) => at,
                        _ => return false,
                    },
                };
                completed <= now
                    && now < progress.saturating_add(STALL_MS)
                    && !self.final_reply_exists(id)
                    && self.confirmed_partials(id, proof)
                    && (completion["quiet_at"].is_null() || self.unverified_reply_pending_for(id))
            })
    }

    pub(in crate::discord) fn advance_completed_reply(
        &mut self,
        conversation: &str,
        now: u64,
        cursor: &mut Option<(u64, u64)>,
        verify_grant: impl FnOnce(&mut Self, &str, &Value) -> io::Result<()>,
        probe_worker: impl FnOnce(&str, &str, &Value) -> io::Result<Value>,
        clock: impl FnOnce() -> io::Result<u64>,
    ) -> io::Result<bool> {
        let Some((due, order, id)) = self.next_quiet_reply(conversation, now, *cursor) else {
            return Ok(false);
        };
        let id = id.to_owned();
        // A stale or unreadable source stays untouched and yields this probe to the next due row.
        *cursor = Some((due, order));
        let _writer = match self.lock_writer() {
            Ok(writer) => writer,
            Err(error) if error.kind() == io::ErrorKind::WouldBlock => return Ok(false),
            Err(error) => return Err(error),
        };
        if runtime::read_json(&self.file)? != self.data {
            return Ok(false);
        }
        let row = &self.data["messages"][&id];
        let receipt = &row["native_completion"];
        let proof = &receipt["proof"];
        let Some(completed_at) = proof["completed_at"]
            .as_u64()
            .filter(|at| *at > 0 && *at <= now)
        else {
            return Ok(false);
        };
        let progress = receipt.get("progress_at").and_then(Value::as_u64);
        if receipt["digest"] != hash(proof)?
            || !completion_matches(&id, row, proof, self.identity()[0])
            || proof["quiet"]["admitted_at"]
                .as_u64()
                .is_none_or(|at| at > completed_at)
            || progress.is_some_and(|time| {
                time < completed_at || quiet_first_due(proof).is_none_or(|due| time < due)
            })
            || !self.confirmed_partials(&id, proof)
        {
            return Ok(false);
        }
        let worker = if progress.is_some()
            && proof["quiet"]["worker_actions"] != json!({})
            && !self.final_reply_exists(&id)
        {
            let Ok((completion, action)) = reply_worker_anchor(&self.data, &id) else {
                return Ok(false);
            };
            match probe_worker(field(&completion["binding"], "session"), &id, action) {
                Ok(worker)
                    if valid_missing_worker(&worker)
                        && worker["attempt_id"] == action["operation_id"] =>
                {
                    Some(worker)
                }
                _ => return Ok(false),
            }
        } else {
            None
        };
        let revision = row["revision"].clone();
        if self
            .check_source_without_writing(|inbox| verify_grant(inbox, &id, &revision))
            .is_err()
        {
            return Ok(false);
        }
        let mut next = self.data.clone();
        let field = if progress.is_some() {
            "quiet_at"
        } else {
            "progress_at"
        };
        let verified_at = clock()?;
        if verified_at < now {
            return Ok(false);
        }
        next["messages"][&id]["native_completion"][field] = json!(verified_at);
        if let Some(worker) = worker {
            self.stage_unverified_reply(&mut next, &id, worker, verified_at)?;
        }
        self.write_locked(next)?;
        Ok(true)
    }

    #[cfg(test)]
    pub(in crate::discord) fn advance_quiet_reply(
        &mut self,
        conversation: &str,
        now: u64,
        cursor: &mut Option<(u64, u64)>,
        verify_grant: impl FnOnce(&mut Self, &str, &Value) -> io::Result<()>,
        clock: impl FnOnce() -> io::Result<u64>,
    ) -> io::Result<bool> {
        self.advance_completed_reply(
            conversation,
            now,
            cursor,
            verify_grant,
            |_, _, _| unreachable!(),
            clock,
        )
    }

    pub(super) fn confirmed_partials(&self, id: &str, proof: &Value) -> bool {
        let Some(outputs) = proof["publications"]
            .as_array()
            .filter(|outputs| !outputs.is_empty())
        else {
            return false;
        };
        let mut sequence = 0;
        for nonce in outputs {
            let Some(nonce) = nonce.as_str() else {
                return false;
            };
            let entry = &self.data["outbox"][nonce];
            let publication = &entry["proof"];
            let Some(next) = publication["arguments"]["sequence"]
                .as_u64()
                .filter(|next| *next > sequence)
            else {
                return false;
            };
            if verify_publication(nonce, entry).is_err()
                || publication["source_id"] != id
                || publication["binding"] != proof["binding"]
                || publication["turn_id"] != proof["observation"]["native_turn_id"]
                || publication["call_receipt"]["native_input_id"]
                    != proof["observation"]["native_input_id"]
                || publication["audience"] != self.data["team_discussion"]
                || !publication["voice_worker"].is_null()
                || publication["arguments"]["final"] != false
                || entry["state"] != "sent"
                || !entry["message_id"].as_str().is_some_and(snowflake)
                || !["gateway_seen", "history_seen", "rest_acknowledged"]
                    .iter()
                    .any(|kind| entry[*kind] == true)
            {
                return false;
            }
            sequence = next;
        }
        true
    }
}

fn quiet_first_due(proof: &Value) -> Option<u64> {
    let quiet = &proof["quiet"];
    if quiet["scope"] != "enrolled"
        || quiet["worker_actions"] != json!({})
            && one_codex_launch(
                &quiet["worker_actions"],
                field(&proof["origin"], "message_id"),
                &proof["origin"],
                &proof["observation"]["native_input_id"],
            )
            .is_none()
    {
        return None;
    }
    quiet["admitted_at"]
        .as_u64()
        .filter(|at| *at > 0)?
        .checked_add(FIRST_CHECK_MS)
}

fn one_codex_launch<'a>(
    actions: &'a Value,
    id: &str,
    origin: &Value,
    nonce: &Value,
) -> Option<&'a Value> {
    let rows = actions.as_object().filter(|rows| rows.len() == 1)?;
    let (key, action) = rows.iter().next()?;
    let args = &action["arguments"];
    let input = json!({"composer":{"manager_admission":{"nonce":nonce}}});
    (action["key"] == *key
        && action["kind"].is_null()
        && action["operation_id"]
            .as_str()
            .is_some_and(|id| !id.is_empty())
        && action["call"]["name"] == "swarm_create"
        && action["call"]["arguments"] == *args
        && args["agent"] == "codex"
        && args["worktree"] != true
        && action["source_anchor"] == json!({"source_id":id,"composer":{"origin":origin}})
        && crate::discord::enrolled_worker::key(&input, args)
            .is_ok_and(|expected| expected == *key))
    .then_some(action)
}

pub(crate) fn reply_worker_anchor<'a>(
    data: &'a Value,
    id: &str,
) -> io::Result<(&'a Value, &'a Value)> {
    let row = &data["messages"][id];
    let receipt = &row["native_completion"];
    let proof = &receipt["proof"];
    let manager = &data["manager"];
    let unqualified =
        || invalid("original enrolled worker reply lacks exact completed source coverage");
    let action = one_codex_launch(
        &proof["quiet"]["worker_actions"],
        id,
        &proof["origin"],
        &proof["observation"]["native_input_id"],
    )
    .ok_or_else(unqualified)?;
    if receipt["digest"] != hash(proof)?
        || quiet_first_due(proof).is_none()
        || !proof["origin"]["voice"].is_null()
        || proof["completed_at"].as_u64().is_none_or(|completed| {
            proof["quiet"]["admitted_at"]
                .as_u64()
                .is_none_or(|admitted| admitted > completed)
        })
        || !completion_matches(
            id,
            row,
            proof,
            data["identity"][0].as_str().unwrap_or_default(),
        )
        || proof["binding"] != manager["binding"]
        || manager["worker_actions"][field(action, "key")] != *action
        || manager["worker_actions"].as_object().is_none_or(|actions| {
            actions.values().any(|other| {
                other["kind"] == "send" && other["target"]["attempt"] == action["operation_id"]
            })
        })
    {
        return Err(unqualified());
    }
    Ok((proof, action))
}

fn completion_matches(id: &str, row: &Value, proof: &Value, installation: &str) -> bool {
    let origin = &proof["origin"];
    let observation = &proof["observation"];
    let binding = &proof["binding"];
    let discussion = field(origin, "discussion_id");
    row["state"] == "accepted"
        && origin["message_id"] == id
        && origin["installation_id"] == installation
        && origin["request_id"] == row["operation_id"]
        && origin["revision"] == row["revision"]
        && origin["kind"] == row["authority"]
        && matches!(
            origin["kind"].as_str(),
            Some("owner_direct" | "team_participant")
        )
        && origin["actor_id"].as_str().is_some_and(snowflake)
        && origin["actor_id"] == row["author"]["id"]
        && origin["requester_id"] == origin["actor_id"]
        && origin["channel_id"] == row["route"]["channel_id"]
        && field(origin, "guild_id") == field(&row["route"], "guild_id")
        && !discussion.is_empty()
        && binding["conversation_id"] == format!("discord-teammate-{discussion}")
        && *binding == row["native_binding"]
        && observation["state"] == "completed"
        && matches!(
            observation["externally_steered"],
            Value::Null | Value::Bool(false)
        )
        && observation["external_input"].is_u64()
        && observation["native_turn_id"] == row["native_turn_id"]
        && !field(observation, "native_turn_id").is_empty()
        && observation["native_input_id"] == row["native_input_id"]
        && crate::discord::manager_nonce(
            discussion,
            &origin["request_id"],
            &origin["revision"],
            binding,
            &observation["external_input"],
        )
        .is_ok_and(|nonce| observation["native_input_id"] == nonce)
}
