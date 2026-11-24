package discord

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

var errAttachmentURLExpired = errors.New("attachment URL expired")

var errAttachmentRetry = errors.New("attachment download is temporarily unavailable. The original message is queued for retry")

const (
	attachmentMaxFile     = 16 << 20
	attachmentMaxMessage  = 32 << 20
	attachmentMaxStore    = 256 << 20
	attachmentMaxBundles  = 512
	attachmentRetryWindow = 2 * time.Minute
)

type attachmentFile struct {
	ID          string
	Name        string
	ContentType string
	Size        int
	File        string `json:",omitempty"`
	Unavailable string `json:",omitempty"`
	MessageID   string `json:",omitempty"`
	Forwarded   bool   `json:",omitempty"`
}

type attachmentSource struct {
	route   Route
	message *discordgo.Message
}

type inboundAttachment struct {
	file      *discordgo.MessageAttachment
	index     int
	forwarded bool
}

func (source attachmentSource) attachments() (out []inboundAttachment) {
	for i, file := range source.message.Attachments {
		if file != nil {
			out = append(out, inboundAttachment{file: file, index: i})
		}
	}
	for _, snapshot := range teammateSnapshots(source.message) {
		for _, file := range snapshot.Attachments {
			if file != nil {
				out = append(out, inboundAttachment{file: file, forwarded: true})
			}
		}
	}
	return out
}

// Capture the current verified message and its explicit same-channel reply target.
// Forward sources are always the visible copies, never the original channels.
func (t *teammateConnector) captureAttachments(ctx context.Context, a *TeammateAdmission, m *discordgo.Message) error {
	cfg := t.service.Config
	a.ForwardedContext, a.AttachmentNotice = nil, ""
	if !t.admissibleHuman(m) || a.Discussion.InstallationID != cfg.InstallationID || a.Discussion.ID == "" ||
		a.Discussion.Route.GuildID != a.Route.GuildID || a.Route.ChannelID != a.Discussion.Route.ChannelID && a.Route != a.Discussion.Starter ||
		m.Author.ID != a.Author.ID || m.ID != a.Route.MessageID || m.ChannelID != a.Route.ChannelID || m.GuildID != a.Route.GuildID ||
		a.Revision != teammateRevision(m) ||
		cfg.Mode == "personal" && (a.Discussion.Route != (Route{ChannelID: cfg.DMChannelID}) || a.Author.ID != cfg.OwnerID) {
		return ErrConflict
	}
	sources := []attachmentSource{{route: a.Route, message: m}}
	if reply := teammateReply(m); reply != "" && snowflakeLess(reply, m.ID) {
		visible := true
		if cfg.Mode == "personal" {
			floor, err := t.service.Store.personalHistoryConsent(cfg, a.Discussion, m.ID)
			if err != nil {
				return err
			}
			visible = floor != "" && !snowflakeLess(reply, floor)
		}
		if visible {
			route := a.Route
			route.MessageID = reply
			earlier, err := t.client.teammateMessage(ctx, route)
			if err != nil {
				a.AttachmentNotice = "I couldn't read the earlier message. If it contained a file you want checked, reattach it here."
			} else {
				sources = append(sources, attachmentSource{route: route, message: earlier})
			}
		}
	}
	for _, source := range sources {
		for _, snapshot := range teammateSnapshots(source.message) {
			if snapshot.Content != "" {
				a.ForwardedContext = append(a.ForwardedContext, TeammateForwardedContext{MessageID: source.route.MessageID, QuotedText: clipUTF16(snapshot.Content, 320)})
			}
		}
	}
	var err error
	a.AttachmentManifest, err = t.snapshotAttachments(ctx, *a, sources)
	if errors.Is(err, errAttachmentRetry) {
		// The unbound connector notice lane also operates before the first manager starts.
		notice := TeammatePublication{ID: "attachment_retry_" + digest([]string{a.Discussion.ID, a.Route.MessageID})[:32], Body: "Discord could not supply an attachment for message " + a.Route.MessageID + ". I kept your request and will retry for up to two minutes. If the download still fails, reattach the file in a new message."}
		err = errors.Join(err, t.service.Store.teammateQueue(a.Discussion.InstallationID, "", a.Route, notice, t.service.Now(), true))
	}
	return err
}

