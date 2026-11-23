// Package workermail stores accepted VM worker notes on the owning supervisor.
package workermail

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

const ReceiptRetention = 7 * 24 * time.Hour

const SocketEnv = "SWARM_WORKER_MAILBOX_SOCKET"
const ProofEnv = "SWARM_DISPATCHER_SPAWN_PROOF"
const WorkerProofEnv = "SWARM_WORKER_MAILBOX_PROOF"

func Proof(token, purpose string) string {
	if token == "" {
		return ""
	}
	h := hmac.New(sha256.New, []byte(token))
	h.Write([]byte(purpose))
	return hex.EncodeToString(h.Sum(nil))
}

type Binding struct {
	Discussion, Parent, Session, Instance, Attempt, Agent, Project string
	ParentInputHash                                                string `json:",omitempty"`
}
type Note struct {
	Binding
	ID, Kind, Text, State, Answer string
	Sequence                      uint64
	Created                       time.Time
	Settled                       time.Time
}

func ValidText(text string) bool {
	return len(text) <= 4096 && utf8.ValidString(text) && strings.TrimSpace(text) != "" && !strings.ContainsRune(text, 0)
}
func ValidKind(kind string) bool { return kind == "" || kind == "needs-input" || kind == "complete" }

func transact(dir string, write bool, fn func(*bolt.Tx) error) error {
	if !filepath.IsAbs(dir) {
		return errors.New("worker mailbox needs a supervisor state directory")
	}
	path := filepath.Join(dir, "dispatcher-mail.db")
	_, statErr := os.Lstat(path)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !fresh {
		return statErr
	}
	if !write && fresh {
		return nil
	}
	opts := &bolt.Options{Timeout: time.Second, ReadOnly: !write}
	if !write {
		opts.OpenFile = openMailboxForRead
	}
	db, err := bolt.Open(path, 0600, opts)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if fresh {
		directory, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			return err
		}
	}
	if !write {
		return db.View(fn)
	}
	return db.Update(fn)
}

func openMailboxForRead(path string, _ int, _ os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
			err = errors.New("worker mailbox: expected a private regular file")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func noteKey(b Binding, id string) []byte {
	return []byte(strings.Join([]string{b.Discussion, b.Session, b.Instance, b.Attempt, id}, "\x00"))
}
func orderKey(n Note) []byte {
	return binary.BigEndian.AppendUint64(append([]byte(n.Discussion), 0), n.Sequence)
}
func save(b *bolt.Bucket, key []byte, n Note) error {
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return b.Put(key, raw)
}

var ErrAbsent = errors.New("worker mailbox: unknown note")
var ErrConflict = errors.New("worker mailbox: conflicting note")

func Submit(dir string, b Binding, id, kind, text string, fresh bool) (n Note, err error) {
	if b.Discussion == "" || b.Instance == "" || b.Session == "" || b.Parent == "" || b.Attempt == "" || id == "" || len(id) > 128 || !ValidKind(kind) || !ValidText(text) {
		return n, errors.New("invalid worker note")
	}
	err = transact(dir, true, func(tx *bolt.Tx) error {
		notes, e := tx.CreateBucketIfNotExists([]byte("notes"))
		if e != nil {
			return e
		}
		key := noteKey(b, id)
		if raw := notes.Get(key); raw != nil {
			if e = json.Unmarshal(raw, &n); e != nil {
				return e
			}
			if n.Binding != b || n.Text != text || n.Kind != kind {
				return ErrConflict
			}
			return nil
		}
		if !fresh {
			return errors.New("worker mailbox: expired new note")
		}
		pending, e := tx.CreateBucketIfNotExists([]byte("pending"))
		if e != nil {
			return e
		}
		if err := pruneSettled(tx, time.Now()); err != nil {
			return err
		}
		if notes.Stats().KeyN >= 8192 || pending.Stats().KeyN >= 4096 {
			return errors.New("worker mailbox: backlog full; retry later")
		}
		seq, e := pending.NextSequence()
		if e != nil {
			return e
		}
		n = Note{Binding: b, ID: id, Kind: kind, Text: text, State: "queued", Sequence: seq, Created: time.Now().UTC()}
		if e = save(notes, key, n); e != nil {
			return e
		}
		return pending.Put(orderKey(n), key)
	})
	return
}

func Lookup(dir string, b Binding, id string) (n Note, err error) {
	err = transact(dir, false, func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("notes"))
		if bucket == nil {
			return ErrAbsent
		}
		raw := bucket.Get(noteKey(b, id))
		if raw == nil {
			return ErrAbsent
		}
		return json.Unmarshal(raw, &n)
	})
	if err == nil && n.ID == "" {
		err = ErrAbsent
	}
	return
}

