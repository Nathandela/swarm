//! Deliver one already authorized, plain Go personal outbox record.
use super::*;
use crate::discord::rest::{Discord, Post};
use jiff::Timestamp;

const EMPTY_TIME: &str = "0001-01-01T00:00:00Z";

fn digest(value: &Value) -> io::Result<String> {
    let body = crate::ipc::escape_json(serde_json::to_string(value)?);
    Ok(format!("{:x}", Sha256::digest(body.as_bytes())))
}

struct Publication {
    key: Vec<u8>,
    row: Value,
    binding: Value,
    nonce: String,
    body: String,
}

fn publication<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    id: &str,
) -> io::Result<Option<Publication>> {
    let install = cfg.text("installation_id");
    let channel = cfg.text("dm_channel_id");
    let scope = format!("{install}\0{channel}");
    let binding = value(tx, "teammate_discussions", scope.as_bytes())?.ok_or_else(invalid)?;
    let active = tx
        .bucket(b"teammate_active_discussions")
        .ok_or_else(invalid)?;
    let ids = tx.bucket(b"teammate_discussion_ids").ok_or_else(invalid)?;
    let discussion = field(&binding, "ID");
    if discussion.is_empty()
        || active.get(scope.as_bytes()) != Some([1].as_slice())
        || ids.get(format!("{install}\0{discussion}").as_bytes()) != Some(scope.as_bytes())
        || binding["InstallationID"] != install
        || binding["Route"]["channel_id"] != channel
        || binding["Route"]["guild_id"] != ""
        || !binding["Route"]["parent_id"].is_null()
        || binding["Opened"] != true
        || (!binding["supervisor_socket"].is_null()
            && binding["supervisor_socket"] != cfg.text("daemon_socket"))
    {
        return Err(invalid());
    }
    require_repaired(tx, cfg, &binding)?;
    let key = format!("{install}\0{discussion}\0{id}").into_bytes();
    let row = value(tx, "teammate_outbox", &key)?.ok_or_else(invalid)?;
    let output = &row["Publication"];
    let chunk = row["Chunks"]
        .as_array()
        .filter(|rows| rows.len() == 1)
        .and_then(|rows| rows.first())
        .ok_or_else(invalid)?;
    let expected = digest(&json!([install, discussion, id, 0]))?[..24].to_owned();
    let nonce_key = format!("{install}\0nonce:{expected}");
    let nonce_index = tx.bucket(b"teammate_meta").ok_or_else(invalid)?;
    if row["Route"] != binding["Route"]
        || row["DiscussionID"] != discussion
        || row["Route"]["message_id"] != ""
        || row["Internal"] != false
        || output["ID"] != id
        || [
            "JobID",
            "AttemptID",
            "QuestionID",
            "QuestionHash",
            "RecipientID",
            "SystemCause",
        ]
        .iter()
        .any(|key| !field(output, key).is_empty())
        || !output["Files"].is_null()
        || !field(output, "Body").is_empty()
        || !output["Sequence"].as_u64().is_some_and(|n| n > 0)
        || field(output, "AdmissionID").is_empty()
        || chunk["id"] != expected
        || nonce_index.get(nonce_key.as_bytes()) != Some(key.as_slice())
    {
        return Err(invalid());
    }
    if chunk["state"] == "sent"
        && snowflake(field(chunk, "message_id"))
        && !field(&row, "SettledAt").is_empty()
        && row["SettledAt"] != EMPTY_TIME
    {
        return Ok(None);
    }
    if chunk["state"] != "prepared"
        || row["SettledAt"] != EMPTY_TIME
        || row["Attempted"] != EMPTY_TIME
        || !chunk["message_id"].is_null()
    {
        return Err(io::Error::other(
            "original Discord publication is unresolved; reconcile the existing nonce",
        ));
    }
    let body = field(chunk, "body");
    let hash = digest(&json!(body))?;
    if body.trim().is_empty()
        || body.chars().count() > 1800
        || row["BodyHash"] != hash
        || row["ChunkHashes"] != json!([hash])
    {
        return Err(invalid());
    }
    Ok(Some(Publication {
        key,
        row: row.clone(),
        binding,
        nonce: expected,
        body: body.to_owned(),
    }))
}

