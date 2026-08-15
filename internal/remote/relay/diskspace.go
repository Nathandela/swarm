package relay

import "golang.org/x/sys/unix"

// diskFreeBytes reports bytes available to an unprivileged writer on the
// filesystem holding dir (playbook 6.5's low-disk alarm). golang.org/x/sys/unix
// is already a repo dependency (see internal/attach, internal/daemon) and
// covers both goreleaser targets for this binary, darwin and linux.
func diskFreeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
