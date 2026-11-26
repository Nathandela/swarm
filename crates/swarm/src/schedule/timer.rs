use super::{Result, home};
use crate::service_unit::{Platform, systemd_string, xml_text};
use std::{
    env,
    fs::{self, DirBuilder, File, OpenOptions},
    io::{self, Read, Write},
    os::{
        fd::{AsRawFd, OwnedFd},
        unix::{
            fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt},
            net::UnixStream,
            process::CommandExt,
        },
    },
    path::{Path, PathBuf},
    process::{Command, Output, Stdio},
    sync::atomic::{AtomicBool, Ordering},
    time::{Duration, Instant},
};

const LABEL: &str = "com.swarm.schedule";
const TIMER: &str = "swarm-schedule.timer";
const SERVICE: &str = "swarm-schedule.service";

fn local_manager<'a>(run: &'a Run<'a>) -> Result<Manager<'a>> {
    let home = home()?;
    safe_path(&home)?;
    let platform = match env::consts::OS {
        "macos" => Platform::Launchd,
        "linux" => Platform::Systemd,
        other => return Err(format!("scheduletimer: unsupported platform {other:?}").into()),
    };
    let paths = match platform {
        Platform::Launchd => vec![
            home.join("Library/LaunchAgents")
                .join(format!("{LABEL}.plist")),
        ],
        Platform::Systemd => {
            let dir = crate::ipc::env_path("XDG_CONFIG_HOME")
                .filter(|p| p.is_absolute())
                .unwrap_or_else(|| home.join(".config"))
                .join("systemd/user");
            vec![dir.join(SERVICE), dir.join(TIMER)]
        }
    };
    Ok(Manager {
        platform,
        paths,
        run,
    })
}

pub(super) fn command(verb: &str) -> Result<Option<String>> {
    let run = |name: &str, args: &[&str]| Command::new(name).args(args).env("LC_ALL", "C").output();
    let manager = local_manager(&run)?;
    match verb {
        "status" => Ok(Some(manager.status()?)),
        "install" => {
            super::rpc::qualified_timer()?;
            let exe = stable_executable()?;
            let state = crate::ipc::state_path()?;
            let bindings = [
                ("SWARM_DAEMON_STATE", state.clone()),
                ("SWARM_DAEMON_SOCK", crate::ipc::socket_path()?),
                (
                    "SWARM_DAEMON_LOCK",
                    crate::ipc::env_path("SWARM_DAEMON_LOCK")
                        .unwrap_or_else(|| state.join("daemon.lock")),
                ),
                (
                    "SWARM_DAEMON_LOG",
                    crate::ipc::env_path("SWARM_DAEMON_LOG")
                        .unwrap_or_else(|| state.join("daemon.log")),
                ),
            ];
            let env = bindings
                .iter()
                .map(|(key, path)| Ok((*key, safe_path(path)?)))
                .collect::<Result<Vec<_>>>()?;
            manager.change(Some(render(manager.platform, &exe, &env)))?;
            Ok(None)
        }
        "uninstall" => {
            manager.change(None)?;
            Ok(None)
        }
        _ => unreachable!(),
    }
}

pub(super) fn board_status(cancel: &AtomicBool) -> Result<String> {
    let run = |name: &str, args: &[&str]| status_output(name, args, cancel);
    local_manager(&run)?.status()
}

fn status_pipe() -> io::Result<(UnixStream, Stdio)> {
    let (reader, writer) = UnixStream::pair()?;
    reader.set_nonblocking(true)?;
    Ok((reader, Stdio::from(OwnedFd::from(writer))))
}

fn read_status(pipe: &mut UnixStream, bytes: &mut Vec<u8>) -> io::Result<bool> {
    let mut buffer = [0; 4096];
    match pipe.read(&mut buffer) {
        Ok(0) => Ok(false),
        Ok(count) => {
            if bytes.len() + count > 64 * 1024 {
                return Err(io::Error::other("timer manager output exceeds 64 KiB"));
            }
            bytes.extend_from_slice(&buffer[..count]);
            Ok(true)
        }
        Err(error) if error.kind() == io::ErrorKind::WouldBlock => Ok(false),
        Err(error) if error.kind() == io::ErrorKind::Interrupted => Ok(true),
        Err(error) => Err(error),
    }
}

