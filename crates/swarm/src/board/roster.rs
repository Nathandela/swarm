use super::{
    Board, field,
    launch::{ACCENT, DIM, PLAIN, TITLE, spans},
    name,
    navigation::{GROUPS, ORDERS, Row, board_capacity, board_first, truncation},
};
use serde_json::Value;
use unicode_width::UnicodeWidthStr;

const BOLD: &str = "\x1b[0;1m";
const SPINNER: [&str; 10] = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];

struct Columns {
    name: u16,
    cwd: u16,
    summary: u16,
}

fn columns(width: u16) -> Columns {
    let width = if width == 0 { 120 } else { width };
    let available = width.saturating_sub(36);
    let (mut name, mut cwd) = (20, 24);
    if available >= 84 {
        let extra = available - 84;
        let grow_name = ((u32::from(extra) * 55 + 50) / 100).min(34) as u16;
        let grow_cwd = (extra - grow_name).min(34);
        return Columns {
            name: name + grow_name,
            cwd: cwd + grow_cwd,
            summary: 40 + extra - grow_name - grow_cwd,
        };
    }
    if available >= 56 {
        return Columns {
            name,
            cwd,
            summary: available - 44,
        };
    }
    let mut deficit = 56 - available;
    while deficit > 0 && (name > 8 || cwd > 10) {
        if name > 8 {
            name -= 1;
            deficit -= 1;
        }
        if deficit > 0 && cwd > 10 {
            cwd -= 1;
            deficit -= 1;
        }
    }
    Columns {
        name,
        cwd,
        summary: 12_u16.saturating_sub(deficit),
    }
}

pub(super) fn compact(width: u16) -> bool {
    let c = columns(width);
    c.name < 16 || c.cwd < 12 || c.summary < 12
}

fn group_style(group: &str, bold: bool) -> &'static str {
    match (group, bold) {
        ("needs_input", false) => ACCENT,
        ("needs_input", true) => TITLE,
        ("working", false) => "\x1b[0;38;5;79m",
        ("working", true) => "\x1b[0;1;38;5;79m",
        ("ready_for_review", false) => "\x1b[0;38;5;114m",
        ("ready_for_review", true) => "\x1b[0;1;38;5;114m",
        (_, false) => DIM,
        (_, true) => "\x1b[0;1;38;5;60m",
    }
}

fn counts<'a>(rows: impl Iterator<Item = &'a Value>) -> (usize, usize) {
    rows.fold((0, 0), |(input, review), row| {
        let group = field(row, "group");
        (
            input + usize::from(group == "needs_input"),
            review + usize::from(group == "ready_for_review"),
        )
    })
}

fn status(row: &Value, compact: bool) -> &str {
    match field(row, "group") {
        "needs_input" => match field(&row["status"], "Interaction") {
            "permission" => "approval",
            "prompt" => "question",
            "error" => "error",
            _ if compact => "input",
            _ => "needs input",
        },
        "working" => "working",
        "ready_for_review" if compact => "review",
        "ready_for_review" => "ready for review",
        _ if compact => "ended",
        _ => "completed",
    }
}

fn pad(text: &str, width: u16) -> String {
    format!(
        "{text}{}",
        " ".repeat(usize::from(width).saturating_sub(text.width()))
    )
}

fn elapsed(row: &Value) -> String {
    let parsed = ["group_entered_at", "last_activity"]
        .iter()
        .find_map(|key| field(row, key).parse::<jiff::Timestamp>().ok())
        .unwrap_or(jiff::Timestamp::UNIX_EPOCH);
    let seconds =
        (jiff::Timestamp::now().as_nanosecond() - parsed.as_nanosecond()).max(0) / 1_000_000_000;
    match seconds {
        0..=59 => format!("{seconds}s"),
        60..=3599 => format!("{}m", seconds / 60),
        3600..=86399 => format!("{}h", seconds / 3600),
        _ => format!("{}d", (seconds / 86400).min(9999)),
    }
}

