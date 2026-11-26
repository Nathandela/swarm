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

    fn prior_state(&self) -> io::Result<(bool, bool)> {
        if self.platform == Platform::Systemd {
            let enabled = self.systemd_state("is-enabled")?;
            let active = self.systemd_state("is-active")?;
            let output = (self.runner)(
                "systemctl",
                &[
                    "--user",
                    "show",
                    "--property=FragmentPath",
                    "--property=DropInPaths",
                    SERVICE,
                ],
            )?;
            if !output.status.success() {
                return Err(invalid("unable to establish Discord unit definition"));
            }
            let mut fragment: Option<String> = None;
            let mut dropins: Option<String> = None;
            let stdout = String::from_utf8_lossy(&output.stdout);
            for line in stdout.lines() {
                if let Some(value) = line.strip_prefix("FragmentPath=") {
                    if fragment.replace(value.to_owned()).is_some() {
                        return Err(invalid("ambiguous Discord unit fragment"));
                    }
                } else if let Some(value) = line.strip_prefix("DropInPaths=") {
                    if dropins.replace(value.to_owned()).is_some() {
                        return Err(invalid("ambiguous Discord unit drop-ins"));
                    }
                }
            }
            let fragment =
                fragment.ok_or_else(|| invalid("unable to establish Discord unit fragment"))?;
            let dropins =
                dropins.ok_or_else(|| invalid("unable to establish Discord unit drop-ins"))?;
            let expected = self
                .path
                .to_str()
                .ok_or_else(|| invalid("invalid Discord unit path"))?;
            if (!fragment.is_empty() && fragment != expected)
                || (!fragment.is_empty() && !dropins.is_empty())
                || ((enabled || active) && fragment.is_empty())
            {
                return Err(invalid(
                    "Discord service manager references a foreign definition",
                ));
            }
            return Ok((enabled, active));
        }
        let target = format!("gui/{}/{LABEL}", runtime::uid());
        let output = (self.runner)("launchctl", &["print", &target])?;
        if output.status.success() {
            let body = self
                .read()?
                .ok_or_else(|| invalid("loaded Discord LaunchAgent has no definition"))?;
            let place = definition::bound_place(self.platform, &body)?;
            let binary = definition::bound_program(&body)?;
            let expected_args = [
                binary.as_str(),
                "discord",
                "run",
                "--state-dir",
                place
                    .to_str()
                    .ok_or_else(|| invalid("invalid Discord state path"))?,
                "--unattended",
            ];
            let log = place.join("connector.log");
            verify_launchd_print(
                &String::from_utf8_lossy(&output.stdout),
                &self.path,
                &expected_args,
                &log,
            )?;
            return Ok((true, true));
        }
        let detail = String::from_utf8_lossy(&output.stderr).to_ascii_lowercase();
        if detail.contains("could not find service") || detail.contains("service not found") {
            return Ok((false, false));
        }
        Err(invalid("unable to establish Discord LaunchAgent state"))
    }

    fn apply_manager(&self, enabled: bool, active: bool) -> io::Result<()> {
        match self.platform {
            Platform::Systemd => {
                self.run("systemctl", &["--user", "daemon-reload"])?;
                self.run(
                    "systemctl",
                    &[
                        "--user",
                        if enabled { "enable" } else { "disable" },
                        SERVICE,
                    ],
                )?;
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
                    let path = self
                        .path
                        .to_str()
                        .ok_or_else(|| invalid("invalid LaunchAgent path"))?;
                    if String::from_utf8_lossy(&output.stdout)
                        .lines()
                        .find_map(|line| line.trim().strip_prefix("path = "))
                        != Some(path)
                    {
                        return Err(invalid("loaded Discord LaunchAgent path changed"));
                    }
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
        let (enabled, active) = self.prior_state()?;
        if old.is_none() && (enabled || active) {
            return Err(invalid("loaded Discord service has no owned definition"));
        }
        self.write(Some(&body), 0o600)?;
        if let Err(error) = self.apply_manager(true, true) {
            let rollback = (|| {
                if self.read()?.as_deref() != Some(&body) {
                    return Err(invalid(
                        "Discord service definition changed during activation",
                    ));
                }
                if old.is_none() {
                    let stopped = self.apply_manager(false, false);
                    self.write(None, 0)?;
                    stopped?;
                    if self.platform == Platform::Systemd {
                        self.run("systemctl", &["--user", "daemon-reload"])?;
                    }
                } else {
                    self.write(old.as_deref(), previous_mode)?;
                    self.apply_manager(enabled, active)?;
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
        let (enabled, active) = self.prior_state()?;
        if (enabled || active)
            && let Err(error) = self.apply_manager(false, false)
        {
            return match self.apply_manager(enabled, active) {
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

#[cfg(test)]
mod ownership_tests {
    use super::*;
    use std::{cell::RefCell, os::unix::process::ExitStatusExt};

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

    #[test]
    fn systemd_reports_fragment_and_dropins_before_mutation() {
        for manager_state in [
            "FragmentPath=/tmp/foreign.service\nDropInPaths=\n",
            "FragmentPath=/tmp/swarm-discord.service\nDropInPaths=/tmp/override.conf\n",
        ] {
            let dir = std::env::temp_dir().join(format!("swarm-unit-{}", std::process::id()));
            let path = dir.join(SERVICE);
            fs::create_dir_all(&dir).unwrap();
            fs::write(
                &path,
                definition::render(
                    Platform::Systemd,
                    Path::new("/bin/swarm"),
                    Path::new("/tmp/place"),
                )
                .unwrap(),
            )
            .unwrap();
            let mutations = RefCell::new(Vec::new());
            let runner = |name: &str, args: &[&str]| -> io::Result<Output> {
                if args.contains(&"show") {
                    return Ok(output(manager_state, "", true));
                }
                if args.contains(&"is-enabled") {
                    return Ok(output("enabled\n", "", true));
                }
                if args.contains(&"is-active") {
                    return Ok(output("active\n", "", true));
                }
                mutations.borrow_mut().push(format!("{name} {args:?}"));
                Ok(output("", "", true))
            };
            let service = unit(Platform::Systemd, path.clone(), &runner);
            assert!(
                service
                    .install(Path::new("/bin/new-swarm"), Path::new("/tmp/place"))
                    .is_err()
            );
            assert!(service.stop(Path::new("/tmp/place")).is_err());
            assert!(mutations.borrow().is_empty());
            assert_eq!(
                service.read().unwrap().unwrap(),
                fs::read_to_string(&path).unwrap()
            );
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