fn status_output(name: &str, args: &[&str], cancel: &AtomicBool) -> io::Result<Output> {
    let deadline = Instant::now() + Duration::from_secs(3);
    if cancel.load(Ordering::Relaxed) {
        return Err(io::ErrorKind::Interrupted.into());
    }
    let (mut stdout, child_stdout) = status_pipe()?;
    let (mut stderr, child_stderr) = status_pipe()?;
    let mut child = Command::new(name)
        .args(args)
        .env("LC_ALL", "C")
        .stdin(Stdio::null())
        .stdout(child_stdout)
        .stderr(child_stderr)
        .process_group(0)
        .spawn()?;
    let mut out = Vec::new();
    let mut err = Vec::new();
    let completed = (|| -> io::Result<()> {
        loop {
            if cancel.load(Ordering::Relaxed) {
                return Err(io::ErrorKind::Interrupted.into());
            }
            if Instant::now() >= deadline {
                return Err(io::ErrorKind::TimedOut.into());
            }
            let read_stdout = read_status(&mut stdout, &mut out)?;
            let read_stderr = read_status(&mut stderr, &mut err)?;
            if !read_stdout && !read_stderr {
                if crate::supervisor::runtime::child_exited(&child)? {
                    return Ok(());
                }
                std::thread::sleep(Duration::from_millis(10));
            }
        }
    })();
    // The unreaped child anchors the dedicated group, including inherited pipes.
    if unsafe { libc::kill(-(child.id() as libc::pid_t), libc::SIGKILL) } < 0 {
        let _ = child.kill();
    }
    let status = child.wait();
    completed?;
    let status = status?;
    while read_status(&mut stdout, &mut out)? | read_status(&mut stderr, &mut err)? {
        if Instant::now() >= deadline {
            return Err(io::ErrorKind::TimedOut.into());
        }
    }
    Ok(Output {
        status,
        stdout: out,
        stderr: err,
    })
}

type Run<'a> = dyn Fn(&str, &[&str]) -> io::Result<Output> + 'a;

struct Manager<'a> {
    platform: Platform,
    paths: Vec<PathBuf>,
    run: &'a Run<'a>,
}

#[derive(Default)]
struct State {
    active: String,
    enabled: String,
    path: PathBuf,
    drops: bool,
}

