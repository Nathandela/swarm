use std::io;

#[cfg(target_os = "linux")]
pub(super) fn matches_image(pid: u32) -> io::Result<bool> {
    use sha2::{Digest, Sha256};
    use std::fs::File;
    use std::os::unix::fs::MetadataExt;

    let own = File::open("/proc/self/exe")?;
    let peer = File::open(format!("/proc/{pid}/exe"))?;
    let a = own.metadata()?;
    let b = peer.metadata()?;
    if a.dev() == b.dev() && a.ino() == b.ino() {
        return Ok(true);
    }
    if a.len() != b.len() {
        return Ok(false);
    }
    let digest = |mut image: File| -> io::Result<_> {
        let mut hash = Sha256::new();
        io::copy(&mut image, &mut hash)?;
        Ok(hash.finalize())
    };
    Ok(digest(own)? == digest(peer)?)
}

#[cfg(target_os = "linux")]
pub(super) fn stdout(pid: u32, route: &str, tty: u32) -> io::Result<[u64; 3]> {
    use std::os::unix::fs::{FileTypeExt, MetadataExt};

    let info = std::fs::metadata(format!("/proc/{pid}/fd/1"))?;
    if (route == "pipe" && info.file_type().is_fifo())
        || (route == "tty" && info.file_type().is_char_device() && info.rdev() == u64::from(tty))
    {
        return Ok([info.dev(), info.ino(), info.rdev()]);
    }
    Err(io::Error::other("permission peek output route is unproved"))
}

#[cfg(target_os = "macos")]
pub(super) fn matches_image(pid: u32) -> io::Result<bool> {
    unsafe extern "C" {
        fn csops(pid: libc::pid_t, operation: u32, data: *mut libc::c_void, size: usize) -> i32;
    }
    // XNU bsd/sys/codesign.h and osfmk/kern/cs_blobs.h.
    const STATUS: u32 = 0;
    const CDHASH: u32 = 5;
    const VALID: u32 = 1;
    let hash = |pid: u32| -> io::Result<[u8; 20]> {
        let mut status = 0_u32;
        let mut digest = [0_u8; 20];
        if unsafe { csops(pid as i32, CDHASH, digest.as_mut_ptr().cast(), digest.len()) } != 0
            || unsafe {
                csops(
                    pid as i32,
                    STATUS,
                    (&mut status as *mut u32).cast(),
                    size_of::<u32>(),
                )
            } != 0
            || status & VALID == 0
        {
            return Err(io::Error::other(
                "permission operator code signature is unproved",
            ));
        }
        Ok(digest)
    };
    Ok(hash(pid)? == hash(std::process::id())?)
}

#[cfg(target_os = "macos")]
pub(super) fn stdout(pid: u32, route: &str, tty: u32) -> io::Result<[u64; 3]> {
    // libproc's sys/proc_info.h: both records start with proc_fileinfo (24
    // bytes) followed by vinfo_stat (136 bytes), then the variant fields.
    #[repr(C)]
    struct Descriptor<T> {
        file: [u64; 3],
        device: u32,
        mode: u16,
        links: u16,
        inode: u64,
        owner: [u32; 2],
        stat: [u64; 10],
        block_size: i32,
        flags: u32,
        generation: u32,
        special_device: u32,
        reserved: [i64; 2],
        details: T,
    }
    fn query<T>(pid: u32, flavor: i32) -> io::Result<Descriptor<T>> {
        let mut info = std::mem::MaybeUninit::<Descriptor<T>>::uninit();
        let size = size_of::<Descriptor<T>>() as i32;
        if unsafe {
            libc::proc_pidfdinfo(
                pid as i32,
                libc::STDOUT_FILENO,
                flavor,
                info.as_mut_ptr().cast(),
                size,
            )
        } != size
        {
            return Err(io::Error::last_os_error());
        }
        Ok(unsafe { info.assume_init() })
    }

    const PROC_PIDFDVNODEINFO: i32 = 1;
    const PROC_PIDFDPIPEINFO: i32 = 6;
    if route == "pipe" {
        let info = query::<[u64; 3]>(pid, PROC_PIDFDPIPEINFO)?;
        let pipe_handle = info.details[0];
        if pipe_handle == 0 {
            return Err(io::Error::other("permission pipe identity is unproved"));
        }
        return Ok([pipe_handle, 0, 0]);
    }
    let info = query::<[i32; 4]>(pid, PROC_PIDFDVNODEINFO)?;
    if info.special_device == tty && info.mode & libc::S_IFMT == libc::S_IFCHR {
        return Ok([u64::from(info.device), info.inode, u64::from(tty)]);
    }
    Err(io::Error::other("permission peek output route is unproved"))
}
