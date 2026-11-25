mod support;

use serde_json::Value;
use std::fs;
use std::os::unix::fs::{PermissionsExt, symlink};
use std::process::Command;

#[test]
fn diagnose_absent_and_degraded_state_without_starting_or_executing_anything() {
    let home = std::env::temp_dir().join(format!("sw-doctor-{}", std::process::id()));
    fs::create_dir(&home).unwrap();
    let state = home.join("sessions");
    let shell = home.join("bin");
    fs::create_dir(&shell).unwrap();
    let cli = shell.join("claude");
    let canary = home.join("probe-executed");
    fs::write(&cli, format!("#!/bin/sh\ntouch '{}'\n", canary.display())).unwrap();
    fs::set_permissions(&cli, fs::Permissions::from_mode(0o700)).unwrap();
    let run = || {
        let r = Command::new(support::executable())
            .env_clear()
            .env("HOME", &home)
            .env("PATH", &shell)
            .env("SWARM_DAEMON_STATE", &state)
            .args(["doctor", "--json"])
            .output()
            .unwrap();
        assert!(r.stderr.is_empty(), "{r:?}");
        (
            r.status.code(),
            serde_json::from_slice::<Vec<Value>>(&r.stdout).unwrap(),
        )
    };
    let (code, findings) = run();
    assert_eq!(code, Some(0));
    assert_eq!(
        findings
            .iter()
            .map(|f| f["check"].as_str().unwrap())
            .collect::<Vec<_>>(),
        ["daemon", "daemon.env", "sessions", "auto-update", "storage"]
    );
    assert!(!state.exists());
    fs::create_dir(&state).unwrap();
    let saved = state.join("daemon.env");
    fs::write(&saved, "HOME=/private-fixture\nPATH=/absent-directory\n").unwrap();
    fs::set_permissions(&saved, fs::Permissions::from_mode(0o600)).unwrap();
    symlink(&home, state.join("unrelated")).unwrap();
    let (code, findings) = run();
    assert_eq!(code, Some(1));
    assert!(
        findings
            .iter()
            .any(|f| f["check"] == "agent:claude" && f["status"] == "fail")
    );
    assert!(findings.iter().any(|f| {
        f["check"] == "storage"
            && f["detail"]
                .as_str()
                .unwrap()
                .contains("1 symlink targets excluded")
    }));
    assert!(!canary.exists());
    assert!(!state.join("daemon.lock").exists());
    assert!(!state.join("daemon.sock").exists());
    fs::remove_dir_all(home).unwrap();
}
