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
//
// Session names the target namespaced session id and is bound INSIDE the sealed
// envelope, so it is authentic end to end: the untrusted relay can drop or reorder
// sealed frames but cannot alter their contents. The machine routes the keystroke
// by THIS field, never by mutable focus state -- an input for a session whose
// take_control the relay dropped then finds no lease and is dropped, never riding
// another session's live lease (A7 cross-session misroute).
type InputFrame struct {
	T       string `json:"t"`           // "data" or "resize"
	Session string `json:"s,omitempty"` // target namespaced session id
	Data    []byte `json:"data,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
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
// mirroring SealCommandEnvelope. session is the target namespaced session id, bound
// inside the sealed frame so the machine routes by it. seq must be unique per epoch
// (from a Sequencer shared with commands).
func SealInputData(key crypto.ContentKey, epochID uint32, seq uint64, session string, data []byte) ([]byte, error) {
	return sealInputFrame(key, epochID, seq, InputFrame{T: "data", Session: session, Data: data})
}

// SealInputResize seals a terminal resize as a mailbox INPUT-FRAME envelope,
// mirroring SealInputData. session is the target namespaced session id; seq must be
// unique per epoch.
func SealInputResize(key crypto.ContentKey, epochID uint32, seq uint64, session string, cols, rows int) ([]byte, error) {
	return sealInputFrame(key, epochID, seq, InputFrame{T: "resize", Session: session, Cols: cols, Rows: rows})
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
