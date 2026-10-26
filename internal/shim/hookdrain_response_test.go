package shim

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func a1DrainResponse(t *testing.T) HookDrainResponse {
	t.Helper()
	body, err := json.Marshal(map[string]string{"text": strings.Repeat("<&>", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return HookDrainResponse{
		Records: []HookRecord{{Seq: ^uint64(0) - 1, Body: body}},
		Gap:     true, GapBoundary: ^uint64(0),
		SpoolIncarnation:    strings.Repeat("i", 1024),
		ProducerCertificate: strings.Repeat("p", 2048),
		SpoolAdoptedLegacy:  true,
	}
}

func TestHookDrainResponseEncodedBoundary(t *testing.T) {
	response := a1DrainResponse(t)
	want, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeHookDrainResponseLimit(response, len(want))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("maximum fit changed encoding: bytes=%d err=%v", len(got), err)
	}
	if got, err := encodeHookDrainResponseLimit(response, len(want)-1); err == nil || len(got) != 0 {
		t.Fatalf("encoded +1 accepted: bytes=%d err=%v", len(got), err)
	}
	// A limit that fits one complete record must not turn a larger reply into a prefix.
	response.Records = append(response.Records, HookRecord{Seq: ^uint64(0), Body: json.RawMessage(`{"second":true}`)})
	if got, err := encodeHookDrainResponseLimit(response, len(want)); err == nil || len(got) != 0 {
		t.Fatalf("over-cap multi-record response became success/prefix: bytes=%d err=%v", len(got), err)
	}
	response.Records[0].Body = json.RawMessage(`not-json`)
	if got, err := encodeHookDrainResponseLimit(response, HookDrainResponseMaxBytes); err == nil || len(got) != 0 {
		t.Fatalf("malformed socket RawMessage became success: bytes=%d err=%v", len(got), err)
	}
}

func TestHookDrainResponseMetadataBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		set   func(*HookDrainResponse, string)
	}{
		{name: "incarnation", limit: 1024, set: func(r *HookDrainResponse, value string) { r.SpoolIncarnation = value }},
		{name: "certificate", limit: 2048, set: func(r *HookDrainResponse, value string) { r.ProducerCertificate = value }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := a1DrainResponse(t)
			tc.set(&response, strings.Repeat("<", tc.limit))
			encoded, err := encodeHookDrainResponseLimit(response, HookDrainResponseMaxBytes)
			if err != nil {
				t.Fatalf("maximum valid metadata refused: %v", err)
			}
			if _, err := DecodeHookDrainResponse(bytes.NewReader(encoded)); err != nil {
				t.Fatalf("maximum valid metadata decode refused: %v", err)
			}
			tc.set(&response, strings.Repeat("<", tc.limit+1))
			if encoded, err := encodeHookDrainResponseLimit(response, HookDrainResponseMaxBytes); err == nil || len(encoded) != 0 {
				t.Fatalf("metadata +1 encoded successfully: bytes=%d err=%v", len(encoded), err)
			}
			// Bypass the encoder to test an oversized metadata field from a peer.
			encoded, err = json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > 64<<10 {
				t.Fatal("metadata boundary control unexpectedly needs a large response")
			}
			if _, err := DecodeHookDrainResponse(bytes.NewReader(encoded)); err == nil {
				t.Fatal("metadata +1 reached response authority")
			}
		})
	}
}

type a1CountingReader struct {
	r io.Reader
	n int
}

