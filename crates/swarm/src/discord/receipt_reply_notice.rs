//! A durable policy notice backed by a completed enrolled text admission.
use super::*;
use crate::ipc::field;

pub(in crate::discord) const UNVERIFIED_REPLY: &str =
    "I don’t have a confirmed final answer for this yet. Ask me here for a status update.";
const WINDOW_MS: u64 = 30 * 60_000;

pub(in crate::discord) fn valid_missing_worker(worker: &Value) -> bool {
    worker.as_object().is_some_and(|fields| fields.len() == 5)
        && matches!(field(worker, "state"), "interrupted" | "failed")
        && ["session", "session_instance", "attempt_id"]
            .into_iter()
            .all(|key| {
                worker[key].as_str().is_some_and(|s| {
                    !s.is_empty() && s.len() <= 256 && !s.chars().any(char::is_control)
                })
            })
        && crate::providers::codex_receipt::canonical_thread(field(worker, "native_turn_id"))
}

fn nonce(proof: &Value) -> io::Result<String> {
    Ok(hash(&json!([
        "unverified_reply",
        proof["installation_id"],
        proof["conversation_id"],
        proof["source_id"],
        proof["revision"]
    ]))?[..24]
        .to_owned())
}

pub(super) fn verify_notice(key: &str, entry: &Value) -> io::Result<()> {
    let proof = &entry["proof"];
    let observed = ["gateway_seen", "history_seen", "rest_acknowledged"]
        .into_iter()
        .any(|kind| entry[kind] == true);
    let valid_state = match field(entry, "state") {
        "prepared" => {
            entry["message_id"].is_null()
                && !observed
                && (entry["retry_at"].is_null() || entry["retry_at"].is_u64())
        }
        "sending" | "uncertain" => {
            entry["message_id"].is_null() && !observed && entry["retry_at"].is_null()
        }
        "sent" => snowflake(field(entry, "message_id")) && observed && entry["retry_at"].is_null(),
        _ => false,
    };
    if proof.as_object().is_none_or(|fields| fields.len() != 10)
        || proof["kind"] != "unverified_reply"
        || !valid_state
        || proof["body"] != UNVERIFIED_REPLY
        || entry["body"] != UNVERIFIED_REPLY
        || !snowflake(field(proof, "source_id"))
        || ![
            "installation_id",
            "conversation_id",
            "revision",
            "completion_digest",
        ]
        .into_iter()
        .all(|key| {
            proof[key].as_str().is_some_and(|s| {
                !s.is_empty() && s.len() <= 256 && !s.chars().any(char::is_control)
            })
        })
        || !proof["completed_at"].as_u64().is_some_and(|at| at > 0)
        || !(proof["audience"].is_null() || proof["audience"].is_object())
        || !valid_missing_worker(&proof["worker"])
        || entry["proof_digest"] != hash(proof)?
        || nonce(proof)? != key
    {
        return Err(invalid(
            "unverified reply differs from its original policy receipt",
        ));
    }
    Ok(())
}

impl Inbox {
    pub(super) fn final_reply_exists(&self, source: &str) -> bool {
        self.data["outbox"]
            .as_object()
            .unwrap()
            .values()
            .any(|entry| {
                entry["proof"]["source_id"] == source
                    && entry["proof"]["arguments"]["final"] == true
            })
    }

    pub(super) fn stage_unverified_reply(
        &self,
        next: &mut Value,
        source: &str,
        worker: Value,
        now: u64,
    ) -> io::Result<()> {
        let row = &self.data["messages"][source];
        let completion = &row["native_completion"];
        let proof = json!({"kind":"unverified_reply","installation_id":self.identity()[0],
            "conversation_id":completion["proof"]["binding"]["conversation_id"],
            "source_id":source,"revision":row["revision"],"completion_digest":completion["digest"],
            "completed_at":completion["proof"]["completed_at"],"audience":self.data["team_discussion"],
            "worker":worker,"body":UNVERIFIED_REPLY});
        let key = nonce(&proof)?;
        let rows = next["outbox"].as_object_mut().unwrap();
        if rows.contains_key(&key)
            || rows.values().any(|entry| {
                let saved = &entry["proof"];
                saved["kind"] == "unverified_reply"
                    && saved["conversation_id"] == proof["conversation_id"]
                    && saved["completed_at"]
                        .as_u64()
                        .is_some_and(|at| now.saturating_sub(at) < WINDOW_MS)
            })
        {
            return Ok(());
        }
        if rows.len() >= MAX_RECORDS {
            return Err(invalid("publication ledger reached its capacity"));
        }
        let entry = json!({"proof":proof,"proof_digest":hash(&proof)?,"state":"prepared","body":UNVERIFIED_REPLY});
        verify_notice(&key, &entry)?;
        rows.insert(key, entry);
        Ok(())
    }

