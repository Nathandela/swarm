package upgrade

import (
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/codexstock"
)

// Stock custody can precede immutable projection publication and outlive the
// registry reference. Scan every bounded private profile, including candidates.
// Compatibility reads intrinsic metadata only, never credentials or owner assets.
func accountStockCustodyGuard(root *os.Root, card CompatManifest) error {
	before, err := root.Lstat("profiles")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !privateAccountInfo(before, true) {
		return errors.New("account stock profile inventory cannot be verified")
	}
	profiles, entries, err := openAccountInventory(root, "profiles")
	if err != nil || profiles == nil {
		return errors.New("account stock profile inventory cannot be verified")
	}
	defer func() { _ = profiles.Close() }()
	actual, err := profiles.Stat(".")
	if err != nil || !os.SameFile(before, actual) || !privateAccountInfo(actual, true) {
		return errors.New("account stock profile inventory cannot be verified")
	}
	for _, entry := range entries {
		name := entry.Name()
		if stem, ok := strings.CutSuffix(name, ".lock"); ok && accountProfileName(stem) {
			// Claude Code's legacy proper-lockfile directory, possibly left by a crash (ADR-030).
			info, err := profiles.Lstat(name)
			if err != nil || !info.IsDir() {
				return errors.New("account stock profile inventory contains an unsafe entry")
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
				return errors.New("account stock profile inventory contains an unsafe entry")
			}
			continue
		}
		if !accountProfileName(name) {
			return errors.New("account stock profile inventory contains an unknown entry")
		}
		before, err := profiles.Lstat(name)
		if err != nil || !privateAccountInfo(before, true) {
			return errors.New("account stock profile inventory contains an unsafe entry")
		}
		profile, err := profiles.OpenRoot(name)
		if err != nil {
			return errors.New("account stock profile custody cannot be verified")
		}
		err = checkAccountStockCustody(profile, before, card)
		_ = profile.Close()
		if err != nil {
			return err
		}
		current, err := profiles.Lstat(name)
		if err != nil || !os.SameFile(before, current) || !privateAccountInfo(current, true) {
			return errors.New("account stock profile custody cannot be verified")
		}
	}
	current, err := root.Lstat("profiles")
	if err != nil || !os.SameFile(actual, current) || !privateAccountInfo(current, true) {
		return errors.New("account stock profile inventory cannot be verified")
	}
	return nil
}

func accountProfileName(name string) bool {
	id, err := hex.DecodeString(name)
	return err == nil && len(id) == 16 && name == strings.ToLower(name)
}

func checkAccountStockCustody(profile *os.Root, before os.FileInfo, card CompatManifest) error {
	actual, err := profile.Stat(".")
	if err != nil || !os.SameFile(before, actual) || !privateAccountInfo(actual, true) {
		return errors.New("account stock profile custody cannot be verified")
	}
	id, err := codexstock.ProfileIdentity(profile)
	if err != nil {
		return errors.New("account stock profile custody cannot be verified")
	}
	outer, err := codexstock.ReadOuterGuard(profile)
	if err != nil {
		return errors.New("account stock installation guard cannot be verified")
	}
	pending, err := codexstock.ReadReceipt(profile, codexstock.PendingFile)
	if err != nil {
		return errors.New("account stock custody cannot be verified")
	}
	completed, err := codexstock.ReadReceipt(profile, codexstock.CustodyFile)
	if err != nil {
		return errors.New("account stock custody cannot be verified")
	}
	var receipt *codexstock.Receipt
	for _, candidate := range []*codexstock.Receipt{outer, pending, completed} {
		if candidate == nil {
			continue
		}
		if candidate.Profile != id || receipt != nil && *candidate != *receipt {
			return errors.New("account stock custody cannot be verified")
		}
		receipt = candidate
	}
	if pending != nil && outer == nil {
		// A real global refresh journal can provide the old-reader fence instead
		// of a dedicated stock sentinel. ReadOuterGuard already classified it.
		guard, err := profile.Lstat(codexstock.ContextUpdateFile)
		if err != nil || !privateAccountInfo(guard, false) {
			return errors.New("account stock installation guard cannot be verified")
		}
	}
	retained, retainedErr := profile.Lstat(codexstock.RetainedDirectory)
	if !errors.Is(retainedErr, os.ErrNotExist) && (retainedErr != nil || receipt == nil) {
		return errors.New("account retained stock custody cannot be verified")
	}
	if completed != nil && retainedErr != nil {
		return errors.New("account retained stock custody cannot be verified")
	}
	if receipt != nil {
		node := retained
		if errors.Is(retainedErr, os.ErrNotExist) {
			// Rename is atomic: before it, the original tree has the recorded
			// inode; afterward the retained tree does. Both absent is unknown.
			node, err = profile.Lstat("skills")
			if err != nil {
				return errors.New("account stock custody cannot be verified")
			}
		}
		stat, ok := node.Sys().(*syscall.Stat_t)
		if !node.IsDir() || node.Mode().Perm()&0002 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
			return errors.New("account retained stock custody cannot be verified")
		}
		stock := codexstock.Identity{Device: uint64(stat.Dev), Inode: stat.Ino}
		if receipt.Stock != stock {
			return errors.New("account retained stock custody cannot be verified")
		}
	}
	if (receipt != nil || retainedErr == nil) && card.AccountConfig < 4 {
		return errors.New("the target build cannot preserve native Codex stock custody")
	}
	return nil
}
