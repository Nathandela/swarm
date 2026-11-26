package workermail

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	bolt "go.etcd.io/bbolt"
)

const maxFileStoreBytes = 256 << 20

var fileBucket = []byte("note-files")
var manifestKey = []byte("manifest")

type FileSubmission struct {
	file schema.PublicationFile
	data []byte
}

type fileManifest struct {
	Files   []schema.PublicationFile
	Pins    map[string][]string `json:",omitempty"`
	Settled time.Time           `json:",omitzero"`
}

// CaptureFiles only opens regular files within the worker's recorded checkout.
func CaptureFiles(checkout string, paths []string) ([]FileSubmission, error) {
	if len(paths) == 0 || len(paths) > schema.MaxPublicationFiles || !filepath.IsAbs(checkout) {
		return nil, errors.New("worker files: select 1 to 10 files from your checkout")
	}
	root, err := openWorkerCheckout(checkout)
	if err != nil {
		return nil, errors.New("worker files: use a worker checkout with a real directory path")
	}
	defer func() { _ = root.Close() }()
	total := 0
	seen := make(map[string]bool)
	files := make([]FileSubmission, 0, len(paths))
	for _, path := range paths {
		relative, err := filepath.Rel(checkout, path)
		if err != nil || !filepath.IsAbs(path) || !filepath.IsLocal(relative) || len(path) > 4096 {
			return nil, errors.New("worker files: all files must be inside your checkout")
		}
		original := filepath.Base(relative)
		name := discordSafeName(original)
		folded := strings.ToLower(name)
		if original == "." || len(original) > 128 || !utf8.ValidString(original) || strings.TrimSpace(original) != original ||
			strings.ContainsRune(original, '\\') || strings.ContainsFunc(original, unicode.IsControl) {
			return nil, errors.New("worker files: use file names of at most 128 bytes, without surrounding spaces")
		}
		if seen[folded] {
			return nil, errors.New("worker files: two files would both be named " + name + " in Discord; rename one")
		}
		if secretFilePath(relative) || secretFilePath(name) {
			return nil, errors.New("worker files: " + original + " looks like a secret or hidden file and stays on the VM")
		}
		seen[folded] = true
		data, err := readWorkerFile(root, relative, schema.MaxPublicationFileBytes-total)
		if err != nil {
			return nil, err
		}
		contentType := workerFileType(name, data)
		if contentType == "" {
			return nil, errors.New("worker files: " + original + " was refused. Share PDF, PNG, JPEG, GIF, WebP, CSV, DOCX, XLSX, PPTX, ODT, ODS, ODP, ZIP, text (txt, md, json, log, yaml, tsv) or opaque .bin/.dat files whose contents match the extension")
		}
		total += len(data)
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		identity := sha256.Sum256([]byte(name + "\x00" + digest))
		files = append(files, FileSubmission{file: schema.PublicationFile{
			ID: hex.EncodeToString(identity[:]), Name: name, ContentType: contentType, Size: int64(len(data)), SHA256: digest,
		}, data: data})
	}
	return files, nil
}

// Keep each directory anchored while opening the next component so a renamed
// checkout or symlink cannot redirect a file read to another workspace.
func openWorkerCheckout(checkout string) (*os.Root, error) {
	absolute := filepath.Clean(checkout)
	if absolute == "/" {
		return nil, os.ErrInvalid
	}
	current, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	for component := range strings.SplitSeq(strings.TrimPrefix(absolute, "/"), "/") {
		before, err := current.Lstat(component)
		if err != nil || !before.IsDir() {
			_ = current.Close()
			return nil, os.ErrInvalid
		}
		next, err := current.OpenRoot(component)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			_ = next.Close()
			return nil, os.ErrInvalid
		}
		current = next
	}
	return current, nil
}

func readWorkerFile(root *os.Root, path string, remaining int) ([]byte, error) {
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("worker files: unable to open a regular file in the worker checkout")
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil {
		return nil, errors.New("worker files: could not inspect the file")
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return nil, errors.New("worker files: expected a regular, singly linked file owned by this worker's VM user")
	}
	if before.Size() <= 0 || before.Size() > int64(remaining) {
		return nil, errors.New("worker files: provide nonempty files totalling at most 8 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(remaining)+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || len(data) > remaining || int64(len(data)) != before.Size() ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("worker files: the file changed or could not be read; retry after writing it")
	}
	return data, nil
}

// Discord rewrites other characters in attachment names, which would break the
// receipt's name match. Send a name it keeps: runs of other characters become "_".
func discordSafeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_') {
			b.WriteRune(r)
		} else if !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Hidden paths and common credential names stay on the VM.
