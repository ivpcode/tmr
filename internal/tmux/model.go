// Package tmux holds the core model: a server owning a flat set of named
// sessions. Each session runs one real process in a pseudo-terminal that stays
// alive while clients attach and detach. There are no windows or panes — the
// clone is deliberately scoped to session management for long-running agents.
package tmux

import (
	"fmt"
	"sync"
	"time"

	"github.com/ivpcode/tmr/internal/pty"
)

// ringSize is how much recent pty output is retained to replay on attach.
const ringSize = 256 << 10 // 256 KiB

// Control is an out-of-band message sent to an attached client.
type Control struct {
	Kind    string // "detached", "switch", "exit"
	Session string // target session for "switch"
	Code    int    // process exit code for "exit"
}

// Client is a server-side view of one attached client (a running `resume`).
type Client struct {
	Out  chan []byte  // pty output destined for this client
	Ctrl chan Control // out-of-band control messages
	done chan struct{}
}

// NewClient makes a client with reasonable buffering.
func NewClient() *Client {
	return &Client{
		Out:  make(chan []byte, 512),
		Ctrl: make(chan Control, 8),
		done: make(chan struct{}),
	}
}

// Close marks the client finished so broadcasts skip it.
func (c *Client) Close() { close(c.done) }

// Done is closed when the client is finished; server writers select on it.
func (c *Client) Done() <-chan struct{} { return c.done }

// Server owns all sessions.
type Server struct {
	mu       sync.Mutex
	sessions map[string]*Session
	order    []string // session names in creation order
	nextNum  int      // for auto-generated names

	last    *Client // most-recently attached client (target of `to`)
	onEmpty func()  // invoked when the last session is gone
	Created time.Time
}

// Session is one named process running in a pty.
type Session struct {
	Name    string
	Cmd     []string
	Created time.Time

	srv  *Server
	proc *pty.Process

	mu       sync.Mutex
	ring     *ring
	clients  map[*Client]struct{}
	exited   bool
	exitCode int
}

// NewServer returns an empty server. onEmpty (may be nil) is called once the
// last session disappears, so the daemon can exit like tmux does.
func NewServer(onEmpty func()) *Server {
	return &Server{
		sessions: map[string]*Session{},
		onEmpty:  onEmpty,
		Created:  time.Now(),
	}
}

// ---- Session lifecycle ----------------------------------------------------

// NewSession creates a session named name (auto-generated if empty) running
// cmd (the login shell if cmd is empty), sized cols x rows.
func (s *Server) NewSession(name string, cmd []string, cols, rows uint16) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "" {
		name = s.uniqueNameLocked()
	}
	if _, dup := s.sessions[name]; dup {
		return nil, fmt.Errorf("duplicate session: %s", name)
	}
	if len(cmd) == 0 {
		cmd = []string{loginShell()}
	}

	proc, err := pty.Start(cmd[0], cmd[1:], childEnv(), cols, rows)
	if err != nil {
		return nil, err
	}

	sess := &Session{
		Name:    name,
		Cmd:     cmd,
		Created: time.Now(),
		srv:     s,
		proc:    proc,
		ring:    newRing(ringSize),
		clients: map[*Client]struct{}{},
	}
	s.sessions[name] = sess
	s.order = append(s.order, name)

	go sess.pump()
	return sess, nil
}

// Kill terminates a session's process and removes it.
func (s *Server) Kill(name string) error {
	s.mu.Lock()
	sess := s.sessions[name]
	s.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("session not found: %s", name)
	}
	sess.proc.Proc.Kill()
	// pump() will observe the exit and call remove().
	return nil
}

// Rename changes a session's name.
func (s *Server) Rename(oldName, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[oldName]
	if sess == nil {
		return fmt.Errorf("session not found: %s", oldName)
	}
	if _, dup := s.sessions[newName]; dup {
		return fmt.Errorf("duplicate session: %s", newName)
	}
	delete(s.sessions, oldName)
	s.sessions[newName] = sess
	sess.Name = newName
	for i, n := range s.order {
		if n == oldName {
			s.order[i] = newName
			break
		}
	}
	return nil
}

// List returns sessions in creation order.
func (s *Server) List() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.sessions[n])
	}
	return out
}

