use std::io::{self, Read, Write};

pub(crate) const MAX_FRAME: usize = 1 << 20;
pub const CONTROL: u8 = 1;

pub fn read_frame(reader: &mut impl Read) -> io::Result<(u8, Vec<u8>)> {
    let mut prefix = [0; 4];
    reader.read_exact(&mut prefix)?;
    let length = u32::from_be_bytes(prefix) as usize;
    if !(1..=MAX_FRAME).contains(&length) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "wire: invalid frame length",
        ));
    }
    let mut tag = [0];
    reader.read_exact(&mut tag)?;
    if !(1..=4).contains(&tag[0]) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "wire: unknown frame type",
        ));
    }
    let mut payload = vec![0; length - 1];
    reader.read_exact(&mut payload)?;
    Ok((tag[0], payload))
}

pub fn write_frame(writer: &mut impl Write, tag: u8, payload: &[u8]) -> io::Result<()> {
    if payload.len() >= MAX_FRAME || !(1..=4).contains(&tag) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "wire: invalid frame",
        ));
    }
    writer.write_all(&((payload.len() + 1) as u32).to_be_bytes())?;
    writer.write_all(&[tag])?;
    writer.write_all(payload)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frozen_envelope_and_limits() {
        let mut bytes = Vec::new();
        write_frame(&mut bytes, CONTROL, b"{}").unwrap();
        assert_eq!(bytes, b"\0\0\0\x03\x01{}");
        assert_eq!(
            read_frame(&mut bytes.as_slice()).unwrap(),
            (CONTROL, b"{}".to_vec())
        );

        for invalid in [&b"\0\0\0\0"[..], b"\0\x10\0\x01", b"\0\0\0\x01\x05"] {
            assert_eq!(
                read_frame(&mut &*invalid).unwrap_err().kind(),
                io::ErrorKind::InvalidData
            );
        }
        assert_eq!(
            read_frame(&mut &bytes[..bytes.len() - 1])
                .unwrap_err()
                .kind(),
            io::ErrorKind::UnexpectedEof
        );
    }
}
