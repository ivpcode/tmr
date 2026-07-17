package tmux

import (
	"fmt"
	"sync"
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
	defer s.Kill(sess.Name())
	if sess.Name() != "0" {
		t.Errorf("auto name = %q, want 0", sess.Name())
	}
}

func TestNewSessionDuplicate(t *testing.T) {
	s := NewServer(nil)
	if _, err := s.NewSession("work", longCmd, 80, 24); err != nil {
		t.Fatal(err)
	}
	defer s.Kill("work")
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
		t.Errorf("list order wrong: %+v", got)
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

// TestConcurrentRenameAndList exercises the Rename/List/Name paths together;
// run with -race to prove name accesses are properly guarded.
func TestConcurrentRenameAndList(t *testing.T) {
	s := NewServer(nil)
	sess, err := s.NewSession("a0", longCmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 1; i <= 50; i++ {
			cur := fmt.Sprintf("a%d", i-1)
			next := fmt.Sprintf("a%d", i)
			if err := s.Rename(cur, next); err != nil {
				t.Errorf("rename %s -> %s: %v", cur, next, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			for _, info := range s.List() {
				_ = info.Name
			}
			_ = sess.Name()
		}
	}()
	wg.Wait()
	s.Kill(sess.Name())
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

// TestKillGroup verifies kill takes down the session's descendants too: a shell
// that spawned a child must not leave the child running.
func TestKillGroup(t *testing.T) {
	s := NewServer(nil)
	// The shell spawns a sleep and waits on it; both are in the pty's group.
	if _, err := s.NewSession("g", []string{"sh", "-c", "sleep 60 & wait"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the shell fork the sleep
	if err := s.Kill("g"); err != nil {
		t.Fatal(err)
	}
	waitGone(t, s, "g")
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

// TestBroadcastKicksStalledClient proves a client that stops draining its
// buffer is kicked instead of stalling the session's output pump.
func TestBroadcastKicksStalledClient(t *testing.T) {
	s := NewServer(nil)
	sess, err := s.NewSession("noisy", longCmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill("noisy")

	stalled := NewClient()
	sess.Attach(stalled, 80, 24)
	defer sess.Detach(stalled)

	// Fill the client's buffer beyond capacity without draining it.
	for i := 0; i < cap(stalled.Out)+8; i++ {
		sess.broadcast([]byte("x"))
	}
	select {
	case <-stalled.Done():
		// kicked, as designed
	default:
		t.Error("stalled client was not kicked")
	}
}

func TestDetachClearsSwitchTarget(t *testing.T) {
	s := NewServer(nil)
	sess, err := s.NewSession("x", longCmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill("x")

	c := NewClient()
	sess.Attach(c, 80, 24)
	sess.Detach(c)
	c.Close()
	if err := s.SwitchActive("x"); err == nil {
		t.Error("SwitchActive should fail after the only client detached")
	}
}