// Get returns a session by name, or nil.
func (s *Server) Get(name string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[name]
}

func (s *Server) uniqueNameLocked() string {
	for {
		name := fmt.Sprintf("%d", s.nextNum)
		s.nextNum++
		if _, ok := s.sessions[name]; !ok {
			return name
		}
	}
}

func (s *Server) remove(name string) {
	s.mu.Lock()
	delete(s.sessions, name)
	for i, n := range s.order {
		if n == name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	empty := len(s.sessions) == 0
	onEmpty := s.onEmpty
	s.mu.Unlock()
	if empty && onEmpty != nil {
		onEmpty()
	}
}

// ---- Attach / detach / switch --------------------------------------------

// DetachClients tells every client attached to name to detach.
func (s *Server) DetachClients(name string) error {
	sess := s.Get(name)
	if sess == nil {
		return fmt.Errorf("session not found: %s", name)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.clients) == 0 {
		return fmt.Errorf("session %s has no attached client", name)
	}
	for c := range sess.clients {
		sendCtrl(c, Control{Kind: "detached"})
	}
	return nil
}

// SwitchActive asks the most-recently-attached client to switch to session
// name. Used by the `to` command from another terminal.
func (s *Server) SwitchActive(name string) error {
	if s.Get(name) == nil {
		return fmt.Errorf("session not found: %s", name)
	}
	s.mu.Lock()
	c := s.last
	s.mu.Unlock()
	if c == nil {
		return fmt.Errorf("no attached client to switch")
	}
	sendCtrl(c, Control{Kind: "switch", Session: name})
	return nil
}

// Attach registers c with the session, sizes the pty and replays recent output.
func (sess *Session) Attach(c *Client, cols, rows uint16) {
	sess.Resize(cols, rows)
	sess.mu.Lock()
	sess.clients[c] = struct{}{}
	backlog := sess.ring.Bytes()
	sess.mu.Unlock()

	sess.srv.mu.Lock()
	sess.srv.last = c
	sess.srv.mu.Unlock()

	if len(backlog) > 0 {
		select {
		case c.Out <- backlog:
		case <-c.done:
		}
	}
}

// Detach unregisters c from the session.
func (sess *Session) Detach(c *Client) {
	sess.mu.Lock()
	delete(sess.clients, c)
	sess.mu.Unlock()
}

// WriteInput forwards client keystrokes to the pty.
func (sess *Session) WriteInput(p []byte) {
	sess.proc.Master.Write(p)
}

// Resize changes the pty window size.
func (sess *Session) Resize(cols, rows uint16) {
	pty.Setsize(sess.proc.Master, cols, rows)
}

// Attached reports how many clients are attached.
func (sess *Session) Attached() int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return len(sess.clients)
}

// pump reads pty output, feeds the ring buffer and broadcasts to clients until
// the process exits, then notifies clients and removes the session.
func (sess *Session) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := sess.proc.Master.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			sess.broadcast(chunk)
		}
		if err != nil {
			break
		}
	}

	code := waitCode(sess.proc.Wait())

	sess.mu.Lock()
	sess.exited = true
	sess.exitCode = code
	clients := make([]*Client, 0, len(sess.clients))
	for c := range sess.clients {
		clients = append(clients, c)
	}
	sess.mu.Unlock()

	for _, c := range clients {
		sendCtrl(c, Control{Kind: "exit", Code: code})
	}
	sess.srv.remove(sess.Name)
	sess.proc.Master.Close()
}

// broadcast stores a chunk in the ring and sends a copy to every live client.
func (sess *Session) broadcast(chunk []byte) {
	sess.mu.Lock()
	sess.ring.Write(chunk)
	clients := make([]*Client, 0, len(sess.clients))
	for c := range sess.clients {
		clients = append(clients, c)
	}
	sess.mu.Unlock()

	for _, c := range clients {
		select {
		case c.Out <- chunk:
		case <-c.done:
		}
	}
}

// sendCtrl delivers a control message without blocking on a dead client.
func sendCtrl(c *Client, m Control) {
	select {
	case c.Ctrl <- m:
	case <-c.done:
	}
}