// ponytail: name-based guard; content scanning for secrets is out of scope.
func secretFilePath(path string) bool {
	for part := range strings.SplitSeq(strings.ToLower(path), "/") {
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_.") {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(path))
	for _, marker := range []string{"id_rsa", "id_ed25519", "id_ecdsa", "credential", "secret", ".env"} {
		if strings.Contains(base, marker) {
			return true
		}
	}
	return false
}

// Allowed formats cannot run code when opened. ZIP containers need the ZIP signature.
var zipFileTypes = map[string]string{
	".zip":  "application/zip",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",
}

var textFileExts = strings.Fields(".txt .md .json .log .yaml .yml .tsv")

// Native program signatures: PE, ELF, Mach-O (both byte orders) and universal binaries.
var programMagic = [][]byte{[]byte("MZ"), []byte("\x7fELF"), {0xfe, 0xed, 0xfa}, {0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe}}

func workerFileType(name string, data []byte) string {
	detected := http.DetectContentType(data)
	ext := strings.ToLower(filepath.Ext(name))
	if kind, ok := zipFileTypes[ext]; ok {
		if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
			return kind
		}
		return ""
	}
	if slices.Contains(textFileExts, ext) {
		if utf8.Valid(data) && bytes.IndexByte(data, 0) < 0 {
			return "text/plain; charset=utf-8"
		}
		return ""
	}
	switch ext {
	case ".png", ".gif", ".webp":
		if detected == "image/"+ext[1:] {
			return detected
		}
	case ".jpg", ".jpeg":
		if detected == "image/jpeg" {
			return detected
		}
	case ".bin", ".dat":
		if !slices.ContainsFunc(programMagic, func(m []byte) bool { return bytes.HasPrefix(data, m) }) {
			return "application/octet-stream"
		}
	case ".pdf":
		if detected == "application/pdf" {
			return detected
		}
	case ".csv":
		if !strings.HasPrefix(detected, "text/plain") || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			break
		}
		r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
		r.FieldsPerRecord, r.ReuseRecord = -1, true
		var hasRecords bool
		for {
			_, err := r.Read()
			if errors.Is(err, io.EOF) {
				if hasRecords {
					return "text/csv"
				}
				break
			}
			if err != nil {
				break
			}
			hasRecords = true
		}
	}
	return ""
}

func noteFiles(tx *bolt.Tx, key []byte) *bolt.Bucket {
	root := tx.Bucket(fileBucket)
	if root == nil {
		return nil
	}
	return root.Bucket(key)
}

func readFileManifest(bucket *bolt.Bucket) (m fileManifest, err error) {
	if bucket != nil {
		err = json.Unmarshal(bucket.Get(manifestKey), &m)
	}
	return
}

func writeFileManifest(b *bolt.Bucket, m fileManifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return b.Put(manifestKey, raw)
}

func writeNewNoteFiles(tx *bolt.Tx, key []byte, files []FileSubmission) error {
	if len(files) == 0 {
		return nil
	}
	root, err := tx.CreateBucketIfNotExists(fileBucket)
	if err != nil {
		return err
	}
	total := 0
	for _, file := range files {
		total += len(file.data)
	}
	if total > schema.MaxPublicationFileBytes || root.Stats().LeafInuse+total > maxFileStoreBytes {
		return errors.New("worker files: file storage is full; retry after pending handoffs are delivered or expire")
	}
	bucket, err := root.CreateBucket(key)
	if err != nil {
		return err
	}
	var manifest fileManifest
	for _, file := range files {
		if err := bucket.Put([]byte(file.file.ID), file.data); err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, file.file)
	}
	return writeFileManifest(bucket, manifest)
}

func readNote(tx *bolt.Tx, key, data []byte) (n Note, err error) {
	if err = json.Unmarshal(data, &n); err != nil {
		return n, err
	}
	manifest, err := readFileManifest(noteFiles(tx, key))
	n.Files = manifest.Files
	return n, err
}

