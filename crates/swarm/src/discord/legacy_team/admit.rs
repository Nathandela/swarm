//! Queue one original guild input in the Go Voice store before native preparation.
use super::*;
use crate::{discord::legacy_ingress::prepare as voice, ipc::field};
use bbolt_rs::{BucketApi, DbRwAPI, TxRwApi};

fn unavailable() -> io::Error {
    invalid("original teammate ingress does not have one qualified Go Voice admission")
}

pub(super) fn pending<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    message: &str,
) -> io::Result<capture::Pending> {
    let key = format!("{}\0{message}", cfg.text("installation_id"));
    let ingress = tx.bucket(b"teammate_ingress").ok_or_else(unavailable)?;
    let raw = ingress
        .get(key.as_bytes())
        .filter(|raw| raw.len() <= 1 << 20)
        .ok_or_else(unavailable)?;
    let row: Value = serde_json::from_slice(raw)?;
    if row["State"] != "sending" {
        return Err(unavailable());
    }
    capture::indexed_pending(
        tx,
        cfg,
        key.as_bytes(),
        &row,
        field(&row["Admission"], "Revision"),
    )
}

pub(super) fn verify_record<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    pending: &capture::Pending,
    binding: &prepare::Discussion,
) -> io::Result<()> {
    let ingress = tx.bucket(b"teammate_ingress").ok_or_else(unavailable)?;
    if ingress.get(&pending.ingress_key)
        != Some(prepare::sending_record(cfg, pending, binding)?.as_slice())
    {
        return Err(unavailable());
    }
    Ok(())
}

#[derive(Clone, Copy)]
pub(super) struct Advancement {
    pub(super) native: bool,
    pub(super) dry_run: bool,
}

pub(super) fn advance<'a>(
    place: &Place,
    cfg: &Config,
    binding: &Value,
    input: &render::Input,
    action: Advancement,
    recheck: impl FnOnce() -> io::Result<render::Input>,
    guard_commit: impl FnOnce() -> io::Result<std::sync::MutexGuard<'a, ()>>,
) -> io::Result<()> {
    let accepted = Some((&input.origin, action.native));
    let mut manager = prepare::Manager::claim(place, cfg, binding, accepted)?;
    let settings = if action.native {
        crate::manager_prompt::load(place.supervisor_root())?
    } else {
        String::new()
    };
    let verify_settings = || {
        if action.native && crate::manager_prompt::load(place.supervisor_root())? != settings {
            return Err(unavailable());
        }
        Ok(())
    };
    let complete = |state: &mut voice::Voice| {
        if action.native {
            voice::guild_native_ready(
                state,
                cfg,
                &input.origin,
                &input.text,
                &input.prompt,
                &input.project,
                &settings,
            )
        } else {
            Ok(voice::queued_guild_admission(
                state,
                &input.origin,
                &input.text,
                &input.prompt,
                &input.project,
            )?
            .is_none())
        }
    };
    let existing = {
        let (_pin, db) = open_bolt(&manager.directory.join("voice.db"), 64 << 20)?;
        let mut state = voice::read_voice_with_queue(
            &db.begin().map_err(io::Error::other)?,
            &manager.id,
            cfg,
            &manager.endpoint,
            accepted,
        )?;
        if state.conversation["binding"] != manager.binding
            || state.generation()? != manager.generation
        {
            return Err(unavailable());
        }
        complete(&mut state)?
    };
    if existing || action.dry_run {
        manager.verify(place, cfg)?;
        if recheck()? != *input {
            return Err(unavailable());
        }
        manager.verify_pins()?;
        verify_settings()?;
        println!(
            "{}",
            match (action.native, existing) {
                (false, true) => "The original Go Voice store already has this queued input.",
                (false, false) => "The original Go Voice store can queue this teammate input.",
                (true, true) => "The original Go Voice store already has this prepared input.",
                (true, false) => "The original Go Voice store can prepare this teammate input.",
            },
        );
        return Ok(());
    }
    let (_pin, mut db) = manager.open_writer()?;
    let mut tx = db.begin_rw().map_err(io::Error::other)?;
    let mut state =
        voice::read_voice_with_queue(&tx, &manager.id, cfg, &manager.endpoint, accepted)?;
    if state.conversation["binding"] != manager.binding || state.generation()? != manager.generation
    {
        return Err(unavailable());
    }
    manager.verify(place, cfg)?;
    if recheck()? != *input {
        return Err(unavailable());
    }
    manager.verify_pins()?;
    verify_settings()?;
    if action.native {
        if complete(&mut state)? {
            return Err(unavailable());
        }
        voice::stage_guild_native(
            &mut tx,
            state,
            cfg,
            &input.origin,
            &input.prompt,
            &input.project,
            &settings,
        )?;
    } else {
        let (key, admission) = voice::queued_guild_admission(
            &mut state,
            &input.origin,
            &input.text,
            &input.prompt,
            &input.project,
        )?
        .ok_or_else(unavailable)?;
        voice::stage(&mut tx, state, &key, &admission)?;
    }
    {
        let _commit = guard_commit()?;
        tx.commit().map_err(io::Error::other)?;
    }
    if action.native {
        println!("Prepared the original teammate native request for Go to submit.");
    } else {
        println!("Queued the original teammate input in Go Voice.");
    }
    Ok(())
}
