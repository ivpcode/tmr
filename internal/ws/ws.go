// Package ws is a minimal server-side WebSocket implementation (RFC 6455)
// built on the standard library only. It supports exactly what the ivt web
// terminal needs: the HTTP upgrade handshake, text/binary messages with
// continuation frames, ping/pong, and clean closes.
package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Message opcodes (RFC 6455 §5.2).
const (
	OpText   byte = 0x1
	OpBinary byte = 0x2
	opClose  byte = 0x8
	opPing   byte = 0x9
	opPong   byte = 0xA
)

// maxMessage bounds a reassembled message; terminal traffic is far smaller.
const maxMessage = 4 << 20 // 4 MiB

// wsGUID is the fixed handshake GUID from RFC 6455 §1.3.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ErrClosed is returned by ReadMessage when the peer sent a close frame.
var ErrClosed = errors.New("ws: connection closed by peer")

// Conn is an upgraded WebSocket connection. Reads must come from a single
// goroutine; writes are internally serialized and may come from several.
type Conn struct {
	c  net.Conn
	br *bufio.Reader

	wmu sync.Mutex // serializes writes (data, pongs, close)
}

// Upgrade performs the server side of the WebSocket handshake and hijacks the
// HTTP connection. On error it writes the HTTP error response itself.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.Method != http.MethodGet ||
		!headerHas(r.Header, "Connection", "upgrade") ||
		!headerHas(r.Header, "Upgrade", "websocket") ||
		r.Header.Get("Sec-WebSocket-Version") != "13" ||
		r.Header.Get("Sec-WebSocket-Key") == "" {
		http.Error(w, "not a websocket handshake", http.StatusBadRequest)
		return nil, fmt.Errorf("ws: not a websocket handshake")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return nil, fmt.Errorf("ws: response does not support hijacking")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	h := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{c: conn, br: rw.Reader}, nil
}

// headerHas reports whether a comma-separated header contains value
// (case-insensitive), as required for Connection: keep-alive, Upgrade.
func headerHas(h http.Header, key, value string) bool {
	for _, v := range h.Values(key) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return true
			}
		}
	}
	return false
}

// ReadMessage returns the next text or binary message, transparently handling
// continuation frames, answering pings and discarding pongs. It returns
// ErrClosed when the peer closes.
func (c *Conn) ReadMessage() (op byte, data []byte, err error) {
	var msgOp byte
	var msg []byte
	for {
		fin, frameOp, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch frameOp {
		case opPing:
			if err := c.write(opPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			c.write(opClose, nil) // echo the close, best effort
			return 0, nil, ErrClosed
		case OpText, OpBinary:
			if msg != nil {
				return 0, nil, fmt.Errorf("ws: new message before continuation finished")
			}
			msgOp = frameOp
			msg = payload
		case 0x0: // continuation
			if msg == nil {
				return 0, nil, fmt.Errorf("ws: continuation without a message")
			}
			msg = append(msg, payload...)
		default:
			return 0, nil, fmt.Errorf("ws: unsupported opcode %d", frameOp)
		}
		if len(msg) > maxMessage {
			return 0, nil, fmt.Errorf("ws: message too large")
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}

// readFrame reads one raw frame, unmasking client payloads (RFC 6455 §5.3).
func (c *Conn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.br, hdr[:]); err != nil {
		return
	}
	fin = hdr[0]&0x80 != 0
	if hdr[0]&0x70 != 0 { // RSV bits: no extensions negotiated
		return false, 0, nil, fmt.Errorf("ws: unexpected RSV bits")
	}
	op = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	n := uint64(hdr[1] & 0x7F)

	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > maxMessage {
		return false, 0, nil, fmt.Errorf("ws: frame too large (%d)", n)
	}

	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}

// WriteMessage sends one unfragmented text or binary message.
func (c *Conn) WriteMessage(op byte, data []byte) error {
	return c.write(op, data)
}

// write emits a single server frame (unmasked) with a single Write call.
func (c *Conn) write(op byte, data []byte) error {
	var hdr [10]byte
	hdr[0] = 0x80 | op // FIN + opcode
	n := len(data)
	hlen := 2
	switch {
	case n < 126:
		hdr[1] = byte(n)
	case n <= 0xFFFF:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hlen = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		hlen = 10
	}
	buf := make([]byte, hlen+n)
	copy(buf, hdr[:hlen])
	copy(buf[hlen:], data)

	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.c.Write(buf)
	return err
}

// Ping sends a ping frame (keepalive; browsers answer automatically).
func (c *Conn) Ping() error { return c.write(opPing, nil) }

// Close sends a close frame (best effort) and closes the connection.
func (c *Conn) Close() error {
	c.write(opClose, nil)
	return c.c.Close()
}