fn original_voice(
    path: &Path,
    id: &str,
    expected: &Publication,
    cfg: &Config,
) -> io::Result<(fs::File, impl DbApi + use<>, Value)> {
    open_owner_file(&path.join("voice.initialized"))?;
    let (pin, db) = open_bolt(&path.join("voice.db"), 64 << 20)?;
    let tx = db.begin().map_err(io::Error::other)?;
    let root = tx.bucket(b"controller").ok_or_else(invalid)?;
    let raw = root.get(b"state").ok_or_else(invalid)?;
    if raw.len() > 4096
        || root.get(b"digest") != Some(format!("{:x}", Sha256::digest(raw)).as_bytes())
    {
        return Err(invalid());
    }
    let header: Value = serde_json::from_slice(raw)?;
    let conversation = format!("discord-teammate-{id}");
    if header["version"] != 3 || header["current_conversation"] != conversation {
        return Err(invalid());
    }
    let output = &expected.row["Publication"];
    let reference = serde_json::to_string(&[conversation.as_str(), field(output, "ID")])?;
    let key = format!("publications\0{reference}");
    let records = root.bucket(b"records").ok_or_else(invalid)?;
    let mut sum = [0; 32];
    let mut count = 0;
    for (key, raw) in records.iter_entries() {
        count += 1;
        if count > 4096 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        for (left, right) in sum.iter_mut().zip(record_hash(key, raw)) {
            *left ^= right;
        }
    }
    let existing: Value = serde_json::from_slice(records.get(key.as_bytes()).ok_or_else(invalid)?)?;
    let sequence = existing["admission"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(invalid)?;
    let admission_key = format!(
        "admissions\0{}",
        serde_json::to_string(&[conversation.as_str(), &sequence.to_string()])?
    );
    let admission: Value =
        serde_json::from_slice(records.get(admission_key.as_bytes()).ok_or_else(invalid)?)?;
    let origin = &admission["origin"];
    if admission["conversation_id"] != conversation
        || admission["source_cutover"] != true
        || origin["kind"] != "owner_direct"
        || origin["installation_id"] != expected.binding["InstallationID"]
        || origin["discussion_id"] != expected.binding["ID"]
        || origin["request_id"] != output["AdmissionID"]
        || origin["channel_id"] != expected.binding["Route"]["channel_id"]
        || !origin["guild_id"].is_null()
        || !snowflake(field(origin, "actor_id"))
        || origin["actor_id"] != origin["requester_id"]
        || origin["actor_id"] != cfg.text("owner_id")
        || admission["native_turn_id"] != existing["native_turn_id"]
        || field(&admission, "native_turn_id").is_empty()
    {
        return Err(invalid());
    }
    if header["records"] != count
        || header["record_hash"] != json!(sum)
        || existing["conversation_id"] != conversation
        || existing["id"] != output["ID"]
        || existing["text"] != expected.body
        || existing["text_hash"] != format!("{:x}", Sha256::digest(expected.body.as_bytes()))
        || existing["output_sequence"] != output["Sequence"]
        || existing["final"] != true
        || !existing["admission"].as_u64().is_some_and(|n| n > 0)
        || existing["discussion_source"]["admission_id"] != output["AdmissionID"]
        || !field(&existing["discussion_source"], "job_id").is_empty()
        || !field(&existing["discussion_source"], "attempt_id").is_empty()
        || !field(&existing["discussion_source"], "system_cause").is_empty()
        || !field(&existing, "recipient_id").is_empty()
        || !existing["discussion_files"].is_null()
    {
        return Err(invalid());
    }
    let origin = origin.clone();
    drop(tx);
    Ok((pin, db, origin))
}

fn verify_guard<'tx>(tx: &impl TxApi<'tx>, cfg: &Config, origin: &Value) -> io::Result<()> {
    let install = cfg.text("installation_id");
    let id = field(origin, "message_id");
    if !snowflake(id) || origin["request_id"] != operation(install, id)? {
        return Err(invalid());
    }
    let key = format!("{install}\0{id}");
    let guard = value(tx, "teammate_ingress_guard", key.as_bytes())?.ok_or_else(invalid)?;
    if guard["Disposition"] != "sent"
        || guard["ActorID"] != origin["actor_id"]
        || guard["Route"]["message_id"] != id
        || guard["Route"]["channel_id"] != origin["channel_id"]
        || guard["Route"]["guild_id"] != ""
        || guard["DiscussionID"] != origin["discussion_id"]
        || guard["OperationID"] != origin["request_id"]
        || guard["Revision"] != origin["revision"]
    {
        return Err(invalid());
    }
    Ok(())
}

