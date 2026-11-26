//! Read one current owner DM attachment from its original Go ingress grant.
use super::*;
use crate::discord::received_files;

fn revision(source: &Value, origin: &Value) -> io::Result<String> {
    let content = source["content"].as_str().ok_or_else(invalid)?;
    if source["id"] != origin["message_id"]
        || source["channel_id"] != origin["channel_id"]
        || source["author"]["id"] != origin["actor_id"]
        || source["type"] != 0
        || source["author"].get("bot").is_some_and(|b| b != false)
        || source["author"].get("system").is_some_and(|b| b != false)
        || !source["guild_id"].is_null()
        || !source["edited_timestamp"].is_null()
        || !source["message_reference"].is_null()
        || !source["webhook_id"].is_null()
        || !source["message_snapshots"].is_null()
    {
        return Err(invalid());
    }
    let list = source["attachments"]
        .as_array()
        .filter(|files| (1..=10).contains(&files.len()))
        .ok_or_else(invalid)?;
    let mut evidence = Vec::with_capacity(list.len());
    for file in list {
        let attachment = field(file, "id");
        let name = field(file, "filename");
        let url = field(file, "url");
        let size = file["size"]
            .as_u64()
            .filter(|size| *size <= 16 << 20)
            .ok_or_else(invalid)?;
        if !snowflake(attachment)
            || name.is_empty()
            || name.len() > 256
            || url.is_empty()
            || !(file["content_type"].is_string() || file["content_type"].is_null())
        {
            return Err(invalid());
        }
        evidence.push(
            json!({"ID":attachment,"Name":name,"ContentType":field(file,"content_type"),
            "Availability":"metadata only, file access unverified","Size":size}),
        );
    }
    let tuple = json!([
        source["author"]["id"],
        content,
        source["channel_id"],
        "",
        source["id"],
        null,
        "",
        evidence
    ]);
    let canonical = crate::ipc::escape_json(serde_json::to_string(&tuple)?);
    Ok(format!("{:x}", Sha256::digest(canonical.as_bytes())))
}

pub(in crate::discord) fn read(
    place: &Place,
    message: &str,
    file: &str,
    sha: &str,
    offset: usize,
) -> io::Result<Value> {
    read_with(place, message, capture_source, |source, channel| {
        received_files::read_current(source, channel, file, sha, offset)
    })
}

