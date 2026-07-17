// Package ipc defines the client/server wire protocol and framing. It replaces
// tmux's imsg IPC with a dependency-free binary framing over a unix-domain
// socket.
//
// Frame layout on the wire:
//
//	[1 byte kind][4 bytes big-endian payload length N][N bytes payload]
//
// Hot-path frames (stdin/output) carry their bytes raw — no JSON, no base64 —
// and each frame is emitted with a single Write call. Structured frames
// (cmd/result) use a small JSON payload.
//
// A connection is either a one-shot command (a KindCmd frame answered by a
// KindResult frame) or an interactive attach (a KindAttach frame followed by a
// bidirectional stream).
package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const maxFrame = 16 << 20 // 16 MiB guard against corrupt/hostile lengths

// Kind identifies a frame type.
type Kind byte

const (
	// client -> server
	KindCmd    Kind = iota + 1 // one-shot command: Argv, Cols, Rows
	KindAttach                 // begin interactive attach: Session, Cols, Rows
	KindStdin                  // keystrokes for the pty: Data
	KindResize                 // terminal resized: Cols, Rows
	KindDetach                 // client wants to detach

	// server -> client
	KindResult   // reply to a cmd: Stdout, Stderr, Code
	KindOutput   // pty output: Data
	KindDetached // attach ended by a detach request
	KindSwitch   // re-attach to Session
	KindExit     // session process exited: Code, Stderr
)

// Frame is the single message type carried in both directions. Only the fields
// relevant to a given Kind are populated.
type Frame struct {
	Kind Kind

	Argv    []string
	Session string
	Cols    uint16
	Rows    uint16
	Data    []byte

	Stdout string
	Stderr string
	Code   int
}

// cmdBody and resultBody are the JSON payloads of the two structured kinds.
type cmdBody struct {
	Argv []string `json:"argv"`
	Cols uint16   `json:"cols"`
	Rows uint16   `json:"rows"`
}

type resultBody struct {
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Code   int    `json:"code,omitempty"`
}

// Write encodes and writes f as one frame with a single Write call.
func Write(w io.Writer, f *Frame) error {
	payload, err := encodePayload(f)
	if err != nil {
		return err
	}
	if len(payload) > maxFrame {
		return fmt.Errorf("frame too large: %d bytes", len(payload))
	}
	buf := make([]byte, 5+len(payload))
	buf[0] = byte(f.Kind)
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	_, err = w.Write(buf)
	return err
}

// Read reads and decodes one frame.
func Read(r io.Reader) (*Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:5])
	if n > maxFrame {
		return nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return decodePayload(Kind(hdr[0]), payload)
}

func encodePayload(f *Frame) ([]byte, error) {
	switch f.Kind {
	case KindStdin, KindOutput:
		return f.Data, nil
	case KindDetach, KindDetached:
		return nil, nil
	case KindResize:
		var p [4]byte
		binary.BigEndian.PutUint16(p[0:2], f.Cols)
		binary.BigEndian.PutUint16(p[2:4], f.Rows)
		return p[:], nil
	case KindAttach:
		p := make([]byte, 4+len(f.Session))
		binary.BigEndian.PutUint16(p[0:2], f.Cols)
		binary.BigEndian.PutUint16(p[2:4], f.Rows)
		copy(p[4:], f.Session)
		return p, nil
	case KindSwitch:
		return []byte(f.Session), nil
	case KindExit:
		p := make([]byte, 4+len(f.Stderr))
		binary.BigEndian.PutUint32(p[0:4], uint32(int32(f.Code)))
		copy(p[4:], f.Stderr)
		return p, nil
	case KindCmd:
		return json.Marshal(cmdBody{Argv: f.Argv, Cols: f.Cols, Rows: f.Rows})
	case KindResult:
		return json.Marshal(resultBody{Stdout: f.Stdout, Stderr: f.Stderr, Code: f.Code})
	default:
		return nil, fmt.Errorf("unknown frame kind: %d", f.Kind)
	}
}

func decodePayload(kind Kind, payload []byte) (*Frame, error) {
	f := &Frame{Kind: kind}
	switch kind {
	case KindStdin, KindOutput:
		f.Data = payload
	case KindDetach, KindDetached:
		// no payload
	case KindResize:
		if len(payload) != 4 {
			return nil, fmt.Errorf("resize frame: bad payload size %d", len(payload))
		}
		f.Cols = binary.BigEndian.Uint16(payload[0:2])
		f.Rows = binary.BigEndian.Uint16(payload[2:4])
	case KindAttach:
		if len(payload) < 4 {
			return nil, fmt.Errorf("attach frame: bad payload size %d", len(payload))
		}
		f.Cols = binary.BigEndian.Uint16(payload[0:2])
		f.Rows = binary.BigEndian.Uint16(payload[2:4])
		f.Session = string(payload[4:])
	case KindSwitch:
		f.Session = string(payload)
	case KindExit:
		if len(payload) < 4 {
			return nil, fmt.Errorf("exit frame: bad payload size %d", len(payload))
		}
		f.Code = int(int32(binary.BigEndian.Uint32(payload[0:4])))
		f.Stderr = string(payload[4:])
	case KindCmd:
		var b cmdBody
		if err := json.Unmarshal(payload, &b); err != nil {
			return nil, err
		}
		f.Argv, f.Cols, f.Rows = b.Argv, b.Cols, b.Rows
	case KindResult:
		var b resultBody
		if err := json.Unmarshal(payload, &b); err != nil {
			return nil, err
		}
		f.Stdout, f.Stderr, f.Code = b.Stdout, b.Stderr, b.Code
	default:
		return nil, fmt.Errorf("unknown frame kind: %d", kind)
	}
	return f, nil
}
