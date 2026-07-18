// Package term puts the controlling terminal into raw mode and reads its size,
// using only the standard library. Linux only.
//
// This is the small piece of what tmux uses termios/terminfo for on the client
// side; there is no terminfo database — a raw terminal is all the attach loop
// needs.
package term

import (
	"syscall"
	"unsafe"
)

const tiocGWINSZ = 0x5413

type winsize struct {
	rows, cols     uint16
	xpixel, ypixel uint16
}

// State holds a saved terminal mode so it can be restored.
type State struct {
	fd  int
	old syscall.Termios
}

// MakeRaw switches the terminal at fd into raw mode and returns the previous
// state. Call Restore with it to undo.
func MakeRaw(fd int) (*State, error) {
	var old syscall.Termios
	if err := tcget(fd, &old); err != nil {
		return nil, err
	}
	raw := old
	// Input: no break processing, no CR/NL translation, no flow control.
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK |
		syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	// Output: raw.
	raw.Oflag &^= syscall.OPOST
	// Local: no echo, no canonical mode, no signals, no extended input.
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON |
		syscall.ISIG | syscall.IEXTEN
	// Control: 8-bit, no parity.
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	// Read returns as soon as 1 byte is available.
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	if err := tcset(fd, &raw); err != nil {
		return nil, err
	}
	return &State{fd: fd, old: old}, nil
}

// Restore returns the terminal to its saved state.
func (s *State) Restore() error {
	if s == nil {
		return nil
	}
	return tcset(s.fd, &s.old)
}

// Size returns the terminal's (cols, rows), defaulting to 80x24 on error.
func Size(fd int) (cols, rows uint16) {
	var ws winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(tiocGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.cols == 0 {
		return 80, 24
	}
	return ws.cols, ws.rows
}

// IsTerminal reports whether fd refers to a terminal.
func IsTerminal(fd int) bool {
	var t syscall.Termios
	return tcget(fd, &t) == nil
}

func tcget(fd int, t *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}

func tcset(fd int, t *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}