fn session(board: &Board, row: &Value, selected: bool, cols: u16) -> Vec<(&'static str, String)> {
    let group = field(row, "group");
    let style = group_style(group, false);
    let marker = if selected {
        (ACCENT, "▌".into())
    } else {
        (PLAIN, " ".into())
    };
    let icon = match group {
        "needs_input" => "●",
        "working" => SPINNER[board.spinner],
        "ready_for_review" if super::scheduled_review(row) => "◆",
        "ready_for_review" => "✓",
        _ => "─",
    };
    let mut label = name(row).to_owned();
    if field(row, "role") == "manager" {
        label = format!("[manager] {label}");
    }
    let mut spans = vec![
        marker,
        (PLAIN, " ".into()),
        (style, icon.into()),
        (PLAIN, " ".into()),
    ];
    if compact(cols) {
        let budget = cols.saturating_sub(4);
        let state = truncation(status(row, true), budget.saturating_sub(5));
        let name_width = budget.saturating_sub(state.width() as u16 + 1);
        let label = truncation(&label, name_width);
        let padding = usize::from(name_width).saturating_sub(label.width()) + 1;
        spans.extend([(BOLD, label), (PLAIN, " ".repeat(padding)), (style, state)]);
    } else {
        let widths = columns(cols);
        let cwd = field(row, "cwd");
        let shortened = std::env::var("HOME").ok().and_then(|home| {
            let suffix = cwd.strip_prefix(&home)?;
            (suffix.is_empty() || suffix.starts_with('/')).then(|| format!("~{suffix}"))
        });
        let cwd = shortened.as_deref().unwrap_or(cwd);
        let clipped = |text, width: u16| pad(&super::clip(text, width.saturating_sub(1)), width);
        let label = super::clip(&label, widths.name.saturating_sub(1));
        let padding = usize::from(widths.name).saturating_sub(label.width());
        spans.extend([
            (BOLD, label),
            (PLAIN, " ".repeat(padding)),
            (DIM, clipped(field(row, "agent"), 9)),
            (DIM, clipped(cwd, widths.cwd)),
            (style, pad(status(row, false), 17)),
            (
                DIM,
                pad(&elapsed(row), 6) + &super::clip(field(row, "summary"), widths.summary),
            ),
        ]);
    }
    spans
}

pub(super) fn view(board: &Board, screen: &mut String, cols: u16, height: u16) {
    let (input, review) = counts(board.rows.iter());
    let mut detail = format!("{input} input · {review} review");
    let title = if detail.width() + 8 <= usize::from(cols) {
        "swarm · "
    } else {
        ""
    };
    if cols >= 72 {
        detail += &format!(
            " · group: {} · order: {}",
            GROUPS[board.layout.group], ORDERS[board.layout.order]
        );
    }
    spans(screen, 1, cols, &[(TITLE, title.into()), (DIM, detail)]);
    let focus = board.current_focus();
    for (index, row) in board
        .display_rows()
        .iter()
        .skip(board.top)
        .take(board_capacity(height))
        .enumerate()
    {
        let selected = focus.is_some() && row.focus() == focus;
        let y = board_first(height) + index as u16;
        match row {
            Row::Session(row) => spans(screen, y, cols, &session(board, row, selected, cols)),
            Row::Section { id, count, folded } => {
                let (input, review) = counts(
                    board
                        .sections
                        .iter()
                        .filter(|(section, _)| section == id)
                        .flat_map(|(_, indices)| indices.iter())
                        .map(|&index| &board.rows[index]),
                );
                let marker = if selected {
                    "▌"
                } else if *folded {
                    "▸"
                } else {
                    "▾"
                };
                let mut title = board.section_title(id).to_uppercase();
                let mut totals = format!(" ({count} · {input} input · {review} review)");
                if 2 + title.width() + totals.width() > usize::from(cols) {
                    totals = format!(" ({count}) {input}i {review}r");
                    title = truncation(&title, cols.saturating_sub(2 + totals.width() as u16));
                }
                let style = if board.layout.group == 0 {
                    group_style(id, true)
                } else {
                    TITLE
                };
                spans(
                    screen,
                    y,
                    cols,
                    &[(style, format!("{marker} {title}{totals}"))],
                );
            }
            Row::Spacer => {}
        }
    }

    let mut footer = board
        .button_view(cols, height)
        .into_iter()
        .map(|(button, ..)| button)
        .collect::<Vec<_>>()
        .join(" ");
    let version = option_env!("SWARM_BUILD_VERSION").unwrap_or("dev");
    let gap = usize::from(cols).saturating_sub(footer.width() + version.width());
    if cols >= 72 && gap >= 3 && !version.is_empty() {
        footer += &" ".repeat(gap);
        footer += version;
    }
    spans(screen, height, cols, &[(PLAIN, footer)]);
}
