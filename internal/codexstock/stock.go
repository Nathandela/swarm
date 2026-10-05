// Package codexstock recognizes only the pinned native-generated Codex skills
// tree. It never executes native code, reads credentials, or mutates assets.
package codexstock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const (
	Version           = "0.160.0"
	Digest            = "8feb5b0889075ff695465a068fbeec422e3d27c8074a48af16185f92e8ac406c"
	PendingFile       = ".swarm-codex-stock-skills-update.json"
	CustodyFile       = ".swarm-codex-stock-skills.json"
	RetainedDirectory = ".swarm-codex-stock-skills-0.160.0"
	ContextUpdateFile = ".swarm-codex-context-update.json"
)

var ErrInvalid = errors.New("invalid pinned native stock custody")

type Identity struct{ Device, Inode uint64 }
type Receipt struct {
	SchemaVersion                                               int
	NativeVersion, GlobalGeneration, SourceTarget, SourceSHA256 string
	RetainedDirectory, StockSHA256                              string
	Profile, Stock                                              Identity
}
type OuterGuard struct {
	SchemaVersion int
	StockSkills   Receipt
}

func identity(info os.FileInfo) (Identity, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0002 != 0 || (!info.IsDir() && (!info.Mode().IsRegular() || stat.Nlink != 1)) {
		return Identity{}, false
	}
	return Identity{uint64(stat.Dev), stat.Ino}, true
}

func ProfileIdentity(root *os.Root) (Identity, error) {
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return Identity{}, ErrInvalid
	}
	id, ok := identity(info)
	if !ok {
		return Identity{}, ErrInvalid
	}
	return id, nil
}

func digest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}

func ValidReceipt(r Receipt) bool {
	return r.SchemaVersion == 1 && r.NativeVersion == Version && digest(r.GlobalGeneration) && digest(r.SourceSHA256) && filepath.IsAbs(r.SourceTarget) && filepath.Clean(r.SourceTarget) == r.SourceTarget && filepath.Base(r.SourceTarget) == "skills" && !strings.ContainsRune(r.SourceTarget, 0) && r.RetainedDirectory == RetainedDirectory && r.StockSHA256 == Digest && r.Profile.Device != 0 && r.Profile.Inode != 0 && r.Stock.Device != 0 && r.Stock.Inode != 0
}

// ReadReceipt checks intrinsic private metadata only; it never follows owner
// assets or inspects stock contents. Missing receipts are returned as nil.
func ReadReceipt(root *os.Root, name string) (*Receipt, error) {
	if name != PendingFile && name != CustodyFile {
		return nil, ErrInvalid
	}
	raw, err := readMetadata(root, name)
	if err != nil || raw == nil {
		return nil, err
	}
	var receipt Receipt
	profileID, err := ProfileIdentity(root)
	if err != nil || json.Unmarshal(raw, &receipt) != nil || !ValidReceipt(receipt) || receipt.Profile != profileID {
		return nil, ErrInvalid
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, ErrInvalid
	}
	return &receipt, nil
}

// ReadOuterGuard discriminates the stock-only schema-2 sentinel from the
// existing schema-1 global context journal, without inspecting native assets.
func ReadOuterGuard(root *os.Root) (*Receipt, error) {
	raw, err := readMetadata(root, ContextUpdateFile)
	if err != nil || raw == nil {
		return nil, err
	}
	var header struct{ SchemaVersion int }
	if json.Unmarshal(raw, &header) != nil {
		return nil, ErrInvalid
	}
	if header.SchemaVersion == 1 {
		return nil, nil
	}
	if header.SchemaVersion != 2 || len(raw) > 16384 {
		return nil, ErrInvalid
	}
	var guard OuterGuard
	profileID, err := ProfileIdentity(root)
	if err != nil || json.Unmarshal(raw, &guard) != nil || !ValidReceipt(guard.StockSkills) || guard.StockSkills.Profile != profileID {
		return nil, ErrInvalid
	}
	canonical, err := json.Marshal(guard)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, ErrInvalid
	}
	return &guard.StockSkills, nil
}

