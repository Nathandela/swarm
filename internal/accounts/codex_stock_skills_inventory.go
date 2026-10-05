package accounts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/codexstock"
)

// Completed stock custody remains independent of credential erasure. This
// validates retained native assets, and never follows or removes owner aliases.
func validCodexStockInventory(profile *os.Root) bool {
	receipt, err := codexstock.ReadReceipt(profile, codexstock.CustodyFile)
	if err != nil {
		return false
	}
	_, retainedErr := profile.Lstat(codexstock.RetainedDirectory)
	if receipt == nil {
		return errors.Is(retainedErr, os.ErrNotExist)
	}
	id, err := codexstock.Validate(profile, codexstock.RetainedDirectory)
	if err != nil || id != receipt.Stock {
		return false
	}
	raw, err := readPrivate(profile, ".swarm-codex-context.json", 2<<20)
	if err != nil {
		return false
	}
	var context struct {
		GlobalGeneration string
		SourceProfile    struct{ Canonical string }
	}
	if json.Unmarshal(raw, &context) != nil || len(context.GlobalGeneration) != 64 || !filepath.IsAbs(context.SourceProfile.Canonical) || filepath.Clean(context.SourceProfile.Canonical) != context.SourceProfile.Canonical || receipt.SourceTarget != filepath.Join(context.SourceProfile.Canonical, "skills") {
		return false
	}
	prefix := []byte(`{"GlobalGeneration":"` + context.GlobalGeneration + `",`)
	if !bytes.HasPrefix(raw, prefix) {
		return false
	}
	empty := append([]byte(`{"GlobalGeneration":"",`), raw[len(prefix):]...)
	sum := sha256.Sum256(empty)
	return hex.EncodeToString(sum[:]) == context.GlobalGeneration
}
