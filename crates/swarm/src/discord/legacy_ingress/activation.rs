//! Durable ownership handoff for a stopped original personal connector.
use super::*;
use bbolt_rs::CursorApi;

const MARKER: &str = ".rust-personal-handoff-v1.json";

fn pin_identity(path: &Path) -> io::Result<Value> {
    let pinned = fs::symlink_metadata(path)?;
    Ok(json!([pinned.dev(), pinned.ino()]))
}

fn identity_of(place: &Place, cfg: &Config) -> io::Result<Value> {
    let mut stable = cfg.0.clone();
    stable.sort_all_objects();
    // Stop can disable a connector without replacing its retained authority.
    stable["enabled"] = json!(true);
    let enrollment = format!("{:x}", Sha256::digest(serde_json::to_vec(&stable)?));
    let mut marker = json!({"version":1,"installation":cfg.text("installation_id"), "enrollment":enrollment,
        "directory":pin_identity(&place.dir)?, "connector":pin_identity(&place.dir.join("discord.db"))?});
    if retained_vm(cfg) {
        let checkout = Path::new(cfg.text("work_dir"));
        if !grants::project(cfg, cfg.text("work_dir")) {
            return Err(invalid());
        }
        marker["vm_checkout"] = pin_identity(checkout)?;
    }
    Ok(marker)
}

pub(in crate::discord) fn selected(place: &Place) -> io::Result<bool> {
    let marker = match runtime::read_json(&place.dir.join(MARKER)) {
        Ok(marker) => marker,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(false),
        Err(error) => return Err(error),
    };
    if marker != identity_of(place, &place.load()?)? {
        return Err(io::Error::other(
            "original Discord migration handoff no longer matches the enrollment",
        ));
    }
    Ok(true)
}

pub(super) enum Ready {
    Idle,
    AdmittedFile { parent_message: String },
    QueuedFile,
}

pub(super) fn ready(place: &Place, cfg: &Config, db: &impl DbApi) -> io::Result<Ready> {
    probe(place, cfg, db, false)
}

pub(super) fn ready_to_run(place: &Place, cfg: &Config, db: &impl DbApi) -> io::Result<Ready> {
    probe(place, cfg, db, retained_vm(cfg))
}

