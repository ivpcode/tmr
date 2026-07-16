// Package server runs the tmr daemon: it owns the single tmux.Server state and
// serves commands from clients over a unix-domain socket. The event loop is
// just goroutines — one per accepted connection — replacing libevent.
package server

import (
	"bytes"
	"errors"
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
	handler sync.WaitGroup // in-flight client handlers
}

// Run starts a daemon listening on sockPath and blocks until the server is
// asked to exit (e.g. via kill-server). It removes any stale socket first.
func Run(sockPath string) error {
	// Remove a stale socket left by a previous crashed server.
	if _, err := os.Stat(sockPath); err == nil {
		if c, derr := net.Dial("unix", sockPath); derr == nil {
			c.Close()
			return errors.New("server already running")
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
		tmux: tmux.NewServer(),
		quit: make(chan struct{}),
	}
	command.SetShutdownHook(s.Shutdown)
	return s.serve()
}

func (s *Server) serve() error {
	defer os.Remove(s.sock)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				s.handler.Wait() // let in-flight replies (e.g. kill-server) flush
				return nil       // clean shutdown
			default:
				log.Printf("accept: %v", err)
				return err
			}
		}
		s.handler.Add(1)
		go s.handle(conn)
	}
}

// handle serves a single client connection: read one request, dispatch, reply.
func (s *Server) handle(conn net.Conn) {
	defer s.handler.Done()
	defer conn.Close()

	var req ipc.Request
	if err := ipc.ReadFrame(conn, &req); err != nil {
		return
	}

	var out, errb bytes.Buffer
	code := 0
	if err := command.Dispatch(s.tmux, req.Argv, &out, &errb); err != nil {
		code = 1
	}

	_ = ipc.WriteFrame(conn, &ipc.Response{
		Stdout: out.String(),
		Stderr: errb.String(),
		Code:   code,
	})
}

// Shutdown stops the accept loop and closes the listener.
func (s *Server) Shutdown() {
	s.closed.Do(func() {
		close(s.quit)
		s.ln.Close()
	})
}
