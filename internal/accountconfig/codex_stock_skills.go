package accountconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/codexstock"
)

func openCodexStockProfile(profile string) (*os.Root, error) {
	if safePath(profile, true) != nil || privateDir(profile) != nil {
		return nil, Conflict("unsafe-native-stock-custody")
	}
	root, err := os.OpenRoot(profile)
	if err != nil {
		return nil, Conflict("unsafe-native-stock-custody")
	}
	return root, nil
}

// Schema 2 is deliberately rejected by the older context-update reader, while
// its existing presence guard blocks both launch and credential erasure.
type codexStockOuterGuard = codexstock.OuterGuard

func readCodexStockOuterGuard(profile string) (*codexstock.Receipt, error) {
	root, err := openCodexStockProfile(profile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	guard, err := codexstock.ReadOuterGuard(root)
	if err != nil {
		return nil, Conflict("invalid-native-stock-custody")
	}
	return guard, nil
}

func codexStockRecordMatches(c CodexContext, r codexstock.Receipt, profileID codexstock.Identity, pending bool) bool {
	return codexstock.ValidReceipt(r) && r.Profile == profileID && r.SourceTarget == c.Assets[0].Target && (!pending || r.GlobalGeneration == c.GlobalGeneration && r.SourceSHA256 == c.Assets[0].SHA256 && c.Assets[0].SHA256 != "absent")
}

// Planning is read-only and precedes validation of all other destination
// collisions. Only the caller's stopped-writer lease may apply this plan.
func planCodexStockSkills(c CodexContext, profile string) (*codexstock.Receipt, bool, error) {
	root, err := openCodexStockProfile(profile)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = root.Close() }()
	profileID, err := codexstock.ProfileIdentity(root)
	if err != nil {
		return nil, false, Conflict("unsafe-native-stock-custody")
	}
	pending, err := codexstock.ReadReceipt(root, codexstock.PendingFile)
	if err != nil {
		return nil, false, Conflict("invalid-native-stock-custody")
	}
	outer, err := readCodexStockOuterGuard(profile)
	if err != nil {
		return nil, false, err
	}
	if outer != nil {
		if pending != nil && *pending != *outer {
			return nil, false, Conflict("invalid-native-stock-custody")
		}
		pending = outer
	}
	if pending != nil && outer == nil {
		update, err := readCodexContextUpdate(profile)
		if err != nil || update == nil || update.Next.GlobalGeneration != pending.GlobalGeneration || update.Next.Assets[0].Target != pending.SourceTarget || update.Next.Assets[0].SHA256 != pending.SourceSHA256 {
			return nil, false, Conflict("native-stock-adoption-outer-guard-missing")
		}
	}
	completed, err := codexstock.ReadReceipt(root, codexstock.CustodyFile)
	if err != nil {
		return nil, false, Conflict("invalid-native-stock-custody")
	}
	retained, retainedErr := root.Lstat(codexstock.RetainedDirectory)
	if completed != nil {
		if !codexStockRecordMatches(c, *completed, profileID, false) {
			return nil, false, Conflict("invalid-native-stock-custody")
		}
		id, err := codexstock.Validate(root, codexstock.RetainedDirectory)
		if err != nil || id != completed.Stock {
			return nil, false, Conflict("invalid-native-stock-custody")
		}
	} else if pending == nil && !errors.Is(retainedErr, os.ErrNotExist) {
		return nil, false, Conflict("unproven-retained-native-stock")
	}
	if pending != nil {
		if !codexStockRecordMatches(c, *pending, profileID, true) || completed != nil && *completed != *pending {
			return nil, false, Conflict("native-stock-adoption-source-changed")
		}
		original, originalErr := root.Lstat("skills")
		if errors.Is(retainedErr, os.ErrNotExist) {
			id, err := codexstock.Validate(root, "skills")
			if err != nil || id != pending.Stock {
				return nil, false, Conflict("invalid-native-stock-custody")
			}
		} else {
			id, err := codexstock.Validate(root, codexstock.RetainedDirectory)
			if retained == nil || err != nil || id != pending.Stock {
				return nil, false, Conflict("invalid-native-stock-custody")
			}
			if !errors.Is(originalErr, os.ErrNotExist) {
				target, err := root.Readlink("skills")
				if original == nil || err != nil || target != pending.SourceTarget {
					return nil, false, Conflict("invalid-native-stock-custody")
				}
			}
		}
		return pending, true, nil
	}
	info, err := root.Lstat("skills")
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, Conflict("unsafe-native-stock-custody")
	}
	if !info.IsDir() {
		return nil, false, nil
	} // Exact aliases are checked by the ordinary context guard.
	id, err := codexstock.Validate(root, "skills")
	if err != nil {
		return nil, false, Conflict("candidate-customization-not-context-alias")
	}
	if c.Assets[0].SHA256 == "absent" {
		return nil, false, nil
	}
	if completed != nil {
		return nil, false, Conflict("duplicate-retained-native-stock")
	}
	record := codexstock.Receipt{SchemaVersion: 1, NativeVersion: codexstock.Version, GlobalGeneration: c.GlobalGeneration, SourceTarget: c.Assets[0].Target, SourceSHA256: c.Assets[0].SHA256, RetainedDirectory: codexstock.RetainedDirectory, StockSHA256: codexstock.Digest, Profile: profileID, Stock: id}
	return &record, false, nil
}

