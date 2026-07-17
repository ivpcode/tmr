// Package server runs the ivt daemon: it owns the tmux.Server state and serves
// clients over a unix-domain socket. Each connection is either a one-shot
// command or an interactive attach stream. The event loop is just goroutines,
// replacing libevent.
package server

import (
	"bytes"
	"log"
	"net"
	"os"
	"sync"

	"github.com/ivpcode/tmr/internal/command"
	"github.com/ivpcode/tmr/internal/ipc"
	"github.com/ivpcode/tmr/internal/tmux"

	// Register the built-in commands.
	_ "github.com/ivpcode/tmr/internal/command/cmds"
)

// Server is the running daemon.
type Server struct {
	sock    string
	ln      net.Listener
	tmux    *tmux.Server
	quit    chan struct{}
	closed  sync.Once
	handler sync.WaitGroup
}

// Run starts a daemon listening on sockPath and blocks until it is asked to
// exit (kill-server, or the last session ending). Stale sockets are cleared.
func Run(sockPath string) error {
	if _, err := os.Stat(sockPath); err == nil {
		if c, derr := net.Dial("unix", sockPath); derr == nil {
			c.Close()
			return nil // a server is already running; nothing to do
		}
		os.Remove(sockPath)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	s := &Server{
		sock: sockPath,
		ln:   ln,
		quit: make(chan struct{}),
	}
	// When the last session goes away, exit like tmux.
	s.tmux = tmux.NewServer(s.Shutdown)
	return s.serve()
}

func (s *Server) serve() error {
	defer os.Remove(s.sock)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				s.handler.Wait()
				return nil
			default:
				log.Printf("accept: %v", err)
				return err
			}
		}
		s.handler.Add(1)
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer s.handler.Done()
	defer conn.Close()

	first, err := ipc.Read(conn)
	if err != nil {
		return
	}
	switch first.Kind {
	case ipc.KindCmd:
		s.handleCmd(conn, first)
	case ipc.KindAttach:
		s.handleAttach(conn, first)
	}
}

// handleCmd runs a one-shot command and replies with a result frame.
func (s *Server) handleCmd(conn net.Conn, f *ipc.Frame) {
	var out, errb bytes.Buffer
	code := 0
	if err := command.Dispatch(s.tmux, f.Argv, f.Cols, f.Rows, &out, &errb); err != nil {
		code = 1
	}
	ipc.Write(conn, &ipc.Frame{
		Kind:   ipc.KindResult,
		Stdout: out.String(),
		Stderr: errb.String(),
		Code:   code,
	})
}

// handleAttach streams a session's pty to and from the client until either side
// ends the attach.
func (s *Server) handleAttach(conn net.Conn, f *ipc.Frame) {
	sess := s.tmux.Get(f.Session)
	if sess == nil {
		ipc.Write(conn, &ipc.Frame{Kind: ipc.KindExit, Code: 1,
			Stderr: "no such session: " + f.Session})
		return
	}

	client := tmux.NewClient()
	sess.Attach(client, f.Cols, f.Rows)
	defer client.Close()
	defer sess.Detach(client)

	// Writer: pty output and control messages -> client. Every control message
	// ends the attach, so after relaying one it closes the connection to
	// unblock the reader below.
	go func() {
		for {
			select {
			case d := <-client.Out:
				if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindOutput, Data: d}); err != nil {
					conn.Close()
					return
				}
			case m := <-client.Ctrl:
				ipc.Write(conn, ctrlToFrame(m))
				conn.Close()
				return
			case <-client.Done():
				return
			}
		}
	}()

	// Reader: client keystrokes / control -> pty.
	for {
		fr, err := ipc.Read(conn)
		if err != nil {
			return
		}
		switch fr.Kind {
		case ipc.KindStdin:
			sess.WriteInput(fr.Data)
		case ipc.KindResize:
			sess.Resize(fr.Cols, fr.Rows)
		case ipc.KindDetach:
			return
		}
	}
}

func ctrlToFrame(m tmux.Control) *ipc.Frame {
	switch m.Kind {
	case tmux.CtrlSwitch:
		return &ipc.Frame{Kind: ipc.KindSwitch, Session: m.Session}
	case tmux.CtrlExit:
		return &ipc.Frame{Kind: ipc.KindExit, Code: m.Code}
	default:
		return &ipc.Frame{Kind: ipc.KindDetached}
	}
}

// Shutdown stops the accept loop and closes the listener.
func (s *Server) Shutdown() {
	s.closed.Do(func() {
		close(s.quit)
		s.ln.Close()
	})
}
