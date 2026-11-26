mod board;
mod bootstrap;
mod checkout;
mod clean;
mod discord;
mod doctor;
mod environment;
mod flags;
mod hook;
mod ipc;
mod manager_mcp;
mod manager_prompt;
mod providers;
mod release;
mod schedule;
mod setup_trust;
mod shim;
mod socket;
mod supervisor;
mod teammate;
mod terminal_mouse;
mod upgrade;
mod workspace;

use std::io::{self, IsTerminal, Write};
use std::process::ExitCode;

const USAGE: &str = include_str!("usage.txt");
const GUIDE: &str = include_str!("../assets/swarm-dispatcher/guide.txt");
const SKILL: &str = include_str!("../assets/swarm-dispatcher/SKILL.md");

fn guide(args: &[String]) -> u8 {
    let output = match args {
        [] => GUIDE,
        [one] if one == "--skill" => SKILL,
        [one] if one == "--help" || one == "-h" => "swarm dispatcher-guide [--skill]\n",
        _ => {
            eprintln!("usage: swarm dispatcher-guide [--skill]");
            return 2;
        }
    };
    u8::from(io::stdout().lock().write_all(output.as_bytes()).is_err())
}

fn main() -> ExitCode {
    let args: Vec<_> = std::env::args_os().skip(1).collect();
    if args.first().is_some_and(|s| s == "upgrade") {
        return ExitCode::from(upgrade::run(&args[1..]));
    }
    let read_only = args.first().is_some_and(|arg| {
        [
            "--help",
            "-h",
            "help",
            "version",
            "--version",
            "doctor",
            "release-info",
            "release-check-media",
            "dispatcher-guide",
        ]
        .iter()
        .any(|name| arg == *name)
    });
    let inventory = args.first().is_some_and(|arg| arg == "checkout")
        && args.get(1).is_some_and(|arg| arg == "inventory");
    let _installation = if read_only || inventory {
        None
    } else {
        match upgrade::writer(&args) {
            Ok(lock) => lock,
            Err(error) => {
                eprintln!("swarm: {error}");
                return ExitCode::FAILURE;
            }
        }
    };
    if args.first().is_some_and(|s| s == "setup-trust") {
        return ExitCode::from(setup_trust::run(&args[1..]));
    }
    let Ok(args): Result<Vec<_>, _> = args.into_iter().map(|s| s.into_string()).collect() else {
        eprintln!("swarm: invalid UTF-8 argument");
        return ExitCode::from(2);
    };
    let code = match args.first().map(String::as_str) {
        Some("version" | "--version") => {
            println!(
                "swarm {} (Rust)",
                option_env!("SWARM_BUILD_VERSION").unwrap_or("dev")
            );
            0
        }
        Some("--help" | "-h" | "help") => {
            print!("{USAGE}");
            0
        }
        Some("--pilot") => ipc::pilot::run(&args[1..], true),
        Some("pilot") => ipc::pilot::run(&args[1..], false),
        Some("daemon") => supervisor::run(&args[1..]),
        Some("hook") => hook::run(&args[1..]),
        Some("native-input") => providers::agy_input::cli(&args[1..]),
        Some("shim") => shim::run(&args[1..]),
        Some("checkout") => checkout::run(&args[1..]),
        Some("clean") => clean::run(&args[1..]),
        Some("doctor") => doctor::run(&args[1..]),
        Some("release-info") if args.len() == 1 => {
            println!("{}", release::info());
            0
        }
        Some("release-check-media") if args.len() == 1 => discord::check_packaged_media(),
        Some("discord") => discord::run(&args[1..]),
        Some("schedule") => schedule::run(&args[1..]),
        Some("schedule-receipt-bridge") => schedule::worker::bridge(),
        Some("schedule-agy-worker") => schedule::agy::worker(&args[1..]),
        Some("manager-mcp") => manager_mcp::run(&args[1..]),
        Some("dispatcher-guide") => guide(&args[1..]),
        Some("teammate") => teammate::run(&args[1..]),
        Some(
            "reattach" | "ls" | "peek" | "watch" | "handoff" | "spawn" | "message" | "requests"
            | "decide" | "send" | "kill",
        ) => ipc::run(&args) as u8,
        None if !io::stdin().is_terminal() || !io::stdout().is_terminal() => {
            eprintln!("swarm: not a terminal; the TUI needs an interactive terminal");
            1
        }
        None => board::run(),
        _ => {
            eprint!("{USAGE}");
            2
        }
    };
    ExitCode::from(code)
}