func (r *a1CountingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

func TestHookDrainDecoderEncodedBoundaryAndReadBudget(t *testing.T) {
	want := a1DrainResponse(t)
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeHookDrainResponseLimit(bytes.NewReader(data), len(data))
	if err != nil || len(got.Records) != 1 || got.Records[0].Seq != want.Records[0].Seq || got.GapBoundary != want.GapBoundary {
		t.Fatalf("maximum fit decoder rejected/changed reply: %+v %v", got, err)
	}
	if _, err := decodeHookDrainResponseLimit(bytes.NewReader(data), len(data)-1); err == nil {
		t.Fatal("decoder accepted encoded +1")
	}
	const limit = 128
	r := &a1CountingReader{r: bytes.NewReader(data)}
	_, err = decodeHookDrainResponseLimit(r, limit)
	if err == nil || r.n > limit+1 {
		t.Fatalf("decoder did not bound reads before parsing: read=%d limit=%d err=%v", r.n, limit, err)
	}
}

type a1ObjectWithoutEOF struct {
	data []byte
}

func (r *a1ObjectWithoutEOF) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.ErrNoProgress
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestHookDrainDecoderDoesNotWaitForEOF(t *testing.T) {
	r := &a1ObjectWithoutEOF{data: []byte(`{"records":[],"spool_incarnation":"legacy"}`)}
	if _, err := DecodeHookDrainResponse(r); err != nil {
		t.Fatalf("self-delimited response required connection EOF: %v", err)
	}
}

func TestHookSpoolBoundedReadCatchesPostStatGrowth(t *testing.T) {
	const limit = 128
	data := bytes.Repeat([]byte("x"), 2*limit)
	got, err := readHookSpoolBounded(bytes.NewReader(data[:limit]), limit)
	if err != nil || !bytes.Equal(got, data[:limit]) {
		t.Fatalf("maximum fit read refused: bytes=%d err=%v", len(got), err)
	}
	// The finite reader models growth beyond a size that previously fit Stat.
	// Even with the guard omitted, this test can consume only 256 bytes.
	r := &a1CountingReader{r: bytes.NewReader(data)}
	got, err = readHookSpoolBounded(r, limit)
	if !errors.Is(err, ErrHookCleanupTooLarge) || len(got) != 0 || r.n > limit+1 {
		t.Fatalf("post-Stat growth bypassed read budget: read=%d result=%d err=%v", r.n, len(got), err)
	}
}

func a1OversizedSpool(t *testing.T) (*HookSpool, string) {
	t.Helper()
	s, path := openTestSpool(t, hookSpoolDefaultMaxBytes+1024)
	// This is a valid custom-spool record, not a new restriction on Append.
	body := []byte(`"` + strings.Repeat("x", hookSpoolDefaultMaxBytes+1-hookRecordHeaderLen-2) + `"`)
	if _, err := s.Append(body); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != int64(hookSpoolDefaultMaxBytes+1) {
		t.Fatalf("oversize fixture: info=%v err=%v", info, err)
	}
	return s, path
}

func TestHookDrainDefaultSpoolReadBound(t *testing.T) {
	for _, mode := range []string{"live", "disk"} {
		t.Run(mode, func(t *testing.T) {
			s, path := a1OversizedSpool(t)
			var err error
			if mode == "live" {
				_, err = s.readDrainResponse(0, 0)
			} else {
				_, err = ReadHookSpoolFileResponse(path, 0)
			}
			if err == nil {
				t.Fatal("DRAIN parsed a file above the supported default spool bound")
			}
			if info, statErr := os.Stat(path); statErr != nil || info.Size() != int64(hookSpoolDefaultMaxBytes+1) {
				t.Fatalf("refused custom spool was changed: info=%v err=%v", info, statErr)
			}
		})
	}
}

func a1ServeHookSpool(t *testing.T, spool *HookSpool) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "a1h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "h")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHookServer(l, spool, "")
	done := make(chan struct{})
	go func() { defer close(done); h.acceptLoop() }()
	t.Cleanup(func() { h.shutdown(); <-done })
	return path
}

func a1ReadDrainSocket(t *testing.T, path string) (HookDrainResponse, error) {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(hookConnTimeout))
	request, err := json.Marshal(HookDrainRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append([]byte{HookDrainTag}, request...)); err != nil {
		t.Fatal(err)
	}
	return DecodeHookDrainResponse(conn)
}

func TestHookDrainSocketRefusesOversizedDefaultSpool(t *testing.T) {
	s, _ := a1OversizedSpool(t)
	if _, err := a1ReadDrainSocket(t, a1ServeHookSpool(t, s)); err == nil {
		t.Fatal("live DRAIN bypassed the default-spool read bound")
	}
}

func TestHookDrainLegacyAboveWireFrameSurvivesSocket(t *testing.T) {
	s, _ := openTestSpool(t, 0)
	text := strings.Repeat("<", (1<<20)+32)
	body := []byte(`{"text":"` + text + `"}`)
	if _, err := s.Append(body); err != nil {
		t.Fatal(err)
	}
	reply, err := a1ReadDrainSocket(t, a1ServeHookSpool(t, s))
	if err != nil || len(reply.Records) != 1 {
		t.Fatalf("preserved large legacy record refused: records=%d err=%v", len(reply.Records), err)
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(reply.Records[0].Body, &decoded); err != nil || decoded.Text != text {
		t.Fatalf("legacy logical body changed: bytes=%d err=%v", len(decoded.Text), err)
	}
	if HookDrainResponseMaxBytes < 6*hookSpoolDefaultMaxBytes+(64<<10) {
		t.Fatal("reply cap no longer covers legacy escaping/record/metadata bound")
	}
}