func (t *teammateConnector) snapshotAttachments(ctx context.Context, a TeammateAdmission, sources []attachmentSource) (string, error) {
	count := 0
	for _, source := range sources {
		count += len(source.attachments())
	}
	if count == 0 {
		return "", nil
	}
	if t.service.Config.EffectiveProjectAccess() != ProjectAccessVM {
		return "File access unavailable in enrolled-project mode.", nil
	}
	if count > 10 {
		return "Too many attachments; files unavailable (maximum 10).", nil
	}
	if !t.client.attachmentMu.TryLock() {
		return "", errAttachmentRetry
	}
	defer t.client.attachmentMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	root, err := filepath.Abs(filepath.Join(filepath.Dir(t.service.Store.db.Path()), "attachment-files"))
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", errors.New("attachment snapshot directory unavailable")
	}
	identity := []string{a.Discussion.InstallationID, a.Discussion.ID, a.Route.ChannelID, a.Route.MessageID, a.Revision}
	if len(sources) > 1 {
		identity = append(identity, sources[1].route.MessageID, teammateRevision(sources[1].message))
	}
	key := digest(identity)
	final := filepath.Join(root, key)
	manifest := filepath.Join(final, "files.json")
	if info, err := os.Lstat(final); err == nil {
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", ErrConflict
		}
		if info, err = os.Lstat(manifest); err != nil || !info.Mode().IsRegular() {
			return "", ErrConflict
		}
		if err = writeAttachmentManifest(filepath.Join(final, ".discussion"), []byte(a.Discussion.ID)); err != nil {
			return "", err
		}
		return manifest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(root, ".pending-"+key)
	pending, err := t.service.Store.teammatePending(a.Discussion.InstallationID)
	if err != nil {
		return "", err
	}
	pinned := map[string]bool{}
	for _, item := range pending {
		if item.Admission != nil && filepath.IsAbs(item.Admission.AttachmentManifest) {
			pinned[filepath.Dir(item.Admission.AttachmentManifest)] = true
		}
	}
	active, err := t.service.Store.teammateActiveBindings(a.Discussion.InstallationID)
	if err != nil {
		return "", err
	}
	activeDiscussions := map[string]bool{a.Discussion.ID: true}
	for _, binding := range active {
		activeDiscussions[binding.ID] = true
	}
	var used int64
	bundles := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		key := strings.TrimPrefix(entry.Name(), ".pending-")
		_, hashErr := hex.DecodeString(key)
		if len(key) != 64 || hashErr != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		dir := filepath.Join(root, entry.Name())
		// Completed files in this revision's staging directory resume after restart.
		// They count once via total below.
		if dir == tmp {
			continue
		}
		leased := entry.Name() == key && activeDiscussions[attachmentDiscussion(dir)]
		if !pinned[dir] && !leased && t.service.Now().Sub(info.ModTime()) > 7*24*time.Hour {
			if err = os.RemoveAll(dir); err != nil {
				return "", err
			}
			continue
		}
		bundles++
		files, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		for _, f := range files {
			i, err := f.Info()
			if err != nil {
				return "", err
			}
			used += i.Size()
		}
	}
	if bundles >= attachmentMaxBundles || used+65536 > attachmentMaxStore {
		return "Attachment storage capacity reached; files unavailable.", nil
	}
	if err = os.Mkdir(tmp, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if info, err = os.Lstat(tmp); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", ErrConflict
	}
	if err = writeAttachmentManifest(filepath.Join(tmp, ".discussion"), []byte(a.Discussion.ID)); err != nil {
		return "", err
	}
	var files []attachmentFile
	started := filepath.Join(tmp, ".started")
	if _, err = os.Lstat(started); errors.Is(err, os.ErrNotExist) {
		if err = writeAttachmentManifest(started, nil); err != nil {
			return "", err
		}
	}
	info, err = os.Lstat(started)
	if err != nil {
		return "", err
	}
	expired := t.service.Now().Sub(info.ModTime()) >= attachmentRetryWindow
	var total int
	for _, source := range sources {
		var refreshed *discordgo.Message
		var refreshAttempted bool
		legacyNames := t.service.Config.Mode == "personal" && len(sources) == 1 && len(teammateSnapshots(source.message)) == 0
		for _, candidate := range source.attachments() {
			att := candidate.file
			file := attachmentFile{ID: att.ID, Name: att.Filename, ContentType: att.ContentType, Size: att.Size, MessageID: source.route.MessageID, Forwarded: candidate.forwarded}
			if !snowflake(att.ID) || len(file.Name) > 1024 || len(file.ContentType) > 256 {
				file.Name = ""
				file.ContentType = ""
				file.Unavailable = "Invalid attachment metadata."
			} else if att.Size < 0 || att.Size > attachmentMaxFile || total+att.Size > attachmentMaxMessage || used+int64(total)+int64(att.Size)+65536 > attachmentMaxStore {
				file.Unavailable = "Attachment exceeds storage limits."
			} else {
				// Keep v0.29.16 filenames for direct personal bundles, including
				// partially downloaded bundles that survive an upgrade.
				label := fmt.Sprintf("file-%d%s", len(files)+1, inboundAttachmentExtension(att.ContentType))
				if legacyNames {
					label = fmt.Sprintf("file-%d", candidate.index+1)
				}
				dest := filepath.Join(tmp, label)
				existing, statErr := os.Lstat(dest)
				if statErr == nil && existing.Mode().IsRegular() && existing.Size() == int64(att.Size) {
					file.File = label
					total += att.Size
					files = append(files, file)
					continue
				}
				if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
					return "", statErr
				}
				if expired {
					file.Unavailable = "Download timed out after retries. Reattach this file in a new message."
					files = append(files, file)
					continue
				}
				err = t.client.downloadAttachment(ctx, source.route.ChannelID, att, dest)
				if errors.Is(err, errAttachmentURLExpired) {
					if !refreshAttempted {
						refreshAttempted = true
						refreshed, err = t.client.teammateMessage(ctx, source.route)
						if err != nil && !teammateAPIStatus(err, http.StatusNotFound) && !teammateAPIStatus(err, http.StatusForbidden) && !errors.Is(err, ErrConflict) {
							return "", errAttachmentRetry
						}
						if err != nil || teammateRevision(refreshed) != teammateRevision(source.message) {
							refreshed = nil
						}
					}
					err = errAttachmentURLExpired
					if refreshed != nil {
						for _, replacement := range (attachmentSource{message: refreshed}).attachments() {
							if replacement.forwarded == candidate.forwarded && replacement.file.ID == att.ID {
								err = t.client.downloadAttachment(ctx, source.route.ChannelID, replacement.file, dest)
								break
							}
						}
					}
				}
				if ctx.Err() != nil {
					return "", errAttachmentRetry
				}
				if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, errAttachmentURLExpired) {
					return "", err
				}
				if err != nil {
					file.Unavailable = "Download unavailable or incomplete. Reattach this file in a new message."
				} else {
					file.File = label
					total += att.Size
				}
			}
			files = append(files, file)
		}
	}
	if err = os.Remove(started); err != nil {
		return "", err
	}
	data, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	if err = writeAttachmentManifest(filepath.Join(tmp, "files.json"), data); err != nil {
		return "", err
	}
	if err = syncAttachmentDir(tmp); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, final); err != nil {
		return "", err
	}
	return manifest, syncAttachmentDir(root)
}

