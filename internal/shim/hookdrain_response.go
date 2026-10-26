package shim

import (
	"encoding/json"
	"errors"
	"io"
)

// HookDrainResponseMaxBytes bounds unframed DRAIN JSON, independently of wire.MaxFrame.
// Sixfold JSON escaping of the default 8MiB spool plus bounded metadata fits 64MiB.
const HookDrainResponseMaxBytes = 64 << 20

var errHookDrainResponseBounds = errors.New("shim: hook drain response exceeds supported bounds")

func checkHookDrainResponseMetadata(response HookDrainResponse) error {
	if len(response.SpoolIncarnation) > 1024 || len(response.ProducerCertificate) > 2048 {
		return errHookDrainResponseBounds
	}
	return nil
}

// encodeHookDrainResponseLimit preserves legacy encoding without trimming a prefix.
func encodeHookDrainResponseLimit(response HookDrainResponse, maxBytes int) ([]byte, error) {
	if maxBytes < 1 || maxBytes > HookDrainResponseMaxBytes {
		return nil, errHookDrainResponseBounds
	}
	if err := checkHookDrainResponseMetadata(response); err != nil {
		return nil, err
	}
	data, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, errHookDrainResponseBounds
	}
	return data, nil
}

// DecodeHookDrainResponse reads one self-delimited reply; the connection need not reach EOF.
func DecodeHookDrainResponse(r io.Reader) (HookDrainResponse, error) {
	return decodeHookDrainResponseLimit(r, HookDrainResponseMaxBytes)
}

func decodeHookDrainResponseLimit(r io.Reader, maxBytes int) (HookDrainResponse, error) {
	if maxBytes < 1 || maxBytes > HookDrainResponseMaxBytes {
		return HookDrainResponse{}, errHookDrainResponseBounds
	}
	limited := &io.LimitedReader{R: r, N: int64(maxBytes) + 1}
	decoder := json.NewDecoder(limited)
	var response HookDrainResponse
	if err := decoder.Decode(&response); err != nil {
		return HookDrainResponse{}, err
	}
	if decoder.InputOffset() > int64(maxBytes) {
		return HookDrainResponse{}, errHookDrainResponseBounds
	}
	if err := checkHookDrainResponseMetadata(response); err != nil {
		return HookDrainResponse{}, err
	}
	return response, nil
}
