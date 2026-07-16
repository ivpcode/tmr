// Command tmr is a dependency-free tmux clone in Go.
//
// Usage:
//
//	tmr server            run the daemon in the foreground
//	tmr <command> [args]  send a command to the daemon (auto-starting it)
//
// The socket path is $TMR_SOCK, or /tmp/tmr-<uid>/default.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ivpcode/tmr/internal/client"
	"github.com/ivpcode/tmr/internal/server"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	sock := socketPath()

	// `tmr server` runs the daemon in the foreground (this is what the
	// auto-start path re-execs).
	if len(argv) > 0 && argv[0] == "server" {
		if err := server.Run(sock); err != nil {
			fmt.Fprintf(os.Stderr, "tmr: server: %v\n", err)
			return 1
		}
		return 0
	}

	// No command given: default to new-session (as tmux does).
	if len(argv) == 0 {
		argv = []string{"new-session"}
	}

	// Ensure a server is running, starting one in the background if needed.
	if !client.ServerRunning(sock) {
		if err := startServer(sock); err != nil {
			fmt.Fprintf(os.Stderr, "tmr: %v\n", err)
			return 1
		}
	}

	code, err := client.Send(sock, argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmr: %v\n", err)
		return 1
	}
	return code
}

// startServer re-execs this binary as `tmr server` detached in the background
// and waits (briefly) for the socket to come up.
func startServer(sock string) error {
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "server")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()

	// Wait up to ~2s for the server to start listening.
	for i := 0; i < 200; i++ {
		if client.ServerRunning(sock) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("server did not start")
}

// socketPath returns the unix-socket path for this user.
func socketPath() string {
	if s := os.Getenv("TMR_SOCK"); s != "" {
		return s
	}
	dir := fmt.Sprintf("/tmp/tmr-%d", os.Getuid())
	return filepath.Join(dir, "default")
}
