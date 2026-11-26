//! Deliver one already authorized, plain Go personal outbox record.
use super::*;
use crate::discord::rest::{Discord, Post};
use crate::supervisor::mail_files::{Captured, Descriptor};
use jiff::Timestamp;

const EMPTY_TIME: &str = "0001-01-01T00:00:00Z";

fn digest(value: &Value) -> io::Result<String> {
    let body = crate::ipc::escape_json(serde_json::to_string(value)?);
    Ok(format!("{:x}", Sha256::digest(body.as_bytes())))
}

pub(super) struct Publication {
    key: Vec<u8>,
    row: Value,
    binding: Value,
    pub(super) nonce: String,
    pub(super) body: String,
    pub(super) files: Vec<Captured>,
}

fn publication<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    id: &str,
    read_only: bool,
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
        || ["QuestionID", "QuestionHash", "RecipientID", "SystemCause"]
            .iter()
            .any(|key| !field(output, key).is_empty())
        || (field(output, "JobID").is_empty() != field(output, "AttemptID").is_empty())
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
    let pending = chunk["state"] == "prepared" && row["Attempted"] == EMPTY_TIME;
    let uncertain = read_only
        && matches!(field(chunk, "state"), "sending" | "uncertain")
        && row["Attempted"]
            .as_str()
            .is_some_and(|time| time != EMPTY_TIME && time.parse::<Timestamp>().is_ok());
    if !(pending || uncertain) || row["SettledAt"] != EMPTY_TIME || !chunk["message_id"].is_null() {
        return Err(io::Error::other(
            "original Discord publication is unresolved; reconcile the existing nonce",
        ));
    }
    let body = field(chunk, "body");
    let files = payload(tx, &key, &output["Files"])?;
    let hash = digest(&json!(body))?;
    let content = if files.is_empty() {
        hash.clone()
    } else {
        digest(&json!([body, output["Files"], ""]))?
    };
    if (body.trim().is_empty() && files.is_empty())
        || body.encode_utf16().count() > 1900
        || row["BodyHash"] != content
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
        files,
    }))
}

fn payload<'tx>(tx: &impl TxApi<'tx>, key: &[u8], metadata: &Value) -> io::Result<Vec<Captured>> {
    if metadata.is_null() {
        return Ok(Vec::new());
    }
    let list = metadata
        .as_array()
        .filter(|files| !files.is_empty() && files.len() <= 10)
        .ok_or_else(invalid)?;
    let bucket = tx.bucket(b"teammate_file_payloads").ok_or_else(invalid)?;
    let mut bytes = bucket
        .get(key)
        .filter(|raw| raw.len() <= 8 << 20)
        .ok_or_else(invalid)?;
    let mut ids = std::collections::BTreeSet::new();
    let mut names = std::collections::BTreeSet::new();
    let mut captures = Vec::with_capacity(list.len());
    for raw in list {
        let file: Descriptor = serde_json::from_value(raw.clone())?;
        if *raw != serde_json::to_value(&file)?
            || file.size == 0
            || file.id.is_empty()
            || file.id.len() > 128
            || !file.id.bytes().all(|b| (33..=126).contains(&b))
            || file.name.is_empty()
            || file.name.len() > 160
            || file.name.trim() != file.name
            || matches!(file.name.as_str(), "." | "..")
            || file.name.contains(['/', '\\'])
            || file.name.chars().any(char::is_control)
            || !matches!(
                file.content_type.as_str(),
                "image/png" | "image/jpeg" | "text/csv" | "application/pdf"
            )
            || !ids.insert(file.id.clone())
            || !names.insert(file.name.to_lowercase())
        {
            return Err(invalid());
        }
        let (part, remaining) = bytes.split_at_checked(file.size).ok_or_else(invalid)?;
        if file.sha256 != format!("{:x}", Sha256::digest(part)) {
            return Err(invalid());
        }
        captures.push(Captured {
            file,
            data: part.to_vec(),
        });
        bytes = remaining;
    }
    if !bytes.is_empty() {
        return Err(invalid());
    }
    Ok(captures)
}