impl Manager<'_> {
    fn raw(&self, args: &[&str]) -> Result<(Output, String)> {
        let (name, args) = match self.platform {
            Platform::Launchd => ("launchctl", args.to_vec()),
            Platform::Systemd => ("systemctl", [&["--user"][..], args].concat()),
        };
        let output =
            (self.run)(name, &args).map_err(|e| format!("{name} {}: {e}", args.join(" ")))?;
        let mut status = String::from_utf8_lossy(&output.stdout).into_owned();
        status.push_str(&String::from_utf8_lossy(&output.stderr));
        Ok((output, status))
    }

    fn ctl(&self, args: &[&str]) -> Result<()> {
        let (output, text) = self.raw(args)?;
        check(&output, args, &text)
    }

    fn inspect(&self) -> Result<State> {
        let mut state = State {
            active: "inactive".into(),
            ..Default::default()
        };
        if self.platform == Platform::Launchd {
            let args = ["print", &job_domain()];
            let (output, text) = self.raw(&args)?;
            if !output.status.success()
                && text.contains(&format!("Could not find service \"{LABEL}\" in domain"))
            {
                return Ok(state);
            }
            check(&output, &args, &text)?;
            let path = text
                .lines()
                .find_map(|line| line.trim().strip_prefix("path = "))
                .filter(|s| !s.is_empty())
                .ok_or("loaded launchd job has no definition path")?;
            state.active = "active".into();
            state.path = path.into();
            return Ok(state);
        }
        let args = [
            "show",
            "--property=LoadState,ActiveState,UnitFileState,FragmentPath,DropInPaths",
            TIMER,
        ];
        let (output, text) = self.raw(&args)?;
        let mut load = "";
        state.active.clear();
        for (key, value) in text.lines().filter_map(|line| line.split_once('=')) {
            match key {
                "LoadState" => load = value,
                "ActiveState" => state.active = value.into(),
                "UnitFileState" => state.enabled = value.into(),
                "FragmentPath" => state.path = value.into(),
                "DropInPaths" => state.drops = !value.is_empty(),
                _ => {}
            }
        }
        if load == "not-found"
            && state.active == "inactive"
            && state.path.as_os_str().is_empty()
            && (output.status.success() || output.status.code() == Some(4))
        {
            return Ok(State {
                active: "inactive".into(),
                ..Default::default()
            });
        }
        check(&output, &args, &text)?;
        if load != "loaded"
            || state.active.is_empty()
            || state.enabled.is_empty()
            || state.path.as_os_str().is_empty()
        {
            return Err(format!("unsupported systemd status: {text:?}").into());
        }
        Ok(state)
    }

    fn status(&self) -> Result<String> {
        let mut count = 0usize;
        for path in &self.paths {
            if definition(path)?.is_some() {
                count += 1;
            }
        }
        let installed = match count {
            0 => "not installed",
            n if n == self.paths.len() => "installed",
            _ => "partially installed",
        };
        let state = self
            .inspect()
            .map_err(|e| format!("{installed}; runtime status unknown: {e}"))?;
        Ok(format!("{installed}, {}", state.active))
    }

    fn check_systemd_service(&self) -> Result<()> {
        let args = [
            "show",
            "--property=LoadState,FragmentPath,DropInPaths",
            SERVICE,
        ];
        let (output, text) = self.raw(&args)?;
        let value = |field| {
            text.lines()
                .filter_map(|line| line.split_once('='))
                .find_map(|(key, value)| (key == field).then_some(value))
                .unwrap_or_default()
        };
        let origin = Path::new(value("FragmentPath"));
        if !value("DropInPaths").is_empty() {
            return Err("cannot replace Swarm timer with a systemd service override".into());
        }
        if value("LoadState") == "not-found"
            && origin.as_os_str().is_empty()
            && (output.status.success() || output.status.code() == Some(4))
        {
            return Ok(());
        }
        check(&output, &args, &text)?;
        if value("LoadState") != "loaded"
            || (origin != self.paths[0] && !same_file(origin, &self.paths[0]))
        {
            return Err(format!(
                "timer manager uses another service definition at {}",
                origin.display()
            )
            .into());
        }
        Ok(())
    }

    fn change(&self, rendered: Option<Vec<String>>) -> Result<()> {
        let install = rendered.is_some();
        let dir = self.paths[0].parent().ok_or("invalid timer path")?;
        if install {
            DirBuilder::new().mode(0o700).recursive(true).create(dir)?;
        }
        let _lock = match File::open(dir) {
            Ok(file) => {
                // Keep the same directory lock as the Go installer, through rollback.
                loop {
                    if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX) } == 0 {
                        break;
                    }
                    let err = io::Error::last_os_error();
                    if err.kind() != io::ErrorKind::Interrupted {
                        return Err(err.into());
                    }
                }
                Some(file)
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => None,
            Err(e) => return Err(e.into()),
        };
        let old = self
            .paths
            .iter()
            .map(|p| definition(p))
            .collect::<Result<Vec<_>>>()?;
        let state = self.inspect()?;
        if self.platform == Platform::Systemd {
            if state.drops {
                return Err("cannot replace Swarm timer with a systemd timer override".into());
            }
            self.check_systemd_service()?;
        }
        let old_path = self.paths.last().ok_or("no timer paths")?;
        let has_path = !state.path.as_os_str().is_empty();
        if has_path && state.path != *old_path && !same_file(&state.path, old_path) {
            return Err(format!(
                "timer manager uses another definition at {}",
                state.path.display()
            )
            .into());
        }
        let active = state.active == "active";
        if active && (!has_path || old.last().is_none_or(Option::is_none)) {
            return Err("loaded timer has no restorable definition".into());
        }
        if !matches!(state.active.as_str(), "active" | "inactive") {
            return Err(format!("cannot preserve timer state {:?}", state.active).into());
        }
        if !matches!(state.enabled.as_str(), "" | "enabled" | "disabled") {
            return Err(format!("cannot preserve unit file state {:?}", state.enabled).into());
        }
        let next: Vec<_> = match rendered {
            Some(files) => files
                .into_iter()
                .map(|text| {
                    Some(Definition {
                        data: text.into_bytes(),
                        mode: 0o600,
                    })
                })
                .collect(),
            None => (0..old.len()).map(|_| None).collect(),
        };
        let edits: Vec<_> = old
            .iter()
            .zip(next.iter())
            .enumerate()
            .filter(|(_, (a, b))| a != b)
            .collect();
        let changed = !edits.is_empty();
        let linux = self.platform == Platform::Systemd;
        let set_active = |activate| match (self.platform, activate) {
            (Platform::Launchd, true) => self.ctl(&["bootstrap", &domain(), safe_path(old_path)?]),
            (Platform::Launchd, false) => self.ctl(&["bootout", &job_domain()]),
            (Platform::Systemd, true) => self.ctl(&["start", TIMER]),
            (Platform::Systemd, false) => self.ctl(&["stop", TIMER]),
        };
        let set_enabled = |on| self.ctl(&[if on { "enable" } else { "disable" }, TIMER]);
        let mut undo: Vec<Box<dyn FnOnce() -> Result<()>>> = Vec::new();
        let result: Result<()> = (|| {
            if active && (!install || changed) {
                undo.push(Box::new(|| {
                    if self.inspect()?.active == "inactive" {
                        set_active(true)?;
                    }
                    Ok(())
                }));
                set_active(false)?;
            }
            if !install && linux && state.enabled == "enabled" {
                undo.push(Box::new(|| {
                    if self.inspect()?.enabled != "enabled" {
                        set_enabled(true)?;
                    }
                    Ok(())
                }));
                set_enabled(false)?;
            }
            let reload = linux && (changed || install && !has_path);
            if reload {
                undo.push(Box::new(|| self.ctl(&["daemon-reload"])));
            }
            for (index, (before, after)) in edits {
                let path = &self.paths[index];
                undo.push(Box::new(move || apply(path, before.as_ref())));
                apply(path, after.as_ref())?;
            }
            if reload {
                self.ctl(&["daemon-reload"])?;
            }
            if install && linux && state.enabled != "enabled" {
                undo.push(Box::new(|| {
                    if self.inspect()?.enabled == "enabled" {
                        set_enabled(false)?;
                    }
                    Ok(())
                }));
                set_enabled(true)?;
            }
            if install && (!active || changed) {
                undo.push(Box::new(|| {
                    if self.inspect()?.active == "active" {
                        set_active(false)?;
                    }
                    Ok(())
                }));
                set_active(true)?;
            }
            Ok(())
        })();
        if let Err(e) = result {
            let mut errors = e.to_string();
            for f in undo.into_iter().rev() {
                if let Err(e) = f() {
                    errors.push_str(&format!("\ntimer rollback: {e}"));
                }
            }
            return Err(errors.into());
        }
        Ok(())
    }
}

