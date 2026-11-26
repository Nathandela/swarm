use super::launch::{ACCENT, DIM, PLAIN, TITLE, field, label_layout, spans, status};
use super::navigation::{GROUPS, ORDERS};
use super::preferences::Layout;
use unicode_width::UnicodeWidthStr;

const HINT: &str = "←→ change · tab/↑↓ next · enter apply · esc cancel";
const CAPTURE_HINT: &str = "←→ change · terminal-wide mouse capture can affect text selection · tab/↑↓ next · enter apply · esc cancel";
const CAPTURE_NOTICE: [&str; 2] = [
    "Touch captures the whole screen.",
    "Text selection may need your terminal's override.",
];

pub(super) fn fits((cols, rows): (u16, u16)) -> bool {
    cols >= 32 && rows >= 8
}

pub(super) fn view(out: &mut String, cols: u16, rows: u16, layout: Layout, focus: usize) {
    let group = picker(&GROUPS, layout.group, cols);
    let order = picker(&ORDERS, layout.order, cols);
    let touch = [(
        PLAIN,
        if layout.touch_back {
            "[x] on"
        } else {
            "[ ] off"
        }
        .into(),
    )];
    let fields = [
        ("group", group.as_slice()),
        ("order", order.as_slice()),
        ("CLI button", touch.as_slice()),
    ];
    let capture_notice = layout.touch_back || focus == 2;
    if !fits((cols, rows)) {
        spans(out, 1, cols, &[(PLAIN, "Resize to 32×8".into())]);
        field(out, 2, cols, fields[focus].0, fields[focus].1, true);
        spans(out, 3, cols, &[(PLAIN, "Esc back / n deny".into())]);
        status(out, rows, cols, "esc back");
        return;
    }
    let compact = capture_notice
        && cols < 64
        && rows <= 10
        && CAPTURE_NOTICE
            .iter()
            .any(|notice| notice.width() > usize::from(cols));
    if compact {
        spans(out, 1, cols, &[(PLAIN, "Options".into())]);
        field(out, 2, cols, fields[focus].0, fields[focus].1, true);
        let mut row = 3;
        for notice in CAPTURE_NOTICE {
            let mut line = String::new();
            for word in notice.split_whitespace() {
                if !line.is_empty() && line.width() + 1 + word.width() > usize::from(cols) {
                    spans(out, row, cols, &[(PLAIN, std::mem::take(&mut line))]);
                    row += 1;
                }
                if !line.is_empty() {
                    line.push(' ');
                }
                line.push_str(word);
            }
            spans(out, row, cols, &[(PLAIN, line)]);
            row += 1;
        }
        for (index, (label, value)) in fields.into_iter().enumerate() {
            if index != focus && row < rows {
                field(out, row, cols, label, value, false);
                row += 1;
            }
        }
    } else {
        spans(
            out,
            1,
            cols,
            &[(TITLE, "swarm".into()), (DIM, " · options".into())],
        );
        for (index, (label, value)) in fields.into_iter().enumerate() {
            field(out, 3 + index as u16, cols, label, value, index == focus);
        }
        if capture_notice {
            for (i, text) in CAPTURE_NOTICE.into_iter().enumerate() {
                spans(out, 6 + i as u16, cols, &[(DIM, text.into())]);
            }
        }
    }
    status(
        out,
        rows,
        cols,
        if focus == 2 { CAPTURE_HINT } else { HINT },
    );
}

fn picker(labels: &[&str], selected: usize, cols: u16) -> Vec<(&'static str, String)> {
    let mut parts = vec![(DIM, "◂ ".into())];
    for (index, label) in labels.iter().enumerate() {
        if index > 0 {
            parts.push((PLAIN, "   ".into()));
        }
        parts.push(if index == selected {
            (ACCENT, format!("● {label}"))
        } else {
            (PLAIN, format!("○ {label}"))
        });
    }
    parts.push((DIM, " ▸".into()));
    // The three retained fields all use the same label padding.
    let (_, budget) = label_layout("CLI button", cols);
    if parts.iter().map(|(_, text)| text.width()).sum::<usize>() <= usize::from(budget) {
        parts
    } else {
        vec![
            (DIM, "◂ ".into()),
            (ACCENT, labels[selected].into()),
            (DIM, " ▸".into()),
        ]
    }
}
