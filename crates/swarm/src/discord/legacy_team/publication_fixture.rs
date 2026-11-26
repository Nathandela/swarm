//! Fixture-only handoff of one original guild Voice publication to Go export.
use super::*;
use crate::discord::legacy_ingress::manager_publication::stage_guild_speech;
use bbolt_rs::{DbRwAPI, TxRwApi};
use std::{io::Write, os::unix::fs::OpenOptionsExt};

const PUBLISH: &str = "publish";

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
    phase: &str,
    recheck: impl Fn() -> io::Result<Scope>,
    verify_pins: impl Fn() -> io::Result<()>,
) -> io::Result<()> {
    super::super::isolated_guild_fixture(place, cfg)?;
    let scope = recheck()?;
    let input = &scope.input;
    let settings = crate::manager_prompt::load(place.supervisor_root())?;
    let (directory, id, endpoint) = legacy_ingress::voice_location(place, cfg, original)?;
    let custody = Custody::claim(&directory, &id, true)?;
    open_owner_file(&directory.join("voice.initialized"))?;
    let path = directory.join("voice.db");
    let (pin, mut db) = legacy_state::bounded_writer(legacy_state::writable_pin(&path)?)?;
    let check_path = || {
        if !legacy_state::unchanged(&pin, &path)?
            || crate::manager_prompt::load(place.supervisor_root())? != settings
        {
            return Err(unavailable());
        }
        verify_pins()
    };
    let inspect = |voice: &mut voice::Voice| {
        voice::executing_guild_admission(
            voice,
            cfg,
            &input.origin,
            &input.text,
            &input.prompt,
            &input.project,
            &settings,
        )
    };
    let row = inspect(&mut voice::read_executing_guild(
        &db.begin().map_err(io::Error::other)?,
        &id,
        cfg,
        &endpoint,
        &input.origin,
    )?)?;
    let request = &row["request"];
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
    let bridge = {
        let _stop = status.commit_guard()?;
        custody.listen()?
    };
    println!("Serving the isolated original guild publication call.");
    let until = Instant::now() + Duration::from_secs(25);
    while !status.stopping() && Instant::now() < until {
        let mut received = false;
        let reply = bridge.accept_guarded(|raw| {
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
            std::fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(place.supervisor_root().join("rust-guild-publish-call"))?
                .write_all(raw)?;
            if phase != "publish-stage" {
                super::super::wait_at_test_boundary("holding supervisor-proven publication call")?;
            }
            let mut tx = db.begin_rw().map_err(io::Error::other)?;
            let mut voice = voice::read_executing_guild(&tx, &id, cfg, &endpoint, &input.origin)?;
            if inspect(&mut voice)? != row || recheck()? != scope {
                return Err(unavailable());
            }
            let publication = stage_guild_speech(&mut tx, voice, &turn, &call.arguments)?;
            if prove(&mut supervisor)? != turn {
                return Err(unavailable());
            }
            check_path()?;
            bridge.verify_path()?;
            let stop = status.commit_guard()?;
            if phase == "publish-before-commit" {
                std::process::exit(87);
            }
            tx.commit().map_err(io::Error::other)?;
            if phase == "publish-after-commit" {
                std::process::exit(88);
            }
            Ok((publication, stop))
        })?;
        if received {
            assert_eq!(reply.is_some(), phase == "publish-stage");
            println!("Original guild publication call settled at the fixture boundary.");
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    Err(unavailable())
}
