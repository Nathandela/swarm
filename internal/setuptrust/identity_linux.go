//go:build linux

package setuptrust

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func directoryIdentity(cwd string) (string, error) {
	var stat unix.Statx_t
	mask := unix.STATX_TYPE | unix.STATX_INO | unix.STATX_BTIME
	if err := unix.Statx(unix.AT_FDCWD, cwd, unix.AT_SYMLINK_NOFOLLOW, mask, &stat); err != nil {
		return "", err
	}
	if int(stat.Mask)&mask != mask || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Btime.Sec == 0 {
		return "", ErrWorkspaceIdentity
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", stat.Dev_major, stat.Dev_minor, stat.Ino, stat.Btime.Sec, stat.Btime.Nsec), nil
}
