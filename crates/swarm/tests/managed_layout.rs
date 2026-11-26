mod support;

use std::os::unix::fs::{PermissionsExt, symlink};
use std::{fs, process::Command};

#[test]
fn an_inactive_slot_cannot_become_an_unmanaged_writer_by_removing_its_marker() {
    let root = std::env::temp_dir().join(format!("swarm-marker-test-{}", std::process::id()));
    fs::create_dir(&root).unwrap();
    fs::set_permissions(&root, fs::Permissions::from_mode(0o700)).unwrap();
    let stale = root.join("releases/stale");
    fs::create_dir_all(&stale).unwrap();
    let binary = stale.join("swarm");
    fs::copy(support::executable(), &binary).unwrap();
    fs::set_permissions(&binary, fs::Permissions::from_mode(0o700)).unwrap();
    let workspace = root.join("project");
    fs::create_dir(&workspace).unwrap();
    let run = || {
        Command::new(&binary)
            .env_clear()
            .env("HOME", &root)
            .env("XDG_CONFIG_HOME", root.join("config"))
            .args(["setup-trust", "on"])
            .arg(&workspace)
            .output()
            .unwrap()
    };
    // A partial installation can retain a dangling current link after interrupted repair.
    symlink("routes/active", root.join("current")).unwrap();
    for key in ["writers.lock", "admin.lock"] {
        fs::write(root.join(key), b"").unwrap();
    }
    let blocked = run();
    assert!(!blocked.status.success());
    assert!(
        String::from_utf8_lossy(&blocked.stderr).contains("managed installation marker is missing")
    );
    assert!(!root.join("config").exists());
    for key in ["current", "writers.lock", "admin.lock"] {
        fs::remove_file(root.join(key)).unwrap();
    }
    let standalone = run();
    assert!(
        standalone.status.success(),
        "{}",
        String::from_utf8_lossy(&standalone.stderr)
    );
    fs::remove_dir_all(root).unwrap();
}
