use super::{
    Calendar, Result, Spec,
    calendar::Rule,
    catalog::{Read, Records},
    file, id,
    store::Store,
};
use jiff::Timestamp;
use serde::Serialize;
use std::{
    collections::BTreeMap,
    io::{self, Write},
    path::Path,
};
use unicode_width::UnicodeWidthStr;

const USAGE: &str = "usage: swarm schedule <command>
  create --name NAME --dir PATH --agents ORDERED_LIST --authority MODE
         --workspace worktree|directory --prompt-file FILE [--timezone ZONE]
         (--once YYYY-MM-DD | --daily | --weekdays | --weekly Monday | --cron EXPR)
         [--time HH:MM] [--catch-up 1h] [--allow-overlap=false]
  Supported agents: codex, claude, agy (in fallback order).
  create --spec-file FILE         create from a JSON job specification
  edit JOB --spec-file FILE [--revision ID]    replace a job specification
  list [--json] | show JOB | history JOB
  pause JOB | resume JOB | archive JOB | run JOB [--operation-id ULID]
  templates list | show ID | create NAME FILE | edit ID NAME FILE | delete ID
  timer install | timer status | timer uninstall
  tick | complete

Authority: observe, workspace-write, agent-default. Agents are tried in order.
Use '-' for a prompt file on stdin. Run now leaves the calendar unchanged.
If a run reply is lost, retry the same job with the printed operation ID.
";

pub(crate) fn run(args: &[String]) -> u8 {
    let mut out = io::stdout().lock();
    let mut err = io::stderr().lock();
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    if matches!(args.first(), None | Some(&"help" | &"--help")) {
        return u8::from(out.write_all(USAGE.as_bytes()).is_err());
    }
    if args == ["run", "--help"] || args == ["run", "-h"] {
        return u8::from(
            writeln!(err, "usage: swarm schedule run JOB [--operation-id ULID]").is_err(),
        );
    }
    let manual = if args.first() == Some(&"run") {
        match parse_run(&args[1..]) {
            Ok(pair) => Some(pair),
            Err(e) => {
                let _ = writeln!(err, "{e}");
                return 2;
            }
        }
    } else {
        None
    };
    if args.first() == Some(&"tick") && args.len() != 1 {
        let _ = writeln!(err, "usage: swarm schedule tick");
        return 2;
    }
    if args.first() == Some(&"timer") || args == ["status"] {
        let verb = if args == ["status"] {
            &["status"][..]
        } else {
            &args[1..]
        };
        if verb.len() != 1 {
            let _ = writeln!(err, "usage: swarm schedule timer install|status|uninstall");
            return 2;
        }
        if !matches!(verb[0], "install" | "status" | "uninstall") {
            let _ = writeln!(err, "unknown timer command: {}", verb[0]);
            return 2;
        }
        return match super::timer::command(verb[0]) {
            Ok(None) => 0,
            Ok(Some(line)) => u8::from(writeln!(out, "{line}").is_err()),
            Err(e) => {
                let _ = writeln!(err, "{e}");
                1
            }
        };
    }
    if args.first() == Some(&"complete") {
        if args[1..] == ["--help"] || args[1..] == ["-h"] {
            return u8::from(
                writeln!(
                    err,
                    "usage: swarm schedule complete --summary TEXT [--link LINK]"
                )
                .is_err(),
            );
        }
        return match super::rpc::complete(&args[1..]) {
            Ok(()) => u8::from(writeln!(out, "Completion recorded.").is_err()),
            Err(e) => {
                let _ = writeln!(err, "{e}");
                1
            }
        };
    }

    let result = (|| -> Result<()> {
        let now = Timestamp::now();
        if let Some((job, invocation)) = &manual {
            let mut client = super::rpc::owner(false)?;
            let operation = invocation.clone().map_or_else(|| id(now), Ok)?;
            writeln!(err, "Operation ID: {operation:?}")?;
            return match super::rpc::run(&mut client, job, &operation) {
                Ok(run) => {
                    writeln!(out, "{run}")?;
                    Ok(())
                }
                Err(e) => Err(format!("{e}\nTo retry the same run, repeat the command for the same job with --operation-id {operation:?}.").into()),
            };
        }
        if args == ["tick"] {
            return super::rpc::tick();
        }
        let read = match args.as_slice() {
            ["list"] | ["list", "--json"] => Some(Read::List),
            ["list", ..] => return Err("usage: swarm schedule list [--json]".into()),
            ["show", id] => Some(Read::Get { id: (*id).into() }),
            ["history", id] => Some(Read::History { id: (*id).into() }),
            _ => None,
        };
        if let Some(read) = read {
            let records = super::catalog::project(&crate::ipc::state_path()?, read)?;
            return read_catalog(records, args == ["list"], now, &mut out);
        }
        let store = Store::open(&crate::ipc::state_path()?, Path::new("/etc/localtime"))?;
        catalog(&store, &args, now, &mut out)
    })();
    if let Err(e) = result {
        let _ = writeln!(err, "schedule: {e}");
        return 1;
    }
    0
}

