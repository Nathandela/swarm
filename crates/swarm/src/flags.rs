//! Parse the established single/double-dash Go flag syntax before positional arguments.
pub(crate) fn parse_bool(name: &str, value: &str) -> Result<bool, String> {
    match value {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err(format!(
            "invalid boolean value {value:?} for -{name}: parse error"
        )),
    }
}

pub(crate) fn options<'a>(
    mut args: &'a [String],
    names: &[&str],
    boolean: &[&str],
    mut set: impl FnMut(&str, &'a str) -> Result<(), String>,
) -> Result<&'a [String], String> {
    while let Some((arg, rest)) = args.split_first() {
        if arg == "--" {
            return Ok(rest);
        }
        let Some(flag) = arg.strip_prefix('-').filter(|flag| !flag.is_empty()) else {
            break;
        };
        let flag = flag.strip_prefix('-').unwrap_or(flag);
        if flag.starts_with(['-', '=']) {
            return Err(format!("bad flag syntax: {arg}"));
        }
        let (name, value) = match flag.split_once('=') {
            Some((name, value)) => (name, Some(value)),
            None => (flag, None),
        };
        if !names.contains(&name) {
            return Err(if matches!(name, "h" | "help") {
                String::new()
            } else {
                format!("flag provided but not defined: -{name}")
            });
        }
        args = rest;
        let value = match value {
            Some(value) => value,
            None if boolean.contains(&name) => "true",
            None => {
                let (value, rest) = args
                    .split_first()
                    .ok_or_else(|| format!("flag needs an argument: -{name}"))?;
                args = rest;
                value
            }
        };
        set(name, value)?;
    }
    Ok(args)
}