fn probe(place: &Place, cfg: &Config, db: &impl DbApi, inspect_queued: bool) -> io::Result<Ready> {
    let tx = db.begin().map_err(io::Error::other)?;
    let install = cfg.text("installation_id");
    let channel = cfg.text("dm_channel_id");
    let key = format!("{install}\0{channel}");
    let binding = value(&tx, "teammate_discussions", key.as_bytes())?.ok_or_else(invalid)?;
    let id = field(&binding, "ID");
    let active = tx
        .bucket(b"teammate_active_discussions")
        .ok_or_else(invalid)?;
    let ids = tx.bucket(b"teammate_discussion_ids").ok_or_else(invalid)?;
    if id.is_empty()
        || binding["InstallationID"] != install
        || binding["Opened"] != true
        || binding["ProvisionalCancelled"] != false
        || binding["StartupNoticePending"] != false
        || binding["Route"]["channel_id"] != channel
        || binding["Route"]["guild_id"] != ""
        || !binding["Route"]["parent_id"].is_null()
        || (!binding["supervisor_socket"].is_null()
            && binding["supervisor_socket"] != cfg.text("daemon_socket"))
        || active.get(key.as_bytes()) != Some([1].as_slice())
        || ids.get(format!("{install}\0{id}").as_bytes()) != Some(key.as_bytes())
    {
        return Err(invalid());
    }
    require_repaired(&tx, cfg, &binding)?;
    original_boundary(&tx, cfg, &binding).map_err(|error| {
        io::Error::other(format!("original personal consent boundary: {error}"))
    })?;
    prepare::no_pending_output(&tx, cfg)?;
    let meta = tx.bucket(b"teammate_meta").ok_or_else(invalid)?;
    if meta.get(b"active_ingress_count") != Some([0; 8].as_slice()) {
        return Err(io::Error::other(
            "original personal ingress count is not settled",
        ));
    }
    for bucket in ["teammate_ingress", "teammate_pending"] {
        let source = tx.bucket(bucket).ok_or_else(invalid)?;
        if source.iter_entries().next().is_some() || source.iter_buckets().next().is_some() {
            return Err(io::Error::other(
                "original personal ingress index is not empty",
            ));
        }
    }
    let (directory, ledger, endpoint) = voice_location(place, cfg, &binding)?;
    if inspect_queued
        && retained_vm(cfg)
        && pending_vm_mail(place, &format!("discord-teammate-{ledger}"))?
    {
        return queue::inspect(place, cfg, db)
            .and_then(|proof| proof.ok_or_else(invalid))
            .map(|_| Ready::QueuedFile)
            .map_err(|error| {
                io::Error::other(format!(
                    "original VM worker mailbox has a pending completion or request: {error}"
                ))
            });
    }
    open_owner_file(&directory.join("voice.initialized"))?;
    let path = directory.join("voice.db");
    let (_pin, voice) = open_bolt(&path, 64 << 20)?;
    let voice_tx = voice.begin().map_err(io::Error::other)?;
    let state = match prepare::read_voice(&voice_tx, &ledger, cfg, &endpoint) {
        Err(_) if retained_vm(cfg) => {
            if inspect_queued && queue::inspect(place, cfg, db)?.is_some() {
                return Ok(Ready::QueuedFile);
            }
            require_no_pending_vm_mail(place, &format!("discord-teammate-{ledger}"))?;
            return wake::inspect(place, cfg, &tx, &voice_tx, &binding, &ledger, &endpoint).map(
                |proof| Ready::AdmittedFile {
                    parent_message: field(&proof.origin, "message_id").to_owned(),
                },
            );
        }
        result => result?,
    };
    let stored = &state.conversation["binding"];
    if cfg.text("project_access") == "vm" {
        require_no_pending_vm_mail(place, field(stored, "conversation_id"))?;
    }
    let caps = ["semantic-send", "manager-provenance", "message-origin"];
    let (mut owner, rows) = super::super::migrate_supervisor::verified_owner(
        place.supervisor_root(),
        Path::new(cfg.text("daemon_socket")),
        Path::new(field(&cfg.0["supervisor"], "lock")),
        &caps,
    )?;
    cfg.check_supervisor(&owner, &caps)?;
    if rows
        .iter()
        .filter(|row| {
            row["id"] == stored["session_id"]
                && row["role"] == "manager"
                && row["manager_conversation_id"] == stored["conversation_id"]
                && row["capabilities"]["session_instance"] == stored["session_instance"]
                && row["status"]["Process"] == "running"
                && row["agent"] == cfg.text("manager_cli")
                && row["cwd"] == cfg.text("work_dir")
        })
        .count()
        != 1
    {
        return Err(invalid());
    }
    let current = owner.request_body(json!({"op":"manager_runtime","session_id":stored["session_id"],
        "body_version":1,"manager_runtime_request":{"session_instance":stored["session_instance"]}}),"manager_runtime")?;
    let current = &current["manager_runtime"];
    let mut expected = stored.clone();
    let values = expected.as_object_mut().ok_or_else(invalid)?;
    let session = values.remove("session_id").ok_or_else(invalid)?;
    values.insert("session".to_owned(), session);
    values.remove("generation");
    values.remove("native_cli");
    let bridge = fs::canonicalize("/tmp")?
        .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
        .join(&ledger[..32])
        .join("bridge.sock");
    if current["binding"] != expected
        || !native_cli_matches(&current["native_cli"], cfg.text("project_access") == "vm")
        || current["ready_for_input"] != true
        || current["provider_exhausted"] == true
        || !matches!(field(&current["turn"], "state"), "idle" | "completed")
        || current["bridge_socket"] != json!(bridge)
        || current["turn"]["externally_steered"] == true
        || current["turn"]["external_input"] != 0
        || current["auth_file"] != json!(directory.join("bridge.auth"))
    {
        return Err(invalid());
    }
    open_owner_file(&directory.join("bridge.auth"))?;
    Ok(Ready::Idle)
}

pub(super) fn pending_vm_mail(place: &Place, discussion: &str) -> io::Result<bool> {
    let path = place.supervisor_root().join("dispatcher-mail.db");
    let (_pin, db) = match open_bolt(&path, 256 << 20) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(false),
        result => result?,
    };
    let tx = db.begin().map_err(io::Error::other)?;
    let pending = tx.bucket(b"pending").ok_or_else(invalid)?;
    let prefix = format!("{discussion}\0");
    let mut cursor = pending.cursor();
    Ok(cursor
        .seek(prefix.as_bytes())
        .is_some_and(|(key, _)| key.starts_with(prefix.as_bytes())))
}

pub(super) fn require_no_pending_vm_mail(place: &Place, discussion: &str) -> io::Result<()> {
    if pending_vm_mail(place, discussion)? {
        return Err(io::Error::other(
            "original VM worker mailbox has a pending completion or request",
        ));
    }
    Ok(())
}

pub(in crate::discord) fn inspect_vm(place: &Place) -> io::Result<bool> {
    let cfg = personal_config(place, true)?;
    let checkout = Path::new(cfg.text("work_dir"));
    let pinned = fs::symlink_metadata(checkout)?;
    if !checkout.is_absolute() || !pinned.is_dir() || fs::canonicalize(checkout)? != checkout {
        return Err(invalid());
    }
    let source = place.dir.join("discord.db");
    let (pin, db) = open_bolt(&source, 64 << 20)?;
    probe(place, &cfg, &db, true)?;
    let current = fs::symlink_metadata(checkout)?;
    if !identity(&pin, &source)?
        || (current.dev(), current.ino()) != (pinned.dev(), pinned.ino())
        || personal_config(place, true)?.0 != cfg.0
    {
        return Err(invalid());
    }
    println!("Verified original Go personal VM manager custody. No migration handoff was written.");
    Ok(true)
}