fn put<'tx>(mut tx: impl TxRwApi<'tx>, row: &Publication) -> io::Result<()> {
    tx.bucket_mut(b"teammate_outbox")
        .ok_or_else(invalid)?
        .put(&row.key, serde_json::to_vec(&row.row)?)
        .map_err(io::Error::other)?;
    tx.commit().map_err(io::Error::other)
}

pub(in crate::discord) fn run(place: &Place, id: &str, dry_run: bool) -> io::Result<bool> {
    with(place, id, dry_run, |cfg| {
        let channel = cfg.text("dm_channel_id").to_owned();
        let api = Discord::connect(cfg)?;
        api.verify_app(cfg)?;
        api.verify_dm(cfg, None)?;
        Ok(move |row: &Publication| {
            api.post_publication_channel(&channel, &row.nonce, &row.body, &[])
        })
    })
}

fn with<F>(
    place: &Place,
    id: &str,
    dry_run: bool,
    authorize: impl FnOnce(&Config) -> io::Result<F>,
) -> io::Result<bool>
where
    F: FnOnce(&Publication) -> Post,
{
    if id.is_empty() || id.len() > 128 || !id.bytes().all(|b| (33..=126).contains(&b)) {
        return Err(invalid());
    }
    let cfg = retained_config(place)?;
    let source = place.dir.join("discord.db");
    let expected = fs::symlink_metadata(&source)?;
    if dry_run {
        let (_pin, db) = open_bolt(&source, 64 << 20)?;
        let tx = db.begin().map_err(io::Error::other)?;
        if let Some(out) = publication(&tx, &cfg, id)? {
            let (directory, scope, _) = voice_location(place, &cfg, &out.binding)?;
            let (_, _, origin) = original_voice(&directory, &scope, &out, &cfg)?;
            verify_guard(&tx, &cfg, &origin)?;
        }
        return Ok(true);
    }
    let mut lease = ConnectorLease::open(place, &cfg, &expected)?;
    under_lease(place, &cfg, id, &mut lease, authorize)
}

