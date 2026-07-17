package server

import (
	"bytes"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/ivpcode/tmr/internal/ipc"
)

// startServer runs the daemon on a fresh socket and returns the socket path and
// a channel that yields Run's result when the server exits.
func startServer(t *testing.T) (string, <-chan error) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s")
	done := make(chan error, 1)
	go func() { done <- Run(sock) }()
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return sock, done
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return "", nil
}

// cmd sends one command frame and returns the result frame.
func cmd(t *testing.T, sock string, argv ...string) *ipc.Frame {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindCmd, Argv: argv, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	resp, err := ipc.Read(conn)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestEndToEnd drives the daemon over a real unix socket: create a session
// running cat, attach, send stdin, see it echoed back, detach, rename, kill,
// and watch the server exit on its own.
func TestEndToEnd(t *testing.T) {
	sock, done := startServer(t)

	// Create a session running cat (echoes stdin via the pty).
	if r := cmd(t, sock, "new", "echoer", "cat"); r.Code != 0 {
		t.Fatalf("new failed: %+v", r)
	}
	if r := cmd(t, sock, "ls"); !bytes.Contains([]byte(r.Stdout), []byte("echoer")) {
		t.Fatalf("ls does not show echoer: %+v", r)
	}

	// Attach and exchange bytes.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindAttach, Session: "echoer", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindStdin, Data: []byte("ping\n")}); err != nil {
		t.Fatal(err)
	}
	got := []byte{}
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(got, []byte("ping")) {
		if time.Now().After(deadline) {
			t.Fatalf("no echo from session; got %q", got)
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		f, err := ipc.Read(conn)
		if err != nil {
			continue
		}
		if f.Kind == ipc.KindOutput {
			got = append(got, f.Data...)
		}
	}
	conn.SetReadDeadline(time.Time{})

	// Detach cleanly.
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindDetach}); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// The session must have survived the detach.
	if r := cmd(t, sock, "ls"); !bytes.Contains([]byte(r.Stdout), []byte("echoer")) {
		t.Fatalf("session died on detach: %+v", r)
	}

	// Rename, then kill; the server must exit once the last session is gone.
	if r := cmd(t, sock, "rename", "echoer", "cat0"); r.Code != 0 {
		t.Fatalf("rename failed: %+v", r)
	}
	if r := cmd(t, sock, "kill", "cat0"); r.Code != 0 {
		t.Fatalf("kill failed: %+v", r)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit after last session was killed")
	}
}

// TestAttachMissingSession verifies an attach to a nonexistent session gets a
// clean exit frame instead of a hang.
func TestAttachMissingSession(t *testing.T) {
	sock, done := startServer(t)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindAttach, Session: "ghost", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	f, err := ipc.Read(conn)
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != ipc.KindExit || f.Code == 0 || f.Stderr == "" {
		t.Errorf("want error exit frame, got %+v", f)
	}
	conn.Close()

	// Shut the server down for cleanup: create+kill one session.
	cmd(t, sock, "new", "tmp", "sleep", "60")
	cmd(t, sock, "kill", "tmp")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit")
	}
}
