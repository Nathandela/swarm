package wire

// R1.4.1(c) perf baseline benchmark (docs/verification/perf-baseline-2026-07-18.md):
// frame round-trip (WriteFrame + ReadFrame) over a real net.Pipe, at a small
// (256B, one keystroke echo) and large (32KiB, a big styled snapshot chunk)
// payload size.

import (
	"bytes"
	"net"
	"testing"
)

func benchFrameRoundTrip(b *testing.B, payload []byte) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	var readErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < b.N; i++ {
			if _, _, err := ReadFrame(server); err != nil {
				readErr = err
				return
			}
		}
	}()

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := WriteFrame(client, TDataOut, payload); err != nil {
			b.Fatal(err)
		}
	}
	<-done
	if readErr != nil {
		b.Fatal(readErr)
	}
}

func BenchmarkFrameRoundTrip_256B(b *testing.B) {
	benchFrameRoundTrip(b, bytes.Repeat([]byte{0x5a}, 256))
}

func BenchmarkFrameRoundTrip_32KiB(b *testing.B) {
	benchFrameRoundTrip(b, bytes.Repeat([]byte{0x5a}, 32*1024))
}
