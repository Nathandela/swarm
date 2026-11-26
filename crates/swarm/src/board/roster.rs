use super::{
    Board, Modal, field,
    launch::{ACCENT, DIM, PLAIN, TITLE, block_cursor, spans},
    name,
    navigation::{GROUPS, ORDERS, Row, truncation},
    session_form::{CONFIRM_HINT, RENAME_HINT, TAG_HINT},
};
use serde_json::Value;
use unicode_width::UnicodeWidthStr;

pub(super) const BOLD: &str = "\x1b[0;1m";
const ERROR: &str = "\x1b[0;38;5;173m";
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

pub(super) fn status(row: &Value, compact: bool) -> &str {
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

fn session(
    board: &Board,
    row: &Value,
    selected: bool,
    cols: u16,
    y: u16,
) -> Vec<(&'static str, String)> {
    let group = field(row, "group");
    let style = group_style(group, false);
    let id = field(row, "id");
    let edit = match &board.modal {
        Some(Modal::Edit {
            id: target,
            tag,
            draft,
            ..
        }) if id == target => Some((*tag, draft)),
        _ => None,
    };
    let confirm = match &board.modal {
        Some(Modal::Confirm {
            id: target,
            running,
            ..
        }) if id == target => Some(*running),
        _ => None,
    };
    let prefix: (&str, String) = if let Some(running) = confirm {
        let prompt = if running { "kill? y/n" } else { "delete? y/n" };
        let minimum = if compact(cols) {
            prompt.width() + 12
        } else {
            prompt.width() + 1 + 2 + 9 + 17 + 6
        };
        (
            ERROR,
            if usize::from(cols) < minimum {
                "?".into()
            } else {
                prompt.into()
            },
        )
    } else if selected {
        (ACCENT, "▌".into())
    } else {
        (PLAIN, " ".into())
    };
    let prefix_width = prefix.1.width() as u16 + 1;
    let icon = if field(row, "role") == "manager" {
        "◈"
    } else if super::scheduled_review(row) {
        "◆"
    } else {
        match group {
            "needs_input" => "●",
            "working" => SPINNER[board.spinner],
            "ready_for_review" => "✓",
            _ => "─",
        }
    };
    let mut label = name(row).to_owned();
    if compact(cols) && field(row, "role") == "manager" {
        label = format!("[manager] {label}");
    }
    let mut spans = vec![
        prefix,
        (PLAIN, " ".into()),
        (style, icon.into()),
        (PLAIN, " ".into()),
    ];
    if compact(cols) {
        let (state, name_width) = compact_layout(row, cols, prefix_width);
        let original = truncation(&label, name_width);
        let label = edit.map_or_else(
            || original.clone(),
            |(tag, draft)| editor_label(draft, tag, name_width),
        );
        let padding = usize::from(name_width).saturating_sub(label.width());
        let mut leading = 0;
        let trailing = if edit.is_some_and(|(tag, _)| tag) {
            // Erase the glyphs replaced by the shorter tag editor.
            leading = (original.width() + 1)
                .saturating_sub(label.width())
                .min(padding);
            0
        } else if edit.is_some() || confirm.is_some() {
            0
        } else {
            let end = usize::from(prefix_width + 2 + name_width);
            terminal_bold_padding(end - padding, end, y)
        };
        spans.extend([
            (BOLD, label + &" ".repeat(leading)),
            (PLAIN, " ".repeat(padding - leading - trailing)),
            (BOLD, " ".repeat(trailing)),
            (PLAIN, " ".into()),
            (style, state),
        ]);
    } else {
        let widths = columns(cols.saturating_sub(prefix_width.saturating_sub(2)));
        let cwd = field(row, "cwd");
        let shortened = std::env::var("HOME").ok().and_then(|home| {
            let suffix = cwd.strip_prefix(&home)?;
            (suffix.is_empty() || suffix.starts_with('/')).then(|| format!("~{suffix}"))
        });
        let cwd = shortened.as_deref().unwrap_or(cwd);
        let clipped = |text, width: u16| pad(&super::clip(text, width.saturating_sub(1)), width);
        let label = match edit {
            Some((_, draft)) => block_cursor(draft, widths.name.saturating_sub(1)),
            None => truncation(&label, widths.name.saturating_sub(1)),
        };
        let padding = " ".repeat(usize::from(widths.name).saturating_sub(label.width()));
        spans.extend([(BOLD, label), (PLAIN, padding)]);
        let summary = summary(board, row, widths.summary);
        spans.extend([
            (DIM, clipped(field(row, "agent"), 9)),
            (DIM, clipped(cwd, widths.cwd)),
            (style, pad(status(row, false), 17)),
            (DIM, pad(&elapsed(row), 6) + &summary.0),
        ]);
        if !summary.1.is_empty() {
            spans.push((ACCENT, summary.1));
        }
    }
    spans
}

fn compact_layout(row: &Value, cols: u16, prefix_width: u16) -> (String, u16) {
    let budget = cols.saturating_sub(prefix_width + 2);
    let mut state = status(row, true).to_owned();
    let mut diagnostics = Vec::new();
    if !field(row, "backend_plan_error").is_empty() {
        diagnostics.push("!backend");
    }
    if !field(row, "resume_error").is_empty() {
        diagnostics.push("!resume");
    }
    if row["supervision_pending"] == true {
        diagnostics.push("!supervision");
    }
    if !diagnostics.is_empty() {
        let width = cols
            .saturating_sub(prefix_width + 6 + state.width() as u16)
            .max(1);
        state.push(' ');
        state.push_str(&truncation(&diagnostics.join(" "), width));
    }
    state = truncation(&state, budget.saturating_sub(5));
    let width = budget.saturating_sub(state.width() as u16 + 1);
    (state, width)
}

fn local_attempt(row: &Value, endpoint: &str, attempt: &str) -> bool {
    if endpoint != field(row, "endpoint_id") {
        return false;
    }
    let namespaced = |id: &str| id.split_once('/') == Some((endpoint, attempt));
    namespaced(field(row, "id"))
        || row["supersedes"]
            .as_array()
            .is_some_and(|ids| ids.iter().filter_map(Value::as_str).any(namespaced))
}

pub(super) fn supervision(board: &Board, row: &Value) -> &'static str {
    let endpoint = field(row, "endpoint_id");
    let parent = field(row, "spawned_from");
    if !parent.is_empty()
        && board.rows.iter().any(|candidate| {
            field(candidate, "id").split_once('/') == Some((endpoint, parent))
                && super::running(candidate)
        })
    {
        "supervisor pending"
    } else {
        "supervisor gone"
    }
}