fn catalog(store: &Store, args: &[&str], now: Timestamp, out: &mut impl Write) -> Result<()> {
    match args {
        ["create", rest @ ..] => {
            let spec = read_spec(rest)?;
            super::rpc::host_calendar(&spec.calendar)?;
            write_json(out, &store.create(spec, now, false)?)?;
        }
        ["edit", job, "--spec-file", file, tail @ ..] => {
            let revision = match tail {
                [] | ["--revision", ""] => None,
                ["--revision", revision] => Some(*revision),
                _ => return Err("usage: swarm schedule edit JOB --spec-file FILE".into()),
            };
            let spec = read_spec(&["--spec-file", file])?;
            super::rpc::host_calendar(&spec.calendar)?;
            write_json(out, &store.edit(job, revision, spec, now)?)?;
        }
        ["edit", ..] => return Err("usage: swarm schedule edit JOB --spec-file FILE".into()),
        ["pause", job] => write_json(out, &store.set_state(job, "paused", now)?)?,
        ["resume", job] => {
            super::rpc::host_calendar(&store.job(job)?.spec.calendar)?;
            write_json(out, &store.set_state(job, "active", now)?)?;
        }
        ["archive", job] => write_json(out, &store.set_state(job, "archived", now)?)?,
        ["templates", "list"] => write_list(out, store.templates()?)?,
        ["templates", "show", id] => write_json(out, &store.template(id)?)?,
        ["templates", "delete", id] => store.delete_template(id)?,
        ["templates", "create", name, path] => write_json(out, &store.save_template(None, (*name).into(), file(path)?, now)?)?,
        ["templates", "edit", id, name, path] => write_json(out, &store.save_template(Some(id), (*name).into(), file(path)?, now)?)?,
        ["templates", ..] => return Err("usage: swarm schedule templates list|show ID|create NAME FILE|edit ID NAME FILE|delete ID".into()),
        [verb] => return Err(format!("expected a job ID for {verb}").into()),
        [_, _, _, ..] => return Err("unexpected arguments".into()),
        [verb, _] => return Err(format!("unknown command {verb:?}; see swarm schedule help").into()),
        [] => unreachable!(),
    }
    Ok(())
}

