// Command ivt is a dependency-free, session-only tmux-style multiplexer for
// running long-lived agents (Claude Code and similar).
//
// The socket is $IVT_SOCK, or /tmp/ivt-<uid>/default. The daemon starts on
// demand and exits when the last session ends; its log goes next to the socket.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ivpcode/tmr/internal/client"
	"github.com/ivpcode/tmr/internal/server"
	"github.com/ivpcode/tmr/internal/term"
)

const usage = `usage: ivt <command> [args]

  new     | n   [name] [command [args...]]   create a session and attach to it
  resume  | r   <name>                       attach to an existing session
  detach  | d   <name>                       detach clients from a session
  kill          <name>                       destroy a session
  ls                                         list sessions
  rename  | rn  <old> <new>                  rename a session
  to            <name>                       switch the active client to a session

Inside a session, press Ctrl-\ to detach (the session keeps running).
Socket: $IVT_SOCK or /tmp/ivt-<uid>/default.`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	sock := socketPath()

	// No arguments: create and attach a new session.
	if len(argv) == 0 {
		argv = []string{"new"}
	}

	switch argv[0] {
	case "help", "-h", "--help":
		fmt.Println(usage)
		return 0

	case "server":
		// Run the daemon in the foreground (the auto-start path re-execs this).
		if err := server.Run(sock); err != nil {
			fmt.Fprintf(os.Stderr, "ivt: server: %v\n", err)
			return 1
		}
		return 0

	case "resume", "r":
		if len(argv) != 2 {
			fmt.Fprintln(os.Stderr, "usage: ivt resume <session>")
			return 1
		}
		if !client.ServerRunning(sock) {
			fmt.Fprintln(os.Stderr, "ivt: no server running")
			return 1
		}
		return attach(sock, argv[1])

	case "new", "n":
		if err := ensureServer(sock); err != nil {
			fmt.Fprintf(os.Stderr, "ivt: %v\n", err)
			return 1
		}
		out, errs, code, err := client.SendCmd(sock, argv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ivt: %v\n", err)
			return 1
		}
		if code != 0 {
			fmt.Fprint(os.Stderr, errs)
			return code
		}
		name := strings.TrimSpace(out)
		// Attach to the freshly-created session when we have a terminal.
		if name != "" && term.IsTerminal(0) {
			return attach(sock, name)
		}
		fmt.Print(out)
		return 0

	default:
		// One-shot commands: ls, kill, rename, detach, to.
		if !client.ServerRunning(sock) {
			fmt.Fprintln(os.Stderr, "ivt: no server running")
			return 1
		}
		out, errs, code, err := client.SendCmd(sock, argv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ivt: %v\n", err)
			return 1
		}
		fmt.Print(out)
		fmt.Fprint(os.Stderr, errs)
		return code
	}
}

// attach runs the interactive attach loop. Its exit code is the session
// process's exit code when the session ended while attached, 0 on detach.
func attach(sock, session string) int {
	code, err := client.Attach(sock, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ivt: %v\n", err)
		return 1
	}
	return code
}

// ensureServer starts the daemon in the background if it isn't already running.
func ensureServer(sock string) error {
	if client.ServerRunning(sock) {
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
	// Keep the daemon's (rare) diagnostics next to the socket.
	if logf, err := os.OpenFile(filepath.Join(dir, "server.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	// Detach the daemon into its own session with no controlling terminal, so
	// it survives the client's terminal closing (SIGHUP) on detach.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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
	if s := os.Getenv("IVT_SOCK"); s != "" {
		return s
	}
	return filepath.Join(fmt.Sprintf("/tmp/ivt-%d", os.Getuid()), "default")
}
