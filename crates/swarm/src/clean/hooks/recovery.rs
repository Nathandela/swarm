//! The old Go producer proof remains valid until the staged suffix replaces its spool.
//! Its optional Rust intent lets a later clean finish publication of the new inode proof.
use super::{Directory, FLOOR, PROOF, Proof, SPOOL, unproven};
use crate::supervisor::runtime;
use rustix::fs::{Mode, OFlags, openat, renameat};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::fs::{self, File, Metadata};
use std::io::{self, Write};
use std::os::unix::fs::MetadataExt;

#[derive(Clone, Deserialize, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Pending {
    stage_id: String,
    sequence: u64,
    original_sha256: String,
    suffix_sha256: String,
    target: Option<[u64; 2]>,
}
fn identity(m: &Metadata) -> [u64; 2] {
    [m.dev(), m.ino()]
}
fn digest(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}
fn persist(
    dir: &Directory,
    previous: &Proof,
    next: &Proof,
    changed: &mut bool,
    fence: &impl Fn() -> io::Result<()>,
) -> io::Result<()> {
    dir.replace(PROOF, &serde_json::to_vec(next)?, changed, || {
        fence()?;
        let stored: Proof =
            serde_json::from_slice(&dir.read(PROOF, 2048)?).map_err(|_| unproven())?;
        if &stored != previous {
            return Err(unproven());
        }
        Ok(())
    })
}

