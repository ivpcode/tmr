package client

import (
	"bytes"
	"testing"
	"time"
)

func feedAll(t *testing.T, kf *keyFilter, chunks ...string) (out, actions []byte) {
	t.Helper()
	for _, c := range chunks {
		o, a := kf.Feed([]byte(c))
		out = append(out, o...)
		actions = append(actions, a...)
	}
	return
}

func TestKeysPassthrough(t *testing.T) {
	var kf keyFilter
	out, actions := feedAll(t, &kf, "ls -la\n")
	if string(out) != "ls -la\n" || len(actions) != 0 {
		t.Errorf("out=%q actions=%v", out, actions)
	}
}

func TestKeysPrefixDetach(t *testing.T) {
	var kf keyFilter
	out, actions := feedAll(t, &kf, "\x02d")
	if len(out) != 0 || string(actions) != "d" {
		t.Errorf("out=%q actions=%q", out, actions)
	}
}

func TestKeysPrefixAcrossChunks(t *testing.T) {
	// Ctrl-B and the command key arriving in separate reads must still work.
	var kf keyFilter
	out, actions := feedAll(t, &kf, "abc\x02", "d", "efg")
	if string(out) != "abcefg" || string(actions) != "d" {
		t.Errorf("out=%q actions=%q", out, actions)
	}
}

func TestKeysLiteralPrefix(t *testing.T) {
	var kf keyFilter
	out, actions := feedAll(t, &kf, "\x02\x02")
	if !bytes.Equal(out, []byte{0x02}) || len(actions) != 0 {
		t.Errorf("out=%v actions=%v", out, actions)
	}
}

func TestKeysNextPrevAliases(t *testing.T) {
	var kf keyFilter
	_, actions := feedAll(t, &kf, "\x02n\x02p\x02)\x02(")
	if string(actions) != "npnp" {
		t.Errorf("actions=%q, want npnp", actions)
	}
}

func TestKeysUnboundDropped(t *testing.T) {
	var kf keyFilter
	out, actions := feedAll(t, &kf, "\x02x\x02qhello")
	if string(out) != "hello" || len(actions) != 0 {
		t.Errorf("out=%q actions=%v", out, actions)
	}
}

func TestStatusLineWidthAndContent(t *testing.T) {
	bar := string(statusLine(80, 24, "work", time.Date(2026, 7, 18, 15, 4, 0, 0, time.UTC)))
	for _, want := range []string{"[work]", "15:04", "18/07/2026", "\x1b[24;1H", barStyle, saveCursor, restoreCursor} {
		if !bytes.Contains([]byte(bar), []byte(want)) {
			t.Errorf("status line misses %q in %q", want, bar)
		}
	}
}

func TestSetTitle(t *testing.T) {
	if got := string(setTitle("work")); got != "\x1b]0;work\x07" {
		t.Errorf("setTitle = %q", got)
	}
}

func TestStatusLineNarrowTerminal(t *testing.T) {
	// Must not panic nor overflow on tiny widths; the clock is dropped.
	bar := string(statusLine(10, 5, "longsessionname", time.Now()))
	if bytes.Contains([]byte(bar), []byte(":")) {
		t.Errorf("narrow bar should drop the clock: %q", bar)
	}
}
