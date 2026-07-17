// Package client talks to the ivt server: one-shot commands and the interactive
// attach loop that takes over the local terminal. Interactive input/output is
// streamed raw; there is no client-side rendering because the server owns the
// pty.
package client

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/ivpcode/tmr/internal/ipc"
	"github.com/ivpcode/tmr/internal/term"
)

// detachKey is the byte that detaches the client locally (Ctrl-\), leaving the
// session running. Detaching from another terminal via `ivt detach` also works.
const detachKey = 0x1c

// ServerRunning reports whether a server is listening on sockPath.
func ServerRunning(sockPath string) bool {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// SendCmd runs one command on the server and returns its output and exit code.
func SendCmd(sockPath string, argv []string) (stdout, stderr string, code int, err error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return "", "", 1, err
	}
	defer conn.Close()

	cols, rows := term.Size(int(os.Stdout.Fd()))
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindCmd, Argv: argv, Cols: cols, Rows: rows}); err != nil {
		return "", "", 1, err
	}
	resp, err := ipc.Read(conn)
	if err != nil {
		return "", "", 1, err
	}
	return resp.Stdout, resp.Stderr, resp.Code, nil
}

// action is how a single attach stream ended.
type action struct {
	kind    string // "detached", "exit", "switch", "closed"
	session string // next session for "switch"
	code    int    // session process exit code for "exit"
	stderr  string // error text for "exit"
}

// Attach takes over the terminal and streams the given session until the user
// detaches or the session exits. It follows `to` switches by reconnecting.
// The returned code is the session process's exit code when it ended while
// attached, 0 on detach.
func Attach(sockPath, session string) (int, error) {
	if !term.IsTerminal(0) {
		return 1, fmt.Errorf("attach: stdin is not a terminal")
	}
	state, err := term.MakeRaw(0)
	if err != nil {
		return 1, fmt.Errorf("raw mode: %w", err)
	}
	defer state.Restore()

	// Raw mode survives only as long as this process: if something kills the
	// client (SIGTERM/SIGHUP — keyboard signals are off in raw mode), restore
	// the terminal before dying instead of leaving the shell unusable.
	killCh := make(chan os.Signal, 1)
	signal.Notify(killCh, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(killCh)
	go func() {
		if _, ok := <-killCh; ok {
			state.Restore()
			os.Exit(1)
		}
	}()

	stdinCh := make(chan []byte, 16)
	detachCh := make(chan struct{}, 1)
	go readStdin(stdinCh, detachCh)

	winchCh := make(chan struct{}, 1)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			select {
			case winchCh <- struct{}{}:
			default:
			}
		}
	}()

	cur := session
	for {
		act, err := stream(sockPath, cur, stdinCh, detachCh, winchCh)
		if err != nil {
			return 1, err
		}
		switch act.kind {
		case "switch":
			cur = act.session
			continue
		case "exit":
			state.Restore()
			if act.stderr != "" {
				fmt.Fprintln(os.Stderr, "ivt: "+act.stderr)
			}
			return act.code, nil
		case "closed":
			state.Restore()
			return 1, fmt.Errorf("server connection lost")
		default: // detached
			return 0, nil
		}
	}
}

// stream runs one attach connection and returns how it ended.
func stream(sockPath, session string, stdinCh <-chan []byte, detachCh, winchCh <-chan struct{}) (action, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return action{}, err
	}
	defer conn.Close()

	cols, rows := term.Size(0)
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindAttach, Session: session, Cols: cols, Rows: rows}); err != nil {
		return action{}, err
	}

	frameCh := make(chan *ipc.Frame)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			f, err := ipc.Read(conn)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case frameCh <- f:
			case <-done:
				return
			}
		}
	}()

	for {
		select {
		case f := <-frameCh:
			switch f.Kind {
			case ipc.KindOutput:
				os.Stdout.Write(f.Data)
			case ipc.KindDetached:
				return action{kind: "detached"}, nil
			case ipc.KindSwitch:
				return action{kind: "switch", session: f.Session}, nil
			case ipc.KindExit:
				return action{kind: "exit", code: f.Code, stderr: f.Stderr}, nil
			}
		case d := <-stdinCh:
			if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindStdin, Data: d}); err != nil {
				return action{kind: "closed"}, nil
			}
		case <-winchCh:
			cols, rows := term.Size(0)
			ipc.Write(conn, &ipc.Frame{Kind: ipc.KindResize, Cols: cols, Rows: rows})
		case <-detachCh:
			ipc.Write(conn, &ipc.Frame{Kind: ipc.KindDetach})
			return action{kind: "detached"}, nil
		case <-readErr:
			return action{kind: "closed"}, nil
		}
	}
}

// readStdin forwards stdin to stdinCh, signalling detachCh when it sees the
// local detach key (and dropping that byte and anything after it).
func readStdin(stdinCh chan<- []byte, detachCh chan<- struct{}) {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			data := buf[:n]
			if i := bytes.IndexByte(data, detachKey); i >= 0 {
				if i > 0 {
					send(stdinCh, data[:i])
				}
				select {
				case detachCh <- struct{}{}:
				default:
				}
				return
			}
			send(stdinCh, data)
		}
		if err != nil {
			return
		}
	}
}

// send delivers a copy of data (buf is reused by the reader).
func send(ch chan<- []byte, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	ch <- cp
}
