// Package client talks to the ivt server: one-shot commands and the interactive
// attach loop that takes over the local terminal. Interactive input/output is
// streamed raw; there is no client-side rendering because the server owns the
// pty.
package client

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ivpcode/tmr/internal/ipc"
	"github.com/ivpcode/tmr/internal/term"
)

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
	actionCh := make(chan byte, 8)
	go readStdin(stdinCh, actionCh)

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
		act, err := stream(sockPath, cur, stdinCh, actionCh, winchCh)
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

// stream runs one attach connection and returns how it ended. While attached
// it maintains the tmux-style status bar on the terminal's bottom row (the pty
// runs one row shorter, protected by a scroll region).
func stream(sockPath, session string, stdinCh <-chan []byte, actionCh <-chan byte, winchCh <-chan struct{}) (action, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return action{}, err
	}
	defer conn.Close()

	cols, rows := term.Size(0)
	barRow := 0 // 0 = terminal too small for a bar
	ptyRows := rows
	if rows >= 3 {
		barRow = int(rows)
		ptyRows = rows - 1
	}
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindAttach, Session: session, Cols: cols, Rows: ptyRows}); err != nil {
		return action{}, err
	}
	if barRow > 0 {
		os.Stdout.WriteString(setRegion(int(ptyRows)))
		os.Stdout.Write(statusLine(int(cols), barRow, session, time.Now()))
		defer func() { os.Stdout.Write(clearBar(barRow)) }()
	}
	// Window/tab title = session name, restored when the attach ends.
	os.Stdout.WriteString(pushTitle)
	os.Stdout.Write(setTitle(session))
	defer os.Stdout.WriteString(popTitle)

	clock := time.NewTicker(time.Second)
	defer clock.Stop()

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
			cols, rows = term.Size(0)
			ptyRows = rows
			barRow = 0
			if rows >= 3 {
				barRow = int(rows)
				ptyRows = rows - 1
			}
			ipc.Write(conn, &ipc.Frame{Kind: ipc.KindResize, Cols: cols, Rows: ptyRows})
			if barRow > 0 {
				os.Stdout.WriteString(setRegion(int(ptyRows)))
				os.Stdout.Write(statusLine(int(cols), barRow, session, time.Now()))
			} else {
				os.Stdout.WriteString(resetRegion)
			}
		case <-clock.C:
			if barRow > 0 {
				c, _ := term.Size(0)
				os.Stdout.Write(statusLine(int(c), barRow, session, time.Now()))
			}
			os.Stdout.Write(setTitle(session))
		case a := <-actionCh:
			switch a {
			case actDetach:
				ipc.Write(conn, &ipc.Frame{Kind: ipc.KindDetach})
				return action{kind: "detached"}, nil
			case actNext, actPrev:
				target := sessionNeighbor(sockPath, session, a)
				if target != "" && target != session {
					ipc.Write(conn, &ipc.Frame{Kind: ipc.KindDetach})
					return action{kind: "switch", session: target}, nil
				}
			}
		case <-readErr:
			return action{kind: "closed"}, nil
		}
	}
}

// sessionNeighbor returns the next/previous session name relative to current,
// wrapping around ("" if the list can't be fetched or has one entry).
func sessionNeighbor(sockPath, current string, dir byte) string {
	stdout, _, code, err := SendCmd(sockPath, []string{"ls", "-j"})
	if err != nil || code != 0 {
		return ""
	}
	var list []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(stdout)), &list) != nil || len(list) < 2 {
		return ""
	}
	cur := 0
	for i, s := range list {
		if s.Name == current {
			cur = i
			break
		}
	}
	step := 1
	if dir == actPrev {
		step = len(list) - 1
	}
	return list[(cur+step)%len(list)].Name
}

// readStdin forwards keyboard bytes to stdinCh, translating Ctrl-B prefix
// sequences into client actions on actionCh (see keys.go).
func readStdin(stdinCh chan<- []byte, actionCh chan<- byte) {
	var kf keyFilter
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			out, actions := kf.Feed(buf[:n])
			if len(out) > 0 {
				cp := make([]byte, len(out))
				copy(cp, out)
				stdinCh <- cp
			}
			for _, a := range actions {
				select {
				case actionCh <- a:
				default:
				}
			}
		}
		if err != nil {
			return
		}
	}
}