fn summary(board: &Board, row: &Value, width: u16) -> (String, String) {
    let endpoint = field(row, "endpoint_id");
    let mut text = field(row, "summary").to_owned();
    let parent = field(row, "spawned_from");
    if !parent.is_empty() {
        let label = board
            .rows
            .iter()
            .find(|candidate| {
                local_attempt(candidate, endpoint, parent) && !field(candidate, "name").is_empty()
            })
            .map_or(parent, |candidate| field(candidate, "name"));
        text.push_str(&format!(" from {label}"));
    }
    let children = board
        .rows
        .iter()
        .filter(|child| {
            let parent = field(child, "spawned_from");
            field(child, "endpoint_id") == endpoint
                && !parent.is_empty()
                && local_attempt(row, endpoint, parent)
        })
        .count();
    if children > 0 {
        text.push_str(&format!(" spawned {children}"));
    }
    let mut markers = Vec::new();
    if row["role"] == "manager" {
        markers.push("[manager]".to_owned());
    }
    if row["supervision_pending"] == true {
        markers.push(supervision(board, row).to_owned());
    }
    if !field(row, "backend_plan_error").is_empty() {
        markers.push("no backend".to_owned());
    }
    if !field(row, "resume_error").is_empty() {
        markers.push(format!("resume: {}", field(row, "resume_error")));
    }
    let marker = if markers.is_empty() {
        String::new()
    } else {
        super::clip(&format!(" {}", markers.join(" ")), width)
    };
    (
        super::clip(&text, width.saturating_sub(marker.width() as u16)),
        marker,
    )
}

pub(super) fn attention_change(previous: &[Value], rows: &[Value]) -> Option<String> {
    if previous.is_empty() {
        return None;
    }
    rows.iter()
        .filter(|row| {
            matches!(field(row, "group"), "needs_input" | "ready_for_review")
                && previous
                    .iter()
                    .find(|old| field(old, "id") == field(row, "id"))
                    .is_none_or(|old| {
                        field(old, "group") != field(row, "group")
                            || status(old, false) != status(row, false)
                    })
        })
        .max_by_key(|row| (field(row, "group_entered_at"), field(row, "id")))
        .map(|row| {
            let group = field(row, "group").replace('_', " ");
            let attention = status(row, false);
            if attention == group {
                format!("{} {group}", name(row))
            } else {
                format!("{} {group} ({attention})", name(row))
            }
        })
}

