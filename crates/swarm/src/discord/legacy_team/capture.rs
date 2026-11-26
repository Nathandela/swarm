//! Capture one verified follow-up in Go's existing ingress queue.
use super::*;
use crate::ipc::field;
use bbolt_rs::{BucketApi, DbRwAPI, TxRwApi};
use std::{fs::Metadata, os::unix::fs::MetadataExt, path::Path};

fn denied() -> io::Error {
    invalid("retained teammate ingress lacks an exact open, active, repaired source")
}

pub(super) fn binding<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    route: &Route,
    guard: &Guard,
    pending: Option<&Pending>,
) -> io::Result<Value> {
    let install = cfg.text("installation_id");
    let key = format!("{install}\0{}", route.channel_id);
    let active = tx
        .bucket(b"teammate_active_discussions")
        .ok_or_else(denied)?;
    let discussions = tx.bucket(b"teammate_discussions").ok_or_else(denied)?;
    let raw = discussions.get(key.as_bytes()).ok_or_else(denied)?;
    let saved: Value = serde_json::from_slice(raw)?;
    let meta = tx.bucket(b"teammate_meta").ok_or_else(denied)?;
    let checkpoint: Value = serde_json::from_slice(
        meta.get(format!("{install}\0checkpoint").as_bytes())
            .ok_or_else(denied)?,
    )?;
    let epoch = checkpoint["GapEpoch"].as_u64().ok_or_else(denied)?;
    if active.get(key.as_bytes()) != Some([1].as_slice())
        || saved["ID"] != guard.discussion_id
        || saved["Opened"] != true
        || saved["ProvisionalCancelled"] != false
        || saved["StartupNoticePending"] != false
        || saved["supervisor_socket"] != cfg.text("daemon_socket")
        || saved["RepairEpoch"] != epoch
        || saved["RepairTargetEpoch"] != epoch
        || saved["RepairUpper"].as_str() != Some("")
        || saved["RepairLower"].as_str() != Some("")
        || saved["RepairAfter"].as_str() != Some("")
        || saved["Boundary"] != guard.route.message_id
        || meta.get(b"active_ingress_count")
            != Some(u64::from(pending.is_some()).to_be_bytes().as_slice())
        || pending.is_some_and(|row| saved["LastIngressSequence"] != row.sequence)
        || checkpoint["Error"].as_str() != Some("")
        || !checkpoint["WorkerMailIssue"].is_null()
            && checkpoint["WorkerMailIssue"].as_str() != Some("")
    {
        return Err(denied());
    }
    Ok(saved)
}

#[derive(PartialEq)]
pub(super) struct Candidate {
    route: Value,
    author: Value,
    revision: String,
}

pub(super) struct Pending {
    pub(super) ingress_key: Vec<u8>,
    pub(super) pending_key: Vec<u8>,
    sequence: u64,
    candidate: Candidate,
}