fn read_catalog(records: Records, table: bool, now: Timestamp, out: &mut impl Write) -> Result<()> {
    match records {
        Records::List(jobs) if table => {
            let mut rows = vec![[
                "ID".into(),
                "STATE".into(),
                "NAME".into(),
                "NEXT LOCAL TIME".into(),
                "AGENTS".into(),
            ]];
            for job in jobs {
                let mut next = "—".to_owned();
                if job.state == "active" {
                    let rule = Rule::compile(&job.spec.calendar, Path::new("/etc/localtime"))?;
                    if let Some(due) = rule.find(now, false) {
                        next = due
                            .to_zoned(rule.zone.tz)
                            .strftime("%Y-%m-%d %H:%M %Z")
                            .to_string();
                    }
                }
                rows.push([
                    job.id,
                    job.state,
                    job.spec.name,
                    next,
                    job.spec.agents.join(" → "),
                ]);
            }
            let widths: [usize; 4] =
                std::array::from_fn(|i| rows.iter().map(|r| r[i].width()).max().unwrap_or(0));
            for row in rows {
                for i in 0..4 {
                    write!(
                        out,
                        "{}{:width$}",
                        row[i],
                        "",
                        width = widths[i] + 2 - row[i].width()
                    )?;
                }
                writeln!(out, "{}", row[4])?;
            }
        }
        Records::List(jobs) => write_list(out, jobs)?,
        Records::Get(job) => write_json(out, &job)?,
        Records::History(runs) => write_list(out, runs)?,
    }
    Ok(())
}

fn write_json(out: &mut impl Write, value: &impl Serialize) -> Result<()> {
    let json = serde_json::to_string_pretty(value)?;
    writeln!(out, "{}", super::store::go_json(json))?;
    Ok(())
}

fn write_list<T: Serialize>(out: &mut impl Write, values: Vec<T>) -> Result<()> {
    write_json(out, &(!values.is_empty()).then_some(values))
}

fn parse_run<'a>(args: &'a [&'a str]) -> Result<(&'a str, Option<String>)> {
    let usage = "usage: swarm schedule run JOB [--operation-id ULID]";
    let mut args = args.iter().copied().peekable();
    let first = args.next_if(|s| !s.starts_with('-'));
    let mut operation = None;
    while let Some(flag) = args.next_if(|s| s.starts_with('-') && *s != "-") {
        if flag == "--" {
            break;
        }
        let (name, value) = flag
            .split_once('=')
            .map_or((flag, None), |(k, v)| (k, Some(v)));
        if !matches!(name, "--operation-id" | "-operation-id") {
            return Err(usage.into());
        }
        let value = value
            .or_else(|| args.next())
            .ok_or("flag needs an argument: -operation-id")?;
        if value.is_empty() {
            return Err("expected a non-empty operation ID".into());
        }
        operation = Some(value.to_owned());
    }
    let job = first
        .or_else(|| args.next())
        .filter(|s| !s.is_empty())
        .ok_or(usage)?;
    if args.next().is_some() {
        return Err(usage.into());
    }
    Ok((job, operation))
}

fn read_spec(args: &[&str]) -> Result<Spec> {
    if let ["--spec-file", path] = args {
        return Ok(serde_json::from_str(&file(path)?)?);
    }
    let mut values = BTreeMap::new();
    let mut args = args.iter().copied();
    while let Some(arg) = args.next() {
        let arg = arg
            .strip_prefix("--")
            .or_else(|| arg.strip_prefix('-'))
            .ok_or("unexpected positional argument")?;
        let (key, value) = arg
            .split_once('=')
            .map_or((arg, None), |(k, v)| (k, Some(v)));
        let value = match key {
            "allow-overlap" | "daily" | "weekdays" => match value.unwrap_or("true") {
                "1" | "t" | "T" | "true" | "TRUE" | "True" => "true",
                "0" | "f" | "F" | "false" | "FALSE" | "False" => "false",
                v => return Err(format!("invalid boolean value {v:?} for -{key}").into()),
            },
            "name" | "dir" | "agents" | "authority" | "workspace" | "prompt-file" | "timezone"
            | "time" | "once" | "weekly" | "cron" | "catch-up" => value
                .or_else(|| args.next())
                .ok_or_else(|| format!("flag needs an argument: -{key}"))?,
            _ => return Err(format!("flag provided but not defined: -{key}").into()),
        };
        values.insert(key, value);
    }
    let get = |key| values.get(key).copied().unwrap_or_default();
    let choices = [
        ("once", !get("once").is_empty()),
        ("daily", get("daily") == "true"),
        ("weekdays", get("weekdays") == "true"),
        ("weekly", !get("weekly").is_empty()),
        ("cron", !get("cron").is_empty()),
    ];
    let mut kinds = choices
        .into_iter()
        .filter_map(|(kind, chosen)| chosen.then_some(kind));
    let kind = kinds
        .next()
        .filter(|_| kinds.next().is_none())
        .ok_or("choose one of --once, --daily, --weekdays, --weekly, --cron")?;
    if get("prompt-file").is_empty() {
        return Err("--prompt-file is required".into());
    }
    Ok(Spec {
        name: get("name").into(),
        prompt: file(get("prompt-file"))?,
        directory: get("dir").into(),
        authority: get("authority").into(),
        workspace: get("workspace").into(),
        agents: get("agents").split(',').map(|a| a.trim().into()).collect(),
        calendar: Calendar {
            kind: kind.into(),
            time_zone: values.get("timezone").copied().unwrap_or("host").into(),
            time: get("time").into(),
            date: get("once").into(),
            weekday: get("weekly").into(),
            cron: get("cron").into(),
        },
        grace: values.get("catch-up").map_or(Ok(0), |s| duration(s))?,
        allow_overlap: get("allow-overlap") != "false",
    })
}

