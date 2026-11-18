package journal

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
)

const journalSourceFile = "source.json"
const journalSourceMarker = "source-initialized"

// SourceID names this durable journal namespace, independently of its cursor.
func (j *Journal) SourceID() string { return j.sourceID }

type journalSourceDocument struct {
	Version  int    `json:"version"`
	SourceID string `json:"source_id"`
}

func validJournalSourceID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id[14] != '4' || id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b' {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
func newJournalSourceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&15 | 64
	raw[8] = raw[8]&63 | 128
	s := hex.EncodeToString(raw[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func privateJournalSource(info os.FileInfo, dir bool) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return false
	}
	if dir {
		return info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 == 0
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && owner.Nlink == 1 && info.Size() <= 512
}
func readJournalSource(root *os.Root, name string) (string, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !privateJournalSource(info, false) {
		return "", errors.New("journal: unsafe source identity")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 513))
	if err != nil {
		return "", err
	}
	var doc journalSourceDocument
	if json.Unmarshal(raw, &doc) != nil || doc.Version != 1 || !validJournalSourceID(doc.SourceID) {
		return "", errors.New("journal: damaged or unsupported source identity")
	}
	canonical, _ := json.Marshal(doc)
	if !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		return "", errors.New("journal: noncanonical source identity")
	}
	return doc.SourceID, nil
}

// Link publishes a fully synced inode without replacing another opener's identity.
func createJournalSource(root *os.Root, name, id string) error {
	token, err := newJournalSourceID()
	if err != nil {
		return err
	}
	temp := ".source-" + token
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temp) }()
	raw, _ := json.Marshal(journalSourceDocument{Version: 1, SourceID: id})
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = root.Link(temp, name); err != nil {
		return err
	}
	if err = root.Remove(temp); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func openJournalSource(dir string) (string, os.FileInfo, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return "", nil, err
	}
	if !privateJournalSource(info, true) {
		return "", nil, errors.New("journal: unsafe source directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return "", nil, errors.New("journal: source directory changed")
	}
	id, err := readJournalSource(root, journalSourceFile)
	if errors.Is(err, os.ErrNotExist) {
		if _, markerErr := root.Lstat(journalSourceMarker); !errors.Is(markerErr, os.ErrNotExist) {
			return "", nil, errors.New("journal: source identity is missing after initialization")
		}
		id, err = newJournalSourceID()
		if err != nil {
			return "", nil, err
		}
		if err = createJournalSource(root, journalSourceFile, id); err != nil && !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
		id, err = readJournalSource(root, journalSourceFile)
	}
	if err != nil {
		return "", nil, err
	}
	marker, err := readJournalSource(root, journalSourceMarker)
	if errors.Is(err, os.ErrNotExist) {
		if err = createJournalSource(root, journalSourceMarker, id); err != nil && !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
		marker, err = readJournalSource(root, journalSourceMarker)
	}
	if err != nil {
		return "", nil, err
	}
	if marker != id {
		return "", nil, errors.New("journal: source identity conflicts with initialization marker")
	}
	return id, info, nil
}

func validateJournalSource(dir, id string, anchor os.FileInfo) error {
	current, err := os.Lstat(dir)
	if err != nil || anchor == nil || !os.SameFile(anchor, current) || !privateJournalSource(current, true) {
		return errors.New("journal: source directory custody changed")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(anchor, opened) {
		return errors.New("journal: source directory custody changed")
	}
	for _, name := range []string{journalSourceFile, journalSourceMarker} {
		actual, err := readJournalSource(root, name)
		if err != nil {
			return err
		}
		if actual != id {
			return errors.New("journal: source identity changed")
		}
	}
	return nil
}