pub(in crate::discord) fn migrate(
    place: &Place,
    dry_run: bool,
    rollback: bool,
    vm: bool,
) -> io::Result<bool> {
    let path = place.dir.join(MARKER);
    if rollback {
        let _configuration = place.lock()?;
        let _bridge = runtime::lock(&place.dir.join("bridge.lock"))?;
        if selected(place)? {
            if retained_vm(&place.load()?) != vm {
                return Err(io::Error::other(
                    "the rollback command does not match the retained personal mode",
                ));
            }
            let source = place.dir.join("discord.db");
            let (_pin, _db) = open_bolt(&source, 64 << 20)?;
            fs::remove_file(path)?;
            fs::File::open(&place.dir)?.sync_all()?;
        }
        println!("Original Go personal connector remains selected.");
        return Ok(true);
    }
    let cfg = personal_config(place, vm)?;
    grants::enrolled(&cfg)?;
    let source = place.dir.join("discord.db");
    let expected = fs::symlink_metadata(&source)?;
    let record = identity_of(place, &cfg)?;
    if dry_run {
        let (_pin, db) = open_bolt(&source, 64 << 20)?;
        ready_to_run(place, &cfg, &db)?;
        if identity_of(place, &place.load()?)? != record {
            return Err(invalid());
        }
    } else {
        let lease = ConnectorLease::acquire(place, &cfg, &expected, true)?;
        ready_to_run(place, &cfg, &lease.db)?;
        if !lease.source_unchanged()? || identity_of(place, &place.load()?)? != record {
            return Err(invalid());
        }
        if !selected(place)? {
            runtime::write_json(&path, &record)?;
        }
    }
    println!("Verified retained original personal connector and live native manager.");
    Ok(true)
}

pub(in crate::discord) fn disconnect(place: &Place) -> io::Result<()> {
    use crate::discord::local;
    use std::time::{Duration, Instant};
    let current = place.load()?;
    let deadline = Instant::now() + Duration::from_secs(45);
    let stop = match local::call_until(
        &place.dir,
        "shutdown",
        "",
        Some((current.text("installation_id"), &json!({}))),
        deadline,
    ) {
        Ok(receipt) if receipt["stopped"] == true => true,
        Err(err)
            if matches!(
                err.kind(),
                io::ErrorKind::NotFound | io::ErrorKind::ConnectionRefused
            ) =>
        {
            false
        }
        Ok(_) => return Err(invalid()),
        Err(err) => return Err(err),
    };
    let _configuration = loop {
        match place.lock() {
            Ok(lock) => break lock,
            Err(_) if stop && Instant::now() < deadline => {
                std::thread::sleep(Duration::from_millis(40))
            }
            Err(err) => return Err(err),
        }
    };
    let _bridge = runtime::lock(&place.dir.join("bridge.lock"))?;
    if !selected(place)? {
        return Err(invalid());
    }
    let path = place.dir.join("discord.db");
    let (_pin, _original) = open_bolt(&path, 64 << 20)?;
    let mut verified = place.load()?;
    if verified.0 != current.0 {
        return Err(invalid());
    }
    if verified.enabled() {
        verified.0["enabled"] = json!(false);
        runtime::write_json(&place.dir.join("config.json"), &verified.0)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    #[ignore = "requires the pinned original Go Discord fixture"]
    fn pending_vm_completion_is_read_only_and_discussion_scoped() {
        let fixture = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE")
            .expect("set SWARM_TEST_GO_DISCORD_FIXTURE to the pinned helper");
        let root = std::env::temp_dir()
            .canonicalize()
            .unwrap()
            .join(format!("vm-mail-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        let place = Place::fixture(root.clone());
        require_no_pending_vm_mail(&place, "retained-room").unwrap();
        let worker_file = root.join("completion.csv");
        let original = b"fixture,value\noriginal,1\n";
        fs::write(&worker_file, original).unwrap();
        let run = |action| {
            std::process::Command::new(&fixture)
                .arg(action)
                .arg(&root)
                .arg(&root)
                .arg(&worker_file)
                .arg("retained-room")
                .output()
                .unwrap()
        };
        let result = run("worker-file");
        assert!(
            result.status.success(),
            "{}",
            String::from_utf8_lossy(&result.stderr)
        );
        let mailbox = root.join("dispatcher-mail.db");
        let before = fs::read(&mailbox).unwrap();
        require_no_pending_vm_mail(&place, "another-room").unwrap();
        assert!(
            require_no_pending_vm_mail(&place, "retained-room")
                .unwrap_err()
                .to_string()
                .contains("pending completion")
        );
        assert_eq!(before, fs::read(&mailbox).unwrap());
        fs::write(&worker_file, b"fixture,value\nchanged,2\n").unwrap();
        let duplicate = run("worker-file");
        assert!(!duplicate.status.success());
        assert!(String::from_utf8_lossy(&duplicate.stderr).contains("conflicting note"));
        let reopened = run("worker-file-read");
        assert!(
            reopened.status.success(),
            "{}",
            String::from_utf8_lossy(&reopened.stderr)
        );
        assert_eq!(reopened.stdout, original);
        assert_eq!(before, fs::read(mailbox).unwrap());
        fs::remove_dir_all(root).unwrap();
    }
}