fn check(output: &Output, args: &[&str], text: &str) -> Result<()> {
    if !output.status.success() {
        return Err(format!("{}: {}: {}", args.join(" "), output.status, text.trim()).into());
    }
    Ok(())
}

#[derive(PartialEq)]
struct Definition {
    data: Vec<u8>,
    mode: u32,
}

fn definition(path: &Path) -> Result<Option<Definition>> {
    let mut file = match OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)
    {
        Ok(file) => file,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e.into()),
    };
    let meta = file.metadata()?;
    if !meta.is_file() {
        return Err(format!("expected regular unit file at {}", path.display()).into());
    }
    let mut data = Vec::new();
    file.read_to_end(&mut data)?;
    Ok(Some(Definition {
        data,
        mode: meta.mode() & 0o777,
    }))
}

fn apply(path: &Path, def: Option<&Definition>) -> Result<()> {
    let parent = path.parent().ok_or("invalid timer path")?;
    if let Some(def) = def {
        let temp = parent.join(format!(
            ".swarm-schedule-{}",
            crate::supervisor::runtime::random_id()?
        ));
        let mut file = OpenOptions::new()
            .create_new(true)
            .write(true)
            .mode(0o600)
            .open(&temp)?;
        let result = (|| -> io::Result<()> {
            file.write_all(&def.data)?;
            file.set_permissions(fs::Permissions::from_mode(def.mode))?;
            file.sync_all()?;
            fs::rename(&temp, path)
        })();
        if let Err(e) = result {
            let _ = fs::remove_file(&temp);
            return Err(e.into());
        }
    } else {
        match fs::remove_file(path) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(e) => return Err(e.into()),
        }
    }
    File::open(parent)?.sync_all()?;
    Ok(())
}

