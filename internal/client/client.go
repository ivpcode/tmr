// Package client talks to the ivt server: one-shot commands and the interactive
// attach loop that takes over the local terminal. Interactive input/output is
// streamed raw; there is no client-side rendering because the server owns the
// pty.
package client

import (
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
	stderr  string // error text for "exit"
}

// Attach takes over the terminal and streams the given session until the user
// detaches or the session exits. It follows `to` switches by reconnecting.
func Attach(sockPath, session string) error {
	if !term.IsTerminal(0) {
		return fmt.Errorf("attach: stdin is not a terminal")
	}
	state, err := term.MakeRaw(0)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer state.Restore()

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
			return err
		}
		if act.kind == "switch" {
			cur = act.session
			continue
		}
		state.Restore()
		if act.kind == "exit" && act.stderr != "" {
			fmt.Fprintln(os.Stderr, "ivt: "+act.stderr)
			return fmt.Errorf("%s", act.stderr)
		}
		return nil
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
				return action{kind: "exit", stderr: f.Stderr}, nil
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
// local detach key (and dropping that byte).
func readStdin(stdinCh chan<- []byte, detachCh chan<- struct{}) {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			data := buf[:n]
			if i := indexByte(data, detachKey); i >= 0 {
				// Forward anything before the detach key, then signal.
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

func send(ch chan<- []byte, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	ch <- cp
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}
