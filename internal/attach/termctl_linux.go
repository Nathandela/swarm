//go:build linux

package attach

import "golang.org/x/sys/unix"

// readTermios / writeTermios wrap the linux termios ioctls (TCGETS/TCSETS). They
// are named distinctly from the test suite's getTermios helper, which is compiled
// only in the test build.
func readTermios(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TCGETS)
}

func writeTermios(fd int, t *unix.Termios) error {
	return unix.IoctlSetTermios(fd, unix.TCSETS, t)
}