pub(super) struct Transaction<'a> {
    pub(super) dir: &'a Directory,
    pub(super) original: &'a File,
    pub(super) metadata: &'a Metadata,
    pub(super) bytes: &'a [u8],
    pub(super) sequence: u64,
    pub(super) floor: u64,
}
impl Transaction<'_> {
    fn check_original(&self) -> io::Result<()> {
        let m = self.dir.verify_file(SPOOL.as_ref(), self.original)?;
        if (m.len(), m.ctime(), m.ctime_nsec())
            != (
                self.metadata.len(),
                self.metadata.ctime(),
                self.metadata.ctime_nsec(),
            )
        {
            return Err(unproven());
        }
        Ok(())
    }
    pub(super) fn published(&self, proof: &Proof) -> io::Result<bool> {
        if identity(self.metadata) == [proof.device, proof.inode] {
            return Ok(false);
        }
        let pending = proof.rust_clean_pending.as_ref().ok_or_else(unproven)?;
        if self.sequence != self.floor
            || pending.sequence != self.sequence
            || !runtime::valid_id(&pending.stage_id)
            || pending.target != Some(identity(self.metadata))
            || pending.suffix_sha256 != digest(self.bytes)
        {
            return Err(unproven());
        }
        Ok(true)
    }
    pub(super) fn finish(
        &self,
        proof: &Proof,
        spool: &File,
        changed: &mut bool,
        locks: &impl Fn() -> io::Result<()>,
    ) -> io::Result<()> {
        let new = self.dir.verify_file(SPOOL.as_ref(), spool)?;
        let mut next = proof.clone();
        [next.device, next.inode] = identity(&new);
        next.rust_clean_pending = None;
        persist(self.dir, proof, &next, changed, &|| {
            locks()?;
            self.dir.verify_file(SPOOL.as_ref(), spool)?;
            Ok(())
        })
    }
    pub(super) fn compact(
        &self,
        mut proof: Proof,
        cut: usize,
        changed: &mut bool,
        locks: impl Fn() -> io::Result<()>,
    ) -> io::Result<()> {
        let dir = self.dir;
        let old = || {
            locks()?;
            self.check_original()
        };
        let suffix = &self.bytes[cut..];
        let old_hash = digest(self.bytes);
        let new_hash = digest(suffix);
        if proof.rust_clean_pending.is_none() {
            let mut next = proof.clone();
            next.rust_clean_pending = Some(Pending {
                stage_id: runtime::random_id()?,
                sequence: self.sequence,
                original_sha256: old_hash.clone(),
                suffix_sha256: new_hash.clone(),
                target: None,
            });
            persist(dir, &proof, &next, changed, &old)?;
            proof = next;
        }
        let pending = proof.rust_clean_pending.as_ref().ok_or_else(unproven)?;
        if !runtime::valid_id(&pending.stage_id)
            || pending.sequence != self.sequence
            || pending.original_sha256 != old_hash
            || pending.suffix_sha256 != new_hash
        {
            return Err(unproven());
        }
        let stage = format!(".swarm-hook-clean-{}", pending.stage_id);
        let mut staged = match (pending.target, fs::symlink_metadata(dir.path.join(&stage))) {
            (None, Err(error)) if error.kind() == io::ErrorKind::NotFound => File::from(openat(
                &dir.file,
                stage.as_str(),
                OFlags::RDWR | OFlags::CREATE | OFlags::EXCL | OFlags::CLOEXEC,
                Mode::RUSR | Mode::WUSR,
            )?),
            (_, Ok(_)) => dir.open_file(stage.as_ref(), OFlags::RDWR)?,
            _ => return Err(unproven()),
        };
        let stat = dir.verify_file(stage.as_ref(), &staged)?;
        if stat.mode() & 0o077 != 0
            || pending
                .target
                .map_or(stat.len() != 0, |expected| expected != identity(&stat))
        {
            return Err(unproven());
        }
        if pending.target.is_none() {
            let mut next = proof.clone();
            next.rust_clean_pending.as_mut().unwrap().target = Some(identity(&stat));
            persist(dir, &proof, &next, changed, &old)?;
            proof = next;
        }
        // Recovery may encounter a partial stage; its inode is durably bound before its first byte.
        old()?;
        staged.set_len(0)?;
        #[cfg(test)]
        if std::env::var("SWARM_TEST_CLEAN_STOP").as_deref() == Ok("staging") {
            staged.write_all(&suffix[..suffix.len().div_ceil(2)])?;
            staged.sync_all()?;
            std::process::exit(77);
        }
        staged.write_all(suffix)?;
        staged.sync_all()?;
        let check = || {
            old()?;
            dir.verify_file(stage.as_ref(), &staged)?;
            Ok(())
        };
        if self.floor != self.sequence {
            dir.replace(FLOOR, self.sequence.to_string().as_bytes(), changed, check)?;
        }
        check()?;
        renameat(&dir.file, stage.as_str(), &dir.file, SPOOL)?;
        *changed = true;
        dir.file.sync_all()?;
        #[cfg(test)]
        if std::env::var("SWARM_TEST_CLEAN_STOP").as_deref() == Ok("published") {
            std::process::exit(78);
        }
        self.finish(&proof, &staged, changed, &locks)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::clean::Item;
    use crate::clean::hooks::{Cert, clean};
    use std::os::unix::fs::PermissionsExt;
    use std::process::Command;

    #[test]
    fn process_death_after_spool_publication_recovers_only_the_published_inode() {
        const CASE: &str = "clean::hooks::recovery::tests::process_death_after_spool_publication_recovers_only_the_published_inode";
        if let Some(path) = std::env::var_os("SWARM_TEST_CLEAN_ROOT") {
            let dir = Directory::open(std::path::Path::new(&path)).unwrap();
            let mut item = Item::new(&dir.path.join(SPOOL), "hook-prefix");
            clean(&dir, true, &mut item, || Ok(())).unwrap();
            panic!("child missed the commit barrier");
        }
        let path =
            std::env::temp_dir().join(format!("hook-recovery-{}", runtime::random_id().unwrap()));
        fs::create_dir(&path).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
        let dir = Directory::open(&path).unwrap();
        let put = |key: &str, bytes: &[u8]| fs::write(path.join(key), bytes).unwrap();
        let write_json =
            |key: &str, v: &serde_json::Value| put(key, &serde_json::to_vec(v).unwrap());
        let suffix = b"torn";
        let bytes = [&1_u64.to_be_bytes()[..], &1_u32.to_be_bytes(), b"x", suffix].concat();
        put(SPOOL, &bytes);
        let meta = fs::metadata(path.join(SPOOL)).unwrap();
        for lock in ["hooks.spool.open.lock", "hooks.spool.producer.lock"] {
            put(lock, b"");
        }
        write_json(
            "meta.json",
            &serde_json::json!({"id":path.file_name().unwrap().to_str(),"schema_version":1}),
        );
        write_json(
            "hooks.spool.incarnation",
            &serde_json::json!({"id":"origin"}),
        );
        write_json(
            PROOF,
            &serde_json::json!({"incarnation":"origin","nonce":"nonce","device":meta.dev(),"inode":meta.ino(),"pid":1,"start":1,"closed":true}),
        );
        put("hook.fold", b"1");
        let cert = serde_json::json!({"incarnation":"origin","producer":"nonce","sequence":1});
        write_json("hook.fold.cert", &cert);
        let crash = |point: &str| {
            Command::new(std::env::current_exe().unwrap())
                .args(["--exact", CASE])
                .env("SWARM_TEST_CLEAN_ROOT", &path)
                .env("SWARM_TEST_CLEAN_STOP", point)
                .status()
                .unwrap()
                .code()
        };
        assert_eq!(crash("staging"), Some(77));
        assert_eq!(fs::read(path.join(SPOOL)).unwrap(), bytes);
        let partial: Proof = serde_json::from_slice(&dir.read(PROOF, 2048).unwrap()).unwrap();
        let stage = path.join(format!(
            ".swarm-hook-clean-{}",
            partial.rust_clean_pending.unwrap().stage_id
        ));
        let linked = path.join("unrelated-link");
        fs::hard_link(&stage, &linked).unwrap();
        let mut withheld = Item::new(&path.join(SPOOL), "hook-prefix");
        assert!(clean(&dir, true, &mut withheld, || Ok(())).is_err());
        assert_eq!(fs::read(path.join(SPOOL)).unwrap(), bytes);
        fs::remove_file(linked).unwrap();
        assert_eq!(crash("published"), Some(78));
        assert!(!stage.exists());
        let spool = path.join(SPOOL);
        assert_eq!(fs::read(&spool).unwrap(), suffix);
        let expected = fs::metadata(&spool).unwrap().ino();
        let saved = path.join("published-spool");
        fs::rename(&spool, &saved).unwrap();
        put(SPOOL, suffix);
        let mut item = Item::new(&spool, "hook-prefix");
        assert!(clean(&dir, true, &mut item, || Ok(())).is_err());
        fs::rename(&saved, &spool).unwrap();
        clean(&dir, true, &mut item, || Ok(())).unwrap();
        let proof: Proof = serde_json::from_slice(&dir.read(PROOF, 2048).unwrap()).unwrap();
        assert_eq!(proof.inode, expected);
        assert!(proof.rust_clean_pending.is_none());
        assert_eq!(fs::read(&spool).unwrap(), suffix);
        assert_eq!(fs::read(path.join(FLOOR)).unwrap(), b"1");
        assert_eq!(
            serde_json::from_slice::<Cert>(&dir.read("hook.fold.cert", 2048).unwrap())
                .unwrap()
                .sequence,
            1
        );
        fs::remove_dir_all(path).unwrap();
    }
}
