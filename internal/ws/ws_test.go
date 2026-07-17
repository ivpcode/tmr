package ws

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- a tiny hand-rolled WebSocket *client* for exercising the server --------

type testClient struct {
	c  net.Conn
	br *bufio.Reader
}

// dialWS performs a client handshake against an httptest server URL.
func dialWS(t *testing.T, url string) *testClient {
	t.Helper()
	addr := strings.TrimPrefix(url, "http://")
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	req := "GET /ws HTTP/1.1\r\nHost: " + addr + "\r\n" +
		"Connection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + key + "\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("handshake failed: %q err=%v", status, err)
	}
	wantAccept := ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "sec-websocket-accept:") {
			wantAccept = strings.TrimSpace(line[len("sec-websocket-accept:"):])
		}
	}
	h := sha1.Sum([]byte(key + wsGUID))
	if wantAccept != base64.StdEncoding.EncodeToString(h[:]) {
		t.Fatalf("bad Sec-WebSocket-Accept: %q", wantAccept)
	}
	return &testClient{c: c, br: br}
}

// send writes one client frame (masked, per RFC 6455).
func (tc *testClient) send(fin bool, op byte, payload []byte) error {
	var buf bytes.Buffer
	b0 := op
	if fin {
		b0 |= 0x80
	}
	buf.WriteByte(b0)
	n := len(payload)
	switch {
	case n < 126:
		buf.WriteByte(0x80 | byte(n))
	case n <= 0xFFFF:
		buf.WriteByte(0x80 | 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		buf.Write(ext[:])
	default:
		buf.WriteByte(0x80 | 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		buf.Write(ext[:])
	}
	mask := [4]byte{0x11, 0x22, 0x33, 0x44}
	buf.Write(mask[:])
	masked := make([]byte, n)
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	buf.Write(masked)
	_, err := tc.c.Write(buf.Bytes())
	return err
}

// recv reads one server frame (unmasked).
func (tc *testClient) recv(t *testing.T) (op byte, payload []byte) {
	t.Helper()
	var hdr [2]byte
	if _, err := io.ReadFull(tc.br, hdr[:]); err != nil {
		t.Fatal(err)
	}
	op = hdr[0] & 0x0F
	n := uint64(hdr[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		io.ReadFull(tc.br, ext[:])
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		io.ReadFull(tc.br, ext[:])
		n = binary.BigEndian.Uint64(ext[:])
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(tc.br, payload); err != nil {
		t.Fatal(err)
	}
	return op, payload
}

// echoServer upgrades and echoes every message back.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			op, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(op, data); err != nil {
				return
			}
		}
	}))
}

func TestEchoTextAndBinary(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()
	tc := dialWS(t, srv.URL)
	defer tc.c.Close()

	tc.send(true, OpText, []byte("ciao"))
	if op, p := tc.recv(t); op != OpText || string(p) != "ciao" {
		t.Errorf("text echo: op=%d p=%q", op, p)
	}
	bin := []byte{0, 1, 2, 0xff, 0xfe}
	tc.send(true, OpBinary, bin)
	if op, p := tc.recv(t); op != OpBinary || !bytes.Equal(p, bin) {
		t.Errorf("binary echo: op=%d p=%v", op, p)
	}
}

func TestLargeMessage(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()
	tc := dialWS(t, srv.URL)
	defer tc.c.Close()

	big := bytes.Repeat([]byte("x"), 70000) // forces the 64-bit length path
	tc.send(true, OpBinary, big)
	if op, p := tc.recv(t); op != OpBinary || !bytes.Equal(p, big) {
		t.Errorf("large echo failed: op=%d len=%d", op, len(p))
	}
}

func TestFragmentedMessage(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()
	tc := dialWS(t, srv.URL)
	defer tc.c.Close()

	tc.send(false, OpText, []byte("foo"))
	tc.send(false, 0x0, []byte("bar"))
	tc.send(true, 0x0, []byte("baz"))
	if op, p := tc.recv(t); op != OpText || string(p) != "foobarbaz" {
		t.Errorf("fragmented echo: op=%d p=%q", op, p)
	}
}

func TestPingGetsPong(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()
	tc := dialWS(t, srv.URL)
	defer tc.c.Close()

	tc.send(true, opPing, []byte("hb"))
	if op, p := tc.recv(t); op != opPong || string(p) != "hb" {
		t.Errorf("ping answer: op=%d p=%q", op, p)
	}
}

func TestCloseHandshake(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()
	tc := dialWS(t, srv.URL)
	defer tc.c.Close()

	tc.send(true, opClose, nil)
	if op, _ := tc.recv(t); op != opClose {
		t.Errorf("close echo: op=%d", op)
	}
}

func TestRejectsPlainHTTP(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("plain GET status = %d, want 400", resp.StatusCode)
	}
}

func TestServerFrameSizes(t *testing.T) {
	// Exercise the three server-side length encodings via the echo server.
	srv := echoServer(t)
	defer srv.Close()
	tc := dialWS(t, srv.URL)
	defer tc.c.Close()

	for _, n := range []int{1, 125, 126, 65535, 65536} {
		payload := bytes.Repeat([]byte("y"), n)
		tc.send(true, OpBinary, payload)
		if _, p := tc.recv(t); len(p) != n {
			t.Fatalf("size %d: got %d bytes back", n, len(p))
		}
	}
}