fn domain() -> String {
    // launchctl identifies the current GUI session by the invoking uid.
    format!("gui/{}", unsafe { libc::getuid() })
}

fn job_domain() -> String {
    format!("{}/{LABEL}", domain())
}

fn safe_path(path: &Path) -> Result<&str> {
    path.to_str()
        .filter(|s| {
            path.is_absolute()
                && !s
                    .chars()
                    .any(|c| c.is_control() || matches!(c, '\u{fffe}' | '\u{ffff}'))
        })
        .ok_or_else(|| format!("invalid timer path {path:?}").into())
}

fn same_file(a: &Path, b: &Path) -> bool {
    fs::metadata(a)
        .ok()
        .zip(fs::metadata(b).ok())
        .is_some_and(|(a, b)| a.dev() == b.dev() && a.ino() == b.ino())
}

fn versioned(path: &str) -> Option<&str> {
    ["/Caskroom/", "/Cellar/"]
        .into_iter()
        .find_map(|marker| path.split_once(marker).map(|(prefix, _)| prefix))
}

fn stable_executable() -> Result<String> {
    let exe = crate::upgrade::launcher()?;
    let exe_str = safe_path(&exe)?;
    let info = fs::metadata(&exe)?;
    if !info.is_file() || info.mode() & 0o111 == 0 {
        return Err(format!("{exe:?} is not executable").into());
    }
    let mut candidates = Vec::new();
    if let Some(prefix) = versioned(exe_str) {
        candidates.push(Path::new(prefix).join("bin/swarm"));
    }
    if let Some(path) = env::var_os("PATH") {
        candidates.extend(env::split_paths(&path).map(|dir| dir.join("swarm")));
    }
    if let Some(arg) = env::args_os().next() {
        candidates.push(arg.into());
    }
    candidates.push(exe.clone());
    for path in candidates {
        if let Ok(path_str) = safe_path(&path)
            && versioned(path_str).is_none()
            && same_file(&exe, &path)
        {
            return Ok(path_str.into());
        }
    }
    Err(format!("cannot locate a stable executable link for {exe:?}").into())
}