func scopedFileManifest(tx *bolt.Tx, b Binding, noteID string, allowingSuperseded bool) (*bolt.Bucket, fileManifest, error) {
	var manifest fileManifest
	key := noteKey(b, noteID)
	bucket := noteFiles(tx, key)
	notes := tx.Bucket([]byte("notes"))
	if bucket == nil || notes == nil || b.Parent == "" || notes.Get(key) == nil {
		return nil, manifest, ErrAbsent
	}
	var n Note
	if err := json.Unmarshal(notes.Get(key), &n); err != nil {
		return nil, manifest, err
	}
	if n.ID != noteID || n.Discussion != b.Discussion || n.Parent != b.Parent || n.Session != b.Session ||
		n.Instance != b.Instance || n.Attempt != b.Attempt || !validFileNoteState(n.State, allowingSuperseded) {
		return nil, manifest, ErrAbsent
	}
	manifest, err := readFileManifest(bucket)
	return bucket, manifest, err
}

func validFileNoteState(state string, allowingSuperseded bool) bool {
	return state == "queued" || state == "ingested" || allowingSuperseded && state == "superseded"
}

// ReadFile verifies immutable bytes against a file in the exact accepted mailbox note.
func ReadFile(dir string, b Binding, noteID, fileID string) (data []byte, err error) {
	if !validFileID(fileID) {
		return nil, ErrAbsent
	}
	err = transact(dir, false, func(tx *bolt.Tx) error {
		bucket, manifest, err := scopedFileManifest(tx, b, noteID, false)
		if err != nil {
			return err
		}
		for _, file := range manifest.Files {
			if file.ID != fileID {
				continue
			}
			raw := bucket.Get([]byte(fileID))
			if len(raw) == 0 || len(raw) > schema.MaxPublicationFileBytes || int64(len(raw)) != file.Size {
				return errors.New("worker files: staged bytes are missing or incomplete")
			}
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != file.SHA256 {
				return errors.New("worker files: staged bytes failed verification")
			}
			data = bytes.Clone(raw)
			return nil
		}
		return ErrAbsent
	})
	if data == nil && err == nil {
		err = ErrAbsent
	}
	return
}

func validFileID(id string) bool {
	if len(id) != 64 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// PinFiles protects the exact selected files while the publication is pending.
func PinFiles(dir string, b Binding, noteID, publicationID string, fileIDs []string) error {
	if publicationID == "" || len(publicationID) > 256 || len(fileIDs) == 0 || len(fileIDs) > schema.MaxPublicationFiles {
		return errors.New("worker files: invalid publication pin")
	}
	return transact(dir, true, func(tx *bolt.Tx) error {
		bucket, manifest, err := scopedFileManifest(tx, b, noteID, false)
		if err != nil {
			return err
		}
		selected := make(map[string]bool)
		for _, id := range fileIDs {
			if selected[id] || !slices.ContainsFunc(manifest.Files, func(f schema.PublicationFile) bool { return f.ID == id }) {
				return ErrConflict
			}
			selected[id] = true
		}
		if existing, ok := manifest.Pins[publicationID]; ok {
			if !slices.Equal(existing, fileIDs) {
				return ErrConflict
			}
			return nil
		}
		if len(manifest.Pins) >= 128 {
			return errors.New("worker files: too many pending publications for this note")
		}
		if manifest.Pins == nil {
			manifest.Pins = make(map[string][]string)
		}
		manifest.Pins[publicationID] = fileIDs
		return writeFileManifest(bucket, manifest)
	})
}

// ReleaseFiles removes one publication pin after the downstream store has
// durably copied the selected files or the published operation is known absent.
func ReleaseFiles(dir string, b Binding, noteID, publicationID string) error {
	return transact(dir, true, func(tx *bolt.Tx) error {
		bucket, manifest, err := scopedFileManifest(tx, b, noteID, true)
		if errors.Is(err, ErrAbsent) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := manifest.Pins[publicationID]; !ok {
			return nil
		}
		delete(manifest.Pins, publicationID)
		return writeFileManifest(bucket, manifest)
	})
}

// SettleFiles starts retention and keeps only the exact publications that still
// need mailbox bytes. An unpublished pin that never acquired a durable voice
// publication cannot survive the attested end of its admission.
func SettleFiles(dir string, b Binding, noteID string, keepPublicationIDs []string) error {
	return transact(dir, true, func(tx *bolt.Tx) error {
		bucket, manifest, err := scopedFileManifest(tx, b, noteID, true)
		if err != nil {
			return err
		}
		changed := manifest.Settled.IsZero()
		if changed {
			manifest.Settled = time.Now().UTC()
		}
		for id := range manifest.Pins {
			if !slices.Contains(keepPublicationIDs, id) {
				delete(manifest.Pins, id)
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return writeFileManifest(bucket, manifest)
	})
}
