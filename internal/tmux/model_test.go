package tmux

import (
	"testing"
	"time"
)

// longCmd is a harmless process that stays alive reading its pty.
var longCmd = []string{"sleep", "60"}

func waitGone(t *testing.T, s *Server, name string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if s.Get(name) == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %q was not removed", name)
}

func TestNewSessionAutoName(t *testing.T) {
	s := NewServer(nil)
	sess, err := s.NewSession("", longCmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill(sess.Name)
	if sess.Name != "0" {
		t.Errorf("auto name = %q, want 0", sess.Name)
	}
}

func TestNewSessionDuplicate(t *testing.T) {
	s := NewServer(nil)
	sess, err := s.NewSession("work", longCmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill(sess.Name)
	if _, err := s.NewSession("work", longCmd, 80, 24); err == nil {
		t.Error("expected duplicate session error")
	}
}

func TestListOrder(t *testing.T) {
	s := NewServer(nil)
	for _, n := range []string{"a", "b", "c"} {
		if _, err := s.NewSession(n, longCmd, 80, 24); err != nil {
			t.Fatal(err)
		}
		defer s.Kill(n)
	}
	got := s.List()
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Errorf("list order wrong: %v", names(got))
	}
}

func TestRename(t *testing.T) {
	s := NewServer(nil)
	if _, err := s.NewSession("old", longCmd, 80, 24); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("old", "new"); err != nil {
		t.Fatal(err)
	}
	defer s.Kill("new")
	if s.Get("old") != nil {
		t.Error("old name still present")
	}
	if s.Get("new") == nil {
		t.Error("new name missing")
	}
	if err := s.Rename("missing", "x"); err == nil {
		t.Error("expected error renaming missing session")
	}
}

func TestKillRemoves(t *testing.T) {
	s := NewServer(nil)
	if _, err := s.NewSession("work", longCmd, 80, 24); err != nil {
		t.Fatal(err)
	}
	if err := s.Kill("work"); err != nil {
		t.Fatal(err)
	}
	waitGone(t, s, "work")
	if err := s.Kill("work"); err == nil {
		t.Error("expected error killing removed session")
	}
}

func TestOnEmptyCalled(t *testing.T) {
	done := make(chan struct{}, 1)
	s := NewServer(func() { done <- struct{}{} })
	if _, err := s.NewSession("only", longCmd, 80, 24); err != nil {
		t.Fatal(err)
	}
	s.Kill("only")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("onEmpty was not called after last session died")
	}
}

func names(ss []*Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}
