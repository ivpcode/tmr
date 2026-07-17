// Command ivt is a dependency-free, session-only tmux-style multiplexer for
// running long-lived agents (Claude Code and similar).
//
// Commands:
//
//	ivt [n|new]  [name] [command [args...]]   create a session and attach
//	ivt [r|resume] <name>                     attach to an existing session
//	ivt [d|detach] <name>                     detach clients from a session
//	ivt kill <name>                           destroy a session
//	ivt ls                                    list sessions
//	ivt [rn|rename] <old> <new>               rename a session
//	ivt to <name>                             switch the active client to a session
//
// The socket is $IVT_SOCK, or /tmp/ivt-<uid>/default.
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

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	sock := socketPath()

	// `ivt server` runs the daemon in the foreground.
	if len(argv) > 0 && argv[0] == "server" {
		if err := server.Run(sock); err != nil {
			fmt.Fprintf(os.Stderr, "ivt: server: %v\n", err)
			return 1
		}
		return 0
	}

	// No arguments: create and attach a new session.
	if len(argv) == 0 {
		argv = []string{"new"}
	}

	switch argv[0] {
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

// attach runs the interactive attach loop and maps its result to an exit code.
func attach(sock, session string) int {
	if err := client.Attach(sock, session); err != nil {
		return 1
	}
	return 0
}

// ensureServer starts the daemon in the background if it isn't already running.
func ensureServer(sock string) error {
	if client.ServerRunning(sock) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "server")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	// Detach the daemon into its own session with no controlling terminal, so
	// it survives the client's terminal closing (SIGHUP) on detach.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()

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