fn render(platform: Platform, exe: &str, env: &[(&str, &str)]) -> Vec<String> {
    match platform {
        Platform::Launchd => {
            let entries: String = env
                .iter()
                .map(|(key, value)| format!("<key>{key}</key><string>{}</string>", xml_text(value)))
                .collect();
            let minutes: String = (0..60)
                .map(|m| format!("<dict><key>Minute</key><integer>{m}</integer></dict>"))
                .collect();
            vec![format!(
                concat!(
                    "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n",
                    "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n",
                    "<plist version=\"1.0\"><dict>\n",
                    "<key>Label</key><string>{}</string>\n",
                    "<key>ProgramArguments</key><array><string>{}</string><string>schedule</string><string>tick</string></array>\n",
                    "<key>EnvironmentVariables</key><dict>{}</dict>\n",
                    "<key>AbandonProcessGroup</key><true/>\n",
                    "<key>StartCalendarInterval</key><array>{}</array>\n",
                    "</dict></plist>\n"
                ),
                LABEL,
                xml_text(exe),
                entries,
                minutes
            )]
        }
        Platform::Systemd => {
            let entries: String = env
                .iter()
                .map(|(k, v)| format!("Environment=\"{}\"\n", systemd_string(&format!("{k}={v}"))))
                .collect();
            vec![
                format!(concat!(
                    "[Unit]\nDescription=Swarm schedule tick\n[Service]\nType=oneshot\nKillMode=process\n",
                    "{}ExecStart=:/bin/sh -c 'exec \"$@\"' swarm-schedule \"{}\" schedule tick\n"
                ), entries, systemd_string(exe)),
                "[Unit]\nDescription=Run the Swarm schedule tick every minute\n[Timer]\nOnCalendar=*-*-* *:*:00\nPersistent=true\nAccuracySec=1s\nUnit=swarm-schedule.service\n[Install]\nWantedBy=timers.target\n".into(),
            ]
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::schedule::Temp;
    use std::{cell::Cell, os::unix::process::ExitStatusExt, process::ExitStatus};

    fn output(code: i32, stdout: String) -> io::Result<Output> {
        Ok(Output {
            status: ExitStatus::from_raw(code << 8),
            stdout: stdout.into_bytes(),
            stderr: Vec::new(),
        })
    }

    #[test]
    fn failed_start_restores_prior_systemd_definitions_and_activity() {
        let dir = Temp::new();
        let paths = vec![dir.0.join(SERVICE), dir.0.join(TIMER)];
        for path in &paths {
            fs::write(path, b"old definition\0").unwrap();
            fs::set_permissions(path, fs::Permissions::from_mode(0o640)).unwrap();
        }
        let active = Cell::new(true);
        let enabled = Cell::new(true);
        let run = |name: &str, args: &[&str]| {
            assert_eq!(name, "systemctl");
            assert_eq!(args[0], "--user");
            match args[1] {
                "show" if args.last() == Some(&SERVICE) => output(
                    0,
                    format!(
                        "LoadState=loaded\nFragmentPath={}\nDropInPaths=\n",
                        paths[0].display()
                    ),
                ),
                "show" => output(
                    0,
                    format!(
                        "LoadState=loaded\nActiveState={}\nUnitFileState={}\nFragmentPath={}\n",
                        if active.get() { "active" } else { "inactive" },
                        if enabled.get() { "enabled" } else { "disabled" },
                        paths[1].display()
                    ),
                ),
                "stop" => {
                    active.set(false);
                    output(0, "".into())
                }
                "start" => {
                    active.set(true);
                    let new = fs::read_to_string(&paths[0]).unwrap().starts_with("[Unit]");
                    output(i32::from(new), "fixture rejected activation".into())
                }
                "daemon-reload" => output(0, "".into()),
                other => panic!("Unexpected fake systemctl command {other}"),
            }
        };
        let manager = Manager {
            platform: Platform::Systemd,
            paths: paths.clone(),
            run: &run,
        };
        assert_eq!(manager.status().unwrap(), "installed, active");
        let units = render(
            Platform::Systemd,
            "/tmp/swarm =%$&",
            &[("SWARM_DAEMON_STATE", "/tmp/own=%")],
        );
        assert!(
            units[0].contains(
                "ExecStart=:/bin/sh -c 'exec \"$@\"' swarm-schedule \"/tmp/swarm =%%$&\""
            )
        );
        assert!(units[1].contains("Persistent=true"));
        assert!(
            manager
                .change(Some(units))
                .unwrap_err()
                .to_string()
                .contains("fixture rejected activation")
        );
        assert!(active.get() && enabled.get());
        for path in &paths {
            assert_eq!(fs::read(path).unwrap(), b"old definition\0");
            assert_eq!(fs::metadata(path).unwrap().mode() & 0o777, 0o640);
        }
    }

    #[test]
    fn systemd_refuses_foreign_companion_before_mutation() {
        let dir = Temp::new();
        let paths = vec![dir.0.join(SERVICE), dir.0.join(TIMER)];
        fs::write(&paths[0], b"previous service").unwrap();
        fs::write(&paths[1], b"previous timer").unwrap();
        let run = |_: &str, args: &[&str]| {
            assert_eq!(args[1], "show");
            if args.last() == Some(&SERVICE) {
                return output(0, "LoadState=loaded\nFragmentPath=/elsewhere/swarm-schedule.service\nDropInPaths=\n".into());
            }
            output(
                0,
                format!(
                    "LoadState=loaded\nActiveState=active\nUnitFileState=enabled\nFragmentPath={}\n",
                    paths[1].display()
                ),
            )
        };
        let manager = Manager {
            platform: Platform::Systemd,
            paths: paths.clone(),
            run: &run,
        };
        assert!(
            manager
                .change(Some(render(Platform::Systemd, "/tmp/swarm", &[])))
                .is_err()
        );
        assert_eq!(fs::read(&paths[0]).unwrap(), b"previous service");
        assert_eq!(fs::read(&paths[1]).unwrap(), b"previous timer");
    }

    #[test]
    fn launchd_owns_every_minute_and_preserves_paths() {
        let units = render(
            Platform::Launchd,
            "/tmp/A&B",
            &[("SWARM_DAEMON_STATE", "/test <main>")],
        );
        assert!(units[0].contains("/tmp/A&amp;B"));
        assert!(units[0].contains("/test &lt;main&gt;"));
        assert_eq!(units[0].matches("<key>Minute</key>").count(), 60);
    }
}
