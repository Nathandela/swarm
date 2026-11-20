package discord

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
}

// Capture from the REST-verified message. Only exact message-scoped local paths
// are handed to native workers; signed CDN URLs never enter their prompts.
func (t *teammateConnector) captureAttachments(ctx context.Context, a TeammateAdmission, m *discordgo.Message) (string, error) {
	result, err := t.snapshotAttachments(ctx, a, m)
	if errors.Is(err, errAttachmentRetry) {
		// The unbound connector notice lane also operates before the first manager starts.
		notice := TeammatePublication{ID: "attachment_retry_" + digest([]string{a.Discussion.ID, a.Route.MessageID})[:32], Body: "Discord could not supply an attachment for message " + a.Route.MessageID + ". I kept your request and will retry for up to two minutes. If the download still fails, reattach the file in a new message."}
		err = errors.Join(err, t.service.Store.teammateQueue(a.Discussion.InstallationID, "", a.Route, notice, t.service.Now(), true))
	}
	return result, err
}

func (t *teammateConnector) snapshotAttachments(ctx context.Context, a TeammateAdmission, m *discordgo.Message) (string, error) {
	if len(m.Attachments) == 0 || t.service.Config.Mode != "personal" {
		return "", nil
	}
	if a.Discussion.InstallationID != t.service.Config.InstallationID || a.Discussion.ID == "" || a.Discussion.Route != (Route{ChannelID: t.service.Config.DMChannelID}) || a.Route.GuildID != "" || a.Author.ID != t.service.Config.OwnerID || a.Route.ChannelID != t.service.Config.DMChannelID || m.Author == nil || m.Author.ID != a.Author.ID || m.ID != a.Route.MessageID || m.ChannelID != a.Route.ChannelID || m.GuildID != "" || a.Revision != teammateRevision(m) {
		return "", ErrConflict
	}
	if t.service.Config.EffectiveProjectAccess() != ProjectAccessVM {
		return "File access unavailable in enrolled-project mode.", nil
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
	key := digest([]string{a.Discussion.InstallationID, a.Discussion.ID, a.Route.ChannelID, m.ID, a.Revision})
	final := filepath.Join(root, key)
	manifest := filepath.Join(final, "files.json")
	if _, err = os.Stat(manifest); err == nil {
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
	var used int64
	bundles := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_, hashErr := hex.DecodeString(entry.Name())
		if !strings.HasPrefix(entry.Name(), ".pending-") && (len(entry.Name()) != 64 || hashErr != nil) {
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
		if !pinned[dir] && t.service.Now().Sub(info.ModTime()) > 7*24*time.Hour {
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
	if err = os.MkdirAll(tmp, 0700); err != nil {
		return "", err
	}
	var files []attachmentFile
	if len(m.Attachments) > 10 {
		return "Too many attachments; files unavailable (maximum 10).", nil
	}
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
	var refreshed *discordgo.Message
	var refreshAttempted bool
	var total int
	for i, att := range m.Attachments {
		if att == nil {
			continue
		}
		file := attachmentFile{ID: att.ID, Name: att.Filename, ContentType: att.ContentType, Size: att.Size}
		if !snowflake(att.ID) || len(file.Name) > 1024 || len(file.ContentType) > 256 {
			file.Name = ""
			file.ContentType = ""
			file.Unavailable = "Invalid attachment metadata."
		} else if att.Size < 0 || att.Size > attachmentMaxFile || total+att.Size > attachmentMaxMessage || used+int64(total)+int64(att.Size)+65536 > attachmentMaxStore {
			file.Unavailable = "Attachment exceeds storage limits."
		} else {
			label := fmt.Sprintf("file-%d", i+1)
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
			err = t.client.downloadAttachment(ctx, m.ChannelID, att, dest)
			if errors.Is(err, errAttachmentURLExpired) {
				if !refreshAttempted {
					refreshAttempted = true
					refreshed, err = t.client.teammateMessage(ctx, a.Route)
					if err != nil {
						return "", errors.Join(errAttachmentRetry, err)
					}
					if !t.admissibleHuman(refreshed) || teammateRevision(refreshed) != a.Revision {
						return "", ErrConflict
					}
				}
				err = errAttachmentURLExpired
				if refreshed != nil {
					for _, replacement := range refreshed.Attachments {
						if replacement != nil && replacement.ID == att.ID {
							err = t.client.downloadAttachment(ctx, m.ChannelID, replacement, filepath.Join(tmp, label))
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
				file.Unavailable = "Download unavailable or incomplete."
			} else {
				file.File = label
				total += att.Size
			}
		}
		files = append(files, file)
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
