pub fn go_int(input: &str) -> Result<i64, &'static str> {
    let negative = input.starts_with('-');
    let digits = input.strip_prefix(['-', '+']).unwrap_or(input);
    let (radix, digits) = if let Some(digits) = digits
        .strip_prefix("0x")
        .or_else(|| digits.strip_prefix("0X"))
    {
        (16, digits.strip_prefix('_').unwrap_or(digits))
    } else if let Some(digits) = digits
        .strip_prefix("0b")
        .or_else(|| digits.strip_prefix("0B"))
    {
        (2, digits.strip_prefix('_').unwrap_or(digits))
    } else if let Some(digits) = digits
        .strip_prefix("0o")
        .or_else(|| digits.strip_prefix("0O"))
    {
        (8, digits.strip_prefix('_').unwrap_or(digits))
    } else if digits.starts_with('0') {
        (8, digits)
    } else {
        (10, digits)
    };
    if digits.starts_with('_')
        || digits.ends_with('_')
        || digits.contains("__")
        || digits.starts_with(['+', '-'])
    {
        return Err("parse error");
    }
    let amount = u64::from_str_radix(&digits.replace('_', ""), radix).map_err(|err| {
        if *err.kind() == std::num::IntErrorKind::PosOverflow {
            "value out of range"
        } else {
            "parse error"
        }
    })? as i128;
    if negative { -amount } else { amount }
        .try_into()
        .map_err(|_| "value out of range")
}

pub fn parse(input: &str) -> Option<i64> {
    let negative = input.starts_with('-');
    let mut rest = input.strip_prefix(['-', '+']).unwrap_or(input);
    if rest == "0" {
        return Some(0);
    }
    if rest.is_empty() {
        return None;
    }
    let mut ns = 0i128;
    while !rest.is_empty() {
        let number = rest
            .find(|c: char| !c.is_ascii_digit() && c != '.')
            .unwrap_or(rest.len());
        let (digits, tail) = rest.split_at(number);
        let unit = tail
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(tail.len());
        let (unit, next) = tail.split_at(unit);
        let scale = match unit {
            "ns" => 1,
            "us" | "µs" | "μs" => 1_000,
            "ms" => 1_000_000,
            "s" => 1_000_000_000,
            "m" => 60_000_000_000,
            "h" => 3_600_000_000_000,
            _ => return None,
        };
        if digits.is_empty() || digits == "." {
            return None;
        }
        let (whole, fraction) = digits.split_once('.').unwrap_or((digits, ""));
        let whole = if whole.is_empty() {
            0
        } else {
            whole.parse::<i128>().ok()?
        };
        // Work from the least significant digit so long fractions remain bounded.
        let mut part = 0u64;
        for digit in fraction.bytes().rev() {
            if !digit.is_ascii_digit() {
                return None;
            }
            part = (part + u64::from(digit - b'0') * scale) / 10;
        }
        ns = ns
            .checked_add(whole.checked_mul(i128::from(scale))?)?
            .checked_add(i128::from(part))?;
        rest = next;
    }
    if negative { -ns } else { ns }.try_into().ok()
}

pub fn format(ns: i64) -> String {
    let value = ns.unsigned_abs();
    let sign = if ns < 0 { "-" } else { "" };
    if value == 0 {
        return "0s".into();
    }
    if value < 1_000 {
        return format!("{sign}{value}ns");
    }
    let (scale, places, unit) = match value {
        0..1_000_000 => (1_000, 3, "µs"),
        1_000_000..1_000_000_000 => (1_000_000, 6, "ms"),
        _ => (1_000_000_000, 9, "s"),
    };
    let (hours, minutes, seconds, fraction) = (
        value / scale / 3600,
        value / scale / 60 % 60,
        value / scale % 60,
        value % scale,
    );
    let mut output = sign.to_owned();
    if unit != "s" {
        output.push_str(&(value / scale).to_string());
    } else {
        if hours > 0 {
            output.push_str(&format!("{hours}h"));
        }
        if hours > 0 || minutes > 0 {
            output.push_str(&format!("{minutes}m"));
        }
        output.push_str(&seconds.to_string());
    }
    if fraction > 0 {
        let digits = format!("{fraction:0places$}");
        output.push('.');
        output.push_str(digits.trim_end_matches('0'));
    }
    output.push_str(unit);
    output
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_numbers() {
        assert_eq!(go_int("-0x_20"), Ok(-32));
        assert_eq!(go_int("0_10"), Ok(8));
        assert_eq!(go_int("0x+20"), Err("parse error"));
        assert_eq!(parse("1h2m3.040005s"), Some(3_723_040_005_000));
        assert_eq!(parse(".999999999998ns"), Some(0));
        assert_eq!(parse("-9223372036854775808ns"), Some(i64::MIN));
        assert_eq!(parse("9223372036854775808ns"), None);
        assert_eq!(parse("2m1"), None);
        assert_eq!(format(3_723_040_005_000), "1h2m3.040005s");
        assert_eq!(format(-11_500), "-11.5µs");
    }
}
