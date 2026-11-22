package setuptrust

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
)

func directoryIdentity(cwd string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(cwd, &stat); err != nil {
		return "", err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Birthtimespec.Sec == 0 {
		return "", errors.New("setup trust: workspace must be a directory on a filesystem that records its creation time")
	}
	return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec), nil
}
