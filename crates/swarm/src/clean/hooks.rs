//! Compact Go's closed hook journal only through its certified byte boundary.
mod recovery;
use super::{
    Item,
    files::{Directory, unsafe_target},
};
use crate::supervisor::runtime;
use rustix::fs::OFlags;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::io::{self, Read};

const SPOOL: &str = "hooks.spool";
const PROOF: &str = "hooks.spool.producer";
const FLOOR: &str = "hooks.spool.floor";
const INCARNATION: &str = "hooks.spool.incarnation";
const MAX_SPOOL: u64 = 64 << 20;

fn unproven() -> io::Error {
    io::Error::new(
        io::ErrorKind::InvalidInput,
        "shim: hook spool retirement proof unavailable",
    )
}
fn busy() -> io::Error {
    io::Error::new(
        io::ErrorKind::WouldBlock,
        "shim: hook producer still owns spool",
    )
}
fn number(bytes: &[u8]) -> io::Result<u64> {
    std::str::from_utf8(bytes)
        .ok()
        .and_then(|s| s.trim().parse().ok())
        .ok_or_else(unproven)
}
#[derive(Deserialize)]
struct Cert {
    incarnation: String,
    producer: String,
    sequence: u64,
}
#[derive(Clone, Deserialize, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct Proof {
    incarnation: String,
    nonce: String,
    device: u64,
    inode: u64,
    pid: i32,
    start: u64,
    #[serde(default)]
    closed: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    rust_clean_pending: Option<recovery::Pending>,
}
fn cut(data: &[u8], seq: u64, floor: u64) -> io::Result<usize> {
    if seq == 0 || floor > seq {
        return Err(unproven());
    }
    let mut offset = 0;
    while data.len() - offset >= 12 {
        let header = &data[offset..offset + 12];
        let sequence = u64::from_be_bytes(header[..8].try_into().unwrap());
        let length = u32::from_be_bytes(header[8..].try_into().unwrap()) as usize;
        if length > data.len() - offset - 12 {
            break;
        }
        offset += 12 + length;
        if sequence == seq {
            return Ok(offset);
        }
    }
    if seq == floor
        && (data.is_empty()
            || data.len() >= 12 && u64::from_be_bytes(data[..8].try_into().unwrap()) > floor)
    {
        return Ok(0);
    }
    Err(unproven())
}
pub(super) fn clean(
    dir: &Directory,
    apply: bool,
    item: &mut Item,
    fence: impl Fn() -> io::Result<()>,
) -> io::Result<()> {
    fence()?;
    let raw = dir.read("meta.json", 1 << 20)?;
    let meta: Value = serde_json::from_slice(&raw).map_err(|_| unsafe_target())?;
    let schema = meta.get("schema_version").map_or(Some(0), Value::as_u64);
    if meta["id"].as_str() != dir.path.file_name().and_then(|s| s.to_str())
        || !matches!(schema, Some(0 | 1 | 3 | 4))
    {
        return Err(unsafe_target());
    }
    let cert: Cert =
        serde_json::from_slice(&dir.read("hook.fold.cert", 2048)?).map_err(|_| unproven())?;
    if cert.sequence == 0
        || cert.incarnation.is_empty()
        || cert.producer.is_empty()
        || number(&dir.read("hook.fold", 64)?)? != cert.sequence
    {
        return Err(unproven());
    }
    let producer = dir
        .lock("hooks.spool.producer.lock".as_ref(), OFlags::RDWR)
        .map_err(|e| {
            if e.kind() == io::ErrorKind::WouldBlock {
                busy()
            } else {
                e
            }
        })?;
    let proof: Proof = serde_json::from_slice(&dir.read(PROOF, 2048)?).map_err(|_| unproven())?;
    if proof.incarnation != cert.incarnation
        || proof.nonce != cert.producer
        || proof.pid <= 0
        || proof.start == 0
    {
        return Err(unproven());
    }
    if !proof.closed {
        if meta["shim_pid"].as_i64() != Some(i64::from(proof.pid))
            || meta["shim_start_time"].as_u64() != Some(proof.start)
        {
            return Err(unproven());
        }
        match runtime::birth(proof.pid as u32) {
            Ok(start) if start == proof.start => return Err(busy()),
            Ok(_) => {}
            Err(_) => {
                // Failed birth lookup alone cannot prove absence.
                if unsafe { libc::kill(proof.pid, 0) } == 0
                    || io::Error::last_os_error().raw_os_error() != Some(libc::ESRCH)
                {
                    return Err(unproven());
                }
            }
        }
    }
    let generation = dir.lock("hooks.spool.open.lock".as_ref(), OFlags::RDWR)?;
    let locks = || -> io::Result<()> {
        fence()?;
        dir.verify_file("hooks.spool.producer.lock".as_ref(), &producer)?;
        dir.verify_file("hooks.spool.open.lock".as_ref(), &generation)?;
        Ok(())
    };
    let spool = dir.open_file(
        SPOOL.as_ref(),
        if apply { OFlags::RDWR } else { OFlags::RDONLY },
    )?;
    let before = spool.metadata()?;
    item.before = Some((&before).into());
    #[derive(Deserialize)]
    struct Incarnation {
        id: String,
        #[serde(default)]
        adopted_legacy: bool,
    }
    let identity: Incarnation =
        serde_json::from_slice(&dir.read(INCARNATION, 1024)?).map_err(|_| unproven())?;
    if identity.id.trim().is_empty() || identity.adopted_legacy || identity.id != proof.incarnation
    {
        return Err(unproven());
    }
    let floor = match dir.read(FLOOR, 64) {
        Ok(bytes) => number(&bytes)?,
        Err(e) if e.kind() == io::ErrorKind::NotFound => 0,
        Err(e) => return Err(e),
    };
    let mut bytes = Vec::new();
    (&spool).take(MAX_SPOOL + 1).read_to_end(&mut bytes)?;
    if bytes.len() as u64 > MAX_SPOOL {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "shim: hook cleanup size limit exceeded",
        ));
    }
    let tx = recovery::Transaction {
        dir,
        original: &spool,
        metadata: &before,
        bytes: &bytes,
        sequence: cert.sequence,
        floor,
    };
    let published = tx.published(&proof)?;
    let end = if published {
        0
    } else {
        cut(&bytes, cert.sequence, floor)?
    };
    if end == 0 && !published && proof.rust_clean_pending.is_some() {
        return Err(unproven());
    }
    item.eligible(end as u64);
    item.after = Some((&before).into());
    if !apply || end == 0 && !published {
        return Ok(());
    }
    let mut changed = false;
    let result = if published {
        tx.finish(&proof, &spool, &mut changed, &locks)
    } else {
        tx.compact(proof, end, &mut changed, locks)
    };
    if changed {
        item.changed();
        item.after = if result.is_ok() {
            Some((&dir.open_file(SPOOL.as_ref(), OFlags::RDONLY)?.metadata()?).into())
        } else {
            None
        };
    }

    result
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn a_torn_tail_is_never_part_of_a_certified_prefix() {
        let mut bytes = Vec::from(17_u64.to_be_bytes());
        bytes.extend(2_u32.to_be_bytes());
        bytes.extend(b"{}");
        let boundary = bytes.len();
        bytes.extend_from_slice(&18_u64.to_be_bytes());
        assert_eq!(cut(&bytes, 17, 0).unwrap(), boundary);
        assert!(cut(&bytes, 18, 0).is_err());
        assert!(cut(&bytes, 16, 17).is_err());
        assert_eq!(cut(&[], 17, 17).unwrap(), 0);
    }
}
