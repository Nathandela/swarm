//! A single installed Discord service per user, shared with the Go distribution.
mod definition;
use super::config::{Config, Place, invalid};
use crate::{ipc, supervisor::runtime};
use definition::Platform;
use std::{
    fs::{self, DirBuilder, File, OpenOptions},
    io::{self, Read, Write},
    os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt},
    path::{Path, PathBuf},
    process::{Command, Output},
};

const LABEL: &str = "com.swarm.discord";
const SERVICE: &str = "swarm-discord.service";

type Runner<'a> = dyn Fn(&str, &[&str]) -> io::Result<Output> + 'a;

fn command(name: &str, args: &[&str]) -> io::Result<Output> {
    Command::new(name).args(args).env("LC_ALL", "C").output()
}

struct Unit<'a> {
    platform: Platform,
    path: PathBuf,
    runner: &'a Runner<'a>,
}

impl Unit<'_> {
    fn current() -> io::Result<Self> {
        let home = ipc::env_path("HOME").ok_or_else(|| invalid("HOME is undefined"))?;
        let platform = if cfg!(target_os = "macos") {
            Platform::Launchd
        } else {
            Platform::Systemd
        };
        Ok(Self {
            platform,
            path: definition::path(platform, &home)?,
            runner: &command,
        })
    }

    fn read(&self) -> io::Result<Option<String>> {
        let file = match OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
            .open(&self.path)
        {
            Ok(file) => file,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(error) => return Err(error),
        };
        let meta = file.metadata()?;
        if !meta.is_file()
            || meta.nlink() != 1
            || meta.uid() != runtime::uid()
            || meta.mode() & 0o022 != 0
            || meta.len() > 65536
        {
            return Err(invalid("unsafe Discord service unit"));
        }
        let mut body = String::new();
        file.take(65537).read_to_string(&mut body)?;
        if body.len() > 65536 {
            return Err(invalid("Discord service unit exceeds size limit"));
        }
        definition::bound_place(self.platform, &body)?;
        Ok(Some(body))
    }

    fn write(&self, body: Option<&str>, mode: u32) -> io::Result<()> {
        let parent = self
            .path
            .parent()
            .ok_or_else(|| invalid("missing unit directory"))?;
        match body {
            Some(body) => {
                DirBuilder::new()
                    .mode(0o700)
                    .recursive(true)
                    .create(parent)?;
                let meta = fs::symlink_metadata(parent)?;
                if !meta.is_dir() || meta.uid() != runtime::uid() || meta.mode() & 0o022 != 0 {
                    return Err(invalid("unsafe Discord service directory"));
                }
                runtime::write_atomic(&self.path, |file| {
                    file.set_permissions(fs::Permissions::from_mode(mode))?;
                    file.write_all(body.as_bytes())
                })
            }
            None => {
                fs::remove_file(&self.path)?;
                File::open(parent)?.sync_all()
            }
        }
    }

    fn run(&self, name: &str, args: &[&str]) -> io::Result<()> {
        let output = (self.runner)(name, args)?;
        if output.status.success() {
            return Ok(());
        }
        Err(io::Error::other(format!(
            "Discord service {name} {args:?}: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        )))
    }

    fn systemd_state(&self, op: &str) -> io::Result<bool> {
        let output = (self.runner)("systemctl", &["--user", op, SERVICE])?;
        let stdout = String::from_utf8_lossy(&output.stdout);
        match stdout.trim() {
            "active" | "activating" | "reloading" | "enabled" | "enabled-runtime"
                if output.status.success() =>
            {
                Ok(true)
            }
            "inactive" | "failed" | "disabled" | "not-found" => Ok(false),
            _ if !output.status.success() && {
                let reason = String::from_utf8_lossy(&output.stderr);
                reason.contains(SERVICE)
                    && (reason.contains("does not exist")
                        || reason.contains("No such file or directory")
                        || reason.contains("could not be found"))
            } =>
            {
                Ok(false)
            }
            _ => Err(invalid("unable to establish Discord user-service state")),
        }
    }

    fn verify_systemd(
        &self,
        manager_bodies: &[&str],
        process_bodies: &[&str],
        disk_body: Option<&str>,
        allow_unloaded: bool,
    ) -> io::Result<()> {
        let active = self.systemd_state("is-active")?;
        let output = (self.runner)(
            "systemctl",
            &[
                "--user",
                "show",
                "--all",
                "--property=FragmentPath",
                "--property=DropInPaths",
                "--property=ExecStart",
                "--property=ExecMainPID",
                SERVICE,
            ],
        )?;
        if !output.status.success() {
            return Err(invalid("unable to establish Discord unit definition"));
        }
        let mut fields = std::collections::BTreeMap::new();
        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            if let Some((key, value)) = line.split_once('=') {
                if matches!(
                    key,
                    "FragmentPath" | "DropInPaths" | "ExecStart" | "ExecMainPID"
                ) && fields.insert(key, value).is_some()
                {
                    return Err(invalid("ambiguous Discord unit manager state"));
                }
            }
        }
        let fragment = fields
            .get("FragmentPath")
            .ok_or_else(|| invalid("Discord unit fragment is unavailable"))?;
        let dropins = fields
            .get("DropInPaths")
            .ok_or_else(|| invalid("Discord unit drop-ins are unavailable"))?;
        let exec_start = fields
            .get("ExecStart")
            .ok_or_else(|| invalid("Discord unit command is unavailable"))?;
        let pid = fields
            .get("ExecMainPID")
            .and_then(|s| s.parse::<u32>().ok())
            .ok_or_else(|| invalid("Discord unit PID is unavailable"))?;
        if self.read()?.as_deref() != disk_body {
            return Err(invalid(
                "Discord service unit changed during manager operation",
            ));
        }
        let loaded_matches = manager_bodies.iter().any(|body| {
            let Ok(program) = definition::bound_systemd_program(body) else {
                return false;
            };
            let Ok(place) = definition::bound_place(self.platform, body) else {
                return false;
            };
            let Some(place) = place.to_str() else {
                return false;
            };
            let argv = [
                program.as_str(),
                "discord",
                "run",
                "--state-dir",
                place,
                "--unattended",
            ]
            .join(" ");
            self.path.to_str() == Some(*fragment)
                && dropins.is_empty()
                && systemd_exec_matches(exec_start, &program, &argv)
        });
        if !loaded_matches {
            let unloaded = fragment.is_empty() && dropins.is_empty() && exec_start.is_empty();
            if !allow_unloaded || !unloaded {
                return Err(invalid(if manager_bodies.is_empty() {
                    "loaded Discord service has no owned definition"
                } else {
                    "loaded Discord unit differs from its previous definition"
                }));
            }
        }
        if active && pid == 0 {
            return Err(invalid("active Discord service has no main process"));
        }
        if active {
            let processes = process_bodies.iter().any(|body| {
                let Ok(program) = definition::bound_systemd_program(body) else {
                    return false;
                };
                let Ok(place) = definition::bound_place(self.platform, body) else {
                    return false;
                };
                let Ok(place) = place.into_os_string().into_string() else {
                    return false;
                };
                let argv = vec![
                    program.clone(),
                    "discord".to_owned(),
                    "run".to_owned(),
                    "--state-dir".to_owned(),
                    place,
                    "--unattended".to_owned(),
                ];
                verify_systemd_process(pid, &program, &argv).is_ok()
            });
            if !processes {
                return Err(invalid(
                    "running Discord process differs from its previous command",
                ));
            }
        }
        Ok(())
    }

    fn prior_state(&self, expected_body: Option<&str>) -> io::Result<(bool, bool)> {
        if self.platform == Platform::Systemd {
            let enabled = self.systemd_state("is-enabled")?;
            let active = self.systemd_state("is-active")?;
            let bodies = expected_body.into_iter().collect::<Vec<_>>();
            self.verify_systemd(&bodies, &bodies, expected_body, true)?;
            return Ok((enabled, active));
        }
        let target = format!("gui/{}/{LABEL}", runtime::uid());
        let output = (self.runner)("launchctl", &["print", &target])?;
        if output.status.success() {
            let body = expected_body
                .ok_or_else(|| invalid("loaded Discord LaunchAgent has no owned definition"))?;
            verify_launchd_body(&String::from_utf8_lossy(&output.stdout), &self.path, body)?;
            return Ok((true, true));
        }
        let detail = String::from_utf8_lossy(&output.stderr).to_ascii_lowercase();
        if detail.contains("could not find service") || detail.contains("service not found") {
            return Ok((false, false));
        }
        Err(invalid("unable to establish Discord LaunchAgent state"))
    }

    fn apply_manager(
        &self,
        enabled: bool,
        active: bool,
        previous_body: Option<&str>,
        body: Option<&str>,
        changed: &mut bool,
    ) -> io::Result<()> {
        match self.platform {
            Platform::Systemd => {
                let allowed = [previous_body, body]
                    .into_iter()
                    .flatten()
                    .collect::<Vec<_>>();
                self.verify_systemd(&allowed, &allowed, body, true)?;
                *changed = true;
                self.run("systemctl", &["--user", "daemon-reload"])?;
                let loaded = body.into_iter().collect::<Vec<_>>();
                let running = previous_body.into_iter().chain(body).collect::<Vec<_>>();
                self.verify_systemd(&loaded, &running, body, false)?;
                *changed = true;
                self.run(
                    "systemctl",
                    &[
                        "--user",
                        if enabled { "enable" } else { "disable" },
                        SERVICE,
                    ],
                )?;
                self.verify_systemd(&loaded, &running, body, false)?;
                *changed = true;
                self.run(
                    "systemctl",
                    &["--user", if active { "restart" } else { "stop" }, SERVICE],
                )
            }
            Platform::Launchd => {
                let domain = format!("gui/{}", runtime::uid());
                let name = format!("{domain}/{LABEL}");
                let output = (self.runner)("launchctl", &["print", &name])?;
                if output.status.success() {
                    let expected = previous_body.ok_or_else(|| {
                        invalid("loaded Discord LaunchAgent appeared during activation")
                    })?;
                    verify_launchd_body(
                        &String::from_utf8_lossy(&output.stdout),
                        &self.path,
                        expected,
                    )?;
                    *changed = true;
                    self.run("launchctl", &["bootout", &name])?;
                } else {
                    let detail = String::from_utf8_lossy(&output.stderr).to_ascii_lowercase();
                    if !detail.contains("could not find service")
                        && !detail.contains("service not found")
                    {
                        return Err(invalid("unable to establish Discord LaunchAgent state"));
                    }
                }
                if active {
                    let path = self
                        .path
                        .to_str()
                        .ok_or_else(|| invalid("invalid LaunchAgent path"))?;
                    *changed = true;
                    self.run("launchctl", &["bootstrap", &domain, path])?;
                }
                Ok(())
            }
        }
    }

    fn install(&self, binary: &Path, place: &Path) -> io::Result<()> {
        let body = definition::render(self.platform, binary, place)?;
        let old = self.read()?;
        let previous_mode = if old.is_some() {
            fs::symlink_metadata(&self.path)?.mode() & 0o777
        } else {
            0o600
        };
        let (enabled, active) = self.prior_state(old.as_deref())?;
        if old.is_none() && (enabled || active) {
            return Err(invalid("loaded Discord service has no owned definition"));
        }
        self.write(Some(&body), 0o600)?;
        let mut manager_changed = false;
        if let Err(error) = self.apply_manager(
            true,
            true,
            old.as_deref(),
            Some(&body),
            &mut manager_changed,
        ) {
            let rollback = (|| {
                if self.read()?.as_deref() != Some(&body) {
                    return Err(invalid(
                        "Discord service definition changed during activation",
                    ));
                }
                if old.is_none() {
                    let stopped = if manager_changed {
                        self.apply_manager(
                            false,
                            false,
                            Some(&body),
                            Some(&body),
                            &mut manager_changed,
                        )
                    } else {
                        Ok(())
                    };
                    self.write(None, 0)?;
                    stopped?;
                    if manager_changed && self.platform == Platform::Systemd {
                        self.run("systemctl", &["--user", "daemon-reload"])?;
                    }
                } else {
                    self.write(old.as_deref(), previous_mode)?;
                    if manager_changed {
                        self.apply_manager(
                            enabled,
                            active,
                            Some(&body),
                            Some(old.as_deref().unwrap()),
                            &mut manager_changed,
                        )?;
                    }
                }
                Ok(())
            })();
            return Err(match rollback {
                Ok(()) => error,
                Err(undo) => io::Error::other(format!(
                    "{error}; restoring previous Discord service: {undo}"
                )),
            });
        }
        Ok(())
    }

    fn stop(&self, place: &Path) -> io::Result<()> {
        let Some(body) = self.read()? else {
            return Ok(());
        };
        if definition::bound_place(self.platform, &body)? != place {
            return Ok(());
        }
        let (enabled, active) = self.prior_state(Some(&body))?;
        let mut manager_changed = false;
        if (enabled || active)
            && let Err(error) =
                self.apply_manager(false, false, Some(&body), Some(&body), &mut manager_changed)
        {
            return match if manager_changed {
                self.apply_manager(
                    enabled,
                    active,
                    Some(&body),
                    Some(&body),
                    &mut manager_changed,
                )
            } else {
                Ok(())
            } {
                Ok(()) => Err(error),
                Err(undo) => Err(io::Error::other(format!(
                    "{error}; restoring Discord service: {undo}"
                ))),
            };
        }
        if self.read()?.as_deref() != Some(&body) {
            return Err(invalid("Discord service definition changed while stopping"));
        }
        self.write(None, 0)?;
        if self.platform == Platform::Systemd {
            self.run("systemctl", &["--user", "daemon-reload"])?;
        }
        Ok(())
    }
}

fn verify_launchd_print(
    output: &str,
    plist: &Path,
    expected_args: &[&str],
    log: &Path,
) -> io::Result<()> {
    let field = |name: &str| {
        output.lines().find_map(|line| {
            line.trim()
                .strip_prefix(name)
                .and_then(|value| value.strip_prefix(" = "))
        })
    };
    let expected_path = plist
        .to_str()
        .ok_or_else(|| invalid("invalid LaunchAgent path"))?;
    if field("path") != Some(expected_path)
        || field("program") != expected_args.first().copied()
        || field("stdout path") != log.to_str()
        || field("stderr path") != log.to_str()
    {
        return Err(invalid(
            "loaded Discord LaunchAgent differs from its definition",
        ));
    }
    let args_start = output
        .find("arguments = {")
        .ok_or_else(|| invalid("LaunchAgent arguments are unavailable"))?;
    let args = output[args_start + "arguments = {".len()..]
        .split_once('}')
        .ok_or_else(|| invalid("LaunchAgent arguments are malformed"))?
        .0
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .collect::<Vec<_>>();
    if args != expected_args {
        return Err(invalid(
            "loaded Discord LaunchAgent arguments differ from its definition",
        ));
    }
    Ok(())
}

fn verify_launchd_body(output: &str, plist: &Path, body: &str) -> io::Result<()> {
    let place = definition::bound_place(Platform::Launchd, body)?;
    let binary = definition::bound_program(body)?;
    let args = [
        binary.as_str(),
        "discord",
        "run",
        "--state-dir",
        place
            .to_str()
            .ok_or_else(|| invalid("invalid Discord state path"))?,
        "--unattended",
    ];
    verify_launchd_print(output, plist, &args, &place.join("connector.log"))
}

fn systemd_exec_matches(value: &str, program: &str, argv: &str) -> bool {
    if program.contains(" ; argv[]=") || argv.contains(" ; ignore_errors=") {
        return false;
    }
    value
        .strip_prefix("{ path=")
        .and_then(|rest| rest.strip_prefix(program))
        .and_then(|rest| rest.strip_prefix(" ; argv[]="))
        .and_then(|rest| rest.strip_prefix(argv))
        .is_some_and(|rest| {
            rest.starts_with(" ; ignore_errors=no ; start_time=[")
                && rest.ends_with(" }")
                && !rest.contains(" } {")
        })
}

fn verify_systemd_process(pid: u32, program: &str, expected_argv: &[String]) -> io::Result<()> {
    verify_systemd_process_at(Path::new("/proc"), pid, program, expected_argv)
}

fn verify_systemd_process_at(
    proc_root: &Path,
    pid: u32,
    program: &str,
    expected_argv: &[String],
) -> io::Result<()> {
    let proc = proc_root.join(pid.to_string());
    let status = fs::read_to_string(proc.join("status"))?;
    let uid = status
        .lines()
        .find_map(|line| line.strip_prefix("Uid:"))
        .and_then(|line| line.split_whitespace().next())
        .and_then(|uid| uid.parse::<u32>().ok());
    if uid != Some(runtime::uid()) {
        return Err(invalid("Discord service process has a foreign owner"));
    }
    let raw = fs::read(proc.join("cmdline"))?;
    let args = raw
        .split(|byte| *byte == 0)
        .filter(|arg| !arg.is_empty())
        .collect::<Vec<_>>();
    let expected = expected_argv
        .iter()
        .map(String::as_bytes)
        .collect::<Vec<_>>();
    if args != expected {
        return Err(invalid(
            "running Discord process differs from its previous command",
        ));
    }
    let actual_exe = fs::read_link(proc.join("exe"))?;
    let expected_exe = Path::new(program).canonicalize()?;
    if actual_exe != expected_exe {
        return Err(invalid(
            "running Discord executable differs from its previous command",
        ));
    }
    Ok(())
}

#[cfg(test)]
mod ownership_tests {
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
            fs::create_dir_all(&dir).unwrap();
            let old = definition::render(
                Platform::Systemd,
                Path::new("/bin/swarm"),
                Path::new("/tmp/place"),
            )
            .unwrap();
            fs::write(&path, &old).unwrap();
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
        fs::create_dir_all(&dir).unwrap();
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
        let dir =
            std::env::temp_dir().join(format!("swarm-launchd-recheck-{}", std::process::id()));
        fs::create_dir_all(&dir).unwrap();
        let plist = dir.join("com.swarm.discord.plist");
        let old = definition::render(
            Platform::Launchd,
            Path::new("/bin/swarm"),
            Path::new("/tmp/place"),
        )
        .unwrap();
        fs::write(&plist, &old).unwrap();
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
            let dir = std::env::temp_dir()
                .join(format!("swarm-unit-swap-{}-{phase}", std::process::id()));
            fs::create_dir_all(&dir).unwrap();
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
}

fn registration_lock() -> io::Result<File> {
    let directory = ipc::user_config_dir()?.join("swarm/installed-discord");
    runtime::private_dir(&directory)?;
    runtime::lock(&directory.join(".service.lock"))
}

pub(super) fn ensure_supervisor(cfg: &Config, unattended: bool) -> io::Result<()> {
    if !cfg.managed() {
        return Ok(());
    }
    let previous = &cfg.0["previous_supervisor"];
    if !previous.is_null() {
        let root = Path::new(ipc::field(previous, "state_dir"));
        let endpoint =
            crate::supervisor::endpoint(&root.canonicalize().unwrap_or_else(|_| root.to_owned()));
        if previous["endpoint"] != endpoint {
            return Err(invalid("previous supervisor identity changed"));
        }
    }
    let socket = Path::new(cfg.text("daemon_socket"));
    let binding = &cfg.0["supervisor"];
    if binding.is_null() {
        if socket != ipc::socket_path()? {
            return Err(invalid("select the original supervisor environment"));
        }
        let root = ipc::state_path()?;
        let lock = ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| root.join("daemon.lock"));
        crate::bootstrap::ensure_bound(
            socket,
            &root,
            &lock,
            &crate::clean::DiagnosticLog::path(&root),
            unattended,
        )?;
    } else {
        crate::bootstrap::ensure_bound(
            socket,
            Path::new(ipc::field(binding, "state_dir")),
            Path::new(ipc::field(binding, "lock")),
            Path::new(ipc::field(binding, "log")),
            unattended,
        )?;
    }
    if !previous.is_null()
        && let Err(error) = crate::bootstrap::ensure_bound(
            Path::new(ipc::field(previous, "socket")),
            Path::new(ipc::field(previous, "state_dir")),
            Path::new(ipc::field(previous, "lock")),
            Path::new(ipc::field(previous, "log")),
            true,
        )
    {
        eprintln!(
            "discord: existing discussions are waiting for their previous supervisor: {error}"
        );
    }
    Ok(())
}

pub(super) fn install(place: &Place, cfg: &Config) -> io::Result<()> {
    let _registration = registration_lock()?;
    let unit = Unit::current()?;
    unit.read()?;
    ensure_supervisor(cfg, false)?;
    unit.install(&crate::upgrade::launcher()?, &place.dir)
}

pub(super) fn disconnect(place: &Place) -> io::Result<()> {
    let unit = Unit::current()?;
    if unit.read()?.is_some() {
        let _registration = registration_lock()?;
        unit.stop(&place.dir)?;
        place.disconnect()
    } else {
        place.disconnect()
    }
}
