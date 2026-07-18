package ipc

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func roundTrip(t *testing.T, f *Frame) *Frame {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, f); err != nil {
		t.Fatalf("Write(%+v): %v", f, err)
	}
	got, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read after Write(%+v): %v", f, err)
	}
	return got
}

func TestRoundTripAllKinds(t *testing.T) {
	frames := []*Frame{
		{Kind: KindCmd, Argv: []string{"new", "work", "claude"}, Cols: 120, Rows: 40},
		{Kind: KindAttach, Session: "work", Cols: 132, Rows: 43},
		{Kind: KindStdin, Data: []byte("ls -la\n")},
		{Kind: KindResize, Cols: 80, Rows: 24},
		{Kind: KindDetach},
		{Kind: KindResult, Stdout: "out\n", Stderr: "err\n", Code: 3},
		{Kind: KindOutput, Data: []byte("\x1b[31mhello\x1b[0m\r\n")},
		{Kind: KindDetached},
		{Kind: KindSwitch, Session: "other"},
		{Kind: KindExit, Code: -1, Stderr: "boom"},
		{Kind: KindExit, Code: 42},
	}
	for _, f := range frames {
		got := roundTrip(t, f)
		// Raw kinds decode empty Data/Session as their zero forms; normalize.
		if len(got.Data) == 0 {
			got.Data = f.Data
		}
		if !reflect.DeepEqual(got, f) {
			t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, f)
		}
	}
}

func TestBinaryDataIsRaw(t *testing.T) {
	// The whole point of the binary framing: output bytes must appear verbatim
	// on the wire (no base64), with exactly 5 bytes of header overhead.
	data := []byte("payload-\x00\xff-bytes")
	var buf bytes.Buffer
	if err := Write(&buf, &Frame{Kind: KindOutput, Data: data}); err != nil {
		t.Fatal(err)
	}
	wire := buf.Bytes()
	if len(wire) != 5+len(data) {
		t.Errorf("wire size = %d, want %d", len(wire), 5+len(data))
	}
	if !bytes.Equal(wire[5:], data) {
		t.Errorf("payload not raw on the wire: %q", wire[5:])
	}
}

func TestReadRejectsOversizedFrame(t *testing.T) {
	var hdr [5]byte
	hdr[0] = byte(KindOutput)
	binary.BigEndian.PutUint32(hdr[1:5], maxFrame+1)
	if _, err := Read(bytes.NewReader(hdr[:])); err == nil {
		t.Error("expected error for oversized frame length")
	}
}

func TestReadTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, &Frame{Kind: KindOutput, Data: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	trunc := buf.Bytes()[:buf.Len()-2]
	if _, err := Read(bytes.NewReader(trunc)); err == nil {
		t.Error("expected error for truncated payload")
	}
}

func TestUnknownKind(t *testing.T) {
	if err := Write(&bytes.Buffer{}, &Frame{Kind: 0}); err == nil {
		t.Error("expected error writing unknown kind")
	}
	wire := []byte{200, 0, 0, 0, 0}
	if _, err := Read(bytes.NewReader(wire)); err == nil {
		t.Error("expected error reading unknown kind")
	}
}

func TestMalformedFixedPayloads(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		n    int
	}{
		{KindResize, 3},
		{KindAttach, 2},
		{KindExit, 1},
	} {
		wire := make([]byte, 5+tc.n)
		wire[0] = byte(tc.kind)
		binary.BigEndian.PutUint32(wire[1:5], uint32(tc.n))
		if _, err := Read(bytes.NewReader(wire)); err == nil {
			t.Errorf("kind %d with %d-byte payload: expected error", tc.kind, tc.n)
		}
	}
}

// BenchmarkWriteOutput measures the hot path: one pty output chunk per frame.
func BenchmarkWriteOutput(b *testing.B) {
	data := bytes.Repeat([]byte("x"), 32<<10)
	f := &Frame{Kind: KindOutput, Data: data}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		if err := Write(&buf, f); err != nil {
			b.Fatal(err)
		}
		if _, err := Read(&buf); err != nil {
			b.Fatal(err)
		}
	}
}
