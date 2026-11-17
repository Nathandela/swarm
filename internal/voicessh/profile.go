package voicessh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/voiceclient"
	"github.com/Nathandela/swarm/internal/voicewire"
)

var (
	ErrEnrollmentRequired = errors.New("voice: this SSH alias is not enrolled; add --enroll explicitly")
	ErrTargetChanged      = errors.New("voice: this SSH alias now resolves to a different target; select and enroll a distinct alias")
	ErrProfile            = errors.New("voice: invalid private SSH voice profile")
	ErrProfileBusy        = errors.New("voice: this SSH voice alias already has an active panel")
	ErrEnrollment         = errors.New("voice: SSH voice enrollment could not be confirmed")
)

type profile struct {
	Version    int                        `json:"version"`
	Identity   string                     `json:"identity"`
	Seed       []byte                     `json:"seed"`
	Credential voicewire.DeviceCredential `json:"credential"`
}

// Connection holds one desktop device grant and its signed client.
type Connection struct {
	Client     *voiceclient.DeviceClient
	Credential voicewire.DeviceCredential
	profile    *profileFiles
}

func (c *Connection) Close() {
	c.Client.Close()
	c.profile.close()
}

// Open resolves an explicitly named OpenSSH alias and optionally enrolls this desktop.
func Open(ctx context.Context, dir, alias string, enroll bool) (*Connection, error) {
	if !ValidAlias(alias) {
		return nil, ErrInvalidAlias
	}
	binary, err := exec.LookPath("ssh")
	if err != nil {
		return nil, ErrSSH
	}
	return open(ctx, dir, connector{binary: binary, alias: alias}, enroll)
}

func open(ctx context.Context, dir string, s connector, enroll bool) (*Connection, error) {
	files, err := openProfileFiles(dir, s.alias, enroll)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			files.close()
		}
	}()
	stored, err := files.read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if stored.Credential.DeviceID == "" && !enroll {
		return nil, ErrEnrollmentRequired
	}
	identity, err := s.identity(ctx)
	if err != nil {
		return nil, err
	}
	if stored.Identity != "" && stored.Identity != identity {
		return nil, ErrTargetChanged
	}
	raw, err := s.remote(ctx, "device-endpoint")
	if err != nil {
		return nil, err
	}
	var endpoint string
	if json.Unmarshal(raw, &endpoint) != nil || !validEndpoint(endpoint) {
		return nil, ErrForward
	}
	if stored.Version == 0 {
		stored = profile{Version: 1, Identity: identity, Seed: make([]byte, ed25519.SeedSize)}
		if _, err = rand.Read(stored.Seed); err != nil {
			return nil, err
		}
		if err = files.write(stored); err != nil {
			return nil, err
		}
	}
	signer := ed25519.NewKeyFromSeed(stored.Seed)
	client, err := voiceclient.NewDeviceClientWithDial(ctx, endpoint, signer, func(lifetime context.Context) (net.Conn, error) {
		return s.forward(lifetime, endpoint)
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if !keep {
			client.Close()
		}
	}()
	needsEnrollment := stored.Credential.DeviceID == ""
	if !needsEnrollment {
		if _, err = client.Status(ctx, stored.Credential); err != nil {
			var denied *voiceclient.DeviceHTTPError
			if !enroll || !errors.As(err, &denied) || denied.StatusCode != http.StatusForbidden {
				return nil, err
			}
			needsEnrollment = true
		}
	}
	if needsEnrollment {
		raw, err = s.remote(ctx, "enroll", "--device-key", base64.RawURLEncoding.EncodeToString(signer.Public().(ed25519.PublicKey)))
		if err != nil {
			return nil, err
		}
		var invitation voicewire.DeviceInvitation
		if json.Unmarshal(raw, &invitation) != nil || invitation.Endpoint != endpoint || !validHex(invitation.Reference, 32) ||
			!time.Now().Before(invitation.ExpiresAt) {
			return nil, ErrEnrollment
		}
		if stored.Credential.PlaceID != "" && invitation.PlaceID != stored.Credential.PlaceID {
			return nil, ErrTargetChanged
		}
		grant, err := client.Enroll(ctx, invitation)
		if err != nil {
			return nil, errors.Join(ErrEnrollment, err)
		}
		if !validCredential(grant) {
			return nil, ErrEnrollment
		}
		if _, err = client.Status(ctx, grant); err != nil {
			return nil, err
		}
		stored.Credential = grant
		if err = files.write(stored); err != nil {
			return nil, err
		}
	}
	keep = true
	return &Connection{Client: client, Credential: stored.Credential, profile: files}, nil
}

func validHex(s string, n int) bool {
	word, err := hex.DecodeString(s)
	return err == nil && len(word) == n && hex.EncodeToString(word) == s
}

func validCredential(g voicewire.DeviceCredential) bool {
	return g.PlaceID != "" && len(g.PlaceID) <= 128 && validHex(g.DeviceID, 16) && validHex(g.Token, 32) && g.Generation > 0
}

type profileFiles struct {
	root *os.Root
	lock *os.File
	name string
}

func openProfileFiles(dir, alias string, create bool) (*profileFiles, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, ErrProfile
	}
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrEnrollmentRequired
	}
	if err != nil || !info.IsDir() || !privateOwner(info, 0700) {
		return nil, ErrProfile
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrProfile
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, ErrProfile
	}
	digest := sha256.Sum256([]byte(alias))
	name := hex.EncodeToString(digest[:])
	lock, err := root.OpenFile(name+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		_ = root.Close()
		return nil, ErrProfile
	}
	files := &profileFiles{root: root, lock: lock, name: name + ".json"}
	locked, err := lock.Stat()
	if err != nil || !privateRegular(locked) {
		files.close()
		return nil, ErrProfile
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		files.close()
		return nil, ErrProfileBusy
	}
	return files, nil
}

func (f *profileFiles) close() {
	_ = f.lock.Close()
	_ = f.root.Close()
}

func privateOwner(info os.FileInfo, mode os.FileMode) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Getuid()) && info.Mode().Perm() == mode
}

func privateRegular(info os.FileInfo) bool {
	if !info.Mode().IsRegular() || !privateOwner(info, 0600) {
		return false
	}
	return info.Sys().(*syscall.Stat_t).Nlink == 1
}

func (f *profileFiles) read() (profile, error) {
	var result profile
	fd, err := f.root.OpenFile(f.name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, err
	}
	defer func() { _ = fd.Close() }()
	info, err := fd.Stat()
	if err != nil || !privateRegular(info) {
		return result, ErrProfile
	}
	raw, err := io.ReadAll(io.LimitReader(fd, 4097))
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &result) != nil || result.Version != 1 ||
		len(result.Seed) != ed25519.SeedSize || !validHex(result.Identity, 32) ||
		(result.Credential != (voicewire.DeviceCredential{}) && !validCredential(result.Credential)) {
		return profile{}, ErrProfile
	}
	canonical, _ := json.Marshal(result)
	if !bytes.Equal(raw, canonical) {
		return profile{}, ErrProfile
	}
	return result, nil
}

func (f *profileFiles) write(value profile) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return err
	}
	name := ".profile-" + hex.EncodeToString(token[:])
	fd, err := f.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.root.Remove(name) }()
	if _, err = fd.Write(raw); err == nil {
		err = fd.Sync()
	}
	if err = errors.Join(err, fd.Close()); err != nil {
		return err
	}
	if err = f.root.Rename(name, f.name); err != nil {
		return err
	}
	parent, err := f.root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