fn original_voice(
    path: &Path,
    id: &str,
    expected: &Publication,
    cfg: &Config,
    endpoint: &str,
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
    if !matches!(header["version"].as_u64(), Some(3 | 4))
        || (!expected.files.is_empty() && header["version"] != 4)
        || header["current_conversation"] != conversation
    {
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
        || existing["exported"] != true
        || !existing["admission"].as_u64().is_some_and(|n| n > 0)
        || existing["discussion_source"]["admission_id"] != output["AdmissionID"]
        || field(&existing["discussion_source"], "job_id") != field(output, "JobID")
        || field(&existing["discussion_source"], "attempt_id") != field(output, "AttemptID")
        || !field(&existing["discussion_source"], "system_cause").is_empty()
        || !field(&existing, "recipient_id").is_empty()
        || !manifest_matches(
            &existing["discussion_files"],
            &expected.row["Publication"]["Files"],
        )
    {
        return Err(invalid());
    }
    grants::output(
        &records,
        cfg,
        endpoint,
        &conversation,
        field(&expected.binding, "ID"),
        &existing,
    )?;
    let origin = origin.clone();
    drop(tx);
    Ok((pin, db, origin))
}

fn manifest_matches(encoded: &Value, expected: &Value) -> bool {
    if expected.is_null() {
        return encoded.is_null() || encoded == "";
    }
    encoded
        .as_str()
        .filter(|body| body.len() <= 1 << 16)
        .and_then(|body| serde_json::from_str::<Value>(body).ok())
        .is_some_and(|actual| actual == *expected)
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

fn put<'tx>(mut tx: impl TxRwApi<'tx>, row: &Publication, clear_files: bool) -> io::Result<()> {
    tx.bucket_mut(b"teammate_outbox")
        .ok_or_else(invalid)?
        .put(&row.key, serde_json::to_vec(&row.row)?)
        .map_err(io::Error::other)?;
    if clear_files && !row.files.is_empty() {
        tx.bucket_mut(b"teammate_file_payloads")
            .ok_or_else(invalid)?
            .delete(&row.key)
            .map_err(io::Error::other)?;
    }
    tx.commit().map_err(io::Error::other)
}

pub(in crate::discord) fn read(
    place: &Place,
    output: &str,
    id: &str,
    sha256: &str,
    offset: usize,
) -> io::Result<Value> {
    use base64::{Engine, engine::general_purpose::STANDARD};
    let cfg = retained_config(place)?;
    let source = place.dir.join("discord.db");
    let (_pin, db) = open_bolt(&source, 64 << 20)?;
    let tx = db.begin().map_err(io::Error::other)?;
    let publication = publication(&tx, &cfg, output, true)?.ok_or_else(invalid)?;
    let (directory, scope, endpoint) = voice_location(place, &cfg, &publication.binding)?;
    let (_, _voice, origin) = original_voice(&directory, &scope, &publication, &cfg, &endpoint)?;
    verify_guard(&tx, &cfg, &origin)?;
    let entry = publication
        .files
        .iter()
        .find(|file| file.file.id == id && file.file.sha256 == sha256)
        .ok_or_else(|| {
            io::Error::other("attachment is outside the original authorized publication")
        })?;
    if offset > entry.file.size {
        return Err(invalid());
    }
    let end = offset.saturating_add(8192).min(entry.file.size);
    Ok(
        json!({"id":id,"sha256":sha256,"filename":entry.file.name,"offset":offset,
        "next_offset":end,"complete":end==entry.file.size,"encoding":"base64",
        "data":STANDARD.encode(&entry.data[offset..end])}),
    )
}

pub(in crate::discord) fn run(place: &Place, id: &str, dry_run: bool) -> io::Result<bool> {
    with(place, id, dry_run, |cfg| {
        let channel = cfg.text("dm_channel_id").to_owned();
        let api = Discord::connect(cfg)?;
        api.verify_app(cfg)?;
        api.verify_dm(cfg, None)?;
        Ok(move |row: &Publication| {
            api.post_publication_channel(&channel, &row.nonce, &row.body, &row.files)
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
        if let Some(out) = publication(&tx, &cfg, id, false)? {
            let (directory, scope, endpoint) = voice_location(place, &cfg, &out.binding)?;
            let (_, _, origin) = original_voice(&directory, &scope, &out, &cfg, &endpoint)?;
            verify_guard(&tx, &cfg, &origin)?;
        }
        return Ok(true);
    }
    let mut lease = ConnectorLease::open(place, &cfg, &expected)?;
    under_lease(place, &cfg, id, &mut lease, authorize)
}

pub(super) fn under_lease<F>(
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
    let Some(mut out) = publication(&tx, cfg, id, false)? else {
        return Ok(true);
    };
    let (directory, scope, endpoint) = voice_location(place, cfg, &out.binding)?;
    let (voice_pin, _voice, origin) = original_voice(&directory, &scope, &out, cfg, &endpoint)?;
    verify_guard(&tx, cfg, &origin)?;
    let post = authorize(cfg)?;
    if !identity(&lease.pin, &lease.source)? || !identity(&voice_pin, &directory.join("voice.db"))?
    {
        return Err(invalid());
    }
    out.row["Attempted"] = json!(Timestamp::now().to_string());
    out.row["Chunks"][0]["state"] = json!("sending");
    out.row["Error"] = json!("");
    put(tx, &out, false)?;
    let outcome = post(&out);
    if let Post::Acknowledged(remote) = outcome
        && remote["channel_id"] == cfg.text("dm_channel_id")
        && remote["author"]["id"] == cfg.text("application_id")
        && remote["webhook_id"].is_null()
        && remote["guild_id"].is_null()
        && remote["content"] == out.body
        && (remote["nonce"].is_null() || remote["nonce"] == out.nonce)
        && let Some(attachments) = attachment_ack(&out.files, &remote["attachments"])
        && snowflake(field(&remote, "id"))
    {
        out.row["SettledAt"] = json!(Timestamp::now().to_string());
        out.row["Chunks"][0]["state"] = json!("sent");
        out.row["Chunks"][0]["body"] = json!("");
        out.row["Chunks"][0]["message_id"] = remote["id"].clone();
        out.row["Chunks"][0]["rest_acknowledged"] = json!(true);
        if !attachments.is_empty() {
            out.row["Chunks"][0]["discord_attachment_ids"] = json!(attachments);
        }
        put(lease.db.begin_rw().map_err(io::Error::other)?, &out, true)?;
        println!("Original Go personal publication acknowledged.");
        return Ok(true);
    }
    out.row["Chunks"][0]["state"] = json!("uncertain");
    out.row["Error"] = json!("Discord publication awaiting its original nonce receipt");
    put(lease.db.begin_rw().map_err(io::Error::other)?, &out, false)?;
    Err(io::Error::other(
        "Discord publication outcome remains unresolved under its original nonce",
    ))
}

fn attachment_ack(expected: &[Captured], value: &Value) -> Option<Vec<String>> {
    if expected.is_empty() && value.is_null() {
        return Some(Vec::new());
    }
    let rows = value
        .as_array()
        .filter(|rows| rows.len() == expected.len())?;
    let mut seen = std::collections::BTreeSet::new();
    rows.iter()
        .zip(expected)
        .map(|(row, file)| {
            let id = field(row, "id");
            (snowflake(id)
                && seen.insert(id)
                && row["filename"] == file.file.name
                && row["size"] == file.file.size)
                .then(|| id.to_owned())
        })
        .collect()
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
        for (acknowledge, grant) in [
            (true, ""),
            (false, ""),
            (true, "project-final"),
            (true, "attempt-final"),
            (true, "attachment-final"),
            (false, "attachment-final"),
        ] {
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
            let attached = grant == "attachment-final";
            if attached {
                call(&["voice-ingress", p, "original-room", "701", grant]);
            } else if !grant.is_empty() {
                let project = root.join("current-project");
                runtime::private_dir(&project).unwrap();
                let project = project.to_str().unwrap();
                call(&["project", p, project]);
                call(&["voice-ingress", p, "original-room", "701", grant, project]);
            } else {
                call(&["voice-ingress", p, "original-room", "701", "final"]);
            }
            let mut queue = vec!["publication-queue", p, "original-room", "701"];
            if attached {
                queue.push("attachment");
            }
            call(&queue);
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
            let file_sha = format!("{:x}", Sha256::digest(b"header,value\nfixture,1\n"));
            if attached {
                assert!(read(&place, "fixture-final", "wrong-file", &file_sha, 0).is_err());
                assert!(
                    read(&place, "fixture-final", "original-file", &"0".repeat(64), 0).is_err()
                );
                let part = read(&place, "fixture-final", "original-file", &file_sha, 7).unwrap();
                assert_eq!(part["filename"], "fixture.csv");
                assert_eq!(part["offset"], 7);
                assert_eq!(part["complete"], true);
                use base64::{Engine, engine::general_purpose::STANDARD};
                assert_eq!(
                    STANDARD.decode(part["data"].as_str().unwrap()).unwrap(),
                    b"value\nfixture,1\n"
                );
            }
            let nonce_copy = nonce.clone();
            let result = with(&place, "fixture-final", false, |_| {
                Ok(move |out: &Publication| {
                    assert_eq!(out.nonce, nonce_copy);
                    assert_eq!(out.body, "Original final personal reply.");
                    let disc_files = if attached {
                        let [file] = out.files.as_slice() else {
                            panic!("missing original staged attachment");
                        };
                        assert_eq!(file.file.name, "fixture.csv");
                        assert_eq!(file.data, b"header,value\nfixture,1\n");
                        json!([{"id":"901","filename":file.file.name,"size":file.file.size}])
                    } else {
                        assert!(out.files.is_empty());
                        Value::Null
                    };
                    if acknowledge {
                        Post::Acknowledged(
                            json!({"channel_id":"400","author":{"id":"100"},"id":"900",
                            "content":out.body,"attachments":disc_files}),
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
            if attached {
                assert_eq!(
                    read(&place, "fixture-final", "original-file", &file_sha, 0).is_ok(),
                    !acknowledge
                );
                let size = call(&["publication-payload", p, "original-room"]);
                assert_eq!(
                    size.trim().parse::<usize>().unwrap(),
                    if acknowledge {
                        0
                    } else {
                        b"header,value\nfixture,1\n".len()
                    }
                );
            }
            call(&queue);
            assert_eq!(call(&["publication-read", p, "original-room"]), durable);
            fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn original_unexported_or_removed_project_publication_cannot_send() {
        let Some(fixture) = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE") else {
            return;
        };
        for phase in ["unexported-final", "project-final", "attempt-final"] {
            let root = std::env::temp_dir()
                .canonicalize()
                .unwrap()
                .join(format!("dp-{}", runtime::random_id().unwrap()));
            runtime::private_dir(&root).unwrap();
            let path = root.join("fixture");
            let p = path.to_str().unwrap();
            let run = |args: &[&str]| {
                let output = Command::new(&fixture).args(args).output().unwrap();
                assert!(
                    output.status.success(),
                    "{}",
                    String::from_utf8_lossy(&output.stderr)
                );
            };
            run(&["create", p, root.to_str().unwrap(), "personal", "true"]);
            run(&["bind", p, "original-room", "400"]);
            let old = root.join("former-project");
            runtime::private_dir(&old).unwrap();
            if phase != "unexported-final" {
                run(&["project", p, old.to_str().unwrap()]);
            }
            run(&[
                "voice-ingress",
                p,
                "original-room",
                "701",
                phase,
                old.to_str().unwrap(),
            ]);
            run(&["publication-queue", p, "original-room", "701"]);
            if phase != "unexported-final" {
                run(&["project", p, "remove"]);
            }
            let place = Place::for_supervisor(&root).unwrap();
            assert!(settle(&place, "701", false).unwrap());
            let source = path.join("discord.db");
            let before = fs::read(&source).unwrap();
            let receipt = fs::read_dir(path.join("discussions"))
                .unwrap()
                .next()
                .unwrap()
                .unwrap()
                .path()
                .join("voice.db");
            let voice_before = fs::read(&receipt).unwrap();
            assert!(
                with(
                    &place,
                    "fixture-final",
                    false,
                    |_| -> io::Result<fn(&Publication) -> Post> {
                        panic!("revoked or unexported original publication cannot be posted")
                    }
                )
                .is_err()
            );
            assert_eq!(before, fs::read(&source).unwrap());
            assert_eq!(voice_before, fs::read(&receipt).unwrap());
            fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn altered_go_attachment_payload_cannot_send() {
        let Some(fixture) = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE") else {
            return;
        };
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
        };
        call(&["create", p, root.to_str().unwrap(), "personal", "true"]);
        call(&["bind", p, "original-room", "400"]);
        call(&[
            "voice-ingress",
            p,
            "original-room",
            "701",
            "attachment-final",
        ]);
        call(&["publication-queue", p, "original-room", "701", "attachment"]);
        let place = Place::for_supervisor(&root).unwrap();
        assert!(settle(&place, "701", false).unwrap());
        call(&["publication-damage", p, "original-room"]);
        let source = path.join("discord.db");
        let before = fs::read(&source).unwrap();
        assert!(
            with(
                &place,
                "fixture-final",
                false,
                |_| -> io::Result<fn(&Publication) -> Post> {
                    panic!("a changed attachment payload must not reach the outbound transport")
                }
            )
            .is_err()
        );
        assert_eq!(before, fs::read(&source).unwrap());
        fs::remove_dir_all(root).unwrap();
    }
}