fn under_lease<F>(
    place: &Place,
    cfg: &Config,
    id: &str,
    lease: &mut ConnectorLease,
    authorize: impl FnOnce(&Config) -> io::Result<F>,
) -> io::Result<bool>
where
    F: FnOnce(&Publication) -> Post,
{
    let tx = lease.db.begin_rw().map_err(io::Error::other)?;
    let Some(mut out) = publication(&tx, cfg, id)? else {
        return Ok(true);
    };
    let (directory, scope, _) = voice_location(place, cfg, &out.binding)?;
    let (voice_pin, _voice, origin) = original_voice(&directory, &scope, &out, cfg)?;
    verify_guard(&tx, cfg, &origin)?;
    let post = authorize(cfg)?;
    if !identity(&lease.pin, &lease.source)? || !identity(&voice_pin, &directory.join("voice.db"))?
    {
        return Err(invalid());
    }
    out.row["Attempted"] = json!(Timestamp::now().to_string());
    out.row["Chunks"][0]["state"] = json!("sending");
    out.row["Error"] = json!("");
    put(tx, &out)?;
    let outcome = post(&out);
    if let Post::Acknowledged(remote) = outcome
        && remote["channel_id"] == cfg.text("dm_channel_id")
        && remote["author"]["id"] == cfg.text("application_id")
        && remote["webhook_id"].is_null()
        && remote["guild_id"].is_null()
        && remote["content"] == out.body
        && (remote["nonce"].is_null() || remote["nonce"] == out.nonce)
        && (remote["attachments"].is_null()
            || remote["attachments"].as_array().is_some_and(Vec::is_empty))
        && snowflake(field(&remote, "id"))
    {
        out.row["SettledAt"] = json!(Timestamp::now().to_string());
        out.row["Chunks"][0]["state"] = json!("sent");
        out.row["Chunks"][0]["body"] = json!("");
        out.row["Chunks"][0]["message_id"] = remote["id"].clone();
        out.row["Chunks"][0]["rest_acknowledged"] = json!(true);
        put(lease.db.begin_rw().map_err(io::Error::other)?, &out)?;
        println!("Original Go personal publication acknowledged.");
        return Ok(true);
    }
    out.row["Chunks"][0]["state"] = json!("uncertain");
    out.row["Error"] = json!("Discord publication awaiting its original nonce receipt");
    put(lease.db.begin_rw().map_err(io::Error::other)?, &out)?;
    Err(io::Error::other(
        "Discord publication outcome remains unresolved under its original nonce",
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;

    #[test]
    fn pinned_go_keeps_original_publication_nonce_and_outcome() {
        let Some(fixture) = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE") else {
            return;
        };
        for acknowledge in [true, false] {
            let root = std::env::temp_dir()
                .canonicalize()
                .unwrap()
                .join(format!("dp-{}", runtime::random_id().unwrap()));
            runtime::private_dir(&root).unwrap();
            let path = root.join("fixture");
            let p = path.to_str().unwrap();
            let call = |args: &[&str]| {
                let output = Command::new(&fixture).args(args).output().unwrap();
                assert!(
                    output.status.success(),
                    "{}",
                    String::from_utf8_lossy(&output.stderr)
                );
                String::from_utf8(output.stdout).unwrap()
            };
            call(&["create", p, root.to_str().unwrap(), "personal", "true"]);
            call(&["bind", p, "original-room", "400"]);
            call(&["voice-ingress", p, "original-room", "701", "final"]);
            call(&["publication-queue", p, "original-room", "701"]);
            let place = Place::for_supervisor(&root).unwrap();
            let not_sealed = fs::read(path.join("discord.db")).unwrap();
            assert!(
                with(
                    &place,
                    "fixture-final",
                    false,
                    |_| -> io::Result<fn(&Publication) -> Post> {
                        panic!("missing original connector guard cannot authorize a post")
                    }
                )
                .is_err()
            );
            assert_eq!(fs::read(path.join("discord.db")).unwrap(), not_sealed);
            assert!(settle(&place, "701", false).unwrap());
            let voice = fs::read_dir(path.join("discussions"))
                .unwrap()
                .next()
                .unwrap()
                .unwrap()
                .path()
                .join("voice.db");
            let voice_before = fs::read(&voice).unwrap();
            let source = path.join("discord.db");
            let original = fs::read(&source).unwrap();
            let expected = call(&["publication-read", p, "original-room"]);
            let nonce = expected
                .trim()
                .strip_prefix("prepared ")
                .unwrap()
                .to_owned();
            assert!(
                with(&place, "fixture-final", true, |_| Ok(
                    |_: &Publication| panic!("dry run cannot publish")
                ))
                .unwrap()
            );
            assert_eq!(original, fs::read(&source).unwrap());
            let nonce_copy = nonce.clone();
            let result = with(&place, "fixture-final", false, |_| {
                Ok(move |out: &Publication| {
                    assert_eq!(out.nonce, nonce_copy);
                    assert_eq!(out.body, "Original final personal reply.");
                    if acknowledge {
                        Post::Acknowledged(
                            json!({"channel_id":"400","author":{"id":"100"},"id":"900","content":out.body}),
                        )
                    } else {
                        Post::Unconfirmed
                    }
                })
            });
            assert_eq!(result.is_ok(), acknowledge, "{result:?}");
            let after = fs::read(&source).unwrap();
            let again = with(&place, "fixture-final", false, |_| {
                Ok(|_: &Publication| panic!("existing publication cannot be submitted again"))
            });
            assert_eq!(again.is_ok(), acknowledge);
            assert_eq!(after, fs::read(&source).unwrap());
            assert_eq!(voice_before, fs::read(&voice).unwrap());
            let durable = call(&["publication-read", p, "original-room"]);
            let phase = if acknowledge { "sent" } else { "uncertain" };
            assert_eq!(durable.trim(), format!("{phase} {nonce}"));
            call(&["publication-queue", p, "original-room", "701"]);
            assert_eq!(call(&["publication-read", p, "original-room"]), durable);
            fs::remove_dir_all(root).unwrap();
        }
    }
}