func Pending(dir, discussion string) (out []Note, err error) {
	err = transact(dir, false, func(tx *bolt.Tx) error {
		p := tx.Bucket([]byte("pending"))
		if p == nil {
			return nil
		}
		notes := tx.Bucket([]byte("notes"))
		cursor := p.Cursor()
		prefix := append([]byte(discussion), 0)
		for k, v := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix) && len(out) < 64; k, v = cursor.Next() {
			var n Note
			if e := json.Unmarshal(notes.Get(v), &n); e != nil {
				return e
			}
			out = append(out, n)
		}
		return nil
	})
	return
}
func HasPending(dir, discussion string) (bool, error) {
	var pending bool
	err := transact(dir, false, func(tx *bolt.Tx) error {
		if bucket := tx.Bucket([]byte("pending")); bucket != nil {
			prefix := append([]byte(discussion), 0)
			key, _ := bucket.Cursor().Seek(prefix)
			pending = bytes.HasPrefix(key, prefix)
		}
		return nil
	})
	return pending, err
}

func Discussions(dir string) (out map[string]bool, err error) {
	out = map[string]bool{}
	err = transact(dir, false, func(tx *bolt.Tx) error {
		p := tx.Bucket([]byte("pending"))
		if p == nil {
			return nil
		}
		c := p.Cursor()
		for k, _ := c.First(); k != nil; {
			i := bytes.IndexByte(k, 0)
			if i < 1 {
				return errors.New("worker mailbox: invalid discussion key")
			}
			out[string(k[:i])] = true
			next := append([]byte(nil), k[:i]...)
			next = append(next, 1)
			k, _ = c.Seek(next)
		}
		return nil
	})
	return
}

func Acknowledge(dir string, n Note, answer string) error {
	return updateReceipt(dir, n, answer, false)
}
func Reject(dir string, n Note) error { return updateReceipt(dir, n, "", true) }

func updateReceipt(dir string, n Note, answer string, reject bool) error {
	return transact(dir, true, func(tx *bolt.Tx) error {
		notes := tx.Bucket([]byte("notes"))
		if notes == nil {
			return ErrAbsent
		}
		key := noteKey(n.Binding, n.ID)
		raw := notes.Get(key)
		if raw == nil {
			return ErrAbsent
		}
		var prior Note
		if err := json.Unmarshal(raw, &prior); err != nil {
			return err
		}
		if n.Sequence != prior.Sequence {
			return ErrConflict
		}
		if reject {
			if prior.State != "queued" && prior.State != "superseded" {
				return ErrConflict
			}
			prior.State = "superseded"
		} else if answer != "" {
			if !ValidText(answer) || prior.Answer != "" && prior.Answer != answer {
				return ErrConflict
			}
			prior.Answer, prior.State = answer, "answered"
		} else if prior.State == "queued" {
			prior.State = "ingested"
		}
		if prior.Settled.IsZero() && (prior.State == "superseded" || prior.Kind == "complete" || prior.Kind == "" && prior.State == "ingested") {
			if err := settle(tx, key, &prior, time.Now()); err != nil {
				return err
			}
		}
		if err := save(notes, key, prior); err != nil {
			return err
		}
		return tx.Bucket([]byte("pending")).Delete(orderKey(n))
	})
}

// Retain unconsumed answers. A lost read acknowledgement can repeat during the
// same seven-day receipt horizon as the discussion ledger.
func MarkAnswerRead(dir string, n Note) error {
	if n.Answer == "" || !n.Settled.IsZero() {
		return nil
	}
	return transact(dir, true, func(tx *bolt.Tx) error {
		notes := tx.Bucket([]byte("notes"))
		if notes == nil {
			return ErrAbsent
		}
		key := noteKey(n.Binding, n.ID)
		var prior Note
		if err := json.Unmarshal(notes.Get(key), &prior); err != nil {
			return err
		}
		if prior.Answer != n.Answer {
			return ErrConflict
		}
		if !prior.Settled.IsZero() {
			return nil
		}
		if err := settle(tx, key, &prior, time.Now()); err != nil {
			return err
		}
		return save(notes, key, prior)
	})
}
func settle(tx *bolt.Tx, key []byte, n *Note, now time.Time) error {
	settled, err := tx.CreateBucketIfNotExists([]byte("settled"))
	if err != nil {
		return err
	}
	n.Settled = now.UTC()
	k := binary.BigEndian.AppendUint64(nil, uint64(now.UnixMilli()))
	k = append(k, key...)
	return settled.Put(k, key)
}
func pruneSettled(tx *bolt.Tx, now time.Time) error {
	settled := tx.Bucket([]byte("settled"))
	if settled == nil {
		return nil
	}
	cutoff := uint64(now.Add(-ReceiptRetention).UnixMilli())
	notes := tx.Bucket([]byte("notes"))
	c := settled.Cursor()
	for i := 0; i < 64; i++ {
		k, v := c.First()
		if len(k) < 8 || binary.BigEndian.Uint64(k[:8]) > cutoff {
			break
		}
		if err := notes.Delete(v); err != nil {
			return err
		}
		if err := c.Delete(); err != nil {
			return err
		}
	}
	return nil
}
