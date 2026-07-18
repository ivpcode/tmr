package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// EnsureServer starts the ivt daemon in the background if one isn't already
// listening on sock, and waits for it to come up. The daemon is re-execed from
// this binary as `ivt server`, detached into its own session so it survives
// the caller's terminal; its diagnostics go to server.log next to the socket.
func EnsureServer(sock string) error {
	if ServerRunning(sock) {
		return nil
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "server")
	cmd.Stdin = nil
	if logf, err := os.OpenFile(filepath.Join(dir, "server.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()

	for i := 0; i < 200; i++ {
		if ServerRunning(sock) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("server did not start")
}
