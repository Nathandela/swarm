package phonecore

import (
	"encoding/json"
	"sync/atomic"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// InputFrame is the plaintext the phone seals into a mailbox envelope for a
// keystroke burst ("data") or a terminal resize ("resize"). It shares the phone
// -> machine mailbox with commands; the discriminating `t` field lets the
// machine side tell an input frame from a RemoteCommand (which carries no `t`).
type InputFrame struct {
	T    string `json:"t"` // "data" or "resize"
	Data []byte `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// Sequencer hands out the strictly increasing seq numbers (1, 2, 3, ...) that
// stamp EVERY phone -> machine mailbox envelope. Commands AND input frames draw
// from ONE Sequencer per epoch because they share a single MailboxReceiver key
// (SenderKeyID stays zero), so a private per-kind counter would collide.
type Sequencer struct{ n atomic.Uint64 }

// Next returns the next seq (1 on first call). Safe for concurrent use.
func (s *Sequencer) Next() uint64 { return s.n.Add(1) }

// SealInputData seals a keystroke burst as a mailbox INPUT-FRAME envelope under
// the epoch content key so it travels through the untrusted relay as ciphertext,
// mirroring SealCommandEnvelope. seq must be unique per epoch (from a Sequencer
// shared with commands).
func SealInputData(key crypto.ContentKey, epochID uint32, seq uint64, data []byte) ([]byte, error) {
	return sealInputFrame(key, epochID, seq, InputFrame{T: "data", Data: data})
}

// SealInputResize seals a terminal resize as a mailbox INPUT-FRAME envelope,
// mirroring SealInputData. seq must be unique per epoch.
func SealInputResize(key crypto.ContentKey, epochID uint32, seq uint64, cols, rows int) ([]byte, error) {
	return sealInputFrame(key, epochID, seq, InputFrame{T: "resize", Cols: cols, Rows: rows})
}

func sealInputFrame(key crypto.ContentKey, epochID uint32, seq uint64, f InputFrame) ([]byte, error) {
	plaintext, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1,
		EpochID: epochID,
		Seq:     seq,
	}, plaintext)
	if err != nil {
		return nil, err
	}
	return env.Marshal(), nil
}