func readMetadata(root *os.Root, name string) ([]byte, error) {
	if _, err := ProfileIdentity(root); err != nil {
		return nil, err
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	limit := int64(16384)
	if name == ContextUpdateFile {
		limit = 2 << 20
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, ErrInvalid
	}
	if _, ok := identity(info); !ok {
		return nil, ErrInvalid
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) {
		return nil, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	current, pathErr := root.Lstat(name)
	if pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || current.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	if _, ok := identity(current); !ok {
		return nil, ErrInvalid
	}
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrInvalid
	}
	return raw, nil
}

// Validate uses rooted, non-following reads with fixed bounds, then checks the
// whole path/type/content digest. The native marker alone has no authority.
func Validate(root *os.Root, name string) (Identity, error) {
	if name != "skills" && name != RetainedDirectory {
		return Identity{}, ErrInvalid
	}
	if _, err := ProfileIdentity(root); err != nil {
		return Identity{}, err
	}
	before, err := root.Lstat(name)
	if err != nil || !before.IsDir() {
		return Identity{}, ErrInvalid
	}
	id, ok := identity(before)
	if !ok {
		return Identity{}, ErrInvalid
	}
	tree, err := root.OpenRoot(name)
	if err != nil {
		return Identity{}, ErrInvalid
	}
	defer func() { _ = tree.Close() }()
	after, err := tree.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return Identity{}, ErrInvalid
	}
	var records []string
	var total int64
	files := 0
	var walk func(string) error
	walk = func(path string) error {
		info, err := tree.Lstat(path)
		if err != nil {
			return ErrInvalid
		}
		if _, ok := identity(info); !ok {
			return ErrInvalid
		}
		if len(records) >= 72 {
			return ErrInvalid
		}
		if info.IsDir() {
			records = append(records, filepath.ToSlash(path)+"\x00D\x00-\n")
			dir, err := tree.Open(path)
			if err != nil {
				return ErrInvalid
			}
			actual, statErr := dir.Stat()
			if statErr == nil {
				if _, ok := identity(actual); !ok {
					statErr = ErrInvalid
				}
			}
			entries, readErr := dir.ReadDir(73)
			_ = dir.Close()
			current, pathErr := tree.Lstat(path)
			if pathErr == nil {
				if _, ok := identity(current); !ok {
					pathErr = ErrInvalid
				}
			}
			if statErr != nil || pathErr != nil || !os.SameFile(info, current) || !current.IsDir() || !os.SameFile(info, actual) || len(entries) > 72 || readErr != nil && !errors.Is(readErr, io.EOF) {
				return ErrInvalid
			}
			for _, entry := range entries {
				if err := walk(filepath.Join(path, entry.Name())); err != nil {
					return err
				}
			}
			return nil
		}
		if total+info.Size() > 307566 {
			return ErrInvalid
		}
		f, err := tree.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return ErrInvalid
		}
		actual, statErr := f.Stat()
		if statErr == nil {
			if _, ok := identity(actual); !ok {
				statErr = ErrInvalid
			}
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, 307567-total))
		_ = f.Close()
		current, pathErr := tree.Lstat(path)
		if pathErr == nil {
			if _, ok := identity(current); !ok {
				pathErr = ErrInvalid
			}
		}
		if statErr != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || !os.SameFile(info, actual) || readErr != nil || int64(len(raw)) != info.Size() {
			return ErrInvalid
		}
		total += int64(len(raw))
		files++
		sum := sha256.Sum256(raw)
		records = append(records, filepath.ToSlash(path)+"\x00F\x00"+hex.EncodeToString(sum[:])+"\n")
		return nil
	}
	if walk(".") != nil || len(records) != 72 || files != 49 || total != 307566 {
		return Identity{}, ErrInvalid
	}
	sort.Strings(records)
	sum := sha256.Sum256([]byte(strings.Join(records, "")))
	if hex.EncodeToString(sum[:]) != Digest {
		return Identity{}, ErrInvalid
	}
	current, err := root.Lstat(name)
	if err != nil || !current.IsDir() || !os.SameFile(before, current) {
		return Identity{}, ErrInvalid
	}
	if _, ok := identity(current); !ok {
		return Identity{}, ErrInvalid
	}
	return id, nil
}