pub(super) fn pending<'tx>(tx: &impl TxApi<'tx>, cfg: &Config) -> io::Result<Option<Pending>> {
    let ingress = tx.bucket(b"teammate_ingress").ok_or_else(denied)?;
    let Some((key, raw)) = ingress.iter_entries().next() else {
        return Ok(None);
    };
    if raw.len() > 1 << 20 {
        return Err(denied());
    }
    let row: Value = serde_json::from_slice(raw)?;
    let sequence = row["Sequence"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(denied)?;
    let revision = field(&row, "CaptureRevision");
    let install = cfg.text("installation_id");
    let route = &row["Route"];
    let message = field(route, "message_id");
    let route = json!({"guild_id":field(route,"guild_id"),"channel_id":field(route,"channel_id"),"message_id":message});
    let author = json!({"id":field(&row["Author"],"id"),"name":field(&row["Author"],"name")});
    let pending_key = [install.as_bytes(), b"\0", &sequence.to_be_bytes()].concat();
    if key != format!("{install}\0{message}").as_bytes()
        || !super::super::config::snowflake(message)
        || revision.len() != 64
        || !revision
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        // Go accepts duplicate and case-aliased members. Require exactly what this writer emits.
        || raw != super::super::legacy_ingress::pending_ingress_row(
            &route, &author, sequence, Some(revision),
        )?
        || tx
            .bucket(b"teammate_pending")
            .ok_or_else(denied)?
            .get(&pending_key)
            != Some(key)
        || tx
            .bucket(b"teammate_ingress_guard")
            .ok_or_else(denied)?
            .get(key)
            .is_some()
    {
        return Err(denied());
    }
    Ok(Some(Pending {
        ingress_key: key.to_vec(),
        pending_key,
        sequence,
        candidate: Candidate {
            route,
            author,
            revision: revision.to_owned(),
        },
    }))
}

pub(super) fn verify_pending(
    api: &Discord,
    cfg: &Config,
    route: &Route,
    guard: &Guard,
    pending: &Pending,
) -> io::Result<()> {
    let saved = &pending.candidate;
    let hint = json!({"id":saved.route["message_id"],"author":saved.author});
    if read_source(api, cfg, route, guard, &hint)? != *saved {
        return Err(denied());
    }
    Ok(())
}

pub(super) fn read_source(
    api: &Discord,
    cfg: &Config,
    route: &Route,
    guard: &Guard,
    hint: &Value,
) -> io::Result<Candidate> {
    let id = field(hint, "id");
    let actor = field(&hint["author"], "id");
    if !super::super::config::snowflake(actor)
        || actor == cfg.text("application_id")
        || !super::super::config::snowflake(id)
        || id.parse::<u64>().map_err(io::Error::other)?
            <= guard
                .route
                .message_id
                .parse::<u64>()
                .map_err(io::Error::other)?
    {
        return Err(denied());
    }
    verify_read_authority(api, cfg, route)?;
    let messages = human_boundary_rows(api, cfg, route)?;
    let [current, baseline] = messages.as_slice() else {
        return Err(denied());
    };
    verify_revision(route, guard, baseline)?;
    let empty = |name: &str| current[name].as_array().is_some_and(Vec::is_empty);
    let name = field(&current["author"], "username");
    let text = field(current, "content");
    if current["id"] != id
        || current["channel_id"] != route.channel_id
        || !current["guild_id"].is_null() && current["guild_id"] != route.guild_id
        || current["author"]["id"] != actor
        || !matches!(&current["author"]["bot"], Value::Null | Value::Bool(false))
        || !matches!(
            &current["author"]["system"],
            Value::Null | Value::Bool(false)
        )
        || name.is_empty()
        || name.len() > 256
        || name.chars().any(char::is_control)
        || text.trim().is_empty()
        || text.len() > 8_000
        || current["type"] != 0
        || current["mention_everyone"] != false
        || !empty("mentions")
        || !empty("mention_roles")
        || !empty("attachments")
        || !current["message_snapshots"].is_null() && !empty("message_snapshots")
        || !current["edited_timestamp"].is_null()
        || !current["message_reference"].is_null()
        || !current["webhook_id"].is_null()
    {
        return Err(denied());
    }
    verify_read_authority(api, cfg, route)?;
    Ok(Candidate {
        route: json!({"guild_id":route.guild_id,"channel_id":route.channel_id,"message_id":id}),
        author: json!({"id":actor,"name":name}),
        revision: plain_revision(route, id, actor, text)?,
    })
}

pub(super) fn commit(
    source: &Path,
    expected: &Metadata,
    cfg: &Config,
    route: &Route,
    guard: &Guard,
    captured: Candidate,
    recheck: impl FnOnce(&Candidate) -> io::Result<bool>,
) -> io::Result<()> {
    let open = legacy_state::writable_pin(source)?;
    let current = open.metadata()?;
    if (expected.dev(), expected.ino()) != (current.dev(), current.ino()) {
        return Err(denied());
    }
    let (pin, mut db) = legacy_state::bounded_writer(open)?;
    let mut tx = db.begin_rw().map_err(io::Error::other)?;
    let (fresh_route, fresh_guard) = read_scope(&tx, cfg, None)?;
    if fresh_route != *route || fresh_guard != *guard {
        return Err(denied());
    }
    let mut saved = binding(&tx, cfg, route, guard, None)?;
    let id = format!(
        "{}\0{}",
        cfg.text("installation_id"),
        field(&captured.route, "message_id")
    );
    if tx
        .bucket(b"teammate_ingress_guard")
        .ok_or_else(denied)?
        .get(id.as_bytes())
        .is_some()
        || !recheck(&captured)?
        || !legacy_state::unchanged(&pin, source)?
    {
        return Err(denied());
    }
    super::super::legacy_ingress::write_pending_ingress(
        &mut tx,
        cfg,
        &mut saved,
        captured.route,
        captured.author,
        Some(&captured.revision),
    )?;
    tx.commit().map_err(io::Error::other)
}
