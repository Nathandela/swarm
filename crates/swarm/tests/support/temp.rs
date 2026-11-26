use std::path::PathBuf;

/// Keep room for the session and bridge components of a Unix socket address.
pub fn root() -> PathBuf {
    let root = std::env::temp_dir();
    if root.as_os_str().len() <= 32 {
        root
    } else {
        PathBuf::from("/tmp")
    }
}