fn terminal_bold_padding(start: usize, end: usize, row: u16) -> usize {
    let spaces = end - start;
    // The reference uses ECH for long runs of blanks. xterm erases the
    // foreground attributes there. A short run, or a short overwrite after
    // advancing to a tab stop, still paints its styled space cells.
    let erase_cost = format!("\x1b[{spaces}X").len() + format!("\x1b[{end};{row}H").len();
    if spaces <= erase_cost {
        return spaces;
    }
    let tabs = end / 8 - start / 8;
    let tail = end % 8;
    let move_cost = format!("\x1b[{spaces}C")
        .len()
        .min(format!("\x1b[{end}G").len());
    if tabs > 0 && tabs + tail < move_cost {
        tail
    } else {
        0
    }
}

fn editor_label(draft: &super::Editor, tag: bool, width: u16) -> String {
    if tag {
        format!("[{}]", block_cursor(draft, width.saturating_sub(2).max(1)))
    } else {
        block_cursor(draft, width.max(1))
    }
}

pub(super) fn edit_label(row: &Value, draft: &super::Editor, tag: bool, cols: u16) -> String {
    if compact(cols) {
        let (_, width) = compact_layout(row, cols, 2);
        editor_label(draft, tag, width)
    } else {
        block_cursor(draft, columns(cols).name.saturating_sub(1))
    }
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
    if let Some((message, _)) = &board.banner {
        let text = truncation(&format!("● {message}"), cols.saturating_sub(2));
        spans(screen, 2, cols, &[(PLAIN, "  ".into()), (TITLE, text)]);
    }
    let focus = board.current_focus();
    for (index, row) in board
        .display_rows()
        .iter()
        .skip(board.top)
        .take(board.body_capacity(height))
        .enumerate()
    {
        let selected = focus.is_some() && row.focus() == focus;
        let y = board.body_first(height) + index as u16;
        match row {
            Row::Session(row) => spans(screen, y, cols, &session(board, row, selected, cols, y)),
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
                let style = if board.layout.group == 0 && *id != super::discussions::SECTION {
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

    let overlay_hint = match &board.modal {
        Some(Modal::Edit { tag: true, .. }) => Some(TAG_HINT),
        Some(Modal::Edit { .. }) => Some(RENAME_HINT),
        Some(Modal::Confirm { .. }) => Some(CONFIRM_HINT),
        _ => None,
    };
    if let Some(hint) = overlay_hint {
        super::session_form::footer(screen, height, cols, hint);
        return;
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

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn wide_lineage_matches_earlier_attempts_on_the_same_owner() {
        let parent = json!({"endpoint_id":"a","id":"a/new","name":"Dispatch","role":"manager",
                            "supersedes":["a/old"]});
        let child = json!({"endpoint_id":"a","id":"a/child","spawned_from":"old","supervision_pending":true});
        let board = Board {
            rows: vec![
                parent.clone(),
                child.clone(),
                json!({"endpoint_id":"b","id":"b/old","name":"Other owner","status":{"Process":"running"}}),
            ],
            ..Board::default()
        };
        assert_eq!(
            summary(&board, &parent, 48),
            (" spawned 1".into(), " [manager]".into())
        );
        assert_eq!(
            summary(&board, &child, 48),
            (" from Dispatch".into(), " supervisor gone".into())
        );
    }

    #[test]
    fn banner_is_announced_when_the_visible_attention_changes() {
        let old = json!({"id":"a/one","name":"Fixture","group":"needs_input","status":{"Interaction":"prompt"}});
        let next = json!({"id":"a/one","name":"Fixture","group":"needs_input","status":{"Interaction":"permission"}});
        assert_eq!(
            attention_change(&[old], std::slice::from_ref(&next)).as_deref(),
            Some("Fixture needs input (approval)")
        );
        assert_eq!(
            attention_change(std::slice::from_ref(&next), std::slice::from_ref(&next)),
            None
        );
    }
}
