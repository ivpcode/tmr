package tmux

import "testing"

func setup(t *testing.T) *Server {
	t.Helper()
	s := NewServer()
	if _, err := s.NewSession("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewSession("dev"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSessionAutoName(t *testing.T) {
	s := NewServer()
	sess, err := s.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Name != "0" {
		t.Errorf("auto name = %q, want 0", sess.Name)
	}
	if len(sess.Windows) != 1 || len(sess.Windows[0].Panes) != 1 {
		t.Errorf("new session should have 1 window with 1 pane")
	}
}

func TestNewSessionDuplicate(t *testing.T) {
	s := setup(t)
	if _, err := s.NewSession("work"); err == nil {
		t.Error("expected duplicate session error")
	}
}

func TestResolveSession(t *testing.T) {
	s := setup(t)
	tg, err := s.ResolveTarget(KindSession, "work", true)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Session.Name != "work" {
		t.Errorf("got session %q, want work", tg.Session.Name)
	}
}

func TestResolveMissingSession(t *testing.T) {
	s := setup(t)
	if _, err := s.ResolveTarget(KindSession, "nope", true); err == nil {
		t.Error("expected error resolving missing session")
	}
}

func TestResolveWindowFromSessionName(t *testing.T) {
	// -t work with a window kind should default to work's current window.
	s := setup(t)
	tg, err := s.ResolveTarget(KindWindow, "work", true)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Session.Name != "work" || tg.Window == nil {
		t.Errorf("window target = %+v, want work + current window", tg)
	}
}

func TestResolvePaneExplicit(t *testing.T) {
	s := setup(t)
	tg, err := s.ResolveTarget(KindPane, "work:0.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Pane == nil || tg.Pane.ID != tg.Window.Panes[0].ID {
		t.Errorf("pane target = %+v", tg)
	}
}

func TestResolveDefaultCurrent(t *testing.T) {
	// No target given -> current session (most recent).
	s := setup(t)
	tg, err := s.ResolveTarget(KindSession, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Session.Name != "dev" {
		t.Errorf("current session = %q, want dev", tg.Session.Name)
	}
}

func TestKillSession(t *testing.T) {
	s := setup(t)
	if err := s.KillSession("work"); err != nil {
		t.Fatal(err)
	}
	if len(s.SessionList()) != 1 {
		t.Errorf("expected 1 session after kill, got %d", len(s.SessionList()))
	}
	if err := s.KillSession("work"); err == nil {
		t.Error("expected error killing already-removed session")
	}
}
