// Package pty opens pseudo-terminals and starts processes attached to them,
// using only the Go standard library (no cgo, no external modules). Linux only.
//
// This replaces tmux's forkpty/openpty compat layer.
package pty

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// Linux ioctl request numbers (amd64/arm64 share these values).
const (
	tiocSPTLCK = 0x40045431 // lock/unlock pty slave
	tiocGPTN   = 0x80045430 // get pty number
	tiocSWINSZ = 0x5414     // set window size
)

// winsize mirrors struct winsize.
type winsize struct {
	rows, cols     uint16
	xpixel, ypixel uint16
}

// Process is a started child together with the master side of its pty.
type Process struct {
	Master *os.File     // the master (ptmx) end — read child output, write child input
	Proc   *os.Process  // the child process
	Wait   func() error // waits for the child to exit
}

// Start opens a new pty and starts name+args attached to its slave side as the
// controlling terminal, in a new session. The returned Master is the pty master.
func Start(name string, args, env []string, cols, rows uint16) (*Process, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			master.Close()
		}
	}()

	// Unlock the slave (TIOCSPTLCK = 0) and find its number (TIOCGPTN).
	var zero int32
	if err := ioctl(master.Fd(), tiocSPTLCK, uintptr(unsafe.Pointer(&zero))); err != nil {
		return nil, fmt.Errorf("unlockpt: %w", err)
	}
	var n uint32
	if err := ioctl(master.Fd(), tiocGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		return nil, fmt.Errorf("ptsname: %w", err)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", n)

	// Apply the initial window size to the master (propagates to the slave).
	setSize(master.Fd(), cols, rows)

	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", slavePath, err)
	}
	defer slave.Close() // parent doesn't keep the slave; the child dups it

	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true, // new session, detach from our controlling tty
		Setctty: true, // make the slave the controlling terminal
		Ctty:    0,    // fd 0 in the child (== slave, via Stdin)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}

	ok = true
	return &Process{
		Master: master,
		Proc:   cmd.Process,
		Wait:   cmd.Wait,
	}, nil
}

// Setsize changes the window size of the pty (used on terminal resize).
func Setsize(master *os.File, cols, rows uint16) error {
	return setSize(master.Fd(), cols, rows)
}

func setSize(fd uintptr, cols, rows uint16) error {
	ws := winsize{rows: rows, cols: cols}
	return ioctl(fd, tiocSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

func ioctl(fd, request, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, arg)
	if errno != 0 {
		return errno
	}
	return nil
}
