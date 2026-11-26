use super::*;
use std::{
    cell::{Cell, RefCell},
    os::unix::process::ExitStatusExt,
};

fn output(stdout: &str, stderr: &str, success: bool) -> Output {
    Output {
        status: std::process::ExitStatus::from_raw(if success { 0 } else { 1 << 8 }),
        stdout: stdout.as_bytes().to_vec(),
        stderr: stderr.as_bytes().to_vec(),
    }
}

fn unit<'a>(platform: Platform, path: PathBuf, runner: &'a Runner<'a>) -> Unit<'a> {
    Unit {
        platform,
        path,
        runner,
    }
}

fn systemd_report(path: &Path, body: &str) -> String {
    let program = definition::bound_systemd_program(body).unwrap();
    let place = definition::bound_place(Platform::Systemd, body).unwrap();
    let place = place.to_str().unwrap();
    format!(
        "FragmentPath={}\nDropInPaths=\nExecStart={{ path={program} ; argv[]={program} discord run --state-dir {place} --unattended ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }}\nExecMainPID=0\n",
        path.display()
    )
}

#[test]
fn systemd_reports_fragment_and_dropins_before_mutation() {
    for mode in ["fragment", "dropin", "exec"] {
        let dir = std::env::temp_dir().join(format!("swarm-unit-{}", std::process::id()));
        let path = dir.join(SERVICE);
        runtime::private_dir(&dir).unwrap();
        let old = definition::render(
            Platform::Systemd,
            Path::new("/bin/swarm"),
            Path::new("/tmp/place"),
        )
        .unwrap();
        fs::write(&path, &old).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o600)).unwrap();
        let good = systemd_report(&path, &old);
        let changed = match mode {
            "fragment" => good.replace(path.to_str().unwrap(), "/tmp/foreign.service"),
            "dropin" => good.replace("DropInPaths=\n", "DropInPaths=/tmp/override.conf\n"),
            _ => good.replace("argv[]=/bin/swarm", "argv[]=/bin/foreign"),
        };
        let shows = Cell::new(0);
        let mutations = RefCell::new(Vec::new());
        let runner = |name: &str, args: &[&str]| -> io::Result<Output> {
            if args.contains(&"show") {
                let count = shows.get();
                shows.set(count + 1);
                return Ok(output(if count == 0 { &good } else { &changed }, "", true));
            }
            if args.contains(&"is-enabled") {
                return Ok(output("enabled\n", "", true));
            }
            if args.contains(&"is-active") {
                return Ok(output("inactive\n", "", false));
            }
            mutations.borrow_mut().push(format!("{name} {args:?}"));
            Ok(output("", "", true))
        };
        let service = unit(Platform::Systemd, path.clone(), &runner);
        if mode == "exec" {
            assert!(
                service
                    .install(Path::new("/bin/new-swarm"), Path::new("/tmp/place"))
                    .is_err()
            );
        } else {
            assert!(service.stop(Path::new("/tmp/place")).is_err());
        }
        assert!(mutations.borrow().is_empty());
        assert_eq!(service.read().unwrap().unwrap(), old);
        fs::remove_dir_all(dir).unwrap();
    }
}

#[test]
fn launchd_loaded_job_must_match_plist_program_and_arguments() {
    let plist = Path::new("/tmp/LaunchAgents/com.swarm.discord.plist");
    let expected = [
        "/bin/swarm",
        "discord",
        "run",
        "--state-dir",
        "/tmp/place",
        "--unattended",
    ];
    let log = Path::new("/tmp/place/connector.log");
    let good = format!(
        "path = {}\nstate = running\nprogram = {}\narguments = {{\n{}\n}}\nstdout path = {}\nstderr path = {}\n",
        plist.display(),
        expected[0],
        expected.join("\n"),
        log.display(),
        log.display(),
    );
    assert!(verify_launchd_print(&good, plist, &expected, log).is_ok());
    let wrong_program = good.replace("program = /bin/swarm", "program = /bin/other");
    assert!(verify_launchd_print(&wrong_program, plist, &expected, log).is_err());
    let wrong_path = good.replace(&plist.display().to_string(), "/tmp/foreign.plist");
    assert!(verify_launchd_print(&wrong_path, plist, &expected, log).is_err());
    let wrong_args = good.replace("/tmp/place", "/tmp/foreign-place");
    assert!(verify_launchd_print(&wrong_args, plist, &expected, log).is_err());
}