    pub(in crate::discord) fn pending_unverified_reply(
        &self,
        now: u64,
        cursor: &mut Option<(u64, u128)>,
    ) -> Option<(String, Value)> {
        let rows = self.data["outbox"]
            .as_object()?
            .iter()
            .filter_map(|(nonce, row)| {
                let due = row["proof"]["kind"] == "unverified_reply"
                    && row["state"] == "prepared"
                    && row["retry_at"].as_u64().unwrap_or_default() <= now;
                if !due {
                    return None;
                }
                let order = (
                    row["proof"]["completed_at"].as_u64()?,
                    u128::from_str_radix(nonce, 16).ok()?,
                );
                Some((order, nonce, row))
            });
        let after = *cursor;
        let (order, nonce, row) = rows
            .clone()
            .filter(|(order, _, _)| after.is_none_or(|after| *order > after))
            .min_by_key(|(order, _, _)| *order)
            .or_else(|| rows.min_by_key(|(order, _, _)| *order))?;
        *cursor = Some(order);
        Some((nonce.clone(), row.clone()))
    }

    pub(super) fn unverified_reply_pending_for(&self, source: &str) -> bool {
        self.data["outbox"]
            .as_object()
            .unwrap()
            .values()
            .any(|entry| {
                entry["state"] == "prepared"
                    && entry["proof"]["kind"] == "unverified_reply"
                    && entry["proof"]["source_id"] == source
            })
    }

