// Command ivt is a dependency-free, session-only tmux-style multiplexer for
// running long-lived agents (Claude Code and similar).
//
// The socket is $IVT_SOCK, or /tmp/ivt-<uid>/default. The daemon starts on
// demand and exits when the last session ends; its log goes next to the socket.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ivpcode/tmr/internal/client"
	"github.com/ivpcode/tmr/internal/server"
	"github.com/ivpcode/tmr/internal/term"
	"github.com/ivpcode/tmr/internal/web"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: ivt <command> [args]

  new     | n   [name] [command [args...]]   create a session and attach to it
  resume  | r   <name>                       attach to an existing session
  detach  | d   <name>                       detach clients from a session
  kill          <name>                       destroy a session
  ls                                         list sessions
  rename  | rn  <old> <new>                  rename a session
  to            <name>                       switch the active client to a session
  web           [-t token] <porta|host:porta>  web UI (HTTPS): session list + browser terminal
                  es.: web 9000 (tutte le interfacce) | web 127.0.0.1:9000 (solo locale)
  version                                    print the ivt version

Key bindings inside a session (tmux-style prefix Ctrl-B):
  Ctrl-B d        detach (the session keeps running)
  Ctrl-B n / p    switch to the next / previous session  (also ) and ()
  Ctrl-B Ctrl-B   send a literal Ctrl-B to the application

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

	case "version", "--version":
		fmt.Println("ivt " + version)
		return 0

	case "server":
		// Run the daemon in the foreground (the auto-start path re-execs this).
		if err := server.Run(sock); err != nil {
			fmt.Fprintf(os.Stderr, "ivt: server: %v\n", err)
			return 1
		}
		return 0

	case "web":
		fs := flag.NewFlagSet("web", flag.ContinueOnError)
		token := fs.String("t", "", "access token (default: generated)")
		if err := fs.Parse(argv[1:]); err != nil {
			return 1
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, `usage: ivt web [-t token] <porta|host:porta>
  ivt web 9000                tutte le interfacce (0.0.0.0:9000)
  ivt web 127.0.0.1:9000      solo localhost
  ivt web 192.168.1.234:9000  una interfaccia specifica`)
			return 1
		}
		addr, err := web.ParseAddr(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ivt: web: %v\n", err)
			return 1
		}
		if err := web.Run(web.Config{Addr: addr, Sock: sock, Token: *token}); err != nil {
			fmt.Fprintf(os.Stderr, "ivt: web: %v\n", err)
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
		if err := client.EnsureServer(sock); err != nil {
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

// socketPath returns the unix-socket path for this user.
func socketPath() string {
	if s := os.Getenv("IVT_SOCK"); s != "" {
		return s
	}
	return filepath.Join(fmt.Sprintf("/tmp/ivt-%d", os.Getuid()), "default")
}
