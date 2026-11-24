package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PublicationFile describes immutable bytes staged for one discussion.
type PublicationFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

const (
	MaxPublicationFiles     = 10
	MaxPublicationFileBytes = 8 << 20
)

// PublicationContentHash preserves the text-only receipt format.
func PublicationContentHash(body string, files []PublicationFile, recipient string) string {
	if len(files) == 0 {
		files = nil
	}
	var content any = body
	if len(files) != 0 || recipient != "" {
		content = []any{body, files, recipient}
	}
	raw, _ := json.Marshal(content)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