    pub(in crate::discord) fn unverified_reply_authority(
        &self,
        receipt: &Value,
    ) -> io::Result<(&Value, &Value)> {
        let notice = &receipt["proof"];
        let source = field(notice, "source_id");
        let (proof, action) = reply_worker_anchor(&self.data, source)?;
        if notice["installation_id"] != self.identity()[0]
            || notice["conversation_id"] != proof["binding"]["conversation_id"]
            || notice["revision"] != proof["origin"]["revision"]
            || notice["completed_at"] != proof["completed_at"]
            || notice["completion_digest"]
                != self.data["messages"][source]["native_completion"]["digest"]
            || notice["audience"] != self.data["team_discussion"]
            || notice["worker"]["attempt_id"] != action["operation_id"]
            || !self.confirmed_partials(source, proof)
            || self.final_reply_exists(source)
        {
            return Err(invalid(
                "unverified reply is outside its current completed source",
            ));
        }
        Ok((proof, action))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const CONVERSATION: &str = "discord-teammate-reply";

    fn accept_partial(inbox: &mut Inbox, id: &str, admitted: u64) -> Value {
        let mut next = inbox.data.clone();
        let (_, mut partial) = fixture_publication("On it.");
        let binding = json!({"provider":"codex","native_session_id":"fixture-thread",
            "session":"ep/manager","conversation_id":CONVERSATION});
        let origin = json!({"message_id":id,"installation_id":"fixture","request_id":format!("request-{id}"),
            "revision":format!("revision-{id}"),"actor_id":"300","requester_id":"300","kind":"owner_direct","channel_id":"400",
            "discussion_id":"reply"});
        let actual_nonce = crate::discord::manager_nonce(
            CONVERSATION,
            &origin["request_id"],
            &origin["revision"],
            &binding,
            &json!(0),
        )
        .unwrap();
        let turn = format!("turn-{id}");
        let call = format!("call-{id}");
        partial["source_id"] = json!(id);
        partial["binding"] = binding.clone();
        partial["turn_id"] = json!(turn);
        partial["native_call_id"] = json!(call);
        partial["host_meta"]["callId"] = json!(call);
        partial["host_meta"]["x-codex-turn-metadata"]["turn_id"] = json!(turn);
        partial["arguments"]["final"] = json!(false);
        partial["call_receipt"]["native_input_id"] = json!(actual_nonce);
        partial["call_receipt"]["native_turn_id"] = json!(turn);
        partial["call_receipt"]["native_call_id"] = json!(call);
        partial["output_id"] = json!(format!("partial-{id}"));
        partial["arguments"]["output_id"] = partial["output_id"].clone();
        partial["arguments_hash"] = json!(hash(&partial["arguments"]).unwrap());
        let publication = publication_nonce(&partial).unwrap();
        next["outbox"][&publication] = json!({"proof":partial,"proof_digest":hash(&partial).unwrap(),
            "state":"sent","body":"On it.","message_id":"401","rest_acknowledged":true});
        next["messages"][id] = json!({"state":"accepted","authority":"owner_direct","author":{"id":"300"},
            "operation_id":origin["request_id"],"revision":origin["revision"],"route":{"channel_id":"400"},
            "native_binding":binding,"native_input_id":actual_nonce,"native_turn_id":turn});
        let args = json!({"action_id":"start","agent":"codex","cwd":"/tmp/fixture","prompt":"Run the bounded job."});
        let composer = json!({"origin":origin,"manager_admission":{"binding":binding,"nonce":actual_nonce,"expected_external_input":0}});
        let key =
            crate::discord::enrolled_worker::key(&json!({"composer":composer}), &args).unwrap();
        let action = json!({"key":key,"operation_id":format!("launch:{admitted}:{id}"),"arguments":args,
            "call":{"name":"swarm_create","arguments":args},"source_anchor":{"source_id":id,"composer":{"origin":origin}}});
        next["manager"]["session"] = binding["session"].clone();
        next["manager"]["binding"] = binding.clone();
        next["manager"]["worker_actions"][&key] = action.clone();
        next["manager"]["input"] = json!({"source_id":id,"reply_admitted_at":admitted,"turn_id":turn,
            "composer":composer,"worker_actions":{}});
        next["manager"]["input"]["worker_actions"][&key] = action.clone();
        inbox.save(next).unwrap();
        inbox
            .retire_completed_input(
                inbox.manager().clone(),
                &json!({"state":"completed","native_turn_id":turn,
            "native_input_id":actual_nonce,"external_input":0}),
                admitted + 10,
                &[publication],
                true,
            )
            .unwrap();
        json!({"state":"interrupted","session":"ep/child","session_instance":"child-incarnation",
            "attempt_id":action["operation_id"],"native_turn_id":"11111111-2222-4333-8444-555555555555"})
    }

    fn advance(inbox: &mut Inbox, now: u64, proof: Option<&Value>) -> bool {
        inbox
            .advance_completed_reply(
                CONVERSATION,
                now,
                &mut None,
                |_, _, _| Ok(()),
                |_, _, action| {
                    let proof = proof.ok_or_else(|| invalid("worker proof unavailable"))?;
                    assert_eq!(proof["attempt_id"], action["operation_id"]);
                    Ok(proof.clone())
                },
                || Ok(now),
            )
            .unwrap()
    }

    #[test]
    fn missing_worker_notice_uses_confirmed_progress_clock_window_and_one_receipt() {
        let dir = crate::test_temp::root().join(format!("reply-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"installation_id":"fixture","application_id":"200","owner_id":"300","dm_channel_id":"400"}),
        );
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let admitted = 1000;
        let worker = accept_partial(&mut inbox, "101", admitted);
        let initial = inbox.data.clone();
        assert!(!advance(&mut inbox, admitted + 29_999, None));
        assert_eq!(inbox.data, initial);
        assert!(advance(&mut inbox, admitted + 30_000, None));
        let progress = inbox.data.clone();
        assert!(inbox.keeps_manager_for_reply(admitted + 120_000));
        assert!(!advance(&mut inbox, admitted + 149_999, Some(&worker)));
        assert!(!advance(&mut inbox, admitted + 150_000, None));
        assert_eq!(inbox.data, progress);
        assert!(!inbox.keeps_manager_for_reply(admitted + 630_000));
        let mut stale = Inbox::open(&dir, &cfg).unwrap();
        assert!(advance(&mut inbox, admitted + 150_000, Some(&worker)));
        assert!(!advance(&mut stale, admitted + 150_000, Some(&worker)));
        let (nonce, entry) = inbox
            .pending_unverified_reply(admitted + 150_000, &mut None)
            .unwrap();
        assert!(
            inbox
                .claim_publication(&nonce, &inbox.manager().clone(), &entry, |_| Ok(()))
                .is_ok()
        );
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        assert!(!inbox.keeps_manager_for_reply(admitted + 150_000));
        assert!(
            inbox
                .pending_unverified_reply(u64::MAX, &mut None)
                .is_none()
        );
        let mut echo = json!({"id":"402","channel_id":"401","author":{"id":"200","bot":true},"nonce":nonce,"content":UNVERIFIED_REPLY});
        assert!(!inbox.observe(&echo, Observation::Gateway).unwrap());
        echo["channel_id"] = json!("400");
        assert!(inbox.observe(&echo, Observation::Gateway).unwrap());
        assert!(!inbox.observe(&echo, Observation::Gateway).unwrap());

        let recent = accept_partial(&mut inbox, "102", admitted + 200_000);
        assert!(advance(&mut inbox, admitted + 230_000, None));
        assert!(advance(&mut inbox, admitted + 350_000, Some(&recent)));
        assert!(
            inbox
                .pending_unverified_reply(u64::MAX, &mut None)
                .is_none()
        );
        let boundary = admitted + 10 + WINDOW_MS;
        let later = accept_partial(&mut inbox, "103", boundary - 150_000);
        assert!(advance(&mut inbox, boundary - 120_000, None));
        assert!(advance(&mut inbox, boundary, Some(&later)));
        let (next, _) = inbox.pending_unverified_reply(boundary, &mut None).unwrap();
        assert_ne!(next, nonce);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn stale_notice_keeps_its_bytes_and_yields_to_a_later_source_after_restart() {
        let dir = crate::test_temp::root().join(format!("reply-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"installation_id":"fixture","application_id":"200","owner_id":"300","dm_channel_id":"400"}),
        );
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let admitted = 1000;
        let old = accept_partial(&mut inbox, "101", admitted);
        assert!(advance(&mut inbox, admitted + 30_000, None));
        assert!(advance(&mut inbox, admitted + 150_000, Some(&old)));
        let at = admitted + 10 + WINDOW_MS;
        let later = accept_partial(&mut inbox, "102", at - 150_000);
        assert!(advance(&mut inbox, at - 120_000, None));
        assert!(advance(&mut inbox, at, Some(&later)));
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let mut cursor = None;
        let original = std::fs::read(&inbox.file).unwrap();
        let (first, stale) = inbox.pending_unverified_reply(at, &mut cursor).unwrap();
        assert_eq!(stale["proof"]["source_id"], "101");
        let manager = inbox.manager().clone();
        assert!(matches!(
            inbox.claim_publication(&first, &manager, &stale, |_| Err(invalid("source deleted"))),
            Err(PublicationClaimError::BeforeWrite(_))
        ));
        assert_eq!(std::fs::read(&inbox.file).unwrap(), original);
        let (next, valid) = inbox.pending_unverified_reply(at, &mut cursor).unwrap();
        assert_eq!(valid["proof"]["source_id"], "102");
        assert!(
            inbox
                .claim_publication(&next, &manager, &valid, |_| Ok(()))
                .is_ok()
        );
        assert_eq!(inbox.publication(&first), stale);
        assert_eq!(inbox.publication(&next)["state"], "sending");
        std::fs::remove_dir_all(dir).unwrap();
    }
}
