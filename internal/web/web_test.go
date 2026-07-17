package web

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivpcode/tmr/internal/ipc"
	"github.com/ivpcode/tmr/internal/server"
)

const testToken = "tok-for-tests"

// startStack runs a real daemon on a temp socket plus the web gateway handler
// in an httptest server, and returns the gateway base URL.
func startStack(t *testing.T) (base, sock string, daemonDone <-chan error) {
	t.Helper()
	sock = filepath.Join(t.TempDir(), "s")
	done := make(chan error, 1)
	go func() { done <- server.Run(sock) }()
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	gw := &Server{cfg: Config{Sock: sock, Token: testToken}}
	ts := httptest.NewServer(gw.handler())
	t.Cleanup(ts.Close)
	return ts.URL, sock, done
}

// daemonCmd drives the daemon directly over the unix socket.
func daemonCmd(t *testing.T, sock string, argv ...string) *ipc.Frame {
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

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthRequired(t *testing.T) {
	base, sock, done := startStack(t)

	if resp := get(t, base+"/"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("no token: status %d, want 403", resp.StatusCode)
	}
	if resp := get(t, base+"/?t=wrong"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong token: status %d, want 403", resp.StatusCode)
	}
	resp := get(t, base+"/?t="+testToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("good token: status %d, want 200", resp.StatusCode)
	}
	// The token must set the auth cookie for later requests.
	cookieOK := false
	for _, c := range resp.Cookies() {
		if c.Name == tokenCookie && c.Value == testToken {
			cookieOK = true
		}
	}
	if !cookieOK {
		t.Error("valid ?t= did not set the auth cookie")
	}
	if resp := get(t, base+"/assets/xterm.js"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("assets without token: status %d, want 403", resp.StatusCode)
	}

	shutdown(t, sock, done)
}

func TestSessionsAPIAndPages(t *testing.T) {
	base, sock, done := startStack(t)

	if r := daemonCmd(t, sock, "new", "webby", "sleep", "60"); r.Code != 0 {
		t.Fatalf("new failed: %+v", r)
	}

	resp := get(t, base+"/api/sessions?t="+testToken)
	body, _ := io.ReadAll(resp.Body)
	var list []struct {
		Name     string   `json:"name"`
		Cmd      []string `json:"cmd"`
		Attached int      `json:"attached"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("sessions JSON: %v (%s)", err, body)
	}
	if len(list) != 1 || list[0].Name != "webby" || list[0].Cmd[0] != "sleep" {
		t.Errorf("sessions = %+v", list)
	}

	// Terminal page renders with the session name and references the assets.
	resp = get(t, base+"/s/webby?t="+testToken)
	page, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"webby", "/assets/xterm.js", "/assets/addon-fit.js"} {
		if !bytes.Contains(page, []byte(want)) {
			t.Errorf("terminal page misses %q", want)
		}
	}
	// Assets are served once the cookie/token is presented.
	resp = get(t, base+"/assets/xterm.js?t="+testToken)
	if js, _ := io.ReadAll(resp.Body); len(js) < 100_000 {
		t.Errorf("xterm.js asset looks truncated: %d bytes", len(js))
	}

	// Create + kill via the HTTP API.
	nr, err := http.Post(base+"/api/new?t="+testToken, "application/json",
		strings.NewReader(`{"name":"apisess","cmd":["sleep","60"]}`))
	if err != nil || nr.StatusCode != http.StatusOK {
		t.Fatalf("api/new: %v status=%d", err, nr.StatusCode)
	}
	kr, err := http.Post(base+"/api/kill?t="+testToken, "application/json",
		strings.NewReader(`{"name":"apisess"}`))
	if err != nil || kr.StatusCode != http.StatusNoContent {
		t.Fatalf("api/kill: %v status=%d", err, kr.StatusCode)
	}

	daemonCmd(t, sock, "kill", "webby")
	shutdown(t, sock, done)
}

// TestWebSocketBridge drives the full path: browser-side WS -> gateway ->
// daemon -> pty (cat) and back.
func TestWebSocketBridge(t *testing.T) {
	base, sock, done := startStack(t)

	if r := daemonCmd(t, sock, "new", "echoer", "cat"); r.Code != 0 {
		t.Fatalf("new failed: %+v", r)
	}

	// Raw WS client handshake against the gateway.
	addr := strings.TrimPrefix(base, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	req := "GET /ws/echoer?cols=100&rows=30&t=" + testToken + " HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Connection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + key + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("ws handshake status: %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}

	// Send stdin ("ping\n") as a masked binary frame.
	writeClientFrame(t, conn, 0x2, []byte("ping\n"))

	// Expect the echo back within binary output frames.
	got := []byte{}
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(got, []byte("ping")) {
		if time.Now().After(deadline) {
			t.Fatalf("no echo through the web bridge; got %q", got)
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		op, payload, err := readServerFrame(br)
		if err != nil {
			continue
		}
		if op == 0x2 {
			got = append(got, payload...)
		}
	}
	conn.SetReadDeadline(time.Time{})

	// Resize control message (text frame) must not break the stream.
	writeClientFrame(t, conn, 0x1, []byte(`{"cols":120,"rows":40}`))

	// Close the WS: the gateway must detach, and the session must survive.
	writeClientFrame(t, conn, 0x8, nil)
	conn.Close()
	time.Sleep(200 * time.Millisecond)
	if r := daemonCmd(t, sock, "ls"); !strings.Contains(r.Stdout, "echoer") {
		t.Errorf("session died when the web client disconnected: %+v", r)
	}

	daemonCmd(t, sock, "kill", "echoer")
	shutdown(t, sock, done)
}

func writeClientFrame(t *testing.T, conn net.Conn, op byte, payload []byte) {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteByte(0x80 | op)
	n := len(payload)
	switch {
	case n < 126:
		buf.WriteByte(0x80 | byte(n))
	case n <= 0xFFFF:
		buf.WriteByte(0x80 | 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		buf.Write(ext[:])
	}
	mask := [4]byte{1, 2, 3, 4}
	buf.Write(mask[:])
	for i, b := range payload {
		buf.WriteByte(b ^ mask[i%4])
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func readServerFrame(br *bufio.Reader) (op byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(br, hdr[:]); err != nil {
		return
	}
	op = hdr[0] & 0x0F
	n := uint64(hdr[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(br, payload)
	return
}

// tryCmd is daemonCmd without test failures: the daemon may be auto-exiting
// while cleanup runs, so every step here is best effort.
func tryCmd(sock string, argv ...string) (*ipc.Frame, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindCmd, Argv: argv, Cols: 80, Rows: 24}); err != nil {
		return nil, err
	}
	return ipc.Read(conn)
}

// shutdown kills any remaining session so the daemon exits, then waits for it.
// It tolerates every race with the daemon's auto-exit.
func shutdown(t *testing.T, sock string, done <-chan error) {
	t.Helper()
	if r, err := tryCmd(sock, "ls", "-j"); err == nil && r != nil {
		var list []struct {
			Name string `json:"name"`
		}
		json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &list)
		if len(list) == 0 {
			// Daemon idle with no sessions: create + kill one to trigger exit.
			tryCmd(sock, "new", "shutdown-helper", "sleep", "60")
			tryCmd(sock, "kill", "shutdown-helper")
		} else {
			for _, s := range list {
				tryCmd(sock, "kill", s.Name)
			}
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("daemon did not exit at test end")
	}
}