fn read_with(
    place: &Place,
    message: &str,
    current: impl FnOnce(&Config, &str) -> io::Result<Value>,
    download: impl FnOnce(&Value, &str) -> io::Result<Value>,
) -> io::Result<Value> {
    let cfg = scope(place, message)?;
    let source = place.dir.join("discord.db");
    let (_pin, db) = open_bolt(&source, 64 << 20)?;
    let tx = db.begin().map_err(io::Error::other)?;
    let install = cfg.text("installation_id");
    let channel = cfg.text("dm_channel_id");
    let actor = cfg.text("owner_id");
    let binding_key = format!("{install}\0{channel}");
    let bound = value(&tx, "teammate_discussions", binding_key.as_bytes())?.ok_or_else(invalid)?;
    let discussion = field(&bound, "ID");
    let ids = tx.bucket(b"teammate_discussion_ids").ok_or_else(invalid)?;
    if discussion.is_empty()
        || bound["InstallationID"] != install
        || bound["Opened"] != true
        || bound["Route"]["channel_id"] != channel
        || bound["Route"]["guild_id"] != ""
        || !bound["Route"]["parent_id"].is_null()
        || (!bound["supervisor_socket"].is_null()
            && bound["supervisor_socket"] != cfg.text("daemon_socket"))
        || ids.get(format!("{install}\0{discussion}").as_bytes()) != Some(binding_key.as_bytes())
    {
        return Err(invalid());
    }
    let key = format!("{install}\0{message}");
    let stored = value(&tx, "teammate_ingress_guard", key.as_bytes())?.ok_or_else(invalid)?;
    let origin = json!({"message_id":message,"request_id":operation(install,message)?,"actor_id":actor,
        "channel_id":channel,"discussion_id":discussion,"revision":stored["Revision"]});
    if stored["LastChange"] != "" || field(&stored, "Revision").len() != 64 {
        return Err(invalid());
    }
    publication::verify_guard(&tx, &cfg, &origin)?;
    let source = current(&cfg, message)?;
    if revision(&source, &origin)? != origin["revision"] {
        return Err(invalid());
    }
    let bytes = download(&source, channel)?;
    if personal_enrollment(place)?.0 != cfg.0 {
        return Err(invalid());
    }
    Ok(bytes)
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::{Engine, engine::general_purpose::STANDARD};
    use std::process::Command;

    #[test]
    #[ignore = "requires the pinned original Go Discord fixture"]
    fn current_source_attachment_needs_original_go_revision_and_cdn_scope() {
        let fixture = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE")
            .expect("set SWARM_TEST_GO_DISCORD_FIXTURE to the pinned helper");
        let root = std::env::temp_dir()
            .canonicalize()
            .unwrap()
            .join(format!("da-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        let path = root.join("fixture");
        let p = path.to_str().unwrap();
        let call = |args: &[&str]| {
            let result = Command::new(&fixture).args(args).output().unwrap();
            assert!(
                result.status.success(),
                "{}",
                String::from_utf8_lossy(&result.stderr)
            );
        };
        call(&["create", p, root.to_str().unwrap(), "personal", "true"]);
        call(&["bind", p, "original-room", "400"]);
        call(&["ingress", p, "original-room", "701", "settle-attachment"]);
        let place = Place::for_supervisor(&root).unwrap();
        let bytes = b"header,value\nfixture,1\n";
        let sha = format!("{:x}", Sha256::digest(bytes));
        let url = "https://cdn.discordapp.com/attachments/400/901/fixture.csv";
        let source = json!({"id":"701","channel_id":"400","author":{"id":"200"},"type":0,
            "content":"offline replay witness","attachments":[{"id":"901","size":bytes.len(),
                "filename":"fixture.csv","content_type":"text/csv","url":url}]});
        let database = place.dir.join("discord.db");
        let preserved = fs::read(&database).unwrap();
        let read = read_with(
            &place,
            "701",
            |cfg, id| {
                assert_eq!(cfg.text("dm_channel_id"), "400");
                assert_eq!(id, "701");
                Ok(source.clone())
            },
            |source, channel| {
                received_files::read_current_with(
                    source,
                    channel,
                    "901",
                    &sha,
                    7,
                    |requested, length| {
                        assert_eq!(requested, url);
                        assert_eq!(length, bytes.len() as u64);
                        Ok(bytes.to_vec())
                    },
                )
            },
        )
        .unwrap();
        assert_eq!(read["filename"], "fixture.csv");
        assert_eq!(read["complete"], true);
        assert_eq!(
            STANDARD.decode(read["data"].as_str().unwrap()).unwrap(),
            b"value\nfixture,1\n"
        );
        let mut changed = source.clone();
        changed["attachments"][0]["filename"] = json!("different.csv");
        assert!(
            read_with(
                &place,
                "701",
                |_, _| Ok(changed),
                |_, _| panic!("changed revision must not fetch")
            )
            .is_err()
        );
        let mut changed = source;
        changed["attachments"][0]["url"] =
            json!("https://cdn.discordapp.com/attachments/999/901/fixture.csv");
        assert!(
            read_with(
                &place,
                "701",
                |_, _| Ok(changed),
                |source, channel| {
                    received_files::read_current_with(source, channel, "901", &sha, 0, |_, _| {
                        panic!("foreign CDN locator must not fetch")
                    })
                }
            )
            .is_err()
        );
        assert_eq!(preserved, fs::read(database).unwrap());
        fs::remove_dir_all(root).unwrap();
    }
}
