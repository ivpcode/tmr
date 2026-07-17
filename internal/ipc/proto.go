// Package ipc defines the client/server wire protocol and framing. It replaces
// tmux's imsg IPC with a dependency-free, length-prefixed JSON framing over a
// unix-domain socket.
//
// Frame layout on the wire:
//
//	[4 bytes big-endian length N][N bytes JSON-encoded Frame]
//
// A connection is either a one-shot command (a "cmd" frame answered by a
// "result" frame) or an interactive attach (an "attach" frame followed by a
// bidirectional stream of "stdin"/"resize"/"detach" and "output"/"exit"/… ).
package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const maxFrame = 16 << 20 // 16 MiB guard against corrupt/hostile lengths

// Frame kinds.
const (
	// client -> server
	KindCmd    = "cmd"    // one-shot command: Argv
	KindAttach = "attach" // begin interactive attach: Session, Cols, Rows
	KindStdin  = "stdin"  // keystrokes for the pty: Data
	KindResize = "resize" // terminal resized: Cols, Rows
	KindDetach = "detach" // client wants to detach

	// server -> client
	KindResult   = "result"   // reply to a cmd: Stdout, Stderr, Code
	KindOutput   = "output"   // pty output: Data
	KindDetached = "detached" // attach ended by a detach request
	KindSwitch   = "switch"   // re-attach to Session
	KindExit     = "exit"     // session process exited: Code
)

// Frame is the single message type carried in both directions. Only the fields
// relevant to a given Kind are populated.
type Frame struct {
	Kind string `json:"kind"`

	Argv    []string `json:"argv,omitempty"`
	Session string   `json:"session,omitempty"`
	Cols    uint16   `json:"cols,omitempty"`
	Rows    uint16   `json:"rows,omitempty"`
	Data    []byte   `json:"data,omitempty"`

	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Code   int    `json:"code,omitempty"`
}

// Write length-prefixes and writes f.
func Write(w io.Writer, f *Frame) error {
	payload, err := json.Marshal(f)
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

// Read reads one frame.
func Read(r io.Reader) (*Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	var f Frame
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, err
	}
	return &f, nil
}