fn duration(input: &str) -> Result<i64> {
    let invalid = || -> Box<dyn std::error::Error> { format!("invalid duration {input:?}").into() };
    let sign = if input.starts_with('-') { -1 } else { 1 };
    let mut text = input.strip_prefix(['-', '+']).unwrap_or(input);
    if text == "0" {
        return Ok(0);
    }
    if text.is_empty() {
        return Err(invalid());
    }
    let mut ns = 0i128;
    while !text.is_empty() {
        let i = text
            .find(|c: char| !c.is_ascii_digit() && c != '.')
            .ok_or_else(invalid)?;
        let (number, units) = text.split_at(i);
        let next = units
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(units.len());
        let (unit, rest) = units.split_at(next);
        let scale = match unit {
            "ns" => 1,
            "us" | "µs" | "μs" => 1_000,
            "ms" => 1_000_000,
            "s" => 1_000_000_000,
            "m" => 60_000_000_000,
            "h" => 3_600_000_000_000,
            _ => return Err(invalid()),
        };
        let (whole, fraction) = number.split_once('.').unwrap_or((number, ""));
        if (whole.is_empty() && fraction.is_empty())
            || !fraction.bytes().all(|b| b.is_ascii_digit())
        {
            return Err(invalid());
        }
        let whole = if whole.is_empty() {
            0
        } else {
            i128::from(whole.parse::<u64>().map_err(|_| invalid())?)
        };
        let fraction: String = fraction.chars().take(18).collect();
        let fraction_ns = if fraction.is_empty() {
            0
        } else {
            i128::from(fraction.parse::<u64>().map_err(|_| invalid())?) * scale
                / 10i128.pow(fraction.len() as u32)
        };
        ns += whole * scale + fraction_ns;
        if ns > i128::from(i64::MAX) + 1 {
            return Err(invalid());
        }
        text = rest;
    }
    i64::try_from(sign * ns).map_err(|_| invalid())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn run_requires_explicit_flag_value_and_retains_retry_identity() {
        let operation = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
        assert_eq!(
            parse_run(&["job", "--operation-id", operation]).unwrap(),
            ("job", Some(operation.into()))
        );
        assert_eq!(
            parse_run(&[&format!("--operation-id={operation}"), "job"]).unwrap(),
            ("job", Some(operation.into()))
        );
        assert!(parse_run(&["job", "--operation-id"]).is_err());
        assert!(parse_run(&["job", "--operation-id="]).is_err());
        assert_eq!(parse_run(&["--", "job"]).unwrap(), ("job", None));
    }
}
