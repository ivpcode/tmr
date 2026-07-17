package tmux

import (
	"bytes"
	"testing"
)

func TestRingUnderCapacity(t *testing.T) {
	r := newRing(16)
	r.Write([]byte("hello"))
	if got := r.Bytes(); !bytes.Equal(got, []byte("hello")) {
		t.Errorf("got %q, want hello", got)
	}
}

func TestRingExactCapacity(t *testing.T) {
	r := newRing(5)
	r.Write([]byte("hello"))
	if got := r.Bytes(); !bytes.Equal(got, []byte("hello")) {
		t.Errorf("got %q, want hello", got)
	}
}

func TestRingWrap(t *testing.T) {
	r := newRing(5)
	r.Write([]byte("abc"))
	r.Write([]byte("defg")) // total 7 into cap 5 -> keep last 5: "cdefg"
	if got := r.Bytes(); !bytes.Equal(got, []byte("cdefg")) {
		t.Errorf("got %q, want cdefg", got)
	}
}

func TestRingOversizedWrite(t *testing.T) {
	r := newRing(4)
	r.Write([]byte("abcdefgh")) // keep last 4: "efgh"
	if got := r.Bytes(); !bytes.Equal(got, []byte("efgh")) {
		t.Errorf("got %q, want efgh", got)
	}
}

func TestRingMultipleWraps(t *testing.T) {
	r := newRing(3)
	for _, s := range []string{"1", "2", "3", "4", "5"} {
		r.Write([]byte(s))
	}
	if got := r.Bytes(); !bytes.Equal(got, []byte("345")) {
		t.Errorf("got %q, want 345", got)
	}
}
