//! Recognize the original Go v0.29.25 compacted gap evidence without changing it.
use super::*;
use bbolt_rs::BucketApi;
use serde::Deserialize;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

pub(super) const CHECKPOINT_DIAGNOSTIC: &str = "Unobserved Discord interval. Known threads are repaired as context; an unknown tag may have been missed.";
// Go's PublicationContentHash for the internal restatement notice with no files or recipient.
const BODY_HASH: &str = "24834e78dbf82a8d5ab89c48cb4c6be935a450a118700d68e379adf72cfcbf7e";

fn unqualified() -> io::Error {
    invalid("retained Go missed-gap evidence is unsettled or malformed")
}

fn go_json(value: &Value) -> io::Result<String> {
    Ok(crate::ipc::escape_json(serde_json::to_string(value)?))
}

pub(super) fn settled<'tx>(tx: &impl TxApi<'tx>, cfg: &Config, route: &Route) -> io::Result<bool> {
    let install = cfg.text("installation_id");
    let discussions = tx.bucket(b"teammate_discussions").ok_or_else(unqualified)?;
    let raw = discussions
        .get(format!("{install}\0{}", route.channel_id))
        .ok_or_else(unqualified)?;
    let binding: Value = serde_json::from_slice(raw)?;
    let fields = binding.as_object().ok_or_else(unqualified)?;
    if fields
        .keys()
        .any(|key| !key.is_ascii() || key != "GapMissed" && key.eq_ignore_ascii_case("GapMissed"))
    {
        return Err(unqualified());
    }
    let receipts = tx.bucket(b"teammate_receipts").ok_or_else(unqualified)?;
    let mut rows = receipts.iter_entries();
    let Some(missed) = fields.get("GapMissed") else {
        return if rows.next().is_none() {
            Ok(false)
        } else {
            Err(unqualified())
        };
    };
    let missed = missed
        .as_u64()
        .filter(|value| *value > 0)
        .ok_or_else(unqualified)?;
    let meta = tx.bucket(b"teammate_meta").ok_or_else(unqualified)?;
    let checkpoint = meta
        .get(format!("{install}\0checkpoint"))
        .ok_or_else(unqualified)?;
    let current: Value = serde_json::from_slice(checkpoint)?;
    let epoch = current["GapEpoch"].as_u64().ok_or_else(unqualified)?;
    let discussion = prepare::Discussion::deserialize(&binding)?;
    let timestamp = binding["InputNoticeAt"].as_str().ok_or_else(unqualified)?;
    if cfg.text("mode") != "teammate"
        || missed > epoch
        || discussion.gap > epoch
        || binding["NotifiedEpoch"].as_u64() != Some(discussion.gap)
        || binding["RepairEpoch"] != epoch
        || binding["RepairTargetEpoch"] != epoch
        || binding["Opened"] != true
        || binding["ProvisionalCancelled"] != false
        || binding["StartupNoticePending"] != false
        || !binding["StartupNoticeGeneration"].is_u64()
        || !binding["LastIngressSequence"].is_u64()
        || ["ParentLow", "Boundary"]
            .iter()
            .any(|key| !binding[*key].is_string())
        || ["InputNoticeID", "StartupNoticeID"]
            .iter()
            .any(|key| fields.contains_key(*key))
        || OffsetDateTime::parse(timestamp, &Rfc3339).is_err()
        || binding["Error"] != ""
        || ["RepairAfter", "RepairUpper", "RepairLower"]
            .iter()
            .any(|key| binding[*key] != "")
        || current["Session"].as_str().is_none_or(str::is_empty)
        || current["Error"] != "" && current["Error"] != CHECKPOINT_DIAGNOSTIC
        || current.get("WorkerMailIssue").is_some()
        || meta.get(format!("{install}\0outbox_count")) != Some(0u64.to_be_bytes().as_slice())
    {
        return Err(unqualified());
    }
    let discussion = discussion.id;
    let digest = format!(
        "{:x}",
        Sha256::digest(go_json(&json!([discussion, missed]))?)
    );
    let id = format!("gap_{}", &digest[..32]);
    let publication = json!({"SystemCause":"","ID":id,"Body":"","AdmissionID":"","JobID":"","AttemptID":"",
        "QuestionID":"","QuestionHash":"","Sequence":0});
    let hash = format!("{:x}", Sha256::digest(go_json(&publication)?));
    let (key, raw) = rows.next().ok_or_else(unqualified)?;
    let receipt: Value = serde_json::from_slice(raw)?;
    let messages = receipt["DiscordMessageIDs"]
        .as_array()
        .filter(|ids| ids.len() == 1)
        .ok_or_else(unqualified)?;
    let expected = json!({"PublicationHash":hash,"BodyHash":BODY_HASH,"Route":route,"DiscordMessageIDs":messages});
    if rows.next().is_some()
        || key != format!("{install}\0{discussion}\0{id}").as_bytes()
        || !messages[0]
            .as_str()
            .is_some_and(super::super::config::snowflake)
        || raw != go_json(&expected)?.as_bytes()
    {
        return Err(unqualified());
    }
    Ok(true)
}
