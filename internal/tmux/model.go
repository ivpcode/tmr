// Package tmux holds the core model: a server owning a flat set of named
// sessions. Each session runs one real process in a pseudo-terminal that stays
// alive while clients attach and detach. There are no windows or panes — the
// clone is deliberately scoped to session management for long-running agents.
package tmux

import (
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/ivpcode/tmr/internal/pty"
)

// ringSize is how much recent pty output is retained to replay on attach.
const ringSize = 256 << 10 // 256 KiB

// CtrlKind identifies an out-of-band control message.
type CtrlKind int

const (
	CtrlDetached CtrlKind = iota // the client must detach
	CtrlSwitch                   // the client must re-attach to Control.Session
	CtrlExit                     // the session process exited with Control.Code
)

// Control is an out-of-band message sent to an attached client.
type Control struct {
	Kind    CtrlKind
	Session string // target session for CtrlSwitch
	Code    int    // process exit code for CtrlExit
}

// Client is a server-side view of one attached client (a running `resume`).
type Client struct {
	Out  chan []byte  // pty output destined for this client
	Ctrl chan Control // out-of-band control messages
	done chan struct{}
	once sync.Once
}

// NewClient makes a client with reasonable buffering.
func NewClient() *Client {
	return &Client{
		Out:  make(chan []byte, 512),
		Ctrl: make(chan Control, 8),
		done: make(chan struct{}),
	}
}

// Close marks the client finished so broadcasts skip it. It is safe to call
// from multiple goroutines (the connection handler and a kicking broadcast).
func (c *Client) Close() { c.once.Do(func() { close(c.done) }) }

// Done is closed when the client is finished; senders select on it.
func (c *Client) Done() <-chan struct{} { return c.done }

// SessionInfo is an immutable snapshot of a session for listing, safe to read
// without further locking.
type SessionInfo struct {
	Name     string
	Cmd      []string
	Created  time.Time
	Attached int
}

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

// Session is one named process running in a pty. Cmd and Created are immutable
// after creation; name is guarded by the server mutex (see Name).
type Session struct {
	Cmd     []string
	Created time.Time

	name string // guarded by srv.mu (Rename mutates it)
	srv  *Server
	proc *pty.Process

	mu      sync.Mutex
	ring    *ring
	clients map[*Client]struct{}
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
	if !validName(name) {
		return nil, fmt.Errorf("invalid session name: %q", name)
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
		Cmd:     cmd,
		Created: time.Now(),
		name:    name,
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

// Name returns the session's current name.
func (sess *Session) Name() string {
	sess.srv.mu.Lock()
	defer sess.srv.mu.Unlock()
	return sess.name
}

// Kill terminates a session's process (and its whole process group, so shells
// take their children with them) and removes it.
func (s *Server) Kill(name string) error {
	s.mu.Lock()
	sess := s.sessions[name]
	s.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("session not found: %s", name)
	}
	// The child is a session leader (Setsid), so its pid is also its process
	// group: signal the group to kill descendants too.
	pid := sess.proc.Proc.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		sess.proc.Proc.Kill()
	}
	// pump() observes the exit and removes the session.
	return nil
}

// Rename changes a session's name.
func (s *Server) Rename(oldName, newName string) error {
	if !validName(newName) {
		return fmt.Errorf("invalid session name: %q", newName)
	}
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
	sess.name = newName
	for i, n := range s.order {
		if n == oldName {
			s.order[i] = newName
			break
		}
	}
	return nil
}

// List returns a snapshot of all sessions in creation order.
func (s *Server) List() []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionInfo, 0, len(s.order))
	for _, n := range s.order {
		sess := s.sessions[n]
		out = append(out, SessionInfo{
			Name:     n,
			Cmd:      sess.Cmd,
			Created:  sess.Created,
			Attached: sess.Attached(),
		})
	}
	return out
}

// Get returns a session by name, or nil.
func (s *Server) Get(name string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[name]
}

// validName rejects names that would break addressing: whitespace and control
// characters, '/' (they appear in web URLs) and ':' (reserved), plus a leading
// '-' that would parse as a flag.
func validName(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r == '/' || r == ':' {
			return false
		}
	}
	return true
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

// remove deletes the session from the server, reading its (possibly renamed)
// name under the server lock, and fires onEmpty if it was the last one.
func (s *Server) remove(sess *Session) {
	s.mu.Lock()
	name := sess.name
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
	clients := sess.clientList()
	if len(clients) == 0 {
		return fmt.Errorf("session %s has no attached client", name)
	}
	for _, c := range clients {
		sendCtrl(c, Control{Kind: CtrlDetached})
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
	select {
	case <-c.Done():
		return fmt.Errorf("no attached client to switch")
	default:
	}
	sendCtrl(c, Control{Kind: CtrlSwitch, Session: name})
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

// Detach unregisters c from the session and drops it as the `to` target.
func (sess *Session) Detach(c *Client) {
	sess.mu.Lock()
	delete(sess.clients, c)
	sess.mu.Unlock()

	sess.srv.mu.Lock()
	if sess.srv.last == c {
		sess.srv.last = nil
	}
	sess.srv.mu.Unlock()
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

func (sess *Session) clientList() []*Client {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	out := make([]*Client, 0, len(sess.clients))
	for c := range sess.clients {
		out = append(out, c)
	}
	return out
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
	for _, c := range sess.clientList() {
		sendCtrl(c, Control{Kind: CtrlExit, Code: code})
	}
	sess.srv.remove(sess)
	sess.proc.Master.Close()
}

// broadcast stores a chunk in the ring and sends a copy to every live client.
// A client whose buffer is full (~16 MiB of undelivered output) is stalled or
// dead: it gets kicked instead of stalling the whole session's output.
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
		default:
			c.Close()
		}
	}
}

// sendCtrl delivers a control message without ever blocking: a client that
// cannot take a control message is stalled and gets kicked.
func sendCtrl(c *Client, m Control) {
	select {
	case c.Ctrl <- m:
	case <-c.done:
	default:
		c.Close()
	}
}
