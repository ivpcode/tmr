// Package tmux holds the core data model: the server and its tree of sessions,
// windows and panes. It has no dependency on the command or IPC layers so it
// can be exercised in isolation.
package tmux

import (
	"fmt"
	"sync"
	"time"
)

// TargetKind is the kind of object a target flag points at.
type TargetKind int

const (
	KindNone TargetKind = iota
	KindSession
	KindWindow
	KindPane
	KindClient
)

// Server owns all state. In tmux this is a single-threaded event loop guarded by
// nothing; here concurrent client goroutines touch it, so a mutex protects the
// tree. Callers should hold nothing else while calling exported methods.
type Server struct {
	mu sync.Mutex

	Sessions map[string]*Session
	order    []string // session names, insertion order for stable listing

	nextWindowID int
	nextPaneID   int

	Created time.Time
}

// Session is a named collection of windows.
type Session struct {
	Name     string
	Windows  []*Window
	CurWin   int // index into Windows of the active window
	Created  time.Time
	Attached int // number of attached clients
}

// Window is a named collection of panes within a session.
type Window struct {
	ID      int
	Name    string
	Index   int // display index within the session
	Panes   []*Pane
	CurPane int // index into Panes of the active pane
}

// Pane is a single terminal region. The PTY/grid/screen fields will be filled
// in when the rendering layer lands; for now it is an addressable leaf.
type Pane struct {
	ID     int
	Width  int
	Height int
	// TODO(rendering): pty *pty.PTY, grid *grid.Grid, screen *screen.Screen
}

// Target is a resolved (session, window, pane) triple. Any of the tail fields
// may be nil depending on the requested TargetKind.
type Target struct {
	Session *Session
	Window  *Window
	Pane    *Pane
}

// NewServer returns an empty server.
func NewServer() *Server {
	return &Server{
		Sessions: map[string]*Session{},
		Created:  time.Now(),
	}
}

// ---- Session/window/pane creation ----------------------------------------

// NewSession creates a session with one window and one pane. If name is empty a
// numeric name is generated (as tmux does).
func (s *Server) NewSession(name string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "" {
		name = s.uniqueSessionName()
	}
	if _, exists := s.Sessions[name]; exists {
		return nil, fmt.Errorf("duplicate session: %s", name)
	}

	sess := &Session{Name: name, Created: time.Now()}
	win := s.newWindowLocked("bash", 0)
	sess.Windows = append(sess.Windows, win)

	s.Sessions[name] = sess
	s.order = append(s.order, name)
	return sess, nil
}

func (s *Server) newWindowLocked(name string, index int) *Window {
	w := &Window{ID: s.nextWindowID, Name: name, Index: index}
	s.nextWindowID++
	w.Panes = append(w.Panes, &Pane{ID: s.nextPaneID, Width: 80, Height: 24})
	s.nextPaneID++
	return w
}

func (s *Server) uniqueSessionName() string {
	for i := 0; ; i++ {
		name := fmt.Sprintf("%d", i)
		if _, ok := s.Sessions[name]; !ok {
			return name
		}
	}
}

// KillSession removes a session by name.
func (s *Server) KillSession(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Sessions[name]; !ok {
		return fmt.Errorf("session not found: %s", name)
	}
	delete(s.Sessions, name)
	for i, n := range s.order {
		if n == name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// SessionList returns sessions in stable insertion order.
func (s *Server) SessionList() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.Sessions[n])
	}
	return out
}

// Empty reports whether the server has no sessions.
func (s *Server) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Sessions) == 0
}

// ---- Target resolution ----------------------------------------------------

// ResolveTarget maps a target string to a concrete object. This is the small
// replacement for tmux's 1300-line cmd-find.c. When given is false (no -t on
// the command line) it falls back to the current/most-recent object.
//
// Accepted forms:
//
//	session                 (session, or — for window/pane kinds — its current window/pane)
//	session:window          window within a session
//	session:window.pane     pane within a window
//	window / window.pane    within the current session (no colon)
func (s *Server) ResolveTarget(kind TargetKind, spec string, given bool) (*Target, error) {
	if kind == KindNone {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if !given || spec == "" {
		return s.currentTargetLocked(kind)
	}

	// Determine the session and the remaining "window[.pane]" part.
	var sess *Session
	var rest string
	if before, after, hasColon := cut(spec, ':'); hasColon {
		sess = s.Sessions[before]
		if sess == nil {
			return nil, fmt.Errorf("can't find session: %s", before)
		}
		rest = after
	} else if kind == KindSession {
		sess = s.Sessions[spec]
		if sess == nil {
			return nil, fmt.Errorf("can't find session: %s", spec)
		}
		return &Target{Session: sess}, nil
	} else if named, ok := s.Sessions[spec]; ok {
		// Bare name that matches a session: target its current window/pane.
		sess = named
	} else {
		// Bare "window[.pane]" resolved against the current session.
		sess = s.currentSessionLocked()
		if sess == nil {
			return nil, fmt.Errorf("no current session")
		}
		rest = spec
	}

	t := &Target{Session: sess}
	if kind == KindSession {
		return t, nil
	}

	// Resolve the window, defaulting to the current one when rest is empty.
	winName, paneStr := split(rest, '.')
	if rest == "" {
		t.Window = sess.current()
	} else {
		t.Window = sess.findWindow(winName)
		if t.Window == nil {
			return nil, fmt.Errorf("can't find window: %s", winName)
		}
	}
	if kind == KindWindow || t.Window == nil {
		return t, nil
	}

	// Resolve the pane, defaulting to the window's active pane.
	if paneStr == "" {
		if t.Window.CurPane < len(t.Window.Panes) {
			t.Pane = t.Window.Panes[t.Window.CurPane]
		}
	} else {
		t.Pane = t.Window.findPane(paneStr)
		if t.Pane == nil {
			return nil, fmt.Errorf("can't find pane: %s", paneStr)
		}
	}
	return t, nil
}

func (s *Server) currentTargetLocked(kind TargetKind) (*Target, error) {
	sess := s.currentSessionLocked()
	if sess == nil {
		return nil, fmt.Errorf("no current session")
	}
	t := &Target{Session: sess}
	if kind == KindSession {
		return t, nil
	}
	win := sess.current()
	t.Window = win
	if kind == KindWindow || win == nil {
		return t, nil
	}
	if win.CurPane < len(win.Panes) {
		t.Pane = win.Panes[win.CurPane]
	}
	return t, nil
}

func (s *Server) currentSessionLocked() *Session {
	if len(s.order) == 0 {
		return nil
	}
	// Most-recently created session; good enough until we track a real
	// "current session" per client.
	return s.Sessions[s.order[len(s.order)-1]]
}

func (sess *Session) current() *Window {
	if sess.CurWin < len(sess.Windows) {
		return sess.Windows[sess.CurWin]
	}
	return nil
}

func (sess *Session) findWindow(nameOrIndex string) *Window {
	for _, w := range sess.Windows {
		if w.Name == nameOrIndex || fmt.Sprintf("%d", w.Index) == nameOrIndex {
			return w
		}
	}
	return nil
}

func (w *Window) findPane(idStr string) *Pane {
	for _, p := range w.Panes {
		if fmt.Sprintf("%d", p.ID) == idStr {
			return p
		}
	}
	return nil
}

// split cuts s at the first occurrence of sep, returning (before, after). If
// sep is absent, after is "".
func split(s string, sep byte) (string, string) {
	before, after, _ := cut(s, sep)
	return before, after
}

// cut splits s at the first sep, reporting whether sep was found.
func cut(s string, sep byte) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
