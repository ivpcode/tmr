// Package client connects to a running tmr server, sends a single command, and
// prints the reply. (Interactive attach — taking over the terminal — will be
// added with the rendering layer.)
package client

import (
	"fmt"
	"net"
	"os"

	"github.com/ivpcode/tmr/internal/ipc"
)

// Send dials the server at sockPath, sends argv as one command, prints the
// reply to stdout/stderr and returns the server's exit code.
func Send(sockPath string, argv []string) (int, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return 1, err
	}
	defer conn.Close()

	req := ipc.Request{Argv: argv, Term: os.Getenv("TERM")}
	if err := ipc.WriteFrame(conn, &req); err != nil {
		return 1, err
	}

	var resp ipc.Response
	if err := ipc.ReadFrame(conn, &resp); err != nil {
		return 1, err
	}
	if resp.Stdout != "" {
		fmt.Fprint(os.Stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		fmt.Fprint(os.Stderr, resp.Stderr)
	}
	return resp.Code, nil
}

// ServerRunning reports whether a server is listening on sockPath.
func ServerRunning(sockPath string) bool {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