func attachmentDiscussion(dir string) string {
	file, err := os.OpenFile(filepath.Join(dir, ".discussion"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128 {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, 128))
	if err != nil {
		return ""
	}
	return string(data)
}

func inboundAttachmentExtension(value string) string {
	kind, _, _ := mime.ParseMediaType(value)
	switch kind {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "application/pdf":
		return ".pdf"
	case "text/csv":
		return ".csv"
	case "text/plain":
		return ".txt"
	default:
		return ""
	}
}

func syncAttachmentDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func writeAttachmentManifest(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Sync(), f.Close())
}

func (c *DiscordClient) downloadAttachment(ctx context.Context, channel string, a *discordgo.MessageAttachment, dest string) error {
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Host != "cdn.discordapp.com" || u.User != nil || u.Fragment != "" {
		return ErrInvalid
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "attachments" || parts[2] != channel || parts[3] != a.ID || parts[4] == "" || parts[4] == "." || parts[4] == ".." {
		return ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ErrInvalid
	}
	client := http.Client{Transport: c.http.Transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errAttachmentRetry
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusRequestTimeout || res.StatusCode >= 500 {
		return errAttachmentRetry
	}
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusNotFound {
		return errAttachmentURLExpired
	}
	if res.StatusCode != http.StatusOK || res.ContentLength >= 0 && res.ContentLength != int64(a.Size) {
		return ErrInvalid
	}
	// Publish a staged file only after verifying its complete body.
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(res.Body, int64(a.Size)+1))
	closeErr := errors.Join(f.Sync(), f.Close())
	if copyErr != nil || closeErr != nil || n != int64(a.Size) {
		_ = os.Remove(part)
		if closeErr != nil {
			return closeErr
		}
		if copyErr != nil {
			return errAttachmentRetry
		}
		return ErrInvalid
	}
	return os.Rename(part, dest)
}