func codexAbsentSourceStock(c CodexContext, profile string) bool {
	if c.Assets[0].SHA256 != "absent" {
		return false
	}
	root, err := openCodexStockProfile(profile)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	_, err = codexstock.Validate(root, "skills")
	return err == nil
}

func applyCodexStockSkills(record codexstock.Receipt, profile string) error {
	// Never replace a real global-refresh journal. It already provides the
	// older-reader fence; initial adoption needs its own recognized outer guard.
	if _, err := os.Lstat(filepath.Join(profile, CodexContextUpdateMarker)); errors.Is(err, os.ErrNotExist) {
		raw, _ := json.Marshal(codexStockOuterGuard{SchemaVersion: 2, StockSkills: record})
		if err := writeClaudeContextFile(profile, CodexContextUpdateMarker, raw); err != nil {
			return err
		}
		if err := syncCodexContextDirectory(profile); err != nil {
			return err
		}
	}
	root, err := openCodexStockProfile(profile)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	pending, err := codexstock.ReadReceipt(root, codexstock.PendingFile)
	if err != nil || pending != nil && *pending != record {
		return Conflict("invalid-native-stock-custody")
	}
	if pending == nil {
		raw, _ := json.Marshal(record)
		if err := writeClaudeContextFile(profile, codexstock.PendingFile, raw); err != nil {
			return err
		}
		if err := syncCodexContextDirectory(profile); err != nil {
			return err
		}
	}
	if _, err := root.Lstat(codexstock.RetainedDirectory); errors.Is(err, os.ErrNotExist) {
		id, err := codexstock.Validate(root, "skills")
		if err != nil || id != record.Stock {
			return Conflict("invalid-native-stock-custody")
		}
		if err := root.Rename("skills", codexstock.RetainedDirectory); err != nil {
			return Conflict("native-stock-adoption-install")
		}
		if err := syncCodexContextDirectory(profile); err != nil {
			return err
		}
	}
	if _, err := root.Lstat("skills"); errors.Is(err, os.ErrNotExist) {
		if err := root.Symlink(record.SourceTarget, "skills"); err != nil {
			return Conflict("native-stock-adoption-install")
		}
		if err := syncCodexContextDirectory(profile); err != nil {
			return err
		}
	}
	return nil
}

func completeCodexStockSkills(record codexstock.Receipt, profile string) error {
	root, err := openCodexStockProfile(profile)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	id, err := codexstock.Validate(root, codexstock.RetainedDirectory)
	target, targetErr := root.Readlink("skills")
	if err != nil || id != record.Stock || targetErr != nil || target != record.SourceTarget {
		return Conflict("invalid-native-stock-custody")
	}
	raw, _ := json.Marshal(record)
	if err := writeClaudeContextFile(profile, codexstock.CustodyFile, raw); err != nil {
		return err
	}
	if err := syncCodexContextDirectory(profile); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(profile, codexstock.PendingFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Conflict("native-stock-adoption-install")
	}
	if err := syncCodexContextDirectory(profile); err != nil {
		return err
	}
	outer, err := readCodexStockOuterGuard(profile)
	if err != nil {
		return err
	}
	if outer != nil {
		if *outer != record {
			return Conflict("invalid-native-stock-custody")
		}
		return removeCodexContextUpdate(profile)
	}
	return nil
}

func revalidateCodexStockSkills(c CodexContext, profile string) error {
	root, err := openCodexStockProfile(profile)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(codexstock.PendingFile); !errors.Is(err, os.ErrNotExist) {
		return Conflict("native-stock-adoption-pending")
	}
	outer, err := readCodexStockOuterGuard(profile)
	if err != nil || outer != nil {
		return Conflict("native-stock-adoption-pending")
	}
	_, _, err = planCodexStockSkills(c, profile)
	return err
}
