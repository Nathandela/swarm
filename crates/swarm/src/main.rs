mod board;
mod bootstrap;
mod discord;
mod doctor;
mod environment;
mod flags;
mod hook;
mod ipc;
mod manager_mcp;
mod providers;
mod setup_trust;
mod shim;
mod socket;
mod supervisor;

use std::io::{self, IsTerminal, Write};
use std::process::ExitCode;

const USAGE: &str = "usage: swarm [daemon|shim|ls|peek|watch|spawn|message|requests|send|kill|doctor|discord|setup-trust|dispatcher-guide|version]\n";
const GUIDE: &str = include_str!("../../../cmd/swarm/skills/swarm-dispatcher/guide.txt");
const SKILL: &str = include_str!("../../../cmd/swarm/skills/swarm-dispatcher/SKILL.md");

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
        Some("daemon") => supervisor::run(&args[1..]),
        Some("hook") => hook::run(&args[1..]),
        Some("shim") => shim::run(&args[1..]),
        Some("doctor") => doctor::run(&args[1..]),
        Some("discord") => discord::run(&args[1..]),
        Some("manager-mcp") => manager_mcp::run(&args[1..]),
        Some("dispatcher-guide") => guide(&args[1..]),
        Some("ls" | "peek" | "watch" | "spawn" | "message" | "requests" | "send" | "kill") => {
            ipc::run(&args) as u8
        }
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
