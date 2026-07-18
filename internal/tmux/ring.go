package tmux

// ring is a fixed-capacity byte buffer that keeps the most recent bytes
// written to it. It backs each session's replay-on-attach scrollback.
type ring struct {
	buf  []byte
	pos  int  // next write position
	full bool // whether the buffer has wrapped
}

func newRing(size int) *ring {
	return &ring{buf: make([]byte, size)}
}

// Write appends p, discarding the oldest bytes once capacity is exceeded.
func (r *ring) Write(p []byte) {
	size := len(r.buf)
	if size == 0 {
		return
	}
	// If p is larger than the whole ring, keep only its tail.
	if len(p) >= size {
		copy(r.buf, p[len(p)-size:])
		r.pos = 0
		r.full = true
		return
	}
	n := copy(r.buf[r.pos:], p)
	if n < len(p) {
		// wrapped around the end
		copy(r.buf, p[n:])
		r.full = true
	}
	r.pos = (r.pos + len(p)) % size
	if r.pos == 0 {
		r.full = true
	}
}

// Bytes returns the buffered contents oldest-first.
func (r *ring) Bytes() []byte {
	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}
	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}
