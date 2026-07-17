//go:build darwin

package attach

import "golang.org/x/sys/unix"

// readTermios / writeTermios wrap the darwin termios ioctls (TIOCGETA/TIOCSETA).
// They are named distinctly from the test suite's getTermios helper, which is
// compiled only in the test build.
func readTermios(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TIOCGETA)
}

func writeTermios(fd int, t *unix.Termios) error {
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, t)
}
