package discord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

const teammateFilePayloadBucket = "teammate_file_payloads"
const maxBufferedPublicationFiles = 64 << 20

var errPublicationFilesRequired = errors.New("Discord publication requires its staged file bytes")
var errDiscordAttachFiles = errors.New("Discord is missing Attach Files permission in this discussion")
var errPublicationFilesChanged = errors.New("Discord publication files are missing or changed")

// PublicationContentHash keeps the existing text-only receipt hash. Extended
// rows bind their complete content and cannot be sent as captions by old code.
func PublicationContentHash(body string, files []schema.PublicationFile, recipient string) string {
	return schema.PublicationContentHash(body, files, recipient)
}

func (p TeammatePublication) chunks() []string {
	body := p.Body
	if p.RecipientID != "" {
		prefix := "<@" + p.RecipientID + ">"
		if body != "" {
			prefix += " "
		}
		body = prefix + body
	}
	parts := splitUTF16(body, 1900)
	if len(parts) == 0 && len(p.Files) > 0 {
		return []string{""}
	}
	return parts
}

func publicationFileBytes(files []schema.PublicationFile) (int, error) {
	if len(files) > schema.MaxPublicationFiles {
		return 0, ErrCapacity
	}
	names, ids := make(map[string]bool), make(map[string]bool)
	total := 0
	for _, f := range files {
		hash, err := hex.DecodeString(f.SHA256)
		if err != nil || len(hash) != sha256.Size || strings.ToLower(f.SHA256) != f.SHA256 ||
			f.ID == "" || len(f.ID) > 128 || strings.IndexFunc(f.ID, func(r rune) bool { return r < 33 || r > 126 }) >= 0 ||
			f.Name == "" || len(f.Name) > 160 || !utf8.ValidString(f.Name) || strings.ContainsAny(f.Name, "/\\") ||
			f.Name == "." || f.Name == ".." || strings.TrimSpace(f.Name) != f.Name || strings.IndexFunc(f.Name, unicode.IsControl) >= 0 ||
			names[strings.ToLower(f.Name)] || ids[f.ID] || f.Size <= 0 || !publicationFileType(f.ContentType) {
			return 0, ErrInvalid
		}
		if f.Size > schema.MaxPublicationFileBytes || int64(total)+f.Size > schema.MaxPublicationFileBytes {
			return 0, ErrCapacity
		}
		total += int(f.Size)
		names[strings.ToLower(f.Name)], ids[f.ID] = true, true
	}
	return total, nil
}

func publicationFileType(value string) bool {
	switch value {
	case "image/png", "image/jpeg", "text/csv", "application/pdf":
		return true
	default:
		return false
	}
}

func matchesPublicationFile(f schema.PublicationFile, raw []byte) bool {
	hash := sha256.Sum256(raw)
	return int64(len(raw)) == f.Size && hex.EncodeToString(hash[:]) == f.SHA256
}

func makeFilePayload(files []schema.PublicationFile, data [][]byte, size int) ([]byte, error) {
	if len(files) != len(data) {
		return nil, errPublicationFilesChanged
	}
	var payload []byte
	if size != 0 {
		payload = make([]byte, 0, size)
	}
	for i, f := range files {
		if !matchesPublicationFile(f, data[i]) {
			return nil, errPublicationFilesChanged
		}
		payload = append(payload, data[i]...)
	}
	return payload, nil
}

func storeFilePayload(tx *bolt.Tx, key, payload []byte) error {
	files := tx.Bucket([]byte(teammateFilePayloadBucket))
	used := 0
	if err := files.ForEach(func(_, value []byte) error {
		used += len(value)
		return nil
	}); err != nil {
		return err
	}
	if used+len(payload) > maxBufferedPublicationFiles {
		return ErrCapacity
	}
	return files.Put(key, payload)
}

func (s *Store) readFilePayload(install string, out teammateOutput) ([][]byte, error) {
	files := out.Publication.Files
	size, err := publicationFileBytes(files)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = s.db.View(func(tx *bolt.Tx) error {
		key := teammateOutputKey(install, out.DiscussionID, out.Publication.ID)
		found := tx.Bucket([]byte(teammateFilePayloadBucket)).Get(key)
		// A concurrent Gateway receipt can release bytes before this sender claims
		// the chunk. Let the claim transaction verify receipt state in that case.
		if found != nil && len(found) != size {
			return errPublicationFilesChanged
		}
		raw = bytes.Clone(found)
		return nil
	})
	if err != nil || raw == nil {
		return nil, err
	}
	data := make([][]byte, 0, len(files))
	for _, f := range files {
		part := raw[:int(f.Size)]
		if !matchesPublicationFile(f, part) {
			return nil, errPublicationFilesChanged
		}
		data = append(data, part)
		raw = raw[int(f.Size):]
	}
	return data, nil
}

func expectedChunkFiles(out teammateOutput, n int) []schema.PublicationFile {
	if n == 0 {
		return out.Publication.Files
	}
	return nil
}

// Discord records uploaded names, sizes, and server attachment IDs, but it
// does not return digests. The exact nonce binds these to our pinned request.
func publicationAttachmentIDs(expected []schema.PublicationFile, rows []*discordgo.MessageAttachment) ([]string, bool) {
	if len(rows) != len(expected) {
		return nil, false
	}
	var ids []string
	for i, row := range rows {
		if row == nil || !snowflake(row.ID) || row.Filename != expected[i].Name || int64(row.Size) != expected[i].Size ||
			containsID(ids, row.ID) {
			return nil, false
		}
		ids = append(ids, row.ID)
	}
	return ids, true
}

func (t *teammateConnector) queuePublication(ctx context.Context, d TeammateDiscussion, p TeammatePublication) error {
	store := t.service.Store
	err := store.teammateQueueOutput(d, p, t.service.Now())
	if errors.Is(err, errPublicationFilesRequired) {
		reader, ok := t.manager.(interface {
			ReadPublicationFiles(context.Context, TeammateDiscussion, TeammatePublication) ([][]byte, error)
		})
		if !ok {
			return ErrNotAuthorized
		}
		files, readErr := reader.ReadPublicationFiles(ctx, d, p)
		if readErr != nil {
			return readErr
		}
		if len(files) != len(p.Files) {
			return errPublicationFilesChanged
		}
		err = store.teammateQueueOutput(d, p, t.service.Now(), files...)
	}
	if err != nil || len(p.Files) == 0 {
		return err
	}
	if release, ok := t.manager.(interface {
		ReleasePublicationFiles(context.Context, TeammateDiscussion, TeammatePublication) error
	}); ok {
		return release.ReleasePublicationFiles(ctx, d, p)
	}
	return nil
}
