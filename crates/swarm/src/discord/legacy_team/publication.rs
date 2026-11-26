//! Stage one original guild Voice publication for Go export.
use super::*;
use crate::discord::legacy_ingress::manager_publication::{resolve_live_call, stage_guild_speech};
use bbolt_rs::{DbRwAPI, TxRwApi};
use std::os::unix::fs::MetadataExt;

const PUBLISH: &str = "publish";

fn unavailable() -> io::Error {
    invalid("the original running guild publication is unqualified")
}

fn checked_publish(raw: &[u8], request: &Value) -> io::Result<Call> {
    let call = scoped_call(raw, request, PUBLISH)?;
    let args = &call.arguments;
    let id = field(args, "output_id");
    let text = field(args, "text");
    if args.as_object().is_none_or(|args| args.len() != 4)
        || args["final"] != true
        || args["sequence"].as_u64() != Some(1)
        || id.is_empty()
        || id.len() > 64
        || id.starts_with("publication:")
        || !id.bytes().all(|b| (33..=126).contains(&b))
        || text.is_empty()
        || text.trim() != text
        || text.contains('\0')
        || text.encode_utf16().count() > 1800
    {
        return Err(unavailable());
    }
    Ok(call)
}

pub(in crate::discord::legacy_team) fn serve(
    place: &Place,
    cfg: &Config,
    original: &Value,
    status: &StatusSocket,
    dry_run: bool,
    recheck: impl Fn() -> io::Result<Scope>,
    verify_pins: impl Fn() -> io::Result<()>,
) -> io::Result<()> {
    let scope = recheck()?;
    let input = &scope.input;
    let settings = crate::manager_prompt::load(place.supervisor_root())?;
    let (directory, id, endpoint) = legacy_ingress::voice_location(place, cfg, original)?;
    let custody = Custody::claim(&directory, &id, true)?;
    open_owner_file(&directory.join("voice.initialized"))?;
    let path = directory.join("voice.db");
    let (read_lock, db) = open_bolt(&path, 64 << 20)?;
    let inspect = |voice: &mut voice::Voice| {
        let header = voice.guild_publication_header()?.clone();
        let row = voice::executing_guild_admission(
            voice,
            cfg,
            &input.origin,
            &input.text,
            &input.prompt,
            &input.project,
            &settings,
        )?;
        Ok::<_, io::Error>((header, row))
    };
    let original_voice = inspect(&mut voice::read_executing_guild(
        &db.begin().map_err(io::Error::other)?,
        &id,
        cfg,
        &endpoint,
        &input.origin,
    )?)?;
    drop(db);
    let pin = read_lock.try_clone()?;
    // Keep the original inode pinned while allowing its writer to take the flock.
    drop(LockDescription(read_lock));
    let check_path = || {
        if !legacy_state::unchanged(&pin, &path)?
            || crate::manager_prompt::load(place.supervisor_root())? != settings
        {
            return Err(unavailable());
        }
        input.verify_checkout()?;
        verify_pins()
    };
    let request = &original_voice.1["request"];
    let mut supervisor = owner(place, cfg, request)?;
    let native = supervisor.request_body(
        json!({"op":"manager_runtime","session_id":request["session"],"body_version":1,
        "manager_runtime_request":{"session_instance":request["session_instance"]}}),
        "manager_runtime",
    )?;
    verify_live_runtime(&native["manager_runtime"], request, &custody)?;
    if recheck()? != scope {
        return Err(unavailable());
    }
    check_path()?;
    if dry_run {
        println!("The original teammate turn is eligible to stage a final publication.");
        return Ok(());
    }
    let bridge = {
        let _stop = status.commit_guard()?;
        custody.listen()?
    };
    println!("Waiting for the original guild final publication call.");
    let until = Instant::now() + Duration::from_secs(25);
    while !status.stopping() && Instant::now() < until {
        let mut received = false;
        let mut committed = false;
        let replied = bridge.accept_guarded(|raw| {
            let call = checked_publish(raw, request)?;
            let mut supervisor = owner(place, cfg, request)?;
            let prove = |supervisor: &mut Client| {
                let (runtime, turn) = prove_live_call(
                    supervisor,
                    request,
                    PUBLISH,
                    &call.arguments,
                    &call.meta,
                    false,
                )?;
                verify_live_runtime(&runtime, request, &custody)?;
                Ok::<_, io::Error>(turn)
            };
            let turn = prove(&mut supervisor)?;
            received = true;
            #[cfg(test)]
            super::super::publication_proof_fixture(place, cfg, raw)?;
            let writer = legacy_state::writable_pin(&path)?;
            let (old, new) = (pin.metadata()?, writer.metadata()?);
            if (old.dev(), old.ino()) != (new.dev(), new.ino()) {
                return Err(unavailable());
            }
            let (_writer, mut db) = legacy_state::bounded_writer(writer)?;
            check_path()?;
            let mut tx = db.begin_rw().map_err(io::Error::other)?;
            let mut voice = voice::read_executing_guild(&tx, &id, cfg, &endpoint, &input.origin)?;
            if inspect(&mut voice)? != original_voice || recheck()? != scope {
                return Err(unavailable());
            }
            #[cfg(test)]
            if std::env::var("SWARM_TEST_DISCORD_GUILD_PHASE").as_deref() == Ok("publish-closed") {
                println!("Rechecked the complete original publication source.");
            }
            let publication = stage_guild_speech(&mut tx, voice, &turn, &call.arguments)?;
            if prove(&mut supervisor)? != turn
                || resolve_live_call(
                    &mut supervisor,
                    request,
                    PUBLISH,
                    &call.arguments,
                    &call.meta,
                )? != turn
            {
                return Err(unavailable());
            }
            let stop = status.commit_guard()?;
            check_path()?;
            bridge.verify_path()?;
            #[cfg(test)]
            super::super::publication_commit_fixture(place, cfg, true)?;
            tx.commit().map_err(io::Error::other)?;
            committed = true;
            #[cfg(test)]
            super::super::publication_commit_fixture(place, cfg, false)?;
            Ok((publication, stop))
        });
        if committed {
            println!("Staged the original guild final publication for Go to deliver.");
            return Ok(());
        }
        replied?;
        if received {
            return Err(unavailable());
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    Err(unavailable())
}
