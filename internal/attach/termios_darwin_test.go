package attach

import "golang.org/x/sys/unix"

// getTermios reads the tty termios on darwin (TIOCGETA).
func getTermios(fd uintptr) (*unix.Termios, error) {
	return unix.IoctlGetTermios(int(fd), unix.TIOCGETA)
}