#[test]
fn launchd_stop_leaves_a_foreign_loaded_label_alone() {
    let dir = std::env::temp_dir().join(format!("swarm-launchd-unit-{}", std::process::id()));
    runtime::private_dir(&dir).unwrap();
    let plist = dir.join("com.swarm.discord.plist");
    fs::write(
        &plist,
        definition::render(
            Platform::Launchd,
            Path::new("/bin/swarm"),
            Path::new("/tmp/place"),
        )
        .unwrap(),
    )
    .unwrap();
    let report = format!(
        "path = {}\nstate = running\nprogram = /bin/other\narguments = {{\n/bin/other\ndiscord\nrun\n--state-dir\n/tmp/place\n--unattended\n}}\nstdout path = /tmp/place/connector.log\nstderr path = /tmp/place/connector.log\n",
        plist.display()
    );
    let mutations = RefCell::new(Vec::new());
    let runner = |name: &str, args: &[&str]| -> io::Result<Output> {
        if args.contains(&"print") {
            return Ok(output(&report, "", true));
        }
        mutations.borrow_mut().push(format!("{name} {args:?}"));
        Ok(output("", "", true))
    };
    let service = unit(Platform::Launchd, plist.clone(), &runner);
    assert!(service.stop(Path::new("/tmp/place")).is_err());
    assert!(mutations.borrow().is_empty());
    assert!(plist.exists());
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn launchd_install_rechecks_full_loaded_job_at_bootout() {
    let dir = std::env::temp_dir().join(format!("swarm-launchd-recheck-{}", std::process::id()));
    runtime::private_dir(&dir).unwrap();
    let plist = dir.join("com.swarm.discord.plist");
    let old = definition::render(
        Platform::Launchd,
        Path::new("/bin/swarm"),
        Path::new("/tmp/place"),
    )
    .unwrap();
    fs::write(&plist, &old).unwrap();
    fs::set_permissions(&plist, fs::Permissions::from_mode(0o600)).unwrap();
    let report = |program: &str| {
        format!(
            "path = {}\nstate = running\nprogram = {program}\narguments = {{\n{program}\ndiscord\nrun\n--state-dir\n/tmp/place\n--unattended\n}}\nstdout path = /tmp/place/connector.log\nstderr path = /tmp/place/connector.log\n",
            plist.display()
        )
    };
    let good = report("/bin/swarm");
    let foreign = report("/bin/foreign");
    let prints = Cell::new(0);
    let mutations = RefCell::new(Vec::new());
    let runner = |name: &str, args: &[&str]| -> io::Result<Output> {
        if args.contains(&"print") {
            let count = prints.get();
            prints.set(count + 1);
            return Ok(output(if count == 0 { &good } else { &foreign }, "", true));
        }
        mutations.borrow_mut().push(format!("{name} {args:?}"));
        Ok(output("", "", true))
    };
    let service = unit(Platform::Launchd, plist.clone(), &runner);
    assert!(
        service
            .install(Path::new("/bin/new-swarm"), Path::new("/tmp/place"))
            .is_err()
    );
    assert!(mutations.borrow().is_empty());
    assert_eq!(fs::read_to_string(&plist).unwrap(), old);
    fs::remove_dir_all(dir).unwrap();
}

#[test]
fn systemd_process_must_match_the_previous_command_and_user() {
    let root = std::env::temp_dir().join(format!("swarm-proc-{}", std::process::id()));
    let proc = root.join("42");
    fs::create_dir_all(&proc).unwrap();
    let binary = root.join("swarm");
    fs::write(&binary, b"test").unwrap();
    fs::write(
        proc.join("status"),
        format!(
            "Name:\ttest\nUid:\t{}\t{}\t{}\t{}\n",
            runtime::uid(),
            runtime::uid(),
            runtime::uid(),
            runtime::uid()
        ),
    )
    .unwrap();
    fs::write(
        proc.join("cmdline"),
        [
            binary.as_os_str().as_encoded_bytes(),
            b"\0discord\0run\0--state-dir\0/tmp/place\0--unattended\0",
        ]
        .concat(),
    )
    .unwrap();
    std::os::unix::fs::symlink(&binary, proc.join("exe")).unwrap();
    let argv = vec![
        binary.to_str().unwrap().to_owned(),
        "discord".into(),
        "run".into(),
        "--state-dir".into(),
        "/tmp/place".into(),
        "--unattended".into(),
    ];
    assert!(verify_systemd_process_at(&root, 42, binary.to_str().unwrap(), &argv).is_ok());
    let mut foreign = argv.clone();
    foreign[4] = "/tmp/foreign".into();
    assert!(verify_systemd_process_at(&root, 42, binary.to_str().unwrap(), &foreign).is_err());
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn systemd_249_execstart_output_keeps_execmainpid_separate() {
    let program = "/usr/bin/dbus-daemon";
    let argv = "/usr/bin/dbus-daemon --session --address=systemd: --nofork --nopidfile --systemd-activation --syslog-only";
    let exec_start = "{ path=/usr/bin/dbus-daemon ; argv[]=/usr/bin/dbus-daemon --session --address=systemd: --nofork --nopidfile --systemd-activation --syslog-only ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }";
    let properties = format!(
        "ExecMainPID=2456\nExecStart={exec_start}\nFragmentPath=/usr/lib/systemd/user/dbus.service\nDropInPaths=\n"
    );
    assert!(systemd_exec_matches(exec_start, program, argv));
    assert!(properties.contains("ExecMainPID=2456\n"));
    assert!(properties.contains("; pid=0 ;"));
    assert!(!systemd_exec_matches(
        exec_start,
        program,
        "/usr/bin/dbus-daemon --foreign"
    ));
}

#[test]
fn systemd_never_adopts_a_valid_replacement_at_the_unit_path() {
    for phase in ["install", "stop", "rollback"] {
        let dir =
            std::env::temp_dir().join(format!("swarm-unit-swap-{}-{phase}", std::process::id()));
        runtime::private_dir(&dir).unwrap();
        let path = dir.join(SERVICE);
        let a = definition::render(
            Platform::Systemd,
            Path::new("/bin/swarm-a"),
            Path::new("/tmp/a"),
        )
        .unwrap();
        let b = definition::render(
            Platform::Systemd,
            Path::new("/bin/swarm-b"),
            Path::new("/tmp/b"),
        )
        .unwrap();
        fs::write(&path, &a).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o600)).unwrap();
        let report_a = systemd_report(&path, &a);
        let report_b = systemd_report(&path, &b);
        let shows = Cell::new(0);
        let mutations = RefCell::new(Vec::new());
        let runner = |name: &str, args: &[&str]| -> io::Result<Output> {
            if args.contains(&"show") {
                let count = shows.get();
                shows.set(count + 1);
                if (phase == "rollback" && count == 0) || (phase != "rollback" && count == 1) {
                    fs::write(&path, &b)?;
                    return Ok(output(&report_b, "", true));
                }
                return Ok(output(&report_a, "", true));
            }
            if args.contains(&"is-active") {
                return Ok(output("inactive\n", "", false));
            }
            if args.contains(&"is-enabled") {
                return Ok(output("enabled\n", "", true));
            }
            mutations.borrow_mut().push(format!("{name} {args:?}"));
            Ok(output("", "", true))
        };
        let service = unit(Platform::Systemd, path.clone(), &runner);
        let result = match phase {
            "install" => service.install(Path::new("/bin/swarm-new"), Path::new("/tmp/new")),
            "stop" => service.stop(Path::new("/tmp/a")),
            _ => {
                let mut changed = false;
                service.apply_manager(true, false, Some(&b), Some(&a), &mut changed)
            }
        };
        assert!(result.is_err(), "{phase} accepted the replacement unit");
        assert!(mutations.borrow().is_empty(), "{phase} mutated the manager");
        assert_eq!(fs::read_to_string(&path).unwrap(), b);
        fs::remove_dir_all(dir).unwrap();
    }
}
