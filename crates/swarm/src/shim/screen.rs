use serde_json::{Value, json};
use std::io;
use vt100::{Cell, Color, MouseProtocolEncoding, MouseProtocolMode, Screen};

pub(super) fn terminal(screen: &Screen) -> Value {
    let (rows, cols) = screen.size();
    let lines: Vec<_> = (0..rows.get())
        .map(|row| {
            let mut text = String::new();
            for (_, cell) in cells(screen, row, cols.get()) {
                append_text(&mut text, cell);
            }
            text
        })
        .collect();
    json!({"cols": cols.get(), "rows": rows.get(), "lines": lines})
}

pub(super) fn snapshot(screen: &Screen, mouse: Value) -> io::Result<Vec<u8>> {
    let (rows, cols) = screen.size();
    let (cursor_y, cursor_x) = screen.cursor_position();
    let lines: Vec<_> = (0..rows.get())
        .map(|row| {
            let mut runs = Vec::new();
            let mut text = String::new();
            let mut width = 0;
            let mut style = Style::default();
            for (col, cell) in cells(screen, row, cols.get()) {
                let next = Style::from(cell);
                if width != 0 && style != next {
                    runs.push(style.run(&text, width));
                    text.clear();
                    width = 0;
                }
                style = next;
                append_text(&mut text, cell);
                width += if cell.is_wide() && col + 1 < cols.get() {
                    2
                } else {
                    1
                };
            }
            if width != 0 {
                runs.push(style.run(&text, width));
            }
            json!({"runs": runs})
        })
        .collect();
    Ok(serde_json::to_vec(&json!({
        "version": 1, "cols": cols.get(), "rows": rows.get(),
        "altScreen": screen.alternate_screen(), "cursorX": cursor_x,
        "cursorY": cursor_y, "cursorVisible": !screen.hide_cursor(), "lines": lines,
        "inputModes": input_modes(screen), "mouse": mouse,
    }))?)
}

fn input_modes(screen: &Screen) -> Value {
    let tracking = match screen.mouse_protocol_mode() {
        MouseProtocolMode::None => 0,
        MouseProtocolMode::Press => 9,
        MouseProtocolMode::PressRelease => 1000,
        MouseProtocolMode::ButtonMotion => 1002,
        MouseProtocolMode::AnyMotion => 1003,
    };
    let encoding = match screen.mouse_protocol_encoding() {
        MouseProtocolEncoding::Default => 0,
        MouseProtocolEncoding::Utf8 => 1005,
        MouseProtocolEncoding::Sgr => 1006,
    };
    // shortcut: vt100 exposes these DEC states but has no focus or alternate-scroll state.
    json!({"known":true, "tracking":tracking, "encoding":encoding,
           "paste":screen.bracketed_paste(), "cursorKeys":screen.application_cursor()})
}

fn cells(screen: &Screen, row: u16, cols: u16) -> impl Iterator<Item = (u16, &Cell)> {
    (0..cols).filter_map(move |col| {
        screen
            .cell(row, col)
            .filter(|cell| !cell.is_wide_continuation())
            .map(|cell| (col, cell))
    })
}

fn append_text(text: &mut String, cell: &Cell) {
    if cell.contents().is_empty() {
        text.push(' ');
    } else {
        text.push_str(&sanitize(cell.contents()));
    }
}

fn sanitize(text: &str) -> String {
    // Match the protected renderer's treatment of terminal control and bidi marks.
    text.chars()
        .map(|ch| {
            if ch.is_control()
                || matches!(ch,
            '\u{00a0}' | '\u{00ad}' | '\u{061c}' | '\u{180e}' | '\u{2000}'..='\u{200f}'
            | '\u{2028}'..='\u{202f}' | '\u{205f}'..='\u{206f}' | '\u{3000}' | '\u{feff}'
            | '\u{fff9}'..='\u{fffb}' | '\u{e0001}'..='\u{e007f}')
            {
                ' '
            } else {
                ch
            }
        })
        .collect()
}

#[derive(Default, PartialEq, Eq)]
struct Style {
    fg: Color,
    bg: Color,
    bold: bool,
    faint: bool,
    italic: bool,
    underline: bool,
    reverse: bool,
}

impl From<&Cell> for Style {
    fn from(cell: &Cell) -> Self {
        Self {
            fg: cell.fgcolor(),
            bg: cell.bgcolor(),
            bold: cell.bold(),
            faint: cell.dim(),
            italic: cell.italic(),
            underline: cell.underline(),
            reverse: cell.inverse(),
        }
    }
}

impl Style {
    fn run(&self, text: &str, width: u16) -> Value {
        let mut run = json!({"text": text, "width": width});
        for (key, enabled) in [
            ("bold", self.bold),
            ("faint", self.faint),
            ("italic", self.italic),
            ("underline", self.underline),
            ("reverse", self.reverse),
        ] {
            if enabled {
                run[key] = json!(true);
            }
        }
        for (key, color) in [("fg", self.fg), ("bg", self.bg)] {
            if let Some((r, g, b)) = rgb(color) {
                run[key] = json!(format!("#{r:02x}{g:02x}{b:02x}"));
            }
        }
        run
    }
}

fn rgb(color: Color) -> Option<(u8, u8, u8)> {
    let palette = [
        (0, 0, 0),
        (128, 0, 0),
        (0, 128, 0),
        (128, 128, 0),
        (0, 0, 128),
        (128, 0, 128),
        (0, 128, 128),
        (192, 192, 192),
        (128, 128, 128),
        (255, 0, 0),
        (0, 255, 0),
        (255, 255, 0),
        (0, 0, 255),
        (255, 0, 255),
        (0, 255, 255),
        (255, 255, 255),
    ];
    match color {
        Color::Default => None,
        Color::Rgb(r, g, b) => Some((r, g, b)),
        Color::Idx(index @ 0..=15) => Some(palette[index as usize]),
        Color::Idx(index @ 16..=231) => {
            let color = index - 16;
            let level = |val| if val == 0 { 0 } else { 55 + 40 * val };
            Some((level(color / 36), level(color / 6 % 6), level(color % 6)))
        }
        Color::Idx(index) => {
            let grey = 8 + 10 * (index - 232);
            Some((grey, grey, grey))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn split_input_and_sanitized_snapshot() {
        let mut parser = vt100::Parser::new(3.try_into().unwrap(), 8.try_into().unwrap(), 0);
        parser.process(b"\x1b[");
        parser.process("31m年X\u{202e}\x1b[0m".as_bytes());
        let screen = parser.screen();
        let snap: Value = serde_json::from_slice(&snapshot(screen, Value::Null).unwrap()).unwrap();
        assert_eq!(snap["lines"][0]["runs"][0]["width"], 3);
        assert_eq!(snap["lines"][0]["runs"][0]["fg"], "#800000");
        assert_eq!(snap["lines"][0]["runs"][0]["text"], "年X ");
        assert_eq!(sanitize("a\u{202e}b\x1b"), "a b ");
        assert_eq!(
            terminal(screen)["lines"],
            json!(["年X      ", "        ", "        "])
        );
    }
}
