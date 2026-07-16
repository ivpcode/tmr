// Package ipc defines the client/server wire protocol and framing. It replaces
// tmux's imsg-based IPC with a dependency-free, length-prefixed JSON framing
// over a unix-domain socket.
//
// Frame layout:
//
//	[4 bytes big-endian length N][N bytes JSON payload]
package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// maxFrame bounds a single frame to guard against corrupt/hostile lengths.
const maxFrame = 16 << 20 // 16 MiB

// Request is a command invocation sent from client to server.
type Request struct {
	Argv []string `json:"argv"` // argv[0] is the command name/alias
	Term string   `json:"term"` // client's $TERM (for later use)
}

// Response is the server's reply to a Request.
type Response struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Code   int    `json:"code"` // 0 == success
}

// WriteFrame length-prefixes and writes v as a JSON frame.
func WriteFrame(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(payload) > maxFrame {
		return fmt.Errorf("frame too large: %d bytes", len(payload))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// ReadFrame reads one length-prefixed JSON frame into v.
func ReadFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return fmt.Errorf("frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}
